package directory

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/pkg/blobinfocache/memory"
	"go.podman.io/image/v5/types"
)

var _ private.ImageSource = (*dirImageSource)(nil)

func TestDirImageSourceReference(t *testing.T) {
	ref, tmpDir := refToTempDir(t)
	err := os.WriteFile(filepath.Join(tmpDir, "version"), []byte("Directory Transport Version: 1.1\n"), 0o644)
	require.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	ref2 := src.Reference()
	assert.Equal(t, tmpDir, ref2.StringWithinTransport())
}

func TestDirImageSourceGetBlob(t *testing.T) {
	ref, _ := refToTempDir(t)
	cache := memory.New()

	blobs := []struct {
		algorithm digest.Algorithm
		blob      []byte
		digest    digest.Digest
	}{
		{digest.Canonical, []byte("canonical"), ""},
		{digest.SHA256, []byte("sha256"), ""},
		{digest.SHA512, []byte("sha512"), ""},
	}

	dest, err := ref.NewImageDestination(context.Background(), nil)
	require.NoError(t, err)
	defer dest.Close()
	for i := range blobs {
		blob := &blobs[i]
		blobs[i].digest = blob.algorithm.FromBytes(blob.blob)

		uploaded, err := dest.PutBlob(context.Background(), bytes.NewReader(blob.blob), types.BlobInfo{Digest: blob.digest, Size: int64(len(blob.blob))}, cache, false)
		require.NoError(t, err)
		require.Equal(t, int64(len(blob.blob)), uploaded.Size)
		require.Equal(t, blob.digest, uploaded.Digest)
	}
	err = dest.Commit(context.Background(), nil) // nil unparsedToplevel is invalid, we don’t currently use the value
	require.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	for _, blob := range blobs {
		rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: blob.digest, Size: int64(len(blob.blob))}, cache)
		require.NoError(t, err)
		defer rc.Close()
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, blob.blob, b)
		assert.Equal(t, int64(len(blob.blob)), size)
	}
}
