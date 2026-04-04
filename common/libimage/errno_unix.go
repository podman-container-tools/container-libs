//go:build !windows

package libimage

import "syscall"

const ErrNoSpace = syscall.ENOSPC
