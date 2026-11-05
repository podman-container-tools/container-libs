package ioutils

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
	if err := AtomicWriteFile(filepath.Join(tmpDir, "foo"), expected, testMode); err != nil {
		t.Fatalf("Error writing to file: %v", err)
	}

	actual, err := os.ReadFile(filepath.Join(tmpDir, "foo"))
	if err != nil {
		t.Fatalf("Error reading from file: %v", err)
	}

	if !bytes.Equal(actual, expected) {
		t.Fatalf("Data mismatch, expected %q, got %q", expected, actual)
	}

	st, err := os.Stat(filepath.Join(tmpDir, "foo"))
	if err != nil {
		t.Fatalf("Error statting file: %v", err)
	}
	if expected := testMode; st.Mode() != expected {
		t.Fatalf("Mode mismatched, expected %o, got %o", expected, st.Mode())
	}
}

// TestAtomicWriteFileWithOpenReader replaces a file while a reader holds it
// open. The reader must keep seeing the old content, and new readers must
// see the new content. On Windows the reader has to allow FILE_SHARE_DELETE.
func TestAtomicWriteFileWithOpenReader(t *testing.T) {
	fileName := filepath.Join(t.TempDir(), "foo")
	old := []byte("old")
	if err := os.WriteFile(fileName, old, testMode); err != nil {
		t.Fatalf("Error writing to file: %v", err)
	}

	reader, err := openShareDelete(fileName)
	if err != nil {
		t.Fatalf("Error opening file: %v", err)
	}
	defer reader.Close()

	expected := []byte("new")
	if err := AtomicWriteFile(fileName, expected, testMode); err != nil {
		t.Fatalf("Error writing to file while a reader has it open: %v", err)
	}

	actual, err := os.ReadFile(fileName)
	if err != nil {
		t.Fatalf("Error reading from file: %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("Data mismatch, expected %q, got %q", expected, actual)
	}

	actual, err = io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Error reading from open reader: %v", err)
	}
	if !bytes.Equal(actual, old) {
		t.Fatalf("Open reader data mismatch, expected %q, got %q", old, actual)
	}
}

func TestAtomicCommitAndRollbackFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "foo")

	oldData := "olddata"
	newData := "newdata"

	check := func(n int, initData string, writeData string, expected string, explicit bool, commit bool) {
		if err := os.WriteFile(path, []byte(initData), 0o644); err != nil {
			t.Fatalf("Failed creating initial file: %v", err)
		}

		opts := &AtomicFileWriterOptions{ExplicitCommit: explicit}
		w, err := NewAtomicFileWriterWithOpts(filepath.Join(tmpDir, "foo"), 0o644, opts)
		if err != nil {
			t.Fatalf("(%d) Failed creating writer: %v", n, err)
		}

		if _, err := w.Write([]byte(writeData)); err != nil {
			t.Fatalf("(%d) Failed writing content: %v", n, err)
		}

		if commit {
			if err := w.Commit(); err != nil {
				t.Fatalf("(%d) Failed committing writer: %v", n, err)
			}
		}

		if err := w.Close(); err != nil {
			t.Fatalf("(%d) Failed closing writer: %v", n, err)
		}

		actual, err := os.ReadFile(filepath.Join(tmpDir, "foo"))
		if err != nil {
			t.Fatalf("(%d) Error reading from file: %v", n, err)
		}

		// Verify write never happened since no call to commit
		if !bytes.Equal(actual, []byte(expected)) {
			t.Fatalf("(%d) Data mismatch, expected %q, got %q", n, expected, actual)
		}
	}

	check(1, oldData, newData, oldData, true, false)
	check(2, oldData, newData, newData, true, true)
	check(3, oldData, newData, newData, false, false)
	check(4, oldData, newData, newData, false, true)
}

func TestAtomicWriteSetCommit(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.Mkdir(filepath.Join(tmpDir, "tmp"), 0o700); err != nil {
		t.Fatalf("Error creating tmp directory: %s", err)
	}

	targetDir := filepath.Join(tmpDir, "target")
	ws, err := NewAtomicWriteSet(filepath.Join(tmpDir, "tmp"))
	if err != nil {
		t.Fatalf("Error creating atomic write set: %s", err)
	}

	expected := []byte("barbaz")
	if err := ws.WriteFile("foo", expected, testMode); err != nil {
		t.Fatalf("Error writing to file: %v", err)
	}

	if _, err := os.ReadFile(filepath.Join(targetDir, "foo")); err == nil {
		t.Fatalf("Expected error reading file where should not exist")
	}

	if err := ws.Commit(targetDir); err != nil {
		t.Fatalf("Error committing file: %s", err)
	}

	actual, err := os.ReadFile(filepath.Join(targetDir, "foo"))
	if err != nil {
		t.Fatalf("Error reading from file: %v", err)
	}

	if !bytes.Equal(actual, expected) {
		t.Fatalf("Data mismatch, expected %q, got %q", expected, actual)
	}

	st, err := os.Stat(filepath.Join(targetDir, "foo"))
	if err != nil {
		t.Fatalf("Error statting file: %v", err)
	}
	if expected := testMode; st.Mode() != expected {
		t.Fatalf("Mode mismatched, expected %o, got %o", expected, st.Mode())
	}
}

func TestAtomicWriteSetCancel(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.Mkdir(filepath.Join(tmpDir, "tmp"), 0o700); err != nil {
		t.Fatalf("Error creating tmp directory: %s", err)
	}

	ws, err := NewAtomicWriteSet(filepath.Join(tmpDir, "tmp"))
	if err != nil {
		t.Fatalf("Error creating atomic write set: %s", err)
	}

	expected := []byte("barbaz")
	if err := ws.WriteFile("foo", expected, testMode); err != nil {
		t.Fatalf("Error writing to file: %v", err)
	}

	if err := ws.Cancel(); err != nil {
		t.Fatalf("Error committing file: %s", err)
	}

	if _, err := os.ReadFile(filepath.Join(tmpDir, "target", "foo")); err == nil {
		t.Fatalf("Expected error reading file where should not exist")
	} else if !os.IsNotExist(err) {
		t.Fatalf("Unexpected error reading file: %s", err)
	}
}
