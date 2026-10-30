//go:build linux

package cgroups

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/opencontainers/cgroups"
	"go.podman.io/storage/pkg/unshare"
)

func TestCreated(t *testing.T) {
	// tests only works in rootless mode.
	if unshare.IsRootless() {
		return
	}

	var resources cgroups.Resources
	cgr, err := New("machine.slice", &resources)
	if err != nil {
		t.Fatal(err)
	}
	if err := cgr.Delete(); err != nil {
		t.Fatal(err)
	}

	cgr, err = NewSystemd("machine.slice", &resources)
	if err != nil {
		t.Fatal(err)
	}
	if err := cgr.Delete(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateFirstInvalidPaths(t *testing.T) {
	for _, paths := range [][]string{nil, {"/"}, {"/../outside"}} {
		if path, cleanup, err := CreateFirst(paths, nil); err == nil || path != "" || cleanup != nil {
			t.Fatalf("expected invalid paths %v to fail without ownership", paths)
		}
	}
}

func TestCreateFirst(t *testing.T) {
	if unshare.IsRootless() && os.Getenv("CGROUP_TEST_ROOTLESS") != "1" {
		t.Skip("set CGROUP_TEST_ROOTLESS=1 only in an isolated writable cgroup namespace")
	}

	name := fmt.Sprintf("/test-create-first-%d", os.Getpid())
	// cgroup.procs is not a directory, so the first candidate cannot be created.
	invalid := "/cgroup.procs" + name
	path, cleanup, err := CreateFirst([]string{invalid, "/cgroup.procs", name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != name || cleanup == nil {
		t.Fatalf("expected ownership of %q, got %q", name, path)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})

	path, reusedCleanup, err := CreateFirst([]string{name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != name || reusedCleanup != nil {
		t.Fatalf("expected reuse of %q without ownership, got %q", name, path)
	}
	for _, invalid := range []string{invalid, "/cgroup.procs"} {
		if path, cleanup, err := CreateFirst([]string{invalid}, nil); err == nil || path != "" || cleanup != nil {
			t.Fatalf("expected %q to fail without ownership", invalid)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cgroupRoot, name)); !os.IsNotExist(err) {
		t.Fatalf("owned cgroup was not removed: %v", err)
	}
}

func TestCreateFirstFailureCleanup(t *testing.T) {
	if unshare.IsRootless() && os.Getenv("CGROUP_TEST_ROOTLESS") != "1" {
		t.Skip("set CGROUP_TEST_ROOTLESS=1 only in an isolated writable cgroup namespace")
	}
	parent := filepath.Join(cgroupRoot, fmt.Sprintf("test-create-first-threaded-%d", os.Getpid()))
	threaded := filepath.Join(parent, "threaded")
	for _, path := range []string{parent, threaded} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(threaded, "cgroup.type"), []byte("threaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(parent, "candidate")
	_, cleanup, err := CreateFirst([]string{strings.TrimPrefix(candidate, cgroupRoot)}, &cgroups.Resources{Memory: 64 << 20})
	if err == nil || !strings.Contains(err.Error(), "domain controllers") || cleanup != nil {
		t.Fatalf("expected domain-controller failure without retained ownership, got %v", err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("failed candidate was not removed: %v", err)
	}
}

func TestResources(t *testing.T) {
	// tests only works in rootful mode.
	if unshare.IsRootless() {
		return
	}

	dev1 := cgroups.NewThrottleDevice(1, 3, 2097152)
	dev2 := cgroups.NewThrottleDevice(3, 10, 2097152)
	dev3 := cgroups.NewWeightDevice(5, 9, 500, 0)
	wtDevices := []*cgroups.WeightDevice{dev3}
	devices := []*cgroups.ThrottleDevice{dev1, dev2}

	var resources cgroups.Resources
	resources.CpuPeriod = 100000
	resources.CpuQuota = 100000
	resources.CpuShares = 100
	resources.CpusetCpus = "0"
	resources.CpusetMems = "0"
	resources.Memory = 900
	resources.MemorySwap = 1000
	resources.BlkioWeight = 300

	cgr, err := New("machine.slice", &resources)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cgr.Delete(); err != nil {
			t.Fatal(err)
		}
	}()

	// TestMode is used in the runc packages for unit tests, works without this as well here.
	TestMode = true
	err = cgr.Update(&resources)
	if err != nil {
		t.Fatal(err)
	}
	if cgr.config.CpuPeriod != 100000 || cgr.config.CpuQuota != 100000 {
		t.Fatal("Got the wrong value, set cpu.cfs_period_us failed.")
	}

	cgr2, err := NewSystemd("machine2.slice", &resources)
	if err != nil {
		t.Fatal(err)
	}

	// test CPU Quota adjustment.
	u, _, b, _, _, _ := resourcesToProps(&resources)

	val, ok := u["CPUQuotaPerSecUSec"]
	if !ok {
		t.Fatal("CPU Quota not parsed.")
	}
	if val != 1000000 {
		t.Fatal("CPU Quota incorrect value expected 1000000 got " + strconv.FormatUint(val, 10))
	}

	bits := new(big.Int)
	cpusetVal := bits.SetBit(bits, 0, 1).Bytes()

	cpus, ok := b["AllowedCPUs"]
	if !ok {
		t.Fatal("Cpuset Cpus not parsed.")
	}
	if !bytes.Equal(cpus, cpusetVal) {
		t.Fatal("Cpuset Cpus incorrect value expected " + string(cpusetVal) + " got " + string(cpus))
	}

	mems, ok := b["AllowedMemoryNodes"]
	if !ok {
		t.Fatal("Cpuset Mems not parsed.")
	}
	if !bytes.Equal(mems, cpusetVal) {
		t.Fatal("Cpuset Mems incorrect value expected " + string(cpusetVal) + " got " + string(mems))
	}

	err = os.Mkdir("/dev/foodevdir", os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll("/dev/foodevdir")

	c := exec.Command("mknod", "/dev/foodevdir/null", "b", "1", "3")
	c.Env = os.Environ()
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	err = c.Run()
	if err != nil {
		t.Fatal(err)
	}

	c = exec.Command("mknod", "/dev/foodevdir/bar", "b", "3", "10")
	c.Env = os.Environ()
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	err = c.Run()
	if err != nil {
		t.Fatal(err)
	}

	c = exec.Command("mknod", "/dev/foodevdir/bat", "b", "5", "9")
	c.Env = os.Environ()
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	err = c.Run()
	if err != nil {
		t.Fatal(err)
	}

	resources.BlkioThrottleReadBpsDevice = []*cgroups.ThrottleDevice{devices[0]}
	resources.BlkioThrottleWriteBpsDevice = []*cgroups.ThrottleDevice{devices[1]}
	resources.BlkioWeightDevice = wtDevices

	// machine.slice = parent, libpod_pod_ID = path
	err = cgr2.CreateSystemdUnit(fmt.Sprintf("%s/%s-%s%s", "machine2.slice", "machine2", "libpod_pod_12345", ".slice"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cgr2.Delete(); err != nil {
			t.Fatal(err)
		}
	}()
}
