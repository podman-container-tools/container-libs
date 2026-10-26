//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/drivers/graphtest"
	"go.podman.io/storage/drivers/quota"
	"go.podman.io/storage/pkg/archive"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/reexec"
	"go.podman.io/storage/pkg/systemd"
	"go.podman.io/storage/pkg/unshare"
)

const driverName = "overlay"

// check that Driver correctly implements the ApplyDiffTemporary interface
var _ graphdriver.ApplyDiffStaging = &Driver{}

func init() {
	// Do not sure chroot to speed run time and allow archive
	// errors or hangs to be debugged directly from the test process.
	untar = archive.UntarUncompressed
	graphdriver.ApplyUncompressedLayer = archive.ApplyUncompressedLayer

	reexec.Init()
}

func skipIfNaive(t *testing.T) {
	td := t.TempDir()

	if err := doesSupportNativeDiff(td, ""); err != nil {
		t.Skipf("Cannot run test with naive diff")
	}
}

// Ensure that a layer created with force_mask will keep the root directory mode
// with user.containers.override_stat. This preserved mode should also be
// inherited by the upper layer, whether force_mask is set or not.
//
// This test is placed before TestOverlaySetup() because it uses driver options
// different from the other tests.
func TestContainersOverlayXattr(t *testing.T) {
	driver := graphtest.GetDriver(t, driverName, "force_mask=700")
	require.NoError(t, driver.Create("lower", "", nil))
	graphtest.ReconfigureDriver(t, driverName)
	require.NoError(t, driver.Create("upper", "lower", nil))

	root, err := driver.Get("upper", graphdriver.MountOpts{})
	require.NoError(t, err)
	fi, err := os.Stat(root)
	require.NoError(t, err)
	assert.Equal(t, 0o555&os.ModePerm, fi.Mode()&os.ModePerm, root)
}

func TestSupportsShifting(t *testing.T) {
	contiguousMap := []idtools.IDMap{
		{
			ContainerID: 0,
			HostID:      1000,
			Size:        65536,
		},
	}
	nonContiguousMap := []idtools.IDMap{
		{
			ContainerID: 0,
			HostID:      0,
			Size:        1,
		},
		{
			ContainerID: 2,
			HostID:      2,
			Size:        1,
		},
	}

	t.Run("no mount program", func(t *testing.T) {
		driver := graphtest.GetDriver(t, driverName)

		supported := driver.SupportsShifting(nil, nil)
		assert.Equal(t, supported, driver.SupportsShifting(contiguousMap, contiguousMap), "contiguous map with no mount program")
		assert.Equal(t, supported, driver.SupportsShifting(nonContiguousMap, nonContiguousMap), "non-contiguous map with no mount program")
	})

	t.Run("with mount program", func(t *testing.T) {
		driver := graphtest.GetDriver(t, driverName, "mount_program=/usr/bin/true")

		assert.True(t, driver.SupportsShifting(nil, nil), "nil map with mount program")
		assert.True(t, driver.SupportsShifting(contiguousMap, contiguousMap), "contiguous map with mount program")
		// If a mount program is specified, SupportsShifting must return false
		assert.False(t, driver.SupportsShifting(nonContiguousMap, nonContiguousMap), "non-contiguous map with mount program")
	})
}

// This avoids creating a new driver for each test if all tests are run
// Make sure to put new tests between TestOverlaySetup and TestOverlayTeardown
func TestOverlaySetup(t *testing.T) {
	graphtest.GetDriver(t, driverName)
}

func TestOverlayCreateEmpty(t *testing.T) {
	graphtest.DriverTestCreateEmpty(t, driverName)
}

func TestOverlayCreateBase(t *testing.T) {
	graphtest.DriverTestCreateBase(t, driverName)
}

func TestOverlayCreateSnap(t *testing.T) {
	graphtest.DriverTestCreateSnap(t, driverName)
}

func TestOverlayCreateFromTemplate(t *testing.T) {
	graphtest.DriverTestCreateFromTemplate(t, driverName)
}

func TestOverlay500LayerRead(t *testing.T) {
	graphtest.DriverTestDeepLayerRead(t, 500, driverName)
}

func TestOverlayDiffApply10Files(t *testing.T) {
	skipIfNaive(t)
	graphtest.DriverTestDiffApply(t, 10, driverName)
}

