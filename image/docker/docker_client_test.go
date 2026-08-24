package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/internal/set"
	"go.podman.io/image/v5/internal/useragent"
	"go.podman.io/image/v5/types"
)

func TestDockerCertDir(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempRoot, "xdg"))

	nondefaultFullPath := filepath.Join(tempRoot, "nondefault", "full", "path")
	nondefaultPerHostDir := filepath.Join(tempRoot, "nondefault", "certs.d")
	const variableReference = "$HOME"
	rootPrefix := filepath.Join(tempRoot, "rootprefix")
	const registryHostPort = "thishostdefinitelydoesnotexist:5000"
	const registryHostPortVendorOverDocker = "thishostdefinitelydoesnotexist:5001"
	const registryHostPortDockerOnly = "thishostdefinitelydoesnotexist:5002"

	hostDirs := []string{
		"/etc/containers/certs.d",
		"/etc/docker/certs.d",
	}

	// Create RootForImplicitAbsolutePaths-prefixed locations.
	for _, d := range hostDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(rootPrefix, d, registryHostPort), 0o755))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(rootPrefix, "/etc/docker/certs.d", registryHostPortVendorOverDocker), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(rootPrefix, "/usr/share/containers/certs.d", registryHostPortVendorOverDocker), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(rootPrefix, "/etc/docker/certs.d", registryHostPortDockerOnly), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(nondefaultPerHostDir, registryHostPort), 0o755))

	for _, c := range []struct {
		sys      *types.SystemContext
		hostPort string
		expected string
	}{
		// Work with nil SystemContext.
		{nil, registryHostPort, ""},
		// Work with empty SystemContext.
		{&types.SystemContext{}, registryHostPort, ""},
		// Full path overridden
		{&types.SystemContext{DockerCertPath: nondefaultFullPath}, registryHostPort, nondefaultFullPath},
		// Per-host path overridden
		{
			&types.SystemContext{DockerPerHostCertDirPath: nondefaultPerHostDir},
			registryHostPort,
			filepath.Join(nondefaultPerHostDir, registryHostPort),
		},
		// Both overridden
		{
			&types.SystemContext{
				DockerCertPath:           nondefaultFullPath,
				DockerPerHostCertDirPath: nondefaultPerHostDir,
			},
			registryHostPort,
			nondefaultFullPath,
		},
		// Root overridden
		{
			&types.SystemContext{RootForImplicitAbsolutePaths: rootPrefix},
			registryHostPort,
			filepath.Join(rootPrefix, "/etc/containers/certs.d", registryHostPort),
		},
		{
			&types.SystemContext{RootForImplicitAbsolutePaths: rootPrefix},
			registryHostPortVendorOverDocker,
			filepath.Join(rootPrefix, "/usr/share/containers/certs.d", registryHostPortVendorOverDocker),
		},
		{
			&types.SystemContext{RootForImplicitAbsolutePaths: rootPrefix},
			registryHostPortDockerOnly,
			filepath.Join(rootPrefix, "/etc/docker/certs.d", registryHostPortDockerOnly),
		},
		// Root and path overrides present simultaneously,
		{
			&types.SystemContext{
				DockerCertPath:               nondefaultFullPath,
				RootForImplicitAbsolutePaths: rootPrefix,
			},
			registryHostPort,
			nondefaultFullPath,
		},
		{
			&types.SystemContext{
				DockerPerHostCertDirPath:     nondefaultPerHostDir,
				RootForImplicitAbsolutePaths: rootPrefix,
			},
			registryHostPort,
			filepath.Join(nondefaultPerHostDir, registryHostPort),
		},
		// … and everything at once
		{
			&types.SystemContext{
				DockerCertPath:               nondefaultFullPath,
				DockerPerHostCertDirPath:     nondefaultPerHostDir,
				RootForImplicitAbsolutePaths: rootPrefix,
			},
			registryHostPort,
			nondefaultFullPath,
		},
		// No environment expansion happens in the overridden paths
		{&types.SystemContext{DockerCertPath: variableReference}, registryHostPort, variableReference},
		{
			&types.SystemContext{DockerPerHostCertDirPath: variableReference},
			registryHostPort,
			filepath.Join(variableReference, registryHostPort),
		},
	} {
		path, err := dockerCertDir(c.sys, c.hostPort)
		require.Equal(t, nil, err)
		assert.Equal(t, c.expected, path)
	}
}

