package docker

import (
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
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/docker/distribution/registry/api/errcode"
	v2 "github.com/docker/distribution/registry/api/v2"
	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/internal/set"
	"go.podman.io/image/v5/internal/signature"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"
)

var _ private.ImageSource = (*dockerImageSource)(nil)

func TestDockerImageSourceReference(t *testing.T) {
	manifestPathRegex := regexp.MustCompile("^/v2/.*/manifests/latest$")

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/":
			rw.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && manifestPathRegex.MatchString(r.URL.Path):
			rw.WriteHeader(http.StatusOK)
			// Empty body is good enough for this test
		default:
			require.FailNowf(t, "Unexpected request", "%v %v", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	registryURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	registry := registryURL.Host

	mirrorConfiguration := strings.ReplaceAll(
		`[[registry]]
prefix = "primary-override.example.com"
location = "@REGISTRY@/primary-override"

[[registry]]
location = "with-mirror.example.com"

[[registry.mirror]]
location = "@REGISTRY@/with-mirror"
`, "@REGISTRY@", registry)
	registriesConf := filepath.Join(t.TempDir(), "docker-image-src")
	err = os.WriteFile(registriesConf, []byte(mirrorConfiguration), 0o600)
	require.NoError(t, err)

	for _, c := range []struct{ input, physical string }{
		{registry + "/no-redirection/busybox:latest", registry + "/no-redirection/busybox:latest"},
		{"primary-override.example.com/busybox:latest", registry + "/primary-override/busybox:latest"},
		{"with-mirror.example.com/busybox:latest", registry + "/with-mirror/busybox:latest"},
	} {
		ref, err := ParseReference("//" + c.input)
		require.NoError(t, err, c.input)
		src, err := ref.NewImageSource(context.Background(), &types.SystemContext{
			RegistriesDirPath:           "/this/does/not/exist",
			DockerPerHostCertDirPath:    "/this/does/not/exist",
			SystemRegistriesConfPath:    registriesConf,
			DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
		})
		require.NoError(t, err, c.input)
		defer src.Close()

		// The observable behavior
		assert.Equal(t, "//"+c.input, src.Reference().StringWithinTransport(), c.input)
		assert.Equal(t, ref.StringWithinTransport(), src.Reference().StringWithinTransport(), c.input)
		// Also peek into internal state
		src2, ok := src.(*dockerImageSource)
		require.True(t, ok, c.input)
		assert.Equal(t, "//"+c.input, src2.logicalRef.StringWithinTransport(), c.input)
		assert.Equal(t, "//"+c.physical, src2.physicalRef.StringWithinTransport(), c.input)
	}
}

// testTimeoutError is a net.Error with configurable Timeout() for testing.
type testTimeoutError struct{ timeout bool }

func (e *testTimeoutError) Error() string   { return "test timeout error" }
func (e *testTimeoutError) Timeout() bool   { return e.timeout }
func (e *testTimeoutError) Temporary() bool { return false }

func TestIsMirrorTransientError(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		expected bool
	}{
		// UnexpectedHTTPStatusError: handleErrorResponse returns this for status codes outside 400–499.
		// getBlob wraps it with "fetching blob: ".
		{
			name:     "HTTP 500 from handleErrorResponse",
			err:      fmt.Errorf("fetching blob: %w", UnexpectedHTTPStatusError{StatusCode: 500, status: "500 Internal Server Error"}),
			expected: true,
		},
		{
			name:     "HTTP 503 from handleErrorResponse",
			err:      fmt.Errorf("fetching blob: %w", UnexpectedHTTPStatusError{StatusCode: 503, status: "503 Service Unavailable"}),
			expected: true,
		},
		{
			name:     "HTTP 404 is not transient",
			err:      UnexpectedHTTPStatusError{StatusCode: 404, status: "404 Not Found"},
			expected: false,
		},
		{
			name:     "HTTP 400 is not transient",
			err:      UnexpectedHTTPStatusError{StatusCode: 400, status: "400 Bad Request"},
			expected: false,
		},
		// Network timeout: makeRequest returns net.Error with Timeout() == true.
		{
			name:     "network timeout from makeRequest",
			err:      &testTimeoutError{timeout: true},
			expected: true,
		},
		{
			name:     "network error without timeout",
			err:      &testTimeoutError{timeout: false},
			expected: false,
		},
		{
			name:     "wrapped network timeout",
			err:      fmt.Errorf("making request: %w", &testTimeoutError{timeout: true}),
			expected: true,
		},
		{
			name:     "plain error",
			err:      fmt.Errorf("something unrelated"),
			expected: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, isMirrorTransientError(c.err))
		})
	}
}

