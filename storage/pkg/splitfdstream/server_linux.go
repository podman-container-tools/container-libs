//go:build linux

package splitfdstream

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"

	fdpass "github.com/bootc-dev/jsonrpc-fdpass-go"
	"golang.org/x/sys/unix"
)

// JSON-RPC 2.0 standard error codes as documented here: https://www.jsonrpc.org/specification
const (
	jsonrpcInvalidRequest = -32600
	jsonrpcMethodNotFound = -32601
	jsonrpcInvalidParams  = -32602
	jsonrpcServerError    = -32000
)

// sendRetry retries sender.Send on EAGAIN (non-blocking socket buffer full).
func sendRetry(sender *fdpass.Sender, msg *fdpass.MessageWithFds) error {
	for {
		err := sender.Send(msg)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			runtime.Gosched()
			continue
		}
		return err
	}
}

// JSONRPCServer manages a JSON-RPC server using the external library.
type JSONRPCServer struct {
	driverFunc  DriverFunc
	store       Store
	listener    net.Listener
	running     bool
	mu          sync.RWMutex
	shutdown    chan struct{}
	connections sync.WaitGroup
}

// NewJSONRPCServer creates a new JSON-RPC server.
func NewJSONRPCServer(driverFunc DriverFunc, store Store) *JSONRPCServer {
	return &JSONRPCServer{
		driverFunc: driverFunc,
		store:      store,
		shutdown:   make(chan struct{}),
	}
}

// Start starts the JSON-RPC server listening on the given Unix socket.
func (s *JSONRPCServer) Start(socketPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server already running")
	}

	os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}

	s.listener = listener
	s.running = true

	go s.acceptConnections()

	return nil
}

// Stop stops the JSON-RPC server.
func (s *JSONRPCServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	close(s.shutdown)
	if s.listener != nil {
		s.listener.Close()
	}
	s.connections.Wait()
	s.running = false

	return nil
}

func (s *JSONRPCServer) acceptConnections() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				continue
			}
		}

		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			continue
		}

		s.HandleConnection(unixConn)
	}
}

// HandleConnection handles a single client connection in a new goroutine.
// The connection is tracked by the server's WaitGroup so that Stop()
// waits for all active connections to finish.
func (s *JSONRPCServer) HandleConnection(conn *net.UnixConn) {
	s.connections.Go(func() {
		s.handleConnection(conn)
	})
}

func (s *JSONRPCServer) handleConnection(conn *net.UnixConn) {
	defer conn.Close()

	receiver := fdpass.NewReceiver(conn)
	sender := fdpass.NewSender(conn)
	defer receiver.Close()

	for {
		select {
		case <-s.shutdown:
			return
		default:
		}

		msgWithFds, err := receiver.Receive()
		if err != nil {
			return
		}

		req, ok := msgWithFds.Message.(*fdpass.Request)
		if !ok {
			resp := fdpass.NewErrorResponse(
				&fdpass.Error{Code: jsonrpcInvalidRequest, Message: "Invalid Request"},
				nil,
			)
			if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
				return
			}
			continue
		}

		s.handleRequest(sender, req, msgWithFds.FileDescriptors)
	}
}

func (s *JSONRPCServer) handleRequest(sender *fdpass.Sender, req *fdpass.Request, fds []*os.File) {
	defer func() {
		for _, f := range fds {
			f.Close()
		}
	}()

	switch req.Method {
	case "GetSplitFDStream":
		s.handleGetSplitFDStream(sender, req)
	case "GetImage":
		s.handleGetImage(sender, req)
	default:
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcMethodNotFound, Message: fmt.Sprintf("method %q not found", req.Method)},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending method-not-found response: %v\n", err)
		}
	}
}

