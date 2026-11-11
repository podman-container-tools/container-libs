package directory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/blobinfocache"
	"go.podman.io/image/v5/internal/digests"
	"go.podman.io/image/v5/internal/imagedestination"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/pkg/blobinfocache/memory"
	"go.podman.io/image/v5/types"
)

var _ private.ImageDestination = (*dirImageDestination)(nil)

func TestVersionAssignment(t *testing.T) {
	for _, contentType := range []struct {
		name string
		put  func(*testing.T, types.ImageDestination, digest.Algorithm, int, types.BlobInfoCache)
	}{
		{
			name: "blob",
			put: func(t *testing.T, dest types.ImageDestination, algo digest.Algorithm, i int, cache types.BlobInfoCache) {
				blobData := []byte("test-blob-" + algo.String() + "-" + string(rune(i)))
				var blobDigest digest.Digest
				if algo == digest.SHA256 {
					blobDigest = ""
				} else {
					blobDigest = algo.FromBytes(blobData)
				}
				_, err := dest.PutBlob(context.Background(), bytes.NewReader(blobData), types.BlobInfo{Digest: blobDigest, Size: int64(len(blobData))}, cache, false)
				require.NoError(t, err)
			},
		},
		{
			name: "manifest",
			put: func(t *testing.T, dest types.ImageDestination, algo digest.Algorithm, i int, cache types.BlobInfoCache) {
				manifestData := []byte("test-manifest-" + algo.String() + "-" + string(rune(i)))
				instanceDigest := algo.FromBytes(manifestData)
				err := dest.PutManifest(context.Background(), manifestData, &instanceDigest)
				require.NoError(t, err)
			},
		},
		{
			name: "signature",
			put: func(t *testing.T, dest types.ImageDestination, algo digest.Algorithm, i int, cache types.BlobInfoCache) {
				manifestData := []byte("test-manifest-" + algo.String() + "-" + string(rune(i)))
				instanceDigest := algo.FromBytes(manifestData)
				// These signatures are completely invalid; start with 0xA3 just to be minimally plausible to signature.FromBlob.
				signatures := [][]byte{[]byte("\xA3sig" + algo.String() + "-" + string(rune(i)))}
				err := dest.PutSignatures(context.Background(), signatures, &instanceDigest)
				require.NoError(t, err)
			},
		},
	} {
		for _, c := range []struct {
			name            string
			algorithms      []digest.Algorithm
			expectedVersion version
		}{
			{
				name:            "SHA256 only gets version 1.1",
				algorithms:      []digest.Algorithm{digest.SHA256},
				expectedVersion: version1_1,
			},
			{
				name:            "SHA512 only gets version 1.2",
				algorithms:      []digest.Algorithm{digest.SHA512},
				expectedVersion: version1_2,
			},
			{
				name:            "Mixed SHA256 and SHA512 gets version 1.2",
				algorithms:      []digest.Algorithm{digest.SHA256, digest.SHA512},
				expectedVersion: version1_2,
			},
		} {
			t.Run(contentType.name+": "+c.name, func(t *testing.T) {
				ref, tmpDir := refToTempDir(t)
				cache := memory.New()

				dest, err := ref.NewImageDestination(context.Background(), nil)
				require.NoError(t, err)
				defer dest.Close()

				for i, algo := range c.algorithms {
					contentType.put(t, dest, algo, i, cache)
				}

				err = dest.Commit(context.Background(), nil)
				require.NoError(t, err)

				versionBytes, err := os.ReadFile(filepath.Join(tmpDir, "version"))
				require.NoError(t, err)
				assert.Equal(t, c.expectedVersion.String(), string(versionBytes))
			})
		}
	}
}

func TestDirImageDestinationReference(t *testing.T) {
	ref, tmpDir := refToTempDir(t)

	dest, err := ref.NewImageDestination(context.Background(), nil)
	require.NoError(t, err)
	defer dest.Close()
	ref2 := dest.Reference()
	assert.Equal(t, tmpDir, ref2.StringWithinTransport())
}

