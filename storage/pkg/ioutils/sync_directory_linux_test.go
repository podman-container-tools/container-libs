//go:build linux

package ioutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncDirectoryContents(t *testing.T) {
	dir := t.TempDir()

	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	files := []string{
		filepath.Join(dir, "file1"),
		filepath.Join(nested, "file2"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("storage-resilience"), 0o644); err != nil {
			t.Fatalf("write file %q: %v", file, err)
		}
	}

	if err := SyncDirectoryContents(dir); err != nil {
		t.Fatalf("SyncDirectoryContents: %v", err)
	}
}

// TestSyncDirectoryContentsDanglingSymlink reproduces the failure this test
// was written after observing in practice: pulling quay.io/crio/nginx
// through the partial-pull/staging path failed with
//
//	sync directory contents in ".../overlay/staging/.../dir": open
//	.../dir/etc/alternatives/awk.1.gz: no such file or directory
//
// Debian-based images populate /etc/alternatives with symlinks, some of
// which point outside the layer's own diff (e.g. into a lower layer, or a
// path never materialized in this staging directory at all). os.Open
// dereferences symlinks, so walking the tree and opening every non-dir
// entry -- including symlinks -- fails with ENOENT on any such link. A
// symlink has no file content of its own to fdatasync; its target string
// is directory-entry metadata covered by fsync-ing the parent directory.
func TestSyncDirectoryContentsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "real-file"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write real-file: %v", err)
	}

	// Points at a target that does not exist anywhere on disk, exactly
	// like a symlink whose target lives in a different layer than the
	// one currently staged.
	dangling := filepath.Join(dir, "awk.1.gz")
	if err := os.Symlink("/nonexistent/mawk.1.gz", dangling); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}

	if err := SyncDirectoryContents(dir); err != nil {
		t.Fatalf("SyncDirectoryContents should skip symlinks instead of dereferencing them: %v", err)
	}
}
