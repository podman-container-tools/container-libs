package archive

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "go.podman.io/image/v5/internal/testing/explicitfilepath-tmpdir"
	"go.podman.io/image/v5/types"
)

func TestTransportName(t *testing.T) {
	assert.Equal(t, "oci-archive", Transport.Name())
}

func TestTransportParseReference(t *testing.T) {
	testParseReference(t, Transport.ParseReference)
}

func TestTransportValidatePolicyConfigurationScope(t *testing.T) {
	for _, scope := range []string{
		"/etc",
		"/this/does/not/exist",
	} {
		err := Transport.ValidatePolicyConfigurationScope(scope)
		assert.NoError(t, err, scope)
	}

	for _, scope := range []string{
		"relative/path",
		"/",
		"/double//slashes",
		"/has/./dot",
		"/has/dot/../dot",
		"/trailing/slash/",
	} {
		err := Transport.ValidatePolicyConfigurationScope(scope)
		assert.Error(t, err, scope)
	}
}

func TestParseReference(t *testing.T) {
	testParseReference(t, ParseReference)
}

// testParseReference is a test shared for Transport.ParseReference and ParseReference.
func testParseReference(t *testing.T, fn func(string) (types.ImageReference, error)) {
	tmpDir := t.TempDir()

	for _, path := range []string{
		"/",
		"/etc",
		tmpDir,
		"relativepath",
		tmpDir + "/thisdoesnotexist",
	} {
		for _, image := range []struct {
			suffix string
			image  string
			index  int
		}{
			{":notlatest:image", "notlatest:image", -1},
			{":latestimage", "latestimage", -1},
			{":", "", -1},
			{"", "", -1},
			{":@0", "", 0},
			{":@5", "", 5},
		} {
			input := path + image.suffix
			ref, err := fn(input)
			require.NoError(t, err, input)
			ociArchRef, ok := ref.(ociArchiveReference)
			require.True(t, ok)
			assert.Equal(t, path, ociArchRef.file, input)
			assert.Equal(t, image.image, ociArchRef.image, input)
			assert.Equal(t, image.index, ociArchRef.sourceIndex, input)
		}
	}

	_, err := fn(tmpDir + ":invalid'image!value@")
	assert.Error(t, err)
}

func TestNewReference(t *testing.T) {
	const (
		imageValue   = "imageValue"
		noImageValue = ""
	)

	tmpDir := t.TempDir()

	ref, err := NewReference(tmpDir, imageValue)
	require.NoError(t, err)
	ociArchRef, ok := ref.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, tmpDir, ociArchRef.file)
	assert.Equal(t, imageValue, ociArchRef.image)

	ref, err = NewReference(tmpDir, noImageValue)
	require.NoError(t, err)
	ociArchRef, ok = ref.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, tmpDir, ociArchRef.file)
	assert.Equal(t, noImageValue, ociArchRef.image)

	_, err = NewReference(tmpDir+"/thisparentdoesnotexist/something", imageValue)
	assert.Error(t, err)

	_, err = NewReference(tmpDir, "invalid'image!value@")
	assert.Error(t, err)

	_, err = NewReference(tmpDir+"/has:colon", imageValue)
	assert.Error(t, err)
}

// refToTempOCI creates a temporary directory and returns an reference to it.
func refToTempOCI(t *testing.T) (types.ImageReference, string) {
	tmpDir := t.TempDir()
	m := `{
		"schemaVersion": 2,
		"manifests": [
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f",
			"platform": {
				"architecture": "ppc64le",
				"os": "linux"
			},
			"annotations": {
				"org.opencontainers.image.ref.name": "imageValue"
			}
		}
		]
	}
`
	err := os.WriteFile(filepath.Join(tmpDir, "index.json"), []byte(m), 0o644)
	require.NoError(t, err)
	ref, err := NewReference(tmpDir, "imageValue")
	require.NoError(t, err)
	return ref, tmpDir
}

// refToTempOCIArchive creates a temporary directory, copies the contents of that directory
// to a temporary tar file and returns a reference to the temporary tar file
func refToTempOCIArchive(t *testing.T, tarEntryTimestamp *time.Time) (ref types.ImageReference, tmpTarFile string) {
	tmpDir := t.TempDir()
	m := `{
		"schemaVersion": 2,
		"manifests": [
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f",
			"platform": {
				"architecture": "ppc64le",
				"os": "linux"
			},
			"annotations": {
				"org.opencontainers.image.ref.name": "imageValue"
			}
		}
		]
	}
`
	err := os.WriteFile(filepath.Join(tmpDir, "index.json"), []byte(m), 0o644)
	require.NoError(t, err)
	tarFile := filepath.Join(t.TempDir(), "oci-transport-test.tar")
	err = tarDirectory(tmpDir, tarFile, tarEntryTimestamp)
	require.NoError(t, err)
	ref, err = NewReference(tarFile, "")
	require.NoError(t, err)
	return ref, tarFile
}