func TestOverlayChanges(t *testing.T) {
	skipIfNaive(t)
	graphtest.DriverTestChanges(t, driverName)
}

func TestOverlayEcho(t *testing.T) {
	graphtest.DriverTestEcho(t, driverName)
}

func TestOverlayListLayers(t *testing.T) {
	graphtest.DriverTestListLayers(t, driverName)
}

func TestOverlayTeardown(t *testing.T) {
	graphtest.PutDriver(t)
}

// Benchmarks should always setup new driver

func BenchmarkExists(b *testing.B) {
	graphtest.DriverBenchExists(b, driverName)
}

func BenchmarkGetEmpty(b *testing.B) {
	graphtest.DriverBenchGetEmpty(b, driverName)
}

func BenchmarkDiffBase(b *testing.B) {
	graphtest.DriverBenchDiffBase(b, driverName)
}

func BenchmarkDiffSmallUpper(b *testing.B) {
	graphtest.DriverBenchDiffN(b, 10, 10, driverName)
}

func BenchmarkDiff10KFileUpper(b *testing.B) {
	graphtest.DriverBenchDiffN(b, 10, 10000, driverName)
}

func BenchmarkDiff10KFilesBottom(b *testing.B) {
	graphtest.DriverBenchDiffN(b, 10000, 10, driverName)
}

func BenchmarkDiffApply100(b *testing.B) {
	graphtest.DriverBenchDiffApplyN(b, 100, driverName)
}

func BenchmarkDiff20Layers(b *testing.B) {
	graphtest.DriverBenchDeepLayerDiff(b, 20, driverName)
}

func BenchmarkRead20Layers(b *testing.B) {
	graphtest.DriverBenchDeepLayerRead(b, 20, driverName)
}

