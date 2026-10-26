//go:build linux

package systemd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/cgroups"
	"go.podman.io/storage/pkg/unshare"
)

func TestMovePauseProcessToScopeIdempotent(t *testing.T) {
	if !RunsOnSystemd() {
		t.Skip("Not running on systemd")
	}
	if unified, err := cgroups.IsCgroup2UnifiedMode(); err != nil || !unified {
		t.Skip("Not running in cgroup v2 unified mode")
	}
	if unshare.GetRootlessUID() != 0 {
		conn, err := cgroups.UserConnection(unshare.GetRootlessUID())
		if err != nil {
			t.Skipf("Cannot connect to user systemd bus: %v", err)
		}
		conn.Close()
	}

	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	pausePidPath := filepath.Join(t.TempDir(), "pause.pid")
	require.NoError(t, os.WriteFile(pausePidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600))

	MovePauseProcessToScope(pausePidPath)
	cgroup1, err := cgroups.GetCgroupProcess(cmd.Process.Pid)
	require.NoError(t, err)
	base1 := filepath.Base(cgroup1)
	if !strings.HasPrefix(base1, "podman-pause-") || !strings.HasSuffix(base1, ".scope") {
		t.Skipf("Pause process was not moved to systemd scope (current cgroup: %s)", cgroup1)
	}

	// Calling MovePauseProcessToScope a second time must keep the process in the same scope.
	MovePauseProcessToScope(pausePidPath)
	cgroup2, err := cgroups.GetCgroupProcess(cmd.Process.Pid)
	require.NoError(t, err)
	assert.Equal(t, cgroup1, cgroup2)
}
