package splitfdstream

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestWriterMetadata(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	data := []byte("hello world")
	if err := w.WriteMetadata(data); err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}

	// Expect: type(1) + length(4) + data
	b := buf.Bytes()
	if len(b) != 1+4+len(data) {
		t.Fatalf("expected %d bytes, got %d", 1+4+len(data), len(b))
	}

	if b[0] != ChunkMetadata {
		t.Fatalf("expected chunk type 0x%02x, got 0x%02x", ChunkMetadata, b[0])
	}
	length := binary.LittleEndian.Uint32(b[1:5])
	if length != uint32(len(data)) {
		t.Fatalf("expected length %d, got %d", len(data), length)
	}
	if !bytes.Equal(b[5:], data) {
		t.Fatalf("expected data %q, got %q", data, b[5:])
	}
}

func TestWriterMetadataEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteMetadata(nil); err != nil {
		t.Fatalf("WriteMetadata(nil): %v", err)
	}
	if err := w.WriteMetadata([]byte{}); err != nil {
		t.Fatalf("WriteMetadata(empty): %v", err)
	}

	if buf.Len() != 0 {
		t.Fatalf("expected no output for empty metadata, got %d bytes", buf.Len())
	}
}

func TestWriterInlineData(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	data := []byte("file content")
	if err := w.WriteInlineData(data); err != nil {
		t.Fatalf("WriteInlineData: %v", err)
	}

	b := buf.Bytes()
	if len(b) != 1+4+len(data) {
		t.Fatalf("expected %d bytes, got %d", 1+4+len(data), len(b))
	}

	if b[0] != ChunkInlineData {
		t.Fatalf("expected chunk type 0x%02x, got 0x%02x", ChunkInlineData, b[0])
	}
	length := binary.LittleEndian.Uint32(b[1:5])
	if length != uint32(len(data)) {
		t.Fatalf("expected length %d, got %d", len(data), length)
	}
	if !bytes.Equal(b[5:], data) {
		t.Fatalf("expected data %q, got %q", data, b[5:])
	}
}

func TestWriterFileBackedData(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteFileBackedData(1024, 3, "path/to/file.txt"); err != nil {
		t.Fatalf("WriteFileBackedData: %v", err)
	}

	b := buf.Bytes()
	filename := "path/to/file.txt"
	// type(1) + content_length(8) + dirfd_index(4) + name_len(4) + filename
	expected := 1 + 8 + 4 + 4 + len(filename)
	if len(b) != expected {
		t.Fatalf("expected %d bytes, got %d", expected, len(b))
	}

	off := 0
	if b[off] != ChunkFileBackedData {
		t.Fatalf("expected chunk type 0x%02x, got 0x%02x", ChunkFileBackedData, b[off])
	}
	off++

	contentLen := binary.LittleEndian.Uint64(b[off : off+8])
	off += 8
	if contentLen != 1024 {
		t.Fatalf("expected content_length 1024, got %d", contentLen)
	}

	dirfdIdx := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if dirfdIdx != 3 {
		t.Fatalf("expected dirfd_index 3, got %d", dirfdIdx)
	}

	nameLen := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if nameLen != uint32(len(filename)) {
		t.Fatalf("expected name_len %d, got %d", len(filename), nameLen)
	}

	if string(b[off:]) != filename {
		t.Fatalf("expected filename %q, got %q", filename, string(b[off:]))
	}
}

