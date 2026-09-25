package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// these tests cannot check for "true" functionality
// in order to do that, you need two machines and a place to connect to/from
// these will error but we can check the error message to make sure it is an ssh error message
// not one for a segfault or parsing error

func TestCreate(t *testing.T) {
	options := ConnectionCreateOptions{
		Port:    22,
		Path:    "localhost",
		Name:    "testing",
		Socket:  "/run/user/foo/podman/podman.sock",
		Default: false,
	}
	err := Create(&options, NativeMode)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	err = Create(&options, GolangMode)
	// the error with golang should be nil, we want this to work if we are given a socket path
	// that is the current podman behavior
	require.Nil(t, err)
}

func TestExec(t *testing.T) {
	options := ConnectionExecOptions{
		Port: 22,
		Host: "localhost",
		Args: []string{"ls", "/"},
	}

	_, err := Exec(&options, NativeMode)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	_, err = Exec(&options, GolangMode)
	require.Error(t, err, "failed to connect: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures")
}

func TestExecWithInput(t *testing.T) {
	options := ConnectionExecOptions{
		Port: 22,
		Host: "localhost",
		Args: []string{"md5sum"},
	}

	input, err := os.Open("/etc/fstab")
	require.NoError(t, err)
	defer input.Close()

	_, err = ExecWithInput(&options, NativeMode, input)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	_, err = ExecWithInput(&options, GolangMode, input)
	require.Error(t, err, "failed to connect: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures")
}

func TestDial(t *testing.T) {
	options := ConnectionDialOptions{
		Port: 22,
		Host: "localhost",
	}

	_, err := Dial(&options, NativeMode)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	_, err = Dial(&options, GolangMode)
	require.Error(t, err, "failed to connect: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures")

	// Test again without specifying sshd port, and code should default to port 22
	options = ConnectionDialOptions{
		Host: "localhost",
	}

	_, err = Dial(&options, NativeMode)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	_, err = Dial(&options, GolangMode)
	require.Error(t, err, "failed to connect: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures")
}

func TestScp(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "")
	require.Nil(t, err)

	options := ConnectionScpOptions{
		User:        &url.Userinfo{},
		Source:      f.Name(),
		Destination: "localhost:/does/not/exist",
		Port:        22,
	}

	_, err = Scp(&options, NativeMode)
	// exit status 255 is what you get when ssh is not enabled or the connection failed
	// this means up to that point, everything worked
	require.Error(t, err, "exit status 255")

	_, err = Scp(&options, GolangMode)
	require.Error(t, err, "failed to connect: ssh: handshake failed: ssh: disconnect, reason 2: Too many authentication failures")
}

func TestValidateAndConfigureMachineIgnoresKnownHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))

	// A @cert-authority marker matching every host restricts the host key
	// algorithms to cert types only; this must not break machine
	// connections which ignore the host key anyway.
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	caPub, err := ssh.NewPublicKey(caKey.Public())
	require.NoError(t, err)
	knownHosts := append([]byte("@cert-authority * "), ssh.MarshalAuthorizedKey(caPub)...)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), knownHosts, 0o600))

	_, userKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	userPEM, err := ssh.MarshalPrivateKey(userKey, "")
	require.NoError(t, err)
	iden := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(iden, pem.EncodeToMemory(userPEM), 0o600))

	uri, err := url.Parse("ssh://core@localhost:22")
	require.NoError(t, err)

	cfg, err := ValidateAndConfigure(uri, iden, true)
	require.NoError(t, err)
	require.NotNil(t, cfg.HostKeyCallback)
	require.Empty(t, cfg.HostKeyAlgorithms)

	// Regular connections still honor the known_hosts restriction.
	cfg, err = ValidateAndConfigure(uri, iden, false)
	require.NoError(t, err)
	require.Contains(t, cfg.HostKeyAlgorithms, ssh.CertAlgoED25519v01)
	require.NotContains(t, cfg.HostKeyAlgorithms, ssh.KeyAlgoED25519)
}
