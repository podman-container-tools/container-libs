package copy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/internal/image"
	internalManifest "go.podman.io/image/v5/internal/manifest"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/internal/set"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/pkg/compression"
	"go.podman.io/image/v5/types"
	chunkedToc "go.podman.io/storage/pkg/chunked/toc"
)

type instanceOpKind int

const (
	instanceOpCopy instanceOpKind = iota
	instanceOpClone
	instanceOpDelete
)

// copiedInstanceData stores info about a successfully copied instance,
// used for creating sentinel variants.
type copiedInstanceData struct {
	sourceDigest digest.Digest
	result       copySingleImageResult
	platform     *imgspecv1.Platform
}

type instanceOp struct {
	op           instanceOpKind
	sourceDigest digest.Digest

	// Fields which can be used by callers when operation
	// is `instanceOpCopy`
	copyForceCompressionFormat bool

	// Fields which can be used by callers when operation
	// is `instanceOpClone`
	cloneArtifactType       string
	cloneCompressionVariant OptionCompressionVariant
	clonePlatform           *imgspecv1.Platform
	cloneAnnotations        map[string]string

	// Fields which can be used by callers when operation
	// is `instanceOpDelete`
	deleteIndex int
}

// internal type only to make imgspecv1.Platform comparable
type platformComparable struct {
	architecture string
	os           string
	osVersion    string
	osFeatures   string
	variant      string
}

// Converts imgspecv1.Platform to a comparable format.
func platformV1ToPlatformComparable(platform *imgspecv1.Platform) platformComparable {
	if platform == nil {
		return platformComparable{}
	}
	osFeatures := slices.Clone(platform.OSFeatures)
	sort.Strings(osFeatures)
	return platformComparable{
		architecture: platform.Architecture,
		os:           platform.OS,
		// This is strictly speaking ambiguous, fields of OSFeatures can contain a ','. Probably good enough for now.
		osFeatures: strings.Join(osFeatures, ","),
		osVersion:  platform.OSVersion,
		variant:    platform.Variant,
	}
}

// platformCompressionMap prepares a mapping of platformComparable -> CompressionAlgorithmNames for given digests
func platformCompressionMap(list internalManifest.List, instanceDigests []digest.Digest) (map[platformComparable]*set.Set[string], error) {
	res := make(map[platformComparable]*set.Set[string])
	for _, instanceDigest := range instanceDigests {
		instanceDetails, err := list.Instance(instanceDigest)
		if err != nil {
			return nil, fmt.Errorf("getting details for instance %s: %w", instanceDigest, err)
		}
		platform := platformV1ToPlatformComparable(instanceDetails.ReadOnly.Platform)
		platformSet, ok := res[platform]
		if !ok {
			platformSet = set.New[string]()
			res[platform] = platformSet
		}
		platformSet.AddSeq(slices.Values(instanceDetails.ReadOnly.CompressionAlgorithmNames))
	}
	return res, nil
}

func validateCompressionVariantExists(input []OptionCompressionVariant) error {
	for _, option := range input {
		_, err := compression.AlgorithmByName(option.Algorithm.Name())
		if err != nil {
			return fmt.Errorf("invalid algorithm %q in option.EnsureCompressionVariantsExist: %w", option.Algorithm.Name(), err)
		}
	}
	return nil
}

