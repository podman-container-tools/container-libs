package systemd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	systemdDbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"
	"go.podman.io/storage/pkg/unshare"
	"golang.org/x/sys/unix"
)

var (
	runsOnSystemdOnce sync.Once
	runsOnSystemd     bool

	isUnifiedOnce sync.Once
	isUnified     bool
	isUnifiedErr  error
)

// RunsOnSystemd returns whether the system is using systemd.
func RunsOnSystemd() bool {
	runsOnSystemdOnce.Do(func() {
		// per sd_booted(3), check for this dir
		fd, err := os.Stat("/run/systemd/system")
		runsOnSystemd = err == nil && fd.IsDir()
	})
	return runsOnSystemd
}

func moveProcessPIDFileToScope(pidPath, slice, scope string) error {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		// do not raise an error if the file doesn't exist
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot read pid file: %w", err)
	}
	pid, err := strconv.ParseUint(string(data), 10, 0)
	if err != nil {
		return fmt.Errorf("cannot parse pid file %s: %w", pidPath, err)
	}

	return moveProcessToScope(int(pid), slice, scope)
}

func moveProcessToScope(pid int, slice, scope string) error {
	err := RunUnderSystemdScope(pid, slice, scope)
	// If the PID is not valid anymore, do not return an error.
	if dbusErr, ok := err.(dbus.Error); ok {
		if dbusErr.Name == "org.freedesktop.DBus.Error.UnixProcessIdUnknown" {
			return nil
		}
	}
	return err
}

// MoveRootlessNetnsProcessToUserSlice moves the pasta process for the rootless netns
// into a different scope so that systemd does not kill it with a container.
func MoveRootlessNetnsProcessToUserSlice(pid int) error {
	randBytes := make([]byte, 4)
	_, err := rand.Read(randBytes)
	if err != nil {
		return err
	}
	return moveProcessToScope(pid, "user.slice", fmt.Sprintf("rootless-netns-%x.scope", randBytes))
}

// MoveMountProcessToScope moves the mount program process (such as fuse-overlayfs)
// into a separate scope under user.slice so that systemd does not kill it when
// the service or container that triggered the mount stops.
func MoveMountProcessToScope(pid int) error {
	randBytes := make([]byte, 4)
	_, err := rand.Read(randBytes)
	if err != nil {
		return err
	}
	return moveProcessToScope(pid, "user.slice", fmt.Sprintf("fuse-overlayfs-%x.scope", randBytes))
}

// MovePauseProcessToScope moves the pause process used for rootless mode to keep the namespaces alive to
// a separate scope.
func MovePauseProcessToScope(pausePidPath string) {
	var err error

	for range 10 {
		randBytes := make([]byte, 4)
		_, err = rand.Read(randBytes)
		if err != nil {
			logrus.Errorf("failed to read random bytes: %v", err)
			continue
		}
		err = moveProcessPIDFileToScope(pausePidPath, "user.slice", fmt.Sprintf("podman-pause-%x.scope", randBytes))
		if err == nil {
			return
		}
	}

	if err != nil {
		_, err2 := IsCgroup2UnifiedMode()
		if err2 != nil {
			logrus.Warnf("Failed to detect if running with cgroup unified: %v", err)
		}
		if RunsOnSystemd() {
			logrus.Warnf("Failed to add pause process to systemd sandbox cgroup: %v", err)
		}
	}
}

// RunUnderSystemdScope adds the specified pid to a systemd scope.
func RunUnderSystemdScope(pid int, slice string, unitName string) error {
	var conn *systemdDbus.Conn
	var err error

	if unshare.GetRootlessUID() != 0 {
		conn, err = UserConnection(unshare.GetRootlessUID())
		if err != nil {
			return err
		}
	} else {
		conn, err = systemdDbus.NewWithContext(context.Background())
		if err != nil {
			return err
		}
	}
	defer conn.Close()
	properties := []systemdDbus.Property{
		systemdDbus.PropSlice(slice),
		newProp("PIDs", []uint32{uint32(pid)}),
		newProp("Delegate", true),
		newProp("DefaultDependencies", false),
	}
	ch := make(chan string)
	_, err = conn.StartTransientUnitContext(context.Background(), unitName, "replace", properties, ch)
	if err != nil {
		// On errors check if the cgroup already exists, if it does move the process there
		if props, err := conn.GetUnitTypePropertiesContext(context.Background(), unitName, "Scope"); err == nil {
			if cgroup, ok := props["ControlGroup"].(string); ok && cgroup != "" {
				if err := MoveUnderCgroup(cgroup, "", []uint32{uint32(pid)}); err == nil {
					return nil
				}
				// On errors return the original error message we got from StartTransientUnit.
			}
		}
		return err
	}

	// Block until job is started
	<-ch

	return nil
}