func TestReferenceTransport(t *testing.T) {
	ref, _ := refToTempOCI(t)
	assert.Equal(t, Transport, ref.Transport())
}

func TestReferenceStringWithinTransport(t *testing.T) {
	tmpDir := t.TempDir()

	for _, c := range []struct{ input, result string }{
		{"/dir1:notlatest:notlatest", "/dir1:notlatest:notlatest"}, // Explicit image
		{"/dir3:", "/dir3:"},     // No image
		{"/dir4:@0", "/dir4:@0"}, // Index reference
		{"/dir5:@3", "/dir5:@3"}, // Index reference
	} {
		ref, err := ParseReference(tmpDir + c.input)
		require.NoError(t, err, c.input)
		stringRef := ref.StringWithinTransport()
		assert.Equal(t, tmpDir+c.result, stringRef, c.input)
		// Do one more round to verify that the output can be parsed, to an equal value.
		ref2, err := Transport.ParseReference(stringRef)
		require.NoError(t, err, c.input)
		stringRef2 := ref2.StringWithinTransport()
		assert.Equal(t, stringRef, stringRef2, c.input)
	}
}

func TestReferenceDockerReference(t *testing.T) {
	ref, _ := refToTempOCI(t)
	assert.Nil(t, ref.DockerReference())
}

func TestReferencePolicyConfigurationIdentity(t *testing.T) {
	ref, tmpDir := refToTempOCI(t)

	assert.Equal(t, tmpDir, ref.PolicyConfigurationIdentity())
	// A non-canonical path.  Test just one, the various other cases are
	// tested in explicitfilepath.ResolvePathToFullyExplicit.
	ref, err := NewReference(tmpDir+"/.", "image2")
	require.NoError(t, err)
	assert.Equal(t, tmpDir, ref.PolicyConfigurationIdentity())

	// "/" as a corner case.
	ref, err = NewReference("/", "image3")
	require.NoError(t, err)
	assert.Equal(t, "/", ref.PolicyConfigurationIdentity())
}

func TestReferencePolicyConfigurationNamespaces(t *testing.T) {
	ref, tmpDir := refToTempOCI(t)
	// We don't really know enough to make a full equality test here.
	ns := ref.PolicyConfigurationNamespaces()
	require.NotNil(t, ns)
	assert.True(t, len(ns) >= 2)
	assert.Equal(t, tmpDir, ns[0])
	assert.Equal(t, filepath.Dir(tmpDir), ns[1])

	// Test with a known path which should exist. Test just one non-canonical
	// path, the various other cases are tested in explicitfilepath.ResolvePathToFullyExplicit.
	//
	// It would be nice to test a deeper hierarchy, but it is not obvious what
	// deeper path is always available in the various distros, AND is not likely
	// to contains a symbolic link.
	for _, path := range []string{"/usr/share", "/usr/share/./."} {
		_, err := os.Lstat(path)
		require.NoError(t, err)
		ref, err := NewReference(path, "someimage")
		require.NoError(t, err)
		ns := ref.PolicyConfigurationNamespaces()
		require.NotNil(t, ns)
		assert.Equal(t, []string{"/usr/share", "/usr"}, ns)
	}

	// "/" as a corner case.
	ref, err := NewReference("/", "image3")
	require.NoError(t, err)
	assert.Equal(t, []string{}, ref.PolicyConfigurationNamespaces())
}

func TestReferenceNewImage(t *testing.T) {
	ref, _ := refToTempOCI(t)
	_, err := ref.NewImage(context.Background(), nil)
	assert.Error(t, err)
}

func TestReferenceNewImageSource(t *testing.T) {
	ref, _ := refToTempOCIArchive(t, nil)
	src, err := ref.NewImageSource(context.Background(), nil)
	assert.NoError(t, err)
	defer src.Close()
}

func TestTimestampEntriesPassedThrough(t *testing.T) {
	// set target time to a bit in the future, but rounded
	targetTime := time.Now().Add(time.Hour).Truncate(time.Second)

	_, tmpTarFile := refToTempOCIArchive(t, &targetTime)

	f, err := os.Open(tmpTarFile)
	assert.NoError(t, err)
	defer f.Close()

	numEntries := 0
	tr := tar.NewReader(f)
	for {
		th, err := tr.Next()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		assert.Equal(t, targetTime, th.ModTime) // access time and change time are ignored by Go's tar.Writer unless the creator explicitly sets a non-default header format, so just check mod time
		numEntries++
	}
	assert.NotEqual(t, 0, numEntries)
}

