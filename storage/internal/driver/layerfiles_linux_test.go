//go:build linux

package driver

import (
	"io"
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

func TestLayerFilesOpen(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "usr/bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "usr/bin/tool"), []byte("tool"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "ab"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ab/cdef"), []byte("flat"), 0o600))
	require.NoError(t, os.Symlink("usr/bin/tool", filepath.Join(dir, "link")))

	read := func(f LayerFiles, name string) (string, error) {
		t.Helper()
		r, err := f.Open(name)
		if err != nil {
			return "", err
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		return string(data), nil
	}
	newFiles := func(flatNames map[string]string) LayerFiles {
		t.Helper()
		d, err := os.Open(dir)
		require.NoError(t, err)
		f := NewLayerFiles(d, flatNames)
		t.Cleanup(func() { require.NoError(t, f.Close()) })
		return f
	}

	f := newFiles(nil)
	for _, name := range []string{"usr/bin/tool", "./usr/bin/tool", "/usr/bin/tool", "../usr/bin/tool"} {
		got, err := read(f, name)
		require.NoError(t, err, name)
		assert.Equal(t, "tool", got, name)
	}
	_, err := read(f, "missing")
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = read(f, "link")
	assert.Error(t, err, "symlinks are refused")
	_, err = read(f, "/")
	assert.ErrorIs(t, err, os.ErrNotExist)

	flat := newFiles(map[string]string{"usr/bin/tool": "ab/cdef"})
	got, err := read(flat, "./usr/bin/tool")
	require.NoError(t, err)
	assert.Equal(t, "flat", got)
	_, err = read(flat, "usr/bin/other")
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// A stored name that leaves the diff directory cannot come from a valid
// table of contents, so it is an error rather than a reason to inline.
func TestLayerFilesEscapingStoredName(t *testing.T) {
	d, err := os.Open(t.TempDir())
	require.NoError(t, err)
	f := NewLayerFiles(d, map[string]string{"etc/conf": "../ab/cdef"})
	defer f.Close()

	_, _, _, err = f.Lookup("etc/conf", 4, false)
	assert.Error(t, err)
	_, err = f.Open("etc/conf")
	assert.Error(t, err)
}
