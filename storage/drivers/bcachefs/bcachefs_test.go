//go:build linux

package bcachefs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/drivers/graphtest"
	"go.podman.io/storage/pkg/archive"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/reexec"
)

func init() {
	reexec.Init()
}

func testRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("BCACHEFS_TEST_ROOT")
	if root == "" {
		t.Skip("BCACHEFS_TEST_ROOT not set, skipping bcachefs tests")
	}
	return root
}

func TestBcachefsSubvolumeOperations(t *testing.T) {
	root := testRoot(t)
	testDir := filepath.Join(root, "test-subvol")

	t.Run("CreateSubvolume", func(t *testing.T) {
		err := subvolCreate(testDir)
		if err != nil {
			t.Fatalf("Failed to create subvolume: %v", err)
		}

		if _, err := os.Stat(testDir); os.IsNotExist(err) {
			t.Fatal("Subvolume directory does not exist after creation")
		}
	})

	t.Run("CreateSnapshot", func(t *testing.T) {
		snapshotDir := filepath.Join(root, "test-snapshot")
		err := subvolSnapshot(testDir, snapshotDir)
		if err != nil {
			t.Fatalf("Failed to create snapshot: %v", err)
		}

		if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
			t.Fatal("Snapshot directory does not exist after creation")
		}

		err = subvolDelete(root, "test-snapshot")
		if err != nil {
			t.Fatalf("Failed to delete snapshot: %v", err)
		}
	})

	t.Run("DeleteSubvolume", func(t *testing.T) {
		err := subvolDelete(root, "test-subvol")
		if err != nil {
			t.Fatalf("Failed to delete subvolume: %v", err)
		}

		if _, err := os.Stat(testDir); !os.IsNotExist(err) {
			t.Fatal("Subvolume directory still exists after deletion")
		}
	})
}

