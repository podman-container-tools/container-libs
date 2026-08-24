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

	const targetSize = int64(1234)
	uploadedTarget := map[digest.Digest]uploadedManifestInfo{
		digest.Digest(targetDigest): {size: targetSize, mimeType: imgspecv1.MediaTypeImageIndex},
	}

	t.Run("uploads artifact manifest with subject, registry supports the Referrers API", func(t *testing.T) {
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
			ref:               ref,
			c:                 client,
			uploadedManifests: uploadedTarget,
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
				require.NotNil(t, m.Subject)
				assert.Equal(t, digest.Digest(targetDigest), m.Subject.Digest)
				assert.Equal(t, targetSize, m.Subject.Size)
				assert.Equal(t, imgspecv1.MediaTypeImageIndex, m.Subject.MediaType)
				assert.Equal(t, sigstoreReferrerArtifactType, m.ArtifactType)
				assert.Equal(t, imgspecv1.MediaTypeEmptyJSON, m.Config.MediaType)
				assert.Len(t, m.Layers, 1)
				assert.Equal(t, sigMIMEType, m.Layers[0].MediaType)
			}
		}
		assert.Equal(t, 1, artifactManifestCount, "should upload exactly one artifact manifest")

		tagSchemaTag := strings.Replace(targetDigest, ":", "-", 1)
		_, ok := uploadedManifests[tagSchemaTag]
		assert.False(t, ok, "should not upload the referrers tag schema index when the registry supports the API")
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
			MediaType: imgspecv1.MediaTypeImageIndex,
			Digest:    digest.Digest(targetDigest),
			Size:      targetSize,
		}
		artifactManifest.ArtifactType = sigstoreReferrerArtifactType
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
							ArtifactType: sigstoreReferrerArtifactType,
						},
					},
				})
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(index)
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+existingArtifactDigest.String()):
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(manifestBlob)
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
			ref:               ref,
			c:                 client,
			uploadedManifests: uploadedTarget,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, sigAnnotations)

		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.NoError(t, err)
	})

	t.Run("skips a signature already attached by another tool", func(t *testing.T) {
		// cosign --registry-referrers-mode oci-1-1 wraps the very same signature in a differently
		// shaped manifest (its config mediaType is the artifact type, ours is empty JSON), so the
		// artifact manifest digests never match and only comparing the payload avoids a duplicate.
		sigAnnotations := map[string]string{
			"dev.cosignproject.cosign/signature": "dGVzdA==",
		}
		sigDesc := imgspecv1.Descriptor{
			MediaType:   sigMIMEType,
			Digest:      digest.FromBytes(sigPayload),
			Size:        int64(len(sigPayload)),
			Annotations: sigAnnotations,
		}
		cosignManifest := manifest.OCI1FromComponents(imgspecv1.Descriptor{
			MediaType: sigstoreReferrerArtifactType,
			Digest:    imgspecv1.DescriptorEmptyJSON.Digest,
			Size:      imgspecv1.DescriptorEmptyJSON.Size,
		}, []imgspecv1.Descriptor{sigDesc})
		cosignManifest.Subject = &imgspecv1.Descriptor{
			MediaType: imgspecv1.MediaTypeImageIndex,
			Digest:    digest.Digest(targetDigest),
			Size:      targetSize,
		}
		cosignManifest.ArtifactType = sigstoreReferrerArtifactType
		cosignBlob, err := cosignManifest.Serialize()
		require.NoError(t, err)
		cosignDigest, err := manifest.Digest(cosignBlob)
		require.NoError(t, err)

		// Sanity check: this is a different artifact manifest than the one we would create.
		ours := manifest.OCI1FromComponents(imgspecv1.Descriptor{
			MediaType: imgspecv1.MediaTypeEmptyJSON,
			Digest:    imgspecv1.DescriptorEmptyJSON.Digest,
			Size:      imgspecv1.DescriptorEmptyJSON.Size,
		}, []imgspecv1.Descriptor{sigDesc})
		ours.Subject = cosignManifest.Subject
		ours.ArtifactType = sigstoreReferrerArtifactType
		oursBlob, err := ours.Serialize()
		require.NoError(t, err)
		oursDigest, err := manifest.Digest(oursBlob)
		require.NoError(t, err)
		require.NotEqual(t, oursDigest, cosignDigest)

		var mu sync.Mutex
		uploads := 0

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
				index, _ := json.Marshal(imgspecv1.Index{
					MediaType: imgspecv1.MediaTypeImageIndex,
					Manifests: []imgspecv1.Descriptor{
						{
							MediaType:    imgspecv1.MediaTypeImageManifest,
							Digest:       cosignDigest,
							Size:         int64(len(cosignBlob)),
							ArtifactType: sigstoreReferrerArtifactType,
						},
					},
				})
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(index)
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+cosignDigest.String()):
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(cosignBlob)
			case r.Method == http.MethodPut, r.Method == http.MethodPost, r.Method == http.MethodPatch:
				uploads++
				w.WriteHeader(http.StatusCreated)
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
			ref:               ref,
			c:                 client,
			uploadedManifests: uploadedTarget,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, sigAnnotations)
		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 0, uploads, "should not upload anything for a signature another tool already attached")
	})

	t.Run("merges with existing tag schema index when the registry lacks the Referrers API", func(t *testing.T) {
		existingDesc := imgspecv1.Descriptor{
			MediaType:    imgspecv1.MediaTypeImageManifest,
			Digest:       "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			Size:         300,
			ArtifactType: sigstoreReferrerArtifactType,
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
				w.WriteHeader(http.StatusNotFound)

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
			ref:               ref,
			c:                 client,
			uploadedManifests: uploadedTarget,
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
		require.Len(t, index.Manifests, 2, "index should contain both the pre-existing and new entry")
		assert.Equal(t, existingDesc.Digest, index.Manifests[0].Digest, "pre-existing entry should be preserved")
		assert.Equal(t, sigstoreReferrerArtifactType, index.Manifests[1].ArtifactType)
	})

	t.Run("refuses to overwrite a referrers tag that is not an index", func(t *testing.T) {
		tagSchemaTag := strings.Replace(targetDigest, ":", "-", 1)
		var mu sync.Mutex
		manifestPuts := 0
		blobUploadID := 0

		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v2/":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
				w.WriteHeader(http.StatusNotFound)
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+tagSchemaTag):
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageManifest)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
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
				manifestPuts++
				assert.NotContains(t, r.URL.Path, tagSchemaTag, "the non-index tag must not be overwritten")
				w.WriteHeader(http.StatusCreated)
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
			ref:               ref,
			c:                 client,
			uploadedManifests: uploadedTarget,
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, nil)
		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, digest.Digest(targetDigest))
		require.ErrorContains(t, err, "not an image index")

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 1, manifestPuts, "only the artifact manifest should have been pushed")
	})

	t.Run("fetches the subject descriptor if the manifest was not uploaded by us", func(t *testing.T) {
		// A Docker schema2 manifest: the subject must carry its real media type and size.
		subjectBlob := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"},"layers":[]}`)
		subjectDigest := digest.FromBytes(subjectBlob)

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
				w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`))
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+subjectDigest.String()):
				w.Header().Set("Content-Type", manifest.DockerV2Schema2MediaType)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(subjectBlob)
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
				body := &bytes.Buffer{}
				_, _ = body.ReadFrom(r.Body)
				uploadedManifests[parts[len(parts)-1]] = body.Bytes()
				w.WriteHeader(http.StatusCreated)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer s.Close()

		serverURL, err := url.Parse(s.URL)
		require.NoError(t, err)
		registry := serverURL.Host

		named, err := reference.ParseNormalizedNamed(registry + "/test/repo@" + subjectDigest.String())
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

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, nil)
		err = dest.putSignaturesToReferrers(context.Background(), []signature.Sigstore{sig}, subjectDigest)
		require.NoError(t, err)

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, uploadedManifests, 1)
		for _, data := range uploadedManifests {
			var m imgspecv1.Manifest
			require.NoError(t, json.Unmarshal(data, &m))
			require.NotNil(t, m.Subject)
			assert.Equal(t, subjectDigest, m.Subject.Digest)
			assert.Equal(t, int64(len(subjectBlob)), m.Subject.Size)
			assert.Equal(t, manifest.DockerV2Schema2MediaType, m.Subject.MediaType)
		}
	})
}

func TestPutSignaturesWithFormatWriteMode(t *testing.T) {
	const targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const targetSize = int64(1234)
	cosignTag := strings.Replace(targetDigest, ":", "-", 1) + ".sig"

	sigPayload := []byte(`{"critical":{"type":"cosign container image signature"}}`)
	sigMIMEType := signature.SigstoreSignatureMIMEType

	for _, c := range []struct {
		name                string
		mode                sigstoreAttachmentsWriteMode
		referrersFail       bool
		expectErr           bool
		expectReferrersReqs bool
		expectCosignTag     bool
		expectArtifact      bool
	}{
		{
			name:            "unset writes only the cosign tag",
			mode:            "",
			expectCosignTag: true,
		},
		{
			name:            "cosign-tag does not touch the Referrers API",
			mode:            sigstoreAttachmentsWriteCosignTag,
			expectCosignTag: true,
		},
		{
			name:                "referrers writes only the artifact manifest",
			mode:                sigstoreAttachmentsWriteReferrers,
			expectReferrersReqs: true,
			expectArtifact:      true,
		},
		{
			name:                "both writes the artifact manifest and the cosign tag",
			mode:                sigstoreAttachmentsWriteBoth,
			expectReferrersReqs: true,
			expectCosignTag:     true,
			expectArtifact:      true,
		},
		{
			// The user asked for referrers explicitly, so a failure is reported instead of being
			// silently worked around. The cosign tag is written first, so it survives the failure.
			name:                "an explicitly requested referrers write is not best-effort",
			mode:                sigstoreAttachmentsWriteBoth,
			referrersFail:       true,
			expectErr:           true,
			expectReferrersReqs: true,
			expectCosignTag:     true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			uploadedManifests := map[string][]byte{}
			referrersRequests := 0
			blobUploadID := 0

			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v2/":
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/referrers/"):
					referrersRequests++
					if c.referrersFail {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					emptyIndex, _ := json.Marshal(imgspecv1.Index{
						MediaType: imgspecv1.MediaTypeImageIndex,
						Manifests: []imgspecv1.Descriptor{},
					})
					w.Header().Set("Content-Type", imgspecv1.MediaTypeImageIndex)
					w.WriteHeader(http.StatusOK)
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
					body := &bytes.Buffer{}
					_, _ = body.ReadFrom(r.Body)
					uploadedManifests[parts[len(parts)-1]] = body.Bytes()
					w.Header().Set("Docker-Content-Digest", digest.FromBytes(body.Bytes()).String())
					w.WriteHeader(http.StatusCreated)

				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`))

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
				sys:                      &types.SystemContext{DockerInsecureSkipTLSVerify: types.OptionalBoolTrue},
				registry:                 registry,
				scheme:                   "http",
				client:                   s.Client(),
				tokenCache:               map[string]*bearerToken{},
				reportedWarnings:         set.New[string](),
				useSigstoreAttachments:   true,
				sigstoreAttachmentsWrite: c.mode,
			}
			client.detectPropertiesOnce.Do(func() {})

			dest := &dockerImageDestination{
				ref: ref,
				c:   client,
				uploadedManifests: map[digest.Digest]uploadedManifestInfo{
					digest.Digest(targetDigest): {size: targetSize, mimeType: imgspecv1.MediaTypeImageIndex},
				},
			}

			sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, map[string]string{
				"dev.cosignproject.cosign/signature": "dGVzdA==",
			})

			instanceDigest := digest.Digest(targetDigest)
			err = dest.PutSignaturesWithFormat(context.Background(), []signature.Signature{sig}, &instanceDigest)
			if c.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			mu.Lock()
			defer mu.Unlock()

			assert.Equal(t, c.expectReferrersReqs, referrersRequests > 0, "Referrers API requests")
			_, ok := uploadedManifests[cosignTag]
			assert.Equal(t, c.expectCosignTag, ok, "cosign tag manifest")
			artifacts := 0
			for tag := range uploadedManifests {
				if strings.HasPrefix(tag, "sha256:") {
					artifacts++
				}
			}
			assert.Equal(t, c.expectArtifact, artifacts > 0, "referrer artifact manifest")
		})
	}

	t.Run("referrers still require use-sigstore-attachments", func(t *testing.T) {
		named, err := reference.ParseNormalizedNamed("example.com/test/repo@" + targetDigest)
		require.NoError(t, err)
		ref, err := newReference(named, false)
		require.NoError(t, err)

		dest := &dockerImageDestination{
			ref: ref,
			c: &dockerClient{
				useSigstoreAttachments:   false,
				sigstoreAttachmentsWrite: sigstoreAttachmentsWriteReferrers,
			},
		}

		sig := signature.SigstoreFromComponents(sigMIMEType, sigPayload, nil)
		instanceDigest := digest.Digest(targetDigest)
		err = dest.PutSignaturesWithFormat(context.Background(), []signature.Signature{sig}, &instanceDigest)
		assert.Error(t, err, "attachments disabled must fail before any network access")
	})
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
