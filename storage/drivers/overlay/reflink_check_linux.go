//go:build linux

package overlay

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"go.podman.io/storage/pkg/fileutils"
)

func checkSupportsReflinks(home, runhome string) (bool, error) {
	const feature = "reflinks"

	reflinkCacheResult, _, err := cachedFeatureCheck(runhome, feature)
	if err == nil {
		logrus.Debugf("Cached reflink support: %v", reflinkCacheResult)
		return reflinkCacheResult, nil
	}

	supportsReflinks, err := testReflinkSupport(home)
	if err != nil {
		logrus.Debugf("overlay: reflink test failed: %v", err)
		supportsReflinks = false
	}
	logrus.Debugf("overlay: reflink support: %v", supportsReflinks)

	if err := cachedFeatureRecord(runhome, feature, supportsReflinks, ""); err != nil {
		return false, fmt.Errorf("recording reflink-support status: %w", err)
	}

	return supportsReflinks, nil
}

func testReflinkSupport(testDir string) (bool, error) {
	srcPath := filepath.Join(testDir, ".reflink-test-src")
	dstPath := filepath.Join(testDir, ".reflink-test-dst")
	defer os.Remove(srcPath)
	defer os.Remove(dstPath)

	srcFile, err := os.Create(srcPath)
	if err != nil {
		return false, fmt.Errorf("failed to create source file: %w", err)
	}

	testData := []byte("reflink test")
	if _, err := srcFile.Write(testData); err != nil {
		srcFile.Close()
		return false, fmt.Errorf("failed to write test data: %w", err)
	}

	if err := srcFile.Sync(); err != nil {
		srcFile.Close()
		return false, fmt.Errorf("failed to sync source file: %w", err)
	}
	srcFile.Close()

	srcFile, err = os.Open(srcPath)
	if err != nil {
		return false, fmt.Errorf("failed to reopen source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dstPath)
	if err != nil {
		return false, fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	err = fileutils.Reflink(srcFile, dstFile)
	if err != nil {
		logrus.Debugf("reflink test failed: %v", err)
		return false, nil
	}

	dstFile.Close()
	dstContent, err := os.ReadFile(dstPath)
	if err != nil {
		return false, fmt.Errorf("failed to read destination file: %w", err)
	}

	if string(dstContent) != string(testData) {
		return false, fmt.Errorf("reflink data mismatch")
	}

	return true, nil
}
