//go:build linux

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// layersJSONPath returns the on-disk path of the layer store's metadata file.
func layersJSONPath(t *testing.T, store Store) string {
	t.Helper()
	return filepath.Join(store.GraphRoot(), store.GraphDriverName()+"-layers", "layers.json")
}

// countLayersJSONReplacements runs fn while watching the directory holding
// layers.json, and returns the number of times layers.json was atomically
// replaced. Every metadata save goes through AtomicWriteFileWithOpts, which
// writes a temporary file and renames it over the target, so one IN_MOVED_TO
// for "layers.json" is exactly one save.
//
// Events are drained by a concurrent reader because inotify coalesces
// identical successive events that have not yet been read; counting after the
// fact would collapse every save into one.
func countLayersJSONReplacements(t *testing.T, path string, fn func()) int {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	require.NoError(t, err, "inotify_init1")
	defer unix.Close(fd)
	_, err = unix.InotifyAddWatch(fd, filepath.Dir(path), unix.IN_MOVED_TO)
	require.NoError(t, err, "inotify_add_watch")

	target := filepath.Base(path)
	var mu sync.Mutex
	count := 0
	stop := make(chan struct{})
	done := make(chan struct{})

	drain := func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := unix.Read(fd, buf)
			if n <= 0 || err != nil {
				return // EAGAIN: nothing queued right now
			}
			for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
				raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
				if raw.Len > 0 {
					e := offset + unix.SizeofInotifyEvent + int(raw.Len)
					name := strings.TrimRight(string(buf[offset+unix.SizeofInotifyEvent:e]), "\x00")
					if name == target {
						mu.Lock()
						count++
						mu.Unlock()
					}
				}
				offset += unix.SizeofInotifyEvent + int(raw.Len)
			}
		}
	}

	// Poll continuously: inotify coalesces identical successive events that have
	// not yet been read, so counting only after fn() would collapse every save
	// into one.
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				drain()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	fn()
	time.Sleep(300 * time.Millisecond)
	close(stop)
	<-done
	drain()

	mu.Lock()
	defer mu.Unlock()
	return count
}

// makeImageWithLayers builds a chain of nLayers real layers and an image on top.
func makeImageWithLayers(t *testing.T, store Store, nLayers int) (*Image, []string) {
	t.Helper()
	parent := ""
	ids := []string{}
	for range nLayers {
		layer, err := store.CreateLayer("", parent, nil, "", true, nil)
		require.NoError(t, err)
		ids = append(ids, layer.ID)
		parent = layer.ID
	}
	image, err := store.CreateImage("", nil, parent, "", nil)
	require.NoError(t, err)
	return image, ids
}

// readLayerIDs returns the set of layer IDs currently recorded in layers.json.
func readLayerIDs(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var records []struct {
		ID    string         `json:"id"`
		Flags map[string]any `json:"flags,omitempty"`
	}
	require.NoError(t, json.Unmarshal(data, &records))
	out := map[string]bool{}
	for _, r := range records {
		out[r.ID] = true
	}
	return out
}

// TestDeleteImageBatchesMetadataSaves asserts that removing an image rewrites
// layers.json a fixed number of times regardless of how many layers the image
// has. Deleting layers one at a time costs two saves per layer, so a 10-layer
// image would rewrite the file 20 times.
func TestDeleteImageBatchesMetadataSaves(t *testing.T) {
	const nLayers = 10
	store := newTestStore(t, StoreOptions{})
	path := layersJSONPath(t, store)

	image, ids := makeImageWithLayers(t, store, nLayers)
	require.Len(t, ids, nLayers)

	var removed []string
	saves := countLayersJSONReplacements(t, path, func() {
		var err error
		removed, err = store.DeleteImage(image.ID, true)
		require.NoError(t, err)
	})
	assert.Len(t, removed, nLayers, "every layer should be reported as removed")

	assert.LessOrEqual(t, saves, 2,
		"deleting a %d-layer image should rewrite layers.json at most twice, got %d", nLayers, saves)
	assert.Positive(t, saves, "the deletion must be persisted")

	surviving := readLayerIDs(t, path)
	for _, id := range ids {
		assert.NotContains(t, surviving, id, "layer %s should be gone from layers.json", id)
	}
}
