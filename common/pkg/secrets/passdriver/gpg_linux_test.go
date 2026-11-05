//go:build linux

package passdriver

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/unshare"
)

func TestRootlessUserNamespace(t *testing.T) {
	tests := []struct {
		name       string
		euid       int
		uid        int
		gid        int
		wantNested bool
		wantError  string
	}{
		{name: "ordinary user", euid: 1000, uid: 1000, gid: 1000},
		{name: "rootful", euid: 0},
		{
			name:       "rootless podman namespace",
			euid:       0,
			uid:        1000,
			gid:        1001,
			wantNested: true,
		},
		{
			name:       "rootless user with primary gid zero",
			euid:       0,
			uid:        1000,
			wantNested: true,
		},
		{
			name:      "invalid gid",
			euid:      0,
			uid:       1000,
			gid:       -1,
			wantError: "invalid rootless UID/GID 1000:-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nested, err := rootlessUserNamespace(tc.euid, tc.uid, tc.gid)
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantNested, nested)
		})
	}
}

func TestGPGCommandUsesNestedUserNamespace(t *testing.T) {
	cmd, err := gpgCommandForIdentity(context.Background(), 0, 1000, 1001, "gpg-custom", "--decrypt", "secret.gpg")
	require.NoError(t, err)
	require.Equal(t, []string{"gpg-custom", "--decrypt", "secret.gpg"}, cmd.Args)
	require.Equal(t, uintptr(syscall.CLONE_NEWUSER|syscall.CLONE_NEWPID), cmd.SysProcAttr.Cloneflags)
	require.Equal(t, []syscall.SysProcIDMap{{ContainerID: 1000, HostID: 0, Size: 1}}, cmd.SysProcAttr.UidMappings)
	require.Equal(t, []syscall.SysProcIDMap{{ContainerID: 1001, HostID: 0, Size: 1}}, cmd.SysProcAttr.GidMappings)
}

func TestGPGCommandNestedIdentityAndPID(t *testing.T) {
	if os.Geteuid() != 0 || unshare.GetRootlessUID() == 0 {
		t.Skip("requires UID 0 inside a rootless user namespace")
	}
	uid, gid := unshare.GetRootlessUID(), unshare.GetRootlessGID()
	probe := `printf '%s:%s:%s' "$(id -u)" "$(id -g)" "$$"`
	cmd, err := gpgCommandForIdentity(context.Background(), os.Geteuid(), uid, gid, "sh", "-c", probe)
	require.NoError(t, err)
	output, err := cmd.Output()
	require.NoError(t, err)
	fields := strings.Split(string(output), ":")
	require.Len(t, fields, 3)
	require.Equal(t, strconv.Itoa(uid), fields[0])
	require.Equal(t, strconv.Itoa(gid), fields[1])
	require.Equal(t, "1", fields[2])

	// The child must remain usable for the actual GPG command, not just id.
	gpg, err := exec.LookPath("gpg")
	require.NoError(t, err)
	cmd, err = gpgCommandForIdentity(context.Background(), os.Geteuid(), uid, gid, gpg, "--version")
	require.NoError(t, err)
	require.NoError(t, cmd.Run())
}
