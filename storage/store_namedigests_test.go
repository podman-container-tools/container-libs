package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recomputeDigests counts only Digest and "manifest"-prefixed big data.
func createTestImageWithDigests(t *testing.T, store Store, id string, names []string, digests []digest.Digest) *Image {
	t.Helper()
	require.NotEmpty(t, digests)
	options := &ImageOptions{Digest: digests[0]}
	for i, d := range digests[1:] {
		options.BigData = append(options.BigData, ImageBigDataOption{
			Key:    fmt.Sprintf("manifest-%d", i),
			Data:   []byte(d.String()),
			Digest: d,
		})
	}
	img, err := store.CreateImage(id, names, "", "", options)
	require.NoError(t, err)
	require.ElementsMatch(t, digests, img.Digests)
	return img
}

// newStoreOverReadOnlyCopy shuts roStore down and returns a writable store that
// uses a copy of roStore as a read-only image store.
func newStoreOverReadOnlyCopy(t *testing.T, roStore Store) Store {
	t.Helper()
	roStoreRoot := roStore.GraphRoot()
	_, err := roStore.Shutdown(false)
	require.NoError(t, err)

	roStoreCopy := t.TempDir()
	require.NoError(t, os.CopyFS(roStoreCopy, os.DirFS(roStoreRoot)))

	rwStore := newTestStore(t, StoreOptions{
		GraphDriverOptions: []string{"imagestore=" + roStoreCopy},
	})
	t.Cleanup(func() { _, _ = rwStore.Shutdown(true) })
	return rwStore
}

func TestAddImageNameWithDigests(t *testing.T) {
	root := t.TempDir()
	opts := StoreOptions{RunRoot: filepath.Join(root, "run"), GraphRoot: filepath.Join(root, "root")}
	store := newTestStore(t, opts)

	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")
	img := createTestImageWithDigests(t, store, "image1", nil, []digest.Digest{d1, d2})

	callerDigests := []digest.Digest{d1, d2}
	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:tag", callerDigests))

	got, err := store.Image(img.ID)
	require.NoError(t, err)
	assert.Contains(t, got.Names, "repo:tag")
	assert.Equal(t, []digest.Digest{d1, d2}, got.NameDigests["repo:tag"])

	// The JSON key is the on-disk format.
	imagesJSON, err := os.ReadFile(filepath.Join(store.GraphRoot(), "vfs-images", "images.json"))
	require.NoError(t, err)
	assert.Contains(t, string(imagesJSON), `"name-digests"`)

	// Mutating the caller's slice afterwards must not change the store.
	callerDigests[0] = digest.FromString("mutated")
	got, err = store.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1, d2}, got.NameDigests["repo:tag"])

	// Mutating the slice in an Image() result must not change the store.
	got.NameDigests["repo:tag"][0] = digest.FromString("mutated-again")
	got, err = store.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1, d2}, got.NameDigests["repo:tag"])

	// Survives a store reload.
	_, err = store.Shutdown(false)
	require.NoError(t, err)
	reopened := newTestStore(t, opts)
	got, err = reopened.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1, d2}, got.NameDigests["repo:tag"])
}

func TestAddImageNameWithDigestsReplacesOnSameImage(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")
	img := createTestImageWithDigests(t, store, "image1", nil, []digest.Digest{d1, d2})

	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:tag", []digest.Digest{d1, d2}))
	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:tag", []digest.Digest{d2}))

	got, err := store.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d2}, got.NameDigests["repo:tag"])
}

func TestAddImageNameWithDigestsValidation(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	dOther := digest.FromString("other")
	img := createTestImageWithDigests(t, store, "image1", []string{"repo:tag"}, []digest.Digest{d1})

	err := store.AddImageNameWithDigests(img.ID, "repo:tag2", []digest.Digest{dOther})
	require.ErrorIs(t, err, ErrDigestUnknown)

	err = store.AddImageNameWithDigests(img.ID, "repo:tag2", nil)
	require.Error(t, err)

	got, err := store.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"repo:tag"}, got.Names)
	assert.Empty(t, got.NameDigests)

	_, err = store.Image("repo:tag2")
	require.Error(t, err)
}

