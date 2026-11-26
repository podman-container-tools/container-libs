//go:build seccomp

// SPDX-License-Identifier: Apache-2.0

// Copyright 2013-2018 Docker, Inc.

package seccomp

import (
	"os"
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

func TestLoadProfileNullEntries(t *testing.T) {
	for _, tc := range []struct {
		name, profile, want string
	}{
		{
			name:    "null syscall entry",
			profile: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[null]}`,
			want:    "syscall entry 0 in the seccomp profile is null",
		},
		{
			name:    "null argument",
			profile: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read"],"action":"SCMP_ACT_ALLOW"},{"names":["write"],"action":"SCMP_ACT_ALLOW","args":[{"index":0,"value":1,"op":"SCMP_CMP_EQ"},null]}]}`,
			want:    "argument 1 of syscall entry 1 in the seccomp profile is null",
		},
		{
			name:    "null argument in an entry skipped by the architecture filter",
			profile: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read"],"action":"SCMP_ACT_ALLOW","includes":{"arches":["nonexistent"]},"args":[null]}]}`,
			want:    "argument 0 of syscall entry 0 in the seccomp profile is null",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := generate.New("linux")
			if err != nil {
				t.Fatal(err)
			}
			_, err = LoadProfile(tc.profile, g.Config)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("LoadProfile: got error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadProfileFromConfigNullEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile *Seccomp
		want    string
	}{
		{
			name:    "nil syscall entry",
			profile: &Seccomp{DefaultAction: ActErrno, Syscalls: []*Syscall{nil}},
			want:    "syscall entry 0 in the seccomp profile is null",
		},
		{
			name: "nil argument",
			profile: &Seccomp{DefaultAction: ActErrno, Syscalls: []*Syscall{
				{Names: []string{"write"}, Action: ActAllow, Args: []*Arg{nil}},
			}},
			want: "argument 0 of syscall entry 0 in the seccomp profile is null",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := generate.New("linux")
			if err != nil {
				t.Fatal(err)
			}
			_, err = LoadProfileFromConfig(tc.profile, g.Config)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("LoadProfileFromConfig: got error %v, want %q", err, tc.want)
			}
		})
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
