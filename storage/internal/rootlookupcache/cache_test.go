package rootlookupcache

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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

func TestCacheFileForRoot(t *testing.T) {
	tempDir := t.TempDir()
	root, err := os.OpenRoot(tempDir)
	require.NoError(t, err)
	defer root.Close()

	// To identify the directory handles, we create a numbered ID file in each directory.
	dirIDs := map[string]string{}
	nextDirID := 0

	for _, dir := range []string{".", "dir1", "dir2", "uncached-dir"} {
		err := root.MkdirAll(dir, 0o700)
		require.NoError(t, err)
		dirID := nextDirID
		dirIDBasename := fmt.Sprintf("id%d", dirID)
		dirIDs[dir] = dirIDBasename
		nextDirID++
		err = root.WriteFile(filepath.Join(dir, dirIDBasename), []byte{}, 0o600)
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
		".",    // Root directory is a special case
		"dir2", // Previous non-root directory again
	} {
		parentRoot, err := cache.RootForDir(dir)
		require.NoError(t, err)
		file, err := cache.FileForRoot(parentRoot)
		require.NoError(t, err)

		dirIDBasename, ok := dirIDs[dir]
		require.True(t, ok)

		_, err = file.Seek(0, io.SeekStart)
		require.NoError(t, err)
		contents, err := file.ReadDir(-1)
		require.NoError(t, err)
		assert.True(t, slices.ContainsFunc(contents, func(d fs.DirEntry) bool {
			return d.Name() == dirIDBasename
		}))
	}

	uncachedRoot, err := root.OpenRoot("uncached-dir")
	require.NoError(t, err)
	defer uncachedRoot.Close()
	_, err = cache.FileForRoot(uncachedRoot)
	assert.Error(t, err)
}