func TestAddImageNameWithDigestsMovesNameAndKeepsOldRecord(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")
	imgA := createTestImageWithDigests(t, store, "imageA", nil, []digest.Digest{d1})
	imgB := createTestImageWithDigests(t, store, "imageB", nil, []digest.Digest{d2})

	require.NoError(t, store.AddImageNameWithDigests(imgA.ID, "repo:latest", []digest.Digest{d1}))

	before, err := store.Image("repo:latest")
	require.NoError(t, err)
	assert.Equal(t, imgA.ID, before.ID)

	require.NoError(t, store.AddImageNameWithDigests(imgB.ID, "repo:latest", []digest.Digest{d2}))

	gotA, err := store.Image(imgA.ID)
	require.NoError(t, err)
	assert.NotContains(t, gotA.Names, "repo:latest")
	assert.Equal(t, []digest.Digest{d1}, gotA.NameDigests["repo:latest"])

	gotB, err := store.Image(imgB.ID)
	require.NoError(t, err)
	assert.Contains(t, gotB.Names, "repo:latest")
	assert.Equal(t, []digest.Digest{d2}, gotB.NameDigests["repo:latest"])

	resolved, err := store.Image("repo:latest")
	require.NoError(t, err)
	assert.Equal(t, imgB.ID, resolved.ID)
}

func TestAddNamesMovesNameAndKeepsOldRecord(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	imgA := createTestImageWithDigests(t, store, "imageA", nil, []digest.Digest{d1})
	imgB := createTestImageWithDigests(t, store, "imageB", []string{}, []digest.Digest{d1})

	require.NoError(t, store.AddImageNameWithDigests(imgA.ID, "repo:latest", []digest.Digest{d1}))
	require.NoError(t, store.AddNames(imgB.ID, []string{"repo:latest"}))

	gotA, err := store.Image(imgA.ID)
	require.NoError(t, err)
	assert.NotContains(t, gotA.Names, "repo:latest")
	assert.Equal(t, []digest.Digest{d1}, gotA.NameDigests["repo:latest"])

	resolved, err := store.Image("repo:latest")
	require.NoError(t, err)
	assert.Equal(t, imgB.ID, resolved.ID)
}

