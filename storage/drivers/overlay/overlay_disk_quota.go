//go:build linux && cgo && !exclude_disk_quota

package overlay

import (
	"errors"
	"path"

	"go.podman.io/storage/pkg/directory"
)

// ReadWriteDiskUsage returns the disk usage of the writable directory for the ID.
// For Overlay, it attempts to check the XFS quota for size, and falls back to
// finding the size of the "diff" directory.
func (d *Driver) ReadWriteDiskUsage(id string) (*directory.DiskUsage, error) {
	var quotaErr error
	if d.quotaCtl != nil {
		usage := &directory.DiskUsage{}
		quotaErr = d.quotaCtl.GetDiskUsage(d.dir(id), usage)
		if quotaErr == nil {
			return usage, nil
		}
	}

	usage, diffErr := directory.Usage(path.Join(d.dir(id), "diff"))
	if diffErr != nil {
		return usage, errors.Join(quotaErr, diffErr)
	}

	return usage, nil
}
