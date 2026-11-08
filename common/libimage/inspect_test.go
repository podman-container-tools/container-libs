//go:build !remote

package libimage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/config"
	"go.podman.io/image/v5/manifest"
)

func TestInspectOCIHealthcheck(t *testing.T) {
	runtime := testNewRuntime(t)

	ctx := context.Background()
	images, err := runtime.Pull(
		ctx,
		"oci-archive:testdata/oci-healthcheck.tar",
		config.PullPolicyAlways,
		&PullOptions{},
	)
	require.NoError(t, err)
	require.Len(t, images, 1)

	data, err := images[0].Inspect(ctx, nil)
	require.NoError(t, err)

	expected := &manifest.Schema2HealthConfig{
		Test: []string{"CMD-SHELL", "true"},
	}
	require.Equal(t, expected, data.HealthCheck)
}
