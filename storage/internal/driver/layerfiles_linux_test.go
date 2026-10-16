//go:build linux

package driver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayerFilesLookup(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	write := func(name string, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), mode))
		// WriteFile is subject to the umask.
		require.NoError(t, os.Chmod(p, mode))
	}
	write("usr/bin/tool", "tool", 0o755)
	write("private", "secret", 0o600)
	write("public", "public", 0o644)
	write("ab/cdef", "flat", 0o644)
	require.NoError(t, os.Symlink("usr/bin/tool", filepath.Join(dir, "link")))

	open := func(flatNames map[string]string) LayerFiles {
		t.Helper()
		d, err := os.Open(dir)
		require.NoError(t, err)
		f := NewLayerFiles(d, flatNames)
		t.Cleanup(func() { require.NoError(t, f.Close()) })
		return f
	}

	for _, tc := range []struct {
		name              string
		flatNames         map[string]string
		entry             string
		size              int64
		onlyWorldReadable bool
		parent            string
		parentMode        os.FileMode
		wantFile          string // "" if the content has to be inlined.
	}{
		{name: "regular file", entry: "usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "leading ./", entry: "./usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "size mismatch", entry: "usr/bin/tool", size: 5},
		{name: "missing", entry: "usr/bin/missing", size: 4},
		{name: "symlink", entry: "link", size: 4},
		{name: "absolute", entry: "/usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "dot-dot inside", entry: "usr/../usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "above the root", entry: "../usr/bin/tool", size: 4, wantFile: "usr/bin/tool"},
		{name: "the root", entry: "/", size: 4},
		{name: "private, consumer can bypass DAC", entry: "private", size: 6, wantFile: "private"},
		{name: "private, consumer cannot bypass DAC", entry: "private", size: 6, onlyWorldReadable: true},
		{name: "world-readable, consumer cannot bypass DAC", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, wantFile: "usr/bin/tool"},
		{name: "private immediate parent", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, parent: "usr/bin", parentMode: 0o700},
		{name: "private ancestor", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, parent: "usr", parentMode: 0o700},
		{name: "private diff directory", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, parent: ".", parentMode: 0o700},
		{name: "private diff directory, direct child", entry: "public", size: 6, onlyWorldReadable: true, parent: ".", parentMode: 0o700},
		{name: "readable but unsearchable parent", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, parent: "usr", parentMode: 0o744},
		{name: "searchable but unreadable parent", entry: "usr/bin/tool", size: 4, onlyWorldReadable: true, parent: "usr", parentMode: 0o711, wantFile: "usr/bin/tool"},
		{name: "private parent, consumer can bypass DAC", entry: "usr/bin/tool", size: 4, parent: "usr", parentMode: 0o700, wantFile: "usr/bin/tool"},
		{name: "private diff directory, consumer can bypass DAC", entry: "usr/bin/tool", size: 4, parent: ".", parentMode: 0o700, wantFile: "usr/bin/tool"},
		{name: "flat", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "etc/conf", size: 4, wantFile: "ab/cdef"},
		{name: "flat, private parent", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "etc/conf", size: 4, onlyWorldReadable: true, parent: "ab", parentMode: 0o700},
		{name: "flat, unclean name", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "./etc//conf", size: 4, wantFile: "ab/cdef"},
		{name: "flat, not in the map", flatNames: map[string]string{"etc/conf": "ab/cdef"}, entry: "usr/bin/tool", size: 4},
		{name: "flat, escaping name", flatNames: map[string]string{"etc/conf": "../ab/cdef"}, entry: "etc/conf", size: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.parent != "" {
				p := filepath.Join(dir, tc.parent)
				require.NoError(t, os.Chmod(p, tc.parentMode))
				t.Cleanup(func() {
					assert.NoError(t, os.Chmod(p, 0o755))
				})
			}
			f := open(tc.flatNames)
			index, file, ok, err := f.Lookup(tc.entry, tc.size, tc.onlyWorldReadable)
			require.NoError(t, err)
			if tc.wantFile == "" {
				assert.False(t, ok, "expected the content to be inlined, got %q", file)
				return
			}
			assert.True(t, ok)
			assert.Zero(t, index)
			assert.Equal(t, tc.wantFile, file)
		})
	}
}