// prepareInstanceOps prepares a list of operations to perform on instances (copy, clone, or delete).
// It returns a unified list of all operations and the count of copy/clone operations (excluding deletes).
func prepareInstanceOps(list internalManifest.List, instanceDigests []digest.Digest, options *Options, cannotModifyManifestListReason string) ([]instanceOp, int, error) {
	res := []instanceOp{}
	deleteOps := []instanceOp{}
	if options.ImageListSelection == CopySpecificImages && len(options.EnsureCompressionVariantsExist) > 0 {
		// List can already contain compressed instance for a compression selected in `EnsureCompressionVariantsExist`
		// It's unclear what it means when `CopySpecificImages` includes an instance in options.Instances,
		// EnsureCompressionVariantsExist asks for an instance with some compression,
		// an instance with that compression already exists, but is not included in options.Instances.
		// We might define the semantics and implement this in the future.
		return res, -1, fmt.Errorf("EnsureCompressionVariantsExist is not implemented for CopySpecificImages")
	}
	err := validateCompressionVariantExists(options.EnsureCompressionVariantsExist)
	if err != nil {
		return res, -1, err
	}
	compressionsByPlatform, err := platformCompressionMap(list, instanceDigests)
	if err != nil {
		return nil, -1, err
	}

	// Determine which specific images to copy (combining digest-based and platform-based selection)
	var specificImages *set.Set[digest.Digest]
	if options.ImageListSelection == CopySpecificImages {
		specificImages, err = determineSpecificImages(options, list)
		if err != nil {
			return nil, -1, err
		}
	}

	for i, instanceDigest := range instanceDigests {
		if options.ImageListSelection == CopySpecificImages &&
			!specificImages.Contains(instanceDigest) {
			if options.SparseManifestListAction == StripSparseManifestList {
				if cannotModifyManifestListReason != "" {
					return nil, -1, fmt.Errorf("we should delete instance %s from manifest list, but we cannot: %s", instanceDigest, cannotModifyManifestListReason)
				}
				logrus.Debugf("deleting instance %s from destination’s manifest (%d/%d)", instanceDigest, i+1, len(instanceDigests))
				deleteOps = append(deleteOps, instanceOp{
					op:          instanceOpDelete,
					deleteIndex: i,
				})
			} else {
				logrus.Debugf("skipping instance %s (%d/%d)", instanceDigest, i+1, len(instanceDigests))
			}
			continue
		}
		instanceDetails, err := list.Instance(instanceDigest)
		if err != nil {
			return res, -1, fmt.Errorf("getting details for instance %s: %w", instanceDigest, err)
		}
		forceCompressionFormat, err := shouldRequireCompressionFormatMatch(options)
		if err != nil {
			return nil, -1, err
		}
		res = append(res, instanceOp{
			op:                         instanceOpCopy,
			sourceDigest:               instanceDigest,
			copyForceCompressionFormat: forceCompressionFormat,
		})
		platform := platformV1ToPlatformComparable(instanceDetails.ReadOnly.Platform)
		compressionList := compressionsByPlatform[platform]
		for _, compressionVariant := range options.EnsureCompressionVariantsExist {
			if !compressionList.Contains(compressionVariant.Algorithm.Name()) {
				res = append(res, instanceOp{
					op:                      instanceOpClone,
					sourceDigest:            instanceDigest,
					cloneArtifactType:       instanceDetails.ReadOnly.ArtifactType,
					cloneCompressionVariant: compressionVariant,
					clonePlatform:           instanceDetails.ReadOnly.Platform,
					cloneAnnotations:        maps.Clone(instanceDetails.ReadOnly.Annotations),
				})
				compressionList.Add(compressionVariant.Algorithm.Name())
			}
		}
	}

	// Add delete operations in reverse order (highest to lowest index) to avoid shifting
	slices.Reverse(deleteOps)
	copyLen := len(res) // Count copy/clone operations before appending deletes

	if len(instanceDigests) > 0 && copyLen == 0 && len(deleteOps) == len(instanceDigests) {
		return nil, -1, fmt.Errorf("requested operation filtered out all platforms and would create an empty image")
	}

	res = append(res, deleteOps...)

	return res, copyLen, nil
}

// determineSpecificImages returns a set of images to copy based on the
// Instances and InstancePlatforms fields of the passed-in options structure
func determineSpecificImages(options *Options, updatedList internalManifest.List) (*set.Set[digest.Digest], error) {
	specificImages := set.NewWithValues(options.Instances...)

	if len(options.InstancePlatforms) > 0 {
		// Find ALL instances matching each platform specification (OS and Architecture)
		for _, filter := range options.InstancePlatforms {
			matched := false
			instanceDigests := updatedList.Instances()
			for _, instanceDigest := range instanceDigests {
				instanceDetails, err := updatedList.Instance(instanceDigest)
				if err != nil {
					return nil, fmt.Errorf("getting details for instance %s: %w", instanceDigest, err)
				}

				// Match the platform. We match nil platforms against empty filter values.
				instanceOS := ""
				instanceArch := ""
				if instanceDetails.ReadOnly.Platform != nil {
					instanceOS = instanceDetails.ReadOnly.Platform.OS
					instanceArch = instanceDetails.ReadOnly.Platform.Architecture
				}

				if instanceOS == filter.OS && instanceArch == filter.Architecture {
					specificImages.Add(instanceDigest)
					matched = true
				}
			}

			if !matched {
				return nil, fmt.Errorf("no instances found for platform %s/%s", filter.OS, filter.Architecture)
			}
		}
	}

	return specificImages, nil
}

