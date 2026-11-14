package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is just a smoke test for the common expected header formats,
// by no means comprehensive.
func TestParseValueAndParams(t *testing.T) {
	for _, c := range []struct {
		input  string
		scope  string
		params map[string]string
	}{
		{
			`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/busybox:pull"`,
			"bearer",
			map[string]string{
				"realm":   "https://auth.docker.io/token",
				"service": "registry.docker.io",
				"scope":   "repository:library/busybox:pull",
			},
		},
		{
			`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/busybox:pull,push"`,
			"bearer",
			map[string]string{
				"realm":   "https://auth.docker.io/token",
				"service": "registry.docker.io",
				"scope":   "repository:library/busybox:pull,push",
			},
		},
		{
			`Bearer realm="http://127.0.0.1:5000/openshift/token"`,
			"bearer",
			map[string]string{"realm": "http://127.0.0.1:5000/openshift/token"},
		},
	} {
		scope, params := parseValueAndParams(c.input)
		assert.Equal(t, c.scope, scope, c.input)
		assert.Equal(t, c.params, params, c.input)
	}
}

func TestParseAuthScopes(t *testing.T) {
	for _, c := range []struct {
		input    string
		expected []authScope // nil when an error is expected
	}{
		{"", nil},
		{"   ", nil},
		{"repository:foo", nil},
		{"repository:foo:pull extra", nil}, // a malformed later token rejects the whole value
		{
			"repository:foo:pull",
			[]authScope{{"repository", "foo", "pull"}},
		},
		{
			"  repository:group/dest:pull,push   repository:other/src:pull  ",
			[]authScope{
				{"repository", "group/dest", "pull,push"},
				{"repository", "other/src", "pull"},
			},
		},
	} {
		scopes, err := parseAuthScopes(c.input)
		if c.expected == nil {
			assert.Error(t, err, c.input)
		} else {
			require.NoError(t, err, c.input)
			assert.Equal(t, c.expected, scopes, c.input)
		}
	}
}
