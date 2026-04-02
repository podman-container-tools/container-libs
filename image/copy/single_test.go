package copy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/manifest"
	ociLayout "go.podman.io/image/v5/oci/layout"
	"go.podman.io/image/v5/pkg/compression"
	compressiontypes "go.podman.io/image/v5/pkg/compression/types"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/types"
	chunkedToc "go.podman.io/storage/pkg/chunked/toc"
)

func TestUpdatedBlobInfoFromReuse(t *testing.T) {
	srcInfo := types.BlobInfo{
		Digest:               "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb",
		Size:                 51354364,
		URLs:                 []string{"https://layer.url"},
		Annotations:          map[string]string{"test-annotation-2": "two"},
		MediaType:            imgspecv1.MediaTypeImageLayerGzip,
		CompressionOperation: types.Compress,    // Might be set by blobCacheSource.LayerInfosForCopy
		CompressionAlgorithm: &compression.Gzip, // Set e.g. in copyLayer
		// CryptoOperation is not set by LayerInfos()
	}

	for _, c := range []struct {
		reused   private.ReusedBlob
		expected types.BlobInfo
	}{
		{ // A straightforward reuse without substitution
			reused: private.ReusedBlob{
				Digest: "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb",
				Size:   51354364,
				// CompressionOperation not set
				// CompressionAlgorithm not set
			},
			expected: types.BlobInfo{
				Digest:               "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb",
				Size:                 51354364,
				URLs:                 nil,
				Annotations:          map[string]string{"test-annotation-2": "two"},
				MediaType:            imgspecv1.MediaTypeImageLayerGzip,
				CompressionOperation: types.Compress,    // Might be set by blobCacheSource.LayerInfosForCopy
				CompressionAlgorithm: &compression.Gzip, // Set e.g. in copyLayer
				// CryptoOperation is set to the zero value
			},
		},
		{ // Reuse with substitution
			reused: private.ReusedBlob{
				Digest:                 "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Size:                   513543640,
				CompressionOperation:   types.Decompress,
				CompressionAlgorithm:   nil,
				CompressionAnnotations: map[string]string{"decompressed": "value"},
			},
			expected: types.BlobInfo{
				Digest:               "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Size:                 513543640,
				URLs:                 nil,
				Annotations:          map[string]string{"test-annotation-2": "two", "decompressed": "value"},
				MediaType:            imgspecv1.MediaTypeImageLayerGzip,
				CompressionOperation: types.Decompress,
				CompressionAlgorithm: nil,
				// CryptoOperation is set to the zero value
			},
		},
		{ // Reuse turning zstd into zstd:chunked
			reused: private.ReusedBlob{
				Digest:                 "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb",
				Size:                   51354364,
				CompressionOperation:   types.Compress,
				CompressionAlgorithm:   &compression.ZstdChunked,
				CompressionAnnotations: map[string]string{"zstd-toc": "value"},
			},
			expected: types.BlobInfo{
				Digest:               "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb",
				Size:                 51354364,
				URLs:                 nil,
				Annotations:          map[string]string{"test-annotation-2": "two", "zstd-toc": "value"},
				MediaType:            imgspecv1.MediaTypeImageLayerGzip,
				CompressionOperation: types.Compress,
				CompressionAlgorithm: &compression.ZstdChunked,
				// CryptoOperation is set to the zero value
			},
		},
	} {
		res := updatedBlobInfoFromReuse(srcInfo, c.reused)
		assert.Equal(t, c.expected, res, fmt.Sprintf("%#v", c.reused))
	}
}

func goDiffIDComputationGoroutineWithTimeout(layerStream io.ReadCloser, decompressor compressiontypes.DecompressorFunc) *diffIDResult {
	ch := make(chan diffIDResult)
	go diffIDComputationGoroutine(ch, layerStream, decompressor)
	timeout := time.After(time.Second)
	select {
	case res := <-ch:
		return &res
	case <-timeout:
		return nil
	}
}

