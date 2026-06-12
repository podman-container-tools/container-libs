package tarfile

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/pkg/blobinfocache/memory"
	"go.podman.io/image/v5/types"
)

func TestSourcePrepareLayerData(t *testing.T) {
	// Just a smoke test to verify prepareLayerData does not crash on missing data
	for _, c := range []struct {
		config     string
		shouldFail bool
	}{
		{`{}`, true},             // No RootFS entry: can fail, shouldn’t crash
		{`{"rootfs":{}}`, false}, // Useless no-layer configuration
	} {
		cache := memory.New()
		var tarfileBuffer bytes.Buffer
		ctx := context.Background()

		writer := NewWriter(&tarfileBuffer)
		dest := NewDestination(nil, writer, "transport name", nil, nil)
		// No layers
		configInfo, err := dest.PutBlob(ctx, strings.NewReader(c.config),
			types.BlobInfo{Size: -1}, cache, true)
		require.NoError(t, err, c.config)
		manifest, err := manifest.Schema2FromComponents(
			manifest.Schema2Descriptor{
				MediaType: manifest.DockerV2Schema2ConfigMediaType,
				Size:      configInfo.Size,
				Digest:    configInfo.Digest,
			}, []manifest.Schema2Descriptor{}).Serialize()
		require.NoError(t, err, c.config)
		err = dest.PutManifest(ctx, manifest, nil)
		require.NoError(t, err, c.config)
		err = writer.Close()
		require.NoError(t, err, c.config)

		reader, err := NewReaderFromStream(nil, &tarfileBuffer)
		require.NoError(t, err, c.config)
		src := NewSource(reader, true, "transport name", nil, -1)
		require.NoError(t, err, c.config)
		defer src.Close()
		configStream, _, err := src.GetBlob(ctx, types.BlobInfo{
			Digest: configInfo.Digest,
			Size:   -1,
		}, cache)
		if !c.shouldFail {
			require.NoError(t, err, c.config)
			config2, err := io.ReadAll(configStream)
			require.NoError(t, err, c.config)
			assert.Equal(t, []byte(c.config), config2, c.config)
		} else {
			assert.Error(t, err, c.config)
		}
	}
}

func TestSourceGetBlobSymlinkLayerSizeMatchesBytesReturned(t *testing.T) {
	ctx := context.Background()
	cache := memory.New()

	layerBytes := []byte("not empty")
	diffID := digest.FromBytes(layerBytes)
	configBytes := []byte(`{"rootfs":{"type":"layers","diff_ids":["` + diffID.String() + `"]}}`)

	manifestBytes, err := json.Marshal([]ManifestItem{{Config: "config.json", Layers: []string{"layer-link.tar"}}})
	require.NoError(t, err)

	var tarfileBuffer bytes.Buffer
	tw := tar.NewWriter(&tarfileBuffer)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "manifest.json", body: manifestBytes},
		{name: "config.json", body: configBytes},
		{name: "layer.tar", body: layerBytes},
	} {
		err = tw.WriteHeader(&tar.Header{
			Name: entry.name,
			Mode: 0o644,
			Size: int64(len(entry.body)),
		})
		require.NoError(t, err)
		_, err = tw.Write(entry.body)
		require.NoError(t, err)
	}

	err = tw.WriteHeader(&tar.Header{
		Name:     "layer-link.tar",
		Typeflag: tar.TypeSymlink,
		Linkname: "layer.tar",
		Mode:     0o777,
	})
	require.NoError(t, err)

	err = tw.Close()
	require.NoError(t, err)

	reader, err := NewReaderFromStream(nil, &tarfileBuffer)
	require.NoError(t, err)
	src := NewSource(reader, true, "transport name", nil, -1)
	defer src.Close()

	layerStream, reportedSize, err := src.GetBlob(ctx, types.BlobInfo{
		Digest: diffID,
		Size:   -1,
	}, cache)
	require.NoError(t, err)
	defer layerStream.Close()

	readBytes, err := io.ReadAll(layerStream)
	require.NoError(t, err)
	assert.Equal(t, layerBytes, readBytes)
	assert.Equal(t, int64(len(layerBytes)), reportedSize)
}
