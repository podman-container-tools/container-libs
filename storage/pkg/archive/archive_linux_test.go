package archive

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/fileutils"
	"go.podman.io/storage/pkg/system"
	"golang.org/x/sys/unix"
)

// setupOverlayTestDir creates files in a directory with overlay whiteouts
// Tree layout
// .
// ├── d1     # opaque, 0700
// │   └── f1 # empty file, 0600
// ├── d2     # opaque, 0750
// │   └── f1 # empty file, 0660
// └── d3     # 0700
//
//	└── f1 # whiteout, 0000
func setupOverlayTestDir(t *testing.T, src string) {
	// Create opaque directory containing single file and permission 0700
	err := os.Mkdir(filepath.Join(src, "d1"), 0o700)
	require.NoError(t, err)

	err = system.Lsetxattr(filepath.Join(src, "d1"), getOverlayOpaqueXattrName(), []byte("y"), 0)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(src, "d1", "f1"), []byte{}, 0o600)
	require.NoError(t, err)

	// Create another opaque directory containing single file but with permission 0750
	err = os.Mkdir(filepath.Join(src, "d2"), 0o750)
	require.NoError(t, err)

	err = system.Lsetxattr(filepath.Join(src, "d2"), getOverlayOpaqueXattrName(), []byte("y"), 0)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(src, "d2", "f1"), []byte{}, 0o660)
	require.NoError(t, err)

	// Create regular directory with deleted file
	err = os.Mkdir(filepath.Join(src, "d3"), 0o700)
	require.NoError(t, err)

	err = system.Mknod(filepath.Join(src, "d3", "f1"), unix.S_IFCHR, 0)
	require.NoError(t, err)
}

func setupOverlayLowerDir(t *testing.T, lower string) {
	// Create a subdirectory to use as the "lower layer"'s copy of a deleted directory
	err := os.Mkdir(filepath.Join(lower, "d1"), 0o700)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(lower, "d1", "f1"), []byte{}, 0o600)
	require.NoError(t, err)
}

func checkOpaqueness(t *testing.T, path string, opaque string) {
	xattrOpaque, err := system.Lgetxattr(path, getOverlayOpaqueXattrName())
	require.NoError(t, err)
	if opaque == "" {
		assert.Nil(t, xattrOpaque)
	}
	if string(xattrOpaque) != opaque {
		t.Fatalf("Unexpected opaque value: %q, expected %q", string(xattrOpaque), opaque)
	}
}

func checkOverlayWhiteout(t *testing.T, path string) {
	stat, err := os.Stat(path)
	require.NoError(t, err)

	statT, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Unexpected type: %t, expected *syscall.Stat_t", stat.Sys())
	}
	if statT.Rdev != 0 {
		t.Fatalf("Non-zero device number for whiteout")
	}
}

func checkFileMode(t *testing.T, path string, perm os.FileMode) {
	stat, err := os.Stat(path)
	require.NoError(t, err)

	if stat.Mode() != perm {
		t.Fatalf("Unexpected file mode for %s: %o, expected %o", path, stat.Mode(), perm)
	}
}

func TestOverlayTarUntar(t *testing.T) {
	oldMask, err := system.Umask(0)
	require.NoError(t, err)
	defer func() {
		_, _ = system.Umask(oldMask) // Ignore err. This can only fail with ErrNotSupportedPlatform, in which case we would have failed above.
	}()

	src := t.TempDir()
	setupOverlayTestDir(t, src)

	lower := t.TempDir()
	setupOverlayLowerDir(t, lower)

	dst := t.TempDir()

	options := &TarOptions{
		Compression:    Uncompressed,
		WhiteoutFormat: OverlayWhiteoutFormat,
		WhiteoutData:   []string{lower},
	}
	archive, err := TarWithOptions(src, options)
	require.NoError(t, err)
	defer archive.Close()

	err = Untar(archive, dst, options)
	require.NoError(t, err)

	checkFileMode(t, filepath.Join(dst, "d1"), 0o700|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d2"), 0o750|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d3"), 0o700|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d1", "f1"), 0o600)
	checkFileMode(t, filepath.Join(dst, "d2", "f1"), 0o660)
	checkFileMode(t, filepath.Join(dst, "d3", "f1"), os.ModeCharDevice|os.ModeDevice)

	checkOpaqueness(t, filepath.Join(dst, "d1"), "y")
	checkOpaqueness(t, filepath.Join(dst, "d2"), "")
	checkOpaqueness(t, filepath.Join(dst, "d3"), "")
	checkOverlayWhiteout(t, filepath.Join(dst, "d3", "f1"))
}

