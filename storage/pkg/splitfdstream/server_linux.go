//go:build linux

package splitfdstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/varlink/go/varlink"
	"golang.org/x/sys/unix"
)

const (
	interfaceName        = "org.composefs.Oci"
	interfaceDescription = `
interface org.composefs.Oci

method GetInfo() -> (features: []string)

type StorageLocator (
  storage_path: string,
  layer_id: string
)

type GetLayerParams (
  diff_id: ?string,
  storage: ?StorageLocator
)

type GetLayerReply (
  dir_count: int
)

error RepoNotFound (message: string)
error InvalidHandle (handle: int)
error NoSuchImage (image: string)
error InternalError (message: string)
error NoSuchLayer (diff_id: string)
error InvalidDigest (message: string)
error InvalidRequest (message: string)

method HasLayer(handle: int, diff_id: string) -> (present: bool, layer_verity: ?string)

method GetLayer(handle: int, params: GetLayerParams) -> (dir_count: int)

method GetImage(image_id: string) -> (manifest: string, config: string, layer_digests: []string)
`
)

// ociInterface implements the varlink dispatcher interface for org.composefs.Oci.
type ociInterface struct {
	driverFunc DriverFunc
	store      Store
}

func (i *ociInterface) VarlinkGetName() string {
	return interfaceName
}

func (i *ociInterface) VarlinkGetDescription() string {
	return interfaceDescription
}

func (i *ociInterface) VarlinkDispatch(ctx context.Context, call varlink.Call, methodname string) error {
	switch methodname {
	case "GetInfo":
		return i.getInfo(ctx, call)
	case "HasLayer":
		return i.hasLayer(ctx, call)
	case "GetLayer":
		return i.getLayer(ctx, call)
	case "GetImage":
		return i.getImage(ctx, call)
	default:
		return call.ReplyMethodNotImplemented(ctx, methodname)
	}
}

func (i *ociInterface) getInfo(ctx context.Context, call varlink.Call) error {
	type reply struct {
		Features []string `json:"features"`
	}
	return call.Reply(ctx, &reply{
		Features: []string{"splitdirfdstream-v0"},
	})
}

func (i *ociInterface) hasLayer(ctx context.Context, call varlink.Call) error {
	var params struct {
		Handle uint64 `json:"handle"`
		DiffID string `json:"diff_id"`
	}
	if err := call.GetParameters(&params); err != nil {
		return call.ReplyInvalidParameter(ctx, "diff_id")
	}

	type reply struct {
		Present     bool    `json:"present"`
		LayerVerity *string `json:"layer_verity,omitempty"`
	}
	return call.Reply(ctx, &reply{Present: false})
}

func (i *ociInterface) getLayer(ctx context.Context, call varlink.Call) error {
	var params struct {
		Handle uint64          `json:"handle"`
		Params json.RawMessage `json:"params"`
	}
	if err := call.GetParameters(&params); err != nil {
		return call.ReplyInvalidParameter(ctx, "params")
	}

	var getLayerParams struct {
		DiffID  *string `json:"diff_id,omitempty"`
		Storage *struct {
			StoragePath string `json:"storage_path"`
			LayerID     string `json:"layer_id"`
		} `json:"storage,omitempty"`
	}
	if err := json.Unmarshal(params.Params, &getLayerParams); err != nil {
		return call.ReplyInvalidParameter(ctx, "params")
	}

	var layerID, parentID string
	if getLayerParams.Storage != nil {
		layerID = getLayerParams.Storage.LayerID
	} else if getLayerParams.DiffID != nil {
		layerID = *getLayerParams.DiffID
	} else {
		return call.ReplyInvalidParameter(ctx, "params")
	}

	driver, release, err := i.driverFunc()
	if err != nil {
		return replyOciError(ctx, call, "InternalError", fmt.Sprintf("failed to acquire driver: %v", err))
	}

	stream, dirFDs, err := driver.GetSplitDirFDStream(layerID, parentID, &GetSplitFDStreamOpts{})
	release()
	if err != nil {
		return replyOciError(ctx, call, "NoSuchLayer", err.Error())
	}

	streamFile, ok := stream.(*os.File)
	if !ok {
		stream.Close()
		for _, f := range dirFDs {
			f.Close()
		}
		return replyOciError(ctx, call, "InternalError", "stream is not backed by a file descriptor")
	}

	keepR, keepW, err := os.Pipe()
	if err != nil {
		streamFile.Close()
		for _, f := range dirFDs {
			f.Close()
		}
		return replyOciError(ctx, call, "InternalError", fmt.Sprintf("failed to create keepalive pipe: %v", err))
	}

	// Build FD array without sparsification: the stream data uses
	// logical dirfd indices (0-based) that map directly to dirFDs.
	allFDs := make([]*os.File, 0, 1+len(dirFDs)+1)
	allFDs = append(allFDs, streamFile)
	allFDs = append(allFDs, dirFDs...)
	allFDs = append(allFDs, keepR)
	dirCount := len(dirFDs)

	type reply struct {
		DirCount int `json:"dir_count"`
	}
	err = call.ReplyWithFDs(ctx, &reply{DirCount: dirCount}, allFDs)

	for _, f := range allFDs {
		f.Close()
	}
	keepW.Close()

	return err
}