func TestDiffIDComputationGoroutine(t *testing.T) {
	stream, err := os.Open("fixtures/Hello.uncompressed")
	require.NoError(t, err)
	res := goDiffIDComputationGoroutineWithTimeout(stream, nil)
	require.NotNil(t, res)
	assert.NoError(t, res.err)
	assert.Equal(t, "sha256:185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", res.digest.String())

	// Error reading input
	reader, writer := io.Pipe()
	err = writer.CloseWithError(errors.New("Expected error reading input in diffIDComputationGoroutine"))
	require.NoError(t, err)
	res = goDiffIDComputationGoroutineWithTimeout(reader, nil)
	require.NotNil(t, res)
	assert.Error(t, res.err)
}

func TestComputeDiffID(t *testing.T) {
	for _, c := range []struct {
		filename     string
		decompressor compressiontypes.DecompressorFunc
		result       digest.Digest
	}{
		{"fixtures/Hello.uncompressed", nil, "sha256:185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969"},
		{"fixtures/Hello.gz", nil, "sha256:0bd4409dcd76476a263b8f3221b4ce04eb4686dec40bfdcc2e86a7403de13609"},
		{"fixtures/Hello.gz", compression.GzipDecompressor, "sha256:185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969"},
		{"fixtures/Hello.zst", nil, "sha256:361a8e0372ad438a0316eb39a290318364c10b60d0a7e55b40aa3eafafc55238"},
		{"fixtures/Hello.zst", compression.ZstdDecompressor, "sha256:185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969"},
	} {
		stream, err := os.Open(c.filename)
		require.NoError(t, err, c.filename)
		defer stream.Close()

		diffID, err := computeDiffID(stream, c.decompressor)
		require.NoError(t, err, c.filename)
		assert.Equal(t, c.result, diffID)
	}

	// Error initializing decompression
	_, err := computeDiffID(bytes.NewReader([]byte{}), compression.GzipDecompressor)
	assert.Error(t, err)

	// Error reading input
	reader, writer := io.Pipe()
	defer reader.Close()
	err = writer.CloseWithError(errors.New("Expected error reading input in computeDiffID"))
	require.NoError(t, err)
	_, err = computeDiffID(reader, nil)
	assert.Error(t, err)
}

// createOCILayoutWithSentinel creates a minimal OCI layout directory containing
// a single image with a sentinel layer prepended (simulating a zstd:chunked
// sentinel image). Returns the path to the OCI layout dir.
func createOCILayoutWithSentinel(t *testing.T, dir string) {
	t.Helper()

	blobsDir := filepath.Join(dir, "blobs", "sha256")
	require.NoError(t, os.MkdirAll(blobsDir, 0o755))

	// Write oci-layout
	ociLayoutJSON, err := json.Marshal(imgspecv1.ImageLayout{Version: specs.Version})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oci-layout"), ociLayoutJSON, 0o644))

	// Create a real layer (a minimal gzip stream).
	realLayerContent := []byte("real layer data for testing")
	realLayerDigest := digest.FromBytes(realLayerContent)
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, realLayerDigest.Encoded()), realLayerContent, 0o644))

	// Write sentinel blob.
	sentinelContent := chunkedToc.ZstdChunkedSentinelContent
	sentinelDigest := chunkedToc.ZstdChunkedSentinelDigest
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, sentinelDigest.Encoded()), sentinelContent, 0o644))

	// Create config with sentinel DiffID at [0].
	config := imgspecv1.Image{
		RootFS: imgspecv1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{sentinelDigest, realLayerDigest},
		},
	}
	configBlob, err := json.Marshal(config)
	require.NoError(t, err)
	configDigest := digest.FromBytes(configBlob)
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, configDigest.Encoded()), configBlob, 0o644))

	// Create OCI manifest with sentinel layer at [0].
	ociManifest := imgspecv1.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: imgspecv1.MediaTypeImageManifest,
		Config: imgspecv1.Descriptor{
			MediaType: imgspecv1.MediaTypeImageConfig,
			Digest:    configDigest,
			Size:      int64(len(configBlob)),
		},
		Layers: []imgspecv1.Descriptor{
			{
				MediaType: chunkedToc.ZstdChunkedSentinelMediaType,
				Digest:    sentinelDigest,
				Size:      int64(len(sentinelContent)),
			},
			{
				MediaType: imgspecv1.MediaTypeImageLayerZstd,
				Digest:    realLayerDigest,
				Size:      int64(len(realLayerContent)),
			},
		},
	}
	manifestBlob, err := json.Marshal(ociManifest)
	require.NoError(t, err)
	manifestDigest := digest.FromBytes(manifestBlob)
	require.NoError(t, os.WriteFile(filepath.Join(blobsDir, manifestDigest.Encoded()), manifestBlob, 0o644))

	// Create index.json.
	index := imgspecv1.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: imgspecv1.MediaTypeImageIndex,
		Manifests: []imgspecv1.Descriptor{
			{
				MediaType: imgspecv1.MediaTypeImageManifest,
				Digest:    manifestDigest,
				Size:      int64(len(manifestBlob)),
			},
		},
	}
	indexBlob, err := json.Marshal(index)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), indexBlob, 0o644))
}

