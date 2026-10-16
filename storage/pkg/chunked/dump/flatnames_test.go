//go:build unix

package dump

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/chunked/internal/minimal"
)

func TestLayerFlatFileNames(t *testing.T) {
	const manifest = `{"version":1,"entries":[
		{"type":"dir","name":"usr/"},
		{"type":"reg","name":"./usr/bin/tool","size":4,"digest":"sha256:0123456789abcdef1123456789abcdef2123456789abcdef3123456789abcdef"},
		{"type":"reg","name":"empty"},
		{"type":"symlink","name":"link","linkName":"usr/bin/tool"}
	]}`
	bigData := func(key string) (io.ReadCloser, error) {
		require.Equal(t, minimal.ManifestBigDataKey, key)
		return io.NopCloser(strings.NewReader(manifest)), nil
	}
	names, err := LayerFlatFileNames(bigData)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"usr/bin/tool": "01/23456789abcdef1123456789abcdef2123456789abcdef3123456789abcdef",
	}, names)

	// A layer without a table of contents has no mapping.
	names, err = LayerFlatFileNames(func(string) (io.ReadCloser, error) {
		return nil, os.ErrNotExist
	})
	require.NoError(t, err)
	assert.Nil(t, names)

	_, err = LayerFlatFileNames(func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("not json")), nil
	})
	assert.Error(t, err)
}