func TestOverlayTarAUFSUntar(t *testing.T) {
	oldMask, err := system.Umask(0)
	require.NoError(t, err)
	defer func() {
		_, _ = system.Umask(oldMask) // Ignore err. This can only fail with ErrNotSupportedPlatform, in which case we would have failed above.
	}()

	src := t.TempDir()
	setupOverlayTestDir(t, src)

	lower := t.TempDir()
	setupOverlayLowerDir(t, lower)

	dst := t.TempDir()

	archive, err := TarWithOptions(src, &TarOptions{
		Compression:    Uncompressed,
		WhiteoutFormat: OverlayWhiteoutFormat,
		WhiteoutData:   []string{lower},
	})
	require.NoError(t, err)
	defer archive.Close()

	err = Untar(archive, dst, &TarOptions{
		Compression:    Uncompressed,
		WhiteoutFormat: AUFSWhiteoutFormat,
	})
	require.NoError(t, err)

	checkFileMode(t, filepath.Join(dst, "d1"), 0o700|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d1", WhiteoutOpaqueDir), 0o700)
	checkFileMode(t, filepath.Join(dst, "d2"), 0o750|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d3"), 0o700|os.ModeDir)
	checkFileMode(t, filepath.Join(dst, "d1", "f1"), 0o600)
	checkFileMode(t, filepath.Join(dst, "d2", "f1"), 0o660)
	checkFileMode(t, filepath.Join(dst, "d3", WhiteoutPrefix+"f1"), 0)
}

func TestNestedOverlayWhiteouts(t *testing.T) {
	reader, writer := io.Pipe()

	go func() {
		tw := tar.NewWriter(writer)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     ".wh.foo",
			Size:     0,
			Uid:      os.Geteuid(),
			Gid:      os.Getegid(),
		}))
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "foo/.wh.bar",
			Size:     0,
			Uid:      os.Geteuid(),
			Gid:      os.Getegid(),
		}))
		require.NoError(t, tw.Close())
	}()

	dst := t.TempDir()

	err := Untar(reader, dst, &TarOptions{
		Compression:    Uncompressed,
		WhiteoutFormat: OverlayWhiteoutFormat,
	})
	require.NoError(t, err)
	checkFileMode(t, filepath.Join(dst, "foo"), os.ModeDevice|os.ModeCharDevice)
}

