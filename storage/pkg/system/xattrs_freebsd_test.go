package system

import (
	"os"
	"path/filepath"
	"testing"
)

// Llistxattr must succeed even when the caller cannot read every extattr
// namespace, returning the attributes which are readable.
//
// Reading the "system" namespace requires privilege on FreeBSD
// (PRIV_VFS_EXTATTR_SYSTEM), so extattr_list_link(2) fails with EPERM for an
// unprivileged process, or for any process in a jail without allow.extattr.
// Failing the whole call in that case made callers which only want "user.*"
// - readUserXattrToTarHeader, and therefore every layer diff - drop the file
// they were asked to archive.
//
// This test only exercises the regression when run unprivileged; as root both
// namespaces are readable and it passes trivially.
func TestLlistxattrSkipsUnreadableNamespaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	attrs, err := Llistxattr(path)
	if err != nil {
		t.Fatalf("Llistxattr(%q) = _, %v; want a nil error even when a namespace is unreadable", path, err)
	}
	if attrs == nil {
		t.Errorf("Llistxattr(%q) returned nil attrs, want an empty slice", path)
	}
}
