//go:build linux

package splitfdstreamserver

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/internal/driver"
)

// closeRecorder is a Layer that only reports being closed, which is what
// releases the layer store lock, by closing closed.
type closeRecorder struct {
	closed chan struct{}
}

func newCloseRecorder() *closeRecorder {
	return &closeRecorder{closed: make(chan struct{})}
}

func (c *closeRecorder) Files() driver.LayerFiles { return nil }
func (c *closeRecorder) TarSplit() io.Reader      { return nil }
func (c *closeRecorder) Close() error             { close(c.closed); return nil }

// TestLeaseHeldUntilConsumerIsDone checks that the layer stays held until
// the consumer closes the lifetime descriptor it was handed.
func TestLeaseHeldUntilConsumerIsDone(t *testing.T) {
	layer := newCloseRecorder()
	l, keepW, err := newLease(layer)
	require.NoError(t, err)
	go l.run()

	// The consumer is still working: nothing may be released yet.  The pipe
	// is not visible to testing/synctest, so this can only wait a while.
	select {
	case <-layer.closed:
		assert.Fail(t, "the layer store lock was released while the consumer was still using the layer")
	case <-time.After(50 * time.Millisecond):
	}

	// The consumer is done.
	require.NoError(t, keepW.Close())
	select {
	case <-layer.closed:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting for the layer store lock to be released")
	}
}

// TestStopCutsLeaseShort checks that a store can stop serving without
// waiting for a consumer that never says it is done.
func TestStopCutsLeaseShort(t *testing.T) {
	srv := NewServer()
	layer := newCloseRecorder()
	l, keepW, err := newLease(layer)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, keepW.Close()) })
	require.True(t, srv.startLease(l))

	// keepW stays open: the consumer never finishes.
	done := make(chan struct{})
	go func() {
		srv.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "Stop waited for a consumer that never finished")
	}
	select {
	case <-layer.closed:
	default:
		assert.Fail(t, "Stop returned without releasing the layer")
	}

	// A lease cannot be started once the server is stopping.
	l2, keepW2, err := newLease(newCloseRecorder())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, keepW2.Close()) })
	assert.False(t, srv.startLease(l2))
}
