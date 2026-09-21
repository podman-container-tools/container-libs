package archive

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/system"
)

var defaultArchiver = NewDefaultArchiver()

func defaultTarUntar(src, dst string) error {
	return defaultArchiver.TarUntar(src, dst)
}

func defaultUntarPath(src, dst string) error {
	return defaultArchiver.UntarPath(src, dst)
}

func defaultCopyFileWithTar(src, dst string) (err error) {
	return defaultArchiver.CopyFileWithTar(src, dst)
}

func defaultCopyWithTar(src, dst string) error {
	return defaultArchiver.CopyWithTar(src, dst)
}

func TestIsArchivePathDir(t *testing.T) {
	tmp := t.TempDir()
	cmd := exec.Command("sh", "-c", "mkdir -p archivedir")
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Fail to create an archive file for test : %s.", output)
	}
	if IsArchivePath(tmp + "archivedir") {
		t.Fatalf("Incorrectly recognised directory as an archive")
	}
}

func TestIsArchivePathInvalidFile(t *testing.T) {
	tmp := t.TempDir()
	cmd := exec.Command("sh", "-c", "dd if=/dev/zero bs=1024 count=1 of=archive && gzip --stdout archive > archive.gz")
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Fail to create an archive file for test : %s.", output)
	}
	if IsArchivePath(filepath.Join(tmp, "archive")) {
		t.Fatalf("Incorrectly recognised invalid tar path as archive")
	}
	if IsArchivePath(filepath.Join(tmp, "archive.gz")) {
		t.Fatalf("Incorrectly recognised invalid compressed tar path as archive")
	}
}

func TestIsArchivePathTar(t *testing.T) {
	var whichTar string
	if runtime.GOOS == solaris {
		whichTar = "gtar"
	} else {
		whichTar = "tar"
	}
	cmdStr := fmt.Sprintf("touch archivedata && %s -cf archive archivedata && gzip --stdout archive > archive.gz", whichTar)
	cmd := exec.Command("sh", "-c", cmdStr)
	tmp := t.TempDir()
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Fail to create an archive file for test : %s.", output)
	}
	if !IsArchivePath(filepath.Join(tmp, "archive")) {
		t.Fatalf("Did not recognise valid tar path as archive")
	}
	if !IsArchivePath(filepath.Join(tmp, "archive.gz")) {
		t.Fatalf("Did not recognise valid compressed tar path as archive")
	}
}

func testDecompressStream(t *testing.T, ext, compressCommand string) {
	tmp := t.TempDir()
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("touch archive && %s archive", compressCommand))
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to create an archive file for test : %s.", output)
	}
	filename := filepath.Join(tmp, "archive."+ext)
	archive, err := os.Open(filename)
	if err != nil {
		t.Fatalf("Failed to open file %s: %v", filename, err)
	}
	defer archive.Close()

	r, err := DecompressStream(archive)
	if err != nil {
		t.Fatalf("Failed to decompress %s: %v", filename, err)
	}
	if _, err = io.ReadAll(r); err != nil {
		t.Fatalf("Failed to read the decompressed stream: %v ", err)
	}
	if err = r.Close(); err != nil {
		t.Fatalf("Failed to close the decompressed stream: %v ", err)
	}
}

func TestDecompressStreamGzip(t *testing.T) {
	testDecompressStream(t, "gz", "gzip -f")
}

func TestDecompressStreamBzip2(t *testing.T) {
	testDecompressStream(t, "bz2", "bzip2 -f")
}

func TestDecompressStreamXz(t *testing.T) {
	if runtime.GOOS == windows {
		t.Skip("Xz not present in msys2")
	}
	testDecompressStream(t, "xz", "xz -f")
}

func TestCompressStreamXzUnsupported(t *testing.T) {
	if _, err := CompressStream(&bytes.Buffer{}, Xz); err == nil {
		t.Fatalf("Should fail as xz is unsupported for compression format.")
	}
}

func TestCompressStreamBzip2Unsupported(t *testing.T) {
	if _, err := CompressStream(&bytes.Buffer{}, Bzip2); err == nil {
		t.Fatalf("Should fail as bz2 is unsupported for compression format.")
	}
}

func TestCompressStreamInvalid(t *testing.T) {
	if _, err := CompressStream(&bytes.Buffer{}, -1); err == nil {
		t.Fatalf("Should fail as -1 is an invalid compression format.")
	}
}

func TestExtensionInvalid(t *testing.T) {
	compression := Compression(-1)
	output := compression.Extension()
	if output != "" {
		t.Fatalf("The extension of an invalid compression should be an empty string.")
	}
}

func TestExtensionUncompressed(t *testing.T) {
	compression := Uncompressed
	output := compression.Extension()
	if output != "tar" {
		t.Fatalf("The extension of an uncompressed archive should be 'tar'.")
	}
}

func TestExtensionBzip2(t *testing.T) {
	compression := Bzip2
	output := compression.Extension()
	if output != "tar.bz2" {
		t.Fatalf("The extension of a bzip2 archive should be 'tar.bz2'")
	}
}

func TestExtensionGzip(t *testing.T) {
	compression := Gzip
	output := compression.Extension()
	if output != "tar.gz" {
		t.Fatalf("The extension of a bzip2 archive should be 'tar.gz'")
	}
}

func TestExtensionXz(t *testing.T) {
	compression := Xz
	output := compression.Extension()
	if output != "tar.xz" {
		t.Fatalf("The extension of a bzip2 archive should be 'tar.xz'")
	}
}

func createEmptyFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, nil, 0o666))
}

