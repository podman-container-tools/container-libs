package copy

import (
	"context"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/directory"
	"go.podman.io/image/v5/internal/imagedestination"
	"go.podman.io/image/v5/internal/private"
)

// putManifestCountingDestination counts the PutManifest calls that reach the underlying destination.
type putManifestCountingDestination struct {
	private.ImageDestination
	putManifestCalls int
}

func (d *putManifestCountingDestination) PutManifest(ctx context.Context, manifest []byte, instanceDigest *digest.Digest) error {
	d.putManifestCalls++
	return d.ImageDestination.PutManifest(ctx, manifest, instanceDigest)
}

func TestCopierPutManifest(t *testing.T) {
	const manifestContents = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	instance := digest.FromString("instance")

	for _, c := range []struct {
		name string
		// omitIfUnchanged is the value of Options.OmitPrimaryManifestUpdateIfUnchanged.
		omitIfUnchanged bool
		// instanceDigest is passed to putManifest; nil writes the primary manifest.
		instanceDigest *digest.Digest
		// existing is what the destination already contains, "" for nothing at all.
		existing string
		// expectWrite is whether the destination is expected to be written to.
		expectWrite bool
	}{
		{name: "option not set", omitIfUnchanged: false, existing: manifestContents, expectWrite: true},
		{name: "unchanged primary manifest", omitIfUnchanged: true, existing: manifestContents, expectWrite: false},
		{name: "changed primary manifest", omitIfUnchanged: true, existing: `{"schemaVersion":2}`, expectWrite: true},
		{name: "nothing at the destination", omitIfUnchanged: true, existing: "", expectWrite: true},
		// Instances are written by digest, so they are never skipped, even if unchanged.
		{name: "unchanged instance", omitIfUnchanged: true, instanceDigest: &instance, existing: manifestContents, expectWrite: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			ref, err := directory.NewReference(t.TempDir())
			require.NoError(t, err)
			publicDest, err := ref.NewImageDestination(ctx, nil)
			require.NoError(t, err)
			defer publicDest.Close()
			dest := &putManifestCountingDestination{ImageDestination: imagedestination.FromPublic(publicDest)}

			if c.existing != "" {
				err := dest.PutManifest(ctx, []byte(c.existing), c.instanceDigest)
				require.NoError(t, err)
				dest.putManifestCalls = 0
			}

			copier := &copier{
				dest:    dest,
				options: &Options{OmitPrimaryManifestUpdateIfUnchanged: c.omitIfUnchanged},
			}
			err = copier.putManifest(ctx, []byte(manifestContents), c.instanceDigest)
			require.NoError(t, err)
			if c.expectWrite {
				assert.Equal(t, 1, dest.putManifestCalls)
			} else {
				assert.Equal(t, 0, dest.putManifestCalls)
			}

			// Either way, the destination ends up containing the manifest.
			src, err := ref.NewImageSource(ctx, nil)
			require.NoError(t, err)
			defer src.Close()
			m, _, err := src.GetManifest(ctx, c.instanceDigest)
			require.NoError(t, err)
			assert.Equal(t, manifestContents, string(m))
		})
	}
}
