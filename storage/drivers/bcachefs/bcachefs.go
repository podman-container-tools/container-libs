//go:build linux

package bcachefs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"github.com/opencontainers/selinux/go-selinux/label"
	"github.com/sirupsen/logrus"
	graphdriver "go.podman.io/storage/drivers"
	"go.podman.io/storage/internal/tempdir"
	"go.podman.io/storage/pkg/archive"
	"go.podman.io/storage/pkg/directory"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/ioutils"
	"go.podman.io/storage/pkg/mount"
	"go.podman.io/storage/pkg/system"
	"golang.org/x/sys/unix"
)

const (
	defaultPerms = os.FileMode(0o555)

	// _IOW(0xbc, 16, struct bch_ioctl_subvolume) - struct size 32 bytes
	bchIoctlSubvolumeCreate = 0x4020bc10
	// _IOW(0xbc, 17, struct bch_ioctl_subvolume)
	bchIoctlSubvolumeDestroy = 0x4020bc11

	bchSubvolSnapshotCreate = 1 << 0

	// subvolCreateMode is the initial mode for subvolume creation, masked by umask in kernel
	subvolCreateMode = 0o777
)

// bchIoctlSubvolume matches struct bch_ioctl_subvolume from libbcachefs/bcachefs_ioctl.h.
// The Dirfd field is int32 to accommodate AT_FDCWD (-100).
type bchIoctlSubvolume struct {
	Flags  uint32
	Dirfd  int32
	Mode   uint16
	Pad    [3]uint16
	DstPtr uint64
	SrcPtr uint64
}

func init() {
	graphdriver.MustRegister("bcachefs", Init)
}

// Driver implements graphdriver.ProtoDriver for bcachefs filesystems.
type Driver struct {
	home string
}

// diffDriver wraps NaiveDiffDriver to override Diff and Changes with
// implementations that don't use inode-based pruning. Bcachefs subvolumes
// share st_dev and COW snapshots share inode numbers, which causes the
// Linux inode-pruning optimization in archive.ChangesDirs to skip modified
// subtrees entirely.
type diffDriver struct {
	graphdriver.Driver
	proto *Driver
}

// Init returns a new bcachefs driver.
// An error is returned if the home directory is not on a bcachefs filesystem.
func Init(home string, options graphdriver.Options) (graphdriver.Driver, error) {
	fsMagic, err := graphdriver.GetFSMagic(home)
	if err != nil {
		return nil, err
	}

	if fsMagic != graphdriver.FsMagicBcachefs {
		return nil, fmt.Errorf("%q is not on a bcachefs filesystem: %w", home, graphdriver.ErrPrerequisites)
	}

	if err := os.MkdirAll(filepath.Join(home, "subvolumes"), 0o700); err != nil {
		return nil, err
	}

	if err := mount.MakePrivate(home); err != nil {
		return nil, err
	}

	driver := &Driver{
		home: home,
	}

	naive := graphdriver.NewNaiveDiffDriver(driver, graphdriver.NewNaiveLayerIDMapUpdater(driver))
	return &diffDriver{Driver: naive, proto: driver}, nil
}

// subvolCreate creates a new bcachefs subvolume at the specified absolute path.
func subvolCreate(dstPath string) error {
	if dstPath == "" {
		return fmt.Errorf("destination path cannot be empty")
	}

	parentDir := filepath.Dir(dstPath)

	fd, err := unix.Open(parentDir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("failed to open parent directory %s: %w", parentDir, err)
	}
	defer unix.Close(fd)

	dstBytes := append([]byte(dstPath), 0)

	args := bchIoctlSubvolume{
		Flags:  0,
		Dirfd:  unix.AT_FDCWD,
		Mode:   subvolCreateMode,
		DstPtr: uint64(uintptr(unsafe.Pointer(&dstBytes[0]))),
		SrcPtr: 0,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), bchIoctlSubvolumeCreate,
		uintptr(unsafe.Pointer(&args)))
	runtime.KeepAlive(dstBytes)
	if errno != 0 {
		return fmt.Errorf("failed to create bcachefs subvolume %s: %w", dstPath, errno)
	}
	return nil
}

// subvolSnapshot creates a read-write snapshot of srcPath at dstPath.
// Both paths must be absolute paths on the same bcachefs filesystem.
func subvolSnapshot(srcPath, dstPath string) error {
	if srcPath == "" {
		return fmt.Errorf("source path cannot be empty")
	}
	if dstPath == "" {
		return fmt.Errorf("destination path cannot be empty")
	}

	parentDir := filepath.Dir(dstPath)

	fd, err := unix.Open(parentDir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("failed to open parent directory %s: %w", parentDir, err)
	}
	defer unix.Close(fd)

	dstBytes := append([]byte(dstPath), 0)
	srcBytes := append([]byte(srcPath), 0)

	args := bchIoctlSubvolume{
		Flags:  bchSubvolSnapshotCreate,
		Dirfd:  unix.AT_FDCWD,
		Mode:   subvolCreateMode,
		DstPtr: uint64(uintptr(unsafe.Pointer(&dstBytes[0]))),
		SrcPtr: uint64(uintptr(unsafe.Pointer(&srcBytes[0]))),
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), bchIoctlSubvolumeCreate,
		uintptr(unsafe.Pointer(&args)))
	runtime.KeepAlive(dstBytes)
	runtime.KeepAlive(srcBytes)
	if errno != 0 {
		return fmt.Errorf("failed to create bcachefs snapshot from %s to %s: %w", srcPath, dstPath, errno)
	}
	return nil
}

