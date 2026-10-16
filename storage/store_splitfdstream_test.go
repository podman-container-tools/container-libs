package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSplitFDStreamImageLayers checks that the layers of an image are
// addressed by their position in it, base layer first, as recorded by the
// store.
func TestSplitFDStreamImageLayers(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	t.Cleanup(func() { _, _ = store.Shutdown(true) })

	base, err := store.CreateLayer("", "", nil, "", false, nil)
	require.NoError(t, err)
	top, err := store.CreateLayer("", base.ID, nil, "", false, nil)
	require.NoError(t, err)
	// A layer that is not part of the image.
	_, err = store.CreateLayer("", base.ID, nil, "", false, nil)
	require.NoError(t, err)
	img, err := store.CreateImage("", nil, top.ID, "", nil)
	require.NoError(t, err)

	sfds, err := SplitFDStreamStoreFor(store)
	require.NoError(t, err)
	layerIDs, err := sfds.imageLayerIDs(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{base.ID, top.ID}, layerIDs)

	backend := &splitFDStreamBackend{store: sfds.store, layerIDs: layerIDs}
	id, err := backend.LayerIDByIndex(1)
	require.NoError(t, err)
	assert.Equal(t, top.ID, id)
	_, err = backend.LayerIDByIndex(2)
	assert.ErrorIs(t, err, ErrLayerUnknown)
	_, err = backend.LayerIDByIndex(-1)
	assert.ErrorIs(t, err, ErrLayerUnknown)

	_, err = sfds.imageLayerIDs("missing")
	assert.ErrorIs(t, err, ErrImageUnknown)
}
