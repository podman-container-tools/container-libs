package splitfdstreamserver

import (
	"io"

	graphdriver "go.podman.io/storage/drivers"
)

// StoreBackend serves the layers of one store.
type StoreBackend interface {
	// LayerIDByDiffID returns the ID of the layer holding the content with
	// the given OCI diff ID.
	LayerIDByDiffID(diffID string) (string, error)

	// LayerDiff returns the uncompressed diff of layerID against its
	// parent.  It must reproduce the blob the layer was created from, so
	// that what the consumer reassembles hashes to the expected diff ID.
	LayerDiff(layerID string) (io.ReadCloser, error)

	// LayerFiles opens access to the files of a layer.  The caller is
	// responsible for closing the result.
	LayerFiles(layerID string) (graphdriver.LayerFiles, error)
}
