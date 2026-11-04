package seccomp

import (
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

// Only AF_UNIX and AF_TIPC implement socketpair(2), so nothing else is allowed.
func TestDefaultProfileSocketpairDomains(t *testing.T) {
	var got []uint64
	for _, call := range DefaultProfile().Syscalls {
		if call.Action != ActAllow || !slices.Contains(call.Names, "socketpair") {
			continue
		}
		if len(call.Args) != 1 || call.Args[0].Index != 0 || call.Args[0].Op != OpEqualTo {
			t.Fatalf("socketpair allow rule must be one equality check on arg0, got %+v", call.Args)
		}
		got = append(got, call.Args[0].Value)
	}
	slices.Sort(got)
	want := []uint64{unix.AF_UNIX, unix.AF_TIPC}
	if !slices.Equal(got, want) {
		t.Errorf("socketpair allowed domains = %v, want %v", got, want)
	}
}

// The kernel reads the int arguments of socket(2) from the low 32 bits, but
// seccomp compares all 64, so a deny rule on a value has to mask the rest.
func TestDefaultProfileSocketDenyRulesMaskUpperBits(t *testing.T) {
	for _, call := range DefaultProfile().Syscalls {
		if call.Action != ActErrno || !slices.Contains(call.Names, "socket") {
			continue
		}
		for _, arg := range call.Args {
			if arg.Op != OpMaskedEqual || arg.Value != 0xffffffff {
				t.Errorf("socket deny rule %v: arg%d must use %s with mask 0xffffffff, got %s %#x",
					call.Errno, arg.Index, OpMaskedEqual, arg.Op, arg.Value)
			}
		}
	}
}