// copyMultipleImages copies some or all of an image list's instances, using
// c.policyContext to validate source image admissibility.
func (c *copier) copyMultipleImages(ctx context.Context) (copiedManifest []byte, retErr error) {
	// Parse the list and get a copy of the original value after it's re-encoded.
	manifestList, manifestType, err := c.unparsedToplevel.Manifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading manifest list: %w", err)
	}
	originalList, err := internalManifest.ListFromBlob(manifestList, manifestType)
	if err != nil {
		return nil, fmt.Errorf("parsing manifest list %q: %w", string(manifestList), err)
	}
	updatedList := originalList.CloneInternal()

	sigs, err := c.sourceSignatures(ctx, c.unparsedToplevel,
		"Getting image list signatures",
		"Checking if image list destination supports signatures", true)
	if err != nil {
		return nil, err
	}

	// If the destination is a digested reference, make a note of that, determine what digest value we're
	// expecting, and check that the source manifest matches it.
	destIsDigestedReference := false
	if named := c.dest.Reference().DockerReference(); named != nil {
		if digested, ok := named.(reference.Digested); ok {
			destIsDigestedReference = true
			matches, err := manifest.MatchesDigest(manifestList, digested.Digest())
			if err != nil {
				return nil, fmt.Errorf("computing digest of source image's manifest: %w", err)
			}
			if !matches {
				return nil, errors.New("Digest of source image's manifest would not match destination reference")
			}
		}
	}

	// Determine if we're allowed to modify the manifest list.
	// If we can, set to the empty string. If we can't, set to the reason why.
	// Compare, and perhaps keep in sync with, the version in copySingleImage.
	cannotModifyManifestListReason := ""
	if len(sigs) > 0 {
		cannotModifyManifestListReason = "Would invalidate signatures; consider removing them from the multi-platform list"
	}
	if destIsDigestedReference {
		cannotModifyManifestListReason = "Destination specifies a digest"
	}
	if c.options.PreserveDigests {
		cannotModifyManifestListReason = "Instructed to preserve digests"
	}

	// Determine if we'll need to convert the manifest list to a different format.
	forceListMIMEType := c.options.ForceManifestMIMEType
	switch forceListMIMEType {
	case manifest.DockerV2Schema1MediaType, manifest.DockerV2Schema1SignedMediaType, manifest.DockerV2Schema2MediaType:
		forceListMIMEType = manifest.DockerV2ListMediaType
	case imgspecv1.MediaTypeImageManifest:
		forceListMIMEType = imgspecv1.MediaTypeImageIndex
	}
	// FIXME: This does not take into account cannotModifyManifestListReason.
	selectedListType, otherManifestMIMETypeCandidates, err := c.determineListConversion(manifestType, c.dest.SupportedManifestMIMETypes(), forceListMIMEType)
	if err != nil {
		return nil, fmt.Errorf("determining manifest list type to write to destination: %w", err)
	}
	if selectedListType != originalList.MIMEType() {
		if cannotModifyManifestListReason != "" {
			return nil, fmt.Errorf("Manifest list must be converted to type %q to be written to destination, but we cannot modify it: %s", selectedListType, cannotModifyManifestListReason)
		}
	}

	// Copy each image, or just the ones we want to copy, in turn.
	instanceDigests := updatedList.Instances()
	instanceEdits := []internalManifest.ListEdit{}
	instanceOpList, copyLen, err := prepareInstanceOps(updatedList, instanceDigests, c.options, cannotModifyManifestListReason)
	if err != nil {
		return nil, fmt.Errorf("preparing instances for copy: %w", err)
	}
	var copiedInstances []copiedInstanceData

	c.Printf("Copying %d images generated from %d images in list\n", copyLen, len(instanceDigests))
	copyCount := 0 // Track copy/clone operations separately from delete operations
	for i, instance := range instanceOpList {
		// Update instances to be edited by their `ListOperation` and
		// populate necessary fields.
		switch instance.op {
		case instanceOpCopy:
			copyCount++
			logrus.Debugf("Copying instance %s (%d/%d)", instance.sourceDigest, copyCount, copyLen)
			c.Printf("Copying image %s (%d/%d)\n", instance.sourceDigest, copyCount, copyLen)
			unparsedInstance := image.UnparsedInstance(c.rawSource, &instanceOpList[i].sourceDigest)
			updated, err := c.copySingleImage(ctx, unparsedInstance, &instanceOpList[i].sourceDigest, copySingleImageOptions{requireCompressionFormatMatch: instance.copyForceCompressionFormat})
			if err != nil {
				return nil, fmt.Errorf("copying image %d/%d from manifest list: %w", copyCount, copyLen, err)
			}
			// Record the result of a possible conversion here.
			instanceEdits = append(instanceEdits, internalManifest.ListEdit{
				ListOperation:               internalManifest.ListOpUpdate,
				UpdateOldDigest:             instance.sourceDigest,
				UpdateDigest:                updated.manifestDigest,
				UpdateSize:                  int64(len(updated.manifest)),
				UpdateCompressionAlgorithms: updated.compressionAlgorithms,
				UpdateMediaType:             updated.manifestMIMEType,
			})
			// Capture instance data for sentinel variant creation
			instanceDetails, detailsErr := updatedList.Instance(instance.sourceDigest)
			if detailsErr == nil {
				copiedInstances = append(copiedInstances, copiedInstanceData{
					sourceDigest: instance.sourceDigest,
					result:       updated,
					platform:     instanceDetails.ReadOnly.Platform,
				})
			}
		case instanceOpClone:
			copyCount++
			logrus.Debugf("Replicating instance %s (%d/%d)", instance.sourceDigest, copyCount, copyLen)
			c.Printf("Replicating image %s (%d/%d)\n", instance.sourceDigest, copyCount, copyLen)
			unparsedInstance := image.UnparsedInstance(c.rawSource, &instanceOpList[i].sourceDigest)
			updated, err := c.copySingleImage(ctx, unparsedInstance, &instanceOpList[i].sourceDigest, copySingleImageOptions{
				requireCompressionFormatMatch: true,
				compressionFormat:             &instance.cloneCompressionVariant.Algorithm,
				compressionLevel:              instance.cloneCompressionVariant.Level,
			})
			if err != nil {
				return nil, fmt.Errorf("replicating image %d/%d from manifest list: %w", copyCount, copyLen, err)
			}
			// Record the result of a possible conversion here.
			instanceEdits = append(instanceEdits, internalManifest.ListEdit{
				ListOperation:            internalManifest.ListOpAdd,
				AddDigest:                updated.manifestDigest,
				AddSize:                  int64(len(updated.manifest)),
				AddMediaType:             updated.manifestMIMEType,
				AddArtifactType:          instance.cloneArtifactType,
				AddPlatform:              instance.clonePlatform,
				AddAnnotations:           instance.cloneAnnotations,
				AddCompressionAlgorithms: updated.compressionAlgorithms,
			})
		case instanceOpDelete:
			instanceEdits = append(instanceEdits, internalManifest.ListEdit{
				ListOperation: internalManifest.ListOpDelete,
				DeleteIndex:   instance.deleteIndex,
			})
		default:
			return nil, fmt.Errorf("copying image: invalid copy operation %d", instance.op)
		}
	}

	// Create zstd:chunked sentinel variants for instances where any layer uses zstd:chunked.
	if cannotModifyManifestListReason == "" {
		sentinelEdits, err := c.createZstdChunkedSentinelVariants(ctx, copiedInstances, updatedList)
		if err != nil {
			return nil, fmt.Errorf("creating zstd:chunked sentinel variants: %w", err)
		}
		instanceEdits = append(instanceEdits, sentinelEdits...)
	}

	// Now reset the digest/size/types of the manifests in the list and remove deleted instances.
	if err = updatedList.EditInstances(instanceEdits, cannotModifyManifestListReason != ""); err != nil {
		return nil, fmt.Errorf("updating manifest list: %w", err)
	}

	// Iterate through supported list types, preferred format first.
	c.Printf("Writing manifest list to image destination\n")
	var errs []string
	for _, thisListType := range append([]string{selectedListType}, otherManifestMIMETypeCandidates...) {
		var attemptedList internalManifest.ListPublic = updatedList

		logrus.Debugf("Trying to use manifest list type %s…", thisListType)

		// Perform the list conversion, if we need one.
		if thisListType != updatedList.MIMEType() {
			attemptedList, err = updatedList.ConvertToMIMEType(thisListType)
			if err != nil {
				return nil, fmt.Errorf("converting manifest list to list with MIME type %q: %w", thisListType, err)
			}
		}

		// Check if the updates or a type conversion meaningfully changed the list of images
		// by serializing them both so that we can compare them.
		attemptedManifestList, err := attemptedList.Serialize()
		if err != nil {
			return nil, fmt.Errorf("encoding updated manifest list (%q: %#v): %w", updatedList.MIMEType(), updatedList.Instances(), err)
		}
		originalManifestList, err := originalList.Serialize()
		if err != nil {
			return nil, fmt.Errorf("encoding original manifest list for comparison (%q: %#v): %w", originalList.MIMEType(), originalList.Instances(), err)
		}

		// If we can't just use the original value, but we have to change it, flag an error.
		if !bytes.Equal(attemptedManifestList, originalManifestList) {
			if cannotModifyManifestListReason != "" {
				return nil, fmt.Errorf("Manifest list was edited, but we cannot modify it: %q", cannotModifyManifestListReason)
			}
			logrus.Debugf("Manifest list has been updated")
		} else {
			// We can just use the original value, so use it instead of the one we just rebuilt, so that we don't change the digest.
			attemptedManifestList = manifestList
		}

		// Save the manifest list.
		err = c.dest.PutManifest(ctx, attemptedManifestList, nil)
		if err != nil {
			logrus.Debugf("Upload of manifest list type %s failed: %v", thisListType, err)
			errs = append(errs, fmt.Sprintf("%s(%v)", thisListType, err))
			continue
		}
		errs = nil
		manifestList = attemptedManifestList
		break
	}
	if errs != nil {
		return nil, fmt.Errorf("Uploading manifest list failed, attempted the following formats: %s", strings.Join(errs, ", "))
	}

	// Sign the manifest list.
	newSigs, err := c.createSignatures(ctx, manifestList, c.options.SignIdentity)
	if err != nil {
		return nil, err
	}
	sigs = append(slices.Clone(sigs), newSigs...)

	c.Printf("Storing list signatures\n")
	if err := c.dest.PutSignaturesWithFormat(ctx, sigs, nil); err != nil {
		return nil, fmt.Errorf("writing signatures: %w", err)
	}

	return manifestList, nil
}

