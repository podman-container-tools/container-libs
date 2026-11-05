//go:build windows

package ioutils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	if err := os.WriteFile(fileName, []byte("old"), testMode); err != nil {
		t.Fatalf("Error writing to file: %v", err)
	}

	reader, err := os.Open(fileName)
	if err != nil {
		t.Fatalf("Error opening file: %v", err)
	}
	defer reader.Close()

	err = AtomicWriteFile(fileName, []byte("new"), testMode)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("Expected a sharing violation, got: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(fileName), ".tmp-*")); len(matches) != 0 {
		t.Fatalf("Temporary files left behind: %v", matches)
	}
}
