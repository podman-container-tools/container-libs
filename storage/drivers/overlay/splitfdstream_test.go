//go:build linux

package overlay

import (
	"testing"

	"go.podman.io/storage/pkg/splitfdstream"
)

func TestGetSplitDirFDStreamStub(t *testing.T) {
	driver := &Driver{
		home: t.TempDir(),
	}

	_, _, err := driver.GetSplitDirFDStream("test-layer", "parent-layer", nil)
	if err == nil {
		t.Error("Expected error with nil options")
	}

	opts := &splitfdstream.GetSplitFDStreamOpts{}
	_, _, err = driver.GetSplitDirFDStream("non-existent-layer", "parent-layer", opts)
	if err == nil {
		t.Error("Expected error for non-existent layer")
	}
}

func TestOverlayImplementsSplitDirFDStreamDriver(t *testing.T) {
	driver := &Driver{}

	if _, ok := any(driver).(splitfdstream.SplitDirFDStreamDriver); !ok {
		t.Error("Expected overlay driver to implement SplitDirFDStreamDriver interface")
	}
}