// hasZstdChunkedLayers returns true if any non-empty layer in the manifest has
// zstd:chunked TOC annotations.
func hasZstdChunkedLayers(ociMan *manifest.OCI1) bool {
	for _, l := range ociMan.LayerInfos() {
		if l.EmptyLayer {
			continue
		}
		d, err := chunkedToc.GetTOCDigest(l.Annotations)
		if err == nil && d != nil {
			return true
		}
	}
	return false
}

// pushSentinelVariant creates and pushes a sentinel variant of the given OCI manifest.
// It prepends a sentinel layer and DiffID, creates a new config, and pushes everything
// to the destination. Returns the serialized sentinel manifest and its digest.
func (c *copier) pushSentinelVariant(ctx context.Context, ociMan *manifest.OCI1, ociConfig *imgspecv1.Image) ([]byte, digest.Digest, error) {
	sentinelContent := chunkedToc.ZstdChunkedSentinelContent

	// Push sentinel blob (content-addressed, so idempotent if already pushed).
	sentinelBlobInfo := types.BlobInfo{
		Digest: chunkedToc.ZstdChunkedSentinelDigest,
		Size:   int64(len(sentinelContent)),
	}
	reused, _, err := c.dest.TryReusingBlobWithOptions(ctx, sentinelBlobInfo,
		private.TryReusingBlobOptions{Cache: c.blobInfoCache})
	if err != nil {
		return nil, "", fmt.Errorf("checking sentinel blob: %w", err)
	}
	if !reused {
		_, err = c.dest.PutBlobWithOptions(ctx, bytes.NewReader(sentinelContent),
			sentinelBlobInfo, private.PutBlobOptions{Cache: c.blobInfoCache})
		if err != nil {
			return nil, "", fmt.Errorf("pushing sentinel blob: %w", err)
		}
	}

	// Create new config with sentinel DiffID prepended.
	newDiffIDs := make([]digest.Digest, 0, len(ociConfig.RootFS.DiffIDs)+1)
	newDiffIDs = append(newDiffIDs, chunkedToc.ZstdChunkedSentinelDigest)
	newDiffIDs = append(newDiffIDs, ociConfig.RootFS.DiffIDs...)
	ociConfig.RootFS.DiffIDs = newDiffIDs
	configBlob, err := json.Marshal(ociConfig)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling sentinel config: %w", err)
	}
	configDigest := digest.FromBytes(configBlob)

	// Push new config.
	configBlobInfo := types.BlobInfo{
		Digest:    configDigest,
		Size:      int64(len(configBlob)),
		MediaType: imgspecv1.MediaTypeImageConfig,
	}
	reused, _, err = c.dest.TryReusingBlobWithOptions(ctx, configBlobInfo,
		private.TryReusingBlobOptions{Cache: c.blobInfoCache})
	if err != nil {
		return nil, "", fmt.Errorf("checking sentinel config: %w", err)
	}
	if !reused {
		_, err = c.dest.PutBlobWithOptions(ctx, bytes.NewReader(configBlob),
			configBlobInfo, private.PutBlobOptions{Cache: c.blobInfoCache, IsConfig: true})
		if err != nil {
			return nil, "", fmt.Errorf("pushing sentinel config: %w", err)
		}
	}

	// Build sentinel manifest: sentinel layer + original layers.
	newLayers := make([]imgspecv1.Descriptor, 0, len(ociMan.Layers)+1)
	newLayers = append(newLayers, imgspecv1.Descriptor{
		MediaType: chunkedToc.ZstdChunkedSentinelMediaType,
		Digest:    chunkedToc.ZstdChunkedSentinelDigest,
		Size:      int64(len(sentinelContent)),
	})
	newLayers = append(newLayers, ociMan.Layers...)

	sentinelOCI := manifest.OCI1FromComponents(imgspecv1.Descriptor{
		MediaType: imgspecv1.MediaTypeImageConfig,
		Digest:    configDigest,
		Size:      int64(len(configBlob)),
	}, newLayers)
	sentinelManifestBlob, err := sentinelOCI.Serialize()
	if err != nil {
		return nil, "", fmt.Errorf("serializing sentinel manifest: %w", err)
	}
	sentinelManifestDigest := digest.FromBytes(sentinelManifestBlob)

	// Push sentinel manifest.
	if err := c.dest.PutManifest(ctx, sentinelManifestBlob, &sentinelManifestDigest); err != nil {
		return nil, "", fmt.Errorf("pushing sentinel manifest: %w", err)
	}

	return sentinelManifestBlob, sentinelManifestDigest, nil
}