func TestUnpack(t *testing.T) {
	hdrEditor := func(hdr *tar.Header) {
		hdr.Uid = os.Getuid()
		hdr.Gid = os.Getgid()
	}

	// A smoke test; TestExtractTarFileEntry tests that the correct kinds of individual files are created, with the right properties.
	dest := t.TempDir()
	reader := tarStream(t, []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700},
		{Typeflag: tar.TypeReg, Name: "regular", Mode: 0o600},
		// tar.TypeBlock, tar.typeChar untested because they require root privileges
		{Typeflag: tar.TypeFifo, Name: "fifo", Mode: 0o600},
		{Typeflag: tar.TypeLink, Name: "link", Linkname: "regular", Mode: 0o600},
		{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "dangling/local/target", Mode: 0o700},
		{Typeflag: tar.TypeDir, Name: "bin", Mode: 0o700},
		{Typeflag: tar.TypeReg, Name: "bin/[", Mode: 0o600},
		{Typeflag: tar.TypeLink, Name: "bin/test", Linkname: "bin/[", Mode: 0o600},
	}, hdrEditor)
	err := Unpack(reader, dest, &TarOptions{})
	assert.NoError(t, err)

	// ExcludePatterns
	// Warning: It’s extremely unclear how much of this semantics is intentional; the field is completely undocumented,
	// and has quite different semantics when creating archives. At the very least, the prefix match seems unexpected.
	for _, c := range []struct {
		name, exclude string
		created       bool
	}{
		{name: "file", exclude: "no-match", created: true},
		{name: "basic", exclude: "basic", created: false},
		{name: "././pattern", exclude: "pattern", created: false},
		{name: "/././pattern", exclude: "pattern", created: true},
		{name: "/././pattern", exclude: "/pattern", created: false},
		{name: "dir/file", exclude: "dir", created: false},
		{name: "dir/file", exclude: "file", created: true},
		{name: "dir/../file", exclude: "dir", created: true},
		{name: "prefixwithnodelimiter/file", exclude: "prefix", created: false},
	} {
		t.Run(c.name+"|"+c.exclude, func(t *testing.T) {
			dest := t.TempDir()
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: c.name, Mode: 0o600},
			}, hdrEditor)
			err := Unpack(reader, dest, &TarOptions{ExcludePatterns: []string{c.exclude}})
			assert.NoError(t, err)
			root, err := os.OpenRoot(dest)
			require.NoError(t, err)
			defer root.Close()
			// This allows testing presence of dir/../file when dir does not exist, even though a “real” consumer would be perfectly
			// legitimate failing such an access.
			relPath := filepath.Clean(c.name)
			// Also allow absolute paths, which root.Open() rejects.
			relPath = strings.TrimPrefix(relPath, "/")
			if relPath == "" {
				relPath = "."
			}
			f, err := root.Open(relPath)
			if c.created {
				assert.NoError(t, err)
				f.Close()
			} else {
				require.Error(t, err)
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}

	// Paths are confined to the destination
	for i, headers := range [][]*tar.Header{
		{ // Direct overwrite
			{Typeflag: tar.TypeReg, Name: "../victim/hello", Mode: 0o600},
		},
		{ // Overwrite through an escaping symlink to directory
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "../victim", Mode: 0o755},
			{Typeflag: tar.TypeReg, Name: "symlink/hello", Mode: 0o600},
		},
		{ // Overwrite through an absolute symlink to directory
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "@TOP@/victim", Mode: 0o644},
			{Typeflag: tar.TypeReg, Name: "symlink/hello", Mode: 0o600},
		},
		{ // Overwrite through symlink to directory using paths that _look_ innocuous
			{Typeflag: tar.TypeSymlink, Name: "a/b/c", Linkname: "../..", Mode: 0o755}, // Points at the root
			{Typeflag: tar.TypeSymlink, Name: "a/b/c/d", Linkname: "..", Mode: 0o755},  // = root/..
			{Typeflag: tar.TypeReg, Name: "a/b/c/d/victim/hello", Mode: 0o600},
		},
		{ // Overwrite through an escaping symlink directly to victim
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "../victim/hello", Mode: 0o755},
			{Typeflag: tar.TypeReg, Name: "symlink", Mode: 0o600},
		},
		{ // Overwrite through an absolute symlink directly to victim
			{Typeflag: tar.TypeSymlink, Name: "symlink", Linkname: "@TOP@/victim/hello", Mode: 0o644},
			{Typeflag: tar.TypeReg, Name: "symlink", Mode: 0o600},
		},
		{ // Overwrite through symlink directly to victim using paths that _look_ innocuous
			{Typeflag: tar.TypeSymlink, Name: "a/b/c", Linkname: "../..", Mode: 0o755},                   // Points at the root
			{Typeflag: tar.TypeSymlink, Name: "a/b/c/symlink", Linkname: "../victim/hello", Mode: 0o755}, // = root/../victim/hello
			{Typeflag: tar.TypeReg, Name: "a/b/c/symlink", Mode: 0o600},
		},
	} {
		t.Run(fmt.Sprintf("Breakout%d", i), func(t *testing.T) {
			err := testBreakout(t, breakoutUnpack, headers)
			assert.NoError(t, err)
		})
	}
	// Symbolic links are interpreted relative to the destination.
	t.Run("symlinks", func(t *testing.T) {
		dest := t.TempDir()
		for i := range []int{1, 2} {
			err := os.Mkdir(filepath.Join(dest, fmt.Sprintf("dir%d", i)), 0o700)
			require.NoError(t, err)
		}
		err := os.Symlink("../../../dir2", filepath.Join(dest, "dir1", "relative"))
		require.NoError(t, err)
		err = os.Symlink("/dir2", filepath.Join(dest, "dir1", "absolute"))
		require.NoError(t, err)
		reader := tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeReg, Name: "dir1/relative/through-relative", Mode: 0o600},
			{Typeflag: tar.TypeReg, Name: "dir1/absolute/through-absolute", Mode: 0o600},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{})
		assert.NoError(t, err)
		assert.FileExists(t, filepath.Join(dest, "dir2", "through-relative"))
		assert.FileExists(t, filepath.Join(dest, "dir2", "through-absolute"))
	})

	// The destination must not be created/replaced as a non-directory.
	for _, preexisting := range []bool{true, false} {
		for _, destSuffix := range []string{"", "/"} {
			for _, rootName := range []string{".", "/"} {
				t.Run(fmt.Sprintf("symlink root dest=%s, preexisting=%t, root=%s", destSuffix, preexisting, rootName), func(t *testing.T) {
					victim := t.TempDir()
					victimFile := filepath.Join(victim, "file")
					err := os.WriteFile(victimFile, []byte("content"), 0o600)
					require.NoError(t, err)
					fis := map[string]os.FileInfo{}
					for _, path := range []string{victim, victimFile} {
						fi, err := os.Lstat(path)
						require.NoError(t, err)
						fis[path] = fi
					}

					dest := filepath.Join(t.TempDir(), "dest")
					if preexisting {
						err := os.Mkdir(dest, 0o700)
						require.NoError(t, err)
					}

					reader := tarStream(t, []*tar.Header{
						{Typeflag: tar.TypeSymlink, Name: rootName, Linkname: victim, Mode: 0o700},
						{Typeflag: tar.TypeReg, Name: filepath.Join(rootName, "file"), Mode: 0o600},
					}, hdrEditor)
					err = Unpack(reader, dest+destSuffix, &TarOptions{})
					require.Error(t, err)

					if preexisting {
						fi, err := os.Lstat(dest)
						require.NoError(t, err)
						assert.True(t, fi.IsDir())
					}
					// The victim paths were not affected
					for _, path := range []string{victim, victimFile} {
						fi, err := os.Lstat(path)
						require.NoError(t, err)
						assertCtimeMatches(t, fi, fis[path])
					}
				})

				// TypeDir entries for dest are accepted.
				t.Run(fmt.Sprintf("dir root dest=%s, preexisting=%t, dir=%s", destSuffix, preexisting, rootName), func(t *testing.T) {
					dest := filepath.Join(t.TempDir(), "dest")
					if preexisting {
						err := os.Mkdir(dest, 0o700)
						require.NoError(t, err)
					}

					reader := tarStream(t, []*tar.Header{
						{Typeflag: tar.TypeDir, Name: rootName, Mode: 0o700},
						{Typeflag: tar.TypeReg, Name: filepath.Join(rootName, "file"), Mode: 0o600},
					}, hdrEditor)
					err = Unpack(reader, dest+destSuffix, &TarOptions{})
					require.NoError(t, err)

					fi, err := os.Lstat(dest)
					require.NoError(t, err)
					assert.True(t, fi.IsDir())
				})
			}
		}
	}

	// Parent directory is automatically created
	for _, c := range []struct {
		relDest, fileName string
		dirs              []string // relative to the temporary directory, not relDest (e.g. can be relDest)
	}{
		{relDest: ".", fileName: "file", dirs: []string{"."}},                                            // Pre-existing destination
		{relDest: ".", fileName: "dir/file", dirs: []string{".", "dir"}},                                 // Directory within the destination
		{relDest: "nonexistent", fileName: "file", dirs: []string{"nonexistent"}},                        // Even the destination may be created
		{relDest: "nonexistent", fileName: "dir/file", dirs: []string{"nonexistent", "nonexistent/dir"}}, // Destination + dir within both created
	} {
		t.Run(c.relDest+"|"+c.fileName, func(t *testing.T) {
			top := t.TempDir()
			reader := tarStream(t, []*tar.Header{
				{Typeflag: tar.TypeReg, Name: c.fileName, Mode: 0o600},
			}, hdrEditor)
			dest := filepath.Join(top, c.relDest) // No Mkdir(dest)!
			err := Unpack(reader, dest, &TarOptions{})
			assert.NoError(t, err)
			fi, err := os.Lstat(filepath.Join(dest, c.fileName))
			require.NoError(t, err)
			assert.True(t, fi.Mode().IsRegular())
			for _, dir := range c.dirs {
				fi, err := os.Lstat(filepath.Join(top, dir))
				require.NoError(t, err)
				assert.True(t, fi.IsDir())
			}
		})
	}

	// Overwriting pre-existing files
	for _, c := range []struct {
		tarTypes     []byte
		noOverwrite  bool
		expectError  bool
		expectedType fs.FileMode
	}{
		{tarTypes: []byte{tar.TypeReg, tar.TypeReg}, expectedType: fs.FileMode(0)},                                       // reg -> reg
		{tarTypes: []byte{tar.TypeDir, tar.TypeDir}, expectedType: fs.ModeDir},                                           // dir -> dir
		{tarTypes: []byte{tar.TypeReg, tar.TypeDir}, expectedType: fs.ModeDir},                                           // reg -> dir
		{tarTypes: []byte{tar.TypeDir, tar.TypeReg}, expectedType: fs.FileMode(0)},                                       // dir -> reg
		{tarTypes: []byte{tar.TypeReg, tar.TypeReg}, noOverwrite: true, expectedType: fs.FileMode(0)},                    // reg -> reg
		{tarTypes: []byte{tar.TypeDir, tar.TypeDir}, noOverwrite: true, expectedType: fs.ModeDir},                        // dir -> dir
		{tarTypes: []byte{tar.TypeReg, tar.TypeDir}, noOverwrite: true, expectError: true, expectedType: fs.FileMode(0)}, // reg [-> dir]
		{tarTypes: []byte{tar.TypeDir, tar.TypeReg}, noOverwrite: true, expectError: true, expectedType: fs.ModeDir},     // dir [-> reg]
	} {
		t.Run("", func(t *testing.T) {
			dest := t.TempDir()
			hdrs := []*tar.Header{}
			for _, tarType := range c.tarTypes {
				hdrs = append(hdrs, &tar.Header{Typeflag: tarType, Name: "test", Mode: 0o700})
			}
			reader := tarStream(t, hdrs, hdrEditor)
			err := Unpack(reader, dest, &TarOptions{NoOverwriteDirNonDir: c.noOverwrite})
			if c.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			fi, err := os.Lstat(filepath.Join(dest, "test"))
			require.NoError(t, err)
			assert.Equal(t, c.expectedType, fi.Mode().Type())
		})
	}

	// whiteoutConverter handling is tested in Linux-only TestUnpackWhiteouts.

	// Directory times are set correctly even if we create files inside them.
	mtime := time.Unix(1, 0)
	atime := time.Unix(2, 0)
	t.Run("directory times", func(t *testing.T) {
		dest = t.TempDir()
		// An explicit FormatPAX is necessary, otherwise archive/tar prefers to use a simpler header which does not encode AccessTime,
		reader = tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700, ModTime: mtime, AccessTime: atime, Format: tar.FormatPAX},
			{Typeflag: tar.TypeReg, Name: "dir/inside", Mode: 0o600, ModTime: mtime, AccessTime: atime, Format: tar.FormatPAX},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{})
		assert.NoError(t, err)
		for _, path := range []string{"dir", "dir/inside"} {
			t.Run(path, func(t *testing.T) {
				fi, err := os.Lstat(filepath.Join(dest, path))
				require.NoError(t, err)
				assert.Equal(t, mtime, fi.ModTime())
				assertAtime(t, atime, fi)
			})
		}
	})
	// The directory times code does not follow symlinks.
	t.Run("directory times on symlink", func(t *testing.T) {
		victim := t.TempDir()
		fi1, err := os.Lstat(victim)
		require.NoError(t, err)

		dest = t.TempDir()
		// An explicit FormatPAX is necessary, otherwise archive/tar prefers to use a simpler header which does not encode AccessTime,
		reader = tarStream(t, []*tar.Header{
			{Typeflag: tar.TypeDir, Name: "dir", Mode: 0o700, ModTime: mtime, AccessTime: atime, Format: tar.FormatPAX},
			{Typeflag: tar.TypeSymlink, Name: "dir", Linkname: victim, Mode: 0o700},
		}, hdrEditor)
		err = Unpack(reader, dest, &TarOptions{})
		assert.NoError(t, err)
		fi, err := os.Lstat(filepath.Join(dest, "dir"))
		require.NoError(t, err)
		assert.Equal(t, fs.ModeSymlink, fi.Mode().Type())

		fi2, err := os.Lstat(victim)
		require.NoError(t, err)
		assertCtimeMatches(t, fi2, fi1)
	})

	// Setting BSD flags of directories is untested
}

