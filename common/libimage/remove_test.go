//go:build !remote

package libimage

import (
	"cmp"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/config"
)

// testStorageState returns a snapshot of the local storage, mapping image IDs
// to their names, along with the IDs of all containers.
func testStorageState(t *testing.T, ctx context.Context, runtime *Runtime) (map[string][]string, []string) {
	images, err := runtime.ListImages(ctx, nil)
	require.NoError(t, err)
	imageState := make(map[string][]string, len(images))
	for _, image := range images {
		names := slices.Clone(image.Names())
		slices.Sort(names)
		imageState[image.ID()] = names
	}

	containers, err := runtime.store.Containers()
	require.NoError(t, err)
	containerIDs := make([]string, 0, len(containers))
	for _, container := range containers {
		containerIDs = append(containerIDs, container.ID)
	}
	slices.Sort(containerIDs)

	return imageState, containerIDs
}

// testRemoveImagesDryRun runs RemoveImages with DryRun set and makes sure
// that it did not alter the storage.  It then runs the real RemoveImages on
// the same state and makes sure that both runs produced the same output.
// The output of the real run is returned.
func testRemoveImagesDryRun(t *testing.T, ctx context.Context, runtime *Runtime, names []string, options *RemoveImagesOptions) ([]*RemoveImageReport, []error) {
	var dryRunOptions, realOptions RemoveImagesOptions
	if options != nil {
		dryRunOptions = *options
		realOptions = *options
	}
	dryRunOptions.DryRun = true
	realOptions.DryRun = false

	imagesBefore, containersBefore := testStorageState(t, ctx, runtime)

	dryRunReports, dryRunErrors := runtime.RemoveImages(ctx, names, &dryRunOptions)

	imagesAfter, containersAfter := testStorageState(t, ctx, runtime)
	require.Equal(t, imagesBefore, imagesAfter, "dry run must not alter images")
	require.Equal(t, containersBefore, containersAfter, "dry run must not alter containers")

	rmReports, rmErrors := runtime.RemoveImages(ctx, names, &realOptions)

	require.Equal(t, rmReports, dryRunReports, "dry run reports must match the real run")
	require.Len(t, dryRunErrors, len(rmErrors), "dry run errors must match the real run")
	for i := range rmErrors {
		require.EqualError(t, dryRunErrors[i], rmErrors[i].Error(), "dry run errors must match the real run")
	}

	return rmReports, rmErrors
}

