package libartifact

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specV1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/common/libimage"
	libartTypes "go.podman.io/common/pkg/libartifact/types"
	"go.podman.io/image/v5/oci/layout"
	"go.podman.io/image/v5/types"
)

const (
	ArtifactTestMimeType        = "application/vnd.test+type"
	ArtifactReplaceTestMimeType = "application/vnd.replaced+type"
)

// randomAlphanumeric generates a random alphanumeric string of the specified length.
func randomAlphanumeric(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	randomBytes := make([]byte, length)

	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	for i, b := range randomBytes {
		result[i] = charset[int(b)%len(charset)]
	}

	return string(result), nil
}

// setupTestStore creates a new empty artifact store for testing.
func setupTestStore(t *testing.T) (*ArtifactStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "store")
	sc := &types.SystemContext{}

	as, err := NewArtifactStore(storePath, sc)
	require.NoError(t, err)
	require.NotNil(t, as)

	return as, ctx
}

// createTestBlob creates a temporary file with random content and returns an ArtifactBlob.
func createTestBlob(t *testing.T, fileName string, size int) (libartTypes.ArtifactBlob, [32]byte) {
	t.Helper()

	// Generate random content
	content := make([]byte, size)
	_, err := rand.Read(content)
	require.NoError(t, err)

	// Create temporary file
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, fileName)
	err = os.WriteFile(filePath, content, 0o644)
	require.NoError(t, err)

	return libartTypes.ArtifactBlob{
		BlobFilePath: filePath,
		FileName:     fileName,
	}, sha256.Sum256(content)
}

// helperAddArtifact is a test helper that adds an artifact to the store.
// It creates temporary files with random content and adds them as blobs.
// fileNames maps filename to size in bytes of random content to generate.
// If options is nil, uses a default with ArtifactMIMEType set to "application/vnd.test+type".
func helperAddArtifact(t *testing.T, as *ArtifactStore, refName string, fileNames map[string]int, options *libartTypes.AddOptions) (*digest.Digest, map[string][32]byte) {
	t.Helper()
	ctx := context.Background()

	// If options is nil, create default options
	if options == nil {
		options = &libartTypes.AddOptions{
			ArtifactMIMEType: ArtifactTestMimeType,
		}
	}

	// if no specific files were passed, create a random file
	if fileNames == nil {
		filename, err := randomAlphanumeric(5)
		require.NoError(t, err)
		fileNames = map[string]int{
			filename: 2,
		}
	}

	// Create artifact reference
	ref, err := NewArtifactReference(refName)
	require.NoError(t, err)

	// Create artifact blobs
	blobs := make([]libartTypes.ArtifactBlob, 0, len(fileNames))
	checkSums := make(map[string][32]byte, len(fileNames))
	for fileName, size := range fileNames {
		blob, checkSum256 := createTestBlob(t, fileName, size)
		blobs = append(blobs, blob)
		checkSums[fileName] = checkSum256
	}

	// Add artifact
	artifactDigest, err := as.Add(ctx, ref, blobs, options)
	require.NoError(t, err)
	require.NotNil(t, artifactDigest)

	return artifactDigest, checkSums
}

func TestNewArtifactStore(t *testing.T) {
	// Test with valid absolute path
	storePath := filepath.Join(t.TempDir(), "store")
	sc := &types.SystemContext{}

	as, err := NewArtifactStore(storePath, sc)
	assert.NoError(t, err)
	assert.NotNil(t, as)
	assert.Equal(t, storePath, as.storePath)

	// Verify the index file was created
	indexPath := filepath.Join(storePath, "index.json")
	_, err = os.Stat(indexPath)
	assert.NoError(t, err)

	// Test with empty path
	_, err = NewArtifactStore("", sc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "store path cannot be empty")

	// Test with relative path
	_, err = NewArtifactStore("relative/path", sc)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must be absolute")
}

