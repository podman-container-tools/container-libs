//go:build linux || darwin || freebsd

package xattrs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/internal/rootlookupcache"
)

const (
	testXattr          = ".containerlibs.test"
	testXattrOnSymlink = ".containerlibs.other"
	testXattrMissing   = ".containerlibs.missing"
)

// testXattrNamespace returns a prefix for testXattr* and whether we can test xattrs directly on symbolic links
func testXattrNamespace() (string, bool) {
	// Linux doesn’t allow user.* xattrs on symbolic links (because they have no meaningful permission bits
	// and it could be used to evade quotas); system.* xattrs are only accepted for specific system semantics.
	// So, use trusted.*, but that requires CAP_SYS_ADMIN.
	if runtime.GOOS == "linux" {
		if os.Geteuid() != 0 {
			return "user", false
		}
		return "trusted", true
	}
	return "user", true
}

func testXattrListNames(names []string) []string {
	var res []string
	for _, n := range names {
		if strings.Contains(n, ".containerlibs.") {
			res = append(res, n)
		}
	}
	return res
}

func testXattrs(t *testing.T, dir string,
	lgetxattr func(path, attr string) ([]byte, error),
	llistxattr func(path string) ([]string, error),
) {
	ns, canTestSymlinks := testXattrNamespace()
	xattrName := ns + testXattr
	xattrNameOnSymlink := ns + testXattrOnSymlink
	xattrNameMissing := ns + testXattrMissing

	err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	require.NoError(t, err)
	file := filepath.Join(dir, "subdir", "file")
	err = os.WriteFile(file, nil, 0o644)
	require.NoError(t, err)
	err = Lsetxattr(file, xattrName, []byte("v"), 0)
	require.NoError(t, err)

	link := filepath.Join(dir, "subdir", "symlink")
	link2 := filepath.Join(dir, "subdir", "symlink2")
	if canTestSymlinks {
		err = os.Symlink("../subdir/file", link)
		require.NoError(t, err)
		err = os.Symlink("../../../../this/escapes/but/that/does/not/matter", link2)
		require.NoError(t, err)
		err = Lsetxattr(link, xattrNameOnSymlink, []byte("on-link"), 0)
		require.NoError(t, err)
		err = Lsetxattr(link2, xattrNameOnSymlink, []byte("on-link2"), 0)
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		symlinkTest bool
		fsPath      string
		attr        string
		want        []byte
	}{
		{false, "subdir/file", xattrName, []byte("v")},                    // file, attr set
		{false, "subdir/file", xattrNameMissing, nil},                     // file, attr unset
		{true, "subdir/symlink", xattrNameOnSymlink, []byte("on-link")},   // symlink, attr set on link
		{true, "subdir/symlink", xattrName, nil},                          // symlink, attr unset on link (but set on target)
		{true, "subdir/symlink2", xattrNameOnSymlink, []byte("on-link2")}, // symlink to a nonexistent parent path which we can't resolve, attr set on link
	} {
		if tc.symlinkTest && !canTestSymlinks {
			continue
		}
		got, err := lgetxattr(tc.fsPath, tc.attr)
		assert.NoError(t, err)
		assert.Equal(t, tc.want, got)
	}

	for _, tc := range []struct {
		symlinkTest bool
		fsPath      string
		want        []string
	}{
		{false, "subdir/file", []string{xattrName}},
		{true, "subdir/symlink", []string{xattrNameOnSymlink}},
		{true, "subdir/symlink2", []string{xattrNameOnSymlink}},
	} {
		if tc.symlinkTest && !canTestSymlinks {
			continue
		}
		got, err := llistxattr(tc.fsPath)
		require.NoError(t, err)
		assert.ElementsMatch(t, tc.want, testXattrListNames(got))
	}
}

func TestXattrs(t *testing.T) {
	dir := t.TempDir()
	testXattrs(t, dir,
		func(path, attr string) ([]byte, error) {
			return Lgetxattr(filepath.Join(dir, path), attr)
		}, func(path string) ([]string, error) {
			return Llistxattr(filepath.Join(dir, path))
		})
}

func TestHandleXattrs(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()
	rootCache := rootlookupcache.NewCache(root)
	defer rootCache.Close()

	testXattrs(t, dir,
		func(path, attr string) ([]byte, error) {
			parentRoot, fsBasename, err := rootCache.PreparePath(path)
			require.NoError(t, err)
			handle, err := NewLHandle(parentRoot, fsBasename, rootCache)
			require.NoError(t, err)
			defer handle.Close()
			return handle.Getxattr(attr)
		}, func(path string) ([]string, error) {
			parentRoot, fsBasename, err := rootCache.PreparePath(path)
			require.NoError(t, err)
			handle, err := NewLHandle(parentRoot, fsBasename, rootCache)
			require.NoError(t, err)
			defer handle.Close()
			return handle.Listxattr()
		})

	parentRoot, _, err := rootCache.PreparePath(".") // PreparePath("..") would already reject it.
	require.NoError(t, err)
	_, err = NewLHandle(parentRoot, "..", rootCache)
	assert.Error(t, err)
}

func TestRootXattrs(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()

	testXattrs(t, dir,
		func(path, attr string) ([]byte, error) {
			return RootLgetxattr(root, path, attr)
		}, func(path string) ([]string, error) {
			return RootLlistxattr(root, path)
		})

	ns, _ := testXattrNamespace()
	_, err = RootLgetxattr(root, "..", ns+testXattr)
	assert.Error(t, err)
	_, err = RootLlistxattr(root, "..")
	assert.Error(t, err)
}