func TestWriterMixed(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteMetadata([]byte("header")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFileBackedData(100, 0, "f.dat"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteInlineData([]byte("inline")); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()
	off := 0

	// Chunk 1: Metadata "header"
	if b[off] != ChunkMetadata {
		t.Fatalf("chunk1: expected type 0x%02x, got 0x%02x", ChunkMetadata, b[off])
	}
	off++
	length := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if length != 6 {
		t.Fatalf("chunk1: expected length 6, got %d", length)
	}
	if string(b[off:off+6]) != "header" {
		t.Fatalf("chunk1: expected 'header', got %q", string(b[off:off+6]))
	}
	off += 6

	// Chunk 2: FileBackedData
	if b[off] != ChunkFileBackedData {
		t.Fatalf("chunk2: expected type 0x%02x, got 0x%02x", ChunkFileBackedData, b[off])
	}
	off++
	contentLen := binary.LittleEndian.Uint64(b[off : off+8])
	off += 8
	if contentLen != 100 {
		t.Fatalf("chunk2: expected content_length 100, got %d", contentLen)
	}
	dirfdIdx := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if dirfdIdx != 0 {
		t.Fatalf("chunk2: expected dirfd_index 0, got %d", dirfdIdx)
	}
	nameLen := binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if nameLen != 5 {
		t.Fatalf("chunk2: expected name_len 5, got %d", nameLen)
	}
	if string(b[off:off+5]) != "f.dat" {
		t.Fatalf("chunk2: expected 'f.dat', got %q", string(b[off:off+5]))
	}
	off += 5

	// Chunk 3: InlineData "inline"
	if b[off] != ChunkInlineData {
		t.Fatalf("chunk3: expected type 0x%02x, got 0x%02x", ChunkInlineData, b[off])
	}
	off++
	length = binary.LittleEndian.Uint32(b[off : off+4])
	off += 4
	if length != 6 {
		t.Fatalf("chunk3: expected length 6, got %d", length)
	}
	if string(b[off:off+6]) != "inline" {
		t.Fatalf("chunk3: expected 'inline', got %q", string(b[off:off+6]))
	}
	off += 6

	if off != len(b) {
		t.Fatalf("expected %d total bytes, got %d", off, len(b))
	}
}

func TestInlineDataWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	data := []byte("test data for inline writer")
	iw, err := w.InlineDataWriter(int64(len(data)))
	if err != nil {
		t.Fatalf("InlineDataWriter: %v", err)
	}

	n, err := io.Copy(iw, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("copied %d bytes, expected %d", n, len(data))
	}

	// Verify same binary format as WriteInlineData
	var expected bytes.Buffer
	ew := NewWriter(&expected)
	if err := ew.WriteInlineData(data); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(buf.Bytes(), expected.Bytes()) {
		t.Fatal("InlineDataWriter output differs from WriteInlineData")
	}
}

func TestInlineDataWriterZeroSize(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	iw, err := w.InlineDataWriter(0)
	if err != nil {
		t.Fatalf("InlineDataWriter(0): %v", err)
	}
	if iw != io.Discard {
		t.Fatal("expected io.Discard for zero size")
	}

	iw, err = w.InlineDataWriter(-5)
	if err != nil {
		t.Fatalf("InlineDataWriter(-5): %v", err)
	}
	if iw != io.Discard {
		t.Fatal("expected io.Discard for negative size")
	}

	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %d bytes", buf.Len())
	}
}

// mockStore implements the Store interface for testing.
type mockStore struct {
	bigData      map[string]map[string][]byte
	resolveID    map[string][2]string
	layerParents map[string]string
	parentErr    map[string]error
}

func (m *mockStore) ImageBigData(id, key string) ([]byte, error) {
	if img, ok := m.bigData[id]; ok {
		if data, ok := img[key]; ok {
			return data, nil
		}
	}
	return nil, fmt.Errorf("big data %s/%s not found", id, key)
}

func (m *mockStore) ResolveImageID(id string) (string, string, error) {
	if r, ok := m.resolveID[id]; ok {
		return r[0], r[1], nil
	}
	return "", "", fmt.Errorf("image %s not found", id)
}

func (m *mockStore) LayerParent(id string) (string, error) {
	if m.parentErr != nil {
		if err, ok := m.parentErr[id]; ok {
			return "", err
		}
	}
	parent := m.layerParents[id]
	return parent, nil
}

func makeManifest(configDigest string, layerDigests []string) []byte {
	layers := make([]v1.Descriptor, len(layerDigests))
	for i, d := range layerDigests {
		layers[i] = v1.Descriptor{Digest: digest.Digest(d)}
	}
	m := v1.Manifest{
		Config: v1.Descriptor{Digest: digest.Digest(configDigest)},
		Layers: layers,
	}
	b, _ := json.Marshal(m)
	return b
}