func TestIsMirrorFallbackError(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		expected bool
	}{
		// BLOB_UNKNOWN: registryHTTPResponseToError → handleErrorResponse → parseHTTPErrorResponse
		// returns errcode.Error{Code: v2.ErrorCodeBlobUnknown}. getBlob wraps with "fetching blob: ".
		{
			name:     "BLOB_UNKNOWN from registry",
			err:      fmt.Errorf("fetching blob: %w", errcode.Error{Code: v2.ErrorCodeBlobUnknown, Message: "blob unknown to registry"}),
			expected: true,
		},
		// TOO_MANY_REQUESTS: parseHTTPErrorResponse returns this for HTTP 429,
		// or the detailsErr path in handleErrorResponse produces it.
		{
			name:     "TOO_MANY_REQUESTS from registry",
			err:      fmt.Errorf("fetching blob: %w", errcode.ErrorCodeTooManyRequests.WithMessage("rate limit exceeded")),
			expected: true,
		},
		// MANIFEST_UNKNOWN should not trigger fallback — it means the image doesn't exist,
		// not just the blob.
		{
			name:     "MANIFEST_UNKNOWN is not fallback",
			err:      errcode.Error{Code: v2.ErrorCodeManifestUnknown, Message: "manifest unknown"},
			expected: false,
		},
		// UnexpectedHTTPStatusError is not matched — for regular blobs, handleErrorResponse
		// never produces it for 4xx.
		{
			name:     "UnexpectedHTTPStatusError 404 is not fallback",
			err:      UnexpectedHTTPStatusError{StatusCode: 404, status: "404 Not Found"},
			expected: false,
		},
		{
			name:     "plain error",
			err:      fmt.Errorf("something unrelated"),
			expected: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, isMirrorFallbackError(c.err))
		})
	}
}

func TestSimplifyContentType(t *testing.T) {
	for _, c := range []struct{ input, expected string }{
		{"", ""},
		{"application/json", "application/json"},
		{"application/json;charset=utf-8", "application/json"},
		{"application/json; charset=utf-8", "application/json"},
		{"application/json ; charset=utf-8", "application/json"},
		{"application/json\t;\tcharset=utf-8", "application/json"},
		{"application/json    ;charset=utf-8", "application/json"},
		{`application/json; charset="utf-8"`, "application/json"},
		{"completely invalid", ""},
	} {
		out := simplifyContentType(c.input)
		assert.Equal(t, c.expected, out, c.input)
	}
}

func readNextStream(streams chan io.ReadCloser, errs chan error) ([]byte, error) {
	select {
	case r := <-streams:
		if r == nil {
			return nil, nil
		}
		defer r.Close()
		return io.ReadAll(r)
	case err := <-errs:
		return nil, err
	}
}

type verifyGetBlobAtData struct {
	expectedData  []byte
	expectedError error
}

func verifyGetBlobAtOutput(t *testing.T, streams chan io.ReadCloser, errs chan error, expected []verifyGetBlobAtData) {
	for _, c := range expected {
		data, err := readNextStream(streams, errs)
		assert.Equal(t, c.expectedData, data)
		assert.Equal(t, c.expectedError, err)
	}
}

func TestSplitHTTP200ResponseToPartial(t *testing.T) {
	body := io.NopCloser(bytes.NewReader([]byte("123456789")))
	defer body.Close()
	streams := make(chan io.ReadCloser)
	errs := make(chan error)
	chunks := []private.ImageSourceChunk{
		{Offset: 1, Length: 2},
		{Offset: 4, Length: 1},
	}
	go splitHTTP200ResponseToPartial(streams, errs, body, chunks)

	expected := []verifyGetBlobAtData{
		{[]byte("23"), nil},
		{[]byte("5"), nil},
		{[]byte(nil), nil},
	}

	verifyGetBlobAtOutput(t, streams, errs, expected)
}