func TestStripSentinelOnCompressionChange(t *testing.T) {
	srcDir := t.TempDir()
	destDir := t.TempDir()

	createOCILayoutWithSentinel(t, srcDir)

	srcRef, err := ociLayout.ParseReference(srcDir)
	require.NoError(t, err)
	destRef, err := ociLayout.ParseReference(destDir)
	require.NoError(t, err)

	policy := &signature.Policy{Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()}}
	pc, err := signature.NewPolicyContext(policy)
	require.NoError(t, err)
	defer func() { require.NoError(t, pc.Destroy()) }()

	ctx := context.Background()
	gzipAlgo := compression.Gzip
	copiedManifest, err := Image(ctx, pc, destRef, srcRef, &Options{
		DestinationCtx: &types.SystemContext{
			CompressionFormat: &gzipAlgo,
		},
	})
	require.NoError(t, err)

	// Parse the resulting manifest and verify the sentinel layer is gone.
	ociMan, err := manifest.OCI1FromManifest(copiedManifest)
	require.NoError(t, err)

	for i, layer := range ociMan.Layers {
		assert.NotEqual(t, chunkedToc.ZstdChunkedSentinelMediaType, layer.MediaType,
			"sentinel layer should have been stripped, but found at index %d", i)
	}

	// Read the config from the destination and verify sentinel DiffID is gone.
	configBlob, err := os.ReadFile(filepath.Join(destDir, "blobs", "sha256", ociMan.Config.Digest.Encoded()))
	require.NoError(t, err)
	var ociConfig imgspecv1.Image
	require.NoError(t, json.Unmarshal(configBlob, &ociConfig))

	for i, diffID := range ociConfig.RootFS.DiffIDs {
		assert.NotEqual(t, chunkedToc.ZstdChunkedSentinelDigest, diffID,
			"sentinel DiffID should have been stripped, but found at index %d", i)
	}

	// Should have exactly 1 layer (the real one, not the sentinel).
	assert.Len(t, ociMan.Layers, 1, "expected exactly 1 layer after sentinel stripping")
	assert.Len(t, ociConfig.RootFS.DiffIDs, 1, "expected exactly 1 DiffID after sentinel stripping")
}

func TestStripSentinelOnDefaultCompression(t *testing.T) {
	// When no explicit compression format is set, the default (gzip) is used.
	// The sentinel should be stripped since the destination format differs from zstd:chunked.
	srcDir := t.TempDir()
	destDir := t.TempDir()

	createOCILayoutWithSentinel(t, srcDir)

	srcRef, err := ociLayout.ParseReference(srcDir)
	require.NoError(t, err)
	destRef, err := ociLayout.ParseReference(destDir)
	require.NoError(t, err)

	policy := &signature.Policy{Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()}}
	pc, err := signature.NewPolicyContext(policy)
	require.NoError(t, err)
	defer func() { require.NoError(t, pc.Destroy()) }()

	ctx := context.Background()
	copiedManifest, err := Image(ctx, pc, destRef, srcRef, &Options{})
	require.NoError(t, err)

	ociMan, err := manifest.OCI1FromManifest(copiedManifest)
	require.NoError(t, err)

	for i, layer := range ociMan.Layers {
		assert.NotEqual(t, chunkedToc.ZstdChunkedSentinelMediaType, layer.MediaType,
			"sentinel layer should have been stripped, but found at index %d", i)
	}
	assert.Len(t, ociMan.Layers, 1, "expected exactly 1 layer after sentinel stripping")
}