func TestGetImageMetadata(t *testing.T) {
	manifestJSON := makeManifest("sha256:configabc", []string{"sha256:layer1", "sha256:layer2", "sha256:layer3"})
	configJSON := []byte(`{"architecture":"amd64"}`)

	store := &mockStore{
		resolveID: map[string][2]string{
			"img1": {"actual1", "layer-c"},
		},
		bigData: map[string]map[string][]byte{
			"actual1": {
				"manifest":         manifestJSON,
				"sha256:configabc": configJSON,
			},
		},
		layerParents: map[string]string{
			"layer-c": "layer-b",
			"layer-b": "layer-a",
			"layer-a": "",
		},
	}

	meta, err := GetImageMetadata(store, "img1")
	if err != nil {
		t.Fatalf("GetImageMetadata: %v", err)
	}

	if !bytes.Equal(meta.ManifestJSON, manifestJSON) {
		t.Fatal("manifest mismatch")
	}
	if !bytes.Equal(meta.ConfigJSON, configJSON) {
		t.Fatal("config mismatch")
	}

	if len(meta.LayerDigests) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(meta.LayerDigests))
	}
	if meta.LayerDigests[0] != "layer-c" || meta.LayerDigests[1] != "layer-b" || meta.LayerDigests[2] != "layer-a" {
		t.Fatalf("unexpected layer order: %v", meta.LayerDigests)
	}
}

func TestGetImageMetadataDepthCap(t *testing.T) {
	manifestJSON := makeManifest("sha256:cfg", []string{"sha256:l1"})

	parents := make(map[string]string)
	for i := range 5000 {
		parents[fmt.Sprintf("layer-%d", i)] = fmt.Sprintf("layer-%d", i+1)
	}

	store := &mockStore{
		resolveID: map[string][2]string{
			"img": {"img", "layer-0"},
		},
		bigData: map[string]map[string][]byte{
			"img": {
				"manifest":   manifestJSON,
				"sha256:cfg": []byte(`{}`),
			},
		},
		layerParents: parents,
	}

	_, err := GetImageMetadata(store, "img")
	if err == nil {
		t.Fatal("expected depth cap error")
	}
	if !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("expected depth error, got: %v", err)
	}
}

func TestGetImageMetadataLayerParentError(t *testing.T) {
	manifestJSON := makeManifest("sha256:cfg", []string{"sha256:l1"})

	store := &mockStore{
		resolveID: map[string][2]string{
			"img": {"img", "layer-top"},
		},
		bigData: map[string]map[string][]byte{
			"img": {
				"manifest":   manifestJSON,
				"sha256:cfg": []byte(`{}`),
			},
		},
		layerParents: map[string]string{
			"layer-top": "layer-mid",
		},
		parentErr: map[string]error{
			"layer-mid": fmt.Errorf("storage I/O error"),
		},
	}

	_, err := GetImageMetadata(store, "img")
	if err == nil {
		t.Fatal("expected error from LayerParent")
	}
	if !strings.Contains(err.Error(), "storage I/O error") {
		t.Fatalf("expected wrapped I/O error, got: %v", err)
	}
}

func TestGetImageMetadataFallbackToManifest(t *testing.T) {
	manifestJSON := makeManifest("sha256:cfg", []string{"sha256:digest-a", "sha256:digest-b"})

	store := &mockStore{
		resolveID: map[string][2]string{
			"img": {"img", ""},
		},
		bigData: map[string]map[string][]byte{
			"img": {
				"manifest":   manifestJSON,
				"sha256:cfg": []byte(`{}`),
			},
		},
		layerParents: map[string]string{},
	}

	meta, err := GetImageMetadata(store, "img")
	if err != nil {
		t.Fatalf("GetImageMetadata: %v", err)
	}

	if len(meta.LayerDigests) != 2 {
		t.Fatalf("expected 2 layers from manifest fallback, got %d", len(meta.LayerDigests))
	}
	if meta.LayerDigests[0] != "sha256:digest-a" || meta.LayerDigests[1] != "sha256:digest-b" {
		t.Fatalf("unexpected fallback layers: %v", meta.LayerDigests)
	}
}
