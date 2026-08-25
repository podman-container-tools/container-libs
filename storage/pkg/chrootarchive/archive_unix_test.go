//go:build !windows

package chrootarchive

import (
	gotar "archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/archive"
	"golang.org/x/sys/unix"
)

// Test for CVE-2018-15664
// Assures that in the case where an "attacker" controlled path is a symlink to
// some path outside of a container's rootfs that we do not copy data to a
// container path that will actually overwrite data on the host
func TestUntarWithMaliciousSymlinks(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")

	err := os.MkdirAll(root, 0o755)
	require.NoError(t, err)

	// Add a file into a directory above root
	// Ensure that we can't access this file while tarring.
	err = os.WriteFile(filepath.Join(dir, "host-file"), []byte("I am a host file"), 0o644)
	require.NoError(t, err)

	// Create some data which will be copied into the "container" root into
	// the symlinked path.
	// Before this change, the copy would overwrite the "host" content.
	// With this change it should not.
	data := filepath.Join(dir, "data")
	err = os.MkdirAll(data, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(data, "local-file"), []byte("pwn3d"), 0o644)
	require.NoError(t, err)

	safe := filepath.Join(root, "safe")
	err = unix.Symlink(dir, safe)
	require.NoError(t, err)

	rdr, err := archive.TarWithOptions(data, &archive.TarOptions{IncludeFiles: []string{"local-file"}, RebaseNames: map[string]string{"local-file": "host-file"}})
	require.NoError(t, err)

	// Use tee to test both the good case and the bad case w/o recreating the archive
	bufRdr := bytes.NewBuffer(nil)
	tee := io.TeeReader(rdr, bufRdr)

	err = UntarWithRoot(tee, safe, nil, root)
	assert.ErrorContains(t, err, "open /safe/host-file: no such file or directory")

	// Make sure the "host" file is still in tact
	// Before the fix the host file would be overwritten
	hostData, err := os.ReadFile(filepath.Join(dir, "host-file"))
	require.NoError(t, err)
	assert.Equal(t, "I am a host file", string(hostData))

	// Now test by chrooting to an attacker controlled path
	// This should succeed as is and overwrite a "host" file
	// Note that this would be a mis-use of this function.
	err = UntarWithRoot(bufRdr, safe, nil, safe)
	require.NoError(t, err)

	hostData, err = os.ReadFile(filepath.Join(dir, "host-file"))
	require.NoError(t, err)
	assert.Equal(t, "pwn3d", string(hostData))
}

// Test for CVE-2018-15664
// Assures that in the case where an "attacker" controlled path is a symlink to
// some path outside of a container's rootfs that we do not unwittingly leak
// host data into the archive.
func TestTarWithMaliciousSymlinks(t *testing.T) {
	dir := t.TempDir()
	t.Log(dir)

	root := filepath.Join(dir, "root")

	err := os.MkdirAll(root, 0o755)
	require.NoError(t, err)

	hostFileData := []byte("I am a host file")

	// Add a file into a directory above root
	// Ensure that we can't access this file while tarring.
	err = os.WriteFile(filepath.Join(dir, "host-file"), hostFileData, 0o644)
	require.NoError(t, err)

	safe := filepath.Join(root, "safe")
	err = unix.Symlink(dir, safe)
	require.NoError(t, err)

	data := filepath.Join(dir, "data")
	err = os.MkdirAll(data, 0o755)
	require.NoError(t, err)

	type testCase struct {
		p        string
		includes []string
	}

	cases := []testCase{
		{p: safe, includes: []string{"host-file"}},
		{p: safe + "/", includes: []string{"host-file"}},
		{p: safe, includes: nil},
		{p: safe + "/", includes: nil},
		{p: root, includes: []string{"safe/host-file"}},
		{p: root, includes: []string{"/safe/host-file"}},
		{p: root, includes: nil},
	}

	maxBytes := len(hostFileData)

	for _, tc := range cases {
		t.Run(path.Join(tc.p+"_"+strings.Join(tc.includes, "_")), func(t *testing.T) {
			// Here if we use archive.TarWithOptions directly or change the "root" parameter
			// to be the same as "safe", data from the host will be leaked into the archive
			var opts *archive.TarOptions
			if tc.includes != nil {
				opts = &archive.TarOptions{
					IncludeFiles: tc.includes,
				}
			}
			rdr, err := Tar(tc.p, opts, root)
			require.NoError(t, err)
			defer rdr.Close()

			tr := gotar.NewReader(rdr)
			assert.False(t, isDataInTar(t, tr, hostFileData, int64(maxBytes)), "host data leaked to archive")
		})
	}
}

func isDataInTar(t *testing.T, tr *gotar.Reader, compare []byte, maxBytes int64) bool {
	t.Helper()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		if h.Size == 0 {
			continue
		}
		assert.LessOrEqual(t, h.Size, maxBytes, "%s: file size exceeds max expected size %d: %d", h.Name, maxBytes, h.Size)

		data := make([]byte, int(h.Size))
		_, err = io.ReadFull(tr, data)
		require.NoError(t, err)
		if bytes.Contains(data, compare) {
			return true
		}
	}

	return false
}

// slowTarReader feeds a real tar stream in small chunks so the unpack is still
// running when the test removes the destination.
type slowTarReader struct {
	data   []byte
	offset int
	remove func()
	fired  bool
}

func (s *slowTarReader) Read(p []byte) (int, error) {
	if s.offset >= len(s.data) {
		return 0, io.EOF
	}
	// let the first entries land, then take the destination away
	if !s.fired && s.offset > 0 {
		s.fired = true
		s.remove()
	}
	time.Sleep(20 * time.Millisecond)
	n := copy(p, s.data[s.offset:min(s.offset+512, len(s.data))])
	s.offset += n
	return n, nil
}

// A destination that disappears while the untar child is writing into it makes
// every create fail with ENOENT, which looks like a corrupt archive. Check that
// the error names the real cause instead.
func TestUntarDestinationRemovedWhileUnpacking(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test requires root")
	}
	tmpdir := t.TempDir()

	var buf bytes.Buffer
	tw := gotar.NewWriter(&buf)
	body := bytes.Repeat([]byte("x"), 1024)
	for i := range 200 {
		hdr := &gotar.Header{
			Name:     fmt.Sprintf("file-%d", i),
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: gotar.TypeReg,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	dest := filepath.Join(tmpdir, "dest")
	require.NoError(t, os.MkdirAll(dest, 0o700))

	stream := &slowTarReader{
		data:   buf.Bytes(),
		remove: func() { _ = os.RemoveAll(dest) },
	}

	err := Untar(stream, dest, &archive.TarOptions{})
	require.Error(t, err, "unpack into a removed destination should fail")
	assert.ErrorContains(t, err, "destination was removed while unpacking")
}
