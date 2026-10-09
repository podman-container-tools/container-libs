package storage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCompressedTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func TestApplyDiffPreservesCompressedBlob(t *testing.T) {
	compressedData := makeCompressedTar(t, map[string]string{
		"hello.txt": "world",
	})
	blobPath := filepath.Join(t.TempDir(), "compressed-blob")
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	result, err := applyDiff(
		&LayerOptions{PreserveCompressedBlob: true},
		bytes.NewReader(compressedData),
		tarSplitFile,
		blobPath,
		func(payload io.Reader) (int64, error) {
			n, err := io.Copy(io.Discard, payload)
			return n, err
		},
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	blobBytes, err := os.ReadFile(blobPath)
	require.NoError(t, err, "compressed-blob file should exist")
	assert.Equal(t, compressedData, blobBytes, "blob content should match original compressed stream")
}

func TestApplyDiffNoBlobWhenFlagDisabled(t *testing.T) {
	compressedData := makeCompressedTar(t, map[string]string{
		"hello.txt": "world",
	})
	blobPath := filepath.Join(t.TempDir(), "compressed-blob")
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	_, err = applyDiff(
		&LayerOptions{PreserveCompressedBlob: false},
		bytes.NewReader(compressedData),
		tarSplitFile,
		blobPath,
		func(payload io.Reader) (int64, error) {
			n, err := io.Copy(io.Discard, payload)
			return n, err
		},
	)
	require.NoError(t, err)

	_, err = os.Stat(blobPath)
	assert.True(t, os.IsNotExist(err), "compressed-blob should not exist when flag is disabled")
}

func TestApplyDiffNoBlobWhenUncompressed(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: "hello.txt", Mode: 0o644, Size: 5}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err := tw.Write([]byte("world"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	blobPath := filepath.Join(t.TempDir(), "compressed-blob")
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	_, err = applyDiff(
		&LayerOptions{PreserveCompressedBlob: true},
		bytes.NewReader(buf.Bytes()),
		tarSplitFile,
		blobPath,
		func(payload io.Reader) (int64, error) {
			n, err := io.Copy(io.Discard, payload)
			return n, err
		},
	)
	require.NoError(t, err)

	_, err = os.Stat(blobPath)
	assert.True(t, os.IsNotExist(err), "compressed-blob should not exist for uncompressed streams")
}

func TestApplyDiffNoBlobWhenBlobPathEmpty(t *testing.T) {
	compressedData := makeCompressedTar(t, map[string]string{
		"hello.txt": "world",
	})
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	_, err = applyDiff(
		&LayerOptions{PreserveCompressedBlob: true},
		bytes.NewReader(compressedData),
		tarSplitFile,
		"",
		func(payload io.Reader) (int64, error) {
			n, err := io.Copy(io.Discard, payload)
			return n, err
		},
	)
	require.NoError(t, err)
}

func TestApplyDiffBlobIsSyncedBeforeRename(t *testing.T) {
	compressedData := makeCompressedTar(t, map[string]string{
		"hello.txt": "world",
	})
	blobPath := filepath.Join(t.TempDir(), "compressed-blob")
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	_, err = applyDiff(
		&LayerOptions{PreserveCompressedBlob: true},
		bytes.NewReader(compressedData),
		tarSplitFile,
		blobPath,
		func(payload io.Reader) (int64, error) {
			n, err := io.Copy(io.Discard, payload)
			return n, err
		},
	)
	require.NoError(t, err)

	info, err := os.Stat(blobPath)
	require.NoError(t, err)
	assert.Equal(t, int64(len(compressedData)), info.Size(), "blob size should match compressed data")
}

func TestApplyDiffCleansBlobOnError(t *testing.T) {
	compressedData := makeCompressedTar(t, map[string]string{
		"hello.txt": "world",
	})
	blobDir := t.TempDir()
	blobPath := filepath.Join(blobDir, "compressed-blob")
	tarSplitFile, err := os.CreateTemp(t.TempDir(), "tar-split")
	require.NoError(t, err)
	defer tarSplitFile.Close()

	_, err = applyDiff(
		&LayerOptions{PreserveCompressedBlob: true},
		bytes.NewReader(compressedData),
		tarSplitFile,
		blobPath,
		func(payload io.Reader) (int64, error) {
			_, _ = io.CopyN(io.Discard, payload, 1)
			return 0, assert.AnError
		},
	)
	require.Error(t, err)

	_, statErr := os.Stat(blobPath)
	assert.True(t, os.IsNotExist(statErr), "compressed-blob should be cleaned up on extraction error")

	entries, _ := os.ReadDir(blobDir)
	for _, e := range entries {
		assert.Fail(t, "unexpected file in blobDir after error", e.Name())
	}
}
