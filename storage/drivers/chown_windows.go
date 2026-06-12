//go:build windows

package graphdriver

import (
	"os"
	"sync"
	"syscall"

	"go.podman.io/storage/pkg/idtools"
)

type platformChowner struct{ modifiedDirectories sync.Map }

func newLChowner() *platformChowner {
	return &platformChowner{}
}

func (c *platformChowner) LChown(path string, info os.FileInfo, toHost, toContainer *idtools.IDMappings) error {
	return &os.PathError{Op: "lchown", Path: path, Err: syscall.EWINDOWS}
}
