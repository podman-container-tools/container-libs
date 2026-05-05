package tarbackfill

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/stringutils"
)

func makeTarByteSlice(t *testing.T, headers []*tar.Header, trailerLength int) []byte {
	t.Helper()
	var buf bytes.Buffer
	block := make([]byte, 256)
	for i := range block {
		block[i] = byte(i % 256)
	}
	tw := tar.NewWriter(&buf)
	for i := range headers {
		hdr := *headers[i]
		hdr.Format = tar.FormatPAX
		require.NoError(t, tw.WriteHeader(&hdr))
		if hdr.Size > 0 {
			written := int64(0)
			for written < hdr.Size {
				left := hdr.Size - written
				if left > int64(len(block)) {
					left = int64(len(block))
				}
				n, err := tw.Write(block[:int(left)])
				if err != nil {
					break
				}
				written += int64(n)
			}
		}
		require.NoError(t, tw.Flush())
	}
	require.NoError(t, tw.Close())
	buf.Write(make([]byte, trailerLength))
	return buf.Bytes()
}

func consumeTar(t *testing.T, reader io.Reader, fn func(*tar.Header)) {
	t.Helper()
	tr := tar.NewReader(reader)
	hdr, err := tr.Next()
	for hdr != nil {
		if fn != nil {
			fn(hdr)
		}
		if hdr.Size != 0 {
			n, err := io.Copy(io.Discard, tr)
			require.NoErrorf(t, err, "unexpected error copying payload for %q", hdr.Name)
			require.Equalf(t, hdr.Size, n, "payload for %q had unexpected length", hdr.Name)
		}
		if err != nil {
			break
		}
		hdr, err = tr.Next()
	}
	require.ErrorIs(t, err, io.EOF)
	_, err = io.Copy(io.Discard, reader)
	require.NoError(t, err)
}

type backfillerLogger struct {
	log      *[]string
	backfill bool
	mode     int64
	uid, gid int
	date     time.Time
}

func (b *backfillerLogger) Backfill(path string) (*tar.Header, error) {
	if !stringutils.InSlice(*b.log, path) {
		*b.log = append(*b.log, path)
		sort.Strings(*b.log)
	}
	if b.backfill {
		return &tar.Header{Name: path, Typeflag: tar.TypeDir, Mode: b.mode, Uid: b.uid, Gid: b.gid, ModTime: b.date}, nil
	}
	return nil, nil
}

func TestNewIOReaderWithBackfiller(t *testing.T) {
	directoryMode := int64(0o750)
	directoryUID := 5
	directoryGID := 6
	now := time.Now().UTC()
	testCases := []struct {
		description string
		inputs      []*tar.Header
		backfills   []string
		outputs     []*tar.Header
	}{
		{
			description: "empty",
		},
		{
			description: "shallow",
			inputs: []*tar.Header{
				{Name: "a/b", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1, Gid: 2, Size: 1234, ModTime: now},
				{Name: "a/c", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 1234, ModTime: now},
				{Name: "a/d", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 5, Gid: 6, Size: 0, ModTime: now},
			},
			backfills: []string{"a"},
			outputs: []*tar.Header{
				{Name: "a/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/b", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1, Gid: 2, Size: 1234, ModTime: now},
				{Name: "a/c", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 1234, ModTime: now},
				{Name: "a/d", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 5, Gid: 6, Size: 0, ModTime: now},
			},
		},
		{
			description: "deep",
			inputs: []*tar.Header{
				{Name: "a/c", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 1234, ModTime: now},
				{Name: "a/b/c/d/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 1, Gid: 2, Size: 0, ModTime: now},
				{Name: "a/b/c/d/e/f/g", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 12346, ModTime: now},
			},
			backfills: []string{"a", "a/b", "a/b/c", "a/b/c/d/e", "a/b/c/d/e/f"},
			outputs: []*tar.Header{
				{Name: "a/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/c", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 1234, ModTime: now},
				{Name: "a/b/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/b/c/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/b/c/d/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 1, Gid: 2, Size: 0, ModTime: now},
				{Name: "a/b/c/d/e/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/b/c/d/e/f/", Typeflag: tar.TypeDir, Mode: directoryMode, Uid: directoryUID, Gid: directoryGID, Size: 0, ModTime: now},
				{Name: "a/b/c/d/e/f/g", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 3, Gid: 4, Size: 12346, ModTime: now},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.description, func(t *testing.T) {
			for _, paddingSize := range []int{0, 512, 2048} {
				t.Run(fmt.Sprintf("paddingSize=%d", paddingSize), func(t *testing.T) {
					tarBytes := makeTarByteSlice(t, testCase.inputs, paddingSize)

					t.Run("logged", func(t *testing.T) {
						var backfillLog []string
						reader := bytes.NewReader(tarBytes)
						rc := NewIOReaderWithBackfiller(reader, &backfillerLogger{
							log:      &backfillLog,
							backfill: false,
							mode:     directoryMode,
							uid:      directoryUID,
							gid:      directoryGID,
							date:     now,
						})
						defer rc.Close()
						consumeTar(t, rc, nil)
						require.Equal(t, testCase.backfills, backfillLog)
						assert.Zero(t, reader.Len())
					})

					t.Run("filled", func(t *testing.T) {
						var backfillLog []string
						reader := bytes.NewReader(tarBytes)
						rc := NewIOReaderWithBackfiller(reader, &backfillerLogger{
							log:      &backfillLog,
							backfill: true,
							mode:     directoryMode,
							uid:      directoryUID,
							gid:      directoryGID,
							date:     now,
						})
						defer rc.Close()

						var outputs []*tar.Header
						consumeTar(t, rc, func(hdr *tar.Header) {
							tmp := *hdr
							outputs = append(outputs, &tmp)
						})
						require.Equal(t, len(testCase.outputs), len(outputs))
						for i := range outputs {
							require.EqualValues(t, testCase.outputs[i].Name, outputs[i].Name)
							require.EqualValues(t, testCase.outputs[i].Mode, outputs[i].Mode)
							require.EqualValues(t, testCase.outputs[i].Typeflag, outputs[i].Typeflag)
							require.True(t, outputs[i].ModTime.UTC().Equal(testCase.outputs[i].ModTime.UTC()))
						}
						require.Equal(t, testCase.backfills, backfillLog)
						assert.Zero(t, reader.Len())
					})
				})
			}
		})
	}
}
