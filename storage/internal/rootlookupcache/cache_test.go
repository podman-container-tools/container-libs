package rootlookupcache

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheRootForDir(t *testing.T) {
	const idBasename = "id"

	tempDir := t.TempDir()
	root, err := os.OpenRoot(tempDir)
	require.NoError(t, err)
	defer root.Close()

	for _, dir := range []string{".", "dir1", "dir2"} {
		err := root.MkdirAll(dir, 0o700)
		require.NoError(t, err)
		err = root.WriteFile(filepath.Join(dir, idBasename), []byte(dir), 0o600)
		require.NoError(t, err)
	}

	cache := NewCache(root)
	defer func() {
		err := cache.Close()
		require.NoError(t, err)
	}()

	for _, dir := range []string{
		".",            // Access to the root directory
		"dir1", "dir1", // Accesses to the same non-root directory
		"dir2", // Changing the directory
	} {
		dirRoot, err := cache.RootForDir(dir)
		require.NoError(t, err)
		contents, err := dirRoot.ReadFile(idBasename)
		require.NoError(t, err)
		assert.Equal(t, dir, string(contents))
	}

	_, err = cache.RootForDir("this/does/not/exist")
	assert.Error(t, err)
}

func TestCachePreparePath(t *testing.T) {
	const idBasename = "id"

	tempDir := t.TempDir()
	root, err := os.OpenRoot(tempDir)
	require.NoError(t, err)
	defer root.Close()

	for _, dir := range []string{".", "dir1", "dir2"} {
		err := root.MkdirAll(dir, 0o700)
		require.NoError(t, err)
		err = root.WriteFile(filepath.Join(dir, idBasename), []byte(dir), 0o600)
		require.NoError(t, err)
	}

	cache := NewCache(root)
	defer func() {
		err := cache.Close()
		require.NoError(t, err)
	}()

	for _, dir := range []string{
		".",            // Access to the root directory
		"dir1", "dir1", // Accesses to the same non-root directory
		"dir2", // Changing the directory
	} {
		parentRoot, basename, err := cache.PreparePath(path.Join(dir, idBasename))
		require.NoError(t, err)
		contents, err := parentRoot.ReadFile(basename)
		require.NoError(t, err)
		assert.Equal(t, dir, string(contents))
	}

	_, _, err = cache.PreparePath("this/does/not/exist")
	assert.Error(t, err)
	_, _, err = cache.PreparePath("..")
	assert.Error(t, err)
}
