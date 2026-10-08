// Package splitfdstreamserver serves layers as splitdirfdstream over the
// org.composefs.Oci varlink interface.
package splitfdstreamserver

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"unicode/utf8"

	graphdriver "go.podman.io/storage/drivers"
)

// copyBufferSize is the buffer used when content has to be copied into the
// stream rather than referenced.
const copyBufferSize = 1 << 20

// convert writes tarStream to w in the splitdirfdstream format, referencing
// the content of regular files through files wherever it can.  With
// onlyWorldReadable, only files anyone may read are referenced.
//
// tarStream has to be the layer's diff exactly as it is stored: what the
// consumer reassembles has to hash to the layer's diff ID, so every byte
// that is not file content is passed through untouched rather than
// regenerated.  That includes entry headers, the extended headers the
// tar reader resolves on our behalf, inter-entry padding and whatever
// follows the end-of-archive marker.
func convert(w io.Writer, tarStream io.Reader, files graphdriver.LayerFiles, onlyWorldReadable bool) error {
	writer := newWriter(w)
	cr := &capturingReader{r: tarStream}
	tr := tar.NewReader(cr)

	var buf []byte
	for {
		// Everything the tar reader consumes to produce the next header is
		// structure: padding left over from the previous entry, any
		// extended headers, and the header itself.
		var header *tar.Header
		structure, err := cr.capture(func() error {
			var err error
			header, err = tr.Next()
			return err
		})
		if err == io.EOF {
			// Whatever follows the end-of-archive marker - record padding,
			// typically - is part of the blob as well.
			trailing, err := io.ReadAll(cr.r)
			if err != nil {
				return fmt.Errorf("reading end of archive: %w", err)
			}
			if err := writer.writeMetadata(append(structure, trailing...)); err != nil {
				return fmt.Errorf("writing end of archive: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar header: %w", err)
		}
		if err := writer.writeMetadata(structure); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", header.Name, err)
		}

		if header.Typeflag != tar.TypeReg || header.Size == 0 {
			continue
		}

		index, filename, ok, err := files.Lookup(header.Name, header.Size, onlyWorldReadable)
		if err != nil {
			return fmt.Errorf("looking up %s: %w", header.Name, err)
		}
		// The consumer only accepts UTF-8 names; inline anything else.
		if ok && utf8.ValidString(filename) {
			if err := writer.writeFileBackedData(header.Size, index, filename); err != nil {
				return fmt.Errorf("writing FileBackedData for %s: %w", header.Name, err)
			}
			// The consumer reads the content from the file; drop our copy,
			// but keep reading so that the tar reader advances.
			if _, err := io.CopyN(io.Discard, tr, header.Size); err != nil {
				return fmt.Errorf("skipping content of %s: %w", header.Name, err)
			}
			continue
		}

		if buf == nil {
			buf = make([]byte, copyBufferSize)
		}
		iw, err := writer.inlineDataWriter(header.Size)
		if err != nil {
			return fmt.Errorf("writing inline prefix for %s: %w", header.Name, err)
		}
		if _, err := io.CopyBuffer(iw, io.LimitReader(tr, header.Size), buf); err != nil {
			return fmt.Errorf("writing inline content of %s: %w", header.Name, err)
		}
	}
}

// capturingReader can record the bytes read through it, so that a stretch of
// input consumed by someone else can be reproduced verbatim.
type capturingReader struct {
	r         io.Reader
	buf       bytes.Buffer
	capturing bool
}

func (c *capturingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.capturing && n > 0 {
		c.buf.Write(p[:n])
	}
	return n, err
}

// capture runs f and returns everything f caused to be read from the
// underlying reader.
func (c *capturingReader) capture(f func() error) ([]byte, error) {
	c.buf.Reset()
	c.capturing = true
	err := f()
	c.capturing = false
	return bytes.Clone(c.buf.Bytes()), err
}