func TestArtifactStore_Add(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add artifact using helper with nil options (uses default)
	fileNames := map[string]int{
		"testfile.txt": 1024,
	}

	refName := "quay.io/test/artifact:v1"
	artifactDigest, _ := helperAddArtifact(t, as, refName, fileNames, nil)
	assert.NotEmpty(t, artifactDigest.String())

	// Verify artifact was added to the store
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	// Verify artifact properties
	artifact := artifacts[0]
	assert.Equal(t, refName, artifact.Name)
	assert.Equal(t, ArtifactTestMimeType, artifact.Manifest.ArtifactType)
	assert.Len(t, artifact.Manifest.Layers, 1)

	// Append another file to the same artifact
	appendFileNames := map[string]int{
		"appended.txt": 512,
	}
	appendOptions := &libartTypes.AddOptions{
		Append: true,
	}

	appendDigest, _ := helperAddArtifact(t, as, refName, appendFileNames, appendOptions)
	assert.NotEmpty(t, appendDigest.String())

	// Verify artifact now has 2 layers
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	artifact = artifacts[0]
	assert.Len(t, artifact.Manifest.Layers, 2)

	// Verify both files are present
	foundFiles := make(map[string]bool)
	for _, layer := range artifact.Manifest.Layers {
		title := layer.Annotations[specV1.AnnotationTitle]
		foundFiles[title] = true
	}
	assert.True(t, foundFiles["testfile.txt"])
	assert.True(t, foundFiles["appended.txt"])

	// Replace the artifact with a completely new one
	replaceFileNames := map[string]int{
		"replacement.bin": 2048,
	}
	replaceOptions := &libartTypes.AddOptions{
		Replace:          true,
		ArtifactMIMEType: ArtifactReplaceTestMimeType,
	}

	replaceDigest, _ := helperAddArtifact(t, as, refName, replaceFileNames, replaceOptions)
	assert.NotEmpty(t, replaceDigest.String())

	// Verify artifact was replaced with only the new file
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	artifact = artifacts[0]
	assert.Len(t, artifact.Manifest.Layers, 1)
	assert.Equal(t, ArtifactReplaceTestMimeType, artifact.Manifest.ArtifactType)

	// Verify only the replacement file is present
	assert.Equal(t, "replacement.bin", artifact.Manifest.Layers[0].Annotations[specV1.AnnotationTitle])
}

func TestArtifactStore_Add_MultipleFiles(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add artifact with multiple files using helper with nil options (uses default)
	fileNames := map[string]int{
		"file1.txt": 512,
		"file2.bin": 1024,
		"file3.dat": 2048,
	}
	refName := "quay.io/test/multifile:v1"
	artifactDigest, _ := helperAddArtifact(t, as, refName, fileNames, nil)
	assert.NotEmpty(t, artifactDigest.String())

	// Verify artifact was added to the store
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	// Verify artifact has 3 artifact files
	artifact := artifacts[0]
	assert.Equal(t, refName, artifact.Name)
	assert.Equal(t, ArtifactTestMimeType, artifact.Manifest.ArtifactType)
	assert.Len(t, artifact.Manifest.Layers, 3)

	// Verify all file names are present in layer annotations
	foundFiles := make(map[string]bool)
	for _, layer := range artifact.Manifest.Layers {
		title := layer.Annotations[specV1.AnnotationTitle]
		foundFiles[title] = true
	}

	// Ensure all the files exist by same name
	for f := range fileNames {
		assert.True(t, foundFiles[f], "file %s not found in artifact", f)
	}

	// Verify layer sizes match expected sizes
	for _, layer := range artifact.Manifest.Layers {
		title := layer.Annotations[specV1.AnnotationTitle]
		expectedSize := int64(fileNames[title])
		assert.Equal(t, expectedSize, layer.Size, "Layer size for %s should match", title)
	}
}

func TestArtifactStore_Add_CustomMIMEType(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add artifact with custom MIME type
	fileNames := map[string]int{
		"config.json": 256,
	}
	options := &libartTypes.AddOptions{
		ArtifactMIMEType: "application/vnd.custom+json",
	}

	artifactDigest, _ := helperAddArtifact(t, as, "quay.io/test/custom:v1", fileNames, options)
	assert.NotEmpty(t, artifactDigest.String())

	// Verify artifact uses custom MIME type
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	artifact := artifacts[0]
	assert.Equal(t, "application/vnd.custom+json", artifact.Manifest.ArtifactType)
}