// readerFromFunc allows implementing Reader by any function, e.g. a closure.
type readerFromFunc func([]byte) (int, error)

func (fn readerFromFunc) Read(p []byte) (int, error) {
	return fn(p)
}

func TestDirImageDestinationPutBlobWithOptions(t *testing.T) {
	ref, _ := refToTempDir(t)
	cache := blobinfocache.FromBlobInfoCache(memory.New())

	blobContents := func(i int) []byte {
		return fmt.Appendf(nil, "blob-content-%d", i)
	}
	mustUse := func(algo digest.Algorithm) digests.Options {
		opts, err := digests.MustUse(algo)
		require.NoError(t, err)
		return opts
	}
	prefer := func(algo digest.Algorithm) digests.Options {
		opts, err := digests.CanonicalDefault().WithPreferred(algo)
		require.NoError(t, err)
		return opts
	}
	def := func(algo digest.Algorithm) digests.Options {
		opts, err := digests.CanonicalDefault().WithDefault(algo)
		require.NoError(t, err)
		return opts
	}
	// This set of test cases exercises generic semantics of PutBlobOptions and should be shared
	// across most transports in the future.
	blobs := []struct {
		inputDigestAlgorithm digest.Algorithm // "" if no input digest
		digests              digests.Options
		cannotChangeDigest   string
		expectedAlgorithm    digest.Algorithm // "" if error expected
	}{
		// Caller-provided digest vs. MustUse
		{digest.SHA256, mustUse(digest.SHA256), "", digest.SHA256},
		{digest.SHA256, mustUse(digest.SHA512), "", digest.SHA512},
		{digest.SHA512, mustUse(digest.SHA512), "", digest.SHA512},
		{digest.SHA512, mustUse(digest.SHA256), "", digest.SHA256},
		// Caller-provided digest with CannotChangeDigestReason vs. MustUse
		{digest.SHA256, mustUse(digest.SHA256), "cannot change digest", digest.SHA256},
		{digest.SHA256, mustUse(digest.SHA512), "cannot change digest", ""},
		{digest.SHA512, mustUse(digest.SHA512), "cannot change digest", digest.SHA512},
		{digest.SHA512, mustUse(digest.SHA256), "cannot change digest", ""},
		// Caller-provided digest vs. Prefer
		{digest.SHA256, prefer(digest.SHA256), "", digest.SHA256},
		{digest.SHA256, prefer(digest.SHA512), "", digest.SHA512},
		{digest.SHA512, prefer(digest.SHA512), "", digest.SHA512},
		{digest.SHA512, prefer(digest.SHA256), "", digest.SHA256},
		// Caller-provided digest with CannotChangeDigestReason vs. prefer
		{digest.SHA256, prefer(digest.SHA256), "cannot change digest", digest.SHA256},
		{digest.SHA256, prefer(digest.SHA512), "cannot change digest", digest.SHA256},
		{digest.SHA512, prefer(digest.SHA512), "cannot change digest", digest.SHA512},
		{digest.SHA512, prefer(digest.SHA256), "cannot change digest", digest.SHA512},
		// Caller-provided digest vs. Default
		{digest.SHA256, def(digest.SHA256), "", digest.SHA256},
		{digest.SHA256, def(digest.SHA512), "", digest.SHA256},
		{digest.SHA512, def(digest.SHA512), "", digest.SHA512},
		{digest.SHA512, def(digest.SHA256), "", digest.SHA512},
		// Caller-provided digest with CannotChangeDigestReason vs. Default
		{digest.SHA256, def(digest.SHA256), "cannot change digest", digest.SHA256},
		{digest.SHA256, def(digest.SHA512), "cannot change digest", digest.SHA256},
		{digest.SHA512, def(digest.SHA512), "cannot change digest", digest.SHA512},
		{digest.SHA512, def(digest.SHA256), "cannot change digest", digest.SHA512},

		// Computed digest vs. MustUse
		{"", mustUse(digest.SHA256), "", digest.SHA256},
		{"", mustUse(digest.SHA512), "", digest.SHA512},
		// Computed digest vs. Prefer
		{"", prefer(digest.SHA256), "", digest.SHA256},
		{"", prefer(digest.SHA512), "", digest.SHA512},
		// Computed digest vs. Default
		{"", def(digest.SHA256), "", digest.SHA256},
		{"", def(digest.SHA512), "", digest.SHA512},
	}

	dest_, err := ref.NewImageDestination(context.Background(), nil)
	require.NoError(t, err)
	defer dest_.Close()
	dest := imagedestination.FromPublic(dest_)
	assert.Equal(t, types.PreserveOriginal, dest.DesiredLayerCompression())

	blobDigests := make([]digest.Digest, len(blobs))
	for i, blob := range blobs {
		contents := blobContents(i)
		inputDigest := digest.Digest("")
		if blob.inputDigestAlgorithm != "" {
			inputDigest = blob.inputDigestAlgorithm.FromBytes(contents)
		}

		uploaded, err := dest.PutBlobWithOptions(context.Background(), bytes.NewReader(contents), types.BlobInfo{Digest: inputDigest, Size: int64(len(contents))}, private.PutBlobOptions{
			Cache:                    cache,
			IsConfig:                 false,
			Digests:                  blob.digests,
			CannotChangeDigestReason: blob.cannotChangeDigest,
		})
		if blob.expectedAlgorithm == "" {
			assert.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, int64(len(contents)), uploaded.Size)
			assert.Equal(t, blob.expectedAlgorithm, uploaded.Digest.Algorithm())
			if inputDigest != "" && inputDigest.Algorithm() == blob.expectedAlgorithm {
				assert.Equal(t, inputDigest, uploaded.Digest)
			} else {
				assert.Equal(t, uploaded.Digest.Algorithm().FromBytes(contents), uploaded.Digest)
			}
			blobDigests[i] = uploaded.Digest
		}
	}
	err = dest.Commit(context.Background(), nil) // nil unparsedToplevel is invalid, we don’t currently use the value
	require.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	for i, blob := range blobs {
		if blob.expectedAlgorithm != "" {
			contents := blobContents(i)

			rc, size, err := src.GetBlob(context.Background(), types.BlobInfo{Digest: blobDigests[i], Size: int64(len(contents))}, cache)
			require.NoError(t, err)
			defer rc.Close()
			b, err := io.ReadAll(rc)
			require.NoError(t, err)
			assert.Equal(t, contents, b)
			assert.Equal(t, int64(len(contents)), size)
		}
	}

	t.Run("digest verification failure", func(t *testing.T) {
		const digestErrorString = "Simulated digest error"
		const blobDigest = digest.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

		ref, _ := refToTempDir(t)
		dirRef, ok := ref.(dirReference)
		require.True(t, ok)
		blobPath, err := dirRef.layerPath(blobDigest) // The file at blobPath must never be created until we validate the digest.
		require.NoError(t, err)
		cache := memory.New()

		firstRead := true
		reader := readerFromFunc(func(p []byte) (int, error) {
			_, err := os.Lstat(blobPath)
			require.Error(t, err)
			require.True(t, os.IsNotExist(err))
			if firstRead {
				if len(p) > 0 {
					firstRead = false
				}
				for i := range p {
					p[i] = 0xAA
				}
				return len(p), nil
			}
			return 0, errors.New(digestErrorString)
		})

		dest, err := ref.NewImageDestination(context.Background(), nil)
		require.NoError(t, err)
		defer dest.Close()
		_, err = dest.PutBlob(context.Background(), reader, types.BlobInfo{Digest: blobDigest, Size: -1}, cache, false)
		assert.ErrorContains(t, err, digestErrorString)
		err = dest.Commit(context.Background(), nil) // nil unparsedToplevel is invalid, we don’t currently use the value
		assert.NoError(t, err)

		_, err = os.Lstat(blobPath)
		require.Error(t, err)
		require.True(t, os.IsNotExist(err))
	})
}
