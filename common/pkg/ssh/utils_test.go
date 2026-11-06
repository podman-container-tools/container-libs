package ssh

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDialNetContext_CancelledContext(t *testing.T) {
	u, err := url.Parse("ssh://testhost/run/podman/podman.sock")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = DialNetContext(ctx, nil, "tcp", u)
	require.ErrorIs(t, err, context.Canceled)
}

func TestValidate(t *testing.T) {
	// Test adding ssh port
	dst, uri, err := Validate(nil, "ssh://testhost", 0, "")
	require.Nil(t, err)
	require.Equal(t, dst.URI, "ssh://testhost:22")
	require.Equal(t, dst.URI, uri.String())
	dst, _, err = Validate(nil, "ssh://testhost", 22022, "")
	require.Nil(t, err)
	require.Equal(t, dst.URI, "ssh://testhost:22022")

	// Test adding user
	dst, _, err = Validate(url.User("root"), "ssh://testhost", 0, "")
	require.Nil(t, err)
	require.Equal(t, dst.URI, "ssh://root@testhost:22")

	// Test adding identity
	dst, _, err = Validate(nil, "ssh://testhost", 0, "/path/to/sshkey")
	require.Nil(t, err)
	require.Equal(t, dst.Identity, "/path/to/sshkey")

	// Test that the URI path is preserved (#1551)
	dst, _, err = Validate(nil, "ssh://testhost/run/podman/podman.sock", 0, "")
	require.Nil(t, err)
	require.Equal(t, dst.URI, "ssh://testhost:22/run/podman/podman.sock")
	dst, _, err = Validate(nil, "ssh://testhost/var/run/podman/podman.sock", 0, "")
	require.Nil(t, err)
	require.Equal(t, dst.URI, "ssh://testhost:22/var/run/podman/podman.sock")
}

func TestHostWithSSHScheme(t *testing.T) {
	// golangConnectionDial/Exec/Scp normalize their host through this helper.
	require.Equal(t, "ssh://10.0.0.27", hostWithSSHScheme("10.0.0.27"))
	require.Equal(t, "ssh://10.0.0.27", hostWithSSHScheme("ssh://10.0.0.27")) // idempotent

	// The normalized host validates to the intended dial target.
	_, uri, err := Validate(nil, hostWithSSHScheme("10.0.0.27"), 22, "")
	require.NoError(t, err)
	require.Equal(t, "10.0.0.27:22", uri.Host)
	require.Empty(t, uri.Path)

	// A bare host, on the other hand, is parsed as a path and the dial target
	// degrades to ":22" -- the bug the helper fixes (#46).
	_, broken, err := Validate(nil, "10.0.0.27", 22, "")
	require.NoError(t, err)
	require.Equal(t, ":22", broken.Host)
}
