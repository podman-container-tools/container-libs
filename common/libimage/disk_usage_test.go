//go:build !remote

package libimage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/config"
	"go.podman.io/storage"
)

func TestDiskUsage(t *testing.T) {
	runtime := testNewRuntime(t)
	ctx := context.Background()

	const expectedTotalImageSize int64 = 5847966
	name := "quay.io/libpod/alpine:3.10.2"
	pullOptions := &PullOptions{}
	pulledImages, err := runtime.Pull(ctx, name, config.PullPolicyAlways, pullOptions)
	require.NoError(t, err)
	require.Len(t, pulledImages, 1)
	imgID := pulledImages[0].storageImage.ID
	layerID := pulledImages[0].storageImage.TopLayer
	digest := pulledImages[0].storageImage.Digest
	img, err := pulledImages[0].storageReference.NewImageSource(ctx, &runtime.systemContext)
	require.NoError(t, err)
	defer img.Close()
	manifest, _, err := img.GetManifest(ctx, nil)
	require.NoError(t, err)

	expectedImageDiskUsage := ImageDiskUsage{
		ID:         imgID,
		Repository: "quay.io/libpod/alpine",
		Tag:        "3.10.2",
		SharedSize: 0,
		UniqueSize: expectedTotalImageSize,
		Size:       expectedTotalImageSize,
	}

	res, size, err := runtime.DiskUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, expectedTotalImageSize, size)
	require.Len(t, res, 1)

	// intentionally unsetting the time here, we cannot really equal the time
	// because of the local information that is part of the struct and that
	// can differ even when the time is the same
	res[0].Created = time.Time{}
	require.Equal(t, expectedImageDiskUsage, res[0])

	opts := &storage.ImageOptions{
		BigData: []storage.ImageBigDataOption{
			{
				Key:    storage.ImageDigestBigDataKey,
				Data:   manifest,
				Digest: digest,
			},
		},
	}

	img2, err := runtime.store.CreateImage("", []string{"localhost/test:123"}, layerID, "", opts)
	require.NoError(t, err)

	const sharedSize int64 = 5843968
	// copy the expected and update the expected values
	expectedImageDiskUsage2 := ImageDiskUsage{
		ID:         img2.ID,
		Repository: "localhost/test",
		Tag:        "123",
		SharedSize: sharedSize,
		UniqueSize: int64(len(manifest)),
		Size:       sharedSize + int64(len(manifest)),
	}
	expectedImageDiskUsage.SharedSize = sharedSize
	expectedImageDiskUsage.UniqueSize = expectedImageDiskUsage.Size - sharedSize

	res, size, err = runtime.DiskUsage(ctx)
	require.NoError(t, err)
	require.Equal(t, expectedTotalImageSize+int64(len(manifest)), size)
	require.Len(t, res, 2)
	res[0].Created = time.Time{}
	res[1].Created = time.Time{}
	require.ElementsMatch(t, []ImageDiskUsage{expectedImageDiskUsage, expectedImageDiskUsage2}, res)
}

func TestCalculateKeepBytes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		totalSize int64
		free      int64
		opts      RemoveImagesOptions
		expected  int64
	}{
		{
			name:      "max used space only",
			totalSize: 100,
			opts:      RemoveImagesOptions{MaxUsedSpace: 60},
			expected:  60,
		},
		{
			name:      "min free space already satisfied",
			totalSize: 100,
			free:      500,
			opts:      RemoveImagesOptions{MinFreeSpace: 200},
			expected:  100,
		},
		{
			name:      "min free space not satisfied",
			totalSize: 100,
			free:      150,
			opts:      RemoveImagesOptions{MinFreeSpace: 200},
			expected:  50,
		},
		{
			name:      "min free space is the tighter limit",
			totalSize: 100,
			free:      150,
			opts:      RemoveImagesOptions{MaxUsedSpace: 80, MinFreeSpace: 200},
			expected:  50,
		},
		{
			name:      "max used space is the tighter limit",
			totalSize: 100,
			free:      150,
			opts:      RemoveImagesOptions{MaxUsedSpace: 30, MinFreeSpace: 200},
			expected:  30,
		},
		{
			name:      "max used space with min free space satisfied",
			totalSize: 100,
			free:      500,
			opts:      RemoveImagesOptions{MaxUsedSpace: 60, MinFreeSpace: 200},
			expected:  60,
		},
		{
			name:      "reserved space only",
			totalSize: 100,
			opts:      RemoveImagesOptions{ReservedSpace: 70},
			expected:  70,
		},
		{
			name:      "reserved space raises the floor",
			totalSize: 100,
			opts:      RemoveImagesOptions{MaxUsedSpace: 30, ReservedSpace: 70},
			expected:  70,
		},
		{
			name:      "excess larger than total size",
			totalSize: 100,
			opts:      RemoveImagesOptions{MinFreeSpace: 300},
			expected:  0,
		},
		{
			name:      "reserved space larger than total size",
			totalSize: 100,
			opts:      RemoveImagesOptions{MaxUsedSpace: 10, ReservedSpace: 500},
			expected:  500,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, calculateKeepBytes(tc.totalSize, tc.free, &tc.opts))
		})
	}
}
