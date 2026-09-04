//go:build linux

package overlay

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"go.podman.io/storage/pkg/fileutils"
	"go.podman.io/storage/pkg/idtools"
	"go.podman.io/storage/pkg/splitfdstream"
	"golang.org/x/sys/unix"
)

const copyBufferSize = 1 << 20

// GetSplitDirFDStream generates a splitdirfdstream from the layer differences.
// The returned ReadCloser contains the stream bytes (a pipe or memfd), and
// the []*os.File slice contains directory file descriptors that
// FileBackedData chunks in the stream reference by index.
func (d *Driver) GetSplitDirFDStream(id, parent string, options *splitfdstream.GetSplitFDStreamOpts) (io.ReadCloser, []*os.File, error) {
	if options == nil {
		options = &splitfdstream.GetSplitFDStreamOpts{}
	}

	dir := d.dir(id)
	if err := fileutils.Exists(dir); err != nil {
		return nil, nil, fmt.Errorf("layer %s does not exist: %w", id, err)
	}

	composefsData := d.getComposefsData(id)
	composefsMountFd := -1
	if err := fileutils.Exists(composefsData); err == nil {
		fd, err := openComposefsMount(composefsData)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to mount composefs for layer %s: %w", id, err)
		}
		composefsMountFd = fd
		defer unix.Close(composefsMountFd)
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, nil, err
	}

	logrus.Debugf("overlay: GetSplitDirFDStream for layer %s with parent %s", id, parent)

	idMappings := options.IDMappings
	if idMappings == nil {
		idMappings = &idtools.IDMappings{}
	}

	diffPath, err := d.getDiffPath(id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get diff path for layer %s: %w", id, err)
	}

	tarStream, err := d.Diff(id, idMappings, parent, nil, options.MountLabel)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate diff for layer %s: %w", id, err)
	}
	defer tarStream.Close()

	streamFd, err := unix.MemfdCreate("splitdirfdstream", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, nil, fmt.Errorf("memfd_create: %w", err)
	}
	streamFile := os.NewFile(uintptr(streamFd), "splitdirfdstream")

	// Open the diff directory as a directory FD.
	diffDirFd, err := unix.Open(diffPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		streamFile.Close()
		return nil, nil, fmt.Errorf("failed to open diff directory %s: %w", diffPath, err)
	}
	diffDirFile := os.NewFile(uintptr(diffDirFd), diffPath)

	writer := splitfdstream.NewWriter(streamFile)

	// dirfdIndex 0 always refers to the diff directory.
	err = convertTarToSplitDirFDStream(tarStream, writer, diffDirFd, composefsMountFd)
	if err != nil {
		streamFile.Close()
		diffDirFile.Close()
		return nil, nil, fmt.Errorf("failed to convert tar to splitdirfdstream: %w", err)
	}

	if _, err := streamFile.Seek(0, io.SeekStart); err != nil {
		streamFile.Close()
		diffDirFile.Close()
		return nil, nil, fmt.Errorf("failed to seek stream: %w", err)
	}

	logrus.Debugf("overlay: GetSplitDirFDStream complete for layer %s", id)
	return streamFile, []*os.File{diffDirFile}, nil
}

