//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayerFilesLookup(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		// WriteFile is subject to the umask.
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("usr/bin/tool", "tool", 0o755)
	write("private", "secret", 0o600)
	write("ab/cdef", "flat", 0o644)
	if err := os.Symlink("usr/bin/tool", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	open := func(flatNames map[string]string) *layerFiles {
		t.Helper()
		d, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		f := &layerFiles{diffDir: d, flatNames: flatNames}
		t.Cleanup(func() { f.Close() })
		return f
	}

	for _, tc := range []struct {
		name              string
		flatNames         map[string]string
		entry             string
		size              int64
		onlyWorldReadable bool
		wantFile          string // "" if the content has to be inlined.
	}{
		{name: "regular file", entry: "usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "leading ./", entry: "./usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "size mismatch", entry: "usr/bin/tool", size: 5},
		{name: "missing", entry: "usr/bin/missing", size: 4},
		{name: "symlink", entry: "link", size: 4},
		{name: "absolute", entry: "/usr/bin/tool", size: 4},
		{name: "escapes", entry: "../outside", size: 4},
		{name: "private, consumer can bypass DAC", entry: "private", size: 6, wantFile: "private"},
		{name: "private, consumer cannot bypass DAC", entry: "private", size: 6, onlyWorldReadable: true},
		{name: "world-readable, consumer cannot bypass DAC", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, wantFile: "usr/bin/tool"},
		{name: "flat", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "etc/conf", size: 4, wantFile: "ab/cdef"},
		{name: "flat, not in the map", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "usr/bin/tool", size: 4},
		{name: "flat, escaping name", flatNames: map[string]string{"etc/conf": "../ab/cdef"}, entry: "etc/conf", size: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := open(tc.flatNames)
			index, file, ok, err := f.Lookup(tc.entry, tc.size, tc.onlyWorldReadable)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if tc.wantFile == "" {
				if ok {
					t.Fatalf("expected the content to be inlined, got %q", file)
				}
				return
			}
			if !ok || index != 0 || file != tc.wantFile {
				t.Fatalf("got (%d, %q, %v), want (0, %q, true)", index, file, ok, tc.wantFile)
			}
		})
	}
}
