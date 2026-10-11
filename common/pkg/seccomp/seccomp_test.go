//go:build seccomp

// SPDX-License-Identifier: Apache-2.0

// Copyright 2013-2018 Docker, Inc.

package seccomp

import (
	"os"
	"slices"
	"testing"

	"github.com/opencontainers/runtime-tools/generate"
)

func TestLoadProfile(t *testing.T) {
	f, err := os.ReadFile("fixtures/example.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := generate.New("linux")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(string(f), g.Config); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultProfile(t *testing.T) {
	f, err := os.ReadFile("seccomp.json")
	if err != nil {
		t.Fatal(err)
	}
	g, err := generate.New("linux")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(string(f), g.Config); err != nil {
		t.Fatal(err)
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
