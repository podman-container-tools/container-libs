package splitfdstreamserver

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// fakeLayerFiles serves content for the entries named in byName, and reports
// every other entry as unavailable.
type fakeLayerFiles struct {
	byName map[string][]byte
}

func (f *fakeLayerFiles) DirFDs() []*os.File { return nil }
func (f *fakeLayerFiles) Close() error       { return nil }

func (f *fakeLayerFiles) Lookup(name string, size int64, _ bool) (int, string, bool, error) {
	content, ok := f.byName[name]
	if !ok || int64(len(content)) != size {
		return 0, "", false, nil
	}
	return 0, name, true, nil
}

// reassemble turns a splitdirfdstream back into the tar it was made from,
// taking file-backed content from files.
func reassemble(t *testing.T, stream []byte, files *fakeLayerFiles) []byte {
	t.Helper()
	var out bytes.Buffer
	for len(stream) > 0 {
		switch typ := stream[0]; typ {
		case chunkMetadata, chunkInlineData:
			n := binary.LittleEndian.Uint32(stream[1:5])
			out.Write(stream[5 : 5+n])
			stream = stream[5+n:]
		case chunkFileBackedData:
			length := binary.LittleEndian.Uint64(stream[1:9])
			nameLen := binary.LittleEndian.Uint32(stream[13:17])
			name := string(stream[17 : 17+nameLen])
			content, ok := files.byName[name]
			if !ok {
				t.Fatalf("stream references unknown file %q", name)
			}
			if uint64(len(content)) != length {
				t.Fatalf("file %q: declared %d bytes, have %d", name, length, len(content))
			}
			out.Write(content)
			stream = stream[17+nameLen:]
		default:
			t.Fatalf("unknown chunk type 0x%02x", typ)
		}
	}
	return out.Bytes()
}

// TestConvertRoundTrip is the property the format exists for: what the
// consumer reassembles has to be the original archive, byte for byte,
// because it is checked against the layer's diff ID.
func TestConvertRoundTrip(t *testing.T) {
	referenced := bytes.Repeat([]byte("r"), 4096)
	inlined := []byte("inlined content")
	notUTF8 := []byte("content of a file whose name is not UTF-8")

	var original bytes.Buffer
	tw := tar.NewWriter(&original)
	entries := []struct {
		hdr     tar.Header
		content []byte
	}{
		{tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
		// Carried as a FileBackedData chunk.
		{tar.Header{Name: "dir/referenced", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(referenced))}, referenced},
		// Not available as a file, so it has to be inlined.
		{tar.Header{Name: "dir/inlined", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(inlined))}, inlined},
		// Available as a file, but the consumer only takes UTF-8 names.
		{tar.Header{Name: "dir/\xff", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(notUTF8))}, notUTF8},
		// Exercises PAX extended headers, which the tar reader resolves
		// transparently and which therefore have to be passed through.
		{tar.Header{
			Name: "dir/with-xattrs", Typeflag: tar.TypeReg, Mode: 0o644, Size: 0,
			PAXRecords: map[string]string{"SCHILY.xattr.user.test": "value"},
		}, nil},
		{tar.Header{Name: "dir/link", Typeflag: tar.TypeSymlink, Linkname: "referenced", Mode: 0o777}, nil},
		{tar.Header{Name: "empty", Typeflag: tar.TypeReg, Mode: 0o644, Size: 0}, nil},
	}
	for _, e := range entries {
		hdr := e.hdr
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	files := &fakeLayerFiles{byName: map[string][]byte{"dir/referenced": referenced, "dir/\xff": notUTF8}}

	var stream bytes.Buffer
	if err := convert(&stream, bytes.NewReader(original.Bytes()), files, false); err != nil {
		t.Fatalf("convert: %v", err)
	}

	if got := reassemble(t, stream.Bytes(), files); !bytes.Equal(got, original.Bytes()) {
		t.Fatalf("reassembled archive differs: %d bytes vs %d", len(got), original.Len())
	}

	// The point of the format: the referenced content travels as a
	// reference, not as bytes.
	if bytes.Contains(stream.Bytes(), referenced) {
		t.Error("content of dir/referenced was copied into the stream")
	}
	if !bytes.Contains(stream.Bytes(), inlined) {
		t.Error("content of dir/inlined should have been inlined")
	}
	if !bytes.Contains(stream.Bytes(), notUTF8) {
		t.Error("content of the file with a non-UTF-8 name should have been inlined")
	}
}
