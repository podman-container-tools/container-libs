//go:build !remote && !windows

package libimage

import "golang.org/x/sys/unix"

// diskFree returns the number of bytes available to unprivileged users on
// the file system containing path.
func diskFree(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	// _nolint_: Whether this conversion is required is hardware- and OS-dependent: the value might be uint64 on Linux, int64 on macOS.
	// So, this might trigger either "uncovert" (if the conversion is unnecessary) or "nolintlint" (if it is required)
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert,nolintlint
}
