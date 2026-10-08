//go:build !containers_image_storage_stub

package storage

import (
	"fmt"
	"io"
	"os"

	digest "github.com/opencontainers/go-digest"
	"go.podman.io/storage"
	"go.podman.io/storage/pkg/archive"
	"go.podman.io/storage/pkg/chunked"
)

// SplitFDStreamSocket returns a socket on which the store named by storeSpec
// serves org.composefs.Oci.  storeSpec uses the
// "[driver@graphroot+runroot:options]" form of a containers-storage
// reference; an empty storeSpec means the default store.  The caller is
// responsible for closing the returned file.
//
// This API is experimental and can be changed without bumping the major version number.
func SplitFDStreamSocket(storeSpec string) (*os.File, error) {
	transport, ok := Transport.(*storageTransport)
	if !ok {
		return nil, fmt.Errorf("unexpected containers-storage transport type %T", Transport)
	}
	store, err := transport.storeForSpec(storeSpec)
	if err != nil {
		return nil, fmt.Errorf("opening store %q: %w", storeSpec, err)
	}
	sfds, err := storage.SplitFDStreamStoreFor(store)
	if err != nil {
		return nil, fmt.Errorf("store %q does not support splitfdstream: %w", storeSpec, err)
	}
	return sfds.Socket(&splitFDStreamBackend{store: store, splitFDStream: sfds})
}

// splitFDStreamBackend implements storage.SplitFDStreamBackend for one store.
type splitFDStreamBackend struct {
	store         storage.Store
	splitFDStream storage.SplitFDStreamStore
}

// LayerDiff implements storage.SplitFDStreamBackend.
func (b *splitFDStreamBackend) LayerDiff(layerID string) (io.ReadCloser, error) {
	// Diffing against the parent ("" as from) lets the layer store
	// reproduce the original blob from its tar-split metadata instead of
	// generating a new archive from the files.
	uncompressed := archive.Uncompressed
	return b.store.Diff("", layerID, &storage.DiffOptions{Compression: &uncompressed})
}

// LayerFiles implements storage.SplitFDStreamBackend.
func (b *splitFDStreamBackend) LayerFiles(layerID string) (storage.SplitFDStreamLayerFiles, error) {
	// Read before the graph driver is acquired: reading layer big data
	// takes the graph lock too, and it is not recursive.
	flatNames, err := chunked.LayerFlatFileNames(b.store, layerID)
	if err != nil {
		return nil, fmt.Errorf("reading the table of contents of layer %q: %w", layerID, err)
	}
	return b.splitFDStream.LayerFiles(layerID, flatNames)
}

// LayerIDByDiffID implements storage.SplitFDStreamBackend.
//
// Most layers are found through the store's index of uncompressed digests.
// A layer pulled partially has no uncompressed digest recorded, only a TOC
// digest, and the diff ID it stands for is noted on the layer itself at pull
// time; there is no index for that, so those are looked for one by one.  That
// only happens for such a layer, or for one that is genuinely absent.
func (b *splitFDStreamBackend) LayerIDByDiffID(diffID string) (string, error) {
	d, err := digest.Parse(diffID)
	if err != nil {
		return "", fmt.Errorf("parsing diff ID %q: %w", diffID, err)
	}

	if layers, err := b.store.LayersByUncompressedDigest(d); err == nil && len(layers) > 0 {
		return layers[0].ID, nil
	}

	layers, err := b.store.Layers()
	if err != nil {
		return "", fmt.Errorf("listing layers: %w", err)
	}
	expected := d.String()
	for _, layer := range layers {
		if layer.UncompressedDigest != "" || layer.TOCDigest == "" {
			continue
		}
		if recorded, ok := layer.Flags[expectedLayerDiffIDFlag].(string); ok && recorded == expected {
			return layer.ID, nil
		}
	}
	return "", fmt.Errorf("no layer with diff ID %q", diffID)
}