func TestUntarPathWithInvalidDest(t *testing.T) {
	tempFolder := t.TempDir()
	invalidDestFolder := filepath.Join(tempFolder, "invalidDest")
	// Create a src file
	srcFile := filepath.Join(tempFolder, "src")
	tarFile := filepath.Join(tempFolder, "src.tar")
	createEmptyFile(t, srcFile)
	createEmptyFile(t, invalidDestFolder) // being a file (not dir) should cause an error

	// Translate back to Unix semantics as next exec.Command is run under sh
	srcFileU := srcFile
	tarFileU := tarFile
	if runtime.GOOS == windows {
		tarFileU = "/tmp/" + filepath.Base(filepath.Dir(tarFile)) + "/src.tar"
		srcFileU = "/tmp/" + filepath.Base(filepath.Dir(srcFile)) + "/src"
	}

	cmd := exec.Command("sh", "-c", "tar cf "+tarFileU+" "+srcFileU)
	_, err := cmd.CombinedOutput()
	require.NoError(t, err)

	err = defaultUntarPath(tarFile, invalidDestFolder)
	if err == nil {
		t.Fatalf("UntarPath with invalid destination path should throw an error.")
	}
}

func TestUntarPathWithInvalidSrc(t *testing.T) {
	dest := t.TempDir()
	err := defaultUntarPath("/invalid/path", dest)
	if err == nil {
		t.Fatalf("UntarPath with invalid src path should throw an error.")
	}
}

func TestUntarPath(t *testing.T) {
	tmpFolder := t.TempDir()
	srcFile := filepath.Join(tmpFolder, "src")
	tarFile := filepath.Join(tmpFolder, "src.tar")
	createEmptyFile(t, filepath.Join(tmpFolder, "src"))

	destFolder := filepath.Join(tmpFolder, "dest")
	err := os.MkdirAll(destFolder, 0o740)
	if err != nil {
		t.Fatalf("Fail to create the destination file")
	}

	// Translate back to Unix semantics as next exec.Command is run under sh
	srcFileU := srcFile
	tarFileU := tarFile
	if runtime.GOOS == windows {
		tarFileU = "/tmp/" + filepath.Base(filepath.Dir(tarFile)) + "/src.tar"
		srcFileU = "/tmp/" + filepath.Base(filepath.Dir(srcFile)) + "/src"
	}
	cmd := exec.Command("sh", "-c", "tar cf "+tarFileU+" "+srcFileU)
	_, err = cmd.CombinedOutput()
	require.NoError(t, err)

	err = defaultUntarPath(tarFile, destFolder)
	if err != nil {
		t.Fatalf("UntarPath shouldn't throw an error, %s.", err)
	}
	expectedFile := filepath.Join(destFolder, srcFileU)
	_, err = os.Stat(expectedFile)
	if err != nil {
		t.Fatalf("Destination folder should contain the source file but did not.")
	}
}

// Do the same test as above but with the destination as file, it should fail
func TestUntarPathWithDestinationFile(t *testing.T) {
	tmpFolder := t.TempDir()
	srcFile := filepath.Join(tmpFolder, "src")
	tarFile := filepath.Join(tmpFolder, "src.tar")
	createEmptyFile(t, filepath.Join(tmpFolder, "src"))

	// Translate back to Unix semantics as next exec.Command is run under sh
	srcFileU := srcFile
	tarFileU := tarFile
	if runtime.GOOS == windows {
		tarFileU = "/tmp/" + filepath.Base(filepath.Dir(tarFile)) + "/src.tar"
		srcFileU = "/tmp/" + filepath.Base(filepath.Dir(srcFile)) + "/src"
	}
	cmd := exec.Command("sh", "-c", "tar cf "+tarFileU+" "+srcFileU)
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	destFile := filepath.Join(tmpFolder, "dest")
	createEmptyFile(t, destFile)
	err = defaultUntarPath(tarFile, destFile)
	if err == nil {
		t.Fatalf("UntarPath should throw an error if the destination if a file")
	}
}