func (s *JSONRPCServer) handleGetSplitFDStream(sender *fdpass.Sender, req *fdpass.Request) {
	params, ok := req.Params.(map[string]interface{})
	if !ok {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcInvalidParams, Message: "params must be an object"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	layerID, _ := params["layerId"].(string)
	if layerID == "" {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcInvalidParams, Message: "layerId is required"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	parentID, _ := params["parentId"].(string)

	driver, release, err := s.driverFunc()
	if err != nil {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: fmt.Sprintf("failed to acquire driver: %v", err)},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}
	stream, fileFDs, err := driver.GetSplitFDStream(layerID, parentID, &GetSplitFDStreamOpts{})
	release()
	if err != nil {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: err.Error()},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	// The stream must be backed by an *os.File (e.g. a memfd) so it can
	// be sent over the socket as a file descriptor.
	streamFile, ok := stream.(*os.File)
	if !ok {
		stream.Close()
		for _, f := range fileFDs {
			f.Close()
		}
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: "stream is not backed by a file descriptor"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	info, err := streamFile.Stat()
	if err != nil {
		streamFile.Close()
		for _, f := range fileFDs {
			f.Close()
		}
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: fmt.Sprintf("failed to stat stream: %v", err)},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}
	streamSize := info.Size()

	// allFDs[0] = stream memfd, allFDs[1:] = content file descriptors.
	// The library automatically batches FDs across multiple sendmsg()
	// calls with whitespace padding per the jsonrpc-fdpass protocol.
	allFDs := make([]*os.File, 0, 1+len(fileFDs))
	allFDs = append(allFDs, streamFile)
	allFDs = append(allFDs, fileFDs...)

	result := map[string]interface{}{
		"streamSize": streamSize,
	}

	resp := fdpass.NewResponse(result, req.ID)
	if err := sendRetry(sender, &fdpass.MessageWithFds{
		Message:         resp,
		FileDescriptors: allFDs,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error sending response: %v\n", err)
	}
	// sendmsg duplicates FDs into the kernel — close our copies
	// whether the send succeeded or failed.
	for _, f := range allFDs {
		f.Close()
	}
}

func (s *JSONRPCServer) handleGetImage(sender *fdpass.Sender, req *fdpass.Request) {
	params, ok := req.Params.(map[string]interface{})
	if !ok {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcInvalidParams, Message: "params must be an object"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	imageID, _ := params["imageId"].(string)
	if imageID == "" {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcInvalidParams, Message: "imageId is required"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	if s.store == nil {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: "store not available for image operations"},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	metadata, err := GetImageMetadata(s.store, imageID)
	if err != nil {
		resp := fdpass.NewErrorResponse(
			&fdpass.Error{Code: jsonrpcServerError, Message: fmt.Sprintf("failed to get image metadata: %v", err)},
			req.ID,
		)
		if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
			fmt.Fprintf(os.Stderr, "error sending error response: %v\n", err)
		}
		return
	}

	result := map[string]interface{}{
		"manifest":     string(metadata.ManifestJSON),
		"config":       string(metadata.ConfigJSON),
		"layerDigests": metadata.LayerDigests,
	}

	resp := fdpass.NewResponse(result, req.ID)
	if err := sendRetry(sender, &fdpass.MessageWithFds{Message: resp}); err != nil {
		fmt.Fprintf(os.Stderr, "error sending image response: %v\n", err)
		return
	}
}

// JSONRPCClient implements a JSON-RPC client.
type JSONRPCClient struct {
	conn     *net.UnixConn
	sender   *fdpass.Sender
	receiver *fdpass.Receiver
	mu       sync.Mutex
	nextID   int64
}

// NewJSONRPCClient connects to a JSON-RPC server on the given Unix socket.
func NewJSONRPCClient(socketPath string) (*JSONRPCClient, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("connection is not a unix socket")
	}

	return &JSONRPCClient{
		conn:     unixConn,
		sender:   fdpass.NewSender(unixConn),
		receiver: fdpass.NewReceiver(unixConn),
		nextID:   1,
	}, nil
}

// Close closes the client connection.
func (c *JSONRPCClient) Close() error {
	if c.receiver != nil {
		c.receiver.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetSplitFDStream sends a GetSplitFDStream request and returns the response.
func (c *JSONRPCClient) GetSplitFDStream(layerID, parentID string) ([]byte, []*os.File, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	req := fdpass.NewRequest("GetSplitFDStream", map[string]interface{}{
		"layerId":  layerID,
		"parentId": parentID,
	}, id)

	if err := sendRetry(c.sender, &fdpass.MessageWithFds{Message: req}); err != nil {
		return nil, nil, fmt.Errorf("failed to send request: %w", err)
	}

	// The library handles FD batching transparently — all FDs arrive
	// with the single response via batched sendmsg()/recvmsg() calls.
	respMsg, err := c.receiver.Receive()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to receive response: %w", err)
	}

	resp, ok := respMsg.Message.(*fdpass.Response)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected response type: %T", respMsg.Message)
	}

	if resp.Error != nil {
		return nil, nil, fmt.Errorf("server error: %s", resp.Error.Message)
	}

	allFDs := respMsg.FileDescriptors
	if len(allFDs) == 0 {
		return nil, nil, fmt.Errorf("no file descriptors received")
	}

	// allFDs[0] is a memfd containing the stream data, the rest are content FDs
	streamFile := allFDs[0]
	contentFDs := allFDs[1:]

	streamData, err := io.ReadAll(streamFile)
	streamFile.Close()
	if err != nil {
		for _, f := range contentFDs {
			f.Close()
		}
		return nil, nil, fmt.Errorf("failed to read stream data from fd: %w", err)
	}

	return streamData, contentFDs, nil
}

// GetImage sends a GetImage request and returns image metadata.
func (c *JSONRPCClient) GetImage(imageID string) (*ImageMetadata, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	req := fdpass.NewRequest("GetImage", map[string]interface{}{
		"imageId": imageID,
	}, id)

	if err := sendRetry(c.sender, &fdpass.MessageWithFds{Message: req}); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	respMsg, err := c.receiver.Receive()
	if err != nil {
		return nil, fmt.Errorf("failed to receive response: %w", err)
	}

	resp, ok := respMsg.Message.(*fdpass.Response)
	if !ok {
		return nil, fmt.Errorf("unexpected response type: %T", respMsg.Message)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("server error: %s", resp.Error.Message)
	}

	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected result type: %T", resp.Result)
	}

	manifestJSON, _ := result["manifest"].(string)
	configJSON, _ := result["config"].(string)

	layerDigestsInterface, _ := result["layerDigests"].([]interface{})
	layerDigests := make([]string, len(layerDigestsInterface))
	for i, v := range layerDigestsInterface {
		layerDigests[i], _ = v.(string)
	}

	return &ImageMetadata{
		ManifestJSON: []byte(manifestJSON),
		ConfigJSON:   []byte(configJSON),
		LayerDigests: layerDigests,
	}, nil
}

// CreateSocketPair creates a pair of connected UNIX sockets.
func CreateSocketPair() (*net.UnixConn, *net.UnixConn, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create socket pair: %w", err)
	}

	clientFile := os.NewFile(uintptr(fds[0]), "client")
	defer clientFile.Close()
	serverFile := os.NewFile(uintptr(fds[1]), "server")
	defer serverFile.Close()

	clientConn, err := net.FileConn(clientFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create client connection: %w", err)
	}

	serverConn, err := net.FileConn(serverFile)
	if err != nil {
		clientConn.Close()
		return nil, nil, fmt.Errorf("failed to create server connection: %w", err)
	}

	clientUnix, ok := clientConn.(*net.UnixConn)
	if !ok {
		clientConn.Close()
		serverConn.Close()
		return nil, nil, fmt.Errorf("failed to cast client to UnixConn")
	}

	serverUnix, ok := serverConn.(*net.UnixConn)
	if !ok {
		clientConn.Close()
		serverConn.Close()
		return nil, nil, fmt.Errorf("failed to cast server to UnixConn")
	}

	return clientUnix, serverUnix, nil
}
