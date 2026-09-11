//go:build linux

package passdriver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootlessUserNamespace(t *testing.T) {
	tests := []struct {
		name       string
		euid       int
		env        map[string]string
		wantUID    int
		wantGID    int
		wantNested bool
		wantError  string
	}{
		{name: "ordinary user", euid: 1000},
		{name: "rootful", euid: 0},
		{
			name:       "rootless podman namespace",
			euid:       0,
			env:        map[string]string{rootlessUIDEnv: "1000", rootlessGIDEnv: "1001"},
			wantUID:    1000,
			wantGID:    1001,
			wantNested: true,
		},
		{
			name:      "missing gid",
			euid:      0,
			env:       map[string]string{rootlessUIDEnv: "1000"},
			wantError: "both _CONTAINERS_ROOTLESS_UID and _CONTAINERS_ROOTLESS_GID must be set",
		},
		{
			name:      "invalid uid",
			euid:      0,
			env:       map[string]string{rootlessUIDEnv: "invalid", rootlessGIDEnv: "1000"},
			wantError: `invalid _CONTAINERS_ROOTLESS_UID value "invalid"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				value, found := tc.env[key]
				return value, found
			}
			uid, gid, nested, err := rootlessUserNamespace(tc.euid, lookup)
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantUID, uid)
			require.Equal(t, tc.wantGID, gid)
			require.Equal(t, tc.wantNested, nested)
		})
	}
}

func TestGPGCommandUsesNestedUserNamespace(t *testing.T) {
	env := map[string]string{rootlessUIDEnv: "1000", rootlessGIDEnv: "1001"}
	lookup := func(key string) (string, bool) {
		value, found := env[key]
		return value, found
	}

	cmd, err := gpgCommandForIdentity(context.Background(), 0, lookup, "gpg-custom", "--decrypt", "secret.gpg")
	require.NoError(t, err)
	require.Equal(t, []string{
		"unshare",
		"--user",
		"--map-user=1000",
		"--map-group=1001",
		"--",
		"gpg-custom",
		"--decrypt",
		"secret.gpg",
	}, cmd.Args)
}
