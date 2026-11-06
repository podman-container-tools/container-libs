package splitfdstreamserver

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
	"go.podman.io/storage/internal/driver"
)

// fakeLayerFiles serves content for the entries named in byName, and reports
// every other entry as unavailable.  Content that cannot be referenced is
// read from inline, if set.
type fakeLayerFiles struct {
	byName map[string][]byte
	inline storage.FileGetter
}

func (f *fakeLayerFiles) DirFDs() []*os.File { return nil }
func (f *fakeLayerFiles) Close() error       { return nil }

func (f *fakeLayerFiles) Open(name string) (io.ReadCloser, error) {
	if f.inline == nil {
		return nil, os.ErrNotExist
	}
	return f.inline.Get(name)
}

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
			require.True(t, ok, "stream references unknown file %q", name)
			require.Equal(t, length, uint64(len(content)), "file %q", name)
			out.Write(content)
			stream = stream[17+nameLen:]
		default:
			require.FailNowf(t, "unknown chunk type", "0x%02x", typ)
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
		require.NoError(t, tw.WriteHeader(&hdr))
		_, err := tw.Write(e.content)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	// Record the archive the way the layer store does when a layer is
	// created: tar-split metadata, and file contents kept separately.
	var tarSplit bytes.Buffer
	contents := storage.NewBufferFileGetPutter()
	ts, done, err := asm.NewInputTarStreamWithDone(bytes.NewReader(original.Bytes()), storage.NewJSONPacker(&tarSplit), contents)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, ts)
	require.NoError(t, err)
	require.NoError(t, ts.Close())
	// Wait for everything to be recorded before reading it back.
	require.NoError(t, <-done)

	getter := &recordingGetter{FileGetter: contents, got: map[string]bool{}}
	files := &fakeLayerFiles{byName: map[string][]byte{"dir/referenced": referenced, "dir/\xff": notUTF8}, inline: getter}
	layer := &convertLayer{tarSplit: &tarSplit, files: files}

	var stream bytes.Buffer
	require.NoError(t, convert(&stream, layer, false))

	assert.Equal(t, original.Bytes(), reassemble(t, stream.Bytes(), files))

	// The point of the format: the referenced content travels as a
	// reference, not as bytes.
	assert.NotContains(t, stream.String(), string(referenced))
	assert.Contains(t, stream.String(), string(inlined))
	assert.NotContains(t, stream.String(), string(notUTF8))
	// Content that is referenced is not even read.
	assert.Equal(t, map[string]bool{"dir/inlined": true}, getter.got)
}

// recordingGetter records which entries had their content read.
// convertLayer is a Layer made of the parts a conversion reads.
type convertLayer struct {
	tarSplit io.Reader
	files    *fakeLayerFiles
}

func (l *convertLayer) Files() driver.LayerFiles { return l.files }
func (l *convertLayer) TarSplit() io.Reader      { return l.tarSplit }
func (l *convertLayer) Close() error             { return nil }

type recordingGetter struct {
	storage.FileGetter
	got map[string]bool
}

func (g *recordingGetter) Get(name string) (io.ReadCloser, error) {
	g.got[name] = true
	return g.FileGetter.Get(name)
}
