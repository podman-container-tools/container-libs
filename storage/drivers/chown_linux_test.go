package graphdriver

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/reexec"
	"go.podman.io/storage/pkg/system"
)

func TestMain(m *testing.M) {
	if reexec.Init() {
		return
	}
	os.Exit(m.Run())
}

func TestChownPathByMapsPreservesMetadata(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root for chroot and changing ownership")
	}
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "nested"), 0o750))
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("contents"), 0o750))
	require.NoError(t, os.Chmod(file, 0o750|os.ModeSetuid|os.ModeSetgid))
	capability := []byte{1, 0, 0, 2, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	require.NoError(t, system.Lsetxattr(file, "security.capability", capability, 0))
	require.NoError(t, os.Symlink("file", filepath.Join(root, "symlink")))
	groups := [][]string{{"file", "same", "nested/file", "nested/other"}, {"symlink", "same-symlink", "nested/symlink"}}
	timestamp := time.Unix(1700000000, 0)
	for _, group := range groups {
		for _, path := range group[1:] {
			require.NoError(t, os.Link(filepath.Join(root, group[0]), filepath.Join(root, path)))
		}
		ts := syscall.NsecToTimespec(timestamp.UnixNano())
		require.NoError(t, system.LUtimesNano(filepath.Join(root, group[0]), []syscall.Timespec{ts, ts}))
	}
	dirs := setChownDirectoryTimes(t, root)
	var previous *idtools.IDMappings
	for _, ids := range []idtools.IDPair{{UID: 1, GID: 2}, {UID: 3, GID: 4}, {UID: 3, GID: 4}, {UID: 0, GID: 0}} {
		next := idtools.NewIDMappingsFromMaps(
			[]idtools.IDMap{{ContainerID: 0, HostID: ids.UID, Size: 1}},
			[]idtools.IDMap{{ContainerID: 0, HostID: ids.GID, Size: 1}})
		require.NoError(t, ChownPathByMaps(root, previous, next))
		checkChownDirectoryTimes(t, dirs)
		for _, group := range groups {
			first, err := os.Lstat(filepath.Join(root, group[0]))
			require.NoError(t, err)
			for _, path := range group {
				info, err := os.Lstat(filepath.Join(root, path))
				require.NoError(t, err)
				require.True(t, os.SameFile(first, info), path)
				require.Equal(t, timestamp, info.ModTime(), path)
				st := info.Sys().(*syscall.Stat_t)
				require.EqualValues(t, ids.UID, st.Uid, path)
				require.EqualValues(t, ids.GID, st.Gid, path)
			}
		}
		info, err := os.Stat(file)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o750)|os.ModeSetuid|os.ModeSetgid, info.Mode())
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Equal(t, "contents", string(data))
		actualCapability, err := system.Lgetxattr(file, "security.capability")
		require.NoError(t, err)
		require.Equal(t, capability, actualCapability)
		link, err := os.Readlink(filepath.Join(root, "symlink"))
		require.NoError(t, err)
		require.Equal(t, "file", link)
		info, err = os.Stat(filepath.Join(root, "nested"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o750)|os.ModeDir, info.Mode())
		require.EqualValues(t, ids.UID, info.Sys().(*syscall.Stat_t).Uid)
		require.EqualValues(t, ids.GID, info.Sys().(*syscall.Stat_t).Gid)
		info, err = os.Stat(root)
		require.NoError(t, err)
		require.Zero(t, info.Sys().(*syscall.Stat_t).Uid)
		require.Zero(t, info.Sys().(*syscall.Stat_t).Gid)
		previous = next
	}
}

func TestLChownPreservesParentAtime(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	require.NoError(t, os.WriteFile(first, nil, 0o600))
	require.NoError(t, os.Link(first, second))
	atime, mtime := time.Unix(1600000000, 0), time.Unix(1700000000, 0)
	require.NoError(t, os.Chtimes(root, atime, mtime))
	chowner := newLChowner()
	for _, path := range []string{first, second} {
		info, err := os.Lstat(path)
		require.NoError(t, err)
		require.NoError(t, chowner.LChown(path, info, nil, nil))
	}
	info, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, syscall.NsecToTimespec(atime.UnixNano()), info.Sys().(*syscall.Stat_t).Atim)
	require.Equal(t, mtime, info.ModTime())
}
