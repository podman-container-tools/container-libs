package chunked

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock for ImageSourceSeekable
type mockImageSource struct {
	streams chan io.ReadCloser
	errors  chan error
}

func (m *mockImageSource) GetBlobAt(chunks []ImageSourceChunk) (chan io.ReadCloser, chan error, error) {
	return m.streams, m.errors, nil
}

type mockReadCloser struct {
	io.Reader
	closed bool
}

func (m *mockReadCloser) Close() error {
	m.closed = true
	return nil
}

func mockReadCloserFromContent(content string) *mockReadCloser {
	return &mockReadCloser{Reader: bytes.NewBufferString(content), closed: false}
}

type testDestinationFile struct {
	started chan struct{}
	release chan struct{}
}

func (f *testDestinationFile) Close() error {
	if f.started != nil {
		close(f.started)
		<-f.release
	}
	syscall.ForkLock.RUnlock()
	return nil
}

// TestDestinationFileCloserDoesNotBlockFork reproduces the interleaving that
// used to deadlock: the producer is blocked handing over a destination file
// because the queue is full, so it consumes no close result, while a
// concurrent composefs generation waits for syscall.ForkLock.Lock().  Every
// queued destination file must still be closed, and so release its
// syscall.ForkLock.RLock().
//
// Failures use t.Error, never t.Fatal: syscall.ForkLock is process wide, so
// bailing out while a testDestinationFile still holds it would hang every
// later test that forks.
func TestDestinationFileCloserDoesNotBlockFork(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	unblockFirst := func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
	}
	newDestination := func(blocking bool) *testDestinationFile {
		file := &testDestinationFile{}
		if blocking {
			file.started = firstStarted
			file.release = releaseFirst
		}
		syscall.ForkLock.RLock()
		return file
	}

	filesToClose := newDestinationFileCloser()
	blockedAdd := make(chan struct{})
	var blockedAddErr error
	forkLocked := make(chan struct{})
	releaseFork := make(chan struct{})
	forkStarted, forkDone := false, make(chan struct{})
	// Unwind in a fixed order regardless of which assertion failed: release
	// the closer, wait for the producer, then for the pending fork.
	defer func() {
		unblockFirst()
		<-blockedAdd
		assert.NoError(t, blockedAddErr)
		assert.NoError(t, filesToClose.Wait())
		if forkStarted {
			close(releaseFork)
			<-forkDone
		}
	}()

	// Hand over a file whose Close() blocks, so the closer cannot drain the
	// queue while the remaining files are added.
	require.NoError(t, filesToClose.Add(newDestination(true)))
	select {
	case <-firstStarted:
	case <-time.After(30 * time.Second):
		t.Error("the destination file closer did not start closing the first file")
	}

	// Fill the queue, then block the producer on one more file.
	for range maxPendingDestinationFiles {
		require.NoError(t, filesToClose.Add(newDestination(false)))
	}
	go func() {
		defer close(blockedAdd)
		blockedAddErr = filesToClose.Add(newDestination(false))
	}()

	// The closer is stuck in Close(), so nothing can leave the queue.
	select {
	case <-blockedAdd:
		t.Error("the producer was expected to block on a full queue")
	case <-time.After(100 * time.Millisecond):
	}

	// Only now, with every destination file holding syscall.ForkLock.RLock(),
	// start the concurrent composefs generation: a pending write lock also
	// blocks new readers, which would keep the producer from ever reaching the
	// blocked hand over above.
	forkStarted = true
	go func() {
		defer close(forkDone)
		syscall.ForkLock.Lock()
		close(forkLocked)
		<-releaseFork
		syscall.ForkLock.Unlock()
	}()
	// The queued files still hold the read lock, so the fork cannot proceed.
	// This makes sure the test exercises what it claims to.
	select {
	case <-forkLocked:
		t.Error("the queued destination files did not hold syscall.ForkLock")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the closer make progress.  It must close every queued file without
	// waiting for the blocked producer to collect the results.
	unblockFirst()
	select {
	case <-forkLocked:
	case <-time.After(30 * time.Second):
		t.Error("closing the destination files blocked a concurrent fork")
	}
}

