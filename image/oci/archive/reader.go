package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/internal/tmpdir"
	"go.podman.io/image/v5/types"
	"go.podman.io/storage/pkg/archive"
)

// Reader manages the temp directory that the oci archive is untarred to and the
// manifest of the images. It allows listing its contents and accessing
// individual images with less overhead than creating image references individually
// (because the archive is, if necessary, copied or decompressed only once)
type Reader struct {
	manifest      *imgspecv1.Index
	tempDirectory string
	path          string // The original, user-specified path
}

func NewReader(ctx context.Context, sys *types.SystemContext, src string) (*Reader, error) {
	arch, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer arch.Close()

	dst, err := tmpdir.MkDirBigFileTemp(sys, "oci")
	if err != nil {
		return nil, fmt.Errorf("error creating temp directory: %w", err)
	}

	succeeded := false
	defer func() {
		if !succeeded {
			os.RemoveAll(dst)
		}
	}()

	reader := Reader{
		tempDirectory: dst,
		path:          src,
	}

	if err := archive.NewDefaultArchiver().Untar(arch, dst, &archive.TarOptions{NoLchown: true}); err != nil {
		return nil, fmt.Errorf("untarring file %q: %w", dst, err)
	}

	// Confine file reads to the temp directory to prevent,
	// malicious symlinks in the tarball from reading files
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	indexJSON, err := root.Open("index.json")
	if err != nil {
		return nil, err
	}
	defer indexJSON.Close()

	reader.manifest = &imgspecv1.Index{}
	if err := json.NewDecoder(indexJSON).Decode(reader.manifest); err != nil {
		return nil, err
	}

	succeeded = true // Success! The defer will now leave the directory alone.
	return &reader, nil
}

// ListResult is returned by Reader.List
type ListResult struct {
	ImageRef           types.ImageReference
	ManifestDescriptor imgspecv1.Descriptor
}

// List returns the list of image references and their manifest descriptors
func (r *Reader) List() ([]ListResult, error) {
	var res []ListResult

	for i, md := range r.manifest.Manifests {
		refName := md.Annotations["org.opencontainers.image.ref.name"]
		index := -1
		if refName == "" {
			index = i
		}
		ref, err := newReference(r.path, refName, index, r, nil)
		if err != nil {
			return nil, fmt.Errorf("error creating image reference: %w", err)
		}
		reference := ListResult{
			ImageRef:           ref,
			ManifestDescriptor: md,
		}
		res = append(res, reference)
	}
	return res, nil
}

// Close deletes temporary files associated with the Reader, if any.
func (r *Reader) Close() error {
	return os.RemoveAll(r.tempDirectory)
}
