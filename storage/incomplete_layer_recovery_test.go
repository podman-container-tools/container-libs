package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIncompleteLayersRecoveredChildrenFirst covers the state an interrupted
// deletion leaves behind: every layer of a chain flagged incomplete, none of them
// removed yet. Reopening the store must delete them children first.
//
// Deleting a parent that still has a child contradicts the driver's model and can
// fail outright on snapshot-based drivers. A failed deletion here fails load(),
// and load() only retries maxLayerStoreCleanupIterations times while each retry
// can only unwind one leaf, so a chain longer than that would leave the store
// permanently unopenable.
//
// vfs and overlay both tolerate the wrong order, so the deletion sequence itself
// is what has to be asserted; load() reports it only by logging, hence the
// capture below. It doubles as a way to keep these warnings off the test binary's
// stderr, which some helper subprocesses treat as failure output.
func TestIncompleteLayersRecoveredChildrenFirst(t *testing.T) {
	// Longer than maxLayerStoreCleanupIterations, so retrying could not rescue a
	// parent-first deletion on a driver that rejects it.
	chainLen := maxLayerStoreCleanupIterations + 5

	wd := t.TempDir()
	runRoot := filepath.Join(wd, "run")
	graphRoot := filepath.Join(wd, "root")
	s := newTestStore(t, StoreOptions{RunRoot: runRoot, GraphRoot: graphRoot, GraphDriverName: "vfs"})

	// ids is in creation order, so a parent always precedes its child.
	ids := []string{}
	parent := ""
	for range chainLen {
		layer, err := s.CreateLayer("", parent, nil, "", false, nil)
		require.NoError(t, err)
		ids = append(ids, layer.ID)
		parent = layer.ID
	}

	// Ask the store where its metadata lives rather than assuming a layout.
	st, ok := s.(*store)
	require.True(t, ok)
	rlstore, err := st.getLayerStore()
	require.NoError(t, err)
	lstore, ok := rlstore.(*layerStore)
	require.True(t, ok)
	path := lstore.jsonPath[indexFromLayerLocation(stableLayerLocation)]

	_, err = s.Shutdown(false)
	require.NoError(t, err)

	// Flag the whole chain, as the first save of a batched deletion would.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var layers []*Layer
	require.NoError(t, json.Unmarshal(data, &layers))
	marked := 0
	for _, layer := range layers {
		if slices.Contains(ids, layer.ID) {
			require.True(t, markIncomplete(layer))
			marked++
		}
	}
	require.Equal(t, chainLen, marked, "every layer of the chain should be present on disk")
	out, err := json.Marshal(layers)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))

	var log bytes.Buffer
	prevOut := logrus.StandardLogger().Out
	logrus.SetOutput(&log)
	defer logrus.SetOutput(prevOut)

	// Reopening must reap the whole chain in a single load().
	reopened, err := GetStore(StoreOptions{RunRoot: runRoot, GraphRoot: graphRoot, GraphDriverName: "vfs"})
	require.NoError(t, err, "store must still open after an interrupted deletion")
	defer func() { _, _ = reopened.Shutdown(true) }()

	remaining, err := reopened.Layers()
	require.NoError(t, err)
	for _, layer := range remaining {
		assert.NotContains(t, ids, layer.ID, "layer %s was flagged incomplete and should have been reaped", layer.ID)
	}

	// Recover the deletion sequence as positions in the creation chain.
	deleted := []int{}
	for line := range strings.SplitSeq(log.String(), "\n") {
		if !strings.Contains(line, "Found incomplete layer") {
			continue
		}
		if i := slices.IndexFunc(ids, func(id string) bool { return strings.Contains(line, id) }); i >= 0 {
			deleted = append(deleted, i)
		}
	}
	require.Len(t, deleted, chainLen, "every flagged layer should be reported deleted")
	for i := 1; i < len(deleted); i++ {
		assert.Less(t, deleted[i], deleted[i-1],
			"layer %d was deleted after its descendant %d; deletion must run children first", deleted[i-1], deleted[i])
	}
}