// testTokenHTTPResponse creates just enough of a *http.Response to work with newBearerTokenFromHTTPResponseBody.
func testTokenHTTPResponse(t *testing.T, body string) *http.Response {
	requestURL, err := url.Parse("https://example.com/token")
	require.NoError(t, err)
	return &http.Response{
		Body: io.NopCloser(bytes.NewReader([]byte(body))),
		Request: &http.Request{
			Method: "",
			URL:    requestURL,
		},
	}
}

func TestBearerTokenReadFromHTTPResponseBody(t *testing.T) {
	for _, c := range []struct {
		input    string
		expected *bearerToken // or nil if a failure is expected
	}{
		{ // Invalid JSON
			input:    "IAmNotJson",
			expected: nil,
		},
		{ // "token"
			input:    `{"token":"IAmAToken","expires_in":100,"issued_at":"2018-01-01T10:00:02+00:00"}`,
			expected: &bearerToken{token: "IAmAToken", expirationTime: time.Unix(1514800802+100, 0)},
		},
		{ // "access_token"
			input:    `{"access_token":"IAmAToken","expires_in":100,"issued_at":"2018-01-01T10:00:02+00:00"}`,
			expected: &bearerToken{token: "IAmAToken", expirationTime: time.Unix(1514800802+100, 0)},
		},
		{ // Small expiry
			input:    `{"token":"IAmAToken","expires_in":1,"issued_at":"2018-01-01T10:00:02+00:00"}`,
			expected: &bearerToken{token: "IAmAToken", expirationTime: time.Unix(1514800802+60, 0)},
		},
	} {
		token := &bearerToken{}
		err := token.readFromHTTPResponseBody(testTokenHTTPResponse(t, c.input))
		if c.expected == nil {
			assert.Error(t, err, c.input)
		} else {
			require.NoError(t, err, c.input)
			assert.Equal(t, c.expected.token, token.token, c.input)
			assert.True(t, c.expected.expirationTime.Equal(token.expirationTime),
				"expected [%s] to equal [%s], it did not", token.expirationTime, c.expected.expirationTime)
		}
	}
}

func TestBearerTokenReadFromHTTPResponseBodyIssuedAtZero(t *testing.T) {
	zeroTime := time.Time{}.Format(time.RFC3339)
	now := time.Now()
	tokenBlob := fmt.Sprintf(`{"token":"IAmAToken","expires_in":100,"issued_at":"%s"}`, zeroTime)
	token := &bearerToken{}
	err := token.readFromHTTPResponseBody(testTokenHTTPResponse(t, tokenBlob))
	require.NoError(t, err)
	expectedExpiration := now.Add(time.Duration(100) * time.Second)
	require.False(t, token.expirationTime.Before(expectedExpiration),
		"expected [%s] not to be before [%s]", token.expirationTime, expectedExpiration)
}

