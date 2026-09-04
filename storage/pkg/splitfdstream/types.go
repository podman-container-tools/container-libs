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
	manifestBigDataKey = "manifest"

	// ChunkMetadata identifies a metadata chunk (tar header/padding bytes).
	ChunkMetadata byte = 0x00
	// ChunkInlineData identifies an inline data chunk (file content).
	ChunkInlineData byte = 0x01
	// ChunkFileBackedData identifies a file-backed data chunk.
	// The consumer opens the referenced file via openat2(RESOLVE_BENEATH)
	// from the directory FD at the given index.
	ChunkFileBackedData byte = 0x02

	// MaxInlineChunkSize is the maximum size of a single inline or metadata chunk.
	MaxInlineChunkSize = 256 << 20
	// MaxFilenameLen is the maximum filename length in a FileBackedData chunk.
	MaxFilenameLen = 4096
)

// Store represents the minimal interface needed for image metadata access.
type Store interface {
	ImageBigData(id, key string) ([]byte, error)
	ResolveImageID(id string) (actualID string, topLayerID string, err error)
	LayerParent(id string) (parentID string, err error)
}

// SplitDirFDStreamDriver defines the interface that storage drivers must
// implement to support splitdirfdstream operations.
type SplitDirFDStreamDriver interface {
	// GetSplitDirFDStream generates a splitdirfdstream for a layer.
	// It returns a pipe carrying the stream bytes and a set of directory
	// file descriptors that FileBackedData chunks reference.
	GetSplitDirFDStream(id, parent string, options *GetSplitFDStreamOpts) (io.ReadCloser, []*os.File, error)
}

// DriverFunc acquires a SplitDirFDStreamDriver (and any associated lock)
// and returns a release function that the caller must invoke when done.
type DriverFunc func() (SplitDirFDStreamDriver, func(), error)

// ImageMetadata holds manifest and config data for an OCI image.
type ImageMetadata struct {
	ManifestJSON []byte   `json:"manifest"`
	ConfigJSON   []byte   `json:"config"`
	LayerDigests []string `json:"layerDigests"`
}

func findManifest(store Store, imageID string) ([]byte, error) {
	data, err := store.ImageBigData(imageID, manifestBigDataKey)
	if err != nil {
		return nil, fmt.Errorf("no manifest found for image %s: %w", imageID, err)
	}
	return data, nil
}

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

// GetSplitFDStreamOpts provides options for GetSplitDirFDStream operations.
type GetSplitFDStreamOpts struct {
	MountLabel string
	IDMappings *idtools.IDMappings
}

// SplitDirFDStreamWriter writes data in the composefs-rs splitdirfdstream format.
//
// The format is a sequence of chunks, each prefixed by a type byte:
//   - 0x00 Metadata:       type(1) + length(u32 LE) + data
//   - 0x01 InlineData:     type(1) + length(u32 LE) + data
//   - 0x02 FileBackedData: type(1) + content_length(u64 LE) + dirfd_index(u32 LE) + name_len(u32 LE) + filename
type SplitDirFDStreamWriter struct {
	writer io.Writer
}

// NewWriter creates a new SplitDirFDStreamWriter.
func NewWriter(w io.Writer) *SplitDirFDStreamWriter {
	return &SplitDirFDStreamWriter{writer: w}
}

// WriteMetadata writes a metadata chunk (tar header/padding bytes).
func (w *SplitDirFDStreamWriter) WriteMetadata(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > MaxInlineChunkSize {
		return fmt.Errorf("metadata chunk too large: %d > %d", len(data), MaxInlineChunkSize)
	}
	if err := w.writeChunkHeader(ChunkMetadata, uint32(len(data))); err != nil {
		return err
	}
	_, err := w.writer.Write(data)
	return err
}

// WriteInlineData writes an inline data chunk (file content).
func (w *SplitDirFDStreamWriter) WriteInlineData(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > MaxInlineChunkSize {
		return fmt.Errorf("inline data chunk too large: %d > %d", len(data), MaxInlineChunkSize)
	}
	if err := w.writeChunkHeader(ChunkInlineData, uint32(len(data))); err != nil {
		return err
	}
	_, err := w.writer.Write(data)
	return err
}

// InlineDataWriter writes an InlineData chunk header for size bytes and
// returns an io.Writer that passes data straight through to the
// underlying stream.
func (w *SplitDirFDStreamWriter) InlineDataWriter(size int64) (io.Writer, error) {
	if size <= 0 {
		return io.Discard, nil
	}
	if size > MaxInlineChunkSize {
		return nil, fmt.Errorf("inline data chunk too large: %d > %d", size, MaxInlineChunkSize)
	}
	if err := w.writeChunkHeader(ChunkInlineData, uint32(size)); err != nil {
		return nil, err
	}
	return w.writer, nil
}

// WriteFileBackedData writes a file-backed data chunk referencing a file
// in one of the directory FDs passed alongside the stream.
func (w *SplitDirFDStreamWriter) WriteFileBackedData(contentLength int64, dirfdIndex int, filename string) error {
	if len(filename) > MaxFilenameLen {
		return fmt.Errorf("filename too long: %d > %d", len(filename), MaxFilenameLen)
	}
	if _, err := w.writer.Write([]byte{ChunkFileBackedData}); err != nil {
		return err
	}
	if err := binary.Write(w.writer, binary.LittleEndian, uint64(contentLength)); err != nil {
		return err
	}
	if err := binary.Write(w.writer, binary.LittleEndian, uint32(dirfdIndex)); err != nil {
		return err
	}
	if err := binary.Write(w.writer, binary.LittleEndian, uint32(len(filename))); err != nil {
		return err
	}
	_, err := w.writer.Write([]byte(filename))
	return err
}

func (w *SplitDirFDStreamWriter) writeChunkHeader(chunkType byte, length uint32) error {
	if _, err := w.writer.Write([]byte{chunkType}); err != nil {
		return err
	}
	return binary.Write(w.writer, binary.LittleEndian, length)
}
