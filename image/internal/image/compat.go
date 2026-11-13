package image

import (
	"context"

	"go.podman.io/image/v5/internal/digests"
	"go.podman.io/image/v5/internal/imagedestination"
	"go.podman.io/image/v5/pkg/blobinfocache/none"
	"go.podman.io/image/v5/types"
)

// compat implements the obsolete parts of types.Image
// for implementations of genericManifest.
// See addCompat below.
type compat struct {
	manifest genericManifest
}

// addCompat initializes compat to implement the obsolete parts of types.Image
// for implementations of genericManifest.
//
// Use it like this:
//
//		type yourManifest struct {
//
//		}
//
//	 m := &yourManifest{…}
//	 m.compat = image.addCompat(m)
func addCompat(m genericManifest) compat {
	return compat{m}
}

// UpdatedImageNeedsLayerDiffIDs returns true iff UpdatedImage(options) needs InformationOnly.LayerDiffIDs.
// This is a horribly specific interface, but computing InformationOnly.LayerDiffIDs can be very expensive to compute
// (most importantly it forces us to download the full layers even if they are already present at the destination).
func (m *compat) UpdatedImageNeedsLayerDiffIDs(options types.ManifestUpdateOptions) bool {
	return m.manifest.EditedImageNeedsLayerDiffIDs(editedImageEditsFromManifestUpdateOptions(&options))
}

// UpdatedImage returns a types.Image modified according to options.
// Everything in options.InformationOnly should be provided, other fields should be set only if a modification is desired.
// This does not change the state of the original Image object.
// The returned error will be a manifest.ManifestLayerCompressionIncompatibilityError if
// manifests of type options.ManifestMIMEType can not include layers that are compressed
// in accordance with the CompressionOperation and CompressionAlgorithm specified in one
// or more options.LayerInfos items, though retrying with a different
// options.ManifestMIMEType or with different CompressionOperation+CompressionAlgorithm
// values might succeed.
func (m *compat) UpdatedImage(ctx context.Context, options types.ManifestUpdateOptions) (types.Image, error) {
	return m.manifest.EditedImage(ctx,
		editedImageEditsFromManifestUpdateOptions(&options),
		EditedImageInfo{
			Destination: imagedestination.FromPublic(options.InformationOnly.Destination),
			Cache:       none.NoCache,
			Digests:     digests.CanonicalDefault(),

			LayerInfos:   options.InformationOnly.LayerInfos,
			LayerDiffIDs: options.InformationOnly.LayerDiffIDs,
		})
}

// editedImageEditsFromManifestUpdateOptions returns EditedImageEdits corresponding to types.ManifestUpdateOptions.
func editedImageEditsFromManifestUpdateOptions(options *types.ManifestUpdateOptions) EditedImageEdits {
	return EditedImageEdits{
		LayerInfos:              options.LayerInfos,
		EmbeddedDockerReference: options.EmbeddedDockerReference,
		ManifestMIMEType:        options.ManifestMIMEType,
	}
}