func TestUserAgent(t *testing.T) {
	const sentinelUA = "sentinel/1.0"

	var expectedUA string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("User-Agent")
		assert.Equal(t, expectedUA, got)
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()

	for _, tc := range []struct {
		sys      *types.SystemContext
		expected string
	}{
		// Can't both test nil and set DockerInsecureSkipTLSVerify :(
		// {nil, defaultUA},
		{&types.SystemContext{}, useragent.DefaultUserAgent},
		{&types.SystemContext{DockerRegistryUserAgent: sentinelUA}, sentinelUA},
	} {
		// For this test against localhost, we don't care.
		tc.sys.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue

		registry := strings.TrimPrefix(s.URL, "http://")

		expectedUA = tc.expected
		if err := CheckAuth(context.Background(), tc.sys, "", "", registry); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

var registrySuseComResp = http.Response{
	Status:     "401 Unauthorized",
	StatusCode: http.StatusUnauthorized,
	Proto:      "HTTP/1.1",
	ProtoMajor: 1,
	ProtoMinor: 1,
	Header: map[string][]string{
		"Content-Length":                  {"145"},
		"Content-Type":                    {"application/json"},
		"Date":                            {"Fri, 26 Aug 2022 08:03:13 GMT"},
		"Docker-Distribution-Api-Version": {"registry/2.0"},
		// "Www-Authenticate":                {`Bearer realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="registry:catalog:*",error="insufficient_scope"`},
		"X-Content-Type-Options": {"nosniff"},
	},
	Request: nil,
}

func TestNeedsRetryOnInsuficientScope(t *testing.T) {
	resp := registrySuseComResp
	resp.Header["Www-Authenticate"] = []string{
		`Bearer realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="registry:catalog:*",error="insufficient_scope"`,
	}
	expectedScope := authScope{
		resourceType: "registry",
		remoteName:   "catalog",
		actions:      "*",
	}

	needsRetry, scope := needsRetryWithUpdatedScope(&resp)

	if !needsRetry {
		t.Fatal("Expected needing to retry")
	}

	if expectedScope != *scope {
		t.Fatalf("Got an invalid scope, expected '%q' but got '%q'", expectedScope, *scope)
	}
}

func TestNeedsRetryNoRetryWhenNoAuthHeader(t *testing.T) {
	resp := registrySuseComResp
	delete(resp.Header, "Www-Authenticate")

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)

	if needsRetry {
		t.Fatal("Expected no need to retry, as no Authentication headers are present")
	}
}

func TestNeedsRetryNoRetryWhenNoBearerAuthHeader(t *testing.T) {
	resp := registrySuseComResp
	resp.Header["Www-Authenticate"] = []string{
		`OAuth2 realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="registry:catalog:*"`,
	}

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)

	if needsRetry {
		t.Fatal("Expected no need to retry, as no bearer authentication header is present")
	}
}

func TestNeedsRetryNoRetryWhenNoErrorInBearer(t *testing.T) {
	resp := registrySuseComResp
	resp.Header["Www-Authenticate"] = []string{
		`Bearer realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="registry:catalog:*"`,
	}

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)

	if needsRetry {
		t.Fatal("Expected no need to retry, as no insufficient error is present in the authentication header")
	}
}

func TestNeedsRetryNoRetryWhenInvalidErrorInBearer(t *testing.T) {
	resp := registrySuseComResp
	resp.Header["Www-Authenticate"] = []string{
		`Bearer realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="registry:catalog:*,error="random_error"`,
	}

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)

	if needsRetry {
		t.Fatal("Expected no need to retry, as no insufficient_error is present in the authentication header")
	}
}

func TestNeedsRetryNoRetryWhenInvalidScope(t *testing.T) {
	resp := registrySuseComResp
	resp.Header["Www-Authenticate"] = []string{
		`Bearer realm="https://registry.suse.com/auth",service="SUSE Linux Docker Registry",scope="foo:bar",error="insufficient_scope"`,
	}

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)

	if needsRetry {
		t.Fatal("Expected no need to retry, as no insufficient_error is present in the authentication header")
	}
}

func TestNeedsNoRetry(t *testing.T) {
	resp := http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: map[string][]string{
			"Apptime":                         {"D=49722"},
			"Content-Length":                  {"1683"},
			"Content-Type":                    {"application/json; charset=utf-8"},
			"Date":                            {"Fri, 26 Aug 2022 09:00:21 GMT"},
			"Docker-Distribution-Api-Version": {"registry/2.0"},
			"Link":                            {`</v2/_catalog?last=f35%2Fs2i-base&n=100>; rel="next"`},
			"Referrer-Policy":                 {"same-origin"},
			"Server":                          {"Apache"},
			"Strict-Transport-Security":       {"max-age=31536000; includeSubDomains; preload"},
			"Vary":                            {"Accept"},
			"X-Content-Type-Options":          {"nosniff"},
			"X-Fedora-Proxyserver":            {"proxy10.iad2.fedoraproject.org"},
			"X-Fedora-Requestid":              {"YwiLpHEhLsbSTugJblBF8QAAAEI"},
			"X-Frame-Options":                 {"SAMEORIGIN"},
			"X-Xss-Protection":                {"1; mode=block"},
		},
	}

	needsRetry, _ := needsRetryWithUpdatedScope(&resp)
	if needsRetry {
		t.Fatal("Got the need to retry, but none should be required")
	}
}

