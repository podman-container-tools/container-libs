package directory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
)

func TestGetPutManifest(t *testing.T) {
	ref, _ := refToTempDir(t)

	man := []byte("test-manifest")
	list := []byte("test-manifest-list")
	md, err := manifest.Digest(man)
	require.NoError(t, err)
	dest, err := ref.NewImageDestination(context.Background(), nil)
	require.NoError(t, err)
	defer dest.Close()
	err = dest.PutManifest(context.Background(), man, &md)
	assert.NoError(t, err)
	err = dest.PutManifest(context.Background(), list, nil)
	assert.NoError(t, err)
	err = dest.Commit(context.Background(), nil) // nil unparsedToplevel is invalid, we don’t currently use the value
	assert.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	m, mt, err := src.GetManifest(context.Background(), nil)
	assert.NoError(t, err)
	assert.Equal(t, list, m)
	assert.Equal(t, "", mt)

	m, mt, err = src.GetManifest(context.Background(), &md)
	assert.NoError(t, err)
	assert.Equal(t, man, m)
	assert.Equal(t, "", mt)
}

func TestGetPutSignatures(t *testing.T) {
	ref, _ := refToTempDir(t)

	man := []byte("test-manifest")
	list := []byte("test-manifest-list")
	md, err := manifest.Digest(man)
	require.NoError(t, err)
	// These signatures are completely invalid; start with 0xA3 just to be minimally plausible to signature.FromBlob.
	signatures := [][]byte{
		[]byte("\xA3sig1"),
		[]byte("\xA3sig2"),
	}
	listSignatures := [][]byte{
		[]byte("\xA3sig3"),
		[]byte("\xA3sig4"),
	}

	dest, err := ref.NewImageDestination(context.Background(), nil)
	require.NoError(t, err)
	defer dest.Close()
	err = dest.SupportsSignatures(context.Background())
	assert.NoError(t, err)

	err = dest.PutManifest(context.Background(), man, &md)
	require.NoError(t, err)
	err = dest.PutManifest(context.Background(), list, nil)
	require.NoError(t, err)

	err = dest.PutSignatures(context.Background(), signatures, &md)
	assert.NoError(t, err)
	err = dest.PutSignatures(context.Background(), listSignatures, nil)
	assert.NoError(t, err)
	err = dest.Commit(context.Background(), nil) // nil unparsedToplevel is invalid, we don’t currently use the value
	assert.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	sigs, err := src.GetSignatures(context.Background(), nil)
	assert.NoError(t, err)
	assert.Equal(t, listSignatures, sigs)

	sigs, err = src.GetSignatures(context.Background(), &md)
	assert.NoError(t, err)
	assert.Equal(t, signatures, sigs)
}
