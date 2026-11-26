package storage

import (
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func newTestImageStore(t *testing.T) rwImageStore {
	t.Helper()
	store, err := newImageStore(t.TempDir())
	require.Nil(t, err)
	return store
}

func addTestImage(t *testing.T, store rwImageStore, id string, names []string) {
	err := store.startWriting()
	require.NoError(t, err)
	defer store.stopWriting()

	options := ImageOptions{
		CreationDate: time.Now(),
		Digest:       digest.FromString(""),
	}
	_, err = store.create(
		id, []string{}, "", options,
	)

	require.Nil(t, err)
	require.Nil(t, store.updateNames(id, names, setNames))
}

func TestAddNameToHistorySuccess(t *testing.T) {
	// Given
	image := Image{}

	// When
	image.addNameToHistory("first")
	image.addNameToHistory("first")
	image.addNameToHistory("second")

	// Then
	require.Len(t, image.NamesHistory, 2)
}

func TestImageMatchingDigest(t *testing.T) {
	dir := t.TempDir()
	store, err := newImageStore(dir)
	require.NoError(t, err)

	matchingDigest := digest.FromString("image")

	require.NoError(t, store.startWriting())
	image, err := store.create("first", nil, "", ImageOptions{MatchingDigest: matchingDigest})
	require.NoError(t, err)
	require.Equal(t, matchingDigest, image.MatchingDigest)

	// Creating another image with the same MatchingDigest is rejected as a
	// duplicate ID, so that callers using that error to detect "the image is
	// already present" keep working.
	_, err = store.create("second", nil, "", ImageOptions{MatchingDigest: matchingDigest})
	require.ErrorIs(t, err, ErrDuplicateID)

	// Images without a MatchingDigest don't collide with each other.
	_, err = store.create("third", nil, "", ImageOptions{})
	require.NoError(t, err)
	_, err = store.create("fourth", nil, "", ImageOptions{})
	require.NoError(t, err)

	found, err := store.ByMatchingDigest(matchingDigest)
	require.NoError(t, err)
	require.Equal(t, "first", found.ID)

	_, err = store.ByMatchingDigest(digest.FromString("unknown"))
	require.ErrorIs(t, err, ErrImageUnknown)
	_, err = store.ByMatchingDigest("")
	require.ErrorIs(t, err, ErrImageUnknown)
	store.stopWriting()

	// The value and the index survive a reload.
	reloaded, err := newImageStore(dir)
	require.NoError(t, err)
	require.NoError(t, reloaded.startReading())
	found, err = reloaded.ByMatchingDigest(matchingDigest)
	require.NoError(t, err)
	require.Equal(t, "first", found.ID)
	reloaded.stopReading()

	// Deleting the image also removes it from the index.
	require.NoError(t, store.startWriting())
	require.NoError(t, store.Delete("first"))
	_, err = store.ByMatchingDigest(matchingDigest)
	require.ErrorIs(t, err, ErrImageUnknown)
	store.stopWriting()
}

func TestHistoryNames(t *testing.T) {
	// Given
	store := newTestImageStore(t)

	// When
	const firstImageID = "first"
	addTestImage(t, store, firstImageID, []string{"1", "2"})

	const secondImageID = "second"
	addTestImage(t, store, secondImageID, []string{"2", "3"})

	// Then
	firstImage, err := store.Get(firstImageID)
	require.Nil(t, err)
	require.Len(t, firstImage.Names, 1)
	require.Equal(t, firstImage.Names[0], "1")
	require.Len(t, firstImage.NamesHistory, 2)
	require.Equal(t, firstImage.NamesHistory[0], "2")
	require.Equal(t, firstImage.NamesHistory[1], "1")

	secondImage, err := store.Get(secondImageID)
	require.Nil(t, err)
	require.Len(t, secondImage.Names, 2)
	require.Equal(t, secondImage.Names[0], "2")
	require.Equal(t, secondImage.Names[1], "3")
	require.Len(t, secondImage.NamesHistory, 2)
	require.Equal(t, secondImage.NamesHistory[0], "3")
	require.Equal(t, secondImage.NamesHistory[1], "2")

	// And When
	require.NoError(t, store.startWriting())
	defer store.stopWriting()
	require.Nil(t, store.updateNames(firstImageID, []string{"1", "2", "3", "4"}, setNames))

	// Then
	firstImage, err = store.Get(firstImageID)
	require.Nil(t, err)
	require.Len(t, firstImage.NamesHistory, 4)
	require.Equal(t, firstImage.NamesHistory[0], "4")
	require.Equal(t, firstImage.NamesHistory[1], "3")
	require.Equal(t, firstImage.NamesHistory[2], "2")
	require.Equal(t, firstImage.NamesHistory[3], "1")

	secondImage, err = store.Get(secondImageID)
	require.Nil(t, err)
	require.Len(t, secondImage.Names, 0)
	require.Len(t, secondImage.NamesHistory, 2)
	require.Equal(t, secondImage.NamesHistory[0], "3")
	require.Equal(t, secondImage.NamesHistory[1], "2")

	// test independent add and remove operations
	require.Nil(t, store.updateNames(firstImageID, []string{"5"}, addNames))
	firstImage, err = store.Get(firstImageID)
	require.Nil(t, err)
	require.Equal(t, firstImage.NamesHistory, []string{"4", "3", "2", "1", "5"})

	// history should still contain old values
	require.Nil(t, store.updateNames(firstImageID, []string{"5"}, removeNames))
	firstImage, err = store.Get(firstImageID)
	require.Nil(t, err)
	require.Equal(t, firstImage.NamesHistory, []string{"4", "3", "2", "1", "5"})
}
