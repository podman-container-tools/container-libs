package layout

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/pkg/blobinfocache/memory"
	"go.podman.io/image/v5/types"
)

var _ private.ImageSource = (*ociImageSource)(nil)

const RemoteLayerContent = "This is the remote layer content"

var httpServerAddr string

func TestMain(m *testing.M) {
	httpServer, err := startRemoteLayerServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting test TLS server: %v", err.Error())
		os.Exit(1)
	}

	httpServerAddr = strings.Replace(httpServer.URL, "127.0.0.1", "localhost", 1)
	code := m.Run()
	httpServer.Close()
	os.Exit(code)
}

// Fixture digest for delete_image_only_one_image / shared_blobs layouts used by testConsumeImage.
const testConsumeImageManifestDigest = digest.Digest("sha256:eaa95f3cfaac07c8a5153eb77c933269586ad0226c83405776be08547e4d2a18")

// testConsumeImage ensures that a single-platform image at ref can be read,
// and that manifest and layers have the expected contents.
//
// The config is not read/validated: the transport does not really differentiate
// between configs and layers, so testing layers is sufficient.
func testConsumeImage(t *testing.T, ref types.ImageReference, sys *types.SystemContext, layerDigests []digest.Digest) {
	src, err := ref.NewImageSource(context.Background(), sys)
	require.NoError(t, err)
	defer src.Close()

	manifestBlob, _, err := src.GetManifest(context.Background(), nil)
	require.NoError(t, err)
	md, err := manifest.Digest(manifestBlob)
	require.NoError(t, err)
	assert.Equal(t, testConsumeImageManifestDigest, md)

	var ociManifest imgspecv1.Manifest
	err = json.Unmarshal(manifestBlob, &ociManifest)
	require.NoError(t, err)
	assert.Len(t, ociManifest.Layers, len(layerDigests))

	cache := memory.New()
	for i := range ociManifest.Layers {
		blob, _, err := src.GetBlob(context.Background(), types.BlobInfo{
			Digest: ociManifest.Layers[i].Digest,
			Size:   ociManifest.Layers[i].Size,
		}, cache)
		require.NoError(t, err)
		defer blob.Close()

		blobContent, err := io.ReadAll(blob)
		require.NoError(t, err)
		assert.Equal(t, layerDigests[i], digest.FromBytes(blobContent))
	}
}

func TestImageSourceFSAccess(t *testing.T) {
	// This exercises the success paths of doing file I/O in newImageSource + GetManifest + GetBlob

	layerDigests := []digest.Digest{"sha256:0c8b263642b51b5c1dc40fe402ae2e97119c6007b6e52146419985ec1f0092dc"}

	// Base case
	ref, err := NewReference("fixtures/delete_image_only_one_image", "latest")
	require.NoError(t, err)
	testConsumeImage(t, ref, nil, layerDigests)

	// OCISharedBlobDirPath
	ref, err = NewReference("fixtures/delete_image_shared_blobs_dir", "latest")
	require.NoError(t, err)
	testConsumeImage(t, ref,
		&types.SystemContext{OCISharedBlobDirPath: "fixtures/delete_image_shared_blobs_dir/shared_blobs"},
		layerDigests)

	// Reader
	root, err := os.OpenRoot("fixtures/delete_image_only_one_image")
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	ref, err = reader.NewReference("fixtures/delete_image_only_one_image", "latest")
	require.NoError(t, err)
	testConsumeImage(t, ref, nil, layerDigests)

	// Reader + OCISharedBlobDirPath
	// The fixture has shared_blobs inside the layout root; copy it outside to test that the root does not constrain OCISharedBlobDirPath .
	sharedBlobsDir := t.TempDir()
	err = os.CopyFS(sharedBlobsDir, os.DirFS("fixtures/delete_image_shared_blobs_dir/shared_blobs"))
	require.NoError(t, err)
	root, err = os.OpenRoot("fixtures/delete_image_shared_blobs_dir")
	require.NoError(t, err)
	defer root.Close()
	reader = NewReaderWithRoot(root)
	ref, err = reader.NewReference("fixtures/delete_image_shared_blobs_dir", "latest")
	require.NoError(t, err)
	testConsumeImage(t, ref, &types.SystemContext{OCISharedBlobDirPath: sharedBlobsDir}, layerDigests)
}

// fixtureCopyWithSymlink returns a t.Tempdir() copy of srcDir, except that within the copy, relativeSymlinkPath is a symlink to the
// corresponding file in original srcDir.
func fixtureCopyWithSymlink(t *testing.T, srcDir, relativeSymlinkPath string) string {
	destDir := t.TempDir()
	err := os.CopyFS(destDir, os.DirFS(srcDir))
	require.NoError(t, err)
	err = os.Remove(filepath.Join(destDir, relativeSymlinkPath))
	require.NoError(t, err)
	absSrcDir, err := filepath.Abs(srcDir)
	require.NoError(t, err)
	err = os.Symlink(filepath.Join(absSrcDir, relativeSymlinkPath), filepath.Join(destDir, relativeSymlinkPath))
	require.NoError(t, err)
	return destDir
}

