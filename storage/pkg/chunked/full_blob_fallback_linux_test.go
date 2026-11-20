package chunked

import (
	"bytes"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullBlobOnlySource is an ImageSourceSeekable that, like a server without multi-range support,
// only serves a request for the whole blob and fails the others with ErrFullBlobResponse.
type fullBlobOnlySource struct {
	blob     []byte
	full     []byte // If not nil, the data returned for the whole-blob request instead of blob
	requests int
}

func (s *fullBlobOnlySource) GetBlobAt(chunks []ImageSourceChunk) (chan io.ReadCloser, chan error, error) {
	s.requests++
	if len(chunks) != 1 || chunks[0].Offset != 0 {
		return nil, nil, ErrFullBlobResponse{}
	}
	data := s.blob
	if s.full != nil {
		data = s.full
	}
	return newSeekableFile(nopCloser{bytes.NewReader(data)}).GetBlobAt([]ImageSourceChunk{{Offset: 0, Length: uint64(len(data))}})
}

func TestFullBlobFallbackGetBlobAt(t *testing.T) {
	blob := []byte("0123456789abcdef")
	source := &fullBlobOnlySource{blob: blob}
	f := &fullBlobFallback{source: source, tmpDir: t.TempDir(), blobDigest: digest.FromBytes(blob), blobSize: int64(len(blob))}
	defer f.Close()

	for _, c := range []struct {
		chunks   []ImageSourceChunk
		expected []string
	}{
		{[]ImageSourceChunk{{Offset: 10, Length: 3}, {Offset: 2, Length: 2}}, []string{"abc", "23"}}, // Triggers the download
		{[]ImageSourceChunk{{Offset: 0, Length: 4}, {Offset: 12, Length: 4}}, []string{"0123", "cdef"}},
		{[]ImageSourceChunk{{Offset: 5, Length: 1}}, []string{"5"}},
	} {
		streams, errs, err := f.GetBlobAt(c.chunks)
		require.NoError(t, err)
		var got []string
		for s := range streams {
			data, err := io.ReadAll(s)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			got = append(got, string(data))
		}
		for err := range errs {
			require.NoError(t, err)
		}
		assert.Equal(t, c.expected, got)
	}
	assert.Equal(t, 2, source.requests) // The failed request, and the download; nothing after that
}

func TestFullBlobFallbackGetBlobAtWrongSize(t *testing.T) {
	blob := []byte("0123456789abcdef")
	for _, full := range [][]byte{
		append(append([]byte{}, blob...), "extra data"...), // Too much
		blob[:len(blob)-1], // Too little
	} {
		f := &fullBlobFallback{source: &fullBlobOnlySource{blob: blob, full: full}, tmpDir: t.TempDir(), blobDigest: digest.FromBytes(blob), blobSize: int64(len(blob))}
		_, _, err := f.GetBlobAt([]ImageSourceChunk{{Offset: 2, Length: 2}})
		assert.Error(t, err)
		assert.NoError(t, f.Close())
	}
}

func TestFullBlobFallbackGetBlobAtWrongDigest(t *testing.T) {
	blob := []byte("0123456789abcdef")
	full := []byte("0123456789abcdeX") // Right size, wrong content
	f := &fullBlobFallback{source: &fullBlobOnlySource{blob: blob, full: full}, tmpDir: t.TempDir(), blobDigest: digest.FromBytes(blob), blobSize: int64(len(blob))}
	_, _, err := f.GetBlobAt([]ImageSourceChunk{{Offset: 2, Length: 2}})
	assert.ErrorContains(t, err, "invalid blob digest")
	assert.NoError(t, f.Close())
}
