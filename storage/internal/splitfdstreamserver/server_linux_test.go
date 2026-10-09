//go:build linux

package splitfdstreamserver

import (
	"testing"
	"time"
)

// TestStopWithIdleConnection checks that Stop does not wait for a client
// that keeps its end of the socket open without sending anything.
func TestStopWithIdleConnection(t *testing.T) {
	srv, err := NewVarlinkServer()
	if err != nil {
		t.Fatal(err)
	}
	client, err := srv.NewConnection(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan struct{})
	go func() {
		srv.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return while a connection was idle")
	}

	if _, err := srv.NewConnection(nil); err == nil {
		t.Fatal("NewConnection succeeded on a stopped server")
	}
}
