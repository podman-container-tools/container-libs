package archive

import (
	"fmt"
	"io"
	"os"

	"go.podman.io/image/v5/internal/tmpdir"
	"go.podman.io/image/v5/types"
	"go.podman.io/storage/pkg/archive"
)

// Writer manages an in-progress OCI archive and allows adding images to it
type Writer struct {
	// tempDir will be tarred to oci archive
	tempDir string
	// user-specified path
	path string
}

// NewWriter returns a new Writer for creating an archive at path
// The caller should call .Close() on the returned object.
func NewWriter(sys *types.SystemContext, path string) (*Writer, error) {
	tempDir, err := tmpdir.MkDirBigFileTemp(sys, "oci")
	if err != nil {
		return nil, err
	}
	return &Writer{
		tempDir: tempDir,
		path:    path,
	}, nil
}

// NewReference returns an image reference to add an image to the archive.
// Multiple images can be added using this method. Note that name must be
// unique
func (w *Writer) NewReference(name string) (types.ImageReference, error) {
	return newReference(w.path, name, -1, nil, w)
}

// Close finishes creation of the archive and deletes the temp directory.
func (w *Writer) Close() error {
	defer os.RemoveAll(w.tempDir)
	arch, err := os.Create(w.path)
	if err != nil {
		return err
	}
	defer arch.Close()
	rc, err := archive.TarWithOptions(w.tempDir, &archive.TarOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(arch, rc)
	if err != nil {
		return fmt.Errorf("error writing to %q: %w", w.path, err)
	}
	return nil
}
