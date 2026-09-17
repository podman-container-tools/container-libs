package blobcache

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	cp "go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/internal/image"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/pkg/compression"
	compressionTypes "go.podman.io/image/v5/pkg/compression/types"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/types"
	"go.podman.io/storage/pkg/archive"
)

var (
	_ types.ImageReference     = &BlobCache{}
	_ types.ImageSource        = &blobCacheSource{}
	_ private.ImageSource      = (*blobCacheSource)(nil)
	_ types.ImageDestination   = &blobCacheDestination{}
	_ private.ImageDestination = (*blobCacheDestination)(nil)
)

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Verbose() {
		logrus.SetLevel(logrus.DebugLevel)
	}
	os.Exit(m.Run())
}

// Create a layer containing a single file with the specified name (and its
// name as its contents), compressed using the specified compression type, and
// return the .
func makeLayer(filename string, repeat int, compression archive.Compression) ([]byte, digest.Digest, error) {
	var compressed, uncompressed bytes.Buffer
	layer, err := archive.Generate(filename, strings.Repeat(filename, repeat))
	if err != nil {
		return nil, "", err
	}
	writer, err := archive.CompressStream(&compressed, compression)
	if err != nil {
		return nil, "", err
	}
	reader := io.TeeReader(layer, &uncompressed)
	_, err = io.Copy(writer, reader)
	writer.Close()
	if err != nil {
		return nil, "", err
	}
	return compressed.Bytes(), digest.FromBytes(uncompressed.Bytes()), nil
}

func pushImageThroughCache(t *testing.T, cacheDir string, blobBytes []byte, diffID digest.Digest, layerMediaType string, desiredCompression types.LayerCompression, systemContext *types.SystemContext) (*BlobCache, string) {
	t.Helper()
	blobInfo := types.BlobInfo{
		Digest: digest.FromBytes(blobBytes),
		Size:   int64(len(blobBytes)),
	}
	// Create a configuration that includes the diffID for the layer and not much else.
	config := v1.Image{
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{diffID},
		},
	}
	configBytes, err := json.Marshal(&config)
	if err != nil {
		t.Fatalf("error encoding image configuration: %v", err)
	}
	configInfo := types.BlobInfo{
		Digest: digest.FromBytes(configBytes),
		Size:   int64(len(configBytes)),
	}
	m := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     v1.MediaTypeImageManifest,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    configInfo.Digest,
			Size:      configInfo.Size,
		},
		Layers: []v1.Descriptor{{
			MediaType: layerMediaType,
			Digest:    blobInfo.Digest,
			Size:      blobInfo.Size,
		}},
	}
	manifestBytes, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("error encoding manifest: %v", err)
	}

	srcdir := t.TempDir()
	srcRef, err := directory.NewReference(srcdir)
	if err != nil {
		t.Fatalf("error creating source reference: %v", err)
	}
	cachedRef, err := NewBlobCache(srcRef, cacheDir, desiredCompression)
	if err != nil {
		t.Fatalf("error creating blob cache: %v", err)
	}
	destImage, err := cachedRef.NewImageDestination(context.TODO(), nil)
	if err != nil {
		t.Fatalf("error opening destination: %v", err)
	}
	_, err = destImage.PutBlob(context.TODO(), bytes.NewReader(blobBytes), blobInfo, none.NoCache, false)
	if err != nil {
		t.Fatalf("error writing layer blob: %v", err)
	}
	_, err = destImage.PutBlob(context.TODO(), bytes.NewReader(configBytes), configInfo, none.NoCache, true)
	if err != nil {
		t.Fatalf("error writing config blob: %v", err)
	}
	srcImage, err := srcRef.NewImageSource(context.TODO(), systemContext)
	if err != nil {
		t.Fatalf("error opening source: %v", err)
	}
	defer srcImage.Close()
	err = destImage.PutManifest(context.TODO(), manifestBytes, nil)
	if err != nil {
		t.Fatalf("error writing manifest: %v", err)
	}
	err = destImage.Commit(context.TODO(), image.UnparsedInstance(srcImage, nil))
	if err != nil {
		t.Fatalf("error committing: %v", err)
	}
	if err = destImage.Close(); err != nil {
		t.Fatalf("error closing destination: %v", err)
	}
	return cachedRef, srcdir
}

type testCaseBlobCache struct {
	desiredCompression types.LayerCompression
	layerCompression   archive.Compression
}

func (tc *testCaseBlobCache) layerCompressionOCIType() (string, string) {
	switch tc.layerCompression {
	case archive.Gzip:
		return v1.MediaTypeImageLayerGzip, compressionTypes.GzipAlgorithmName
	case archive.Zstd:
		return v1.MediaTypeImageLayerZstd, compressionTypes.ZstdAlgorithmName
	default:
		return v1.MediaTypeImageLayer, ""
	}
}

