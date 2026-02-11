package splitfdstream

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/storage/pkg/idtools"
)

const (
	// manifestBigDataKey is the BigData key under which the image manifest
	// is stored.  Matches storage.ImageDigestBigDataKey.
	manifestBigDataKey = "manifest"
)

// Store represents the minimal interface needed for image metadata access.
type Store interface {
	ImageBigData(id, key string) ([]byte, error)
	ResolveImageID(id string) (actualID string, topLayerID string, err error)
	LayerParent(id string) (parentID string, err error)
}

// SplitFDStreamDriver defines the interface that storage drivers must implement
// to support splitfdstream operations.
type SplitFDStreamDriver interface {
	// GetSplitFDStream generates a splitfdstream for a layer.
	GetSplitFDStream(id, parent string, options *GetSplitFDStreamOpts) (io.ReadCloser, []*os.File, error)
}

// DriverFunc acquires a SplitFDStreamDriver (and any associated lock)
// and returns a release function that the caller must invoke when done.
// This allows per-request locking so that the graph driver is held only
// for the duration of each operation.
type DriverFunc func() (SplitFDStreamDriver, func(), error)

// ImageMetadata holds manifest and config data for an OCI image.
type ImageMetadata struct {
	ManifestJSON []byte   `json:"manifest"`
	ConfigJSON   []byte   `json:"config"`
	LayerDigests []string `json:"layerDigests"`
}

// findManifest retrieves the image manifest via store.ImageBigData
// using the well-known "manifest" key.  We use this thin wrapper
// (rather than importing containers/image) to avoid pulling a heavy
// dependency into the storage layer.  The image in the store is
// already platform-resolved, so no key scanning is needed.
func findManifest(store Store, imageID string) ([]byte, error) {
	data, err := store.ImageBigData(imageID, manifestBigDataKey)
	if err != nil {
		return nil, fmt.Errorf("no manifest found for image %s: %w", imageID, err)
	}
	return data, nil
}

// findConfig retrieves the image config via store.ImageBigData using
// the config digest from the manifest (e.g. "sha256:abc...").
func findConfig(store Store, imageID string, manifest *v1.Manifest) ([]byte, error) {
	configDigest := manifest.Config.Digest.String()
	data, err := store.ImageBigData(imageID, configDigest)
	if err != nil {
		return nil, fmt.Errorf("config %s not found for image %s: %w", configDigest, imageID, err)
	}
	return data, nil
}

// GetImageMetadata retrieves manifest, config, and layer information for an image.
func GetImageMetadata(store Store, imageID string) (*ImageMetadata, error) {
	actualID, topLayerID, err := store.ResolveImageID(imageID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image %s: %w", imageID, err)
	}

	manifestJSON, err := findManifest(store, actualID)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for %s (resolved to %s): %w", imageID, actualID, err)
	}

	var manifest v1.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	configJSON, err := findConfig(store, actualID, &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to get config for %s (resolved to %s): %w", imageID, actualID, err)
	}

	// Walk the layer chain using store.LayerParent.
	// Cap at a generous limit to prevent infinite loops from corrupted storage.
	const maxLayerDepth = 4096
	var layerIDs []string
	layerID := topLayerID
	for layerID != "" {
		if len(layerIDs) >= maxLayerDepth {
			return nil, fmt.Errorf("layer chain exceeds maximum depth of %d", maxLayerDepth)
		}
		layerIDs = append(layerIDs, layerID)
		parentID, err := store.LayerParent(layerID)
		if err != nil {
			return nil, fmt.Errorf("failed to get parent of layer %s: %w", layerID, err)
		}
		layerID = parentID
	}

	// Fall back to manifest layer digests if layer chain traversal failed
	if len(layerIDs) == 0 {
		layerIDs = make([]string, len(manifest.Layers))
		for i, layer := range manifest.Layers {
			layerIDs[i] = layer.Digest.String()
		}
	}

	return &ImageMetadata{
		ManifestJSON: manifestJSON,
		ConfigJSON:   configJSON,
		LayerDigests: layerIDs,
	}, nil
}

// GetSplitFDStreamOpts provides options for GetSplitFDStream operations.
type GetSplitFDStreamOpts struct {
	MountLabel string
	IDMappings *idtools.IDMappings
}

// SplitFDStreamWriter writes data in the composefs-rs splitfdstream format.
// The format uses signed 64-bit little-endian prefixes:
// - Negative prefix: abs(prefix) bytes of inline data follow
// - Non-negative prefix: reference to external file descriptor at index prefix
type SplitFDStreamWriter struct {
	writer io.Writer
}

// NewWriter creates a new SplitFDStreamWriter.
func NewWriter(w io.Writer) *SplitFDStreamWriter {
	return &SplitFDStreamWriter{writer: w}
}

// WriteInline writes inline data with a negative prefix indicating the data length.
func (w *SplitFDStreamWriter) WriteInline(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	prefix := int64(-len(data))
	if err := binary.Write(w.writer, binary.LittleEndian, prefix); err != nil {
		return fmt.Errorf("failed to write inline prefix: %w", err)
	}
	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write inline data: %w", err)
	}
	return nil
}

// InlineWriter writes a negative prefix for size bytes and returns an
// io.Writer that passes data straight through to the underlying stream.
// This lets callers use io.Copy instead of manual chunked loops.
func (w *SplitFDStreamWriter) InlineWriter(size int64) (io.Writer, error) {
	if size <= 0 {
		return io.Discard, nil
	}
	prefix := -size
	if err := binary.Write(w.writer, binary.LittleEndian, prefix); err != nil {
		return nil, fmt.Errorf("failed to write inline prefix: %w", err)
	}
	return w.writer, nil
}

// WriteExternal writes a reference to an external file descriptor.
func (w *SplitFDStreamWriter) WriteExternal(fdIndex int) error {
	prefix := int64(fdIndex)
	if err := binary.Write(w.writer, binary.LittleEndian, prefix); err != nil {
		return fmt.Errorf("failed to write external fd reference: %w", err)
	}
	return nil
}