// createZstdChunkedSentinelVariants creates sentinel variants for instances
// where any layer uses zstd:chunked. The sentinel variant has a non-tar sentinel
// layer prepended, signaling aware clients to skip the full-digest mitigation.
// It returns ListEdit entries to add the sentinel variants to the manifest list.
func (c *copier) createZstdChunkedSentinelVariants(ctx context.Context, copiedInstances []copiedInstanceData, updatedList internalManifest.List) ([]internalManifest.ListEdit, error) {
	var edits []internalManifest.ListEdit

	// Check which platforms already have a sentinel variant in the source.
	platformsWithSentinel := set.New[platformComparable]()
	for _, d := range updatedList.Instances() {
		details, err := updatedList.Instance(d)
		if err != nil {
			continue
		}
		if details.ReadOnly.Annotations[internalManifest.OCI1InstanceAnnotationZstdChunkedSentinel] == internalManifest.OCI1InstanceAnnotationZstdChunkedSentinelValue {
			platformsWithSentinel.Add(platformV1ToPlatformComparable(details.ReadOnly.Platform))
		}
	}

	for _, ci := range copiedInstances {
		// Only handle OCI manifests (zstd:chunked is OCI-only).
		if ci.result.manifestMIMEType != imgspecv1.MediaTypeImageManifest {
			continue
		}

		// Skip if this platform already has a sentinel variant.
		if platformsWithSentinel.Contains(platformV1ToPlatformComparable(ci.platform)) {
			continue
		}

		ociMan, err := manifest.OCI1FromManifest(ci.result.manifest)
		if err != nil {
			logrus.Debugf("Cannot parse manifest for sentinel variant: %v", err)
			continue
		}

		if !hasZstdChunkedLayers(ociMan) {
			continue
		}

		// Use the config as written to the destination (reflects any edits during copy).
		var ociConfig imgspecv1.Image
		if err := json.Unmarshal(ci.result.configBlob, &ociConfig); err != nil {
			logrus.Debugf("Cannot parse config for sentinel variant: %v", err)
			continue
		}

		sentinelManifestBlob, sentinelManifestDigest, err := c.pushSentinelVariant(ctx, ociMan, &ociConfig)
		if err != nil {
			return nil, err
		}

		edits = append(edits, internalManifest.ListEdit{
			ListOperation: internalManifest.ListOpAdd,
			AddDigest:     sentinelManifestDigest,
			AddSize:       int64(len(sentinelManifestBlob)),
			AddMediaType:  imgspecv1.MediaTypeImageManifest,
			AddPlatform:   ci.platform,
			AddAnnotations: map[string]string{
				internalManifest.OCI1InstanceAnnotationZstdChunkedSentinel: internalManifest.OCI1InstanceAnnotationZstdChunkedSentinelValue,
				internalManifest.OCI1InstanceAnnotationCompressionZSTD:     internalManifest.OCI1InstanceAnnotationCompressionZSTDValue,
			},
			AddCompressionAlgorithms: ci.result.compressionAlgorithms,
		})

		platformsWithSentinel.Add(platformV1ToPlatformComparable(ci.platform))
	}

	return edits, nil
}

