//go:build linux

package storage_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	storage "go.podman.io/storage"
	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/pkg/chunked"
	"go.podman.io/storage/pkg/chunked/compressor"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/reexec"
)

type chunkedFileFetcher struct {
	file *os.File
}

func (f chunkedFileFetcher) GetBlobAt(chunks []chunked.ImageSourceChunk) (chan io.ReadCloser, chan error, error) {
	streams := make(chan io.ReadCloser)
	errs := make(chan error)
	go func() {
		defer close(streams)
		defer close(errs)
		for _, chunk := range chunks {
			streams <- io.NopCloser(io.NewSectionReader(f.file, int64(chunk.Offset), int64(chunk.Length)))
		}
	}()
	return streams, errs, nil
}

type tarEntry struct {
	name     string
	mode     int64
	typ      byte
	contents []byte
}

func makeTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		hdr := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Typeflag: entry.typ,
			Size:     int64(len(entry.contents)),
			ModTime:  time.Unix(1, 0).UTC(),
			Format:   tar.FormatPAX,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(entry.contents) > 0 {
			_, err := tw.Write(entry.contents)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func makeChunkedBlob(t *testing.T, payload []byte) (*os.File, int64, digest.Digest, map[string]string) {
	t.Helper()

	blob, err := os.CreateTemp(t.TempDir(), "chunked-blob-*.zst")
	require.NoError(t, err)

	metadata := make(map[string]string)
	w, err := compressor.ZstdCompressor(blob, metadata, nil)
	require.NoError(t, err)

	d := digest.Canonical.Digester()
	_, err = io.Copy(w, io.TeeReader(bytes.NewReader(payload), d.Hash()))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	size, err := blob.Seek(0, io.SeekCurrent)
	require.NoError(t, err)

	return blob, size, d.Digest(), metadata
}

func newTestStore(t *testing.T, testOptions storage.StoreOptions) storage.Store {
	t.Helper()

	tmpRoot := os.Getenv("STORAGE_TEST_TMPDIR")
	if tmpRoot == "" {
		tmpRoot = os.TempDir()
	}
	wd, err := os.MkdirTemp(tmpRoot, "storage-test-")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(wd)
	})

	options := testOptions
	if options.RunRoot == "" {
		options.RunRoot = filepath.Join(wd, "run")
	}
	if options.GraphRoot == "" {
		options.GraphRoot = filepath.Join(wd, "root")
	}
	if options.GraphDriverName == "" {
		options.GraphDriverName = "vfs"
	}
	if options.GraphDriverOptions == nil {
		options.GraphDriverOptions = []string{}
	}
	if len(options.UIDMap) == 0 {
		options.UIDMap = []idtools.IDMap{{
			ContainerID: 0,
			HostID:      os.Getuid(),
			Size:        1,
		}}
	}
	if len(options.GIDMap) == 0 {
		options.GIDMap = []idtools.IDMap{{
			ContainerID: 0,
			HostID:      os.Getgid(),
			Size:        1,
		}}
	}

	store, err := storage.GetStore(options)
	require.NoError(t, err)
	return store
}

func TestApplyStagedLayerPreservesImplicitDirectoryModes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	if reexec.Init() {
		return
	}

	store := newTestStore(t, storage.StoreOptions{
		GraphDriverName: "overlay",
		PullOptions: map[string]string{
			"enable_partial_images": "true",
		},
	})
	defer store.Free()
	defer func() {
		_, _ = store.Shutdown(true)
	}()

	lowerTar := makeTar(t, []tarEntry{
		{name: "subdirectory1/", mode: 0o700, typ: tar.TypeDir},
		{name: "subdirectory2/", mode: 0o750, typ: tar.TypeDir},
	})
	lower, err := store.CreateLayer("", "", nil, "", false, nil)
	require.NoError(t, err)
	_, err = store.ApplyDiff(lower.ID, bytes.NewReader(lowerTar))
	require.NoError(t, err)

	middleTar := makeTar(t, []tarEntry{
		{name: "subdirectory1/testfile1", mode: 0o644, typ: tar.TypeReg, contents: []byte("one")},
		{name: "subdirectory2/testfile2", mode: 0o644, typ: tar.TypeReg, contents: []byte("two")},
	})
	blob, size, blobDigest, metadata := makeChunkedBlob(t, middleTar)
	defer blob.Close()

	differ, err := chunked.NewDiffer(context.Background(), store, blobDigest, size, metadata, chunkedFileFetcher{file: blob})
	require.NoError(t, err)
	defer differ.Close()

	options := &graphdriver.ApplyDiffWithDifferOpts{}
	out, err := store.PrepareStagedLayer(options, differ)
	require.NoError(t, err)

	middle, err := store.ApplyStagedLayer(storage.ApplyStagedLayerOptions{
		ParentLayer: lower.ID,
		DiffOutput:  out,
		DiffOptions: options,
	})
	require.NoError(t, err)

	mountpoint, err := store.Mount(middle.ID, "")
	require.NoError(t, err)
	defer func() {
		_, _ = store.Unmount(middle.ID, true)
	}()

	info, err := os.Stat(filepath.Join(mountpoint, "subdirectory1"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(mountpoint, "subdirectory2"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}
