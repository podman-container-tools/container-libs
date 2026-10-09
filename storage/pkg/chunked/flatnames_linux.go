package chunked

import (
	"errors"
	"io"
	"os"

	digest "github.com/opencontainers/go-digest"
	storage "go.podman.io/storage"
	"go.podman.io/storage/pkg/chunked/internal/minimal"
	storagePath "go.podman.io/storage/pkg/chunked/internal/path"
)

// LayerFlatFileNames maps the name of a tar entry, exactly as it appears in
// the archive, to the name its content is stored under when the layer is
// applied in the flat layout (graphdriver.DifferOutputFormatFlat): under a
// name derived from its digest rather than under its own name.  That is the
// layout a layer pulled partially for composefs uses.
//
// The mapping comes from the layer's table of contents, so it is nil for a
// layer that has none.  Having one does not mean the layer was stored flat:
// a layer pulled partially without composefs keeps its files under their own
// names, and only the graph driver knows which layout it used.
func LayerFlatFileNames(store storage.Store, layerID string) (map[string]string, error) {
	r, err := store.LayerBigData(layerID, bigDataKey)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer r.Close()

	manifest, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	toc, err := unmarshalToc(manifest)
	if err != nil {
		return nil, err
	}

	names := make(map[string]string, len(toc.Entries))
	for _, entry := range toc.Entries {
		if entry.Type != minimal.TypeReg || entry.Digest == "" {
			continue
		}
		d, err := digest.Parse(entry.Digest)
		if err != nil {
			return nil, err
		}
		p, err := storagePath.RegularFilePathForValidatedDigest(d)
		if err != nil {
			return nil, err
		}
		names[entry.Name] = p
	}
	return names, nil
}
