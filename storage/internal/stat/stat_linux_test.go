//go:build linux

package stat

import (
	"syscall"
	"testing"
)

func platformTestFromStatT(t *testing.T, stat *syscall.Stat_t, s *StatT) {
	if stat.Mode != s.Mode() {
		t.Fatal("got invalid mode")
	}
	if stat.Mtim != s.Mtim() {
		t.Fatal("got invalid mtim")
	}
}
