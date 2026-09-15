//go:build !race

package btrfs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
)

// dependencies: btrfs-progs, libbtrfs-dev
// permission: root, loop device
func TestSubVolDelete(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("test requires root")
	}

	// Helpers
	runCmd := func(name string, arg ...string) error {
		cmd := exec.Command(name, arg...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("command '%s %s' failed: %v, stderr: %s", name, strings.Join(arg, " "), err, stderr.String())
		}
		return nil
	}
	qgroupShow := func(mountPath string) string {
		cmd := exec.Command("btrfs", "qgroup", "show", mountPath)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Run()
		return out.String()
	}

	// parepare btrfs using loop device
	baseDir, err := os.MkdirTemp("/mnt", "btrfs-test-")
	if err != nil {
		t.Fatalf("Failed to create base temp dir: %v", err)
	}
	defer os.RemoveAll(baseDir)

	mnt := path.Join(baseDir, "mountpoint")
	if err := os.Mkdir(mnt, 0o755); err != nil {
		t.Fatalf("Failed to create mountpoint dir: %v", err)
	}

	blockFile := path.Join(baseDir, "btrfs.img")
	if err := runCmd("dd", "if=/dev/zero", "of="+blockFile, "bs=1M", "count=120", "status=none"); err != nil {
		t.Skipf("Failed to create block file: %v", err)
	}

	if err := runCmd("mkfs.btrfs", "-f", blockFile); err != nil {
		t.Skipf("Failed to format to btrfs: %v", err)
	}

	findLoopCmd := exec.Command("losetup", "-f")
	var loopDevBytes bytes.Buffer
	findLoopCmd.Stdout = &loopDevBytes
	if err := findLoopCmd.Run(); err != nil {
		t.Skipf("Failed to find a loop device with 'losetup -f': %v", err)
	}
	loopDev := strings.TrimSpace(loopDevBytes.String())
	if loopDev == "" {
		t.Skip("No available loop devices found")
	}

	if err := runCmd("losetup", loopDev, blockFile); err != nil {
		t.Skipf("Failed to setup loop device with 'losetup %s %s': %v", loopDev, blockFile, err)
	}
	defer runCmd("losetup", "-d", loopDev)

	if err := runCmd("mount", loopDev, mnt); err != nil {
		t.Skipf("Failed to mount loop device %s: %v", loopDev, err)
	}
	defer runCmd("umount", mnt)

	d := &Driver{home: mnt}
	if err := d.enableQuota(); err != nil {
		t.Skipf("Failed to enable qgroup using API: %v", err)
	}

	t.Run("subVolDelete", func(t *testing.T) {
		subvolName := "subvol1"
		subvolPath := path.Join(mnt, subvolName)
		if err := subvolCreate(mnt, subvolName); err != nil {
			t.Fatalf("Failed to create subvolume using API: %v", err)
		}

		if err := d.subvolRescanQuota(); err != nil {
			t.Fatalf("Failed to rescan quota using API: %v", err)
		}

		qtreeid, err := subvolLookupQgroup(subvolPath)
		if err != nil {
			t.Fatalf("Failed to lookup qgroup for subvolume using API: %v", err)
		}
		qgroupID := fmt.Sprintf("0/%d", qtreeid)
		t.Logf("subvolume %s has qgroup ID %s", subvolPath, qgroupID)

		if !strings.Contains(qgroupShow(mnt), qgroupID) {
			t.Fatalf("qgroup %s was not created for subvolume %s", qgroupID, subvolPath)
		}

		if err := subvolDelete(mnt, subvolName, true); err != nil {
			t.Fatalf("Failed to delete subvolume using API: %v", err)
		}

		if err := d.subvolRescanQuota(); err != nil {
			t.Fatalf("Failed to rescan quota after delete using API: %v", err)
		}

		qgroupInfo := qgroupShow(mnt)
		t.Logf("Current qgroup info:\n%s", qgroupInfo)

		if strings.Contains(qgroupInfo, qgroupID) && !strings.Contains(qgroupInfo, "under deletion") {
			t.Fatalf("qgroup %s was not marked as 'under deletion' for subvolume %s", qgroupID, subvolPath)
		}

		runCmd("btrfs", "qgroup", "clear-stale", mnt)

		qgroupInfo = qgroupShow(mnt)
		t.Logf("Current qgroup info after clearing stale:\n%s", qgroupInfo)

		if strings.Contains(qgroupInfo, qgroupID) {
			t.Fatalf("qgroup %s was not deleted for subvolume %s", qgroupID, subvolPath)
		}
	})
}
