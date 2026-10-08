//go:build linux

package overlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/pkg/fileutils"
	"golang.org/x/sys/unix"
)

var _ graphdriver.SplitDirFDStreamDriver = (*Driver)(nil)

// layerFiles gives access to the files of one layer through the layer's
// diff directory.
type layerFiles struct {
	diffDir *os.File
	// flatNames maps a tar entry name to the name its content is stored
	// under, for a layer stored in the flat layout composefs uses.  nil
	// for a layer that stores files under their own names.
	flatNames map[string]string
}

// LayerFiles implements graphdriver.SplitDirFDStreamDriver.
func (d *Driver) LayerFiles(id string, flatNames map[string]string) (graphdriver.LayerFiles, error) {
	dir := d.dir(id)
	if err := fileutils.Exists(dir); err != nil {
		return nil, fmt.Errorf("layer %s does not exist: %w", id, err)
	}

	// Only a layer applied for composefs is stored in the flat layout; any
	// other layer, including one pulled partially without composefs, keeps
	// its files under their own names.
	if err := fileutils.Exists(d.getComposefsData(id)); err != nil {
		if !errors.Is(err, unix.ENOENT) {
			return nil, err
		}
		flatNames = nil
	}

	diffPath, err := d.getDiffPath(id)
	if err != nil {
		return nil, fmt.Errorf("getting diff path of layer %s: %w", id, err)
	}
	diffDir, err := os.Open(diffPath)
	if err != nil {
		return nil, fmt.Errorf("opening diff directory: %w", err)
	}
	return &layerFiles{
		diffDir:   diffDir,
		flatNames: flatNames,
	}, nil
}

// DirFDs implements graphdriver.LayerFiles.
func (f *layerFiles) DirFDs() []*os.File {
	return []*os.File{f.diffDir}
}

// Close implements graphdriver.LayerFiles.
func (f *layerFiles) Close() error {
	if f.diffDir == nil {
		return nil
	}
	err := f.diffDir.Close()
	f.diffDir = nil
	return err
}

// Lookup implements graphdriver.LayerFiles.  The diff directory is the
// only directory handed to the consumer, so the index is always 0.
func (f *layerFiles) Lookup(name string, size int64, onlyWorldReadable bool) (int, string, bool, error) {
	openPath := filepath.Clean(name)
	if f.flatNames != nil {
		flat, ok := f.flatNames[name]
		if !ok {
			return 0, "", false, nil
		}
		openPath = flat
	}

	fd, err := unix.Openat2(int(f.diffDir.Fd()), openPath, &unix.OpenHow{
		Flags:   unix.O_CLOEXEC | unix.O_PATH,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH,
	})
	if err != nil {
		// EXDEV: the name, an absolute one for example, does not stay
		// beneath the diff directory.
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
			return 0, "", false, nil
		}
		return 0, "", false, fmt.Errorf("opening %s: %w", name, err)
	}
	var st unix.Stat_t
	statErr := unix.Fstat(fd, &st)
	unix.Close(fd)
	if statErr != nil {
		return 0, "", false, fmt.Errorf("stat of %s: %w", name, statErr)
	}

	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size != size {
		return 0, "", false, nil
	}
	if onlyWorldReadable && st.Mode&unix.S_IROTH == 0 {
		return 0, "", false, nil
	}
	return 0, openPath, true, nil
}