func TestArtifactStore_Add_ReplaceNonExistent(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Verify store is empty
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, artifacts)

	// Try to Replace an artifact that doesn't exist yet
	// This should succeed and create a new artifact (not error)
	fileNames := map[string]int{
		"newfile.txt": 1024,
	}
	replaceOptions := &libartTypes.AddOptions{
		Replace:          true,
		ArtifactMIMEType: ArtifactTestMimeType,
	}

	refName := "quay.io/test/nonexistent:v1"
	artifactDigest, _ := helperAddArtifact(t, as, refName, fileNames, replaceOptions)
	assert.NotEmpty(t, artifactDigest.String())

	// Verify artifact was created successfully
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	artifact := artifacts[0]
	assert.Equal(t, refName, artifact.Name)
	assert.Equal(t, ArtifactTestMimeType, artifact.Manifest.ArtifactType)
	assert.Len(t, artifact.Manifest.Layers, 1)
	assert.Equal(t, "newfile.txt", artifact.Manifest.Layers[0].Annotations[specV1.AnnotationTitle])
}

func TestArtifactStore_Add_AppendAndReplaceConflict(t *testing.T) {
	as, ctx := setupTestStore(t)

	// First add an artifact normally
	fileNames := map[string]int{
		"testfile.txt": 1024,
	}
	refName := "quay.io/test/conflict:v1"
	artifactDigest, _ := helperAddArtifact(t, as, refName, fileNames, nil)
	assert.NotEmpty(t, artifactDigest.String())

	// Try to use both Append and Replace at the same time
	// This should fail with an error
	conflictFileNames := map[string]int{
		"conflict.txt": 512,
	}
	conflictOptions := &libartTypes.AddOptions{
		Append:  true,
		Replace: true,
	}

	ref, err := NewArtifactReference(refName)
	require.NoError(t, err)

	// Create artifact blobs
	blobs := make([]libartTypes.ArtifactBlob, 0, len(conflictFileNames))
	for fileName, size := range conflictFileNames {
		blob, _ := createTestBlob(t, fileName, size)
		blobs = append(blobs, blob)
	}

	// This should return an error about mutually exclusive options
	_, err = as.Add(ctx, ref, blobs, conflictOptions)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append and replace options are mutually exclusive")
}

func TestArtifactStore_Add_ReplaceChangesDigest(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add initial artifact
	fileNames := map[string]int{
		"original.txt": 1024,
	}
	refName := "quay.io/test/digest-change:v1"
	originalDigest, _ := helperAddArtifact(t, as, refName, fileNames, nil)
	assert.NotEmpty(t, originalDigest.String())

	// Get the artifact to verify original digest
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	firstArtifactDigest := artifacts[0].Digest
	assert.Equal(t, originalDigest.String(), firstArtifactDigest.String())

	// Replace the artifact with different content
	replaceFileNames := map[string]int{
		"replaced.txt": 2048,
	}
	replaceOptions := &libartTypes.AddOptions{
		Replace:          true,
		ArtifactMIMEType: ArtifactReplaceTestMimeType,
	}

	replacedDigest, _ := helperAddArtifact(t, as, refName, replaceFileNames, replaceOptions)
	assert.NotEmpty(t, replacedDigest.String())

	// Verify the digest changed (it's a new artifact)
	assert.NotEqual(t, originalDigest.String(), replacedDigest.String(),
		"Replace should create a new artifact with a different digest")

	// Verify only one artifact exists with the new digest
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	finalArtifactDigest := artifacts[0].Digest
	assert.Equal(t, replacedDigest.String(), finalArtifactDigest.String(),
		"The artifact in the store should have the new digest")
	assert.NotEqual(t, firstArtifactDigest.String(), finalArtifactDigest.String(),
		"The old digest should no longer be in the store")
}

