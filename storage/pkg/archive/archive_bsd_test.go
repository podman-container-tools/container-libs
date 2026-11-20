//go:build netbsd || freebsd || darwin

package archive

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertCtimeMatches asserts that fi1 and fi2 have the same ctime.
func assertCtimeMatches(t *testing.T, fi1, fi2 os.FileInfo) {
	t.Helper()
	st1, ok := fi1.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	st2, ok := fi2.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	assert.Equal(t, st1.Ctimespec, st2.Ctimespec)
}

func assertAtime(t *testing.T, atime time.Time, fi os.FileInfo) {
	t.Helper()
	st, ok := fi.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	assert.Equal(t, atime, time.Unix(int64(st.Atimespec.Sec), int64(st.Atimespec.Nsec))) //nolint:unconvert // The field is int32 on some platforms
}
