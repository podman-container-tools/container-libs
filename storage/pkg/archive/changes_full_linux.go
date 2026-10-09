package archive

import (
	"io/fs"
	"os"
	"syscall"

	"go.podman.io/storage/pkg/idtools"
)

// ChangesDirsFull compares two directories without inode-based pruning.
// Use this instead of ChangesDirs for COW filesystems (like bcachefs) where
// snapshots share inode numbers and device IDs across subvolumes, which
// causes the Linux inode-pruning optimization in ChangesDirs to incorrectly
// skip modified subtrees.
func ChangesDirsFull(newDir string, newMappings *idtools.IDMappings, oldDir string, oldMappings *idtools.IDMappings) ([]Change, error) {
	if oldDir == "" {
		emptyDir, err := os.MkdirTemp("", "empty")
		if err != nil {
			return nil, err
		}
		defer os.Remove(emptyDir)
		oldDir = emptyDir
	}

	var (
		oldRoot, newRoot *FileInfo
		err1, err2       error
		errs             = make(chan error, 2)
	)
	go func() {
		oldRoot, err1 = collectFileInfoFull(oldDir, oldMappings)
		errs <- err1
	}()
	go func() {
		newRoot, err2 = collectFileInfoFull(newDir, newMappings)
		errs <- err2
	}()

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			return nil, err
		}
	}

	return newRoot.Changes(oldRoot), nil
}

// collectFileInfoFull registers every entry below sourceDir with the returned
// tree. Directories on another device are not descended into, so a mount point
// inside the layer contributes only itself.
func collectFileInfoFull(sourceDir string, idMappings *idtools.IDMappings) (*FileInfo, error) {
	// WARNING: This is called in contexts where the contents of sourceDir may be maliciously
	// concurrently modified.

	root, err := os.OpenRoot(sourceDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	rootFI := newRootFileInfo(idMappings)

	sourceStat, err := root.Lstat(".")
	if err != nil {
		return nil, err
	}
	sourceDev := sourceStat.Sys().(*syscall.Stat_t).Dev

	err = fs.WalkDir(root.FS(), ".", func(fsPath string, _ fs.DirEntry, err error) error {
		if err != nil || fsPath == "." {
			return err
		}

		fi, err := root.Lstat(fsPath)
		if err != nil {
			return err
		}
		if fi.IsDir() && fi.Sys().(*syscall.Stat_t).Dev != sourceDev {
			return fs.SkipDir
		}

		return walkchunk(root, fsPath, fi, rootFI)
	})
	if err != nil {
		return nil, err
	}
	return rootFI, nil
}
