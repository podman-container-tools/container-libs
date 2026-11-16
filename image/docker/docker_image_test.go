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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