func TestArtifactStore_Remove(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add multiple artifacts
	fileNames1 := map[string]int{
		"file1.txt": 1024,
	}
	helperAddArtifact(t, as, "quay.io/test/artifact1:v1", fileNames1, nil)

	fileNames2 := map[string]int{
		"file2.txt": 2048,
	}
	helperAddArtifact(t, as, "quay.io/test/artifact2:v1", fileNames2, nil)

	// Verify both artifacts exist
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 2)

	// Get the first artifact and create a reference with it
	artifact1 := artifacts[0]
	digest1 := artifact1.Digest

	// Remove the first artifact by digest
	ref, err := NewArtifactStorageReference(digest1.Encoded())
	require.NoError(t, err)

	removedDigest, err := as.Remove(ctx, ref)
	require.NoError(t, err)
	require.NotNil(t, removedDigest)
	assert.NotEmpty(t, removedDigest.String())

	// Verify only one artifact remains
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 1)

	// Get the remaining artifact
	artifact2 := artifacts[0]
	digest2 := artifact2.Digest

	// Remove the second artifact by digest
	ref2, err := NewArtifactStorageReference(digest2.Encoded())
	require.NoError(t, err)

	removedDigest2, err := as.Remove(ctx, ref2)
	require.NoError(t, err)
	require.NotNil(t, removedDigest2)

	// Verify store is now empty
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, artifacts)
}

func TestArtifactStore_Inspect(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add an artifact with multiple files
	fileNames := map[string]int{
		"file1.txt": 512,
		"file2.bin": 1024,
		"file3.dat": 2048,
	}
	options := &libartTypes.AddOptions{
		ArtifactMIMEType: ArtifactTestMimeType,
		Annotations: map[string]string{
			"custom.annotation": "test-value",
		},
	}

	refName := "quay.io/test/inspect:v1"
	helperAddArtifact(t, as, refName, fileNames, options)

	// Get the artifact from the list
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	// Create a reference using the artifact's digest
	artifact := artifacts[0]
	digest := artifact.Digest

	ref, err := NewArtifactStorageReference(digest.Encoded())
	require.NoError(t, err)

	// Inspect the artifact
	inspectedArtifact, err := as.Inspect(ctx, ref)
	require.NoError(t, err)
	require.NotNil(t, inspectedArtifact)

	// Verify inspected artifact properties
	assert.Equal(t, refName, inspectedArtifact.Name)
	assert.Equal(t, ArtifactTestMimeType, inspectedArtifact.Manifest.ArtifactType)
	assert.Len(t, inspectedArtifact.Manifest.Layers, 3)

	// Verify custom annotation is present
	assert.Equal(t, "test-value", inspectedArtifact.Manifest.Annotations["custom.annotation"])

	// Verify all files are present in layers
	foundFiles := make(map[string]int64)
	for _, layer := range inspectedArtifact.Manifest.Layers {
		title := layer.Annotations[specV1.AnnotationTitle]
		foundFiles[title] = layer.Size
	}
	assert.Equal(t, int64(512), foundFiles["file1.txt"])
	assert.Equal(t, int64(1024), foundFiles["file2.bin"])
	assert.Equal(t, int64(2048), foundFiles["file3.dat"])

	// Verify total size calculation
	totalSize := inspectedArtifact.TotalSizeBytes()
	expectedTotal := int64(512 + 1024 + 2048)
	assert.Equal(t, expectedTotal, totalSize)

	// Test inspecting by digest reference format (repo@digest)
	// This tests the fix for issue #408 - should be able to inspect by digest
	// after adding by tag
	digestRef := "quay.io/test/inspect@" + digest.String()
	refByDigest, err := NewArtifactStorageReference(digestRef)
	require.NoError(t, err)

	inspectedByDigest, err := as.Inspect(ctx, refByDigest)
	require.NoError(t, err, "should be able to inspect artifact by digest reference after adding by tag")
	require.NotNil(t, inspectedByDigest)

	// Verify it's the same artifact
	assert.Equal(t, refName, inspectedByDigest.Name)
	assert.Len(t, inspectedByDigest.Manifest.Layers, 3)

	// Verify the digest matches
	inspectedDigest := inspectedByDigest.Digest
	assert.Equal(t, digest.String(), inspectedDigest.String())
}

