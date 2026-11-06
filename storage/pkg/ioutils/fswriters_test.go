package ioutils

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testMode os.FileMode = 0o640

func init() {
	// Windows does not support full Linux file mode
	if runtime.GOOS == "windows" {
		testMode = 0o666
	}
}

func TestAtomicWriteToFile(t *testing.T) {
	tmpDir := t.TempDir()

	expected := []byte("barbaz")
	require.NoError(t, AtomicWriteFile(filepath.Join(tmpDir, "foo"), expected, testMode))

	actual, err := os.ReadFile(filepath.Join(tmpDir, "foo"))
	require.NoError(t, err)
	assert.Equal(t, expected, actual)

	st, err := os.Stat(filepath.Join(tmpDir, "foo"))
	require.NoError(t, err)
	assert.Equal(t, testMode, st.Mode())
}

// TestAtomicWriteFileWithOpenReader replaces a file while a reader holds it
// open. The reader must keep seeing the old content, and new readers must
// see the new content. On Windows the reader has to allow FILE_SHARE_DELETE.
func TestAtomicWriteFileWithOpenReader(t *testing.T) {
	fileName := filepath.Join(t.TempDir(), "foo")
	old := []byte("old")
	require.NoError(t, os.WriteFile(fileName, old, testMode))

	reader, err := openShareDelete(fileName)
	require.NoError(t, err)
	defer reader.Close()

	expected := []byte("new")
	require.NoError(t, AtomicWriteFile(fileName, expected, testMode), "writing to file while a reader has it open")

	actual, err := os.ReadFile(fileName)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)

	actual, err = io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, old, actual, "open reader should still see the old content")
}

func TestAtomicCommitAndRollbackFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "foo")

	oldData := "olddata"
	newData := "newdata"

	check := func(n int, initData string, writeData string, expected string, explicit bool, commit bool) {
		require.NoError(t, os.WriteFile(path, []byte(initData), 0o644), "(%d)", n)

		opts := &AtomicFileWriterOptions{ExplicitCommit: explicit}
		w, err := NewAtomicFileWriterWithOpts(filepath.Join(tmpDir, "foo"), 0o644, opts)
		require.NoError(t, err, "(%d)", n)

		_, err = w.Write([]byte(writeData))
		require.NoError(t, err, "(%d)", n)

		if commit {
			require.NoError(t, w.Commit(), "(%d)", n)
		}

		require.NoError(t, w.Close(), "(%d)", n)

		actual, err := os.ReadFile(filepath.Join(tmpDir, "foo"))
		require.NoError(t, err, "(%d)", n)
		assert.Equal(t, expected, string(actual), "(%d)", n)
	}

	check(1, oldData, newData, oldData, true, false)
	check(2, oldData, newData, newData, true, true)
	check(3, oldData, newData, newData, false, false)
	check(4, oldData, newData, newData, false, true)
}

func TestAtomicWriteSetCommit(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "tmp"), 0o700))

	targetDir := filepath.Join(tmpDir, "target")
	ws, err := NewAtomicWriteSet(filepath.Join(tmpDir, "tmp"))
	require.NoError(t, err)

	expected := []byte("barbaz")
	require.NoError(t, ws.WriteFile("foo", expected, testMode))

	_, err = os.ReadFile(filepath.Join(targetDir, "foo"))
	require.Error(t, err, "file should not exist before commit")

	require.NoError(t, ws.Commit(targetDir))

	actual, err := os.ReadFile(filepath.Join(targetDir, "foo"))
	require.NoError(t, err)
	assert.Equal(t, expected, actual)

	st, err := os.Stat(filepath.Join(targetDir, "foo"))
	require.NoError(t, err)
	assert.Equal(t, testMode, st.Mode())
}

func TestAtomicWriteSetCancel(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "tmp"), 0o700))

	ws, err := NewAtomicWriteSet(filepath.Join(tmpDir, "tmp"))
	require.NoError(t, err)

	require.NoError(t, ws.WriteFile("foo", []byte("barbaz"), testMode))
	require.NoError(t, ws.Cancel())

	_, err = os.ReadFile(filepath.Join(tmpDir, "target", "foo"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