// convertTarToSplitDirFDStream converts a tar stream to a splitdirfdstream.
// Files accessible in the diff directory are written as FileBackedData chunks
// referencing dirfd_index=0 by filename. Other content is inlined.
func convertTarToSplitDirFDStream(tarStream io.ReadCloser, writer *splitfdstream.SplitDirFDStreamWriter, diffDirFd int, composefsMountFd int) error {
	tr := tar.NewReader(tarStream)

	var buf []byte

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		var headerBuf bytes.Buffer
		tw := tar.NewWriter(&headerBuf)
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to serialize tar header for %s: %w", header.Name, err)
		}
		if err := writer.WriteMetadata(headerBuf.Bytes()); err != nil {
			return fmt.Errorf("failed to write tar header for %s: %w", header.Name, err)
		}

		if header.Typeflag == tar.TypeReg && header.Size > 0 {
			ok, err := tryWriteFileBackedData(writer, diffDirFd, composefsMountFd, header)
			if err != nil {
				return fmt.Errorf("failed to write FileBackedData for %s: %w", header.Name, err)
			}
			if ok {
				if _, err := io.CopyN(io.Discard, tr, header.Size); err != nil {
					return fmt.Errorf("failed to skip content for %s: %w", header.Name, err)
				}
			} else {
				if buf == nil {
					buf = make([]byte, copyBufferSize)
				}
				iw, err := writer.InlineDataWriter(header.Size)
				if err != nil {
					return fmt.Errorf("failed to write inline prefix for %s: %w", header.Name, err)
				}
				if _, err := io.CopyBuffer(iw, io.LimitReader(tr, header.Size), buf); err != nil {
					return fmt.Errorf("failed to write inline content for %s: %w", header.Name, err)
				}
			}

			// Tar entries are padded to 512-byte boundaries.  The tar
			// reader consumes this padding internally, so we must emit
			// it explicitly for the splitdirfdstream consumer.
			if paddingSize := (512 - (header.Size % 512)) % 512; paddingSize > 0 {
				if err := writer.WriteMetadata(make([]byte, paddingSize)); err != nil {
					return fmt.Errorf("failed to write content padding for %s: %w", header.Name, err)
				}
			}
		}
	}

	// Write the tar end-of-archive marker: two 512-byte zero blocks.
	// The tar reader consumes this internally, but the splitstream
	// consumer needs it to detect stream end without hitting EOF
	// mid-parse.
	if err := writer.WriteMetadata(make([]byte, 1024)); err != nil {
		return fmt.Errorf("failed to write end-of-archive marker: %w", err)
	}

	return nil
}

// resolveComposefsRedirect reads the trusted.overlay.redirect xattr from the
// composefs mount to get the flat storage path in the diff directory.
func resolveComposefsRedirect(composefsMountFd int, name string) (string, error) {
	cfd, err := unix.Openat2(composefsMountFd, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_PATH,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH,
	})
	if err != nil {
		return "", err
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Fgetxattr(cfd, "trusted.overlay.redirect", buf)
	unix.Close(cfd)
	if err != nil {
		return "", err
	}

	flatPath := string(buf[:n])
	if filepath.IsAbs(flatPath) || filepath.Clean("/"+flatPath) != "/"+flatPath {
		return "", fmt.Errorf("invalid redirect xattr value: %s", flatPath)
	}
	return flatPath, nil
}

// tryWriteFileBackedData attempts to reference a file in the diff directory
// as a FileBackedData chunk (dirfd_index=0, filename=path).
// Returns (true, nil) on success, (false, nil) if the file is not accessible.
func tryWriteFileBackedData(writer *splitfdstream.SplitDirFDStreamWriter, diffDirFd int, composefsMountFd int, header *tar.Header) (bool, error) {
	cleanName := filepath.Clean(header.Name)
	if filepath.Clean("/"+cleanName) != "/"+cleanName {
		return false, fmt.Errorf("invalid file path: %s", header.Name)
	}

	openPath := cleanName
	if composefsMountFd >= 0 {
		resolvedPath, err := resolveComposefsRedirect(composefsMountFd, cleanName)
		if err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENODATA) {
				return false, nil
			}
			return false, fmt.Errorf("failed to resolve composefs path for %s: %w", cleanName, err)
		}
		openPath = resolvedPath
	}

	// Verify the file exists and is accessible in the diff directory.
	fd, err := unix.Openat2(diffDirFd, openPath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_BENEATH,
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open %s: %w", cleanName, err)
	}

	var fdStat unix.Stat_t
	if err := unix.Fstat(fd, &fdStat); err != nil {
		unix.Close(fd)
		return false, fmt.Errorf("failed to fstat opened file %s: %w", cleanName, err)
	}
	unix.Close(fd)

	if fdStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, fmt.Errorf("file %s is not a regular file", cleanName)
	}
	if fdStat.Size != header.Size {
		return false, nil
	}

	if err := writer.WriteFileBackedData(header.Size, 0, openPath); err != nil {
		return false, fmt.Errorf("failed to write FileBackedData: %w", err)
	}

	return true, nil
}