func TestReferenceNewImageDestination(t *testing.T) {
	ref, _ := refToTempOCI(t)
	dest, err := ref.NewImageDestination(context.Background(), nil)
	assert.NoError(t, err)
	defer dest.Close()
}

func TestReferenceDeleteImage(t *testing.T) {
	ref, _ := refToTempOCI(t)
	err := ref.DeleteImage(context.Background(), nil)
	assert.Error(t, err)
}

func TestNewIndexReference(t *testing.T) {
	tmpDir := t.TempDir()

	ref, err := NewIndexReference(tmpDir, 0)
	require.NoError(t, err)
	ociArchRef, ok := ref.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, tmpDir, ociArchRef.file)
	assert.Equal(t, "", ociArchRef.image)
	assert.Equal(t, 0, ociArchRef.sourceIndex)

	ref, err = NewIndexReference(tmpDir, 5)
	require.NoError(t, err)
	ociArchRef, ok = ref.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, 5, ociArchRef.sourceIndex)

	// Negative index should fail
	_, err = NewIndexReference(tmpDir, -2)
	assert.Error(t, err)

	// Cannot set both image and index
	_, err = newReference(tmpDir, "someimage", 3, nil, nil)
	assert.Error(t, err)
}

func TestReaderAndWriter(t *testing.T) {
	// Create a minimal OCI layout directory with two manifests
	srcDir := t.TempDir()

	m := `{
		"schemaVersion": 2,
		"manifests": [
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f",
			"annotations": {
				"org.opencontainers.image.ref.name": "image1:latest"
			}
		},
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:aaaaaae4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f",
			"annotations": {
				"org.opencontainers.image.ref.name": "image2:latest"
			}
		}
		]
	}`
	err := os.WriteFile(filepath.Join(srcDir, "index.json"), []byte(m), 0o644)
	require.NoError(t, err)

	// Tar it into an archive
	tarFile := filepath.Join(t.TempDir(), "multi.tar")
	err = tarDirectory(srcDir, tarFile, nil)
	require.NoError(t, err)

	// Test the Reader
	reader, err := NewReader(context.Background(), nil, tarFile)
	require.NoError(t, err)
	defer reader.Close()

	entries, err := reader.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Verify the first entry has the correct image name and no index
	ref0, ok := entries[0].ImageRef.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, "image1:latest", ref0.image)
	assert.Equal(t, -1, ref0.sourceIndex)

	// Verify the second entry has the correct image name and no index
	ref1, ok := entries[1].ImageRef.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, "image2:latest", ref1.image)
	assert.Equal(t, -1, ref1.sourceIndex)

	// Test the Writer
	writerPath := filepath.Join(t.TempDir(), "writer-output.tar")
	writer, err := NewWriter(nil, writerPath)
	require.NoError(t, err)

	ref, err := writer.NewReference("test:latest")
	require.NoError(t, err)
	writerRef, ok := ref.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, "test:latest", writerRef.image)
	assert.Equal(t, -1, writerRef.sourceIndex)

	err = writer.Close()
	require.NoError(t, err)

	// Verify the output tar file was created
	_, err = os.Stat(writerPath)
	require.NoError(t, err)
}

func TestReaderListUnnamedImages(t *testing.T) {
	// Test that unnamed images get an index instead of a name
	srcDir := t.TempDir()

	m := `{
		"schemaVersion": 2,
		"manifests": [
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f"
		},
		{
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"size": 7143,
			"digest": "sha256:aaaaaae4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f"
		}
		]
	}`
	err := os.WriteFile(filepath.Join(srcDir, "index.json"), []byte(m), 0o644)
	require.NoError(t, err)

	tarFile := filepath.Join(t.TempDir(), "unnamed.tar")
	err = tarDirectory(srcDir, tarFile, nil)
	require.NoError(t, err)

	reader, err := NewReader(context.Background(), nil, tarFile)
	require.NoError(t, err)
	defer reader.Close()

	entries, err := reader.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Unnamed images should get sourceIndex set to their position
	ref0, ok := entries[0].ImageRef.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, "", ref0.image)
	assert.Equal(t, 0, ref0.sourceIndex)

	ref1, ok := entries[1].ImageRef.(ociArchiveReference)
	require.True(t, ok)
	assert.Equal(t, "", ref1.image)
	assert.Equal(t, 1, ref1.sourceIndex)
}