// subvolDelete deletes a bcachefs subvolume.
func subvolDelete(dirpath, name string) error {
	if name == "" {
		return fmt.Errorf("subvolume name cannot be empty")
	}

	fullPath := filepath.Join(dirpath, name)

	fd, err := unix.Open(dirpath, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("failed to open directory %s: %w", dirpath, err)
	}
	defer unix.Close(fd)

	fullPathBytes := append([]byte(fullPath), 0)

	args := bchIoctlSubvolume{
		Flags:  0,
		Dirfd:  unix.AT_FDCWD,
		Mode:   subvolCreateMode,
		DstPtr: uint64(uintptr(unsafe.Pointer(&fullPathBytes[0]))),
		SrcPtr: 0,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), bchIoctlSubvolumeDestroy,
		uintptr(unsafe.Pointer(&args)))
	runtime.KeepAlive(fullPathBytes)
	if errno != 0 {
		return fmt.Errorf("failed to destroy bcachefs subvolume %s: %w", fullPath, errno)
	}
	return nil
}

func (d *Driver) subvolumesDir() string {
	return filepath.Join(d.home, "subvolumes")
}

func (d *Driver) subvolumesDirID(id string) string {
	return filepath.Join(d.subvolumesDir(), id)
}

// String returns the driver name.
func (d *Driver) String() string {
	return "bcachefs"
}

// Status returns current driver information in a two dimensional string array.
func (d *Driver) Status() [][2]string {
	return [][2]string{}
}

// Metadata returns empty metadata for this driver.
func (d *Driver) Metadata(id string) (map[string]string, error) {
	return nil, nil
}

// Cleanup unmounts the home directory.
func (d *Driver) Cleanup() error {
	return mount.Unmount(d.home)
}

// CreateFromTemplate creates a layer with the same contents and parent as another layer.
func (d *Driver) CreateFromTemplate(id, template string, templateIDMappings *idtools.IDMappings, parent string, parentIDMappings *idtools.IDMappings, opts *graphdriver.CreateOpts, readWrite bool) error {
	return d.Create(id, template, opts)
}

// CreateReadWrite creates a layer that is writable for use as a container file system.
func (d *Driver) CreateReadWrite(id, parent string, opts *graphdriver.CreateOpts) error {
	return d.Create(id, parent, opts)
}

// Create creates a new layer with the given id, using parent as the parent layer.
// If parent is empty, a new subvolume is created; otherwise a snapshot of parent is created.
func (d *Driver) Create(id, parent string, opts *graphdriver.CreateOpts) error {
	subvolumes := d.subvolumesDir()
	if err := os.MkdirAll(subvolumes, 0o700); err != nil {
		return err
	}

	if parent == "" {
		if err := subvolCreate(filepath.Join(subvolumes, id)); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(subvolumes, id), defaultPerms); err != nil {
			return err
		}
	} else {
		parentDir := d.subvolumesDirID(parent)
		st, err := os.Stat(parentDir)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("%s: not a directory", parentDir)
		}
		if err := subvolSnapshot(parentDir, filepath.Join(subvolumes, id)); err != nil {
			return err
		}
	}

	mountLabel := ""
	if opts != nil {
		mountLabel = opts.MountLabel
	}

	return label.Relabel(filepath.Join(subvolumes, id), mountLabel, false)
}

// Remove removes the layer with the given id.
func (d *Driver) Remove(id string) error {
	dir := d.subvolumesDirID(id)
	if _, err := os.Stat(dir); err != nil {
		return err
	}

	if err := subvolDelete(d.subvolumesDir(), id); err != nil {
		return err
	}
	if err := system.EnsureRemoveAll(dir); err != nil {
		return err
	}
	return nil
}

// Get returns the mountpoint for the layered filesystem referred to by id.
func (d *Driver) Get(id string, options graphdriver.MountOpts) (string, error) {
	dir := d.subvolumesDirID(id)
	st, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	for _, opt := range options.Options {
		if opt == "ro" {
			continue
		}
		return "", fmt.Errorf("bcachefs driver does not support mount options")
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s: not a directory", dir)
	}

	return dir, nil
}

// Put is a no-op for bcachefs as there are no runtime resources to release.
func (d *Driver) Put(id string) error {
	return nil
}

// ReadWriteDiskUsage returns the disk usage of the writable directory for the ID.
func (d *Driver) ReadWriteDiskUsage(id string) (*directory.DiskUsage, error) {
	return directory.Usage(d.subvolumesDirID(id))
}

