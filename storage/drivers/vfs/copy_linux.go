package vfs

import driversCopy "go.podman.io/storage/drivers/copy"

func dirCopy(srcDir, dstDir string) error {
	return driversCopy.DirCopy(srcDir, dstDir, driversCopy.Content, true)
}
