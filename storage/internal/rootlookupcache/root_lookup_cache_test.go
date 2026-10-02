package rootlookupcache

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
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

const (
	maxDepth      = 4
	entriesPerDir = 100
	dirDivider    = 25
)

func benchmarkDir(b *testing.B) string {
	tempDir := b.TempDir()
	totalDirs := 0
	totalFiles := 0
	createDirs(b, tempDir, ".", maxDepth, &totalDirs, &totalFiles)
	b.Logf("%d dirs, %d files", totalDirs, totalFiles)
	return tempDir
}

func createDirs(t testing.TB, parentPath, relParentPath string, depth int, totalDirs *int, totalFiles *int) {
	for i := range entriesPerDir {
		idx := fmt.Sprintf("%d", i)
		filePath := filepath.Join(parentPath, idx)
		relFilePath := filepath.Join(relParentPath, idx)
		if i%dirDivider == 0 {
			err := os.MkdirAll(filePath, 0o700)
			require.NoError(t, err)
			if depth != 0 {
				createDirs(t, filePath, relFilePath, depth-1, totalDirs, totalFiles)
			}
			*totalDirs++
		} else {
			err := os.WriteFile(filePath, []byte(relFilePath), 0o600)
			require.NoError(t, err)
			*totalFiles++
		}
	}
}

func BenchmarkNoRoot(b *testing.B) {
	dir := benchmarkDir(b)

	for b.Loop() {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			_, err = os.Readlink(path)
			assert.Error(b, err)
			if !d.IsDir() {
				rel, err := filepath.Rel(dir, path)
				require.NoError(b, err)
				contents, err := os.ReadFile(path)
				require.NoError(b, err)
				assert.Equal(b, contents, []byte(rel))
			}
			return nil
		})
		require.NoError(b, err)
	}
}

func BenchmarkUncached(b *testing.B) {
	dir := benchmarkDir(b)

	root, err := os.OpenRoot(dir)
	require.NoError(b, err)
	defer root.Close()

	for b.Loop() {
		err := fs.WalkDir(root.FS(), ".", func(fsPath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			_, err = root.Readlink(fsPath)
			assert.Error(b, err)
			if !d.IsDir() {
				contents, err := root.ReadFile(fsPath)
				require.NoError(b, err)
				assert.Equal(b, contents, []byte(fsPath))
			}
			return nil
		})
		require.NoError(b, err)
	}
}

func BenchmarkCached(b *testing.B) {
	dir := benchmarkDir(b)

	root, err := os.OpenRoot(dir)
	require.NoError(b, err)
	defer root.Close()
	rootCache := NewCache(root)
	defer rootCache.Close()

	for b.Loop() {
		err := fs.WalkDir(root.FS(), ".", func(fsPath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			parentRoot, err := rootCache.RootForDir(path.Dir(fsPath))
			require.NoError(b, err)
			basename := path.Base(fsPath)
			_, err = parentRoot.Readlink(basename)
			assert.Error(b, err)
			if !d.IsDir() {
				contents, err := parentRoot.ReadFile(basename)
				require.NoError(b, err)
				assert.Equal(b, contents, []byte(fsPath))
			}
			return nil
		})
		require.NoError(b, err)
	}
}

// walkDir recursively descends path, calling walkDirFn.
func rootedWalkDirFn(cache *Cache, fullPath string, parentRoot *os.Root, basename string, d fs.DirEntry, walkDirFn func(fsPath string, parentRoot *os.Root, basename string, d fs.DirEntry, err error) error) error {
	if err := walkDirFn(fullPath, parentRoot, basename, d, nil); err != nil || !d.IsDir() {
		if err == fs.SkipDir && d.IsDir() {
			// Successfully skipped directory.
			err = nil
		}
		return err
	}

	dirs, err := func() ([]fs.DirEntry, error) {
		f, err := parentRoot.Open(basename)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		dirs, err := f.ReadDir(-1)
		slices.SortFunc(dirs, func(a, b fs.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
		return dirs, err
	}()
	if err != nil {
		// Second call, to report ReadDir error.
		err = walkDirFn(fullPath, parentRoot, basename, d, err)
		if err != nil {
			if err == fs.SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}

	for _, d1 := range dirs {
		ourRoot, err := cache.RootForDir(fullPath)
		if err != nil {
			return err
		}
		name1 := d1.Name()
		path1 := path.Join(fullPath, name1)
		if err := rootedWalkDirFn(cache, path1, ourRoot, name1, d1, walkDirFn); err != nil {
			if err == fs.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}

// WalkDir walks the file tree rooted at root, calling fn for each file or
// directory in the tree, including root.
//
// All errors that arise visiting files and directories are filtered by fn:
// see the [fs.WalkDirFunc] documentation for details.
//
// The files are walked in lexical order, which makes the output deterministic
// but requires WalkDir to read an entire directory into memory before proceeding
// to walk that directory.
//
// WalkDir does not follow symbolic links found in directories,
// but if root itself is a symbolic link, its target will be walked.
func rootedWalkDir(cache *Cache, root string, fn func(fsPath string, parentRoot *os.Root, basename string, d fs.DirEntry, err error) error) error {
	rootRoot, err := cache.RootForDir(root)
	if err != nil {
		return err
	}
	info, err := rootRoot.Stat(".")
	if err != nil {
		err = fn(".", rootRoot, ".", nil, err)
	} else {
		err = rootedWalkDirFn(cache, ".", rootRoot, ".", fs.FileInfoToDirEntry(info), fn)
	}
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func BenchmarkStepByStepWalk(b *testing.B) {
	dir := benchmarkDir(b)

	root, err := os.OpenRoot(dir)
	require.NoError(b, err)
	defer root.Close()
	rootCache := NewCache(root)
	defer rootCache.Close()

	for b.Loop() {
		err := rootedWalkDir(rootCache, ".", func(fsPath string, parentRoot *os.Root, basename string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			_, err = parentRoot.Readlink(basename)
			assert.Error(b, err)
			if !d.IsDir() {
				contents, err := parentRoot.ReadFile(basename)
				require.NoError(b, err)
				assert.Equal(b, contents, []byte(fsPath))
			}
			return nil
		})
		require.NoError(b, err)
	}
}
