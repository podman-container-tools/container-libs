package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	digest "github.com/opencontainers/go-digest"
	imgspecv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/docker/reference"
	"go.podman.io/image/v5/internal/private"
	"go.podman.io/image/v5/internal/set"
	"go.podman.io/image/v5/internal/signature"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/types"
)

var _ private.ImageDestination = (*dockerImageDestination)(nil)

func TestIsManifestInvalidError(t *testing.T) {
	// Sadly only a smoke test; this really should record all known errors exactly as they happen.

	// docker/distribution 2.1.1 when uploading to a tag (because it can’t find a matching tag
	// inside the manifest)
	response := "HTTP/1.1 400 Bad Request\r\n" +
		"Connection: close\r\n" +
		"Content-Length: 79\r\n" +
		"Content-Type: application/json; charset=utf-8\r\n" +
		"Date: Sat, 14 Aug 2021 19:27:29 GMT\r\n" +
		"Docker-Distribution-Api-Version: registry/2.0\r\n" +
		"\r\n" +
		"{\"errors\":[{\"code\":\"TAG_INVALID\",\"message\":\"manifest tag did not match URI\"}]}\n"
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader([]byte(response))), nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	err = registryHTTPResponseToError(resp)

	res := isManifestInvalidError(err)
	assert.True(t, res, "%#v", err)
}