func TestParseRegistryWarningHeader(t *testing.T) {
	for _, c := range []struct{ header, expected string }{
		{"completely invalid", ""},
		{`299 - "trivial"`, "trivial"},
		{`100 - "not-299"`, ""},
		{`299 localhost "warn-agent set"`, ""},
		{`299 - "no-terminating-quote`, ""},
		{"299 - \"\x01 control\"", ""},
		{"299 - \"\\\x01 escaped control\"", ""},
		{"299 - \"e\\scaped\"", "escaped"},
		{"299 - \"non-UTF8 \xA1\xA2\"", "non-UTF8 \xA1\xA2"},
		{"299 - \"non-UTF8 escaped \\\xA1\\\xA2\"", "non-UTF8 escaped \xA1\xA2"},
		{"299 - \"UTF8 žluťoučký\"", "UTF8 žluťoučký"},
		{"299 - \"UTF8 \\\xC5\\\xBEluťoučký\"", "UTF8 žluťoučký"},
		{`299 - "unterminated`, ""},
		{`299 - "warning" "some-date"`, ""},
	} {
		res := parseRegistryWarningHeader(c.header)
		assert.Equal(t, c.expected, res, c.header)
	}
}

func TestGetBlobSize(t *testing.T) {
	for _, c := range []struct {
		headers  []string
		expected int64 // -1 if error expected
	}{
		{[]string{}, -1},
		{[]string{"0"}, 0},
		{[]string{"1"}, 1},
		{[]string{"0777"}, 777},  // Not interpreted as octal
		{[]string{"x"}, -1},      // Not a number: Go's response reader rejects such responses.
		{[]string{"1", "2"}, -1}, // Ambiguous: Go's response reader rejects such responses.
		{[]string{""}, -1},       // Empty header: Go's response reader rejects such responses.
		{[]string{"-1"}, -1},     // Negative: Go's response reader rejects such responses.
	} {
		var buf bytes.Buffer
		buf.WriteString("HTTP/1.1 200 OK\r\n")
		for _, v := range c.headers {
			buf.WriteString("Content-Length: ")
			buf.WriteString(v)
			buf.WriteString("\r\n")
		}
		buf.WriteString("\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(&buf), nil)
		if err != nil {
			assert.Equal(t, int64(-1), c.expected)
		} else {
			res, err := getBlobSize(resp)
			if c.expected == -1 {
				assert.Error(t, err, c.headers)
			} else {
				require.NoError(t, err, c.headers)
				assert.Equal(t, c.expected, res)
			}
		}
	}
}

func TestIsManifestUnknownError(t *testing.T) {
	// Mostly a smoke test; we can add more registries here if they need special handling.

	for _, c := range []struct{ name, response string }{
		{
			name: "docker.io when a tag in an _existing repo_ is not found",
			response: "HTTP/1.1 404 Not Found\r\n" +
				"Connection: close\r\n" +
				"Content-Length: 109\r\n" +
				"Content-Type: application/json\r\n" +
				"Date: Thu, 12 Aug 2021 20:51:32 GMT\r\n" +
				"Docker-Distribution-Api-Version: registry/2.0\r\n" +
				"Ratelimit-Limit: 100;w=21600\r\n" +
				"Ratelimit-Remaining: 100;w=21600\r\n" +
				"Strict-Transport-Security: max-age=31536000\r\n" +
				"\r\n" +
				"{\"errors\":[{\"code\":\"MANIFEST_UNKNOWN\",\"message\":\"manifest unknown\",\"detail\":{\"Tag\":\"this-does-not-exist\"}}]}\n",
		},
		{
			name: "registry.redhat.io/v2/this-does-not-exist/manifests/latest",
			response: "HTTP/1.1 404 Not Found\r\n" +
				"Connection: close\r\n" +
				"Content-Length: 53\r\n" +
				"Cache-Control: max-age=0, no-cache, no-store\r\n" +
				"Content-Type: application/json\r\n" +
				"Date: Thu, 13 Oct 2022 18:15:15 GMT\r\n" +
				"Expires: Thu, 13 Oct 2022 18:15:15 GMT\r\n" +
				"Pragma: no-cache\r\n" +
				"Server: Apache\r\n" +
				"Strict-Transport-Security: max-age=63072000; includeSubdomains; preload\r\n" +
				"X-Hostname: crane-tbr06.cran-001.prod.iad2.dc.redhat.com\r\n" +
				"\r\n" +
				"{\"errors\": [{\"code\": \"404\", \"message\": \"Not Found\"}]}\r\n",
		},
		{
			name: "registry.redhat.io/v2/rhosp15-rhel8/openstack-cron/manifests/sha256-8df5e60c42668706ac108b59c559b9187fa2de7e4e262e2967e3e9da35d5a8d7.sig",
			response: "HTTP/1.1 404 Not Found\r\n" +
				"Connection: close\r\n" +
				"Content-Length: 10\r\n" +
				"Accept-Ranges: bytes\r\n" +
				"Date: Thu, 13 Oct 2022 18:13:53 GMT\r\n" +
				"Server: AkamaiNetStorage\r\n" +
				"X-Docker-Size: -1\r\n" +
				"\r\n" +
				"Not found\r\n",
		},
		{
			name: "Harbor v2.10.2",
			response: "HTTP/1.1 404 Not Found\r\n" +
				"Content-Length: 153\r\n" +
				"Connection: keep-alive\r\n" +
				"Content-Type: application/json; charset=utf-8\r\n" +
				"Date: Wed, 08 May 2024 08:14:59 GMT\r\n" +
				"Server: nginx\r\n" +
				"Set-Cookie: sid=f617c257877837614ada2561513d6827; Path=/; HttpOnly\r\n" +
				"X-Request-Id: 1b151fb1-c943-4190-a9ce-5156ed5e3200\r\n" +
				"\r\n" +
				"{\"errors\":[{\"code\":\"NOT_FOUND\",\"message\":\"artifact test/alpine:sha256-443205b0cfcc78444321d56a2fe273f06e27b2c72b5058f8d7e975997d45b015.sig not found\"}]}\n",
		},
	} {
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader([]byte(c.response))), nil)
		require.NoError(t, err, c.name)
		defer resp.Body.Close()
		err = fmt.Errorf("wrapped: %w", registryHTTPResponseToError(resp))

		res := isManifestUnknownError(err)
		assert.True(t, res, "%s: %#v", c.name, err)
	}
}