// Do the same test as above but with the destination folder already exists
// and the destination file is a directory
// It's working, see https://github.com/docker/docker/issues/10040
func TestUntarPathWithDestinationSrcFileAsFolder(t *testing.T) {
	tmpFolder := t.TempDir()
	srcFile := filepath.Join(tmpFolder, "src")
	tarFile := filepath.Join(tmpFolder, "src.tar")
	createEmptyFile(t, srcFile)

	// Translate back to Unix semantics as next exec.Command is run under sh
	srcFileU := srcFile
	tarFileU := tarFile
	if runtime.GOOS == windows {
		tarFileU = "/tmp/" + filepath.Base(filepath.Dir(tarFile)) + "/src.tar"
		srcFileU = "/tmp/" + filepath.Base(filepath.Dir(srcFile)) + "/src"
	}

	cmd := exec.Command("sh", "-c", "tar cf "+tarFileU+" "+srcFileU)
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	destFolder := filepath.Join(tmpFolder, "dest")
	err = os.MkdirAll(destFolder, 0o740)
	if err != nil {
		t.Fatalf("Fail to create the destination folder")
	}
	// Let's create a folder that will has the same path as the extracted file (from tar)
	destSrcFileAsFolder := filepath.Join(destFolder, srcFileU)
	err = os.MkdirAll(destSrcFileAsFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultUntarPath(tarFile, destFolder)
	if err != nil {
		t.Fatalf("UntarPath should throw not throw an error if the extracted file already exists and is a folder")
	}
}

func TestCopyWithTarInvalidSrc(t *testing.T) {
	tempFolder := t.TempDir()
	destFolder := filepath.Join(tempFolder, "dest")
	invalidSrc := filepath.Join(tempFolder, "doesnotexists")
	err := os.MkdirAll(destFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultCopyWithTar(invalidSrc, destFolder)
	if err == nil {
		t.Fatalf("archiver.CopyWithTar with invalid src path should throw an error.")
	}
}

func TestCopyWithTarInexistentDestWillCreateIt(t *testing.T) {
	tempFolder := t.TempDir()
	srcFolder := filepath.Join(tempFolder, "src")
	inexistentDestFolder := filepath.Join(tempFolder, "doesnotexists")
	err := os.MkdirAll(srcFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultCopyWithTar(srcFolder, inexistentDestFolder)
	if err != nil {
		t.Fatalf("CopyWithTar with an inexistent folder shouldn't fail.")
	}
	_, err = os.Stat(inexistentDestFolder)
	if err != nil {
		t.Fatalf("CopyWithTar with an inexistent folder should create it.")
	}
}

// Test CopyWithTar with a file as src
func TestCopyWithTarSrcFile(t *testing.T) {
	folder := t.TempDir()
	dest := filepath.Join(folder, "dest")
	srcFolder := filepath.Join(folder, "src")
	src := filepath.Join(folder, filepath.Join("src", "src"))
	err := os.MkdirAll(srcFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.MkdirAll(dest, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(src, []byte("content"), 0o777)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar couldn't write content, %s.", err)
	}
	err = defaultCopyWithTar(src, dest)
	if err == nil {
		t.Fatalf("archiver.CopyWithTar should have thrown an overwrite error.")
	} else if _, isOverwriteError := err.(overwriteError); !isOverwriteError {
		t.Fatalf("archiver.CopyWithTar shouldn't throw an error other than overwrite, %s.", err)
	}
	err = os.Remove(dest)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar couldn't remove dest dir, %s.", err)
	}
	err = defaultCopyWithTar(src, dest)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar shouldn't have thrown an error, %s.", err)
	}
	err = os.WriteFile(dest, []byte("modified content"), 0o751)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar couldn't write modified content, %s.", err)
	}
	err = defaultCopyWithTar(src, dest)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar shouldn't have thrown an error, %s.", err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar should be able to stat the source, %s.", err)
	}
	destInfo, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar should be able to stat the destination, %s.", err)
	}
	if srcInfo.IsDir() != destInfo.IsDir() {
		t.Fatalf("Destination (dir=%t) should be the same as the source (dir=%t).", destInfo.IsDir(), srcInfo.IsDir())
	}
	if srcInfo.Mode() != destInfo.Mode() {
		t.Fatalf("Destination (mode=%0o) should be the same as the source (mode=%0o).", destInfo.Mode(), srcInfo.Mode())
	}
	if srcInfo.Size() != destInfo.Size() {
		t.Fatalf("Destination (size=%d) should be the same as the source (size=%d).", destInfo.Size(), srcInfo.Size())
	}
	if !srcInfo.ModTime().Equal(destInfo.ModTime()) {
		t.Fatalf("Destination (date=%s) should be the same as the source (date=%s).", destInfo.ModTime(), srcInfo.ModTime())
	}
}