// TestDeepSnapshotChain creates a chain of N snapshots, each snapshotting the
// previous one, and verifies that seed files created in the first subvolume
// survive through the entire chain. This mimics the layer stacking that
// podman build does: base → snap → snap → snap → ...
func TestDeepSnapshotChain(t *testing.T) {
	root := testRoot(t)
	const depth = 20

	base := filepath.Join(root, "chain-base")
	if err := subvolCreate(base); err != nil {
		t.Fatalf("create base: %v", err)
	}
	defer func() {
		for i := depth; i >= 1; i-- {
			_ = subvolDelete(root, fmt.Sprintf("chain-%d", i))
		}
		_ = subvolDelete(root, "chain-base")
	}()

	seedFiles := map[string]string{
		"bin/sh":         "#!/bin/dash\n",
		"usr/bin/python": "#!/usr/bin/python3\n",
		"etc/passwd":     "root:x:0:0:root:/root:/bin/bash\n",
		"tmp/test.sh":    "#!/bin/sh\necho hello\n",
	}
	for relPath, content := range seedFiles {
		full := filepath.Join(base, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", relPath, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	if err := os.Symlink("sh", filepath.Join(base, "bin/dash")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	prev := base
	for i := 1; i <= depth; i++ {
		name := fmt.Sprintf("chain-%d", i)
		snapPath := filepath.Join(root, name)

		if err := subvolSnapshot(prev, snapPath); err != nil {
			t.Fatalf("snapshot %d (%s -> %s): %v", i, prev, snapPath, err)
		}

		for relPath, expectedContent := range seedFiles {
			full := filepath.Join(snapPath, relPath)
			got, err := os.ReadFile(full)
			if err != nil {
				t.Fatalf("layer %d: seed file %s missing: %v", i, relPath, err)
			}
			if string(got) != expectedContent {
				t.Fatalf("layer %d: seed file %s content mismatch: got %q, want %q", i, relPath, got, expectedContent)
			}
		}

		target, err := os.Readlink(filepath.Join(snapPath, "bin/dash"))
		if err != nil {
			t.Fatalf("layer %d: symlink bin/dash missing: %v", i, err)
		}
		if target != "sh" {
			t.Fatalf("layer %d: symlink bin/dash target = %q, want %q", i, target, "sh")
		}

		layerFile := filepath.Join(snapPath, fmt.Sprintf("layer-%d.txt", i))
		if err := os.WriteFile(layerFile, []byte(fmt.Sprintf("layer %d\n", i)), 0o644); err != nil {
			t.Fatalf("layer %d: write layer file: %v", i, err)
		}

		prev = snapPath
	}

	last := filepath.Join(root, fmt.Sprintf("chain-%d", depth))
	for i := 1; i <= depth; i++ {
		layerFile := filepath.Join(last, fmt.Sprintf("layer-%d.txt", i))
		got, err := os.ReadFile(layerFile)
		if err != nil {
			t.Fatalf("final check: layer-%d.txt missing: %v", i, err)
		}
		expected := fmt.Sprintf("layer %d\n", i)
		if string(got) != expected {
			t.Fatalf("final check: layer-%d.txt = %q, want %q", i, got, expected)
		}
	}

	t.Logf("All %d snapshot layers verified: seed files + per-layer files intact", depth)
}

// --- Graphtest integration ---
//
// These exercise the full graphdriver lifecycle: Init, Create, Get,
// ApplyDiff (tar extraction), Diff (tar export), Changes, Put, Remove.
// The root must be on a bcachefs filesystem (BCACHEFS_TEST_ROOT).

func initBcachefsDriver(t *testing.T) graphdriver.Driver {
	t.Helper()
	root := testRoot(t)
	home := filepath.Join(root, "graphtest-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	d, err := Init(home, graphdriver.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Cleanup() })
	return d
}

// TestGraphCreateEmpty tests creating an empty layer.
func TestGraphCreateEmpty(t *testing.T) {
	d := initBcachefsDriver(t)

	err := d.Create("empty", "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { d.Remove("empty") })

	require.True(t, d.Exists("empty"))

	dir, err := d.Get("empty", graphdriver.MountOpts{})
	require.NoError(t, err)
	defer d.Put("empty")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestGraphCreateSnap tests creating a snapshot preserves parent content.
func TestGraphCreateSnap(t *testing.T) {
	d := initBcachefsDriver(t)

	err := d.Create("base", "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { d.Remove("base") })

	dir, err := d.Get("base", graphdriver.MountOpts{})
	require.NoError(t, err)
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin/sh"), []byte("#!/bin/dash\n"), 0o755)
	os.Symlink("sh", filepath.Join(dir, "bin/dash"))
	d.Put("base")

	err = d.Create("snap", "base", nil)
	require.NoError(t, err)
	t.Cleanup(func() { d.Remove("snap") })

	snapDir, err := d.Get("snap", graphdriver.MountOpts{})
	require.NoError(t, err)
	defer d.Put("snap")

	got, err := os.ReadFile(filepath.Join(snapDir, "bin/sh"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/dash\n", string(got))

	target, err := os.Readlink(filepath.Join(snapDir, "bin/dash"))
	require.NoError(t, err)
	require.Equal(t, "sh", target)
}

// TestGraphDiffApply tests the full diff → apply round-trip: create a base
// layer, snapshot it, modify the snapshot, compute the diff, apply it to a
// fresh snapshot, and verify the result matches.
func TestGraphDiffApply(t *testing.T) {
	d := initBcachefsDriver(t)

	// Base layer with seed content
	require.NoError(t, d.Create("base", "", nil))
	t.Cleanup(func() { d.Remove("base") })

	baseDir, err := d.Get("base", graphdriver.MountOpts{})
	require.NoError(t, err)
	for _, dir := range []string{"bin", "usr/bin", "etc", "var/lib"} {
		os.MkdirAll(filepath.Join(baseDir, dir), 0o755)
	}
	os.WriteFile(filepath.Join(baseDir, "bin/sh"), []byte("#!/bin/dash\n"), 0o755)
	os.WriteFile(filepath.Join(baseDir, "etc/passwd"), []byte("root:x:0:0::\n"), 0o644)
	os.WriteFile(filepath.Join(baseDir, "var/lib/removeme"), []byte("gone\n"), 0o644)
	os.Symlink("sh", filepath.Join(baseDir, "bin/dash"))
	d.Put("base")

	// Upper layer: snapshot of base, then modify
	require.NoError(t, d.Create("upper", "base", nil))
	t.Cleanup(func() { d.Remove("upper") })

	upperDir, err := d.Get("upper", graphdriver.MountOpts{})
	require.NoError(t, err)
	os.WriteFile(filepath.Join(upperDir, "usr/bin/python3"), []byte("#!/usr/bin/python3\n"), 0o755)
	os.Remove(filepath.Join(upperDir, "var/lib/removeme"))
	d.Put("upper")

	// Compute diff between upper and base
	diffArchive, err := d.Diff("upper", &idtools.IDMappings{}, "base", &idtools.IDMappings{}, "")
	require.NoError(t, err)

	// Apply diff to a fresh snapshot of base
	require.NoError(t, d.Create("applied", "base", nil))
	t.Cleanup(func() { d.Remove("applied") })

	_, err = d.ApplyDiff("applied", graphdriver.ApplyDiffOpts{
		Diff: diffArchive,
	})
	require.NoError(t, err)

	// Verify the applied layer matches upper
	appliedDir, err := d.Get("applied", graphdriver.MountOpts{})
	require.NoError(t, err)
	defer d.Put("applied")

	// Seed files from base must survive
	got, err := os.ReadFile(filepath.Join(appliedDir, "bin/sh"))
	require.NoError(t, err, "bin/sh must survive from base")
	require.Equal(t, "#!/bin/dash\n", string(got))

	got, err = os.ReadFile(filepath.Join(appliedDir, "etc/passwd"))
	require.NoError(t, err, "etc/passwd must survive from base")
	require.Equal(t, "root:x:0:0::\n", string(got))

	// Symlink must survive
	target, err := os.Readlink(filepath.Join(appliedDir, "bin/dash"))
	require.NoError(t, err, "bin/dash symlink must survive")
	require.Equal(t, "sh", target)

	// Added file must be present
	got, err = os.ReadFile(filepath.Join(appliedDir, "usr/bin/python3"))
	require.NoError(t, err, "added file must be present")
	require.Equal(t, "#!/usr/bin/python3\n", string(got))

	// Removed file must be gone
	_, err = os.Stat(filepath.Join(appliedDir, "var/lib/removeme"))
	require.True(t, os.IsNotExist(err), "removed file must be gone")
}

// TestGraphChanges verifies that the Changes method correctly detects
// additions, modifications, and deletions between a layer and its parent.
func TestGraphChanges(t *testing.T) {
	d := initBcachefsDriver(t)

	require.NoError(t, d.Create("base", "", nil))
	t.Cleanup(func() { d.Remove("base") })

	baseDir, err := d.Get("base", graphdriver.MountOpts{})
	require.NoError(t, err)
	os.MkdirAll(filepath.Join(baseDir, "bin"), 0o755)
	os.WriteFile(filepath.Join(baseDir, "bin/sh"), []byte("old\n"), 0o755)
	os.WriteFile(filepath.Join(baseDir, "bin/rm-me"), []byte("bye\n"), 0o644)
	d.Put("base")

	require.NoError(t, d.Create("child", "base", nil))
	t.Cleanup(func() { d.Remove("child") })

	childDir, err := d.Get("child", graphdriver.MountOpts{})
	require.NoError(t, err)
	os.WriteFile(filepath.Join(childDir, "bin/added"), []byte("new\n"), 0o755)
	os.Remove(filepath.Join(childDir, "bin/rm-me"))
	d.Put("child")

	changes, err := d.Changes("child", nil, "base", nil, "")
	require.NoError(t, err)

	changeMap := make(map[string]archive.ChangeType)
	for _, c := range changes {
		changeMap[c.Path] = c.Kind
	}

	require.Contains(t, changeMap, "/bin/added", "added file should appear in changes")
	require.Equal(t, archive.ChangeType(archive.ChangeAdd), changeMap["/bin/added"], "added file should be ChangeAdd")
	require.Contains(t, changeMap, "/bin/rm-me", "removed file should appear in changes")
	require.Equal(t, archive.ChangeType(archive.ChangeDelete), changeMap["/bin/rm-me"], "removed file should be ChangeDelete")
}

// TestGraphDeepLayerDiffApply simulates a podman build: creates a base layer,
// then iteratively snapshots, modifies, diffs, and applies — verifying that
// seed files survive through N round-trips of diff+apply.
func TestGraphDeepLayerDiffApply(t *testing.T) {
	d := initBcachefsDriver(t)
	const depth = 10

	// Base layer
	require.NoError(t, d.Create("layer-0", "", nil))
	t.Cleanup(func() {
		for i := depth; i >= 0; i-- {
			d.Remove(fmt.Sprintf("layer-%d", i))
			d.Remove(fmt.Sprintf("work-%d", i))
		}
	})

	baseDir, err := d.Get("layer-0", graphdriver.MountOpts{})
	require.NoError(t, err)
	for _, dir := range []string{"bin", "usr/bin", "etc"} {
		os.MkdirAll(filepath.Join(baseDir, dir), 0o755)
	}
	os.WriteFile(filepath.Join(baseDir, "bin/sh"), []byte("#!/bin/dash\n"), 0o755)
	os.Symlink("sh", filepath.Join(baseDir, "bin/dash"))
	os.WriteFile(filepath.Join(baseDir, "etc/passwd"), []byte("root:x:0:0::\n"), 0o644)
	d.Put("layer-0")

	for i := 1; i <= depth; i++ {
		parent := fmt.Sprintf("layer-%d", i-1)
		work := fmt.Sprintf("work-%d", i)
		layer := fmt.Sprintf("layer-%d", i)

		// Simulate container: snapshot parent, run "command" (add a file)
		require.NoError(t, d.CreateReadWrite(work, parent, nil))

		workDir, err := d.Get(work, graphdriver.MountOpts{})
		require.NoError(t, err)
		os.WriteFile(
			filepath.Join(workDir, fmt.Sprintf("etc/layer-%d.conf", i)),
			[]byte(fmt.Sprintf("config from layer %d\n", i)),
			0o644,
		)
		d.Put(work)

		// Compute diff
		diffArchive, err := d.Diff(work, &idtools.IDMappings{}, parent, &idtools.IDMappings{}, "")
		require.NoError(t, err, "diff at layer %d", i)

		// Create new image layer and apply diff
		require.NoError(t, d.Create(layer, parent, nil))
		_, err = d.ApplyDiff(layer, graphdriver.ApplyDiffOpts{Diff: diffArchive})
		require.NoError(t, err, "apply diff at layer %d", i)

		// Verify seed files survive
		layerDir, err := d.Get(layer, graphdriver.MountOpts{})
		require.NoError(t, err)

		got, err := os.ReadFile(filepath.Join(layerDir, "bin/sh"))
		require.NoError(t, err, "layer %d: bin/sh must exist", i)
		require.Equal(t, "#!/bin/dash\n", string(got), "layer %d: bin/sh content", i)

		target, err := os.Readlink(filepath.Join(layerDir, "bin/dash"))
		require.NoError(t, err, "layer %d: bin/dash symlink must exist", i)
		require.Equal(t, "sh", target, "layer %d: bin/dash target", i)

		got, err = os.ReadFile(filepath.Join(layerDir, "etc/passwd"))
		require.NoError(t, err, "layer %d: etc/passwd must exist", i)
		require.Equal(t, "root:x:0:0::\n", string(got), "layer %d: etc/passwd content", i)

		// Verify all per-layer config files accumulated
		for j := 1; j <= i; j++ {
			confFile := filepath.Join(layerDir, fmt.Sprintf("etc/layer-%d.conf", j))
			got, err := os.ReadFile(confFile)
			require.NoError(t, err, "layer %d: etc/layer-%d.conf must exist", i, j)
			expected := fmt.Sprintf("config from layer %d\n", j)
			require.Equal(t, expected, string(got), "layer %d: etc/layer-%d.conf content", i, j)
		}

		d.Put(layer)
	}

	t.Logf("All %d layers verified: seed files + per-layer files survive diff+apply round-trips", depth)
}

// TestGraphCreateFromTemplate tests the CreateFromTemplate path used by
// podman build commit (snapshot the container layer directly).
func TestGraphCreateFromTemplate(t *testing.T) {
	graphtest.DriverTestCreateFromTemplate(t, "bcachefs")
}

// TestGraphListLayers tests ListLayers.
func TestGraphListLayers(t *testing.T) {
	graphtest.DriverTestListLayers(t, "bcachefs")
}
