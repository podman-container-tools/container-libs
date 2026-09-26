//go:build !linux

package systemd

import (
	"context"
	"errors"
)

func RunsOnSystemd() bool {
	return false
}

func MovePauseProcessToScope(pausePidPath string) {}

func RunUnderSystemdScope(pid int, slice string, unitName string) error {
	return errors.New("RunUnderSystemdScope not supported on this OS")
}

// RunUnderSystemdScopeContext is not supported on this OS.
func RunUnderSystemdScopeContext(ctx context.Context, pid int, slice string, unitName string) error {
	return errors.New("RunUnderSystemdScopeContext not supported on this OS")
}