func TestHandle206Response(t *testing.T) {
	body := io.NopCloser(bytes.NewReader([]byte("--AAA\r\n\r\n23\r\n--AAA\r\n\r\n5\r\n--AAA--")))
	defer body.Close()
	streams := make(chan io.ReadCloser)
	errs := make(chan error)
	chunks := []private.ImageSourceChunk{
		{Offset: 1, Length: 2},
		{Offset: 4, Length: 1},
	}
	mediaType := "multipart/form-data"
	params := map[string]string{
		"boundary": "AAA",
	}
	go handle206Response(streams, errs, body, chunks, mediaType, params)

	expected := []verifyGetBlobAtData{
		{[]byte("23"), nil},
		{[]byte("5"), nil},
		{[]byte(nil), nil},
	}
	verifyGetBlobAtOutput(t, streams, errs, expected)

	body = io.NopCloser(bytes.NewReader([]byte("HELLO")))
	defer body.Close()
	streams = make(chan io.ReadCloser)
	errs = make(chan error)
	chunks = []private.ImageSourceChunk{{Offset: 100, Length: 5}}
	mediaType = "text/plain"
	params = map[string]string{}
	go handle206Response(streams, errs, body, chunks, mediaType, params)

	expected = []verifyGetBlobAtData{
		{[]byte("HELLO"), nil},
		{[]byte(nil), nil},
	}
	verifyGetBlobAtOutput(t, streams, errs, expected)
}

func TestParseMediaType(t *testing.T) {
	mediaType, params, err := parseMediaType("multipart/byteranges; boundary=CloudFront:3F750DE0752BEDE3882F7DBE80010D31")
	require.NoError(t, err)
	assert.Equal(t, mediaType, "multipart/byteranges")
	assert.Equal(t, params["boundary"], "CloudFront:3F750DE0752BEDE3882F7DBE80010D31")

	mediaType, params, err = parseMediaType("multipart/byteranges; boundary=00000000000061573284")
	require.NoError(t, err)
	assert.Equal(t, mediaType, "multipart/byteranges")
	assert.Equal(t, params["boundary"], "00000000000061573284")

	mediaType, params, err = parseMediaType("multipart/byteranges; foo=bar; bar=baz")
	require.NoError(t, err)
	assert.Equal(t, mediaType, "multipart/byteranges")
	assert.Equal(t, params["foo"], "bar")
	assert.Equal(t, params["bar"], "baz")

	// quoted symbols '@'
	_, params, err = parseMediaType("multipart/byteranges; boundary=\"@:\"")
	require.NoError(t, err)
	assert.Equal(t, params["boundary"], "@:")

	// unquoted '@'
	_, _, err = parseMediaType("multipart/byteranges; boundary=@")
	require.Error(t, err)
}

func TestIsSigstoreReferrerArtifactType(t *testing.T) {
	for _, c := range []struct {
		artifactType string
		expected     bool
	}{
		{"application/vnd.dev.cosign.simplesigning.v1+json", true},
		{"application/vnd.dev.sigstore.bundle.v0.3+json", true},
		{"application/vnd.dev.cosign.anything", true},
		{"application/vnd.dev.sigstore.anything", true},
		{"application/spdx+json", false},
		{"application/vnd.cyclonedx+json", false},
		{"application/vnd.oci.image.manifest.v1+json", false},
		{"", true},
	} {
		t.Run(c.artifactType, func(t *testing.T) {
			assert.Equal(t, c.expected, isSigstoreReferrerArtifactType(c.artifactType))
		})
	}
}

