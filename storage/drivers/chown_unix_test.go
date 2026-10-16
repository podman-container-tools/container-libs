//go:build !windows && !darwin

package graphdriver

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/selinux/pkg/pwalkdir"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/idtools"
)

func TestLChownPreservesHardlinkParentMtime(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(strconv.FormatBool(parallel), func(t *testing.T) {
			root := t.TempDir()
			var paths []string
			for i := range 16 {
				dir := filepath.Join(root, strconv.Itoa(i), "nested")
				require.NoError(t, os.MkdirAll(dir, 0o700))
				for j := range 4 {
					path := filepath.Join(dir, strconv.Itoa(j))
					if len(paths) == 0 {
						require.NoError(t, os.WriteFile(path, []byte("hardlinked contents"), 0o640))
					} else {
						require.NoError(t, os.Link(paths[0], path))
					}
					paths = append(paths, path)
				}
			}
			// Root is deliberately not chowned by chownByMapsMain, but its mtime
			// must also survive hard link reconstruction.
			rootLink := filepath.Join(root, "root-link")
			require.NoError(t, os.Link(paths[0], rootLink))
			paths = append(paths, rootLink)
			dirs := setChownDirectoryTimes(t, root)
			chowner := newLChowner()
			chown := func(path string, d fs.DirEntry, err error) error {
				if err != nil || path == root {
					return err
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				return chowner.LChown(path, info, nil, nil)
			}
			if parallel {
				require.NoError(t, pwalkdir.Walk(root, chown))
			} else {
				require.NoError(t, filepath.WalkDir(root, chown))
			}
			checkChownDirectoryTimes(t, dirs)
			first, err := os.Stat(paths[0])
			require.NoError(t, err)
			for _, path := range paths {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.True(t, os.SameFile(first, info), path)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "hardlinked contents", string(data))
				require.Equal(t, os.FileMode(0o640), info.Mode())
			}
		})
	}
}

func setChownDirectoryTimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	dirs := make(map[string]time.Time)
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		dirs[path] = time.Unix(1700000000+int64(len(dirs)), 0)
		return os.Chtimes(path, dirs[path], dirs[path])
	}))
	return dirs
}

func checkChownDirectoryTimes(t *testing.T, dirs map[string]time.Time) {
	t.Helper()
	for path, expected := range dirs {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, expected, info.ModTime(), path)
	}
}

func TestLChownConcurrentHardlinks(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	require.NoError(t, os.WriteFile(first, []byte("data"), 0o600))
	paths := []string{first}
	for i := range 64 {
		path := filepath.Join(root, strconv.Itoa(i))
		require.NoError(t, os.Link(first, path))
		paths = append(paths, path)
	}
	dirs := setChownDirectoryTimes(t, root)
	chowner := newLChowner()
	var wg sync.WaitGroup
	errors := make(chan error, len(paths))
	start := make(chan struct{})
	for _, path := range paths {
		info, err := os.Lstat(path)
		require.NoError(t, err)
		wg.Go(func() {
			<-start
			errors <- chowner.LChown(path, info, nil, nil)
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	checkChownDirectoryTimes(t, dirs)
}

func TestLChownFailedRelinkPreservesParentMtime(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	require.NoError(t, os.WriteFile(first, nil, 0o600))
	require.NoError(t, os.Link(first, second))
	chowner := newLChowner()
	info, err := os.Lstat(first)
	require.NoError(t, err)
	require.NoError(t, chowner.LChown(first, info, nil, nil))
	require.NoError(t, os.Remove(first))
	dirs := setChownDirectoryTimes(t, root)
	info, err = os.Lstat(second)
	require.NoError(t, err)
	require.Error(t, chowner.LChown(second, info, nil, nil))
	checkChownDirectoryTimes(t, dirs)
}

func fillTestFiles(b *testing.B, path string, amount int) {
	dirCount := 0
	dir := path
	for i := range amount {
		if i%256 == 0 {
			dir = filepath.Join(path, "dir"+strconv.Itoa(dirCount))
			err := os.Mkdir(dir, 0o700)
			require.NoError(b, err)
			dirCount++
		}
		f, err := os.Create(filepath.Join(dir, strconv.Itoa(i)))
		require.NoError(b, err)
		f.Close()
	}
}

func Benchmark_LChown(b *testing.B) {
	uid := os.Getuid()
	gid := os.Getgid()
	ids := idtools.NewIDMappingsFromMaps([]idtools.IDMap{
		{
			ContainerID: uid,
			HostID:      uid,
			Size:        1,
		},
	}, []idtools.IDMap{
		{
			ContainerID: gid,
			HostID:      gid,
			Size:        1,
		},
	})

	for _, amount := range []int{1, 10, 100, 1_000, 10_000, 100_000} {
		b.Run(strconv.Itoa(amount), func(b *testing.B) {
			path := b.TempDir()
			fillTestFiles(b, path, amount)
			for b.Loop() {
				chowner := newLChowner()
				var chown fs.WalkDirFunc = func(path string, d fs.DirEntry, _ error) error {
					info, err := d.Info()
					if err != nil {
						return err
					}
					return chowner.LChown(path, info, ids, nil)
				}
				err := filepath.WalkDir(path, chown)
				require.NoError(b, err)
				// log as custom metric the number of elements in the inodes map to show the improvement best
				b.ReportMetric(float64(len(chowner.inodes)), "inodes_count/op")
			}
		})
	}
}