func TestAddNamesClearsStaleRecordAndKeepsExistingRecord(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	imgA := createTestImageWithDigests(t, store, "imageA", nil, []digest.Digest{d1})
	imgB := createTestImageWithDigests(t, store, "imageB", nil, []digest.Digest{d1})

	// A move keeps the record on imgA.
	require.NoError(t, store.AddImageNameWithDigests(imgA.ID, "repo:tag", []digest.Digest{d1}))
	require.NoError(t, store.AddNames(imgB.ID, []string{"repo:tag"}))
	got, err := store.Image(imgA.ID)
	require.NoError(t, err)
	require.Contains(t, got.NameDigests, "repo:tag")

	// A generic add clears the stale record.
	require.NoError(t, store.AddNames(imgA.ID, []string{"repo:tag"}))
	got, err = store.Image(imgA.ID)
	require.NoError(t, err)
	assert.NotContains(t, got.NameDigests, "repo:tag")

	// Re-adding a name the image has keeps its record.
	require.NoError(t, store.AddImageNameWithDigests(imgA.ID, "repo:other", []digest.Digest{d1}))
	require.NoError(t, store.AddNames(imgA.ID, []string{"repo:other"}))
	got, err = store.Image(imgA.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:other"])
}

func TestRemoveNamesAndSetNamesDropRecords(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	img := createTestImageWithDigests(t, store, "image1", nil, []digest.Digest{d1})

	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:a", []digest.Digest{d1}))
	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:b", []digest.Digest{d1}))

	require.NoError(t, store.RemoveNames(img.ID, []string{"repo:a"}))
	got, err := store.Image(img.ID)
	require.NoError(t, err)
	assert.NotContains(t, got.NameDigests, "repo:a")
	assert.Contains(t, got.NameDigests, "repo:b")

	// SetNames that keeps "repo:b" but drops everything else keeps repo:b's record.
	require.NoError(t, store.SetNames(img.ID, []string{"repo:b"}))
	got, err = store.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:b"])

	// SetNames without "repo:b" drops its record.
	require.NoError(t, store.SetNames(img.ID, []string{"repo:c"}))
	got, err = store.Image(img.ID)
	require.NoError(t, err)
	assert.NotContains(t, got.NameDigests, "repo:b")
}

func TestCreateImageDuplicateNameFailsWithRecords(t *testing.T) {
	store := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	img := createTestImageWithDigests(t, store, "image1", []string{"repo:a"}, []digest.Digest{d1})
	require.NoError(t, store.AddImageNameWithDigests(img.ID, "repo:a", []digest.Digest{d1}))

	_, err := store.CreateImage("image2", []string{"repo:a"}, "", "", &ImageOptions{Digest: d1})
	require.ErrorIs(t, err, ErrDuplicateName)
}

func TestAddImageNameWithDigestsPullUp(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	img := createTestImageWithDigests(t, roStore, "image1", []string{"repo:a"}, []digest.Digest{d1})
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	require.NoError(t, rwStore.AddImageNameWithDigests(img.ID, "repo:b", []digest.Digest{d1}))

	got, err := rwStore.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:b"])
}

func TestAddNamesPullUpKeepsRecords(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	img := createTestImageWithDigests(t, roStore, "image1", []string{"repo:a"}, []digest.Digest{d1})
	require.NoError(t, roStore.AddImageNameWithDigests(img.ID, "repo:a", []digest.Digest{d1}))
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	require.NoError(t, rwStore.AddNames(img.ID, []string{"repo:c"}))

	got, err := rwStore.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:a"])
}

func TestCreateImagePullUpKeepsNameDigests(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")
	img := createTestImageWithDigests(t, roStore, "image1", []string{"repo:a"}, []digest.Digest{d1, d2})
	require.NoError(t, roStore.AddImageNameWithDigests(img.ID, "repo:a", []digest.Digest{d1}))
	require.NoError(t, roStore.AddImageNameWithDigests(img.ID, "repo:b", []digest.Digest{d2}))
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	_, err := rwStore.CreateImage(img.ID, nil, "", "", &ImageOptions{Digest: d1})
	require.NoError(t, err)

	got, err := rwStore.Image(img.ID)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:a"])
	assert.Equal(t, []digest.Digest{d2}, got.NameDigests["repo:b"])
}

// TestCreateImagePullUpDropsStaleNameDigests covers a name whose NameDigests
// record was left behind after the name itself moved to a different image.
// Pulling up the original image and re-adding that name as a caller-requested
// name must not resurrect the stale record.
func TestCreateImagePullUpDropsStaleNameDigests(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")

	img1 := createTestImageWithDigests(t, roStore, "image1", []string{"keep1"}, []digest.Digest{d1})
	require.NoError(t, roStore.AddImageNameWithDigests(img1.ID, "repo:old", []digest.Digest{d1}))
	other1 := createTestImageWithDigests(t, roStore, "other1", []string{"other1-name"}, []digest.Digest{d2})
	require.NoError(t, roStore.AddNames(other1.ID, []string{"repo:old"}))
	_, err := roStore.DeleteImage(other1.ID, true)
	require.NoError(t, err)

	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	got1, err := rwStore.CreateImage(img1.ID, []string{"repo:old"}, "", "", nil)
	require.NoError(t, err)
	assert.Contains(t, got1.Names, "repo:old")
	assert.NotContains(t, got1.NameDigests, "repo:old")
}

func TestCreateImagePullUpKeepsRecordOfRequestedOwnName(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	img := createTestImageWithDigests(t, roStore, "image1", []string{"repo:a"}, []digest.Digest{d1})
	require.NoError(t, roStore.AddImageNameWithDigests(img.ID, "repo:a", []digest.Digest{d1}))
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	got, err := rwStore.CreateImage(img.ID, []string{"repo:a"}, "", "", nil)
	require.NoError(t, err)
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:a"])
}

func TestCreateImagePullUpMovesNameFromWritableImage(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	d2 := digest.FromString("d2")
	roImg := createTestImageWithDigests(t, roStore, "image1", []string{"repo:a"}, []digest.Digest{d1})
	require.NoError(t, roStore.AddImageNameWithDigests(roImg.ID, "repo:a", []digest.Digest{d1}))
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	rwImg := createTestImageWithDigests(t, rwStore, "image2", nil, []digest.Digest{d2})
	require.NoError(t, rwStore.AddImageNameWithDigests(rwImg.ID, "repo:a", []digest.Digest{d2}))

	_, err := rwStore.CreateImage(roImg.ID, nil, "", "", nil)
	require.NoError(t, err)
	got, err := rwStore.Image(roImg.ID)
	require.NoError(t, err)
	assert.Contains(t, got.Names, "repo:a")
	assert.Equal(t, []digest.Digest{d1}, got.NameDigests["repo:a"])

	moved, err := rwStore.Image(rwImg.ID)
	require.NoError(t, err)
	assert.NotContains(t, moved.Names, "repo:a")
	assert.Equal(t, []digest.Digest{d2}, moved.NameDigests["repo:a"])
}

func TestPullUpFlags(t *testing.T) {
	roStore := newTestStore(t, StoreOptions{})
	d1 := digest.FromString("d1")
	flags := map[string]any{"flag": "value"}
	_, err := roStore.CreateImage("image1", []string{"repo:a"}, "", "", &ImageOptions{Digest: d1, Flags: flags})
	require.NoError(t, err)
	_, err = roStore.CreateImage("image2", []string{"repo:b"}, "", "", &ImageOptions{Digest: d1, Flags: flags})
	require.NoError(t, err)
	rwStore := newStoreOverReadOnlyCopy(t, roStore)

	require.NoError(t, rwStore.AddNames("image1", []string{"repo:c"}))
	got, err := rwStore.Image("image1")
	require.NoError(t, err)
	assert.Equal(t, flags, got.Flags)

	got, err = rwStore.CreateImage("image2", nil, "", "", nil)
	require.NoError(t, err)
	assert.NotContains(t, got.Flags, "flag")
}