func TestDeduplicateSigstoreSignatures(t *testing.T) {
	sigA := signature.SigstoreFromComponents("application/vnd.dev.cosign.simplesigning.v1+json", []byte("payload-a"), map[string]string{"key": "val-a"})
	sigB := signature.SigstoreFromComponents("application/vnd.dev.cosign.simplesigning.v1+json", []byte("payload-b"), map[string]string{"key": "val-b"})
	sigADup := signature.SigstoreFromComponents("application/vnd.dev.cosign.simplesigning.v1+json", []byte("payload-a"), map[string]string{"key": "val-a"})
	preExisting := signature.SimpleSigningFromBlob([]byte("pre-existing"))

	t.Run("no duplicates", func(t *testing.T) {
		sigs := []signature.Signature{preExisting, sigA, sigB}
		result := deduplicateSigstoreSignatures(sigs, 1)
		assert.Len(t, result, 3)
	})

	t.Run("duplicate across referrers and cosign tag", func(t *testing.T) {
		sigs := []signature.Signature{preExisting, sigA, sigB, sigADup}
		result := deduplicateSigstoreSignatures(sigs, 1)
		assert.Len(t, result, 3)
	})

	t.Run("pre-existing signatures preserved", func(t *testing.T) {
		sigs := []signature.Signature{preExisting, sigA}
		result := deduplicateSigstoreSignatures(sigs, 1)
		assert.Len(t, result, 2)
	})

	t.Run("all duplicates", func(t *testing.T) {
		sigs := []signature.Signature{sigA, sigADup, sigADup}
		result := deduplicateSigstoreSignatures(sigs, 0)
		assert.Len(t, result, 1)
	})
}

func TestAppendSignaturesFromReferrersArtifactTypeFilter(t *testing.T) {
	const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cosignDigest := digest.Digest("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	spdxDigest := digest.Digest("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")

	sigPayload := []byte(`{"critical":{"type":"cosign container image signature"}}`)
	layerDigest := digest.FromBytes(sigPayload)

	cosignManifest, err := json.Marshal(manifest.OCI1{
		Manifest: imgspecv1.Manifest{
			MediaType: imgspecv1.MediaTypeImageManifest,
			Config: imgspecv1.Descriptor{
				MediaType: signature.SigstoreSignatureMIMEType,
				Digest:    digest.FromBytes([]byte("{}")),
				Size:      2,
			},
			Layers: []imgspecv1.Descriptor{
				{
					MediaType:   signature.SigstoreSignatureMIMEType,
					Digest:      layerDigest,
					Size:        int64(len(sigPayload)),
					Annotations: map[string]string{"dev.cosignproject.cosign/signature": "dGVzdA=="},
				},
			},
		},
	})
	require.NoError(t, err)

	referrersIndex, err := json.Marshal(imgspecv1.Index{
		MediaType: imgspecv1.MediaTypeImageIndex,
		Manifests: []imgspecv1.Descriptor{
			{
				MediaType:    imgspecv1.MediaTypeImageManifest,
				Digest:       cosignDigest,
				Size:         int64(len(cosignManifest)),
				ArtifactType: signature.SigstoreSignatureMIMEType,
			},
			{
				MediaType:    imgspecv1.MediaTypeImageManifest,
				Digest:       spdxDigest,
				Size:         500,
				ArtifactType: "application/spdx+json",
			},
		},
	})
	require.NoError(t, err)

	var mu sync.Mutex
	requestedPaths := map[string]int{}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths[r.URL.Path]++
		mu.Unlock()

		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/referrers/"):
			w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(referrersIndex)
		case strings.Contains(r.URL.Path, "/manifests/"+cosignDigest.String()):
			w.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(cosignManifest)
		case strings.Contains(r.URL.Path, "/manifests/"+spdxDigest.String()):
			t.Error("SPDX manifest should not have been fetched")
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/blobs/"+layerDigest.String()):
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Docker-Content-Digest", layerDigest.String())
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(sigPayload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()

	registry := strings.TrimPrefix(s.URL, "http://")
	named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + testDigest)
	require.NoError(t, err)
	ref, err := newReference(named, false)
	require.NoError(t, err)

	client := &dockerClient{
		sys:                    &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
		registry:               registry,
		scheme:                 "http",
		client:                 s.Client(),
		tokenCache:             map[string]*bearerToken{},
		reportedWarnings:       set.New[string](),
		useSigstoreAttachments: true,
	}
	client.detectPropertiesOnce.Do(func() {})

	src := &dockerImageSource{
		physicalRef: ref,
		c:           client,
	}

	instanceDigest := digest.Digest(testDigest)
	var sigs []signature.Signature
	err = src.appendSignaturesFromReferrers(context.Background(), &sigs, &instanceDigest)
	require.NoError(t, err)

	assert.Len(t, sigs, 1, "should find exactly one cosign signature")

	mu.Lock()
	defer mu.Unlock()
	for path := range requestedPaths {
		assert.NotContains(t, path, spdxDigest.String(), "SPDX referrer manifest should not be fetched")
	}
}
