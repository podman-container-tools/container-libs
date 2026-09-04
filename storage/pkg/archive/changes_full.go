package archive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/system"
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

// collectFileInfoFull walks sourceDir completely via filepath.WalkDir,
// collecting stat and xattr info for every entry. Cross-device directories
// are skipped to avoid crossing mount boundaries.
func collectFileInfoFull(sourceDir string, idMappings *idtools.IDMappings) (*FileInfo, error) {
	root := newRootFileInfo(idMappings)

	sourceStat, err := system.Lstat(sourceDir)
	if err != nil {
		return nil, err
	}

	err = filepath.WalkDir(sourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		relPath = filepath.Join(string(os.PathSeparator), relPath)

		if relPath == string(os.PathSeparator) {
			return nil
		}

		parent := root.LookUp(filepath.Dir(relPath))
		if parent == nil {
			return fmt.Errorf("collectFileInfoFull: unexpectedly no parent for %s", relPath)
		}

		info := &FileInfo{
			name:       filepath.Base(relPath),
			children:   make(map[string]*FileInfo),
			parent:     parent,
			idMappings: idMappings,
		}

		s, err := system.Lstat(path)
		if err != nil {
			return err
		}

		if s.Dev() != sourceStat.Dev() && s.IsDir() {
			return filepath.SkipDir
		}

		info.stat = s

		if d.Type()&os.ModeSymlink != 0 {
			info.target, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}

		info.capability, err = system.Lgetxattr(path, "security.capability")
		if err != nil && !errors.Is(err, system.ENOTSUP) {
			return err
		}

		xattrs, err := system.Llistxattr(path)
		if err != nil && !errors.Is(err, system.ENOTSUP) {
			return err
		}
		for _, key := range xattrs {
			if strings.HasPrefix(key, "user.") {
				value, err := system.Lgetxattr(path, key)
				if err != nil {
					if errors.Is(err, system.E2BIG) {
						logrus.Errorf("archive: Skipping xattr for file %s since value is too big: %s", path, key)
						continue
					}
					return err
				}
				if info.xattrs == nil {
					info.xattrs = make(map[string]string)
				}
				info.xattrs[key] = string(value)
			}
		}

		parent.children[info.name] = info
		return nil
	})
	if err != nil {
		return nil, err
	}
	return root, nil
}