func TestNewImageSource(t *testing.T) {
	// This should test much more.

	// NewReaderWithRoot is enforced when reading index.json in NewImageSource.
	escapingFixture := fixtureCopyWithSymlink(t, "fixtures/delete_image_only_one_image", "index.json")
	root, err := os.OpenRoot(escapingFixture)
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	ref, err := reader.NewReference(escapingFixture, "latest")
	require.NoError(t, err)
	_, err = ref.NewImageSource(context.Background(), nil)
	assert.ErrorContains(t, err, "path escapes from parent")
}

func TestGetManifest(t *testing.T) {
	// This should test much more.

	// NewReaderWithRoot is enforced when reading manifests.
	escapingFixture := fixtureCopyWithSymlink(t, "fixtures/delete_image_only_one_image", "blobs/sha256/eaa95f3cfaac07c8a5153eb77c933269586ad0226c83405776be08547e4d2a18")
	root, err := os.OpenRoot(escapingFixture)
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	ref, err := reader.NewReference(escapingFixture, "latest")
	require.NoError(t, err)
	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	_, _, err = src.GetManifest(context.Background(), nil)
	assert.ErrorContains(t, err, "path escapes from parent")
}

func TestGetBlob(t *testing.T) {
	// This should test much more.

	// NewReaderWithRoot is enforced when reading blobs.
	escapingFixture := fixtureCopyWithSymlink(t, "fixtures/delete_image_only_one_image", "blobs/sha256/0c8b263642b51b5c1dc40fe402ae2e97119c6007b6e52146419985ec1f0092dc")
	root, err := os.OpenRoot(escapingFixture)
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	ref, err := reader.NewReference(escapingFixture, "latest")
	require.NoError(t, err)
	src, err := ref.NewImageSource(context.Background(), nil)
	require.NoError(t, err)
	defer src.Close()
	_, _, err = src.GetBlob(context.Background(), types.BlobInfo{
		Digest: "sha256:0c8b263642b51b5c1dc40fe402ae2e97119c6007b6e52146419985ec1f0092dc",
		Size:   -1,
	}, memory.New())
	assert.ErrorContains(t, err, "path escapes from parent")
}

func TestGetBlobForRemoteLayers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello world")
	}))
	defer ts.Close()
	cache := memory.New()

	imageSource := createImageSource(t, &types.SystemContext{})
	defer imageSource.Close()
	layerInfo := types.BlobInfo{
		Digest: digest.FromString("Hello world"),
		Size:   -1,
		URLs: []string{
			"brokenurl",
			ts.URL,
		},
	}

	reader, _, err := imageSource.GetBlob(context.Background(), layerInfo, cache)
	require.NoError(t, err)
	defer reader.Close()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Hello world")
}

func TestGetBlobForRemoteLayersWithTLS(t *testing.T) {
	imageSource := createImageSource(t, &types.SystemContext{
		OCICertPath: "fixtures/accepted_certs",
	})
	defer imageSource.Close()
	cache := memory.New()

	layer, size, err := imageSource.GetBlob(context.Background(), types.BlobInfo{
		URLs: []string{httpServerAddr},
	}, cache)
	require.NoError(t, err)

	layerContent, _ := io.ReadAll(layer)
	assert.Equal(t, RemoteLayerContent, string(layerContent))
	assert.Equal(t, int64(len(RemoteLayerContent)), size)
}

func TestGetBlobForRemoteLayersOnTLSFailure(t *testing.T) {
	imageSource := createImageSource(t, &types.SystemContext{
		OCICertPath: "fixtures/rejected_certs",
	})
	defer imageSource.Close()
	cache := memory.New()
	layer, size, err := imageSource.GetBlob(context.Background(), types.BlobInfo{
		URLs: []string{httpServerAddr},
	}, cache)

	require.Error(t, err)
	assert.Nil(t, layer)
	assert.Equal(t, int64(0), size)
}

func remoteLayerContent(w http.ResponseWriter, req *http.Request) {
	fmt.Fprint(w, RemoteLayerContent)
}

func startRemoteLayerServer() (*httptest.Server, error) {
	certBytes, err := os.ReadFile("fixtures/accepted_certs/cert.cert")
	if err != nil {
		return nil, err
	}

	clientCertPool := x509.NewCertPool()
	if !clientCertPool.AppendCertsFromPEM(certBytes) {
		return nil, fmt.Errorf("Could not append certificate")
	}

	cert, err := tls.LoadX509KeyPair("fixtures/accepted_certs/cert.cert", "fixtures/accepted_certs/cert.key")
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		// Reject any TLS certificate that cannot be validated
		ClientAuth: tls.RequireAndVerifyClientCert,
		// Ensure that we only use our "CA" to validate certificates
		ClientCAs:    clientCertPool,
		Certificates: []tls.Certificate{cert},
	}

	httpServer := httptest.NewUnstartedServer(http.HandlerFunc(remoteLayerContent))
	httpServer.TLS = tlsConfig

	httpServer.StartTLS()

	return httpServer, nil
}

