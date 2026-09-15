//go:build !remote

package libimage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/config"
	"go.podman.io/storage"
)

// FreedSize must report the space a removal actually releases.  Size counts
// every layer of an image including the shared ones, so summing it over a
// removal claims more than the images ever occupied.
// See containers/podman#27592.
func TestRemoveImagesReportedSize(t *testing.T) {
	runtime := testNewRuntime(t)
	ctx := context.Background()

	pulledImages, err := runtime.Pull(ctx, "quay.io/libpod/alpine:3.10.2", config.PullPolicyAlways, &PullOptions{})
	require.NoError(t, err)
	require.Len(t, pulledImages, 1)

	// Create a second image on top of the *same* layer, so both images
	// share all of their layer data.
	src, err := pulledImages[0].storageReference.NewImageSource(ctx, &runtime.systemContext)
	require.NoError(t, err)
	defer src.Close()
	manifest, _, err := src.GetManifest(ctx, nil)
	require.NoError(t, err)

	_, err = runtime.store.CreateImage("", []string{"localhost/shared:latest"},
		pulledImages[0].storageImage.TopLayer, "", &storage.ImageOptions{
			BigData: []storage.ImageBigDataOption{{
				Key:    storage.ImageDigestBigDataKey,
				Data:   manifest,
				Digest: pulledImages[0].storageImage.Digest,
			}},
		})
	require.NoError(t, err)

	_, totalSize, err := runtime.DiskUsage(ctx)
	require.NoError(t, err)
	require.Positive(t, totalSize)

	rmReports, rmErrors := runtime.RemoveImages(ctx, nil, &RemoveImagesOptions{WithSize: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 2)

	var freed, size int64
	for _, report := range rmReports {
		require.True(t, report.Removed)
		require.GreaterOrEqual(t, report.FreedSize, int64(0))
		freed += report.FreedSize
		size += report.Size
	}

	// Both images are gone, so exactly the space they occupied is released;
	// the layer they share must not be counted twice.
	require.Equal(t, totalSize, freed)

	// Size still reports the full size of each image, so it double counts
	// the shared layer.  That is what FreedSize exists to avoid.
	require.Greater(t, size, freed)
}
