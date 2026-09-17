package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"
)

func TestBuildLayerInfosForCopy(t *testing.T) {
	manifestInfos := []manifest.LayerInfo{
		{Digest: "sha256:6a5a5368e0c2d3e5909184fa28ddfd56072e7ff3ee9a945876f7eee5896ef5bb", Size: -1, Annotations: map[string]string{
			"io.github.containers.zstd-chunked.manifest-checksum": "tocDigest",
			"io.github.containers.zstd-chunked.manifest-position": "somewhere",
			"io.github.containers.zstd-chunked.tarsplit-position": "somewhereElse",
		}, EmptyLayer: false},
		{Digest: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", Size: -1, EmptyLayer: true},
		{Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: -1, URLs: []string{"https://example.com/layer3"}, Annotations: map[string]string{"key": "value3"}, EmptyLayer: true},
		{Digest: "sha256:1bbf5d58d24c47512e234a5623474acf65ae00d4d1414272a893204f44cc680c", Size: -1, URLs: []string{"https://example.com/layer4"}, Annotations: map[string]string{"key": "value4"}, EmptyLayer: false},
		{Digest: "sha256:5555555555555555555555555555555555555555555555555555555555555555", Size: -1, EmptyLayer: true},
	}
	physicalInfos := []layerForCopy{
		{digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111", size: 111, mediaType: manifest.DockerV2SchemaLayerMediaTypeUncompressed},
		{digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222", size: 222, mediaType: manifest.DockerV2SchemaLayerMediaTypeUncompressed},
	}

	// Success
	res, err := buildLayerInfosForCopy(manifestInfos, physicalInfos, manifest.DockerV2Schema2LayerMediaType)
	require.NoError(t, err)
	assert.Equal(t, []types.BlobInfo{
		{Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111", Size: 111, MediaType: manifest.DockerV2SchemaLayerMediaTypeUncompressed},
		{Digest: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", Size: 32, MediaType: manifest.DockerV2Schema2LayerMediaType},
		{Digest: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", Size: 32, URLs: []string{"https://example.com/layer3"}, Annotations: map[string]string{"key": "value3"}, MediaType: manifest.DockerV2Schema2LayerMediaType},
		{Digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222", Size: 222, URLs: []string{"https://example.com/layer4"}, Annotations: map[string]string{"key": "value4"}, MediaType: manifest.DockerV2SchemaLayerMediaTypeUncompressed},
		{Digest: "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", Size: 32, MediaType: manifest.DockerV2Schema2LayerMediaType},
	}, res)

	// PhysicalInfos too short
	_, err = buildLayerInfosForCopy(manifestInfos, physicalInfos[:len(physicalInfos)-1], manifest.DockerV2Schema2LayerMediaType)
	assert.Error(t, err)

	// PhysicalInfos too long
	_, err = buildLayerInfosForCopy(manifestInfos, append(physicalInfos, physicalInfos[0]), manifest.DockerV2Schema2LayerMediaType)
	assert.Error(t, err)
}
