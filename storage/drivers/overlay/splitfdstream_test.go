//go:build linux

package overlay

import (
	"testing"

	"go.podman.io/storage/pkg/splitfdstream"
)

func TestGetSplitFDStreamStub(t *testing.T) {
	driver := &Driver{
		home: t.TempDir(),
	}

	// Test with nil options
	_, _, err := driver.GetSplitFDStream("test-layer", "parent-layer", nil)
	if err == nil {
		t.Error("Expected error with nil options")
	}

	// Test with valid options but non-existent layer
	opts := &splitfdstream.GetSplitFDStreamOpts{}
	_, _, err = driver.GetSplitFDStream("non-existent-layer", "parent-layer", opts)
	if err == nil {
		t.Error("Expected error for non-existent layer")
	}
}

// TestOverlayImplementsSplitFDStreamDriver verifies that the overlay driver
// implements the SplitFDStreamDriver interface via type assertion.
func TestOverlayImplementsSplitFDStreamDriver(t *testing.T) {
	driver := &Driver{}

	// Verify the driver implements SplitFDStreamDriver
	if _, ok := interface{}(driver).(splitfdstream.SplitFDStreamDriver); !ok {
		t.Error("Expected overlay driver to implement SplitFDStreamDriver interface")
	}
}
