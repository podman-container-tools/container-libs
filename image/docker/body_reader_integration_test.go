package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failAfterReader wraps a reader and returns io.ErrUnexpectedEOF after n bytes.
type failAfterReader struct {
	reader    io.Reader
	remaining int
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= n
	if r.remaining <= 0 && err == nil {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *failAfterReader) Close() error { return nil }

func TestBodyReaderRetryOnFullBlobResponse(t *testing.T) {
	const blobSize = 1024
	blobData := make([]byte, blobSize)
	for i := range blobData {
		blobData[i] = byte(i % 256)
	}

	const blobPath = "/v2/test/blobs/sha256:abc123"
	const failAfterBytes = 100

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			rangeHeader := r.Header.Get("Range")
			if rangeHeader != "" {
				// Simulate a server that does NOT support Range requests:
				// ignore the Range header and return the full blob with 200.
				w.Header().Set("Content-Length", fmt.Sprintf("%d", blobSize))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(blobData)
			} else {
				t.Error("Expected Range header on retry request")
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	// Prevent detectProperties from running (it would try to ping /v2/).
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: failAfterBytes,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	result, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, blobData, result)
}

func TestBodyReaderRetryOnPartialContentResponse(t *testing.T) {
	const blobSize = 1024
	blobData := make([]byte, blobSize)
	for i := range blobData {
		blobData[i] = byte(i % 256)
	}

	const blobPath = "/v2/test/blobs/sha256:abc123"
	const failAfterBytes = 100

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			rangeHeader := r.Header.Get("Range")
			if rangeHeader != "" {
				var offset int64
				_, _ = fmt.Sscanf(rangeHeader, "bytes=%d-", &offset)
				w.Header().Set("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", offset, blobSize-1, blobSize))
				w.Header().Set("Content-Length",
					fmt.Sprintf("%d", blobSize-int(offset)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(blobData[offset:])
			} else {
				t.Error("Expected Range header on retry request")
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: failAfterBytes,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	result, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, blobData, result)
}

func TestBodyReaderRetryOnFullBlobResponseZeroOffset(t *testing.T) {
	const blobSize = 1024
	blobData := make([]byte, blobSize)
	for i := range blobData {
		blobData[i] = byte(i % 256)
	}

	const blobPath = "/v2/test/blobs/sha256:abc123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			w.Header().Set("Content-Length", fmt.Sprintf("%d", blobSize))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(blobData)
		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: 0,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	result, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, blobData, result)
}

func TestBodyReaderRetryOnFullBlobResponseDiscardFailure(t *testing.T) {
	const blobPath = "/v2/test/blobs/sha256:abc123"
	const failAfterBytes = 100

	blobData := make([]byte, 1024)
	for i := range blobData {
		blobData[i] = byte(i % 256)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			// Return 200 but close the body before br.offset bytes can be
			// discarded. The server writes only 50 bytes, less than the 100
			// byte offset the client needs to skip past.
			w.Header().Set("Content-Length", "50")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(blobData[:50])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: failAfterBytes,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	_, err = io.ReadAll(reader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to discard")
}

func TestBodyReaderRetryOnFullBlobResponseMultiRetry(t *testing.T) {
	// Blob must be large enough that progress between retries exceeds
	// bodyReaderMinimumProgress (1 MiB), otherwise the heuristic rejects
	// the second reconnect attempt.
	const blobSize = 3 * 1024 * 1024 // 3 MiB
	blobData := make([]byte, blobSize)
	for i := range blobData {
		blobData[i] = byte(i % 256)
	}

	const blobPath = "/v2/test/blobs/sha256:abc123"
	const firstFailAfter = 100
	// After first retry with 200, the reader discards firstFailAfter bytes
	// then reads secondReadBytes before failing again.
	const secondReadBytes = 2 * 1024 * 1024 // 2 MiB (> bodyReaderMinimumProgress)

	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			req := requestCount.Add(1)
			rangeHeader := r.Header.Get("Range")
			if rangeHeader == "" {
				t.Error("Expected Range header on retry request")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var offset int64
			_, _ = fmt.Sscanf(rangeHeader, "bytes=%d-", &offset)

			if req == 1 {
				// First retry: return 200 (ignoring Range). Write enough
				// for the discard (offset bytes) plus secondReadBytes,
				// then truncate to simulate a connection drop.
				truncatedLen := offset + secondReadBytes
				w.Header().Set("Content-Length", fmt.Sprintf("%d", blobSize))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(blobData[:truncatedLen])
			} else {
				// Second retry: return 206 with correct partial content.
				w.Header().Set("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", offset, blobSize-1, blobSize))
				w.Header().Set("Content-Length",
					fmt.Sprintf("%d", blobSize-int(offset)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(blobData[offset:])
			}
		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: firstFailAfter,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	result, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, blobData, result)
	assert.Equal(t, int32(2), requestCount.Load(), "expected exactly two retry requests")
}

func TestBodyReaderRetryOnServerError(t *testing.T) {
	const blobPath = "/v2/test/blobs/sha256:abc123"
	blobData := make([]byte, 1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == blobPath:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidRange</Code></Error>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	c := &dockerClient{
		registry: serverURL.Host,
		scheme:   "http",
		client:   server.Client(),
	}
	c.detectPropertiesOnce.Do(func() {})

	firstBody := &failAfterReader{
		reader:    bytes.NewReader(blobData),
		remaining: 100,
	}

	reader, err := newBodyReader(context.Background(), c, blobPath, firstBody)
	require.NoError(t, err)
	defer reader.Close()

	_, err = io.ReadAll(reader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "after reconnecting, fetching blob")
}