func TestArtifactStore_Extract(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add an artifact with multiple files
	fileNames := map[string]int{
		"file1.txt": 512,
		"file2.bin": 1024,
		"file3.dat": 2048,
	}

	_, checkSums := helperAddArtifact(t, as, "quay.io/test/extract:v1", fileNames, nil)

	// Get the artifact from the list
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	// Create a reference using the artifact's digest
	artifact := artifacts[0]

	ref, err := NewArtifactStorageReference(artifact.Digest.Encoded())
	require.NoError(t, err)

	// Extract to a directory
	extractDir := t.TempDir()
	err = as.Extract(ctx, ref, extractDir, &libartTypes.ExtractOptions{})
	require.NoError(t, err)

	for f := range fileNames {
		content, err := os.ReadFile(filepath.Join(extractDir, f))
		require.NoError(t, err)
		checkSumoExtractedArt := sha256.Sum256(content)
		assert.Equal(t, checkSums[f], checkSumoExtractedArt)
	}
}

func TestArtifactStore_Extract_SingleFile(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Add an artifact with multiple files
	fileNames := map[string]int{
		"file1.txt": 512,
		"file2.bin": 1024,
	}

	helperAddArtifact(t, as, "quay.io/test/extract-single:v1", fileNames, nil)

	// Get the artifact from the list
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	// Create a reference using the artifact's digest
	artifact := artifacts[0]
	ref, err := NewArtifactStorageReference(artifact.Digest.Encoded())
	require.NoError(t, err)

	// Extract only one file by title
	extractDir := t.TempDir()
	err = as.Extract(ctx, ref, extractDir, &libartTypes.ExtractOptions{
		FilterBlobOptions: libartTypes.FilterBlobOptions{
			Title: "file1.txt",
		},
	})
	require.NoError(t, err)

	// Verify only file1.txt was extracted
	extractedFile1 := filepath.Join(extractDir, "file1.txt")
	extractedFile2 := filepath.Join(extractDir, "file2.bin")

	stat1, err := os.Stat(extractedFile1)
	require.NoError(t, err)
	assert.Equal(t, int64(512), stat1.Size())

	_, err = os.Stat(extractedFile2)
	assert.True(t, os.IsNotExist(err))
}

func TestArtifactStore_List_Multiple(t *testing.T) {
	as, ctx := setupTestStore(t)

	// Verify empty store returns empty list
	artifacts, err := as.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, artifacts)

	// Add multiple artifacts with different configurations
	fileNames1 := map[string]int{
		"file1.txt": 512,
	}
	helperAddArtifact(t, as, "quay.io/test/artifact1:v1", fileNames1, nil)

	fileNames2 := map[string]int{
		"file2a.bin": 1024,
		"file2b.dat": 2048,
	}
	options2 := &libartTypes.AddOptions{
		ArtifactMIMEType: "application/vnd.custom+type",
	}
	helperAddArtifact(t, as, "quay.io/test/artifact2:v2", fileNames2, options2)

	fileNames3 := map[string]int{
		"file3.json": 256,
	}
	helperAddArtifact(t, as, "docker.io/library/artifact3:latest", fileNames3, nil)

	// List all artifacts
	artifacts, err = as.List(ctx)
	require.NoError(t, err)
	assert.Len(t, artifacts, 3)

	// Create a map of artifact names for easy lookup
	artifactMap := make(map[string]*Artifact)
	for _, artifact := range artifacts {
		artifactMap[artifact.Name] = artifact
	}

	// Verify first artifact
	artifact1, exists := artifactMap["quay.io/test/artifact1:v1"]
	require.True(t, exists)
	assert.Equal(t, ArtifactTestMimeType, artifact1.Manifest.ArtifactType)
	assert.Len(t, artifact1.Manifest.Layers, 1)
	assert.Equal(t, int64(512), artifact1.TotalSizeBytes())

	// Verify second artifact
	artifact2, exists := artifactMap["quay.io/test/artifact2:v2"]
	require.True(t, exists)
	assert.Equal(t, "application/vnd.custom+type", artifact2.Manifest.ArtifactType)
	assert.Len(t, artifact2.Manifest.Layers, 2)
	assert.Equal(t, int64(3072), artifact2.TotalSizeBytes())

	// Verify third artifact
	artifact3, exists := artifactMap["docker.io/library/artifact3:latest"]
	require.True(t, exists)
	assert.Equal(t, ArtifactTestMimeType, artifact3.Manifest.ArtifactType)
	assert.Len(t, artifact3.Manifest.Layers, 1)
	assert.Equal(t, int64(256), artifact3.TotalSizeBytes())

	// Verify all artifacts have valid digests
	for _, artifact := range artifacts {
		assert.NotEmpty(t, artifact.Digest.String())
	}
}

