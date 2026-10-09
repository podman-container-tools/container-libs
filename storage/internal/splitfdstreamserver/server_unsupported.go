//go:build !linux

package splitfdstreamserver

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("splitfdstream is not supported on this platform")

// VarlinkServer is not supported on this platform.
type VarlinkServer struct{}

// NewVarlinkServer is not supported on this platform.
func NewVarlinkServer() (*VarlinkServer, error) {
	return nil, errUnsupported
}

// NewConnection is not supported on this platform.
func (s *VarlinkServer) NewConnection(backend StoreBackend) (*os.File, error) {
	return nil, errUnsupported
}

// Stop is not supported on this platform.
func (s *VarlinkServer) Stop() {}
