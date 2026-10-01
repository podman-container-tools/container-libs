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

// These benchmarks demonstrate impact of the root lookup cache,
// and a potential for improvement.

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

// BenchmarkNoRoot measures the minimal standard: no os.Root confinement, syscalls with long paths (trusting the kernel dentry cache).
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

// BenchmarkUncached measures the basic use of *os.Root.
// As of Go 1.26, this is pretty costly (factor of >5 compared to BenchmarkNoRoot).
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

// BenchmarkCached measures the effect of the root lookup cache.
// As of Go 1.26, this is about 13% slower than BenchmarkNoRoot.
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

// walkDir recursively descends fullPath == parentRoot/basename, calling walkDirFn.
// This is an internal implementatino detail of rootedWalkDir.
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

// rootedWalkDir is fs.Walkdir(), modified to use the cache and parent-based ~opendir().
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

// BenchmarkStepByStepWalk is a ~proof-of-concept of a minimal fs.WalkDir replacement.
// This gets us to 5% slower than BenchmarkNoRoot.
// It’s not even the best we can do: the walk knows exactly when it walks to children / parents,
// so an extended version of the cache could be used without building parent directory paths and string comparisons.
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
