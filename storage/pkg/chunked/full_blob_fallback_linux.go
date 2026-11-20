package chunked

import (
	"errors"
	"fmt"
	"io"
)

// fullBlobFallback is an ImageSourceSeekable that forwards requests to source until it fails with
// ErrFullBlobResponse; from then on, it downloads the entire blob to an unnamed temporary file in tmpDir
// and serves this and all later requests from there.
// It is not safe for concurrent use. The caller must call Close.
type fullBlobFallback struct {
	source   ImageSourceSeekable
	tmpDir   string
	blobSize int64
	local    *seekableFile // nil until the blob has been downloaded
}

func (f *fullBlobFallback) GetBlobAt(chunks []ImageSourceChunk) (chan io.ReadCloser, chan error, error) {
	if f.local == nil {
		streams, errs, err := f.source.GetBlobAt(chunks)
		if _, ok := errors.AsType[ErrFullBlobResponse](err); !ok {
			return streams, errs, err
		}
		file, err := openTmpFile(f.tmpDir)
		if err != nil {
			return nil, nil, err
		}
		if _, err := copyAllBlobToFile(f.source, f.blobSize, file, makeCopyBuffer()); err != nil {
			file.Close()
			return nil, nil, err
		}
		if fi, err := file.Stat(); err != nil || fi.Size() != f.blobSize {
			file.Close()
			if err == nil {
				err = fmt.Errorf("blob size %d does not match the expected size %d", fi.Size(), f.blobSize)
			}
			return nil, nil, err
		}
		f.local = newSeekableFile(file)
	}
	return f.local.GetBlobAt(chunks)
}

// Close releases the temporary file, if any.
func (f *fullBlobFallback) Close() error {
	if f.local == nil {
		return nil
	}
	err := f.local.Close()
	f.local = nil
	return err
}
