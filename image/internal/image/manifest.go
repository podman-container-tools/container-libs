package image

import (
	"context"
	"fmt"

	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"
)

// genericManifest is an interface for parsing, modifying image manifests and related data.
// The public methods are related to types.Image so that embedding a genericManifest implements most of it,
// but there are also public methods that are only visible by packages that can import c/image/internal/image.
type genericManifest interface {
	serialize() ([]byte, error)
	manifestMIMEType() string
	// ConfigInfo returns a complete BlobInfo for the separate config object, or a BlobInfo{Digest:""} if there isn't a separate object.
	// Note that the config object may not exist in the underlying storage in the return value of UpdatedImage! Use ConfigBlob() below.
	ConfigInfo() types.BlobInfo
	// ConfigBlob returns the blob described by ConfigInfo, iff ConfigInfo().Digest != ""; nil otherwise.
	// The result is cached; it is OK to call this however often you need.
	ConfigBlob(context.Context) ([]byte, error)
	// OCIConfig returns the image configuration as per OCI v1 image-spec. Information about
	// layers in the resulting configuration isn't guaranteed to be returned to due how
	// old image manifests work (docker v2s1 especially).
	OCIConfig(context.Context) (*imgspecv1.Image, error)
	// LayerInfos returns a list of BlobInfos of layers referenced by this image, in order (the root layer first, and then successive layered layers).
	// The Digest field is guaranteed to be provided; Size may be -1.
	// WARNING: The list may contain duplicates, and they are semantically relevant.
	LayerInfos() []types.BlobInfo
	// EmbeddedDockerReferenceConflicts whether a Docker reference embedded in the manifest, if any, conflicts with destination ref.
	// It returns false if the manifest does not embed a Docker reference.
	// (This embedding unfortunately happens for Docker schema1, please do not add support for this in any new formats.)
	EmbeddedDockerReferenceConflicts(ref reference.Named) bool
	// Inspect returns various information for (skopeo inspect) parsed from the manifest and configuration.
	Inspect(context.Context) (*types.ImageInspectInfo, error)
	// SupportsEncryption returns if encryption is supported for the manifest type
	//
	// Deprecated: Initially used to determine if a manifest can be copied from a source manifest type since
	// the process of updating a manifest between different manifest types was to update then convert.
	// This resulted in some fields in the update being lost. This has been fixed by: https://github.com/containers/image/pull/836
	SupportsEncryption(ctx context.Context) bool

	// The following methods are not a part of types.Image:
	// ===

	// CanChangeLayerCompression returns true if we can compress/decompress layers with mimeType in the current image
	// (and the code can handle that).
	// NOTE: Even if this returns true, the relevant format might not accept all compression algorithms; the set of accepted
	// algorithms depends not on the current format, but possibly on the target of a conversion (if UpdatedImage converts
	// to a different manifest format).
	CanChangeLayerCompression(mimeType string) bool
	// EditedImageNeedsLayerDiffIDs returns true iff EditedImage(options) needs ManifestUpdateInformation.LayerDiffIDs.
	// This is a horribly specific interface, but computing ManifestUpdateInformation.LayerDiffIDs can be very expensive to compute
	// (most importantly it forces us to download the full layers even if they are already present at the destination).
	EditedImageNeedsLayerDiffIDs(edits EditedImageEdits) bool
	// EditedImage FIXME
	EditedImage(ctx context.Context, edits EditedImageEdits, info types.ManifestUpdateInformation) (types.Image, error)
}

// EditedImageEdits collects requested edits in genericManifest.EditedImage as named optional arguments.
type EditedImageEdits struct {
	LayerInfos              []types.BlobInfo // Complete BlobInfos (size+digest+urls+annotations) which should replace the originals, in order (the root layer first, and then successive layered layers). BlobInfos' MediaType fields are ignored.
	EmbeddedDockerReference reference.Named
	ManifestMIMEType        string
}

// manifestInstanceFromBlob returns a genericManifest implementation for (manblob, mt) in src.
// If manblob is a manifest list, it implicitly chooses an appropriate image from the list.
func manifestInstanceFromBlob(ctx context.Context, sys *types.SystemContext, src types.ImageSource, manblob []byte, mt string) (genericManifest, error) {
	switch manifest.NormalizedMIMEType(mt) {
	case manifest.DockerV2Schema1MediaType, manifest.DockerV2Schema1SignedMediaType:
		return manifestSchema1FromManifest(manblob)
	case imgspecv1.MediaTypeImageManifest:
		return manifestOCI1FromManifest(src, manblob)
	case manifest.DockerV2Schema2MediaType:
		return manifestSchema2FromManifest(src, manblob)
	case manifest.DockerV2ListMediaType:
		return manifestSchema2FromManifestList(ctx, sys, src, manblob)
	case imgspecv1.MediaTypeImageIndex:
		return manifestOCI1FromImageIndex(ctx, sys, src, manblob)
	default: // Note that this may not be reachable, manifest.NormalizedMIMEType has a default for unknown values.
		return nil, fmt.Errorf("Unimplemented manifest MIME type %q", mt)
	}
}

// manifestLayerInfosToBlobInfos extracts a []types.BlobInfo from a []manifest.LayerInfo.
func manifestLayerInfosToBlobInfos(layers []manifest.LayerInfo) []types.BlobInfo {
	blobs := make([]types.BlobInfo, len(layers))
	for i, layer := range layers {
		blobs[i] = layer.BlobInfo
	}
	return blobs
}

// manifestConvertFn (a method of genericManifest object) returns a genericManifest implementation
// converted to a specific manifest MIME type.
// It may use info and also adjust *edits to be appropriate for editing the returned value.
// This does not change the state of the original genericManifest object.
type manifestConvertFn func(ctx context.Context, edits *EditedImageEdits,
	info types.ManifestUpdateInformation) (genericManifest, error)

// convertManifestIfRequiredWithUpdate will run conversion functions of a manifest if
// required and re-apply the edits to the converted type.
// It returns (nil, nil) if no conversion was requested.
func convertManifestIfRequiredWithUpdate(ctx context.Context, edits EditedImageEdits, info types.ManifestUpdateInformation,
	converters map[string]manifestConvertFn,
) (types.Image, error) {
	if edits.ManifestMIMEType == "" {
		return nil, nil
	}

	converter, ok := converters[edits.ManifestMIMEType]
	if !ok {
		return nil, fmt.Errorf("Unsupported conversion type: %v", edits.ManifestMIMEType)
	}

	editsCopy := edits
	convertedManifest, err := converter(ctx, &editsCopy, info)
	if err != nil {
		return nil, err
	}
	convertedImage := memoryImageFromManifest(convertedManifest)

	editsCopy.ManifestMIMEType = ""
	return convertedImage.EditedImage(ctx, editsCopy, info)
}
