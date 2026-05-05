package tarbackfill

import (
	"archive/tar"
	"io"
	"path"
	"strings"
)

// Backfiller can synthesize missing tar headers for parent directories.
type Backfiller interface {
	Backfill(string) (*tar.Header, error)
}

// Reader injects synthesized parent-directory headers into a tar stream.
type Reader struct {
	*tar.Reader
	backfiller      Backfiller
	seen            map[string]struct{}
	queue           []*tar.Header
	currentIsQueued bool
	err             error
}

// NewReaderWithBackfiller returns a tar reader that backfills parent dirs.
func NewReaderWithBackfiller(r *tar.Reader, backfiller Backfiller) *Reader {
	return &Reader{
		Reader:     r,
		backfiller: backfiller,
		seen:       make(map[string]struct{}),
	}
}

// Next returns either the next original entry or a synthesized parent dir.
func (r *Reader) Next() (*tar.Header, error) {
	if len(r.queue) > 0 {
		next := r.queue[0]
		r.queue = r.queue[1:]
		r.currentIsQueued = len(r.queue) > 0
		return next, nil
	}

	r.currentIsQueued = false
	if r.err != nil {
		return nil, r.err
	}

	hdr, err := r.Reader.Next()
	if err != nil {
		r.err = err
	}
	if hdr == nil {
		return nil, err
	}

	for {
		name := strings.Trim(hdr.Name, "/")
		if hdr.Typeflag == tar.TypeDir {
			r.seen[name] = struct{}{}
		}

		p := name
		dir, _ := path.Split(name)
		for dir != p {
			dir = strings.Trim(dir, "/")
			if _, ok := r.seen[dir]; dir == "" || ok || dir == name {
				return hdr, err
			}

			newHdr, bfErr := r.backfiller.Backfill(dir)
			if bfErr != nil {
				r.err = bfErr
				return nil, bfErr
			}
			if newHdr == nil {
				dir, _ = path.Split(dir)
				continue
			}

			newHdr.Format = tar.FormatPAX
			newHdr.Name = strings.Trim(newHdr.Name, "/")
			if newHdr.Typeflag == tar.TypeDir {
				r.seen[newHdr.Name] = struct{}{}
				newHdr.Name += "/"
			}
			r.queue = append([]*tar.Header{hdr}, r.queue...)
			hdr = newHdr
			r.currentIsQueued = true
			dir, _ = path.Split(dir)
		}
	}
}

// Read returns an empty payload for queued synthesized directory headers.
func (r *Reader) Read(b []byte) (int, error) {
	if r.currentIsQueued {
		return 0, nil
	}
	return r.Reader.Read(b)
}

// NewIOReaderWithBackfiller wraps an io.Reader tar stream with backfilling.
func NewIOReaderWithBackfiller(reader io.Reader, backfiller Backfiller) io.ReadCloser {
	rc, wc := io.Pipe()
	go func() {
		tr := NewReaderWithBackfiller(tar.NewReader(reader), backfiller)
		tw := tar.NewWriter(wc)
		hdr, err := tr.Next()
		defer func() {
			closeErr := tw.Close()
			_, _ = io.Copy(wc, reader)
			if err != nil {
				_ = wc.CloseWithError(err)
			} else if closeErr != nil {
				_ = wc.CloseWithError(closeErr)
			} else {
				_ = wc.Close()
			}
		}()
		for hdr != nil {
			if writeErr := tw.WriteHeader(hdr); writeErr != nil {
				err = writeErr
				return
			}
			if err != nil {
				break
			}
			if hdr.Size != 0 {
				if _, err = io.Copy(tw, tr); err != nil {
					return
				}
			}
			hdr, err = tr.Next()
		}
	}()
	return rc
}
