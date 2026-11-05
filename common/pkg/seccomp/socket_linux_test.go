//go:build seccomp

package seccomp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"syscall"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	// socketHelperEnv makes the test binary act as the helper process: it loads
	// the default profile into itself and prints the result of each probe.
	// The value is "audit" to keep CAP_AUDIT_WRITE and anything else to drop it.
	socketHelperEnv = "CONTAINERS_SECCOMP_SOCKET_HELPER"

	// maxFamily is above AF_MAX so that unassigned families are probed too.
	maxFamily = 64
)

func errnoOf(err error) syscall.Errno {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return errno
	}
	return 0
}

// TestDefaultProfileSocketHelper is the helper process of
// TestDefaultProfileSockets and does nothing when run on its own.
func TestDefaultProfileSocketHelper(t *testing.T) {
	mode := os.Getenv(socketHelperEnv)
	if mode == "" {
		t.Skip("helper process of TestDefaultProfileSockets")
	}

	// The filter is only loaded into this thread.
	runtime.LockOSThread()

	fatal := func(err error) {
		fmt.Fprintln(os.Stderr, "loading the default profile:", err)
		os.Exit(2)
	}

	body, err := os.ReadFile("seccomp.json")
	if err != nil {
		fatal(err)
	}
	// An empty, non-nil list drops CAP_AUDIT_WRITE; a nil one would keep every
	// capability-dependent rule.
	caps := []string{}
	if mode == "audit" {
		caps = append(caps, "CAP_AUDIT_WRITE")
	}
	spec := &specs.Spec{Process: &specs.Process{Capabilities: &specs.LinuxCapabilities{Bounding: caps}}}
	profile, err := LoadProfileFromBytes(body, spec)
	if err != nil {
		fatal(fmt.Errorf("load profile: %w", err))
	}
	filter, err := BuildFilter(profile)
	if err != nil {
		fatal(fmt.Errorf("build filter: %w", err))
	}
	// BuildFilter leaves no_new_privs to the caller, as a container runtime does.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fatal(fmt.Errorf("set no_new_privs: %w", err))
	}
	if err := filter.Load(); err != nil {
		fatal(fmt.Errorf("load filter: %w", err))
	}

	for family := range maxFamily {
		fd, err := unix.Socket(family, unix.SOCK_STREAM, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		fmt.Fprintf(os.Stdout, "socket %d %d\n", family, errnoOf(err))

		fds, err := unix.Socketpair(family, unix.SOCK_STREAM, 0)
		if err == nil {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
		}
		fmt.Fprintf(os.Stdout, "socketpair %d %d\n", family, errnoOf(err))
	}

	// The kernel reads the family from the low 32 bits only.
	if bits.UintSize == 64 {
		upper := uintptr(1) << uint(32)
		for _, family := range []uintptr{unix.AF_UNIX, unix.AF_VSOCK} {
			fd, _, errno := syscall.RawSyscall(unix.SYS_SOCKET, upper|family, unix.SOCK_STREAM, 0)
			if errno == 0 {
				_ = unix.Close(int(fd))
			}
			fmt.Fprintf(os.Stdout, "socket-upper-bits %d %d\n", family, errno)
		}
	}

	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_AUDIT)
	if err == nil {
		_ = unix.Close(fd)
	}
	fmt.Fprintf(os.Stdout, "netlink-audit %d %d\n", unix.AF_NETLINK, errnoOf(err))

	os.Exit(0)
}

// runSocketHelper loads the default profile into a child process and returns
// the errno of each probe, keyed by "<probe> <family>".
func runSocketHelper(t *testing.T, mode string) map[string]syscall.Errno {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=^TestDefaultProfileSocketHelper$")
	cmd.Env = append(os.Environ(), socketHelperEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "helper output:\n%s", out)

	results := map[string]syscall.Errno{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		var probe string
		var family, errno int
		if n, _ := fmt.Sscanf(scanner.Text(), "%s %d %d", &probe, &family, &errno); n == 3 {
			results[fmt.Sprintf("%s %d", probe, family)] = syscall.Errno(errno)
		}
	}
	require.NotEmpty(t, results, "no probe results in helper output:\n%s", out)
	return results
}

// TestDefaultProfileSockets loads the default profile into a process and checks
// the error that socket(2) and socketpair(2) return for each address family.
// Families outside allowedSocketDomains must fail with the profile default
// (ENOSYS), except AF_VSOCK which has an explicit EPERM rule. For the allowed
// families the error comes from the kernel, which never returns ENOSYS here.
func TestDefaultProfileSockets(t *testing.T) {
	if !IsEnabled() {
		t.Skip("seccomp is not supported")
	}

	for _, mode := range []string{"audit", "no-audit"} {
		t.Run(mode, func(t *testing.T) {
			got := runSocketHelper(t, mode)

			for family := range maxFamily {
				socketErr := got[fmt.Sprintf("socket %d", family)]
				switch {
				case family == unix.AF_VSOCK:
					require.Equal(t, syscall.EPERM, socketErr, "socket(%d)", family)
				case slices.Contains(allowedSocketDomains, uint64(family)):
					require.NotEqual(t, syscall.ENOSYS, socketErr, "socket(%d) must be allowed", family)
				default:
					require.Equal(t, syscall.ENOSYS, socketErr, "socket(%d) must be blocked", family)
				}

				pairErr := got[fmt.Sprintf("socketpair %d", family)]
				if family == unix.AF_UNIX || family == unix.AF_TIPC {
					require.NotEqual(t, syscall.ENOSYS, pairErr, "socketpair(%d) must be allowed", family)
				} else {
					require.Equal(t, syscall.ENOSYS, pairErr, "socketpair(%d) must be blocked", family)
				}
			}

			if bits.UintSize == 64 {
				// Not equal to AF_UNIX when compared as 64 bits, so not allowed,
				// but still equal to AF_VSOCK for the deny rule.
				require.Equal(t, syscall.ENOSYS, got[fmt.Sprintf("socket-upper-bits %d", unix.AF_UNIX)])
				require.Equal(t, syscall.EPERM, got[fmt.Sprintf("socket-upper-bits %d", unix.AF_VSOCK)])
			}

			auditErr := got[fmt.Sprintf("netlink-audit %d", unix.AF_NETLINK)]
			if mode == "audit" {
				require.NotEqual(t, syscall.EINVAL, auditErr, "NETLINK_AUDIT needs CAP_AUDIT_WRITE")
			} else {
				require.Equal(t, syscall.EINVAL, auditErr, "NETLINK_AUDIT must be blocked without CAP_AUDIT_WRITE")
			}
		})
	}
}