func TestGetBlobAtNormalOperation(t *testing.T) {
	errors := make(chan error, 1)
	expectedStreams := []string{"stream1", "stream2"}
	streamsObjs := []*mockReadCloser{
		mockReadCloserFromContent(expectedStreams[0]),
		mockReadCloserFromContent(expectedStreams[1]),
	}
	streams := make(chan io.ReadCloser, len(streamsObjs))

	for _, s := range streamsObjs {
		streams <- s
	}
	close(streams)
	close(errors)

	is := &mockImageSource{streams: streams, errors: errors}

	chunks := []ImageSourceChunk{
		{Offset: 0, Length: 1},
		{Offset: 1, Length: 1},
	}

	resultChan, err := getBlobAt(is, chunks...)
	require.NoError(t, err)

	i := 0
	for result := range resultChan {
		assert.NoError(t, result.err)
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(result.stream)
		result.stream.Close()
		assert.Equal(t, expectedStreams[i], buf.String())
		i++
	}
	assert.Len(t, expectedStreams, i)
	for _, s := range streamsObjs {
		assert.True(t, s.closed)
	}
}

func TestGetBlobAtMaxStreams(t *testing.T) {
	streams := make(chan io.ReadCloser, 5)
	errors := make(chan error)

	streamsObjs := []*mockReadCloser{}

	for i := 1; i <= 5; i++ {
		s := mockReadCloserFromContent(fmt.Sprintf("stream%d", i))
		streamsObjs = append(streamsObjs, s)
		streams <- s
	}
	close(streams)
	close(errors)

	is := &mockImageSource{streams: streams, errors: errors}

	chunks := []ImageSourceChunk{
		{Offset: 0, Length: 1},
		{Offset: 1, Length: 1},
		{Offset: 2, Length: 1},
	}

	resultChan, err := getBlobAt(is, chunks...)
	require.NoError(t, err)

	count := 0
	receivedErr := false
	for result := range resultChan {
		if result.err != nil {
			receivedErr = true
		} else {
			result.stream.Close()
			count++
		}
	}
	assert.True(t, receivedErr)
	assert.Equal(t, 3, count)
	for _, s := range streamsObjs {
		assert.True(t, s.closed)
	}
}

func TestGetBlobAtWithErrors(t *testing.T) {
	streams := make(chan io.ReadCloser)
	errorsC := make(chan error, 2)

	errorsC <- errors.New("error1")
	errorsC <- errors.New("error2")
	close(streams)
	close(errorsC)

	is := &mockImageSource{streams: streams, errors: errorsC}

	chunks := []ImageSourceChunk{
		{Offset: 0, Length: 1},
		{Offset: 1, Length: 1},
	}
	resultChan, err := getBlobAt(is, chunks...)
	require.NoError(t, err)

	expectedErrors := []string{"error1", "error2"}
	i := 0
	for result := range resultChan {
		assert.Nil(t, result.stream)
		assert.NotNil(t, result.err)
		if result.err != nil {
			assert.Equal(t, expectedErrors[i], result.err.Error())
		}
		i++
	}
	assert.Equal(t, len(expectedErrors), i)
}

func TestGetBlobAtMixedStreamsAndErrors(t *testing.T) {
	streams := make(chan io.ReadCloser, 2)
	errorsC := make(chan error, 1)

	streams <- mockReadCloserFromContent("stream1")
	streams <- mockReadCloserFromContent("stream2")
	errorsC <- errors.New("error1")
	close(streams)
	close(errorsC)

	is := &mockImageSource{streams: streams, errors: errorsC}

	chunks := []ImageSourceChunk{
		{Offset: 0, Length: 1},
		{Offset: 1, Length: 1},
	}
	resultChan, err := getBlobAt(is, chunks...)
	require.NoError(t, err)

	var receivedStreams int
	var receivedErrors int
	for result := range resultChan {
		if result.err != nil {
			receivedErrors++
		} else {
			receivedStreams++
		}
	}
	assert.Equal(t, 2, receivedStreams)
	assert.Equal(t, 1, receivedErrors)
}

func TestTypeToOsMode(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  os.FileMode
		expectErr bool
	}{
		{"regular file", TypeReg, 0, false},
		{"hard link", TypeLink, 0, false},
		{"symlink", TypeSymlink, os.ModeSymlink, false},
		{"directory", TypeDir, os.ModeDir, false},
		{"character device", TypeChar, os.ModeDevice | os.ModeCharDevice, false},
		{"block device", TypeBlock, os.ModeDevice, false},
		{"FIFO/pipe", TypeFifo, os.ModeNamedPipe, false},
		{"unknown type", "unknown", 0, true},
		{"empty string", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := typeToOsMode(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, mode)
			}
		})
	}
}