func createImageSource(t *testing.T, sys *types.SystemContext) types.ImageSource {
	imageRef, err := NewReference("fixtures/manifest", "")
	require.NoError(t, err)
	imageSource, err := imageRef.NewImageSource(context.Background(), sys)
	require.NoError(t, err)
	return imageSource
}

func TestGetLocalBlobPath(t *testing.T) {
	tmpDir := loadFixture(t, "delete_image_multiple_images")
	ref, err := NewReference(tmpDir, "latest")
	require.NoError(t, err)

	src, err := ref.NewImageSource(context.Background(), &types.SystemContext{})
	require.NoError(t, err)
	defer src.Close()

	// success cases
	for _, dig := range []digest.Digest{
		"sha256:a2f798327b3f25e3eff54badcb769953de235e62e3e32051d57a5e66246de4a1",
		"sha256:557ac7d133b7770216a8101268640edf4e88beab1b4e1e1bfc9b1891a1cab861",
	} {
		path, err := GetLocalBlobPath(context.Background(), src, dig)
		require.NoError(t, err)
		algo, hash, _ := strings.Cut(string(dig), ":")
		expect := filepath.Join(tmpDir, "blobs", algo, hash)
		assert.Equal(t, expect, path)
	}

	// error cases
	for _, dig := range []digest.Digest{
		// Invalid digest must error.
		"sha256:as",
		// Valid digest but they don't exist in the oci layout thus they must error.
		"sha256:abababababababababababababababababababababababababababababababab",
		"sha512:a2f798327b3f25e3eff54badcb769953de235e62e3e32051d57a5e66246de4a1557ac7d133b7770216a8101268640edf4e88beab1b4e1e1bfc9b1891a1cab861",
	} {
		_, err = GetLocalBlobPath(context.Background(), src, dig)
		require.Error(t, err)
	}

	// NewReaderWithRoot references refuse to return Root-unrestricted paths
	root, err := os.OpenRoot(tmpDir)
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	ref, err = reader.NewReference(tmpDir, "latest")
	require.NoError(t, err)
	src, err = ref.NewImageSource(context.Background(), &types.SystemContext{})
	require.NoError(t, err)
	defer src.Close()
	_, err = GetLocalBlobPath(context.Background(), src, "sha256:a2f798327b3f25e3eff54badcb769953de235e62e3e32051d57a5e66246de4a1")
	assert.ErrorContains(t, err, "root-restricted configurations")
}

func TestLoadManifestDescriptor(t *testing.T) {
	ref, err := NewIndexReference("fixtures/two_images_manifest", 0)
	assert.NoError(t, err)
	root, err := os.OpenRoot("fixtures/two_images_manifest")
	require.NoError(t, err)
	defer root.Close()
	reader := NewReaderWithRoot(root)
	rootRef, err := reader.NewIndexReference("fixtures/two_images_manifest", 0)
	require.NoError(t, err)
	for _, ref := range []types.ImageReference{ref, rootRef} {
		res, err := LoadManifestDescriptor(ref)
		assert.NoError(t, err)
		assert.Equal(t, imgspecv1.Descriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    "sha256:e692418e4cbaf90ca69d05a66403747baa33ee08806650b51fab815ad7fc331f",
			Size:      7143,
			Platform: &imgspecv1.Platform{
				Architecture: "ppc64le",
				OS:           "linux",
			},
		}, res)
	}

	// Out of bounds
	ref, err = NewIndexReference("fixtures/two_images_manifest", 6)
	assert.NoError(t, err)
	_, err = LoadManifestDescriptor(ref)
	assert.Error(t, err)
	assert.Equal(t, "index 6 is too large, only 2 entries available", err.Error())

	// NewReaderWithRoot is enforced.
	escapingFixture := fixtureCopyWithSymlink(t, "fixtures/two_images_manifest", "index.json")
	root, err = os.OpenRoot(escapingFixture)
	require.NoError(t, err)
	defer root.Close()
	reader = NewReaderWithRoot(root)
	ref, err = reader.NewIndexReference(escapingFixture, 0)
	require.NoError(t, err)
	_, err = LoadManifestDescriptor(ref)
	assert.ErrorContains(t, err, "path escapes from parent")
}

func TestIndexFSPath(t *testing.T) {
	res := indexFSPath()
	assert.Equal(t, "index.json", res)
}

func TestReferenceBlobPath(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	bp, err := blobFSPath("sha256:"+hex, false)
	assert.NoError(t, err)
	assert.Equal(t, "blobs/sha256/"+hex, bp)
}

func TestReferenceSharedBlobPathShared(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	bp, err := blobFSPath("sha256:"+hex, true)
	assert.NoError(t, err)
	assert.Equal(t, "sha256/"+hex, bp)
}

func TestReferenceBlobPathInvalid(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, shared := range []bool{false, true} {
		_, err := blobFSPath(hex, shared)
		assert.ErrorContains(t, err, "unexpected digest reference "+hex)
	}
}
