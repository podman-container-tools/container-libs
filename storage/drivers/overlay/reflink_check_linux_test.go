//go:build linux

package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckSupportsReflinks(t *testing.T) {
	tmpHome := t.TempDir()
	tmpRunHome := t.TempDir()

	supported, err := checkSupportsReflinks(tmpHome, tmpRunHome)
	if err != nil {
		t.Fatalf("checkSupportsReflinks failed: %v", err)
	}

	cachedSupported, _, err := cachedFeatureCheck(tmpRunHome, "reflinks")
	if err != nil {
		t.Fatalf("cachedFeatureCheck failed: %v", err)
	}

	if supported != cachedSupported {
		t.Errorf("cached result mismatch: got %v, want %v", cachedSupported, supported)
	}

	supported2, err := checkSupportsReflinks(tmpHome, tmpRunHome)
	if err != nil {
		t.Fatalf("checkSupportsReflinks second call failed: %v", err)
	}

	if supported != supported2 {
		t.Errorf("second call result mismatch: got %v, want %v", supported2, supported)
	}
}

func TestTestReflinkSupport(t *testing.T) {
	tmpDir := t.TempDir()

	supported, err := testReflinkSupport(tmpDir)
	if err != nil {
		t.Logf("testReflinkSupport error (may be expected on non-reflink filesystem): %v", err)
	}

	srcPath := filepath.Join(tmpDir, ".reflink-test-src")
	dstPath := filepath.Join(tmpDir, ".reflink-test-dst")

	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("source file was not cleaned up: %v", err)
	}
	if _, err := os.Stat(dstPath); !os.IsNotExist(err) {
		t.Errorf("destination file was not cleaned up: %v", err)
	}

	t.Logf("Reflink support on this filesystem: %v", supported)
}