// Test CopyWithTar with a folder as src
func TestCopyWithTarSrcFolder(t *testing.T) {
	folder := t.TempDir()
	dest := filepath.Join(folder, "dest")
	src := filepath.Join(folder, filepath.Join("src", "folder"))
	err := os.MkdirAll(src, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.MkdirAll(dest, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(src, "file"), []byte("content"), 0o777)
	require.NoError(t, err)
	err = defaultCopyWithTar(src, dest)
	if err != nil {
		t.Fatalf("archiver.CopyWithTar shouldn't throw an error, %s.", err)
	}
	_, err = os.Stat(dest)
	// FIXME Check the content (the file inside)
	if err != nil {
		t.Fatalf("Destination folder should contain the source file but did not.")
	}
}

func TestCopyFileWithTarInvalidSrc(t *testing.T) {
	tempFolder := t.TempDir()
	destFolder := filepath.Join(tempFolder, "dest")
	err := os.MkdirAll(destFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	invalidFile := filepath.Join(tempFolder, "doesnotexists")
	err = defaultCopyFileWithTar(invalidFile, destFolder)
	if err == nil {
		t.Fatalf("archiver.CopyWithTar with invalid src path should throw an error.")
	}
}

func TestCopyFileWithTarInexistentDestWillCreateIt(t *testing.T) {
	tempFolder := t.TempDir()
	srcFile := filepath.Join(tempFolder, "src")
	inexistentDestFolder := filepath.Join(tempFolder, "doesnotexists")
	createEmptyFile(t, srcFile)
	err := defaultCopyFileWithTar(srcFile, inexistentDestFolder)
	if err != nil {
		t.Fatalf("CopyWithTar with an inexistent folder shouldn't fail.")
	}
	_, err = os.Stat(inexistentDestFolder)
	if err != nil {
		t.Fatalf("CopyWithTar with an inexistent folder should create it.")
	}
	// FIXME Test the src file and content
}

func TestCopyFileWithTarSrcFolder(t *testing.T) {
	folder := t.TempDir()
	dest := filepath.Join(folder, "dest")
	src := filepath.Join(folder, "srcfolder")
	err := os.MkdirAll(src, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.MkdirAll(dest, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultCopyFileWithTar(src, dest)
	if err == nil {
		t.Fatalf("CopyFileWithTar should throw an error with a folder.")
	}
}

func TestCopyFileWithTarSrcFile(t *testing.T) {
	folder := t.TempDir()
	dest := filepath.Join(folder, "dest")
	srcFolder := filepath.Join(folder, "src")
	src := filepath.Join(folder, filepath.Join("src", "src"))
	err := os.MkdirAll(srcFolder, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.MkdirAll(dest, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(src, []byte("content"), 0o777)
	require.NoError(t, err)
	err = defaultCopyWithTar(src, dest+"/")
	if err != nil {
		t.Fatalf("archiver.CopyFileWithTar shouldn't throw an error, %s.", err)
	}
	_, err = os.Stat(dest)
	if err != nil {
		t.Fatalf("Destination folder should contain the source file but did not.")
	}
}

func TestCopySocket(t *testing.T) {
	folder := t.TempDir()
	dest := filepath.Join(folder, "dest")
	src := filepath.Join(folder, "src")
	err := os.MkdirAll(src, 0o740)
	if err != nil {
		t.Fatal(err)
	}

	_, err = net.Listen("unix", filepath.Join(src, "unix-socket"))
	if err != nil {
		t.Fatal(err)
	}

	err = os.MkdirAll(dest, 0o740)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultCopyWithTar(src, dest+"/")
	if err != nil {
		t.Fatalf("archiver.CopyFileWithTar shouldn't throw an error, %s.", err)
	}
	_, err = os.Stat(dest)
	if err != nil {
		t.Fatalf("Destination folder should contain the source file but did not.")
	}
}

func TestTarFiles(t *testing.T) {
	// TODO Windows: Figure out how to port this test.
	if runtime.GOOS == windows {
		t.Skip("Failing on Windows")
	}
	// try without hardlinks
	if err := checkNoChanges(t, 1000, false); err != nil {
		t.Fatal(err)
	}
	// try with hardlinks
	if err := checkNoChanges(t, 1000, true); err != nil {
		t.Fatal(err)
	}
}

func checkNoChanges(t *testing.T, fileNum int, hardlinks bool) error {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	_, err := prepareUntarSourceDirectory(fileNum, srcDir, hardlinks)
	if err != nil {
		return err
	}

	err = defaultTarUntar(srcDir, destDir)
	if err != nil {
		return err
	}

	changes, err := ChangesDirs(destDir, &idtools.IDMappings{}, srcDir, &idtools.IDMappings{})
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		return fmt.Errorf("with %d files and %v hardlinks: expected 0 changes, got %d", fileNum, hardlinks, len(changes))
	}
	return nil
}

func tarUntar(t *testing.T, origin string, options *TarOptions) ([]Change, error) {
	archive, err := TarWithOptions(origin, options)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()

	buf := make([]byte, 10)
	if _, err := archive.Read(buf); err != nil {
		return nil, err
	}
	wrap := io.MultiReader(bytes.NewReader(buf), archive)

	detectedCompression := DetectCompression(buf)
	compression := options.Compression
	if detectedCompression.Extension() != compression.Extension() {
		return nil, fmt.Errorf("wrong compression detected. Actual compression: %s, found %s", compression.Extension(), detectedCompression.Extension())
	}

	tmp := t.TempDir()
	if err := Untar(wrap, tmp, nil); err != nil {
		return nil, err
	}
	if _, err := os.Stat(tmp); err != nil {
		return nil, err
	}

	return ChangesDirs(tmp, &idtools.IDMappings{}, origin, &idtools.IDMappings{})
}

func TestTarUntar(t *testing.T) {
	// TODO Windows: Figure out how to fix this test.
	if runtime.GOOS == windows {
		t.Skip("Failing on Windows")
	}
	origin := t.TempDir()
	if err := os.WriteFile(filepath.Join(origin, "1"), []byte("hello world"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "2"), []byte("welcome!"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "3"), []byte("will be ignored"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, c := range []Compression{
		Uncompressed,
		Gzip,
	} {
		changes, err := tarUntar(t, origin, &TarOptions{
			Compression:     c,
			ExcludePatterns: []string{"3"},
		})
		if err != nil {
			t.Fatalf("Error tar/untar for compression %s: %s", c.Extension(), err)
		}

		if len(changes) != 1 || changes[0].Path != "/3" {
			t.Fatalf("Unexpected differences after tarUntar: %v", changes)
		}
	}
}

func TestTarWithOptionsChownOptsAlwaysOverridesIdPair(t *testing.T) {
	origin := t.TempDir()
	filePath := filepath.Join(origin, "1")
	err := os.WriteFile(filePath, []byte("hello world"), 0o700)
	require.NoError(t, err)

	idMaps := []idtools.IDMap{
		0: {
			ContainerID: 0,
			HostID:      0,
			Size:        65536,
		},
		1: {
			ContainerID: 0,
			HostID:      100000,
			Size:        65536,
		},
	}

	cases := []struct {
		opts        *TarOptions
		expectedUID int
		expectedGID int
	}{
		{&TarOptions{ChownOpts: &idtools.IDPair{UID: 1337, GID: 42}}, 1337, 42},
		{&TarOptions{ChownOpts: &idtools.IDPair{UID: 100001, GID: 100001}, UIDMaps: idMaps, GIDMaps: idMaps}, 100001, 100001},
		{&TarOptions{ChownOpts: &idtools.IDPair{UID: 0, GID: 0}, NoLchown: false}, 0, 0},
		{&TarOptions{ChownOpts: &idtools.IDPair{UID: 1, GID: 1}, NoLchown: true}, 1, 1},
		{&TarOptions{ChownOpts: &idtools.IDPair{UID: 1000, GID: 1000}, NoLchown: true}, 1000, 1000},
	}
	for _, testCase := range cases {
		reader, err := TarWithOptions(filePath, testCase.opts)
		require.NoError(t, err)
		tr := tar.NewReader(reader)
		defer reader.Close()
		hdr, err := tr.Next()
		require.NoError(t, err)
		assert.Equal(t, hdr.Name, "1")
		assert.Equal(t, hdr.Uid, testCase.expectedUID, "Uid equals expected value")
		assert.Equal(t, hdr.Gid, testCase.expectedGID, "Gid equals expected value")
		_, err = tr.Next()
		assert.Equal(t, io.EOF, err)
	}
}

func TestTarWithOptions(t *testing.T) {
	// TODO Windows: Figure out how to fix this test.
	if runtime.GOOS == windows {
		t.Skip("Failing on Windows")
	}
	origin := t.TempDir()
	populateDir(t, origin, time.Now(), []sampleData{
		{Dir, "folder", "", 0o755},
		{Regular, "1", "hello world", 0o700},
		{Regular, "2", "welcome!", 0o700},
		{Dir, "folder2", "", 0o700},
		{Regular, "folder2/1", "in folder2", 0o700},
		{Dir, "folder2/subfolder", "", 0o700},
		{Regular, "folder2/subfolder/sub", "in subfolder", 0o700},
		{Dir, "folder2/subfolder2", "", 0o700},
	})

	cases := []struct {
		opts    *TarOptions
		changes []Change
	}{
		{&TarOptions{IncludeFiles: []string{"."}}, []Change{}},
		{&TarOptions{IncludeFiles: []string{"./"}}, []Change{}},
		{&TarOptions{IncludeFiles: []string{"/"}}, []Change{}},
		{&TarOptions{IncludeFiles: []string{"/."}}, []Change{}},
		{&TarOptions{IncludeFiles: []string{"1"}}, []Change{
			{Path: "/2", Kind: ChangeDelete},
			{Path: "/folder", Kind: ChangeDelete},
			{Path: "/folder2", Kind: ChangeDelete},
		}},
		{&TarOptions{IncludeFiles: []string{"/1"}}, []Change{
			{Path: "/2", Kind: ChangeDelete},
			{Path: "/folder", Kind: ChangeDelete},
			{Path: "/folder2", Kind: ChangeDelete},
		}},
		{&TarOptions{ExcludePatterns: []string{"2"}}, []Change{
			{Path: "/2", Kind: ChangeDelete},
		}},
		{&TarOptions{ExcludePatterns: []string{"1", "folder*"}}, []Change{
			{Path: "/1", Kind: ChangeDelete},
			{Path: "/folder", Kind: ChangeDelete},
			{Path: "/folder2", Kind: ChangeDelete},
		}},
		{&TarOptions{IncludeFiles: []string{"1", "1"}}, []Change{
			{Path: "/2", Kind: ChangeDelete},
			{Path: "/folder", Kind: ChangeDelete},
			{Path: "/folder2", Kind: ChangeDelete},
		}},
		{&TarOptions{IncludeFiles: []string{"1"}, RebaseNames: map[string]string{"1": "test"}}, []Change{
			{Path: "/1", Kind: ChangeDelete},
			{Path: "/2", Kind: ChangeDelete},
			{Path: "/folder", Kind: ChangeDelete},
			{Path: "/test", Kind: ChangeAdd},
			{Path: "/folder2", Kind: ChangeDelete},
		}},
		{&TarOptions{ExcludePatterns: []string{"folder2", "!folder2/subfolder"}}, []Change{
			{Path: "/folder2", Kind: ChangeModify}, // folder2 is excluded, but Untar must create the parent — so it creates it using default values
			{Path: "/folder2/1", Kind: ChangeDelete},
			{Path: "/folder2/subfolder2", Kind: ChangeDelete},
		}},
	}
	for _, testCase := range cases {
		changes, err := tarUntar(t, origin, testCase.opts)
		if err != nil {
			t.Fatalf("Error tar/untar when testing inclusion/exclusion: %s", err)
		}
		assert.ElementsMatch(t, testCase.changes, changes)
	}
}

func TestExtractTarFileEntry(t *testing.T) {
	regularContents := []byte("contents")
	mtime := time.Unix(1, 0)
	atime := time.Unix(2, 0)
	hdrEditor := func(hdr *tar.Header) {
		hdr.Uid = os.Getuid()
		hdr.Gid = os.Getgid()
		hdr.ModTime = mtime
		hdr.AccessTime = atime
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeDir { // Linux restricts user.* xattrs to regular files, directories (and sockets?!)
			hdr.PAXRecords = map[string]string{
				PaxSchilyXattr + "user.test": "helloWord",
			}
		}
	}

	// Wrap the tested extractTarFileEntry so that we don't need to repeat all those parameters all the time
	buffer := make([]byte, 1<<20)
	etfe := func(path string, extractDir string, hdr *tar.Header, reader io.Reader) error {
		return extractTarFileEntry(path, extractDir, hdr, reader, true, nil, false, false, nil, buffer)
	}

	symlinkVictim := filepath.Join(t.TempDir(), "symlink_victim")
	err := os.WriteFile(symlinkVictim, []byte("symlink_victim"), 0o600)
	require.NoError(t, err)
	originalSymlinkVictimInfo, err := os.Lstat(symlinkVictim)
	require.NoError(t, err)

	// Success creating various kinds of files
	for _, c := range []struct {
		hdr          tar.Header
		expectedType fs.FileMode
		assertion    func(path string)
	}{
		{
			hdr:          tar.Header{Typeflag: tar.TypeDir, Mode: 0o700},
			expectedType: fs.ModeDir,
			assertion:    func(path string) {},
		},
		{
			hdr:          tar.Header{Typeflag: tar.TypeReg, Mode: 0o600},
			expectedType: 0,
			assertion: func(path string) {
				fi, err := os.Lstat(path)
				require.NoError(t, err)
				assert.Equal(t, int64(len(regularContents)), fi.Size())
				contents, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, regularContents, contents)
			},
		},
		// tar.TypeBlock, tar.typeChar untested because they require root privileges
		{
			hdr:          tar.Header{Typeflag: tar.TypeFifo, Mode: 0o600},
			expectedType: fs.ModeNamedPipe,
			assertion:    func(path string) {},
		},
		// tar.TypeLink is tested below separately
		{
			hdr:          tar.Header{Typeflag: tar.TypeSymlink, Linkname: "dangling/local/target", Mode: 0o700},
			expectedType: fs.ModeSymlink,
			assertion: func(path string) {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				assert.Equal(t, "dangling/local/target", link)
			},
		},
		{
			hdr:          tar.Header{Typeflag: tar.TypeSymlink, Linkname: "/dangling/absolute/target", Mode: 0o700},
			expectedType: fs.ModeSymlink,
			assertion: func(path string) {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				assert.Equal(t, "/dangling/absolute/target", link)
			},
		},
		{
			hdr:          tar.Header{Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd/arbitrary/escaping/symlink", Mode: 0o700},
			expectedType: fs.ModeSymlink,
			assertion: func(path string) {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				assert.Equal(t, "../../etc/passwd/arbitrary/escaping/symlink", link)
			},
		},
		{ // Even if we create a symlink to an existing file, we don't touch the target at all.
			hdr:          tar.Header{Typeflag: tar.TypeSymlink, Linkname: symlinkVictim, Mode: 0o700},
			expectedType: fs.ModeSymlink,
			assertion: func(path string) {
				link, err := os.Readlink(path)
				require.NoError(t, err)
				assert.Equal(t, symlinkVictim, link)
			},
		},
	} {
		extractDir := t.TempDir()
		hdr := c.hdr
		hdrEditor(&hdr)
		targetPath := filepath.Join(extractDir, "target")
		err := etfe(targetPath, extractDir, &hdr, bytes.NewReader(regularContents))
		require.NoError(t, err)
		fi, err := os.Lstat(targetPath)
		require.NoError(t, err)
		assert.Equal(t, c.expectedType, fi.Mode().Type())
		c.assertion(targetPath)
		// Keep the property tests in sync with the hardlink code below.

		// We don't substantially test Uid/Gid because that requires root privileges.
		if hdr.Typeflag != tar.TypeSymlink || runtime.GOOS != "linux" {
			assert.Equal(t, fs.FileMode(hdr.Mode), fi.Mode().Perm())
		}
		assert.Equal(t, mtime, fi.ModTime())
		assertAtime(t, atime, fi)
		xattr, err := system.Lgetxattr(targetPath, "user.test")
		require.NoError(t, err)
		if hdr.PAXRecords != nil {
			assert.Equal(t, []byte("helloWord"), xattr)
		} else {
			assert.Nil(t, xattr)
		}
		// We don’t test forceMask
		// We don’t test BSD flags
	}
	// symlinkVictim was not affected
	updatedSymlinkVictimInfo, err := os.Lstat(symlinkVictim)
	require.NoError(t, err)
	assertCtimeMatches(t, originalSymlinkVictimInfo, updatedSymlinkVictimInfo)

	// Success creating hard links
	extractDir := t.TempDir()
	err = os.WriteFile(filepath.Join(extractDir, "regular_target"), []byte("regular_target"), 0o600)
	require.NoError(t, err)
	danglingSymlinkTarget := filepath.Join(extractDir, "this_is_a_dangling_symlink")
	err = os.Symlink(danglingSymlinkTarget, filepath.Join(extractDir, "symlink_target"))
	require.NoError(t, err)
	for _, c := range []struct {
		target    string
		isSymlink bool
	}{
		{target: "regular_target"},
		{target: "symlink_target", isSymlink: true},
	} {
		targetPath := filepath.Join(extractDir, c.target)
		targetInfo, err := os.Lstat(targetPath)
		require.NoError(t, err)
		linkPath := filepath.Join(extractDir, c.target+"-link")
		hdr := tar.Header{Typeflag: tar.TypeLink, Linkname: c.target, Mode: 0o600}
		hdrEditor(&hdr)
		err = etfe(linkPath, extractDir, &hdr, nil)
		require.NoError(t, err)

		linkInfo, err := os.Lstat(linkPath)
		require.NoError(t, err)
		assertSameFile(t, targetInfo, linkInfo)
		// Keep the property tests in sync with the non-hardlink code above.

		// We don't substantially test Uid/Gid because that requires root privileges.
		if !c.isSymlink || runtime.GOOS != "linux" {
			assert.Equal(t, fs.FileMode(hdr.Mode), linkInfo.Mode().Perm())
		}
		if !c.isSymlink { // We don’t change times if the target is a symlink.
			assert.Equal(t, mtime, linkInfo.ModTime())
			assertAtime(t, atime, linkInfo)
		}
		xattr, err := system.Lgetxattr(linkPath, "user.test")
		require.NoError(t, err)
		if hdr.PAXRecords != nil {
			assert.Equal(t, []byte("helloWord"), xattr)
		} else {
			assert.Nil(t, xattr)
		}
		// We don’t test forceMask
		// We don’t test BSD flags
	}

	// Hard link targets are constrained to extractDir.
	for _, c := range []struct {
		symlinks [][2]string // (name -> target)
		linkName string
	}{
		{symlinks: [][2]string{{"symlink", "unused"}}, linkName: "../victimDir/victim"},
		{symlinks: [][2]string{{"symlink", "unused"}}, linkName: "TOP/victimDir/victim"},
		{symlinks: [][2]string{{"symlink", "../victimDir"}}, linkName: "symlink/victim"},
		{symlinks: [][2]string{{"symlink", "TOP/victimDir"}}, linkName: "symlink/victim"},
		{symlinks: [][2]string{{"symlink", "../victimDir/victim"}}, linkName: "symlink"},
		{symlinks: [][2]string{{"symlink", "TOP/victimDir/victim"}}, linkName: "symlink"},
		{ // symlink targets that do not individually escape, when looking purely at the syntax
			symlinks: [][2]string{
				{"a/b/c", "../.."}, // Points at the root
				{"a/b/c/d", ".."},  // = root/..
			},
			linkName: "a/b/c/d/victimDir/victim",
		},
	} {
		t.Run(fmt.Sprintf("%q|%s", c.symlinks, c.linkName), func(t *testing.T) {
			topDir := t.TempDir()
			extractDir = filepath.Join(topDir, "extractDir")
			err = os.Mkdir(extractDir, 0o700)
			require.NoError(t, err)
			victimDir := filepath.Join(topDir, "victimDir")
			err = os.Mkdir(victimDir, 0o700)
			require.NoError(t, err)
			victimPath := filepath.Join(victimDir, "victim")
			err = os.WriteFile(victimPath, []byte("victim"), 0o600)
			require.NoError(t, err)

			for _, symlink := range c.symlinks {
				symlinkPath := filepath.Join(extractDir, symlink[0])
				target := strings.Replace(symlink[1], "TOP", topDir, 1)
				err := os.MkdirAll(filepath.Dir(symlinkPath), 0o700)
				require.NoError(t, err)
				err = os.Symlink(target, symlinkPath)
				require.NoError(t, err)
			}
			hdr := tar.Header{Typeflag: tar.TypeLink, Linkname: strings.Replace(c.linkName, "TOP", topDir, 1), Mode: 0o600}
			hdrEditor(&hdr)
			hardlinkPath := filepath.Join(extractDir, "link")
			// Either creating the hard link must fail …
			err = etfe(hardlinkPath, extractDir, &hdr, nil)
			if err == nil {
				// … or it must be a hard link to our symlink, not to the victim.
				linkInfo, err := os.Lstat(hardlinkPath)
				require.NoError(t, err)
				assert.Equal(t, fs.ModeSymlink, linkInfo.Mode().Type())
			}
		})
	}

	// Some tar archives such as http://haproxy.1wt.eu/download/1.5/src/devel/haproxy-1.5-dev21.tar.gz
	// use PAX Global Extended Headers.
	// Failing prevents the archives from being uncompressed during ADD
	extractDir = t.TempDir()
	err = etfe(filepath.Join(extractDir, "pax_global_header"), extractDir, &tar.Header{
		Typeflag: tar.TypeXGlobalHeader,
	}, nil)
	assert.NoError(t, err)
}

// Some tar have both GNU specific (huge uid) and Ustar specific (long name) things.
// Not supposed to happen (should use PAX instead of Ustar for long name) but it does and it should still work.
func TestUntarUstarGnuConflict(t *testing.T) {
	f, err := os.Open("testdata/broken.tar")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	found := false
	tr := tar.NewReader(f)
	// Iterate through the files in the archive.
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			// end of tar archive
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "root/.cpanm/work/1395823785.24209/Plack-1.0030/blib/man3/Plack::Middleware::LighttpdScriptNameFix.3pm" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s not found in the archive", "root/.cpanm/work/1395823785.24209/Plack-1.0030/blib/man3/Plack::Middleware::LighttpdScriptNameFix.3pm")
	}
}

func prepareUntarSourceDirectory(numberOfFiles int, targetPath string, makeLinks bool) (int, error) {
	fileData := []byte("fooo")
	for n := range numberOfFiles {
		fileName := fmt.Sprintf("file-%d", n)
		if err := os.WriteFile(filepath.Join(targetPath, fileName), fileData, 0o700); err != nil {
			return 0, err
		}
		if makeLinks {
			if err := os.Link(filepath.Join(targetPath, fileName), filepath.Join(targetPath, fileName+"-link")); err != nil {
				return 0, err
			}
		}
	}
	totalSize := numberOfFiles * len(fileData)
	return totalSize, nil
}

func BenchmarkTarUntar(b *testing.B) {
	origin := b.TempDir()
	tempDir := b.TempDir()
	target := filepath.Join(tempDir, "dest")
	n, err := prepareUntarSourceDirectory(100, origin, false)
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(n))
	for b.Loop() {
		err := defaultTarUntar(origin, target)
		if err != nil {
			b.Fatal(err)
		}
		os.RemoveAll(target)
	}
}

func BenchmarkTarUntarWithLinks(b *testing.B) {
	origin := b.TempDir()
	tempDir := b.TempDir()
	target := filepath.Join(tempDir, "dest")
	n, err := prepareUntarSourceDirectory(100, origin, true)
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(n))
	for b.Loop() {
		err := defaultTarUntar(origin, target)
		if err != nil {
			b.Fatal(err)
		}
		os.RemoveAll(target)
	}
}

func TestUntarSelinuxLabel(t *testing.T) {
	xattrs := map[string]string{
		"SCHILY.xattr.security.selinux": "invalid-label",
	}
	for i, headers := range [][]*tar.Header{
		{
			{
				Name:       "foo",
				Typeflag:   tar.TypeReg,
				Mode:       0o644,
				PAXRecords: xattrs,
			},
		},
	} {
		if err := testBreakout(t, breakoutUntar, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestUntarInvalidFilenames(t *testing.T) {
	// TODO Windows: Figure out how to fix this test.
	if runtime.GOOS == windows {
		t.Skip("Passes but hits breakoutError: platform and architecture is not supported")
	}
	for i, headers := range [][]*tar.Header{
		{
			{
				Name:     "../victim/dotdot",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{
			{
				// Note the leading slash
				Name:     "/../victim/slash-dotdot",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutUntar, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestUntarHardlinkToSymlink(t *testing.T) {
	// TODO Windows. There may be a way of running this, but turning off for now
	if runtime.GOOS == windows {
		t.Skip("hardlinks on Windows")
	}
	for i, headers := range [][]*tar.Header{
		{
			{
				Name:     "symlink1",
				Typeflag: tar.TypeSymlink,
				Linkname: "regfile",
				Mode:     0o644,
			},
			{
				Name:     "symlink2",
				Typeflag: tar.TypeLink,
				Linkname: "symlink1",
				Mode:     0o644,
			},
			{
				Name:     "regfile",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutUntar, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestUntarInvalidHardlink(t *testing.T) {
	// TODO Windows. There may be a way of running this, but turning off for now
	if runtime.GOOS == windows {
		t.Skip("hardlinks on Windows")
	}
	for i, headers := range [][]*tar.Header{
		{ // try reading victim/hello (../)
			{
				Name:     "dotdot",
				Typeflag: tar.TypeLink,
				Linkname: "../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (/../)
			{
				Name:     "slash-dotdot",
				Typeflag: tar.TypeLink,
				// Note the leading slash
				Linkname: "/../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try writing victim/file
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim/file",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (hardlink, symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "symlink",
				Typeflag: tar.TypeSymlink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // Try reading victim/hello (hardlink, hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "hardlink",
				Typeflag: tar.TypeLink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // Try removing victim directory (hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeLink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutUntar, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestUntarInvalidSymlink(t *testing.T) {
	// TODO Windows. There may be a way of running this, but turning off for now
	if runtime.GOOS == windows {
		t.Skip("hardlinks on Windows")
	}
	for i, headers := range [][]*tar.Header{
		{ // try reading victim/hello (../)
			{
				Name:     "dotdot",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (/../)
			{
				Name:     "slash-dotdot",
				Typeflag: tar.TypeSymlink,
				// Note the leading slash
				Linkname: "/../victim/hello",
				Mode:     0o644,
			},
		},
		{ // try writing victim/file
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim/file",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (symlink, symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "symlink",
				Typeflag: tar.TypeSymlink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // try reading victim/hello (symlink, hardlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "hardlink",
				Typeflag: tar.TypeLink,
				Linkname: "loophole-victim/hello",
				Mode:     0o644,
			},
		},
		{ // try removing victim directory (symlink)
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeSymlink,
				Linkname: "../victim",
				Mode:     0o755,
			},
			{
				Name:     "loophole-victim",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
		{ // try writing to victim/newdir/newfile with a symlink in the path
			{
				// this header needs to be before the next one, or else there is an error
				Name:     "dir/loophole",
				Typeflag: tar.TypeSymlink,
				Linkname: "../../victim",
				Mode:     0o755,
			},
			{
				Name:     "dir/loophole/newdir/newfile",
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			},
		},
	} {
		if err := testBreakout(t, breakoutUntar, headers); err != nil {
			t.Fatalf("i=%d. %v", i, err)
		}
	}
}

func TestTempArchiveCloseMultipleTimes(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("hello"))
	tempArchive, err := NewTempArchive(reader, "")
	require.NoError(t, err)
	buf := make([]byte, 10)
	n, err := tempArchive.Read(buf)
	require.NoError(t, err)
	if n != 5 {
		t.Fatalf("Expected to read 5 bytes. Read %d instead", n)
	}
	for i := range 3 {
		if err = tempArchive.Close(); err != nil {
			t.Fatalf("i=%d. Unexpected error closing temp archive: %v", i, err)
		}
	}
}

func TestReplaceFileTarWrapper(t *testing.T) {
	filesInArchive := 20
	testcases := []struct {
		doc       string
		filename  string
		modifier  TarModifierFunc
		expected  string
		fileCount int
	}{
		{
			doc:       "Modifier creates a new file",
			filename:  "newfile",
			modifier:  createModifier(t),
			expected:  "the new content",
			fileCount: filesInArchive + 1,
		},
		{
			doc:       "Modifier replaces a file",
			filename:  "file-2",
			modifier:  createOrReplaceModifier,
			expected:  "the new content",
			fileCount: filesInArchive,
		},
		{
			doc:       "Modifier replaces the last file",
			filename:  fmt.Sprintf("file-%d", filesInArchive-1),
			modifier:  createOrReplaceModifier,
			expected:  "the new content",
			fileCount: filesInArchive,
		},
		{
			doc:       "Modifier appends to a file",
			filename:  "file-3",
			modifier:  appendModifier,
			expected:  "fooo\nnext line",
			fileCount: filesInArchive,
		},
	}

	for _, testcase := range testcases {
		sourceArchive, cleanup := buildSourceArchive(t, filesInArchive)
		defer cleanup()

		resultArchive := ReplaceFileTarWrapper(
			sourceArchive,
			map[string]TarModifierFunc{testcase.filename: testcase.modifier})

		actual := readFileFromArchive(t, resultArchive, testcase.filename, testcase.fileCount, testcase.doc)
		assert.Equal(t, testcase.expected, actual, testcase.doc)
	}
}

func buildSourceArchive(t *testing.T, numberOfFiles int) (io.ReadCloser, func()) {
	srcDir := t.TempDir()

	_, err := prepareUntarSourceDirectory(numberOfFiles, srcDir, false)
	require.NoError(t, err)

	sourceArchive, err := TarWithOptions(srcDir, &TarOptions{})
	require.NoError(t, err)
	return sourceArchive, func() {
		sourceArchive.Close()
	}
}

func createOrReplaceModifier(path string, header *tar.Header, content io.Reader) (*tar.Header, []byte, error) {
	return &tar.Header{
		Mode:     0o600,
		Typeflag: tar.TypeReg,
	}, []byte("the new content"), nil
}

func createModifier(t *testing.T) TarModifierFunc {
	return func(path string, header *tar.Header, content io.Reader) (*tar.Header, []byte, error) {
		assert.Nil(t, content)
		return createOrReplaceModifier(path, header, content)
	}
}

func appendModifier(path string, header *tar.Header, content io.Reader) (*tar.Header, []byte, error) {
	buffer := bytes.Buffer{}
	if content != nil {
		if _, err := buffer.ReadFrom(content); err != nil {
			return nil, nil, err
		}
	}
	buffer.WriteString("\nnext line")
	return &tar.Header{Mode: 0o600, Typeflag: tar.TypeReg}, buffer.Bytes(), nil
}

func readFileFromArchive(t *testing.T, archive io.ReadCloser, name string, expectedCount int, doc string) string {
	destDir := t.TempDir()

	err := Untar(archive, destDir, nil)
	require.NoError(t, err)

	files, _ := os.ReadDir(destDir)
	assert.Len(t, files, expectedCount, doc)

	content, err := os.ReadFile(filepath.Join(destDir, name))
	assert.NoError(t, err)
	return string(content)
}

func TestTimestamp(t *testing.T) {
	// write single file into dir that we'll tar
	td := t.TempDir()
	tf := filepath.Join(td, "foo")

	require.NoError(t, os.WriteFile(tf, []byte("bar"), 0o644))

	// helper function to tar that dir and return byte slice
	tarToByteSlice := func(options *TarOptions) []byte {
		rc, err := TarWithOptions(td, options)
		assert.NoError(t, err)
		defer rc.Close()

		rv, err := io.ReadAll(rc)
		assert.NoError(t, err)
		return rv
	}

	// default options
	defaultOptions := &TarOptions{}

	// override timestamp option
	epochOptions := &TarOptions{Timestamp: &time.Time{}}

	// get tar bytes slices now
	origTarDefaultOptions := tarToByteSlice(defaultOptions)
	origTarEpochOptions := tarToByteSlice(epochOptions)

	// set the mod time of the file to an hour later
	oneHourLater := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(tf, oneHourLater, oneHourLater))

	// then tar again
	laterTarDefaultOptions := tarToByteSlice(defaultOptions)
	laterTarEpochOptions := tarToByteSlice(epochOptions)

	// we expect the ones without a fixed timestamp to be different
	assert.NotEqual(t, origTarDefaultOptions, laterTarDefaultOptions)

	// we expect the ones with a fixed timestamp to be the same
	assert.Equal(t, origTarEpochOptions, laterTarEpochOptions)
}

type errorBuf struct {
	bytes.Buffer

	err        error
	failWrite  int
	writeCount int
}

func (b *errorBuf) Write(d []byte) (int, error) {
	b.writeCount++
	if b.failWrite == b.writeCount {
		return 0, b.err
	}
	return b.Buffer.Write(d)
}

func TestTarErrorHandling(t *testing.T) {
	dir := t.TempDir()

	for i := range 10 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d", i)), bytes.Repeat([]byte("hello"), 32<<10),
			0o700); err != nil {
			t.Fatal(err)
		}
	}

	dest := &errorBuf{
		failWrite: 1,
		err:       errors.New("boom"),
	}

	if err := tarWithOptionsTo(dest, dir, &TarOptions{
		Compression: Uncompressed,
	}); !errors.Is(err, dest.err) {
		t.Fatalf("Did not propagate error; got %v", err)
	}
}

// makeVfsCap builds a "security.capability" value of the given revision. When
// rootid is >= 0 a v3 value (24 bytes) is produced, otherwise a v2 value (20
// bytes). The permitted/inheritable sets and effective flag are stored so the
// test can check they survive normalization.
func makeVfsCap(rootid int) []byte {
	magic := uint32(vfsCapRevision2)
	size := vfsCapDataSizeV2
	if rootid >= 0 {
		magic = vfsCapRevision3
		size = vfsCapDataSizeV3
	}
	magic |= 0x01 // VFS_CAP_FLAGS_EFFECTIVE
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b[0:4], magic)
	binary.LittleEndian.PutUint32(b[4:8], 0x000001ff)   // permitted, low word
	binary.LittleEndian.PutUint32(b[12:16], 0x000001ff) // inheritable, low word
	if rootid >= 0 {
		binary.LittleEndian.PutUint32(b[vfsCapRootIDOffset:], uint32(rootid))
	}
	return b
}

func TestNormalizeCapabilityRootID(t *testing.T) {
	// container 0..65535 maps to host 1000000..1065535
	mappings := idtools.NewIDMappingsFromMaps(
		[]idtools.IDMap{{ContainerID: 0, HostID: 1000000, Size: 65536}},
		[]idtools.IDMap{{ContainerID: 0, HostID: 1000000, Size: 65536}},
	)

	t.Run("v3 owned by container root (host id 1000000) is downgraded to v2 and keeps its flags", func(t *testing.T) {
		out, err := normalizeCapabilityRootID(mappings, makeVfsCap(1000000))
		require.NoError(t, err)
		require.Len(t, out, vfsCapDataSizeV2)
		magic := binary.LittleEndian.Uint32(out[0:4])
		assert.Equal(t, uint32(vfsCapRevision2), magic&vfsCapRevisionMask)
		assert.Equal(t, uint32(0x01), magic&0x01, "effective flag preserved")
		assert.Equal(t, uint32(0x000001ff), binary.LittleEndian.Uint32(out[4:8]), "permitted set preserved")
		assert.Equal(t, uint32(0x000001ff), binary.LittleEndian.Uint32(out[12:16]), "inheritable set preserved")
	})

	t.Run("v3 owned by a non-root container id keeps v3 with the container rootid", func(t *testing.T) {
		out, err := normalizeCapabilityRootID(mappings, makeVfsCap(1000123))
		require.NoError(t, err)
		require.Len(t, out, vfsCapDataSizeV3)
		assert.Equal(t, uint32(vfsCapRevision3), binary.LittleEndian.Uint32(out[0:4])&vfsCapRevisionMask)
		assert.Equal(t, uint32(123), binary.LittleEndian.Uint32(out[vfsCapRootIDOffset:]))
	})

	t.Run("v3 with an unmapped rootid is an error", func(t *testing.T) {
		_, err := normalizeCapabilityRootID(mappings, makeVfsCap(5))
		assert.Error(t, err)
	})

	t.Run("v2 value is returned unchanged", func(t *testing.T) {
		in := makeVfsCap(-1)
		out, err := normalizeCapabilityRootID(mappings, in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	})

	t.Run("empty mappings return the value unchanged", func(t *testing.T) {
		in := makeVfsCap(1000000)
		out, err := normalizeCapabilityRootID(&idtools.IDMappings{}, in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
		out, err = normalizeCapabilityRootID(nil, in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	})

	t.Run("malformed value is returned unchanged", func(t *testing.T) {
		in := []byte{0x00, 0x01, 0x02}
		out, err := normalizeCapabilityRootID(mappings, in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	})
}
