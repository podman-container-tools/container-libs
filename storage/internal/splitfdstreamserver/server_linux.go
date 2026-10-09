//go:build linux

package splitfdstreamserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	digest "github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
	"github.com/varlink/go/varlink"
	graphdriver "go.podman.io/storage/drivers"
	"golang.org/x/sys/unix"
)

const (
	interfaceName        = "org.composefs.Oci"
	interfaceDescription = `
interface org.composefs.Oci

method GetInfo() -> (features: []string)

type GetLayerParams (
  diff_id: ?string,
  consumer_has_cap_dac_override: ?bool
)

error InternalError (message: string)
error NoSuchLayer (diff_id: string)
error InvalidDigest (message: string)

method GetLayer(handle: int, params: GetLayerParams) -> (dir_count: int)
`
)

// backendKey is the context key under which a connection's StoreBackend is
// passed to the method handlers.
type backendKey struct{}

// ociInterface implements the varlink dispatcher interface for org.composefs.Oci.
type ociInterface struct{}

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
	case "GetLayer":
		return i.getLayer(ctx, call)
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

func (i *ociInterface) getLayer(ctx context.Context, call varlink.Call) error {
	var params struct {
		Handle uint64          `json:"handle"`
		Params json.RawMessage `json:"params"`
	}
	if err := call.GetParameters(&params); err != nil {
		return call.ReplyInvalidParameter(ctx, "params")
	}

	var getLayerParams struct {
		DiffID                    *string `json:"diff_id,omitempty"`
		ConsumerHasCapDACOverride bool    `json:"consumer_has_cap_dac_override,omitempty"`
	}
	if err := json.Unmarshal(params.Params, &getLayerParams); err != nil {
		return call.ReplyInvalidParameter(ctx, "params")
	}
	if getLayerParams.DiffID == nil || *getLayerParams.DiffID == "" {
		return call.ReplyInvalidParameter(ctx, "diff_id")
	}
	diffID := *getLayerParams.DiffID
	if _, err := digest.Parse(diffID); err != nil {
		return replyOciError(ctx, call, "InvalidDigest", err.Error())
	}
	backend, ok := ctx.Value(backendKey{}).(StoreBackend)
	if !ok {
		return replyOciError(ctx, call, "InternalError", "connection has no store")
	}

	layerID, err := backend.LayerIDByDiffID(diffID)
	if err != nil {
		// NoSuchLayer carries only the diff ID, so the reason is logged.
		logrus.Debugf("splitfdstream: resolving %s: %v", diffID, err)
		return replyNoSuchLayer(ctx, call, diffID)
	}

	files, err := backend.LayerFiles(layerID)
	if err != nil {
		return replyOciError(ctx, call, "InternalError", err.Error())
	}
	defer files.Close()

	// A consumer that cannot bypass file permissions could not open a file
	// that is not world-readable, so such content is sent inline instead.
	onlyWorldReadable := !getLayerParams.ConsumerHasCapDACOverride
	streamFile, err := buildStream(backend, layerID, files, onlyWorldReadable)
	if err != nil {
		return replyOciError(ctx, call, "InternalError", err.Error())
	}

	// The consumer expects a lifetime descriptor after the directories,
	// and treats its EOF as the producer being done with the layer.  The
	// directories already keep what the stream references reachable, so
	// there is nothing to hold here: the write end is closed right away.
	keepR, keepW, err := os.Pipe()
	if err != nil {
		streamFile.Close()
		return replyOciError(ctx, call, "InternalError", fmt.Sprintf("failed to create keepalive pipe: %v", err))
	}

	// The stream references directories by their index in this array, less
	// the leading stream FD.
	dirFDs := files.DirFDs()
	allFDs := make([]*os.File, 0, 1+len(dirFDs)+1)
	allFDs = append(allFDs, streamFile)
	allFDs = append(allFDs, dirFDs...)
	allFDs = append(allFDs, keepR)

	type reply struct {
		DirCount int `json:"dir_count"`
	}
	err = call.ReplyWithFDs(ctx, &reply{DirCount: len(dirFDs)}, allFDs)

	streamFile.Close()
	keepR.Close()
	keepW.Close()

	return err
}