func TestPutSignaturesToReferrers(t *testing.T) {
	const targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	sigPayload := []byte(`{"critical":{"type":"cosign container image signature"}}`)
	sigMIMEType := signature.SigstoreSignatureMIMEType

	t.Run("uploads artifact manifest with subject and updates tag index", func(t *testing.T) {
		var mu sync.Mutex
		uploadedBlobs := map[string][]byte{}
		uploadedManifests := map[string][]byte{}
		blobUploadID := 0

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				w.WriteHeader(http.StatusOK)

			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				emptyIndex, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{},
				})
				_, _ = w.Write(emptyIndex)

			case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
				parts := strings.Split(r.URL.Path, "/blobs/")
				blobDigest := parts[len(parts)-1]
				if data, ok := uploadedBlobs[blobDigest]; ok {
					w.Header().Set("Docker-Content-Digest", blobDigest)
					w.Header().Set("Content-Length", strconv.Itoa(len(data)))
					w.WriteHeader(http.StatusOK)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}

			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				blobUploadID++
				w.Header().Set("Location", r.URL.Path+"?id="+strconv.Itoa(blobUploadID))
				w.WriteHeader(http.StatusAccepted)

			case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				body := &bytes.Buffer{}
				_, _ = body.ReadFrom(r.Body)
				uploadedBlobs[r.URL.Query().Get("digest")] = body.Bytes()
				w.Header().Set("Location", r.URL.String())
				w.WriteHeader(http.StatusAccepted)

			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				d := r.URL.Query().Get("digest")
				uploadedBlobs[d] = nil
				w.WriteHeader(http.StatusCreated)

			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
				parts := strings.Split(r.URL.Path, "/manifests/")
				tag := parts[len(parts)-1]
				body := &bytes.Buffer{}
				_, _ = body.ReadFrom(r.Body)
				uploadedManifests[tag] = body.Bytes()
				w.Header().Set("Docker-Content-Digest", digest.FromBytes(body.Bytes()).String())
				w.WriteHeader(http.StatusCreated)

			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/"):
				parts := strings.Split(r.URL.Path, "/manifests/")
				tag := parts[len(parts)-1]
				if data, ok := uploadedManifests[tag]; ok {
					w.Header().Set("Content-Type", manifest.GuessMIMEType(data))
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}

			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer s.Close()

		serverURL, err := url.Parse(s.URL)
		require.NoError(t, err)
		registry := serverURL.Host

		named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + targetDigest)
		require.NoError(t, err)
		ref, err := newReference(named, false)
		require.NoError(t, err)

		client := &dockerClient{
			sys:                    &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
			registry:               registry,
			scheme:                 "http",
			client:                 s.Client(),
			tokenCache:             map[string]*bearerToken{},
			reportedWarnings:       set.New[string](),
			useSigstoreAttachments: true,
		}
		client.detectPropertiesOnce.Do(func() {})

		dest := &dockerImageDestination{
			ref: ref,
			c:   client,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, map[string]string{
			"dev.cosignproject.cosign/signature": "dGVzdA==",
		})

		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()

		artifactManifestCount := 0
		for tag, data := range uploadedManifests {
			if strings.HasPrefix(tag, "sha256:") {
				artifactManifestCount++
				var m imgspecv1.Manifest
				err := json.Unmarshal(data, &m)
				require.NoError(t, err)
				assert.NotNil(t, m.Subject)
				assert.Equal(t, digest.Digest(targetDigest), m.Subject.Digest)
				assert.Equal(t, sigMIMEType, m.ArtifactType)
				assert.Equal(t, imgspecv1.MediaTypeEmptyJSON, m.Config.MediaType)
				assert.Len(t, m.Layers, 1)
				assert.Equal(t, sigMIMEType, m.Layers[0].MediaType)
			}
		}
		assert.Equal(t, 1, artifactManifestCount, "should upload exactly one artifact manifest")

		tagSchemaTag := strings.Replace(targetDigest, ":", "-", 1)
		indexData, ok := uploadedManifests[tagSchemaTag]
		assert.True(t, ok, "should upload referrers tag schema index")
		if ok {
			var index imgspecv1.Index
			err := json.Unmarshal(indexData, &index)
			require.NoError(t, err)
			assert.Len(t, index.Manifests, 1)
			assert.Equal(t, sigMIMEType, index.Manifests[0].ArtifactType)
		}
	})

	t.Run("skips existing referrer", func(t *testing.T) {
		sigAnnotations := map[string]string{
			"dev.cosignproject.cosign/signature": "dGVzdA==",
		}
		sigDesc := imgspecv1.Descriptor{
			MediaType:   sigMIMEType,
			Digest:      digest.FromBytes(sigPayload),
			Size:        int64(len(sigPayload)),
			Annotations: sigAnnotations,
		}
		emptyConfig := imgspecv1.Descriptor{
			MediaType: imgspecv1.MediaTypeEmptyJSON,
			Digest:    imgspecv1.DescriptorEmptyJSON.Digest,
			Size:      imgspecv1.DescriptorEmptyJSON.Size,
		}
		artifactManifest := manifest.OCI1FromComponents(emptyConfig, []imgspecv1.Descriptor{sigDesc})
		artifactManifest.Subject = &imgspecv1.Descriptor{
			MediaType: imgspecv1.MediaTypeImageManifest,
			Digest:    digest.Digest(targetDigest),
		}
		artifactManifest.ArtifactType = sigMIMEType
		manifestBlob, err := artifactManifest.Serialize()
		require.NoError(t, err)
		existingArtifactDigest, err := manifest.Digest(manifestBlob)
		require.NoError(t, err)

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
				index, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{
						{
							MediaType:    imgspecv1.MediaTypeImageManifest,
							Digest:       existingArtifactDigest,
							Size:         int64(len(manifestBlob)),
							ArtifactType: sigMIMEType,
						},
					},
				})
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(index)
			default:
				t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer s.Close()

		serverURL, err := url.Parse(s.URL)
		require.NoError(t, err)
		registry := serverURL.Host

		named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + targetDigest)
		require.NoError(t, err)
		ref, err := newReference(named, false)
		require.NoError(t, err)

		client := &dockerClient{
			sys:                    &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
			registry:               registry,
			scheme:                 "http",
			client:                 s.Client(),
			tokenCache:             map[string]*bearerToken{},
			reportedWarnings:       set.New[string](),
			useSigstoreAttachments: true,
		}
		client.detectPropertiesOnce.Do(func() {})

		dest := &dockerImageDestination{
			ref: ref,
			c:   client,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, sigAnnotations)

		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.NoError(t, err)
	})

	t.Run("merges with existing tag schema index", func(t *testing.T) {
		existingDesc := imgspecv1.Descriptor{
			MediaType:    imgspecv1.MediaTypeImageManifest,
			Digest:       "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			Size:         300,
			ArtifactType: sigMIMEType,
		}
		tagSchemaTag := strings.Replace(targetDigest, ":", "-", 1)
		preExistingIndex, err := json.Marshal(imgspecv1.Index{
			MediaType: imgspecv1.MediaTypeImageIndex,
			Manifests: []imgspecv1.Descriptor{existingDesc},
		})
		require.NoError(t, err)

		var mu sync.Mutex
		uploadedManifests := map[string][]byte{
			tagSchemaTag: preExistingIndex,
		}
		blobUploadID := 0

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				w.WriteHeader(http.StatusOK)

			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				emptyIndex, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{},
				})
				_, _ = w.Write(emptyIndex)

			case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
				w.WriteHeader(http.StatusNotFound)

			case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				blobUploadID++
				w.Header().Set("Location", r.URL.Path+"?id="+strconv.Itoa(blobUploadID))
				w.WriteHeader(http.StatusAccepted)

			case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				w.Header().Set("Location", r.URL.String())
				w.WriteHeader(http.StatusAccepted)

			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
				w.WriteHeader(http.StatusCreated)

			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
				parts := strings.Split(r.URL.Path, "/manifests/")
				tag := parts[len(parts)-1]
				body := &bytes.Buffer{}
				_, _ = body.ReadFrom(r.Body)
				uploadedManifests[tag] = body.Bytes()
				w.Header().Set("Docker-Content-Digest", digest.FromBytes(body.Bytes()).String())
				w.WriteHeader(http.StatusCreated)

			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/"):
				parts := strings.Split(r.URL.Path, "/manifests/")
				tag := parts[len(parts)-1]
				if data, ok := uploadedManifests[tag]; ok {
					w.Header().Set("Content-Type", manifest.GuessMIMEType(data))
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}

			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer s.Close()

		serverURL, err := url.Parse(s.URL)
		require.NoError(t, err)
		registry := serverURL.Host

		named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + targetDigest)
		require.NoError(t, err)
		ref, err := newReference(named, false)
		require.NoError(t, err)

		client := &dockerClient{
			sys:                    &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
			registry:               registry,
			scheme:                 "http",
			client:                 s.Client(),
			tokenCache:             map[string]*bearerToken{},
			reportedWarnings:       set.New[string](),
			useSigstoreAttachments: true,
		}
		client.detectPropertiesOnce.Do(func() {})

		dest := &dockerImageDestination{
			ref: ref,
			c:   client,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, map[string]string{
			"dev.cosignproject.cosign/signature": "dGVzdA==",
		})

		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()

		indexData, ok := uploadedManifests[tagSchemaTag]
		require.True(t, ok, "should upload referrers tag schema index")
		var index imgspecv1.Index
		err = json.Unmarshal(indexData, &index)
		require.NoError(t, err)
		assert.Len(t, index.Manifests, 2, "index should contain both the pre-existing and new entry")
		assert.Equal(t, existingDesc.Digest, index.Manifests[0].Digest, "pre-existing entry should be preserved")
	})
}

