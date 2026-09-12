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

// Allow scheduling overhead beyond the scope startup timeout. These tests use
// the public API so they also cover callers that do not supply a context.
const scopeTestTimeout = 15 * time.Second

func startTestScope(t *testing.T) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- RunUnderSystemdScope(os.Getpid(), "user.slice", "test.scope") }()
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

func requireScopeTimeout(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a scope startup deadline error, got %v", err)
		}
	case <-time.After(scopeTestTimeout):
		t.Error("scope startup did not time out")
	}
}

func TestRunUnderSystemdScopeAuthenticationTimeout(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "bus"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+listener.Addr().String())
	// Exercise UserConnection even when the test itself runs as root. The fake
	// authentication peer does not validate the UID.
	t.Setenv("_CONTAINERS_ROOTLESS_UID", "1000")
	reached := make(chan struct{})
	peerDone := make(chan struct{})
	release := make(chan struct{})
	defer func() { close(release); listener.Close(); <-peerDone }()
	go func() {
		defer close(peerDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(scopeTestTimeout)); err != nil {
			return
		}
		// Withhold the reply only after receiving an authentication request.
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		close(reached)
		<-release
	}()
	done := startTestScope(t)
	waitForScopeStage(t, reached, done)
	requireScopeTimeout(t, done)
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

func newScopeTestManager(t *testing.T, holdReply, complete bool) *scopeTestManager {
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
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", busAddress)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", busAddress)
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
	m := &scopeTestManager{conn: conn, reached: make(chan struct{}), release: make(chan struct{}), holdReply: holdReply, complete: complete, replied: replied}
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

func TestRunUnderSystemdScopeMethodReplyTimeout(t *testing.T) {
	m := newScopeTestManager(t, true, false)
	done := startTestScope(t)
	waitForScopeStage(t, m.reached, done)
	requireScopeTimeout(t, done)
}

func TestRunUnderSystemdScopeJobCompletionTimeout(t *testing.T) {
	m := newScopeTestManager(t, false, false)
	done := startTestScope(t)
	waitForScopeStage(t, m.replied, done)
	requireScopeTimeout(t, done)
}

func TestRunUnderSystemdScopeSuccess(t *testing.T) {
	newScopeTestManager(t, false, true)
	done := startTestScope(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(scopeTestTimeout):
		t.Fatal("scope startup did not complete")
	}
}