func TestRemoveImages(t *testing.T) {
	// Note: this will resolve pull from the GCR registry (see
	// testdata/registries.conf).
	busyboxLatest := "quay.io/libpod/busybox:latest"

	runtime := testNewRuntime(t)
	ctx := context.Background()

	pullOptions := &PullOptions{}
	pullOptions.Writer = os.Stdout
	pulledImages, err := runtime.Pull(ctx, busyboxLatest, config.PullPolicyAlways, pullOptions)
	require.NoError(t, err)
	require.Len(t, pulledImages, 1)

	err = pulledImages[0].Tag("foobar")
	require.NoError(t, err)

	// containers/podman/issues/10685 - force removal on image with
	// multiple tags will only untag but not remove all tags including the
	// image.
	rmReports, rmErrors := runtime.RemoveImages(ctx, []string{"foobar"}, &RemoveImagesOptions{Force: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
	require.Equal(t, pulledImages[0].ID(), rmReports[0].ID)
	require.False(t, rmReports[0].Removed)
	require.Equal(t, []string{"localhost/foobar:latest"}, rmReports[0].Untagged)

	// "foobar" has already been removed, so we'd get an error
	_, rmErrors = runtime.RemoveImages(ctx, []string{"foobar"}, nil)
	require.NotNil(t, rmErrors)
	// ... unless we set Ignore=true
	rmReports, rmErrors = runtime.RemoveImages(ctx, []string{"foobar"}, &RemoveImagesOptions{Ignore: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 0)

	// The busybox image is still present even if foobar was force removed.
	exists, err := runtime.Exists(busyboxLatest)
	require.NoError(t, err)
	require.True(t, exists)

	rmReports, rmErrors = runtime.RemoveImages(ctx, []string{"foobar", "busybox"}, &RemoveImagesOptions{Ignore: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
}

func TestRemoveImagesSpaceLimitsWithNames(t *testing.T) {
	runtime := testNewRuntime(t)
	ctx := context.Background()

	rmReports, rmErrors := runtime.RemoveImages(ctx, []string{"busybox"}, &RemoveImagesOptions{MaxUsedSpace: 1})
	require.Nil(t, rmReports)
	require.Len(t, rmErrors, 1)
	require.ErrorContains(t, rmErrors[0], "space limits cannot be used when removing images by name")
}

func TestRemoveImagesNegativeSpaceLimits(t *testing.T) {
	runtime := testNewRuntime(t)
	ctx := context.Background()

	for _, opts := range []RemoveImagesOptions{
		{ReservedSpace: -1},
		{MaxUsedSpace: -1},
		{MinFreeSpace: -1},
	} {
		rmReports, rmErrors := runtime.RemoveImages(ctx, nil, &opts)
		require.Nil(t, rmReports)
		require.Len(t, rmErrors, 1)
		require.ErrorContains(t, rmErrors[0], "space limits must not be negative")
	}
}

func TestRemoveImagesSpaceLimits(t *testing.T) {
	runtime := testNewRuntime(t)
	ctx := context.Background()

	pull := func(name string) *Image {
		pullOptions := &PullOptions{}
		pullOptions.Writer = os.Stdout
		images, err := runtime.Pull(ctx, name, config.PullPolicyAlways, pullOptions)
		require.NoError(t, err)
		require.Len(t, images, 1)
		return images[0]
	}

	// A tagged image that must never be pruned.
	tagged := pull("quay.io/libpod/alpine_nginx:latest")

	// Two images untagged so that they are dangling prune candidates.
	var pulled []*Image
	for _, name := range []string{"quay.io/libpod/busybox:latest", "quay.io/libpod/alpine:latest"} {
		img := pull(name)
		require.NoError(t, img.Untag(name))
		pulled = append(pulled, img)
	}

	// Order the candidates the same way trimToSpaceLimits does.
	older, newer := pulled[0], pulled[1]
	if cmp.Or(newer.Created().Compare(older.Created()), strings.Compare(newer.ID(), older.ID())) < 0 {
		older, newer = newer, older
	}

	// A candidate only frees the space no other image uses, which is its
	// UniqueSize in DiskUsage.
	usages, _, err := runtime.DiskUsage(ctx)
	require.NoError(t, err)
	unique := make(map[string]int64)
	for _, usage := range usages {
		unique[usage.ID] = usage.UniqueSize
	}
	require.Contains(t, unique, older.ID())
	require.Contains(t, unique, newer.ID())
	total := unique[older.ID()] + unique[newer.ID()]

	options := &RemoveImagesOptions{Filters: []string{"dangling=true"}}

	// Usage is already under the limit, so nothing is removed.
	options.MaxUsedSpace = total + 1
	rmReports, rmErrors := runtime.RemoveImages(ctx, nil, options)
	require.Nil(t, rmErrors)
	require.Empty(t, rmReports)

	// The limit only fits the newer image, so only the older one is removed.
	options.MaxUsedSpace = unique[newer.ID()]
	rmReports, rmErrors = runtime.RemoveImages(ctx, nil, options)
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
	require.Equal(t, older.ID(), rmReports[0].ID)
	require.True(t, rmReports[0].Removed)

	for _, tc := range []struct {
		img    *Image
		exists bool
	}{
		{older, false},
		{newer, true},
		{tagged, true},
	} {
		exists, err := runtime.Exists(tc.img.ID())
		require.NoError(t, err)
		require.Equal(t, tc.exists, exists, tc.img.ID())
	}
}

func TestRemoveImagesDryRun(t *testing.T) {
	// Note: this will resolve pull from the GCR registry (see
	// testdata/registries.conf).
	busyboxLatest := "quay.io/libpod/busybox:latest"

	runtime := testNewRuntime(t)
	ctx := context.Background()

	pullOptions := &PullOptions{}
	pullOptions.Writer = os.Stdout
	pulledImages, err := runtime.Pull(ctx, busyboxLatest, config.PullPolicyAlways, pullOptions)
	require.NoError(t, err)
	require.Len(t, pulledImages, 1)

	err = pulledImages[0].Tag("foobar")
	require.NoError(t, err)

	// containers/podman/issues/10685 - a dry run of force removal on image
	// with multiple tags must only report the untag.
	rmReports, rmErrors := testRemoveImagesDryRun(t, ctx, runtime, []string{"foobar"}, &RemoveImagesOptions{Force: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
	require.Equal(t, pulledImages[0].ID(), rmReports[0].ID)
	require.False(t, rmReports[0].Removed)
	require.Equal(t, []string{"localhost/foobar:latest"}, rmReports[0].Untagged)

	err = pulledImages[0].Tag("foo:a")
	require.NoError(t, err)
	err = pulledImages[0].Tag("foo:b")
	require.NoError(t, err)

	// Removing an image with multiple tags by ID requires force.
	_, rmErrors = testRemoveImagesDryRun(t, ctx, runtime, []string{pulledImages[0].ID()}, nil)
	require.Len(t, rmErrors, 1)

	// Untagging several tags of the same image in one call; the dry run
	// must keep track of the tags it already untagged.
	rmReports, rmErrors = testRemoveImagesDryRun(t, ctx, runtime, []string{"foo:a", "foo:b"}, nil)
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
	require.False(t, rmReports[0].Removed)
	require.Equal(t, []string{"localhost/foo:a", "localhost/foo:b"}, rmReports[0].Untagged)

	// An image used by a container cannot be removed without force.
	_, err = runtime.store.CreateContainer("", nil, pulledImages[0].ID(), "", "", nil)
	require.NoError(t, err)
	_, rmErrors = testRemoveImagesDryRun(t, ctx, runtime, []string{"busybox"}, nil)
	require.Len(t, rmErrors, 1)

	// Force removes the container along with the image.
	rmReports, rmErrors = testRemoveImagesDryRun(t, ctx, runtime, []string{"busybox"}, &RemoveImagesOptions{Force: true})
	require.Nil(t, rmErrors)
	require.Len(t, rmReports, 1)
	require.True(t, rmReports[0].Removed)
	require.Equal(t, []string{busyboxLatest}, rmReports[0].Untagged)

	exists, err := runtime.Exists(busyboxLatest)
	require.NoError(t, err)
	require.False(t, exists)
}
