//go:build linux

package vfs

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/internal/driver"
)

var _ driver.SplitDirFDStreamDriver = (*Driver)(nil)

func TestDriverLayerFiles(t *testing.T) {
	for _, location := range []string{"home", "image store", "additional store"} {
		t.Run(location, func(t *testing.T) {
			home := t.TempDir()
			other := t.TempDir()
			options := graphdriver.Options{}
			layerDir := filepath.Join(home, "dir", "layer")
			switch location {
			case "image store":
				options.ImageStore = other
				layerDir = filepath.Join(other, "vfs", "dir", "layer")
			case "additional store":
				options.DriverOptions = []string{"vfs.imagestore=" + other}
				layerDir = filepath.Join(other, "vfs", "dir", "layer")
			}
			d, err := Init(home, options)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, d.Cleanup()) })
			streamDriver, ok := d.(driver.SplitDirFDStreamDriver)
			require.True(t, ok)
			_, err = streamDriver.LayerFiles("missing", nil)
			require.Error(t, err)

			require.NoError(t, os.MkdirAll(filepath.Join(layerDir, "etc"), 0o755))
			require.NoError(t, os.Chmod(layerDir, 0o755))
			contentPath := filepath.Join(layerDir, "etc", "conf")
			require.NoError(t, os.WriteFile(contentPath, []byte("config"), 0o644))
			require.NoError(t, os.Chmod(contentPath, 0o644))
			// VFS has no use for the table of contents.
			files, err := streamDriver.LayerFiles("layer", func(string) (io.ReadCloser, error) {
				require.FailNow(t, "VFS read layer big data")
				return nil, nil
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, files.Close()) })
			dirs := files.DirFDs()
			require.Len(t, dirs, 1)
			st, err := dirs[0].Stat()
			require.NoError(t, err)
			want, err := os.Stat(layerDir)
			require.NoError(t, err)
			assert.True(t, os.SameFile(st, want))
			index, name, ok, err := files.Lookup("etc/conf", 6, true)
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Zero(t, index)
			assert.Equal(t, "etc/conf", name)
			_, _, ok, err = files.Lookup("etc/conf", 7, false)
			require.NoError(t, err)
			assert.False(t, ok)
			_, _, ok, err = files.Lookup("missing", 6, false)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}
