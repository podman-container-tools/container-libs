//go:build linux

package passdriver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

const (
	rootlessUIDEnv = "_CONTAINERS_ROOTLESS_UID"
	rootlessGIDEnv = "_CONTAINERS_ROOTLESS_GID"
)

// gpgCommand runs GPG with the invoking user's numeric identity when Podman has
// mapped that user to UID/GID 0 in its rootless user namespace.  GnuPG derives
// its runtime socket directory from getuid(), so preserving the numeric identity
// lets it find the invoking user's existing agent and keyboxd sockets.
func gpgCommand(ctx context.Context, program string, args ...string) (*exec.Cmd, error) {
	return gpgCommandForIdentity(ctx, os.Geteuid(), os.LookupEnv, program, args...)
}

func gpgCommandForIdentity(ctx context.Context, euid int, lookupEnv func(string) (string, bool), program string, args ...string) (*exec.Cmd, error) {
	uid, gid, nested, err := rootlessUserNamespace(euid, lookupEnv)
	if err != nil {
		return nil, err
	}
	if !nested {
		return exec.CommandContext(ctx, program, args...), nil
	}

	unshareArgs := []string{
		"--user",
		fmt.Sprintf("--map-user=%d", uid),
		fmt.Sprintf("--map-group=%d", gid),
		"--",
		program,
	}
	unshareArgs = append(unshareArgs, args...)
	return exec.CommandContext(ctx, "unshare", unshareArgs...), nil
}

func rootlessUserNamespace(euid int, lookupEnv func(string) (string, bool)) (int, int, bool, error) {
	uidValue, uidSet := lookupEnv(rootlessUIDEnv)
	gidValue, gidSet := lookupEnv(rootlessGIDEnv)
	if euid != 0 || (!uidSet && !gidSet) {
		return 0, 0, false, nil
	}
	if !uidSet || !gidSet {
		return 0, 0, false, fmt.Errorf("both %s and %s must be set", rootlessUIDEnv, rootlessGIDEnv)
	}

	uid, err := strconv.Atoi(uidValue)
	if err != nil || uid <= 0 {
		return 0, 0, false, fmt.Errorf("invalid %s value %q", rootlessUIDEnv, uidValue)
	}
	gid, err := strconv.Atoi(gidValue)
	if err != nil || gid <= 0 {
		return 0, 0, false, fmt.Errorf("invalid %s value %q", rootlessGIDEnv, gidValue)
	}
	return uid, gid, true, nil
}