func TestArtifactStore_List_BareTagAnnotation(t *testing.T) {
	bareTags := []string{"v1", "latest", "1.14.4"}

	for _, bareTag := range bareTags {
		t.Run(bareTag, func(t *testing.T) {
			as, ctx := setupTestStore(t)

			// Add an artifact normally first, so the store has valid blobs
			fileNames := map[string]int{"test.txt": 64}
			refName := "quay.io/test/artifact:v1"
			helperAddArtifact(t, as, refName, fileNames, nil)

			// Rewrite the index.json to simulate an externally created OCI
			// layout (like skopeo) that only stores the tag in the annotation
			indexPath := filepath.Join(as.storePath, "index.json")
			indexData, err := os.ReadFile(indexPath)
			require.NoError(t, err)

			var index specV1.Index
			require.NoError(t, json.Unmarshal(indexData, &index))

			for i := range index.Manifests {
				if _, ok := index.Manifests[i].Annotations[specV1.AnnotationRefName]; ok {
					index.Manifests[i].Annotations[specV1.AnnotationRefName] = bareTag
				}
			}

			updatedIndex, err := json.Marshal(index)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(indexPath, updatedIndex, 0o644))

			artifacts, err := as.List(ctx)
			require.NoError(t, err)
			require.Len(t, artifacts, 1)
			assert.Empty(t, artifacts[0].Name,
				"bare tag %q should not be used as artifact name", bareTag)
		})
	}
}