// createSingleImageSentinelIndex checks if a single copied image has zstd:chunked
// layers, and if so, creates a sentinel variant and wraps both in an OCI index.
// Returns the serialized index manifest, or nil if no sentinel was needed.
func (c *copier) createSingleImageSentinelIndex(ctx context.Context, single copySingleImageResult) ([]byte, error) {
	if single.manifestMIMEType != imgspecv1.MediaTypeImageManifest {
		return nil, nil
	}

	ociMan, err := manifest.OCI1FromManifest(single.manifest)
	if err != nil {
		return nil, nil
	}

	if !hasZstdChunkedLayers(ociMan) {
		return nil, nil
	}

	// Use the config as written to the destination (reflects any edits during copy).
	var ociConfig imgspecv1.Image
	if err := json.Unmarshal(single.configBlob, &ociConfig); err != nil {
		logrus.Debugf("Cannot parse config for single-image sentinel: %v", err)
		return nil, nil
	}

	sentinelManifestBlob, sentinelManifestDigest, err := c.pushSentinelVariant(ctx, ociMan, &ociConfig)
	if err != nil {
		return nil, err
	}

	// Extract platform from config.
	var platform *imgspecv1.Platform
	if ociConfig.OS != "" || ociConfig.Architecture != "" {
		platform = &imgspecv1.Platform{
			OS:           ociConfig.OS,
			Architecture: ociConfig.Architecture,
			Variant:      ociConfig.Variant,
			OSVersion:    ociConfig.OSVersion,
		}
	}

	// Build OCI index with: original manifest first, sentinel variant last.
	// Both entries get the zstd annotation so that old clients (which prefer
	// zstd over gzip) fall through to position-based selection and pick the
	// original at position 0 instead of the sentinel.
	index := manifest.OCI1IndexFromComponents([]imgspecv1.Descriptor{
		{
			MediaType: imgspecv1.MediaTypeImageManifest,
			Digest:    single.manifestDigest,
			Size:      int64(len(single.manifest)),
			Platform:  platform,
			Annotations: map[string]string{
				internalManifest.OCI1InstanceAnnotationCompressionZSTD: internalManifest.OCI1InstanceAnnotationCompressionZSTDValue,
			},
		},
		{
			MediaType: imgspecv1.MediaTypeImageManifest,
			Digest:    sentinelManifestDigest,
			Size:      int64(len(sentinelManifestBlob)),
			Platform:  platform,
			Annotations: map[string]string{
				internalManifest.OCI1InstanceAnnotationZstdChunkedSentinel: internalManifest.OCI1InstanceAnnotationZstdChunkedSentinelValue,
				internalManifest.OCI1InstanceAnnotationCompressionZSTD:     internalManifest.OCI1InstanceAnnotationCompressionZSTDValue,
			},
		},
	}, nil)
	indexBlob, err := index.Serialize()
	if err != nil {
		return nil, fmt.Errorf("serializing sentinel index: %w", err)
	}

	if err := c.dest.PutManifest(ctx, indexBlob, nil); err != nil {
		return nil, fmt.Errorf("pushing sentinel index: %w", err)
	}

	return indexBlob, nil
}