func TestOverlayWhiteoutConverterConvertRead(t *testing.T) {
	converter := GetWhiteoutConverter(OverlayWhiteoutFormat, nil)
	convertRead := func(path string) (bool, error) {
		// This hard-codes assumptions about which fields the implementation cares about.
		return converter.ConvertRead(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     path,
			Uid:      os.Geteuid(),
			Gid:      os.Getegid(),
		}, path)
	}

	t.Run("non-whiteout", func(t *testing.T) {
		dest := t.TempDir()
		writeFile, err := convertRead(filepath.Join(dest, "file"))
		require.NoError(t, err)
		assert.True(t, writeFile)

		checkOpaqueness(t, dest, "")
	})
	t.Run("opaque directory", func(t *testing.T) {
		dest := t.TempDir()
		whiteoutPath := dest + "/.wh..wh..opq"
		writeFile, err := convertRead(whiteoutPath)
		require.NoError(t, err)
		assert.False(t, writeFile)

		checkOpaqueness(t, dest, "y")
		err = fileutils.Lexists(whiteoutPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("opaque directory error", func(t *testing.T) {
		dest := t.TempDir()
		dir := filepath.Join(dest, "dir") // intentionally not created
		whiteoutPath := dir + "/.wh..wh..opq"
		_, err := convertRead(whiteoutPath)
		require.Error(t, err)
		for _, path := range []string{dir, whiteoutPath} {
			err = fileutils.Lexists(path)
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrNotExist)
		}
	})

	t.Run("whiteout", func(t *testing.T) {
		dest := t.TempDir()
		whiteoutPath := filepath.Join(dest, ".wh.file")
		writeFile, err := convertRead(whiteoutPath)
		require.NoError(t, err)
		assert.False(t, writeFile)
		checkFileMode(t, filepath.Join(dest, "file"), os.ModeDevice|os.ModeCharDevice)
		err = fileutils.Lexists(whiteoutPath)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	// Invalid whiteout base name
	for _, suffix := range []string{"", ".", ".."} {
		t.Run(suffix, func(t *testing.T) {
			dest := t.TempDir()
			whiteoutRelPath := ".wh." + suffix
			_, err := convertRead(filepath.Join(dest, whiteoutRelPath))
			assert.Error(t, err)
			err = fileutils.Lexists(filepath.Join(dest, whiteoutRelPath))
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
	// Whiteout over an existing file
	t.Run("whiteout over regular file", func(t *testing.T) {
		dest := t.TempDir()
		err := os.WriteFile(filepath.Join(dest, "file"), []byte("content"), 0o600)
		require.NoError(t, err)
		_, err = convertRead(filepath.Join(dest, ".wh.file"))
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrExist)
	})
	// Whiteout over an existing symlink
	t.Run("whiteout over symlink", func(t *testing.T) {
		victim := t.TempDir()
		fi1, err := os.Lstat(victim)
		require.NoError(t, err)
		dest := t.TempDir()
		err = os.Symlink(victim, filepath.Join(dest, "symlink"))
		require.NoError(t, err)
		_, err = convertRead(filepath.Join(dest, ".wh.symlink"))
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrExist)
		// victim was not affected
		fi2, err := os.Lstat(victim)
		require.NoError(t, err)
		assertCtimeMatches(t, fi1, fi2)
	})
	// Whiteout inside whiteout
	t.Run("whiteout inside whiteout", func(t *testing.T) {
		dest := t.TempDir()
		writeFile, err := convertRead(filepath.Join(dest, ".wh.dir"))
		require.NoError(t, err)
		assert.False(t, writeFile)
		writeFile, err = convertRead(filepath.Join(dest, "dir", ".wh.file"))
		require.NoError(t, err)
		assert.False(t, writeFile)
		checkFileMode(t, filepath.Join(dest, "dir"), os.ModeDevice|os.ModeCharDevice)
	})
}

func TestUnpackWhiteouts(t *testing.T) {
	hdrEditor := func(hdr *tar.Header) {
		hdr.Uid = os.Getuid()
		hdr.Gid = os.Getgid()
	}

	// WhiteoutOpaqueDir
	for _, path := range []string{"dir", "."} {
		t.Run(path, func(t *testing.T) {
			dest := t.TempDir()
			whiteoutRelPath := path + "/.wh..wh..opq"
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700},
				{Typeflag: tar.TypeReg, Name: whiteoutRelPath, Mode: 0o600},
			}, hdrEditor)
			err := Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
			require.NoError(t, err)
			opaquePath := filepath.Join(dest, path)
			checkOpaqueness(t, opaquePath, "y")
			var otherDir string
			switch strings.TrimPrefix(opaquePath, dest) {
			case "/dir":
				otherDir = dest
			case "":
				otherDir = filepath.Join(dest, "dir")
			default:
				t.Fatalf("Unexpected directory: %s", opaquePath)
			}
			checkOpaqueness(t, otherDir, "")
			err = fileutils.Lexists(filepath.Join(dest, whiteoutRelPath))
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
	// WhiteoutOpaqueDir without an explicit parent directory entry
	// happens to work, because we create the parent as if we were going to create
	// a regular file.
	t.Run("opaque missing", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "dir/.wh..wh..opq", Mode: 0o600},
		}, hdrEditor)
		err := Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.NoError(t, err)
		fi, err := os.Lstat(filepath.Join(dest, "dir"))
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
		checkOpaqueness(t, filepath.Join(dest, "dir"), "y")
	})
	// WhiteoutOpaqueDir targeting a symlink
	t.Run("opaque symlink", func(t *testing.T) {
		victim := t.TempDir()
		dest := t.TempDir()
		symlinkPath := filepath.Join(dest, "symlink")
		err := os.Symlink(victim, symlinkPath)
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "symlink/.wh..wh..opq", Mode: 0o600},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.NoError(t, err)
		checkOpaqueness(t, victim, "") // The target of an escaping symlink is unaffected
		// The symlink path is interpreted as a missing parent directory within dest, and created:
		// Warning: tar archives which use symlinks within parent directories are questionably
		// valid (they are never created through a “normal” archive creation process), we don’t
		// promise this will continue to work.
		symlinkResult := filepath.Join(dest, victim)
		fi, err := os.Lstat(symlinkResult)
		require.NoError(t, err)
		assert.True(t, fi.IsDir())
		checkOpaqueness(t, symlinkResult, "y")
		// The symlink itself is not affected.
		checkOpaqueness(t, symlinkPath, "")
	})

	// Ordinary whiteout
	t.Run("whiteout", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh.file", Mode: 0o600},
		}, hdrEditor)
		err := Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.NoError(t, err)
		checkFileMode(t, filepath.Join(dest, "file"), os.ModeDevice|os.ModeCharDevice)
		err = fileutils.Lexists(filepath.Join(dest, ".wh.file"))
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})
	// Invalid whiteout base name
	for _, suffix := range []string{"", ".", ".."} {
		t.Run(suffix, func(t *testing.T) {
			dest := t.TempDir()
			whiteoutRelPath := ".wh." + suffix
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: whiteoutRelPath, Mode: 0o600},
			}, hdrEditor)
			err := Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
			assert.Error(t, err)
			err = fileutils.Lexists(filepath.Join(dest, whiteoutRelPath))
			require.Error(t, err)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
	// Whiteout over an existing file
	t.Run("whiteout over regular file", func(t *testing.T) {
		dest := t.TempDir()
		err := os.WriteFile(filepath.Join(dest, "file"), []byte("content"), 0o600)
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh.file", Mode: 0o600},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrExist)
	})
	// Whiteout over an existing symlink
	t.Run("whiteout over symlink", func(t *testing.T) {
		victim := t.TempDir()
		fi1, err := os.Lstat(victim)
		require.NoError(t, err)
		dest := t.TempDir()
		err = os.Symlink(victim, filepath.Join(dest, "symlink"))
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh.symlink", Mode: 0o600},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrExist)
		// victim was not affected
		fi2, err := os.Lstat(victim)
		require.NoError(t, err)
		assertCtimeMatches(t, fi1, fi2)
	})
	// Whiteout inside whiteout
	t.Run("whiteout inside whiteout", func(t *testing.T) {
		dest := t.TempDir()
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: ".wh.dir", Mode: 0o600},
			{Typeflag: tar.TypeReg, Name: "dir/.wh.file", Mode: 0o600},
		}, hdrEditor)
		err := Unpack(reader, dest, &TarOptions{WhiteoutFormat: OverlayWhiteoutFormat})
		require.NoError(t, err)
		checkFileMode(t, filepath.Join(dest, "dir"), os.ModeDevice|os.ModeCharDevice)
	})
}

// assertCtimeMatches asserts that fi1 and fi2 have the same ctime.
func assertCtimeMatches(t *testing.T, fi1, fi2 os.FileInfo) {
	t.Helper()
	st1, ok := fi1.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	st2, ok := fi2.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	assert.Equal(t, st1.Ctim, st2.Ctim)
}

func assertAtime(t *testing.T, atime time.Time, fi os.FileInfo) {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	assert.Equal(t, atime, time.Unix(st.Atim.Sec, st.Atim.Nsec))
}
