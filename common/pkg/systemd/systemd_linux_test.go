package systemd

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	systemdDbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

func TestRunUnderSystemdScopeAuthenticationCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		uid        string
		busAddress string
	}{
		{name: "user", uid: "1000", busAddress: "DBUS_SESSION_BUS_ADDRESS"},
		{name: "system", uid: "0", busAddress: "DBUS_SYSTEM_BUS_ADDRESS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Select the connection branch independently of the test process's
			// UID. The fake authentication peer does not validate credentials.
			t.Setenv("_CONTAINERS_ROOTLESS_UID", tc.uid)
			address, reached := startSilentAuthServer(t)
			t.Setenv(tc.busAddress, address)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startTestScope(t, ctx)
			// Cancel only after authentication starts, so an earlier connection
			// failure cannot satisfy the cancellation assertion.
			waitForScopeStage(t, reached, done)
			cancel()
			requireScopeCancellation(t, done)
		})
	}
}

func TestRunUnderSystemdScopeMethodReplyCancellation(t *testing.T) {
	m := newScopeTestManager(t, true, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startTestScope(t, ctx)
	waitForScopeStage(t, m.reached, done)
	cancel()
	requireScopeCancellation(t, done)
}

func TestRunUnderSystemdScopeJobCompletionCancellation(t *testing.T) {
	m := newScopeTestManager(t, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startTestScope(t, ctx)
	waitForScopeStage(t, m.replied, done)
	cancel()
	requireScopeCancellation(t, done)
}

func TestRunUnderSystemdScopeSuccess(t *testing.T) {
	newScopeTestManager(t, false, true)
	// Keep coverage of the public wrapper, which supplies the startup deadline.
	done := make(chan error, 1)
	go func() { done <- RunUnderSystemdScope(os.Getpid(), "user.slice", "test.scope") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(scopeTestTimeout):
		t.Fatal("scope startup did not complete")
	}
}

// Bound fixture setup and failure detection without delaying successful tests.
const scopeTestTimeout = 15 * time.Second

func startTestScope(t *testing.T, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- RunUnderSystemdScopeContext(ctx, os.Getpid(), "user.slice", "test.scope") }()
	return done
}

func waitForScopeStage(t *testing.T, reached <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-reached:
	case err := <-done:
		t.Fatalf("scope returned before reaching the blocked stage: %v", err)
	case <-time.After(scopeTestTimeout):
		t.Fatal("scope did not reach the blocked stage")
	}
}

func requireScopeCancellation(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected a scope startup cancellation error, got %v", err)
		}
	case <-time.After(scopeTestTimeout):
		t.Error("scope startup did not stop after cancellation")
	}
}

// startSilentAuthServer accepts one connection and reads its first authentication
// request without replying. It returns the bus address and a channel that closes
// when the request arrives. Cleanup stops the server and waits for its goroutine.
func startSilentAuthServer(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "bus"))
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	peerDone := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		listener.Close()
		<-peerDone
	})
	go func() {
		defer close(peerDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Bound the fixture's read if a client connects but never sends auth.
		// This watchdog is separate from the cancellation being tested.
		if err := conn.SetReadDeadline(time.Now().Add(scopeTestTimeout)); err != nil {
			return
		}
		// Godbus starts authentication with a NUL byte and an AUTH line.
		// Reading through the newline confirms that the client reached Auth;
		// we do not need to validate the request's contents.
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		close(reached)
		// Keep the connection open without replying until cleanup, so only
		// client cancellation can interrupt the authentication exchange.
		<-release
	}()
	return "unix:path=" + listener.Addr().String(), reached
}

const (
	managerInterface = "org.freedesktop.systemd1.Manager"
	managerPath      = dbus.ObjectPath("/org/freedesktop/systemd1")
	testJobPath      = dbus.ObjectPath("/org/freedesktop/systemd1/job/1")
)

type scopeTestManager struct {
	conn      *dbus.Conn
	reached   chan struct{}
	release   chan struct{}
	holdReply bool
	complete  bool
	replied   <-chan struct{}
}

func (m *scopeTestManager) StartTransientUnit(_ string, _ string, _ []systemdDbus.Property, _ []systemdDbus.PropertyCollection) (dbus.ObjectPath, *dbus.Error) {
	close(m.reached)
	if m.holdReply {
		<-m.release
		return "", dbus.MakeFailedError(errors.New("test manager stopped"))
	}
	if m.complete {
		if err := m.finish(); err != nil {
			return "", dbus.MakeFailedError(err)
		}
	}
	return testJobPath, nil
}

func (m *scopeTestManager) finish() error {
	return m.conn.Emit(managerPath, managerInterface+".JobRemoved", uint32(1), testJobPath, "test.scope", "done")
}

// startPrivateTestBus starts an isolated bus and stops it during test cleanup.
func startPrivateTestBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is required for the private bus fixture")
	}
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1", "--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			address <- scanner.Text()
		} else {
			address <- ""
		}
	}()
	var busAddress string
	select {
	case busAddress = <-address:
		if busAddress == "" {
			t.Fatal("private bus did not publish its address")
		}
	case <-time.After(scopeTestTimeout):
		t.Fatal("private bus startup timed out")
	}
	return busAddress
}

// newScopeTestManager registers a fake systemd manager on its own bus.
func newScopeTestManager(t *testing.T, holdReply, complete bool) *scopeTestManager {
	t.Helper()
	busAddress := startPrivateTestBus(t)
	// Route either connection branch to the fixture instead of the host bus.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", busAddress)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", busAddress)
	// Observe the job-path reply so the completion test can cancel after the
	// server produces a reply, rather than merely entering StartTransientUnit.
	replied := make(chan struct{}, 1)
	conn, err := dbus.Connect(busAddress, dbus.WithOutgoingInterceptor(func(msg *dbus.Message) {
		if msg.Type == dbus.TypeMethodReply && len(msg.Body) == 1 && msg.Body[0] == testJobPath {
			replied <- struct{}{}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	m := &scopeTestManager{
		conn:      conn,
		reached:   make(chan struct{}),
		release:   make(chan struct{}),
		holdReply: holdReply,
		complete:  complete,
		replied:   replied,
	}
	if err := conn.Export(m, managerPath, managerInterface); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue)
	if err != nil {
		t.Fatal(err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("unexpected name request reply: %v", reply)
	}
	t.Cleanup(func() { close(m.release); _ = m.finish() })
	return m
}