func TestResolveRequestURLWithNamespaceProxy(t *testing.T) {
	for _, c := range []struct {
		name           string
		registry       string
		scheme         string
		path           string
		namespaceProxy string
		expected       string
	}{
		{
			name:           "no namespace proxy",
			registry:       "registry.example.com",
			scheme:         "https",
			path:           "/v2/library/nginx/manifests/latest",
			namespaceProxy: "",
			expected:       "https://registry.example.com/v2/library/nginx/manifests/latest",
		},
		{
			name:           "with namespace proxy for docker.io",
			registry:       "proxy.example.com",
			scheme:         "https",
			path:           "/v2/library/nginx/manifests/latest",
			namespaceProxy: "docker.io",
			expected:       "https://proxy.example.com/v2/library/nginx/manifests/latest?ns=docker.io",
		},
		{
			name:           "with namespace proxy for quay.io",
			registry:       "proxy.example.com",
			scheme:         "https",
			path:           "/v2/coreos/etcd/blobs/sha256:abc123",
			namespaceProxy: "quay.io",
			expected:       "https://proxy.example.com/v2/coreos/etcd/blobs/sha256:abc123?ns=quay.io",
		},
		{
			name:           "with namespace proxy and port",
			registry:       "proxy.example.com:5000",
			scheme:         "http",
			path:           "/v2/myimage/manifests/v1.0",
			namespaceProxy: "gcr.io",
			expected:       "http://proxy.example.com:5000/v2/myimage/manifests/v1.0?ns=gcr.io",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := &dockerClient{
				registry:       c.registry,
				scheme:         c.scheme,
				namespaceProxy: c.namespaceProxy,
			}
			result, err := client.resolveRequestURL(c.path)
			require.NoError(t, err)
			assert.Equal(t, c.expected, result.String())
		})
	}
}

