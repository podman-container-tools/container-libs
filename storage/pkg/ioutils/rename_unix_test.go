//go:build !windows

package ioutils

import "os"

// openShareDelete opens a file for reading. On Unix any open file can be
// renamed over.
func openShareDelete(path string) (*os.File, error) {
	return os.Open(path)
}
