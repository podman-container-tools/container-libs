//go:build linux

package splitfdstreamserver

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/varlink/go/varlink"
	"github.com/vbatts/tar-split/tar/asm"
	"github.com/vbatts/tar-split/tar/storage"
	"go.podman.io/storage/internal/driver"
	"go.podman.io/storage/types"
)

// fakeBackend serves an image with one layer, "layer".
type fakeBackend struct {
	tarSplit []byte
	contents storage.FileGetter
	closed   chan struct{}
}

func (b *fakeBackend) LayerIDByIndex(index int) (string, error) {
	if index != 0 {
		return "", types.ErrLayerUnknown
	}
	return "layer", nil
}

func (b *fakeBackend) OpenLayer(string) (Layer, error) {
	return &fakeLayer{backend: b, tarSplit: bytes.NewReader(b.tarSplit)}, nil
}

type fakeLayer struct {
	backend  *fakeBackend
	tarSplit io.Reader
}

func (l *fakeLayer) Files() driver.LayerFiles               { return &fakeLayerFiles{} }
func (l *fakeLayer) TarSplit() io.Reader                    { return l.tarSplit }
func (l *fakeLayer) Get(name string) (io.ReadCloser, error) { return l.backend.contents.Get(name) }
func (l *fakeLayer) Close() error                           { close(l.backend.closed); return nil }

// newFakeBackend records a one-file layer.
func newFakeBackend(t *testing.T, content []byte) *fakeBackend {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	var tarSplit bytes.Buffer
	contents := storage.NewBufferFileGetPutter()
	ts, done, err := asm.NewInputTarStreamWithDone(&archive, storage.NewJSONPacker(&tarSplit), contents)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, ts)
	require.NoError(t, err)
	require.NoError(t, ts.Close())
	require.NoError(t, <-done)
	return &fakeBackend{tarSplit: tarSplit.Bytes(), contents: contents, closed: make(chan struct{})}
}

// fileDialer hands out a connection on a socket that is already connected.
type fileDialer struct {
	file *os.File
}

func (d fileDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return net.FileConn(d.file)
}

// TestServeGetLayer calls the service the way a consumer does, over the
// socket the server hands out, and checks that the stream and the
// lifetime descriptor come back with the reply.
func TestServeGetLayer(t *testing.T) {
	content := []byte("file content")
	backend := newFakeBackend(t, content)
	srv := NewServer()
	t.Cleanup(srv.Stop)
	client, err := srv.Connection(backend, t.TempDir())
	require.NoError(t, err)
	defer client.Close()
	conn, err := varlink.NewConnectionWithDialer(context.Background(), "unix:@unused", fileDialer{file: client})
	require.NoError(t, err)
	defer conn.Close()

	var info struct {
		Features []string `json:"features"`
	}
	require.NoError(t, conn.Call(context.Background(), interfaceName+".GetInfo", nil, &info))
	assert.Equal(t, []string{"splitdirfdstream-v0"}, info.Features)

	type getLayerParams struct {
		LayerIndex int `json:"layer_index"`
		// Sent as null: the IDL makes it optional.
		ConsumerHasCapDACOverride *bool `json:"consumer_has_cap_dac_override"`
	}
	params := struct {
		Handle uint64          `json:"handle"`
		Params json.RawMessage `json:"params"`
	}{}
	params.Params, err = json.Marshal(getLayerParams{LayerIndex: 0})
	require.NoError(t, err)

	var reply struct {
		DirCount int `json:"dir_count"`
	}
	fds, err := conn.CallWithFDs(context.Background(), interfaceName+".GetLayer", params, &reply, nil)
	require.NoError(t, err)
	require.Len(t, fds, 2, "expected the stream and the lifetime descriptor")
	assert.Zero(t, reply.DirCount)
	stream, keepW := fds[0], fds[1]
	defer stream.Close()

	data, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Contains(t, string(data), string(content), "content with no file behind it is inlined")

	// The layer stays open until the consumer closes the lifetime
	// descriptor.
	select {
	case <-backend.closed:
		require.FailNow(t, "the layer was closed while the consumer still held the lifetime descriptor")
	default:
	}
	require.NoError(t, keepW.Close())
	select {
	case <-backend.closed:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting for the layer to be closed")
	}

	// A layer the image does not have.
	params.Params, err = json.Marshal(getLayerParams{LayerIndex: 1})
	require.NoError(t, err)
	err = conn.Call(context.Background(), interfaceName+".GetLayer", params, &reply)
	var verr *varlink.Error
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, interfaceName+".NoSuchLayerIndex", verr.Name)

	// A layer must be named.
	params.Params = json.RawMessage(`{}`)
	err = conn.Call(context.Background(), interfaceName+".GetLayer", params, &reply)
	var perr *varlink.InvalidParameter
	require.ErrorAs(t, err, &perr)
	assert.Equal(t, "layer_index", perr.Parameter)
}