func Test_parseOptions(t *testing.T) {
	var (
		sharedMask  = os.FileMode(0o755)
		privateMask = os.FileMode(0o700)
		customMask  = os.FileMode(0o644)
	)

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test-file")

	f, err := os.Create(tmpFile)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		options []string
		want    *overlayOptions
		wantErr string
	}{
		{
			name:    "no opts",
			options: []string{},
			want:    &overlayOptions{},
		},
		{
			name:    "mountopt",
			options: []string{"mountopt=abc"},
			want:    &overlayOptions{mountOptions: "abc"},
		},
		{
			name:    "overlay prefix handling",
			options: []string{"overlay.mountopt=def"},
			want:    &overlayOptions{mountOptions: "def"},
		},
		{
			name:    "overlay2 prefix handling",
			options: []string{"overlay2.mountopt=ghi"},
			want:    &overlayOptions{mountOptions: "ghi"},
		},
		{
			name:    "size",
			options: []string{"size=50kb"},
			want:    &overlayOptions{quota: quota.Quota{Size: 51200}},
		},
		{
			name:    "size - invalid",
			options: []string{"size=abc"},
			wantErr: "invalid size",
		},
		{
			name:    "inodes",
			options: []string{"inodes=1024"},
			want:    &overlayOptions{quota: quota.Quota{Inodes: 1024}},
		},
		{
			name:    "inodes - invalid",
			options: []string{"inodes=abc"},
			wantErr: "invalid syntax",
		},
		{
			name:    "override_kernel_check",
			options: []string{"override_kernel_check=true"},
			want:    &overlayOptions{}, // Has no effect other than log output
		},
		{
			name:    "imagestore - valid directory",
			options: []string{"imagestore=" + tmpDir},
			want:    &overlayOptions{imageStores: []string{tmpDir}},
		},
		{
			name:    "imagestore - invalid (relative path)",
			options: []string{"imagestore=./relative/path"},
			wantErr: "is not absolute",
		},
		{
			name:    "imagestore - invalid (file instead of dir)",
			options: []string{"imagestore=" + tmpFile},
			wantErr: "must be a directory",
		},
		{
			name:    "additionallayerstore - valid directory",
			options: []string{"additionallayerstore=" + tmpDir},
			want:    &overlayOptions{layerStores: []additionalLayerStore{{path: tmpDir, withReference: false}}},
		},
		{
			name:    "additionallayerstore - with ref",
			options: []string{"additionallayerstore=" + tmpDir + ":ref"},
			want:    &overlayOptions{layerStores: []additionalLayerStore{{path: tmpDir, withReference: true}}},
		},
		{
			name:    "additionallayerstore - ref used twice",
			options: []string{"additionallayerstore=" + tmpDir + ":ref:ref"},
			wantErr: "contains \"ref\" option twice",
		},
		{
			name:    "additionallayerstore - unknown option",
			options: []string{"additionallayerstore=" + tmpDir + ":unknown"},
			wantErr: "contains unknown option \"unknown\"",
		},
		{
			name:    "mount_program - valid file",
			options: []string{"mount_program=" + tmpFile},
			want:    &overlayOptions{mountProgram: tmpFile},
		},
		{
			name:    "mount_program - missing file",
			options: []string{"mount_program=/does/not/exist"},
			wantErr: "can't stat program",
		},
		{
			name:    "use_composefs",
			options: []string{"use_composefs=true"},
			want:    &overlayOptions{useComposefs: true},
		},
		{
			name:    "use_composefs - invalid",
			options: []string{"use_composefs=notabool"},
			wantErr: "invalid syntax",
		},
		{
			name:    "skip_mount_home",
			options: []string{"skip_mount_home=true"},
			want:    &overlayOptions{skipMountHome: true},
		},
		{
			name:    "ignore_chown_errors",
			options: []string{"ignore_chown_errors=true"},
			want:    &overlayOptions{ignoreChownErrors: true},
		},
		{
			name:    "force_mask - shared",
			options: []string{"force_mask=shared"},
			want:    &overlayOptions{forceMask: &sharedMask},
		},
		{
			name:    "force_mask - private",
			options: []string{"force_mask=private"},
			want:    &overlayOptions{forceMask: &privateMask},
		},
		{
			name:    "force_mask - custom octal",
			options: []string{"force_mask=644"},
			want:    &overlayOptions{forceMask: &customMask},
		},
		{
			name:    "force_mask - invalid syntax",
			options: []string{"force_mask=hello"},
			wantErr: "invalid syntax",
		},
		{
			name:    "unknown option",
			options: []string{"unknown=value"},
			wantErr: `unknown option "unknown" ("unknown=value")`,
		},
		{
			name:    "unknown overlay prefix option",
			options: []string{"overlay.123=value"},
			wantErr: `unknown option "123" ("overlay.123=value")`,
		},
		{
			name:    "other driver option should not error",
			options: []string{"vfs.unknown=value"},
			want:    &overlayOptions{},
		},
		{
			name:    "unknown driver name must error",
			options: []string{"driver123.mountopt=value"},
			wantErr: `unknown driver "driver123" in option "driver123.mountopt=value"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := parseOptions(tt.options)
			if tt.wantErr != "" {
				require.ErrorContains(t, gotErr, tt.wantErr)
				return
			}
			require.NoError(t, gotErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRunMountProgram(t *testing.T) {
	tmpDir := t.TempDir()
	outFile := filepath.Join(tmpDir, "out.txt")
	scriptPath := filepath.Join(tmpDir, "fake-mount.sh")
	script := "#!/bin/sh\npwd > " + outFile + "\necho \"$@\" >> " + outFile + "\ncat /proc/self/cgroup >> " + outFile + "\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	targetDir := filepath.Join(tmpDir, "merged")
	require.NoError(t, runMountProgram(scriptPath, tmpDir, "lowerdir=a", targetDir))

	data, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), tmpDir+"\n-o lowerdir=a "+targetDir+"\n")
	if unshare.IsRootless() && systemd.RunsOnSystemd() {
		if conn, err := systemd.UserConnection(unshare.GetRootlessUID()); err == nil {
			conn.Close()
			assert.Contains(t, string(data), "/user.slice/fuse-overlayfs-")
			assert.Contains(t, string(data), ".scope")
		}
	}

	failScriptPath := filepath.Join(tmpDir, "fail-mount.sh")
	failScript := "#!/bin/sh\necho \"custom mount error\" >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(failScriptPath, []byte(failScript), 0o755))

	err = runMountProgram(failScriptPath, tmpDir, "lowerdir=a", targetDir)
	require.ErrorContains(t, err, "using mount program "+failScriptPath+": custom mount error")
}
