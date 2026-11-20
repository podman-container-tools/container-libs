package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/types"
)

func TestRegistryPagination(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/test/repo/tags/list", "/v2/_catalog":
			switch r.URL.Query().Get("page") {
			case "2":
				w.Header().Set("Link", "<http://other.example.invalid"+r.URL.Path+`?page=3>; rel="next"`)
				_, _ = w.Write([]byte(`{"tags":["second"],"repositories":["second"]}`))
			case "3":
				w.Header().Set("Link", `<>; rel="next"`)
				_, _ = w.Write([]byte(`{"tags":["third"],"repositories":["third"]}`))
			default:
				w.Header().Set("Link", `<?page=2>; rel="next"`)
				_, _ = w.Write([]byte(`{"tags":["first"],"repositories":["first"]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	registryURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	registriesConf := filepath.Join(t.TempDir(), "registries.conf")
	require.NoError(t, os.WriteFile(registriesConf, nil, 0o600))
	sys := &types.SystemContext{
		RegistriesDirPath:           "/this/does/not/exist",
		DockerPerHostCertDirPath:    "/this/does/not/exist",
		SystemRegistriesConfPath:    registriesConf,
		DockerAuthConfig:            &types.DockerAuthConfig{},
		DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ref, err := ParseReference("//" + registryURL.Host + "/test/repo:latest")
	require.NoError(t, err)
	tags, err := GetRepositoryTags(ctx, sys, ref)
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second", "third"}, tags)
	results, err := SearchRegistry(ctx, sys, registryURL.Host, "", 4)
	require.NoError(t, err)
	assert.Equal(t, []SearchResult{{Name: "first"}, {Name: "second"}, {Name: "third"}}, results)
}

func TestListReferrers(t *testing.T) {
	manifestBody := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`
	referrersPath := "/v2/test/repo/referrers/" + digest.FromString(manifestBody).String()
	sha512Digest := digest.SHA512.FromString(manifestBody).String()
	referrerA := imgspecv1.Descriptor{MediaType: imgspecv1.MediaTypeImageManifest, Digest: digest.FromString("a"), Size: 1, ArtifactType: "type/x"}
	referrerB := imgspecv1.Descriptor{MediaType: imgspecv1.MediaTypeImageManifest, Digest: digest.FromString("b"), Size: 1, ArtifactType: "type/y"}
	want := []imgspecv1.Descriptor{referrerA, referrerB}
	firstPage := referrersTestIndex(t, referrerA)
	firstPage.link = "<" + referrersPath + `?page=2>; rel="next"`
	secondPage := referrersTestIndex(t, referrerB)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			rw.WriteHeader(http.StatusOK)
		case "/v2/test/repo/manifests/latest", "/v2/test/repo/manifests/" + sha512Digest:
			rw.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
			_, _ = rw.Write([]byte(manifestBody))
		case "/v2/test/repo/referrers/" + sha512Digest:
			secondPage.write(rw)
		case referrersPath:
			if r.URL.Query().Get("page") == "2" {
				secondPage.write(rw)
			} else {
				firstPage.write(rw)
			}
		default:
			assert.Failf(t, "Unexpected request", "%s %s", r.Method, r.URL.Path)
			http.NotFound(rw, r)
		}
	}))
	defer server.Close()
	registryURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	registriesConf := filepath.Join(t.TempDir(), "registries.conf")
	err = os.WriteFile(registriesConf, []byte{}, 0o600)
	require.NoError(t, err)
	sys := &types.SystemContext{
		RegistriesDirPath:           "/this/does/not/exist",
		DockerPerHostCertDirPath:    "/this/does/not/exist",
		SystemRegistriesConfPath:    registriesConf,
		DockerInsecureSkipTLSVerify: types.OptionalBoolTrue,
	}
	ref, err := ParseReference("//" + registryURL.Host + "/test/repo:latest")
	require.NoError(t, err)

	res, err := ListReferrers(context.Background(), sys, ref, "")
	require.NoError(t, err)
	assert.Equal(t, want, res)

	res, err = ListReferrers(context.Background(), sys, ref, "type/y")
	require.NoError(t, err)
	assert.Equal(t, want, res) // The registry ignored artifactType.

	dirRef, err := directory.NewReference(t.TempDir())
	require.NoError(t, err)
	_, err = ListReferrers(context.Background(), sys, dirRef, "")
	assert.Error(t, err)

	t.Run("sha512", func(t *testing.T) {
		ref, err := ParseReference("//" + registryURL.Host + "/test/repo@" + sha512Digest)
		require.NoError(t, err)
		res, err := ListReferrers(context.Background(), sys, ref, "")
		require.NoError(t, err)
		assert.Equal(t, []imgspecv1.Descriptor{referrerB}, res)
	})

	t.Run("mirror", func(t *testing.T) {
		mirrorSys := *sys
		mirrorSys.SystemRegistriesConfPath = filepath.Join(t.TempDir(), "registries.conf")
		require.NoError(t, os.WriteFile(mirrorSys.SystemRegistriesConfPath, []byte(`[[registry]]
location = "with-mirror.example.com"

[[registry.mirror]]
location = "`+registryURL.Host+`/test"
`), 0o600))
		// The mirror adds the /test prefix to the repository path.
		ref, err := ParseReference("//with-mirror.example.com/repo:latest")
		require.NoError(t, err)
		res, err := ListReferrers(context.Background(), &mirrorSys, ref, "")
		require.NoError(t, err)
		assert.Equal(t, want, res)
	})
}