func newProp(name string, units any) systemdDbus.Property {
	return systemdDbus.Property{
		Name:  name,
		Value: dbus.MakeVariant(units),
	}
}

func dbusAuthConnection(uid int, createBus func(opts ...dbus.ConnOption) (*dbus.Conn, error)) (*dbus.Conn, error) {
	conn, err := createBus()
	if err != nil {
		return nil, err
	}

	methods := []dbus.Auth{dbus.AuthExternal(strconv.Itoa(uid))}

	err = conn.Auth(methods)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

// UserConnection returns a user connection to D-BUS.
func UserConnection(uid int) (*systemdDbus.Conn, error) {
	return systemdDbus.NewConnection(func() (*dbus.Conn, error) {
		return dbusAuthConnection(uid, dbus.SessionBusPrivateNoAutoStartup)
	})
}

// IsCgroup2UnifiedMode returns whether we are running in cgroup v2 unified mode.
func IsCgroup2UnifiedMode() (bool, error) {
	isUnifiedOnce.Do(func() {
		var st syscall.Statfs_t
		if err := syscall.Statfs("/sys/fs/cgroup", &st); err != nil {
			isUnified, isUnifiedErr = false, err
		} else {
			isUnified, isUnifiedErr = st.Type == unix.CGROUP2_SUPER_MAGIC, nil
		}
	})
	return isUnified, isUnifiedErr
}

// MoveUnderCgroup moves a group of processes to a new cgroup.
// If cgroup is the empty string, then the current calling process cgroup is used.
// If processes is empty, then the processes from the current cgroup are moved.
func MoveUnderCgroup(cgroup, subtree string, processes []uint32) error {
	procFile := "/proc/self/cgroup"
	f, err := os.Open(procFile)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = IsCgroup2UnifiedMode()
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return fmt.Errorf("cannot parse cgroup line %q", line)
		}

		// root cgroup, skip it
		if parts[2] == "/" && parts[1] != "" {
			continue
		}

		cgroupRoot := "/sys/fs/cgroup"
		if parts[1] != "" {
			// Assume the controller is mounted at /sys/fs/cgroup/$CONTROLLER.
			controller := strings.TrimPrefix(parts[1], "name=")
			cgroupRoot = filepath.Join(cgroupRoot, controller)
		}

		parentCgroup := cgroup
		if parentCgroup == "" {
			parentCgroup = parts[2]
		}
		newCgroup := filepath.Join(cgroupRoot, parentCgroup, subtree)
		if err := os.MkdirAll(newCgroup, 0o755); err != nil && !os.IsExist(err) {
			return err
		}

		procsFile, err := os.OpenFile(filepath.Join(newCgroup, "cgroup.procs"), os.O_RDWR, 0o755)
		if err != nil {
			return err
		}
		defer procsFile.Close()

		if len(processes) > 0 {
			for _, pid := range processes {
				if _, err := fmt.Fprintf(procsFile, "%d\n", pid); err != nil {
					logrus.Debugf("Cannot move process %d to cgroup %q: %v", pid, newCgroup, err)
				}
			}
		} else {
			processesData, err := os.ReadFile(filepath.Join(cgroupRoot, parts[2], "cgroup.procs"))
			if err != nil {
				return err
			}
			for pid := range bytes.SplitSeq(processesData, []byte("\n")) {
				if len(pid) == 0 {
					continue
				}
				if _, err := procsFile.Write(pid); err != nil {
					logrus.Debugf("Cannot move process %s to cgroup %q: %v", string(pid), newCgroup, err)
				}
			}
		}
	}
	return scanner.Err()
}
