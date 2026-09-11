//go:build linux

package ioutils

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// SyncDirectoryContents flushes file data and directory metadata under dir to
// physical storage. Call this before atomically renaming a fully populated
// staging directory to its final location.
func SyncDirectoryContents(dir string) error {
	var dirs []string

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}

		// Only regular files have data worth fdatasync-ing. Symlinks,
		// device nodes, FIFOs, and sockets carry no separate file
		// content: their state lives entirely in directory-entry /
		// inode metadata, which is already covered once the parent
		// directory is fsync'd below. Treating them like regular
		// files is actively wrong: os.Open on a symlink dereferences
		// it, so a symlink whose target does not happen to resolve
		// from inside the staging tree (e.g. Debian's
		// /etc/alternatives/*, or any link into a lower/not-yet-
		// materialized layer) fails the whole sync with ENOENT, and
		// opening a FIFO can block indefinitely waiting for a reader.
		if !d.Type().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}

		syncErr := unix.Fdatasync(int(f.Fd()))
		closeErr := f.Close()
		if syncErr != nil {
			return syncErr
		}

		return closeErr
	})
	if err != nil {
		return fmt.Errorf("sync directory contents in %q: %w", dir, err)
	}

	for i := len(dirs) - 1; i >= 0; i-- {
		dfd, err := os.Open(dirs[i])
		if err != nil {
			return fmt.Errorf("open directory %q for sync: %w", dirs[i], err)
		}

		syncErr := unix.Fsync(int(dfd.Fd()))
		closeErr := dfd.Close()
		if syncErr != nil {
			return fmt.Errorf("sync directory %q: %w", dirs[i], syncErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close directory %q after sync: %w", dirs[i], closeErr)
		}
	}

	return nil
}
