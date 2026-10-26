//go:build !linux

package systemd

import "errors"

func RunsOnSystemd() bool {
	return false
}

func MovePauseProcessToScope(pausePidPath string) {}

func MoveMountProcessToScope(pid int) error {
	return errors.New("MoveMountProcessToScope not supported on this OS")
}

func RunUnderSystemdScope(pid int, slice string, unitName string) error {
	return errors.New("RunUnderSystemdScope not supported on this OS")
}