func (i *ociInterface) getImage(ctx context.Context, call varlink.Call) error {
	var params struct {
		ImageID string `json:"image_id"`
	}
	if err := call.GetParameters(&params); err != nil {
		return call.ReplyInvalidParameter(ctx, "image_id")
	}
	if params.ImageID == "" {
		return call.ReplyInvalidParameter(ctx, "image_id")
	}
	if i.store == nil {
		return replyOciError(ctx, call, "InternalError", "store not available for image operations")
	}

	metadata, err := GetImageMetadata(i.store, params.ImageID)
	if err != nil && !strings.Contains(params.ImageID, ":") {
		// Retry with :latest tag — containers-storage stores names
		// with explicit tags.
		metadata, err = GetImageMetadata(i.store, params.ImageID+":latest")
	}
	if err != nil {
		type noSuchImage struct {
			Image string `json:"image"`
		}
		return call.ReplyError(ctx, interfaceName+".NoSuchImage",
			&noSuchImage{Image: fmt.Sprintf("failed to get image metadata: %v", err)})
	}

	type reply struct {
		Manifest     string   `json:"manifest"`
		Config       string   `json:"config"`
		LayerDigests []string `json:"layer_digests"`
	}
	return call.Reply(ctx, &reply{
		Manifest:     string(metadata.ManifestJSON),
		Config:       string(metadata.ConfigJSON),
		LayerDigests: metadata.LayerDigests,
	})
}

func replyOciError(ctx context.Context, call varlink.Call, name, message string) error {
	type errorParams struct {
		Message string `json:"message"`
	}
	return call.ReplyError(ctx, interfaceName+"."+name, &errorParams{Message: message})
}

// VarlinkServer manages a varlink service for splitdirfdstream operations.
type VarlinkServer struct {
	service     *varlink.Service
	running     bool
	mu          sync.RWMutex
	shutdown    chan struct{}
	connections sync.WaitGroup
}

// NewVarlinkServer creates a new varlink server.
func NewVarlinkServer(driverFunc DriverFunc, store Store) (*VarlinkServer, error) {
	svc, err := varlink.NewService(
		"containers",
		"storage",
		"1.0",
		"https://github.com/containers/container-libs",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create varlink service: %w", err)
	}
	iface := &ociInterface{
		driverFunc: driverFunc,
		store:      store,
	}
	if err := svc.RegisterInterface(iface); err != nil {
		return nil, fmt.Errorf("failed to register interface: %w", err)
	}
	return &VarlinkServer{
		service:  svc,
		shutdown: make(chan struct{}),
	}, nil
}

// HandleConnection handles a single client connection in a new goroutine.
func (s *VarlinkServer) HandleConnection(conn *net.UnixConn) {
	s.connections.Go(func() {
		s.handleConnection(conn)
	})
}

func (s *VarlinkServer) handleConnection(conn *net.UnixConn) {
	defer conn.Close()
	ctx := context.Background()
	vconn := newVarlinkConn(conn)

	for {
		select {
		case <-s.shutdown:
			return
		default:
		}

		request, fds, err := vconn.ReadBytesWithFDs(ctx, '\x00')
		if err != nil {
			return
		}

		// Strip the NUL delimiter.
		err = s.service.HandleMessageWithFDs(ctx, vconn, request[:len(request)-1], fds)
		if err != nil {
			return
		}
	}
}

// Stop stops the varlink server.
func (s *VarlinkServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		close(s.shutdown)
		s.running = false
	}
	s.connections.Wait()
	return nil
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
