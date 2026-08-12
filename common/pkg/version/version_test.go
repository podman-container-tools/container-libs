package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseDlocateList(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		want   string
		wantOk bool
	}{
		{
			name:   "package line in dpkg format",
			out:    "ii  podman  4.9.3-1  arm64  engine to run OCI containers\n",
			want:   "podman_4.9.3-1",
			wantOk: true,
		},
		{
			name:   "last package line is used",
			out:    "ii  crun  1.14-1  arm64  OCI runtime\nii  podman  4.9.3-1  arm64  engine\n",
			want:   "podman_4.9.3-1",
			wantOk: true,
		},
		{
			name:   "exactly the three fields that are read",
			out:    "ii  podman  4.9.3-1\n",
			want:   "podman_4.9.3-1",
			wantOk: true,
		},
		{
			name:   "line with too few fields to name a version",
			out:    "ii  podman\n",
			wantOk: false,
		},
		{
			name:   "no trailing newline leaves nothing to read",
			out:    "ii  podman  4.9.3-1  arm64  engine",
			wantOk: false,
		},
		{
			name:   "empty output",
			out:    "",
			wantOk: false,
		},
		{
			name:   "newline only",
			out:    "\n",
			wantOk: false,
		},
		{
			name:   "whitespace only line",
			out:    "   \n",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDlocateList([]byte(tt.out))
			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