func TestPutSignaturesReferrersFailureFallback(t *testing.T) {
	const targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cosignTag := strings.Replace(targetDigest, ":", "-", 1) + ".sig"

	sigPayload := []byte(`{"critical":{"type":"cosign container image signature"}}`)
	sigMIMEType := signature.SigstoreSignatureMIMEType

	var mu sync.Mutex
	uploadedManifests := map[string][]byte{}
	blobUploadID := 0

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/":
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
			w.WriteHeader(http.StatusInternalServerError)

		case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			blobUploadID++
			w.Header().Set("Location", r.URL.Path+"?id="+strconv.Itoa(blobUploadID))
			w.WriteHeader(http.StatusAccepted)

		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			w.Header().Set("Location", r.URL.String())
			w.WriteHeader(http.StatusAccepted)

		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/uploads/"):
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			parts := strings.Split(r.URL.Path, "/manifests/")
			tag := parts[len(parts)-1]
			body := &bytes.Buffer{}
			_, _ = body.ReadFrom(r.Body)
			uploadedManifests[tag] = body.Bytes()
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/"):
			w.WriteHeader(http.StatusNotFound)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer s.Close()

	serverURL, err := url.Parse(s.URL)
	require.NoError(t, err)
	registry := serverURL.Host

	named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + targetDigest)
	require.NoError(t, err)
	ref, err := newReference(named, false)
	require.NoError(t, err)

	client := &dockerClient{
		sys:                    &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
		registry:               registry,
		scheme:                 "http",
		client:                 s.Client(),
		tokenCache:             map[string]*bearerToken{},
		reportedWarnings:       set.New[string](),
		useSigstoreAttachments: true,
	}
	client.detectPropertiesOnce.Do(func() {})

	dest := &dockerImageDestination{
		ref: ref,
		c:   client,
	}

	sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, map[string]string{
		"dev.cosignproject.cosign/signature": "dGVzdA==",
	})

	instanceDigest := digest.Digest(targetDigest)
	err = dest.PutSignaturesWithFormat(context.Background(), []signature.Signature{sig}, &instanceDigest)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	_, ok := uploadedManifests[cosignTag]
	assert.True(t, ok, "cosign tag manifest should be uploaded even when referrers API fails")
}

func TestReferrerAlreadyExists(t *testing.T) {
	testDigest := digest.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	t.Run("found", func(t *testing.T) {
		index := &imgspecv1.Index{
			Manifests: []imgspecv1.Descriptor{
				{Digest: testDigest},
			},
		}
		assert.True(t, referrerAlreadyExists(index, testDigest))
	})

	t.Run("not found", func(t *testing.T) {
		index := &imgspecv1.Index{
			Manifests: []imgspecv1.Descriptor{
				{Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
		}
		assert.False(t, referrerAlreadyExists(index, testDigest))
	})

	t.Run("empty index", func(t *testing.T) {
		index := &imgspecv1.Index{Manifests: []imgspecv1.Descriptor{}}
		assert.False(t, referrerAlreadyExists(index, testDigest))
	})
}