func (tc *testCaseBlobCache) desiredCompressionString() string {
	switch tc.desiredCompression {
	case types.PreserveOriginal:
		return "PreserveOriginal"
	case types.Compress:
		return "Compress"
	case types.Decompress:
		return "Decompress"
	default:
		panic(fmt.Sprintf("unknown desired compression: %d", tc.desiredCompression))
	}
}

func TestBlobCache(t *testing.T) {
	cacheDir := t.TempDir()

	systemContext := types.SystemContext{BlobInfoCacheDir: "/dev/null/this/does/not/exist"}

	var testCases []testCaseBlobCache
	for _, desiredCompression := range []types.LayerCompression{types.PreserveOriginal, types.Compress, types.Decompress} {
		for _, layerCompression := range []archive.Compression{archive.Uncompressed, archive.Gzip, archive.Zstd} {
			testCases = append(testCases, testCaseBlobCache{
				desiredCompression: desiredCompression,
				layerCompression:   layerCompression,
			})
		}
	}

	for _, repeat := range []int{1, 10000} {
		for _, tc := range testCases {
			layerMediaType, expectedAlgoName := tc.layerCompressionOCIType()

			t.Run(fmt.Sprintf("repeat=%d/desiredCompression=%s/layerCompression=%s", repeat, tc.desiredCompressionString(), expectedAlgoName), func(t *testing.T) {
				// Create a layer with the specified layerCompression.
				blobBytes, diffID, err := makeLayer(fmt.Sprintf("layer-content-%d", int(tc.layerCompression)), repeat, tc.layerCompression)
				if err != nil {
					t.Fatalf("error making layer: %v", err)
				}

				cachedSrcRef, srcdir := pushImageThroughCache(t, cacheDir, blobBytes, diffID, layerMediaType, tc.desiredCompression, &systemContext)

				// Check that the cache was populated.
				cachedNames, err := os.ReadDir(cacheDir)
				if err != nil {
					t.Fatal(err)
				}
				// Expect a layer blob, a config blob, and the manifest.
				expected := 3
				if tc.layerCompression != archive.Uncompressed {
					// Expect a compressed blob, an uncompressed blob, notes for each about the other, a config blob, and the manifest.
					expected = 6
				}
				if len(cachedNames) != expected {
					t.Fatalf("expected %d items in cache directory, got %d: %v", expected, len(cachedNames), cachedNames)
				}

				// Check that the blobs were all correctly stored.
				for _, de := range cachedNames {
					cachedName := de.Name()
					if digest.Digest(cachedName).Validate() == nil {
						cacheMemberBytes, err := os.ReadFile(filepath.Join(cacheDir, cachedName))
						if err != nil {
							t.Fatal(err)
						}
						if digest.FromBytes(cacheMemberBytes).String() != cachedName {
							t.Fatalf("cache member %q was stored incorrectly!", cachedName)
						}
					}
				}

				// Verify the .compressed note records the correct algorithm.
				if tc.layerCompression != archive.Uncompressed {
					noteFilePath := filepath.Join(cacheDir, diffID.String()) + compressedNote
					entries, err := parseCompressedNote(noteFilePath)
					if err != nil {
						t.Fatalf("error reading %s note: %v", compressedNote, err)
					}
					blobDigest := digest.FromBytes(blobBytes).String()
					if algoName, ok := entries[blobDigest]; !ok || algoName != expectedAlgoName {
						t.Fatalf("expected entry %q=%q in note, got entries: %v", blobDigest, expectedAlgoName, entries)
					}
				}

				// Clear out anything in the source directory that probably isn't a manifest, so that we'll
				// have to depend on the cached copies of some of the blobs.
				srcNames, err := os.ReadDir(srcdir)
				if err != nil {
					t.Fatal(err)
				}
				for _, de := range srcNames {
					if name := de.Name(); !strings.HasPrefix(name, "manifest") {
						os.Remove(filepath.Join(srcdir, name))
					}
				}

				// Copying without the cache should fail because blobs are missing.
				srcRef, err := directory.NewReference(srcdir)
				if err != nil {
					t.Fatalf("error creating source reference: %v", err)
				}
				destRef, err := directory.NewReference(t.TempDir())
				if err != nil {
					t.Fatalf("error creating destination reference: %v", err)
				}
				options := cp.Options{
					SourceCtx:      &systemContext,
					DestinationCtx: &systemContext,
				}
				policyContext, err := signature.NewPolicyContext(&signature.Policy{
					Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
				})
				if err != nil {
					t.Fatalf("error creating policy context: %v", err)
				}
				_, err = cp.Image(context.TODO(), policyContext, destRef, srcRef, &options)
				if err == nil {
					t.Fatalf("expected an error copying without cache, but got success")
				}

				// Copying with the cache should succeed.
				destRef, err = directory.NewReference(t.TempDir())
				if err != nil {
					t.Fatalf("error creating destination reference: %v", err)
				}
				_, err = cp.Image(context.TODO(), policyContext, destRef, cachedSrcRef, &options)
				if err != nil {
					t.Fatalf("unexpected error copying with cache: %v", err)
				}

				if err = cachedSrcRef.ClearCache(); err != nil {
					t.Fatalf("error clearing cache: %v", err)
				}
			})
		}
	}
}

