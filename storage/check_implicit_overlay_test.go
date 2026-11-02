//go:build linux

package storage

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "go.podman.io/storage/drivers/register"
	"go.podman.io/storage/pkg/reexec"
)

// These tests use only fresh stores and synthetic changesets, never the default store.
func TestCheckImplicitOverlay(t *testing.T) {
	reexec.Init()
	require.Equal(t, 0, os.Geteuid(), "the overlay integration test requires root")
	root := t.TempDir()
	store, err := GetStore(StoreOptions{GraphRoot: filepath.Join(root, "graph"), RunRoot: filepath.Join(root, "run"), GraphDriverName: "overlay", GraphDriverOptions: []string{"overlay.mountopt=nodev"}})
	require.NoError(t, err)
	t.Cleanup(func() { _, err := store.Shutdown(true); require.NoError(t, err) })
	require.Equal(t, filepath.Join(root, "graph"), store.GraphRoot())
	type entry struct {
		name, body string
		kind       byte
	}
	add := func(parent string, entries ...entry) string {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, e := range entries {
			hdr := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0o755, Uid: 0, Gid: 0, ModTime: time.Unix(1000000000, 0)}
			if e.kind == tar.TypeReg {
				hdr.Size = int64(len(e.body))
			}
			require.NoError(t, tw.WriteHeader(hdr))
			_, err := io.WriteString(tw, e.body)
			require.NoError(t, err)
		}
		require.NoError(t, tw.Close())
		layer, _, err := store.PutLayer("", parent, nil, "", false, nil, &buf)
		require.NoError(t, err)
		_, err = store.CreateImage("", nil, layer.ID, "", nil)
		require.NoError(t, err)
		return layer.ID
	}
	base := add("", entry{"etc/", "", tar.TypeDir}, entry{"etc/explicit/", "", tar.TypeDir}, entry{"etc/explicit/file", "base", tar.TypeReg})
	implicit := add(base, entry{"etc/", "", tar.TypeDir}, entry{"etc/nested/", "", tar.TypeDir}, entry{"etc/nested/payload", "payload", tar.TypeReg})
	add(implicit, entry{"var/", "", tar.TypeDir}, entry{"var/health", "ok", tar.TypeReg})
	check := func() *CheckReport { r, err := store.Check(CheckEverything()); require.NoError(t, err); return &r }
	clean := func() {
		r := check()
		require.Empty(t, r.Layers)
		require.Empty(t, r.ROLayers)
		require.Empty(t, r.Images)
		require.Empty(t, r.Containers)
	}
	clean()
	dir := filepath.Join(store.GraphRoot(), "overlay", implicit, "diff", "etc")
	nested := filepath.Join(dir, "nested")
	file := filepath.Join(nested, "payload")
	nestedInfo, err := os.Stat(nested)
	require.NoError(t, err)
	explicit := filepath.Join(store.GraphRoot(), "overlay", base, "diff", "etc", "explicit")
	fileBytes, err := os.ReadFile(file)
	require.NoError(t, err)
	fileInfo, err := os.Stat(file)
	require.NoError(t, err)
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	explicitInfo, err := os.Stat(explicit)
	require.NoError(t, err)
	faults := []struct {
		name           string
		apply, restore func()
	}{
		{"content with original mtime", func() {
			require.NoError(t, os.WriteFile(file, []byte("PAYLOAD"), 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}, func() {
			require.NoError(t, os.WriteFile(file, fileBytes, 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}},
		{"file size", func() {
			require.NoError(t, os.WriteFile(file, []byte("truncated"), 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}, func() {
			require.NoError(t, os.WriteFile(file, fileBytes, 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}},
		{"file mtime", func() { require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime().Add(time.Hour))) }, func() { require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime())) }},
		{"explicit directory mtime", func() {
			require.NoError(t, os.Chtimes(explicit, explicitInfo.ModTime(), explicitInfo.ModTime().Add(time.Hour)))
		}, func() { require.NoError(t, os.Chtimes(explicit, explicitInfo.ModTime(), explicitInfo.ModTime())) }},
		{"directory mode", func() { require.NoError(t, os.Chmod(dir, 0o700)) }, func() { require.NoError(t, os.Chmod(dir, dirInfo.Mode().Perm())) }},
		{"directory uid", func() { require.NoError(t, os.Chown(dir, 123, 0)) }, func() { require.NoError(t, os.Chown(dir, 0, 0)) }},
		{"directory gid", func() { require.NoError(t, os.Chown(dir, 0, 123)) }, func() { require.NoError(t, os.Chown(dir, 0, 0)) }},
		{"file mode", func() { require.NoError(t, os.Chmod(file, 0o600)) }, func() { require.NoError(t, os.Chmod(file, fileInfo.Mode().Perm())) }},
		{"unexpected file", func() {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "unexpected"), []byte("unexpected"), 0o600))
		}, func() { require.NoError(t, os.Remove(filepath.Join(dir, "unexpected"))) }},
		{"missing file", func() { require.NoError(t, os.Remove(file)) }, func() {
			require.NoError(t, os.WriteFile(file, fileBytes, 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}},
		{"file replaced by directory", func() { require.NoError(t, os.Remove(file)); require.NoError(t, os.Mkdir(file, 0o755)) }, func() {
			require.NoError(t, os.Remove(file))
			require.NoError(t, os.WriteFile(file, fileBytes, 0o755))
			require.NoError(t, os.Chtimes(file, fileInfo.ModTime(), fileInfo.ModTime()))
		}},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			fault.apply()
			t.Cleanup(fault.restore)
			r := check()
			require.NotEmpty(t, r.Layers[implicit], "damaged layer must be reported")
			require.NotEmpty(t, r.Images, "dependent images must be reported")
		})
		require.NoError(t, os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()))
		require.NoError(t, os.Chtimes(nested, nestedInfo.ModTime(), nestedInfo.ModTime()))
		clean()
	}
}

// Omitted metadata must not turn later directory timestamp changes into a clean report.
func TestCheckImplicitDirectoryTimeRemainsFlagged(t *testing.T) {
	reexec.Init()
	require.Equal(t, 0, os.Geteuid())
	root := t.TempDir()
	store, err := GetStore(StoreOptions{GraphRoot: filepath.Join(root, "graph"), RunRoot: filepath.Join(root, "run"), GraphDriverName: "overlay", GraphDriverOptions: []string{"overlay.mountopt=nodev"}})
	require.NoError(t, err)
	t.Cleanup(func() { _, err := store.Shutdown(true); require.NoError(t, err) })
	add := func(parent string, header *tar.Header) string {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		require.NoError(t, tw.WriteHeader(header))
		require.NoError(t, tw.Close())
		layer, _, err := store.PutLayer("", parent, nil, "", false, nil, &buf)
		require.NoError(t, err)
		_, err = store.CreateImage("", nil, layer.ID, "", nil)
		require.NoError(t, err)
		return layer.ID
	}
	base := add("", &tar.Header{Name: "etc/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Unix(1000000000, 0)})
	layer := add(base, &tar.Header{Name: "etc/empty", Typeflag: tar.TypeReg, Mode: 0o755, ModTime: time.Unix(1000000000, 0)})
	directory := filepath.Join(store.GraphRoot(), "overlay", layer, "diff", "etc")
	info, err := os.Stat(directory)
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(directory, info.ModTime(), info.ModTime().Add(time.Hour)))
	report, err := store.Check(CheckEverything())
	require.NoError(t, err)
	require.NotEmpty(t, report.Layers[layer], "post-import directory mtime change must remain visible")
	require.NotEmpty(t, report.Images, "dependent image must remain flagged")
}
