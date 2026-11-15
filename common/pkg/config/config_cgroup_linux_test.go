package config

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cgroupWarningHelperEnv = "_CONFIG_TEST_CGROUP_WARNING_HELPER"

// IsRootless() is cached, so run the checks in a new process.
func TestCheckCgroupsAndAdjustConfigWarnsOnce(t *testing.T) {
	if os.Getenv(cgroupWarningHelperEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCheckCgroupsAndAdjustConfigWarnsOnce$", "-test.v")
		cmd.Env = append(os.Environ(),
			cgroupWarningHelperEnv+"=1",
			"_CONTAINERS_USERNS_CONFIGURED=done",
			"_CONTAINERS_ROOTLESS_UID=1000",
			"DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/bus",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return
	}

	os.Unsetenv("_CONTAINERS_CGROUP_MANAGER_WARNED")
	var logs bytes.Buffer
	logrus.SetOutput(&logs)

	for range 2 {
		conf := &Config{}
		conf.Engine.CgroupManager = SystemdCgroupsManager
		conf.CheckCgroupsAndAdjustConfig()
		assert.Equal(t, CgroupfsCgroupsManager, conf.Engine.CgroupManager)
	}

	count := strings.Count(logs.String(), "The cgroupv2 manager is set to systemd")
	assert.Equal(t, 1, count, "warning should be logged exactly once, logs: %s", logs.String())
}
