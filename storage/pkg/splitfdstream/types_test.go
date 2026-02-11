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

func TestWriterInline(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	data := []byte("hello world")
	if err := w.WriteInline(data); err != nil {
		t.Fatalf("WriteInline: %v", err)
	}

	// Expect: int64 prefix (-11) + "hello world"
	b := buf.Bytes()
	if len(b) != 8+len(data) {
		t.Fatalf("expected %d bytes, got %d", 8+len(data), len(b))
	}

	prefix := int64(binary.LittleEndian.Uint64(b[:8]))
	if prefix != -int64(len(data)) {
		t.Fatalf("expected prefix %d, got %d", -int64(len(data)), prefix)
	}
	if !bytes.Equal(b[8:], data) {
		t.Fatalf("expected data %q, got %q", data, b[8:])
	}
}

func TestWriterInlineEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteInline(nil); err != nil {
		t.Fatalf("WriteInline(nil): %v", err)
	}
	if err := w.WriteInline([]byte{}); err != nil {
		t.Fatalf("WriteInline(empty): %v", err)
	}

	if buf.Len() != 0 {
		t.Fatalf("expected no output for empty inline, got %d bytes", buf.Len())
	}
}

func TestWriterExternal(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteExternal(42); err != nil {
		t.Fatalf("WriteExternal: %v", err)
	}

	b := buf.Bytes()
	if len(b) != 8 {
		t.Fatalf("expected 8 bytes, got %d", len(b))
	}

	prefix := int64(binary.LittleEndian.Uint64(b[:8]))
	if prefix != 42 {
		t.Fatalf("expected prefix 42, got %d", prefix)
	}
}

func TestWriterMixed(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	// inline "abc"
	if err := w.WriteInline([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	// external fd 0
	if err := w.WriteExternal(0); err != nil {
		t.Fatal(err)
	}
	// inline "de"
	if err := w.WriteInline([]byte("de")); err != nil {
		t.Fatal(err)
	}

	b := buf.Bytes()
	off := 0

	// First chunk: prefix -3 + "abc"
	p1 := int64(binary.LittleEndian.Uint64(b[off : off+8]))
	off += 8
	if p1 != -3 {
		t.Fatalf("chunk1 prefix: expected -3, got %d", p1)
	}
	if string(b[off:off+3]) != "abc" {
		t.Fatalf("chunk1 data: expected abc, got %q", b[off:off+3])
	}
	off += 3

	// Second chunk: external fd 0
	p2 := int64(binary.LittleEndian.Uint64(b[off : off+8]))
	off += 8
	if p2 != 0 {
		t.Fatalf("chunk2 prefix: expected 0, got %d", p2)
	}

	// Third chunk: prefix -2 + "de"
	p3 := int64(binary.LittleEndian.Uint64(b[off : off+8]))
	off += 8
	if p3 != -2 {
		t.Fatalf("chunk3 prefix: expected -2, got %d", p3)
	}
	if string(b[off:off+2]) != "de" {
		t.Fatalf("chunk3 data: expected de, got %q", b[off:off+2])
	}
	off += 2

	if off != len(b) {
		t.Fatalf("expected %d total bytes, got %d", off, len(b))
	}
}

func TestInlineWriter(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	data := []byte("test data for inline writer")
	iw, err := w.InlineWriter(int64(len(data)))
	if err != nil {
		t.Fatalf("InlineWriter: %v", err)
	}

	n, err := io.Copy(iw, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("copied %d bytes, expected %d", n, len(data))
	}

	// Verify same binary format as WriteInline
	var expected bytes.Buffer
	ew := NewWriter(&expected)
	if err := ew.WriteInline(data); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(buf.Bytes(), expected.Bytes()) {
		t.Fatal("InlineWriter output differs from WriteInline")
	}
}

func TestInlineWriterZeroSize(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	iw, err := w.InlineWriter(0)
	if err != nil {
		t.Fatalf("InlineWriter(0): %v", err)
	}
	if iw != io.Discard {
		t.Fatal("expected io.Discard for zero size")
	}

	iw, err = w.InlineWriter(-5)
	if err != nil {
		t.Fatalf("InlineWriter(-5): %v", err)
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
	bigData      map[string]map[string][]byte // imageID -> key -> data
	resolveID    map[string][2]string         // input -> [actualID, topLayerID]
	layerParents map[string]string            // layerID -> parentID
	parentErr    map[string]error             // layerID -> error
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

	// Layer chain: leaf-first (layer-c, layer-b, layer-a)
	if len(meta.LayerDigests) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(meta.LayerDigests))
	}
	if meta.LayerDigests[0] != "layer-c" || meta.LayerDigests[1] != "layer-b" || meta.LayerDigests[2] != "layer-a" {
		t.Fatalf("unexpected layer order: %v", meta.LayerDigests)
	}
}

func TestGetImageMetadataDepthCap(t *testing.T) {
	manifestJSON := makeManifest("sha256:cfg", []string{"sha256:l1"})

	// Build a circular chain
	parents := make(map[string]string)
	for i := 0; i < 5000; i++ {
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
			"img": {"img", ""}, // empty top layer → triggers fallback
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