// Exists checks if the id exists in the filesystem.
func (d *Driver) Exists(id string) bool {
	dir := d.subvolumesDirID(id)
	_, err := os.Stat(dir)
	return err == nil
}

// ListLayers returns a list of all layer ids managed by this driver.
func (d *Driver) ListLayers() ([]string, error) {
	entries, err := os.ReadDir(d.subvolumesDir())
	if err != nil {
		return nil, err
	}
	results := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		results = append(results, entry.Name())
	}
	return results, nil
}

// AdditionalImageStores returns additional image stores supported by the driver.
func (d *Driver) AdditionalImageStores() []string {
	return nil
}

// Dedup performs deduplication of the driver's storage.
func (d *Driver) Dedup(req graphdriver.DedupArgs) (graphdriver.DedupResult, error) {
	return graphdriver.DedupResult{}, nil
}

// DeferredRemove is not implemented.
// It calls Remove directly.
func (d *Driver) DeferredRemove(id string) (tempdir.CleanupTempDirFunc, error) {
	return nil, d.Remove(id)
}

// GetTempDirRootDirs is not implemented.
func (d *Driver) GetTempDirRootDirs() []string {
	return []string{}
}

// SyncMode returns the sync mode configured for the driver.
func (d *Driver) SyncMode() graphdriver.SyncMode {
	return graphdriver.SyncModeNone
}

// driverPut calls Put on the driver and handles error accumulation.
// Mirrors the unexported driverPut in drivers/driver.go.
func driverPut(driver graphdriver.ProtoDriver, id string, mainErr *error) {
	if err := driver.Put(id); err != nil {
		err = fmt.Errorf("unmounting layer %s: %w", id, err)
		if *mainErr == nil {
			*mainErr = err
		} else {
			logrus.Errorf(err.Error())
		}
	}
}

// Diff produces an archive of the changes between the specified layer and its
// parent layer. Uses ChangesDirsFull instead of ChangesDirs to avoid the
// inode-based pruning that incorrectly skips modified subtrees on bcachefs.
func (d *diffDriver) Diff(id string, idMappings *idtools.IDMappings, parent string, parentMappings *idtools.IDMappings, mountLabel string) (arch io.ReadCloser, err error) {
	startTime := time.Now()
	driver := d.proto

	if idMappings == nil {
		idMappings = &idtools.IDMappings{}
	}
	if parentMappings == nil {
		parentMappings = &idtools.IDMappings{}
	}

	options := graphdriver.MountOpts{
		MountLabel: mountLabel,
		Options:    []string{"ro"},
	}
	layerFs, err := driver.Get(id, options)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			driverPut(driver, id, &err)
		}
	}()

	if parent == "" {
		a, err := archive.TarWithOptions(layerFs, &archive.TarOptions{
			Compression: archive.Uncompressed,
			UIDMaps:     idMappings.UIDs(),
			GIDMaps:     idMappings.GIDs(),
		})
		if err != nil {
			return nil, err
		}
		return ioutils.NewReadCloserWrapper(a, func() error {
			err := a.Close()
			driverPut(driver, id, &err)
			return err
		}), nil
	}

	parentFs, err := driver.Get(parent, options)
	if err != nil {
		return nil, err
	}
	defer driverPut(driver, parent, &err)

	changes, err := archive.ChangesDirsFull(layerFs, idMappings, parentFs, parentMappings)
	if err != nil {
		return nil, err
	}

	a, err := archive.ExportChanges(layerFs, changes, idMappings.UIDs(), idMappings.GIDs())
	if err != nil {
		return nil, err
	}

	return ioutils.NewReadCloserWrapper(a, func() error {
		err := a.Close()
		driverPut(driver, id, &err)

		// NaiveDiffDriver compares file metadata with parent layers. Parent layers
		// are extracted from tar's with full second precision on modified time.
		// We need this hack here to make sure calls within same second receive
		// correct result.
		time.Sleep(time.Until(startTime.Truncate(time.Second).Add(time.Second)))
		return err
	}), nil
}

// Changes produces a list of changes between the specified layer and its parent
// layer. Uses ChangesDirsFull to avoid inode-based pruning.
func (d *diffDriver) Changes(id string, idMappings *idtools.IDMappings, parent string, parentMappings *idtools.IDMappings, mountLabel string) (_ []archive.Change, retErr error) {
	driver := d.proto

	if idMappings == nil {
		idMappings = &idtools.IDMappings{}
	}
	if parentMappings == nil {
		parentMappings = &idtools.IDMappings{}
	}

	options := graphdriver.MountOpts{
		MountLabel: mountLabel,
	}
	layerFs, err := driver.Get(id, options)
	if err != nil {
		return nil, err
	}
	defer driverPut(driver, id, &retErr)

	parentFs := ""

	if parent != "" {
		options := graphdriver.MountOpts{
			MountLabel: mountLabel,
			Options:    []string{"ro"},
		}
		parentFs, err = driver.Get(parent, options)
		if err != nil {
			return nil, err
		}
		defer driverPut(driver, parent, &retErr)
	}

	return archive.ChangesDirsFull(layerFs, idMappings, parentFs, parentMappings)
}
