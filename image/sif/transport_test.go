package sif

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/private"
	_ "go.podman.io/image/v5/internal/testing/explicitfilepath-tmpdir"
	"go.podman.io/image/v5/types"
)

var _ private.ImageReference = (*sifReference)(nil)

func TestTransportName(t *testing.T) {
	assert.Equal(t, "sif", Transport.Name())
}

func TestTransportParseReference(t *testing.T) {
	testNewReference(t, Transport.ParseReference)
}

func TestTransportValidatePolicyConfigurationScope(t *testing.T) {
	for _, scope := range []string{
		"/etc/passwd",
		"/this/does/not/exist",
	} {
		err := Transport.ValidatePolicyConfigurationScope(scope)
		assert.NoError(t, err, scope)
	}

	for _, scope := range []string{
		"relative/path",
		"/double//slashes",
		"/has/./dot",
		"/has/dot/../dot",
		"/trailing/slash/",
		"/",
	} {
		err := Transport.ValidatePolicyConfigurationScope(scope)
		assert.Error(t, err, scope)
	}
}

func TestNewReference(t *testing.T) {
	testNewReference(t, NewReference)
}

// testNewReference is a test shared for Transport.ParseReference and NewReference.
func testNewReference(t *testing.T, fn func(string) (types.ImageReference, error)) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "image.sif")
	err := os.WriteFile(tmpFile, nil, 0o600)
	require.NoError(t, err)

	for _, file := range []string{
		"/dev/null",
		tmpFile,
		"relativepath",
		tmpDir + "/thisdoesnotexist",
	} {
		ref, err := fn(file)
		require.NoError(t, err, file)
		sifRef, ok := ref.(sifReference)
		require.True(t, ok)
		assert.Equal(t, file, sifRef.file, file)
	}

	_, err = fn(tmpDir + "/thisparentdoesnotexist/something")
	assert.Error(t, err)
}

// refToTempFile creates a temporary file and returns a reference to it.
func refToTempFile(t *testing.T) (types.ImageReference, string) {
	tmpFile := filepath.Join(t.TempDir(), "sif-transport-test")
	err := os.WriteFile(tmpFile, []byte{}, 0o600)
	require.NoError(t, err)
	ref, err := NewReference(tmpFile)
	require.NoError(t, err)
	return ref, tmpFile
}

func TestReferenceTransport(t *testing.T) {
	ref, _ := refToTempFile(t)
	assert.Equal(t, Transport, ref.Transport())
}

func TestReferenceStringWithinTransport(t *testing.T) {
	ref, tmpFile := refToTempFile(t)
	assert.Equal(t, tmpFile, ref.StringWithinTransport())
}

func TestReferenceDockerReference(t *testing.T) {
	ref, _ := refToTempFile(t)
	assert.Nil(t, ref.DockerReference())
}

func TestReferencePolicyConfigurationIdentity(t *testing.T) {
	ref, tmpFile := refToTempFile(t)

	assert.Equal(t, tmpFile, ref.PolicyConfigurationIdentity())
	// A non-canonical path.  Test just one, the various other cases are
	// tested in explicitfilepath.ResolvePathToFullyExplicit.
	ref, err := NewReference("/./" + tmpFile)
	require.NoError(t, err)
	assert.Equal(t, tmpFile, ref.PolicyConfigurationIdentity())
}

func TestReferencePolicyConfigurationNamespaces(t *testing.T) {
	ref, tmpFile := refToTempFile(t)
	// We don't really know enough to make a full equality test here.
	ns := ref.PolicyConfigurationNamespaces()
	require.NotNil(t, ns)
	assert.NotEmpty(t, ns)
	assert.Equal(t, filepath.Dir(tmpFile), ns[0])

	// Test with a known path where the directory should exist. Test just one non-canonical
	// path, the various other cases are tested in explicitfilepath.ResolvePathToFullyExplicit.
	for _, path := range []string{"/usr/share/probablydoesnotexist.sif", "/usr/share/././probablydoesnoexist.sif"} {
		_, err := os.Lstat(filepath.Dir(path))
		require.NoError(t, err)
		ref, err := NewReference(path)
		require.NoError(t, err)
		ns := ref.PolicyConfigurationNamespaces()
		require.NotNil(t, ns)
		assert.Equal(t, []string{"/usr/share", "/usr"}, ns)
	}

	// "/" as a corner case.
	ref, err := NewReference("/")
	require.NoError(t, err)
	assert.Equal(t, []string{}, ref.PolicyConfigurationNamespaces())
}

func TestReferenceNewImage(t *testing.T) {
	ref, _ := refToTempFile(t)
	// A pretty pointless smoke test for now;
	// we don't want to require every developer of c/image to have fakeroot etc. around.
	_, err := ref.NewImage(context.Background(), nil)
	assert.Error(t, err) // Empty file is not valid
}

func TestReferenceNewImageSource(t *testing.T) {
	ref, _ := refToTempFile(t)
	// A pretty pointless smoke test for now;
	// we don't want to require every developer of c/image to have fakeroot etc. around.
	_, err := ref.NewImageSource(context.Background(), nil)
	assert.Error(t, err) // Empty file is not valid
}

func TestReferenceNewImageDestination(t *testing.T) {
	ref, _ := refToTempFile(t)
	_, err := ref.NewImageDestination(context.Background(), nil)
	assert.Error(t, err)
}

func TestReferenceDeleteImage(t *testing.T) {
	ref, _ := refToTempFile(t)
	err := ref.DeleteImage(context.Background(), nil)
	assert.Error(t, err)
}