func TestDetermineBlobMIMEType(t *testing.T) {
	tests := []struct {
		name               string
		setupFunc          func(t *testing.T) libartTypes.ArtifactBlob
		expectedMIMEType   string
		expectNilReader    bool
		expectError        bool
		errorContains      string
		validateReaderFunc func(t *testing.T, reader io.Reader)
	}{
		// TestDetermineBlobMIMEType_FromFile cases
		{
			name: "plain text file",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				tempDir := t.TempDir()
				textFile := filepath.Join(tempDir, "test.txt")
				err := os.WriteFile(textFile, []byte("Hello, World!"), 0o644)
				require.NoError(t, err)

				return libartTypes.ArtifactBlob{
					BlobFilePath: textFile,
					FileName:     "test.txt",
				}
			},
			expectedMIMEType: "text/plain; charset=utf-8",
			expectNilReader:  true,
			expectError:      false,
		},
		{
			name: "JSON file",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				tempDir := t.TempDir()
				jsonFile := filepath.Join(tempDir, "test.json")
				jsonContent := []byte(`{"key": "value", "number": 123}`)
				err := os.WriteFile(jsonFile, jsonContent, 0o644)
				require.NoError(t, err)

				return libartTypes.ArtifactBlob{
					BlobFilePath: jsonFile,
					FileName:     "test.json",
				}
			},
			expectedMIMEType: "text/plain; charset=utf-8",
			expectNilReader:  true,
			expectError:      false,
		},
		{
			name: "JPEG binary file",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				tempDir := t.TempDir()
				binaryFile := filepath.Join(tempDir, "test.bin")
				binaryContent := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}
				err := os.WriteFile(binaryFile, binaryContent, 0o644)
				require.NoError(t, err)

				return libartTypes.ArtifactBlob{
					BlobFilePath: binaryFile,
					FileName:     "test.bin",
				}
			},
			expectedMIMEType: "image/jpeg",
			expectNilReader:  true,
			expectError:      false,
		},
		// TestDetermineBlobMIMEType_SmallFile case
		{
			name: "small file less than 512 bytes",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				tempDir := t.TempDir()
				smallFile := filepath.Join(tempDir, "small.txt")
				smallContent := []byte("Small")
				err := os.WriteFile(smallFile, smallContent, 0o644)
				require.NoError(t, err)

				return libartTypes.ArtifactBlob{
					BlobFilePath: smallFile,
					FileName:     "small.txt",
				}
			},
			expectedMIMEType: "text/plain; charset=utf-8",
			expectNilReader:  true,
			expectError:      false,
		},
		// TestDetermineBlobMIMEType_FromReader cases
		{
			name: "plain text reader",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				textContent := "This is plain text content"
				return libartTypes.ArtifactBlob{
					BlobReader: strings.NewReader(textContent),
					FileName:   "test.txt",
				}
			},
			expectedMIMEType: "text/plain; charset=utf-8",
			expectNilReader:  false,
			expectError:      false,
			validateReaderFunc: func(t *testing.T, reader io.Reader) {
				content, err := io.ReadAll(reader)
				require.NoError(t, err)
				assert.Equal(t, "This is plain text content", string(content))
			},
		},
		{
			name: "HTML content reader",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				htmlContent := "<!DOCTYPE html><html><body>Test</body></html>"
				return libartTypes.ArtifactBlob{
					BlobReader: strings.NewReader(htmlContent),
					FileName:   "test.html",
				}
			},
			expectedMIMEType: "text/html; charset=utf-8",
			expectNilReader:  false,
			expectError:      false,
		},
		{
			name: "PNG binary content reader",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				binaryContent := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
				return libartTypes.ArtifactBlob{
					BlobReader: bytes.NewReader(binaryContent),
					FileName:   "test.png",
				}
			},
			expectedMIMEType: "image/png",
			expectNilReader:  false,
			expectError:      false,
		},
		// TestDetermineBlobMIMEType_Errors cases
		{
			name: "neither file path nor reader",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				return libartTypes.ArtifactBlob{
					FileName: "test.txt",
				}
			},
			expectError:   true,
			errorContains: "Artifact.BlobFile or Artifact.BlobReader must be provided",
		},
		{
			name: "both file path and reader provided",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				return libartTypes.ArtifactBlob{
					BlobFilePath: "/tmp/test.txt",
					BlobReader:   strings.NewReader("content"),
					FileName:     "test.txt",
				}
			},
			expectError:   true,
			errorContains: "Artifact.BlobFile or Artifact.BlobReader must be provided",
		},
		{
			name: "non-existent file",
			setupFunc: func(t *testing.T) libartTypes.ArtifactBlob {
				return libartTypes.ArtifactBlob{
					BlobFilePath: "/nonexistent/file.txt",
					FileName:     "file.txt",
				}
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blob := tt.setupFunc(t)

			reader, mimeType, err := determineBlobMIMEType(blob)

			if tt.expectError {
				require.Error(t, err)
				if len(tt.errorContains) > 0 {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedMIMEType, mimeType)

				if tt.expectNilReader {
					assert.Nil(t, reader)
				} else {
					require.NotNil(t, reader)
					if tt.validateReaderFunc != nil {
						tt.validateReaderFunc(t, reader)
					}
				}
			}
		})
	}
}

func TestArtifactStore_copyArtifact(t *testing.T) {
	// Setup source and destination stores
	asSrc, ctx := setupTestStore(t)
	asDest, _ := setupTestStore(t)

	// Add an artifact to the source store
	fileNames := map[string]int{"file1.txt": 128}
	refName := "quay.io/test/copy-artifact:v1"
	originalDigest, _ := helperAddArtifact(t, asSrc, refName, fileNames, nil)
	require.NotEmpty(t, originalDigest.String())

	// Create references for source and destination
	srcRef, err := layout.NewReference(asSrc.storePath, refName)
	require.NoError(t, err)
	destRef, err := layout.NewReference(asDest.storePath, refName)
	require.NoError(t, err)

	// Copy the artifact
	opts := libimage.CopyOptions{}
	copiedDigest, err := asSrc.copyArtifact(ctx, srcRef, destRef, opts)
	require.NoError(t, err)

	// The manifest digest should be the same
	assert.Equal(t, originalDigest.String(), copiedDigest.String())

	// Verify the artifact exists in the destination store
	artifacts, err := asDest.List(ctx)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)

	copiedArtifact := artifacts[0]
	assert.Equal(t, refName, copiedArtifact.Name)
	assert.Equal(t, originalDigest.String(), copiedArtifact.Digest.String())
	assert.Len(t, copiedArtifact.Manifest.Layers, 1)
	assert.Equal(t, "file1.txt", copiedArtifact.Manifest.Layers[0].Annotations[specV1.AnnotationTitle])
}

