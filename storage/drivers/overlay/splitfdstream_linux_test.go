//go:build linux

package overlay

import "go.podman.io/storage/internal/driver"

var _ driver.SplitDirFDStreamDriver = (*Driver)(nil)
