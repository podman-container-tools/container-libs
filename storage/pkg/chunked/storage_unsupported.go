//go:build !linux

package chunked

import (
	"context"
	"errors"
	"fmt"

	digest "github.com/opencontainers/go-digest"
	storage "go.podman.io/storage"
	graphdriver "go.podman.io/storage/drivers"
)

// SkipMitigation is not supported on non-Linux platforms.
func SkipMitigation(d graphdriver.Differ) error {
	return fmt.Errorf("SkipMitigation is not supported on this platform")
}

// NewDiffer returns a differ than can be used with [Store.PrepareStagedLayer].
// The caller must call Close() on the returned Differ.
func NewDiffer(ctx context.Context, store storage.Store, blobDigest digest.Digest, blobSize int64, annotations map[string]string, iss ImageSourceSeekable) (graphdriver.Differ, error) {
	return nil, newErrFallbackToOrdinaryLayerDownload(errors.New("format not supported on this system"))
}
