//go:build !linux

package splitfdstream

import (
	"fmt"
	"net"
	"os"
)

// JSONRPCServer is not supported on this platform.
type JSONRPCServer struct{}

// NewJSONRPCServer creates a new JSON-RPC server stub for unsupported platforms.
func NewJSONRPCServer(driverFunc DriverFunc, store Store) *JSONRPCServer {
	return &JSONRPCServer{}
}

// HandleConnection is not supported on this platform.
func (s *JSONRPCServer) HandleConnection(conn *net.UnixConn) {
	panic("JSONRPCServer is not supported on this platform")
}

// Start is not supported on this platform.
func (s *JSONRPCServer) Start(socketPath string) error {
	return fmt.Errorf("JSONRPCServer is not supported on this platform")
}

// Stop is not supported on this platform.
func (s *JSONRPCServer) Stop() error {
	return fmt.Errorf("JSONRPCServer is not supported on this platform")
}

// JSONRPCClient is not supported on this platform.
type JSONRPCClient struct{}

// NewJSONRPCClient creates a new JSON-RPC client stub for unsupported platforms.
func NewJSONRPCClient(socketPath string) (*JSONRPCClient, error) {
	return nil, fmt.Errorf("JSONRPCClient is not supported on this platform")
}

// Close is not supported on this platform.
func (c *JSONRPCClient) Close() error {
	return fmt.Errorf("JSONRPCClient is not supported on this platform")
}

// GetSplitFDStream is not supported on this platform.
func (c *JSONRPCClient) GetSplitFDStream(layerID, parentID string) ([]byte, []*os.File, error) {
	return nil, nil, fmt.Errorf("GetSplitFDStream is not supported on this platform")
}

// GetImage is not supported on this platform.
func (c *JSONRPCClient) GetImage(imageID string) (*ImageMetadata, error) {
	return nil, fmt.Errorf("GetImage is not supported on this platform")
}

// CreateSocketPair is not supported on this platform.
func CreateSocketPair() (*net.UnixConn, *net.UnixConn, error) {
	return nil, nil, fmt.Errorf("CreateSocketPair is not supported on this platform")
}
