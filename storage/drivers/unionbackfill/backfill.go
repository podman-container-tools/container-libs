package unionbackfill

import (
	"archive/tar"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.podman.io/storage/pkg/archive"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/system"
)

// NewBackfiller returns a helper that can synthesize metadata for directories
// which exist only in lower layers.
func NewBackfiller(idmap *idtools.IDMappings, lowerDiffDirs []string) *backfiller {
	if idmap != nil {
		uidMaps, gidMaps := idmap.UIDs(), idmap.GIDs()
		if len(uidMaps) > 0 || len(gidMaps) > 0 {
			idmap = idtools.NewIDMappingsFromMaps(append([]idtools.IDMap{}, uidMaps...), append([]idtools.IDMap{}, gidMaps...))
		}
	}
	return &backfiller{
		idmap:         idmap,
		lowerDiffDirs: append([]string{}, lowerDiffDirs...),
	}
}

type backfiller struct {
	idmap         *idtools.IDMappings
	lowerDiffDirs []string
}

// Backfill returns a tar header for pathname if it exists in a lower layer.
func (b *backfiller) Backfill(pathname string) (*tar.Header, error) {
	for _, lowerDiffDir := range b.lowerDiffDirs {
		candidate := filepath.Join(lowerDiffDir, pathname)
		if st, err := os.Lstat(candidate); err == nil {
			var linkTarget string
			if st.Mode()&fs.ModeType == fs.ModeSymlink {
				target, err := os.Readlink(candidate)
				if err != nil {
					return nil, err
				}
				linkTarget = target
			}
			hdr, err := tar.FileInfoHeader(st, linkTarget)
			if err != nil {
				return nil, err
			}
			hdr.Name = strings.Trim(filepath.ToSlash(pathname), "/")
			if st.Mode()&fs.ModeType == fs.ModeDir {
				hdr.Name += "/"
			}
			if b.idmap != nil && !b.idmap.Empty() {
				if uid, gid, err := b.idmap.ToContainer(idtools.IDPair{UID: hdr.Uid, GID: hdr.Gid}); err == nil {
					hdr.Uid, hdr.Gid = uid, gid
				}
			}
			return hdr, nil
		}

		// If this path is hidden by an opaque directory in this lower, stop here.
		p := strings.Trim(pathname, "/")
		subpathname := ""
		for {
			dir, subdir := filepath.Split(p)
			dir = strings.Trim(dir, "/")
			if dir == p {
				break
			}

			xval, err := system.Lgetxattr(filepath.Join(lowerDiffDir, dir), archive.GetOverlayXattrName("opaque"))
			if err == nil && len(xval) == 1 && xval[0] == 'y' {
				return nil, nil
			}
			if _, err := os.Stat(filepath.Join(lowerDiffDir, dir, archive.WhiteoutOpaqueDir)); err == nil {
				return nil, nil
			}

			subpathname = strings.Trim(path.Join(subdir, subpathname), "/")
			xval, err = system.Lgetxattr(filepath.Join(lowerDiffDir, dir), archive.GetOverlayXattrName("redirect"))
			if err == nil && len(xval) > 0 {
				subdir := string(xval)
				if path.IsAbs(subdir) {
					pathname = path.Join(subdir, subpathname)
				} else {
					parent, _ := filepath.Split(dir)
					parent = strings.Trim(parent, "/")
					pathname = path.Join(parent, subdir, subpathname)
				}
				break
			}

			p = dir
		}
	}

	return nil, nil
}