// buildStream writes the splitdirfdstream for a layer into a memfd, and
// returns it positioned at the start, ready to be handed to the consumer.
func buildStream(backend StoreBackend, layerID string, files graphdriver.LayerFiles, onlyWorldReadable bool) (*os.File, error) {
	diff, err := backend.LayerDiff(layerID)
	if err != nil {
		return nil, fmt.Errorf("getting diff of layer %s: %w", layerID, err)
	}
	defer diff.Close()

	fd, err := unix.MemfdCreate("splitdirfdstream", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	streamFile := os.NewFile(uintptr(fd), "splitdirfdstream")

	if err := convert(streamFile, diff, files, onlyWorldReadable); err != nil {
		streamFile.Close()
		return nil, fmt.Errorf("converting diff of layer %s: %w", layerID, err)
	}
	if _, err := streamFile.Seek(0, io.SeekStart); err != nil {
		streamFile.Close()
		return nil, fmt.Errorf("rewinding stream: %w", err)
	}
	return streamFile, nil
}

func replyNoSuchLayer(ctx context.Context, call varlink.Call, diffID string) error {
	type errorParams struct {
		DiffID string `json:"diff_id"`
	}
	return call.ReplyError(ctx, interfaceName+".NoSuchLayer", &errorParams{DiffID: diffID})
}

func replyOciError(ctx context.Context, call varlink.Call, name, message string) error {
	type errorParams struct {
		Message string `json:"message"`
	}
	return call.ReplyError(ctx, interfaceName+"."+name, &errorParams{Message: message})
}

// VarlinkServer serves org.composefs.Oci on the connections it hands out.
type VarlinkServer struct {
	service *varlink.Service

	mu      sync.Mutex
	conns   map[*net.UnixConn]struct{} // Protected by mu.
	stopped bool                       // Protected by mu.
	wg      sync.WaitGroup
}

// NewVarlinkServer creates a new varlink server.
func NewVarlinkServer() (*VarlinkServer, error) {
	svc, err := varlink.NewService(
		"containers",
		"storage",
		"1.0",
		"https://github.com/containers/container-libs",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create varlink service: %w", err)
	}
	if err := svc.RegisterInterface(&ociInterface{}); err != nil {
		return nil, fmt.Errorf("failed to register interface: %w", err)
	}
	return &VarlinkServer{
		service: svc,
		conns:   map[*net.UnixConn]struct{}{},
	}, nil
}

// NewConnection returns one end of a new socket pair, and serves the other
// end from backend.  The caller is responsible for closing the returned file.
func (s *VarlinkServer) NewConnection(backend StoreBackend) (*os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("creating socket pair: %w", err)
	}
	clientFile := os.NewFile(uintptr(fds[0]), "splitfdstream-client")
	serverFile := os.NewFile(uintptr(fds[1]), "splitfdstream-server")
	serverConn, err := net.FileConn(serverFile)
	serverFile.Close()
	if err != nil {
		clientFile.Close()
		return nil, fmt.Errorf("creating server connection: %w", err)
	}
	conn, ok := serverConn.(*net.UnixConn)
	if !ok {
		clientFile.Close()
		serverConn.Close()
		return nil, fmt.Errorf("unexpected connection type %T", serverConn)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		clientFile.Close()
		conn.Close()
		return nil, errors.New("varlink server is stopped")
	}
	s.conns[conn] = struct{}{}
	s.wg.Go(func() {
		s.serve(conn, backend)
	})
	return clientFile, nil
}

func (s *VarlinkServer) serve(conn *net.UnixConn, backend StoreBackend) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	vconn, err := newVarlinkConn(conn)
	if err != nil {
		conn.Close()
		logrus.Errorf("splitfdstream: %v", err)
		return
	}
	defer vconn.Close()

	ctx := context.WithValue(context.Background(), backendKey{}, backend)
	for {
		request, fds, err := vconn.ReadBytesWithFDs(ctx, '\x00')
		if err != nil {
			return
		}
		if err := s.service.HandleMessageWithFDs(ctx, vconn, request[:len(request)-1], fds); err != nil {
			return
		}
	}
}

// Stop stops accepting requests and waits for the connections to be done.
// A request already being served is completed; a connection waiting for
// its next request sees end of file.
func (s *VarlinkServer) Stop() {
	s.mu.Lock()
	s.stopped = true
	for conn := range s.conns {
		// Shutting down only the read side lets a request in flight
		// still send its reply.
		if err := conn.CloseRead(); err != nil {
			logrus.Debugf("splitfdstream: shutting down connection: %v", err)
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}
