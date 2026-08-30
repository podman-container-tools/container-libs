package toc

import (
	"errors"

	digest "github.com/opencontainers/go-digest"
	"go.podman.io/storage/pkg/chunked/internal/minimal"
)

// ChunkedAnnotations contains various annotations that might be set or used by the pkg/chunked-supported
// compression formats.
//
// This set does not define their semantics in detail as a public API.
// The _only_ intended use of this set is: code that _changes_ layer compression to a format
// which is not chunked can/should remove these annotations.
var ChunkedAnnotations = map[string]struct{}{
	minimal.ManifestChecksumKey: {},
	minimal.ManifestInfoKey:     {},
	minimal.TarSplitInfoKey:     {},
	minimal.TarSplitChecksumKey: {}, //nolint:staticcheck // The field is deprecated, so removing it when changing compressionn is all the more desirable.
	tocJSONDigestAnnotation:     {},
}

// tocJSONDigestAnnotation is the annotation key for the digest of the estargz
// TOC JSON.
// It is defined in github.com/containerd/stargz-snapshotter/estargz as TOCJSONDigestAnnotation
// Duplicate it here to avoid a dependency on the package.
const tocJSONDigestAnnotation = "containerd.io/snapshot/stargz/toc.digest"

// ZstdChunkedSentinelContent is the well-known content of the sentinel layer
// used to signal that a manifest's zstd:chunked layers can be used without
// the full-digest mitigation.
//
// It is a 512-byte block (tar header size) that is definitively not valid tar:
//   - Byte 0 is \x00 (marks as binary/non-text)
//   - Bytes 148-155 (checksum field) are all \xff (invalid for any tar variant)
//   - Bytes 257-262 (magic field) are "NOTTAR" instead of "ustar\0"
//
// This ensures that no tar implementation can accidentally parse this as a
// valid archive entry.
var ZstdChunkedSentinelContent = func() []byte {
	var block [512]byte
	// Name field (bytes 0-99): binary zero followed by identifier
	copy(block[1:], "ZSTD-CHUNKED-SENTINEL")
	// Checksum field (bytes 148-155): all 0xff — invalid for v7 and ustar
	for i := 148; i < 156; i++ {
		block[i] = 0xff
	}
	// Magic field (bytes 257-262): "NOTTAR" — not "ustar\0"
	copy(block[257:], "NOTTAR")
	// Version + message area (bytes 263-511):
	msg := "This image must be consumed using the TOC digests " +
		"and NEVER as a tar archive.\n" +
		"See: https://github.com/containers/image"
	copy(block[263:], msg)
	return block[:]
}()

// ZstdChunkedSentinelDigest is the digest of ZstdChunkedSentinelContent.
var ZstdChunkedSentinelDigest = digest.FromBytes(ZstdChunkedSentinelContent)

// ZstdChunkedSentinelMediaType is the MIME type used for the sentinel layer descriptor.
// This allows detection without committing to a specific digest, so the sentinel
// content (e.g., embedded URL) can change over time.
const ZstdChunkedSentinelMediaType = "application/vnd.containers.zstd-chunked-sentinel"

// GetTOCDigest returns the digest of the TOC as recorded in the annotations.
// This function retrieves a digest that represents the content of a
// table of contents (TOC) from the image's annotations.
// This is an experimental feature and may be changed/removed in the future.
func GetTOCDigest(annotations map[string]string) (*digest.Digest, error) {
	d1, ok1 := annotations[tocJSONDigestAnnotation]
	d2, ok2 := annotations[minimal.ManifestChecksumKey]
	switch {
	case ok1 && ok2:
		return nil, errors.New("both zstd:chunked and eStargz TOC found")
	case ok1:
		d, err := digest.Parse(d1)
		if err != nil {
			return nil, err
		}
		return &d, nil
	case ok2:
		d, err := digest.Parse(d2)
		if err != nil {
			return nil, err
		}
		return &d, nil
	default:
		return nil, nil
	}
}