func TestBlobCacheMultipleCompressedVersions(t *testing.T) {
	// Prepare cache with all compressed versions of the same layer.
	cacheDir := t.TempDir()
	systemContext := types.SystemContext{BlobInfoCacheDir: "/dev/null/this/does/not/exist"}

	layerContent := "multi-compression-layer"

	// uncompressed
	uncompressedBytes, diffID, err := makeLayer(layerContent, 1, archive.Uncompressed)
	if err != nil {
		t.Fatalf("error making uncompressed layer: %v", err)
	}
	_, uncompressedSrcdir := pushImageThroughCache(t, cacheDir, uncompressedBytes, diffID, v1.MediaTypeImageLayer, types.PreserveOriginal, &systemContext)
	uncompressedDigest := digest.FromBytes(uncompressedBytes)
	if uncompressedDigest != diffID {
		t.Fatalf("diffIDs should match: uncompressed=%s uncompressedDigest=%s", diffID, uncompressedDigest)
	}

	// gzip
	gzipBytes, gzipDiffID, err := makeLayer(layerContent, 1, archive.Gzip)
	if err != nil {
		t.Fatalf("error making gzip layer: %v", err)
	}
	if diffID != gzipDiffID {
		t.Fatalf("diffIDs should match: uncompressed=%s gzip=%s", diffID, gzipDiffID)
	}
	pushImageThroughCache(t, cacheDir, gzipBytes, diffID, v1.MediaTypeImageLayerGzip, types.PreserveOriginal, &systemContext)
	gzipDigest := digest.FromBytes(gzipBytes)

	// zstd
	zstdBytes, zstdDiffID, err := makeLayer(layerContent, 1, archive.Zstd)
	if err != nil {
		t.Fatalf("error making zstd layer: %v", err)
	}
	if gzipDiffID != zstdDiffID {
		t.Fatalf("diffIDs should match: gzip=%s zstd=%s", diffID, zstdDiffID)
	}
	pushImageThroughCache(t, cacheDir, zstdBytes, diffID, v1.MediaTypeImageLayerZstd, types.PreserveOriginal, &systemContext)
	zstdDigest := digest.FromBytes(zstdBytes)

	// Verify the .compressed note has both entries.
	noteFilePath := filepath.Join(cacheDir, diffID.String()) + compressedNote
	entries, err := parseCompressedNote(noteFilePath)
	if err != nil {
		t.Fatalf("error reading compressed note: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in compressed note, got %d: %v", len(entries), entries)
	}
	if algo, ok := entries[gzipDigest.String()]; !ok || algo != compressionTypes.GzipAlgorithmName {
		t.Fatalf("missing or wrong gzip entry in compressed note; entries: %v", entries)
	}
	if algo, ok := entries[zstdDigest.String()]; !ok || algo != compressionTypes.ZstdAlgorithmName {
		t.Fatalf("missing or wrong zstd entry in compressed note; entries: %v", entries)
	}

	// Search for the blobs in the cache.
	for _, tc := range []struct {
		name         string
		compress     types.LayerCompression
		algo         *compression.Algorithm
		expectDigest digest.Digest
		expectMedia  string
	}{
		{"search for gzip", types.Compress, &compression.Gzip, gzipDigest, v1.MediaTypeImageLayerGzip},
		{"search for zstd", types.Compress, &compression.Zstd, zstdDigest, v1.MediaTypeImageLayerZstd},
		{"search for default compression", types.Compress, nil, gzipDigest, v1.MediaTypeImageLayerGzip},
		{"search for uncompressed", types.Decompress, nil, diffID, v1.MediaTypeImageLayer},
		{"search for preserve original", types.PreserveOriginal, nil, diffID, v1.MediaTypeImageLayer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := directory.NewReference(uncompressedSrcdir)
			if err != nil {
				t.Fatalf("error creating directory reference: %v", err)
			}
			var opts []BlobCacheOption
			if tc.algo != nil {
				opts = append(opts, WithCompressAlgorithm(tc.algo))
			}
			cachedRef, err := NewBlobCache(ref, cacheDir, tc.compress, opts...)
			if err != nil {
				t.Fatalf("error creating blob cache: %v", err)
			}
			src, err := cachedRef.NewImageSource(context.TODO(), &systemContext)
			if err != nil {
				t.Fatalf("error opening image source: %v", err)
			}
			defer src.Close()

			layerInfos, err := src.LayerInfosForCopy(context.TODO(), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(layerInfos) != 1 {
				t.Fatalf("expected 1 layer info, got %d", len(layerInfos))
			}

			result := layerInfos[0]
			if result.Digest != tc.expectDigest {
				t.Fatalf("expected digest %s, got %s", tc.expectDigest.String(), result.Digest.String())
			}
			if result.MediaType != tc.expectMedia {
				t.Fatalf("expected media type %s, got %s", tc.expectMedia, result.MediaType)
			}
		})
	}
}