func TestArtifactStore_withLockedLayout(t *testing.T) {
	as, _ := setupTestStore(t)
	localName := "quay.io/test/locked-layout:v1"

	t.Run("successful execution", func(t *testing.T) {
		var fnCalled bool
		expectedDigest := digest.FromString("test-digest")
		fn := func(localRef types.ImageReference) (digest.Digest, error) {
			fnCalled = true
			require.NotNil(t, localRef)
			imageName := strings.TrimPrefix(localRef.StringWithinTransport(), as.storePath+":")
			assert.Equal(t, localName, imageName)
			return expectedDigest, nil
		}

		d, err := as.withLockedLayout(localName, fn)
		require.NoError(t, err)
		assert.True(t, fnCalled, "fn should have been called")
		assert.Equal(t, expectedDigest, d)
	})

	t.Run("error from callback", func(t *testing.T) {
		var fnCalled bool
		expectedErr := errors.New("test-error")
		fn := func(localRef types.ImageReference) (digest.Digest, error) {
			fnCalled = true
			require.NotNil(t, localRef)
			imageName := strings.TrimPrefix(localRef.StringWithinTransport(), as.storePath+":")
			assert.Equal(t, localName, imageName)
			return "", expectedErr
		}

		d, err := as.withLockedLayout(localName, fn)
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
		assert.True(t, fnCalled, "fn should have been called")
		assert.Empty(t, d)
	})

	t.Run("error creating reference", func(t *testing.T) {
		invalidName := "invalid'image!value@"
		fn := func(localRef types.ImageReference) (digest.Digest, error) {
			t.Fatal("callback should not be called on reference creation error")
			return "", nil
		}

		d, err := as.withLockedLayout(invalidName, fn)
		require.Error(t, err)
		assert.Empty(t, d)
	})
}

func TestArtifactStore_EventChannel(t *testing.T) {
	t.Run("Add", func(t *testing.T) {
		as, _ := setupTestStore(t)
		ch := as.EventChannel()
		require.NotNil(t, ch)

		refName := "quay.io/test/event-add:v1"
		fileNames := map[string]int{"add.txt": 64}

		digest, _ := helperAddArtifact(t, as, refName, fileNames, nil)

		event := <-ch
		require.NotNil(t, event)
		assert.Equal(t, EventTypeArtifactAdd, event.Type)
		assert.Equal(t, refName, event.Name)
		assert.Equal(t, digest.String(), event.ID)
		assert.NoError(t, event.Error)
		as.CloseEventChannel()
	})

	t.Run("Remove", func(t *testing.T) {
		as, _ := setupTestStore(t)
		ch := as.EventChannel()
		require.NotNil(t, ch)

		refName := "quay.io/test/event-remove:v1"
		fileNames := map[string]int{"remove.txt": 32}
		digest, _ := helperAddArtifact(t, as, refName, fileNames, nil)
		<-ch // consume AddEvent

		asr, err := NewArtifactStorageReference(refName)
		require.NoError(t, err)

		removedDigest, err := as.Remove(context.TODO(), asr)
		assert.Equal(t, digest, removedDigest)
		require.NoError(t, err)

		event := <-ch
		require.NotNil(t, event)
		assert.Equal(t, EventTypeArtifactRemove, event.Type)
		assert.Equal(t, refName, event.Name)
		assert.Equal(t, digest.String(), event.ID)
		assert.NoError(t, event.Error)
		as.CloseEventChannel()
	})

	t.Run("Close the event channel", func(t *testing.T) {
		as, _ := setupTestStore(t)
		ch := as.EventChannel()
		require.NotNil(t, ch)

		refName := "quay.io/test/event-add:v1"
		fileNames := map[string]int{"add.txt": 64}

		helperAddArtifact(t, as, refName, fileNames, nil)

		event := <-ch
		require.NotNil(t, event)
		assert.NoError(t, event.Error)

		as.CloseEventChannel()
		assert.Nil(t, as.eventChannel)

		event, ok := <-ch
		assert.False(t, ok)
		assert.Nil(t, event)
	})
}
