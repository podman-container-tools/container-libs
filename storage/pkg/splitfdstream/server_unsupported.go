//go:build !linux

package splitfdstream

import (
	"fmt"
	"net"
)

// VarlinkServer is not supported on this platform.
type VarlinkServer struct{}

// NewVarlinkServer is not supported on this platform.
func NewVarlinkServer(driverFunc DriverFunc, store Store) (*VarlinkServer, error) {
	return nil, fmt.Errorf("VarlinkServer is not supported on this platform")
}

// HandleConnection is not supported on this platform.
func (s *VarlinkServer) HandleConnection(conn *net.UnixConn) {
	panic("VarlinkServer is not supported on this platform")
}

// Stop is not supported on this platform.
func (s *VarlinkServer) Stop() error {
	return fmt.Errorf("VarlinkServer is not supported on this platform")
}

// CreateSocketPair is not supported on this platform.
func CreateSocketPair() (*net.UnixConn, *net.UnixConn, error) {
	return nil, nil, fmt.Errorf("CreateSocketPair is not supported on this platform")
}
