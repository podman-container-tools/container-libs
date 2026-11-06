package libartifact

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/libartifact/types"
)

func TestNewArtifactReference(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectError   bool
		errorContains string
		errorIs       error
		expectedRef   string
	}{
		{
			name:        "valid reference with tag",
			input:       "quay.io/podman/machine-os:5.1",
			expectError: false,
			expectedRef: "quay.io/podman/machine-os:5.1",
		},
		{
			name:        "valid reference docker.io",
			input:       "docker.io/library/nginx:latest",
			expectError: false,
		},
		{
			name:          "empty string",
			input:         "",
			expectError:   true,
			errorContains: "invalid reference format",
		},
		{
			name:          "malformed reference",
			input:         "invalid::reference",
			expectError:   true,
			errorContains: "invalid reference format",
		},
		{
			name:        "no tag adds latest",
			input:       "quay.io/machine-os/podman",
			expectError: false,
			expectedRef: "quay.io/machine-os/podman:latest",
		},
		{
			name:        "valid digest",
			input:       "quay.io/machine-os/podman@sha256:8b96f36deaf1d2713858eebd9ef2fee9610df8452fbd083bbfa7dca66d6fcd0b",
			expectError: false,
		},
		{
			name:          "partial digest",
			input:         "quay.io/machine-os/podman@sha256:8b96f36deaf1d2",
			expectError:   true,
			errorContains: "invalid reference format",
		},
		{
			name:          "64-byte hex ID",
			input:         "84ddb405470e733d0202d6946e48fc75a7ee231337bdeb31a8579407a7052d9e",
			expectError:   true,
			errorContains: "cannot specify 64-byte hexadecimal strings",
		},
		{
			name:          "shortname rejected",
			input:         "machine-os:latest",
			expectError:   true,
			errorContains: "repository name must be canonical",
		},
		{
			name:        "tagged and digested",
			input:       "quay.io/machine-os/podman:latest@sha256:8b96f36deaf1d2713858eebd9ef2fee9610df8452fbd083bbfa7dca66d6fcd0b",
			expectError: true,
			errorIs:     types.ErrTaggedAndDigested,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ar, err := NewArtifactReference(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				if len(tt.errorContains) > 0 {
					assert.ErrorContains(t, err, tt.errorContains)
				}
				if tt.errorIs != nil {
					assert.ErrorIs(t, err, tt.errorIs)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, ar.ref)
				if len(tt.expectedRef) > 0 {
					assert.Equal(t, tt.expectedRef, ar.ref.String())
				}
			}
		})
	}
}

func TestArtifactReference_DigestReference(t *testing.T) {
	ar, err := NewArtifactReference("quay.io/myrepo/myart:latest")
	require.NoError(t, err)

	d := digest.FromString("artifact-a")
	s, err := ar.DigestReference(d)
	require.NoError(t, err)
	assert.Equal(t, "quay.io/myrepo/myart@"+d.String(), s)
}
