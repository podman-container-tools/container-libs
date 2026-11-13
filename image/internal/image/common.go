package image

import (
	"context"

	"go.podman.io/image/v5/types"
)

// commonImage shares code implementing some parts of types.Image
// for implementations of genericManifest.
type commonImage struct {
	genericManifest // Embedded anonymously, so that an anonymous embedding of commonImage exposes the genericManifest methods as well.
}

// UpdatedImageNeedsLayerDiffIDs returns true iff UpdatedImage(options) needs InformationOnly.LayerDiffIDs.
// This is a horribly specific interface, but computing InformationOnly.LayerDiffIDs can be very expensive to compute
// (most importantly it forces us to download the full layers even if they are already present at the destination).
func (i *commonImage) UpdatedImageNeedsLayerDiffIDs(options types.ManifestUpdateOptions) bool {
	return i.genericManifest.EditedImageNeedsLayerDiffIDs(editedImageEditsFromManifestUpdateOptions(&options))
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
func (i *commonImage) UpdatedImage(ctx context.Context, options types.ManifestUpdateOptions) (types.Image, error) {
	return i.genericManifest.editedImage(ctx,
		editedImageEditsFromManifestUpdateOptions(&options),
		options.InformationOnly)
}

// editedImageEditsFromManifestUpdateOptions returns EditedImageEdits corresponding to types.ManifestUpdateOptions.
func editedImageEditsFromManifestUpdateOptions(options *types.ManifestUpdateOptions) EditedImageEdits {
	return EditedImageEdits{
		LayerInfos:              options.LayerInfos,
		EmbeddedDockerReference: options.EmbeddedDockerReference,
		ManifestMIMEType:        options.ManifestMIMEType,
	}
}
