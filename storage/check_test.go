package storage

import (
	"archive/tar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/archive"
)

func TestCheckDirectory(t *testing.T) {
	vectors := []struct {
		description string
		headers     []tar.Header
		expected    []string
	}{
		{
			description: "basic",
			headers: []tar.Header{
				{Name: "a", Typeflag: tar.TypeDir},
			},
			expected: []string{
				"a/",
			},
		},
		{
			description: "whiteout",
			headers: []tar.Header{
				{Name: "a", Typeflag: tar.TypeDir},
				{Name: "a/b", Typeflag: tar.TypeDir},
				{Name: "a/b/c", Typeflag: tar.TypeReg},
				{Name: "a/b/d", Typeflag: tar.TypeReg},
				{Name: "a/b/" + archive.WhiteoutPrefix + "c", Typeflag: tar.TypeReg},
			},
			expected: []string{
				"a/",
				"a/b/",
				"a/b/d",
			},
		},
		{
			description: "opaque",
			headers: []tar.Header{
				{Name: "a", Typeflag: tar.TypeDir},
				{Name: "a/b", Typeflag: tar.TypeDir},
				{Name: "a/b/c", Typeflag: tar.TypeReg},
				{Name: "a/b/d", Typeflag: tar.TypeReg},
				{Name: "a/b/" + archive.WhiteoutOpaqueDir, Typeflag: tar.TypeReg},
			},
			expected: []string{
				"a/",
				"a/b/",
			},
		},
	}
	for i := range vectors {
		t.Run(vectors[i].description, func(t *testing.T) {
			cd := newCheckDirectoryDefaults()
			for _, hdr := range vectors[i].headers {
				cd.header(&hdr)
			}
			actual := cd.names()
			assert.ElementsMatch(t, vectors[i].expected, actual)
		})
	}
}

func TestCheckParentLayerInROStore(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	_, err := roStore.CreateLayer("ro-parent-layer", "", nil, "", false, nil)
	require.NoError(t, err)
	roStoreRoot := roStore.GraphRoot()
	_, err = roStore.Shutdown(false)
	require.NoError(t, err)

	// Copy the graph root to a fresh path so the global lock file cache
	// doesn't conflict (the first store registered these locks as RW).
	roStoreCopy := t.TempDir()
	require.NoError(t, os.CopyFS(roStoreCopy, os.DirFS(roStoreRoot)))

	rwStore := newTestStore(t, StoreOptions{
		GraphDriverOptions: []string{"imagestore=" + roStoreCopy},
	})
	t.Cleanup(func() { _, _ = rwStore.Shutdown(true) })

	_, err = rwStore.CreateLayer("rw-child-layer", "ro-parent-layer", nil, "", true, nil)
	require.NoError(t, err)

	report, err := rwStore.Check(nil)
	require.NoError(t, err)
	assert.Empty(t, report.Layers, "RW child layer with RO parent should not be flagged")
	assert.Empty(t, report.ROLayers, "RO parent layer should not be flagged")
}

func TestCheckDetectWriteable(t *testing.T) {
	var sawRWlayers, sawRWimages bool
	stoar, err := GetStore(StoreOptions{
		RunRoot:         t.TempDir(),
		GraphRoot:       t.TempDir(),
		GraphDriverName: "vfs",
	})
	require.NoError(t, err, "unexpected error initializing test store")
	s, ok := stoar.(*store)
	require.True(t, ok, "unexpected error making type assertion")
	_, done, err := readAllLayerStores(s, func(store roLayerStore) (struct{}, bool, error) {
		if roLayerStoreIsReallyReadWrite(store) { // implicitly checking that the type assertion in this function doesn't panic
			sawRWlayers = true
		}
		return struct{}{}, false, nil
	})
	assert.False(t, done, "unexpected error from readAllLayerStores")
	assert.NoError(t, err, "unexpected error from readAllLayerStores")
	assert.True(t, sawRWlayers, "unexpected error detecting which layer store is writeable")
	_, done, err = readAllImageStores(s, func(store roImageStore) (struct{}, bool, error) {
		if roImageStoreIsReallyReadWrite(store) { // implicitly checking that the type assertion in this function doesn't panic
			sawRWimages = true
		}
		return struct{}{}, false, nil
	})
	assert.False(t, done, "unexpected error from readAllImageStores")
	assert.NoError(t, err, "unexpected error from readAllImageStores")
	assert.True(t, sawRWimages, "unexpected error detecting which image store is writeable")
}

func TestImplicitDirectorySize(t *testing.T) {
	c := newCheckDirectoryDefaults()
	header := &tar.Header{Name: "etc/nested/payload", Typeflag: tar.TypeReg, Mode: 0o755, Size: 7, ModTime: time.Unix(20, 0)}
	c.headers([]*tar.Header{header})
	etc := c.directory["etc"]
	require.NotNil(t, etc)
	nested := etc.directory["nested"]
	require.NotNil(t, nested)
	assert.Zero(t, etc.size)
	assert.Zero(t, nested.size)
	payload, ok := nested.file["payload"]
	require.True(t, ok)
	assert.Equal(t, int64(7), payload.size)
}

func TestImplicitDirectoryConstraintsRemainChecked(t *testing.T) {
	for _, field := range []string{"mtime", "uid", "gid", "mode", "file size"} {
		t.Run(field, func(t *testing.T) {
			header := &tar.Header{Name: "etc/payload", Typeflag: tar.TypeReg, Mode: 0o755, Size: 7, ModTime: time.Unix(20, 0)}
			expected := newCheckDirectoryDefaults()
			expected.headers([]*tar.Header{header})
			actual := newCheckDirectoryDefaults()
			actual.headers([]*tar.Header{header})
			switch field {
			case "mtime":
				actual.directory["etc"].mtime++
			case "uid":
				actual.directory["etc"].uid++
			case "gid":
				actual.directory["etc"].gid++
			case "mode":
				actual.directory["etc"].mode = 0o700
			case "file size":
				f := actual.directory["etc"].file["payload"]
				f.size++
				actual.directory["etc"].file["payload"] = f
			}
			diffs := compareCheckDirectory(expected, actual, nil, checkIgnore{})
			if len(diffs) == 0 {
				t.Fatal("modified attribute not detected")
			}
			if field == "mtime" && !strings.Contains(strings.Join(diffs, " "), "mtime") {
				t.Fatal("directory mtime constraint lost")
			}
		})
	}
}
