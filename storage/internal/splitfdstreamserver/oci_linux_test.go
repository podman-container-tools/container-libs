//go:build linux

package splitfdstreamserver

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestOpenStreamFile checks that streams are written to the store's file
// system without leaving anything behind.
func TestOpenStreamFile(t *testing.T) {
	tmpDir := t.TempDir()
	f, err := openStreamFile(tmpDir)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.WriteString("stream")
	require.NoError(t, err)

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, entries)

	var st unix.Statfs_t
	require.NoError(t, unix.Fstatfs(int(f.Fd()), &st))
	var dirSt unix.Statfs_t
	require.NoError(t, unix.Statfs(tmpDir, &dirSt))
	assert.Equal(t, dirSt.Fsid, st.Fsid, "the stream is not on the store's file system")
}
