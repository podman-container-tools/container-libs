//go:build windows

package ioutils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// openShareDelete opens a file for reading with FILE_SHARE_DELETE, so the
// file can be renamed over while it is open. os.Open doesn't set that flag.
func openShareDelete(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// TestAtomicWriteFileWithOpenReaderNoShareDelete documents that a reader
// opened with os.Open (no FILE_SHARE_DELETE) still blocks the replace.
func TestAtomicWriteFileWithOpenReaderNoShareDelete(t *testing.T) {
	fileName := filepath.Join(t.TempDir(), "foo")
	require.NoError(t, os.WriteFile(fileName, []byte("old"), testMode))

	reader, err := os.Open(fileName)
	require.NoError(t, err)
	defer reader.Close()

	err = AtomicWriteFile(fileName, []byte("new"), testMode)
	assert.ErrorIs(t, err, windows.ERROR_SHARING_VIOLATION)

	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(fileName), ".tmp-*"))
	assert.Empty(t, matches, "temporary files left behind")
}