func TestGetReferrers(t *testing.T) {
	const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDigestParsed := digest.Digest(testDigest)
	fallbackTag := strings.Replace(testDigest, ":", "-", 1)

	referrersIndex := imgspecv1.Index{
		MediaType: imgspecv1.MediaTypeImageIndex,
		Manifests: []imgspecv1.Descriptor{
			{
				MediaType:    imgspecv1.MediaTypeImageManifest,
				Digest:       digest.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
				Size:         100,
				ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json",
			},
		},
	}
	referrersBody, err := json.Marshal(referrersIndex)
	require.NoError(t, err)

	emptyIndex := imgspecv1.Index{
		MediaType: imgspecv1.MediaTypeImageIndex,
		Manifests: []imgspecv1.Descriptor{},
	}
	emptyBody, err := json.Marshal(emptyIndex)
	require.NoError(t, err)

	for _, tc := range []struct {
		name           string
		handler        http.HandlerFunc
		expectNil      bool
		expectErr      bool
		expectManifest int
	}{
		{
			name: "Referrers API returns index with entries",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(referrersBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: 1,
		},
		{
			name: "Referrers API returns empty index",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(emptyBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectNil: true,
		},
		{
			name: "Referrers API returns 404, fallback tag exists",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/"+fallbackTag) {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(referrersBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: 1,
		},
		{
			name: "Referrers API returns 405, fallback tag exists",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/"+fallbackTag) {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(referrersBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: 1,
		},
		{
			name: "Referrers API returns 501, fallback tag exists",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.WriteHeader(http.StatusNotImplemented)
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/"+fallbackTag) {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(referrersBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: 1,
		},
		{
			name: "Referrers API returns 404, fallback tag also missing",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))
			}),
			expectNil: true,
		},
		{
			name: "Referrers API returns server error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			}),
			expectErr: true,
		},
		{
			name: "Referrers API returns malformed JSON",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{not json`))
			}),
			expectErr: true,
		},
		{
			name: "Referrers API returns wrong Content-Type",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html>not an index</html>`))
			}),
			expectNil: true,
		},
		{
			name: "Referrers API returns Content-Type with parameters",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex+"; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(referrersBody)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: 1,
		},
		{
			name: "Fallback tag has non-index MIME type",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if strings.Contains(r.URL.Path, "/manifests/"+fallbackTag) {
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectNil: true,
		},
		{
			name: "Referrers API pagination capped at maxReferrersPages",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				if strings.Contains(r.URL.Path, "/referrers/") {
					pageStr := r.URL.Query().Get("page")
					page := 1
					if pageStr != "" {
						page, _ = strconv.Atoi(pageStr)
					}
					desc := imgspecv1.Descriptor{
						MediaType:    imgspecv1.MediaTypeImageManifest,
						Digest:       digest.Digest(fmt.Sprintf("sha256:%064x", page)),
						Size:         100,
						ArtifactType: "application/vnd.dev.cosign.simplesigning.v1+json",
					}
					body, _ := json.Marshal(imgspecv1.Index{
						MediaType: imgspecv1.MediaTypeImageIndex,
						Manifests: []imgspecv1.Descriptor{desc},
					})
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.Header().Set("Link", fmt.Sprintf(`</v2/test/repo/referrers/%s?page=%d>; rel="next"`, testDigest, page+1))
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(body)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}),
			expectManifest: maxReferrersPages,
		},
		{
			name: "Referrers API with pagination via Link header",
			handler: func() http.HandlerFunc {
				page1Desc := imgspecv1.Descriptor{
					MediaType:    imgspecv1.MediaTypeImageManifest,
					Digest:       digest.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111"),
					Size:         100,
					ArtifactType: "application/vnd.dev.cosign.simplesigning.v1+json",
				}
				page2Desc := imgspecv1.Descriptor{
					MediaType:    imgspecv1.MediaTypeImageManifest,
					Digest:       digest.Digest("sha256:2222222222222222222222222222222222222222222222222222222222222222"),
					Size:         200,
					ArtifactType: "application/vnd.dev.cosign.simplesigning.v1+json",
				}
				page1Body, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{page1Desc},
				})
				page2Body, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{page2Desc},
				})
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v2/" {
						w.WriteHeader(http.StatusOK)
						return
					}
					if strings.Contains(r.URL.Path, "/referrers/") {
						if r.URL.Query().Get("page") == "2" {
							w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write(page2Body)
							return
						}
						w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
						w.Header().Set("Link", fmt.Sprintf(`</v2/test/repo/referrers/%s?page=2>; rel="next"`, testDigest))
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write(page1Body)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}
			}(),
			expectManifest: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(tc.handler)
			defer s.Close()

			registry := strings.TrimPrefix(s.URL, "http://")
			named, err := reference.ParseNormalizedNamed(registry + "/test/repo:latest")
			require.NoError(t, err)
			ref, err := newReference(named, false)
			require.NoError(t, err)

			client := &dockerClient{
				sys:              &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
				registry:         registry,
				scheme:           "http",
				client:           s.Client(),
				tokenCache:       map[string]*bearerToken{},
				reportedWarnings: set.New[string](),
			}
			client.detectPropertiesOnce.Do(func() {})

			index, err := client.getReferrers(context.Background(), ref, testDigestParsed)
			if tc.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.expectNil {
				assert.Nil(t, index)
				return
			}
			require.NotNil(t, index)
			assert.Len(t, index.Manifests, tc.expectManifest)
		})
	}

	t.Run("Invalid digest", func(t *testing.T) {
		named, err := reference.ParseNormalizedNamed("example.com/test/repo:latest")
		require.NoError(t, err)
		ref, err := newReference(named, false)
		require.NoError(t, err)

		client := &dockerClient{}
		_, err = client.getReferrers(context.Background(), ref, digest.Digest("invalid"))
		assert.Error(t, err)
	})
}

func TestNextLinkURL(t *testing.T) {
	for _, c := range []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
		{
			name:     "standard next link",
			header:   `</v2/repo/referrers/sha256:abc?page=2>; rel="next"`,
			expected: "/v2/repo/referrers/sha256:abc?page=2",
		},
		{
			name:     "next link without quotes on rel",
			header:   `</v2/repo/referrers/sha256:abc?page=2>; rel=next`,
			expected: "/v2/repo/referrers/sha256:abc?page=2",
		},
		{
			name:     "multiple links with next",
			header:   `</v2/prev>; rel="prev", </v2/next?n=5>; rel="next"`,
			expected: "/v2/next?n=5",
		},
		{
			name:     "no next rel",
			header:   `</v2/prev>; rel="prev"`,
			expected: "",
		},
		{
			name:     "next substring in other parameter does not match",
			header:   `</v2/path>; title="the next one"; rel="prev"`,
			expected: "",
		},
		{
			name:     "case-insensitive rel matching",
			header:   `</v2/repo/referrers/sha256:abc?page=2>; rel="Next"`,
			expected: "/v2/repo/referrers/sha256:abc?page=2",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, nextLinkURL(c.header))
		})
	}
}
