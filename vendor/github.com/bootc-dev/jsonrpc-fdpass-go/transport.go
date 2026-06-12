package fdpass

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	// DefaultMaxFDsPerSendmsg is the default maximum number of file descriptors
	// per sendmsg() call. Platform limits for SCM_RIGHTS vary (e.g., ~253 on
	// Linux, ~512 on macOS). We start with an optimistic value; if sendmsg()
	// fails with EINVAL, the batch size is automatically reduced and retried.
	DefaultMaxFDsPerSendmsg = 500

	// MaxFDsPerRecvmsg is the maximum number of FDs to expect in a single
	// recvmsg() call. Must be at least as large as the largest platform limit.
	MaxFDsPerRecvmsg = 512

	// ReadBufferSize is the size of the read buffer.
	ReadBufferSize = 4096
)

var (
	// ErrConnectionClosed is returned when the connection is closed.
	ErrConnectionClosed = errors.New("connection closed")
	// ErrFramingError is returned when JSON parsing fails.
	ErrFramingError = errors.New("framing error: invalid JSON")
	// ErrMismatchedCount is returned when the number of FDs doesn't match the fds field.
	ErrMismatchedCount = errors.New("mismatched file descriptor count")
)

// Sender sends JSON-RPC messages with file descriptors over a Unix socket.
type Sender struct {
	conn            *net.UnixConn
	mu              sync.Mutex
	maxFDsPerSendmsg int
}

// NewSender creates a new Sender for the given Unix connection.
func NewSender(conn *net.UnixConn) *Sender {
	return &Sender{
		conn:            conn,
		maxFDsPerSendmsg: DefaultMaxFDsPerSendmsg,
	}
}

// SetMaxFDsPerSendmsg sets the maximum number of file descriptors to send per
// sendmsg() call. This is primarily useful for testing FD batching behavior.
// The value must be at least 1.
func (s *Sender) SetMaxFDsPerSendmsg(max int) {
	if max < 1 {
		max = 1
	}
	s.maxFDsPerSendmsg = max
}

// Send sends a message with optional file descriptors.
func (s *Sender) Send(msg *MessageWithFds) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Set the fds field on the message based on the number of file descriptors
	fdCount := len(msg.FileDescriptors)
	switch m := msg.Message.(type) {
	case *Request:
		m.SetFDs(fdCount)
	case *Response:
		m.SetFDs(fdCount)
	case *Notification:
		m.SetFDs(fdCount)
	}

	// Serialize the message with the fds field set
	msgData, err := json.Marshal(msg.Message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Get the raw file descriptor for the socket
	rawConn, err := s.conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to get syscall conn: %w", err)
	}

	var sendErr error
	err = rawConn.Control(func(fd uintptr) {
		sendErr = s.sendWithFDs(int(fd), msgData, msg.FileDescriptors)
	})
	if err != nil {
		return err
	}
	return sendErr
}

func (s *Sender) sendWithFDs(sockfd int, data []byte, files []*os.File) error {
	// Extract raw FD ints
	allFDs := make([]int, len(files))
	for i, f := range files {
		allFDs[i] = int(f.Fd())
	}

	bytesSent := 0
	fdsSent := 0
	currentMaxFDs := s.maxFDsPerSendmsg

	// Send data and FDs in batches. Each sendmsg can only handle a limited
	// number of FDs. After all data bytes are sent, remaining FDs are sent
	// with whitespace padding bytes per the protocol spec (Section 4.1).
	for bytesSent < len(data) || fdsSent < len(allFDs) {
		remainingData := data[bytesSent:]
		remainingFDs := allFDs[fdsSent:]

		// Determine how many FDs to send in this batch
		fdBatchSize := len(remainingFDs)
		if fdBatchSize > currentMaxFDs {
			fdBatchSize = currentMaxFDs
		}
		fdBatch := remainingFDs[:fdBatchSize]

		var n int
		var err error

		if len(fdBatch) > 0 {
			// Send with FDs using sendmsg with ancillary data
			rights := unix.UnixRights(fdBatch...)

			var payload []byte
			if len(remainingData) > 0 {
				payload = remainingData
			} else {
				// All data bytes already sent; send a whitespace padding byte.
				// The receiver's JSON parser ignores inter-message whitespace
				// per RFC 8259. This is required because some systems need
				// non-empty data for ancillary data delivery.
				payload = []byte{' '}
			}

			n, err = unix.SendmsgN(sockfd, payload, rights, nil, 0)
			if err != nil {
				// EINVAL with multiple FDs likely means we exceeded the
				// kernel's SCM_MAX_FD limit. Halve the batch size and retry.
				if errors.Is(err, unix.EINVAL) && fdBatchSize > 1 {
					currentMaxFDs = fdBatchSize / 2
					continue
				}
				return fmt.Errorf("sendmsg failed: %w", err)
			}
			fdsSent += fdBatchSize

			// Only count actual data bytes, not the padding byte
			if len(remainingData) > 0 {
				bytesSent += n
			}
		} else if len(remainingData) > 0 {
			// No FDs left, just send remaining data bytes
			n, err = unix.Write(sockfd, remainingData)
			if err != nil {
				return fmt.Errorf("write failed: %w", err)
			}
			bytesSent += n
		}
	}

	// If we discovered a lower limit, remember it for future sends
	if currentMaxFDs < s.maxFDsPerSendmsg {
		s.maxFDsPerSendmsg = currentMaxFDs
	}

	return nil
}

// Receiver receives JSON-RPC messages with file descriptors from a Unix socket.
type Receiver struct {
	conn    *net.UnixConn
	buffer  []byte
	fdQueue []*os.File
	mu      sync.Mutex
}

// NewReceiver creates a new Receiver for the given Unix connection.
func NewReceiver(conn *net.UnixConn) *Receiver {
	return &Receiver{
		conn:    conn,
		buffer:  make([]byte, 0),
		fdQueue: make([]*os.File, 0),
	}
}

// Receive receives the next message with its file descriptors.
func (r *Receiver) Receive() (*MessageWithFds, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for {
		// Try to parse a complete message from the buffer
		result, err := r.tryParseMessage()
		if err != nil {
			return nil, err
		}
		if result.msg != nil {
			return result.msg, nil
		}

		// Need more data — either incomplete JSON or waiting for
		// batched FDs from continuation sendmsg() calls.
		if err := r.readMoreData(); err != nil {
			// If we had a parsed message waiting for FDs and the
			// connection closed, that's a mismatched count error.
			if result.needFDs && errors.Is(err, ErrConnectionClosed) {
				return nil, fmt.Errorf("%w: connection closed while waiting for batched FDs", ErrMismatchedCount)
			}
			return nil, err
		}
	}
}

// tryParseResult is used internally to communicate between tryParseMessage
// and Receive about whether more FDs are needed from batched sendmsg calls.
type tryParseResult struct {
	msg     *MessageWithFds
	needFDs bool // true when message is parsed but FD queue is short
}

func (r *Receiver) tryParseMessage() (*tryParseResult, error) {
	if len(r.buffer) == 0 {
		return &tryParseResult{}, nil
	}

	// Use streaming JSON decoder to find message boundaries
	decoder := json.NewDecoder(bytes.NewReader(r.buffer))
	var value map[string]interface{}

	err := decoder.Decode(&value)
	if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
		// Incomplete JSON - need more data
		return &tryParseResult{}, nil
	}
	if err != nil {
		// Actual parse error - framing error
		return nil, fmt.Errorf("%w: %v", ErrFramingError, err)
	}

	// Successfully parsed a complete JSON value
	// Use InputOffset to find consumed bytes (Go 1.21+)
	bytesConsumed := decoder.InputOffset()

	// Extract the consumed bytes for re-parsing
	consumedData := make([]byte, bytesConsumed)
	copy(consumedData, r.buffer[:bytesConsumed])

	// Read the fds count from the message
	fdCount := GetFDCount(value)

	// Check we have enough FDs. When FD batching is in use, the sender
	// sends continuation sendmsg() calls with whitespace padding and more
	// FDs. We need to read more data to collect them.
	if fdCount > len(r.fdQueue) {
		return &tryParseResult{needFDs: true}, nil
	}

	// Remove consumed bytes from buffer
	r.buffer = r.buffer[bytesConsumed:]

	// Dequeue FDs
	fds := make([]*os.File, fdCount)
	copy(fds, r.fdQueue[:fdCount])
	r.fdQueue = r.fdQueue[fdCount:]

	// Parse the message into the appropriate type
	msg, err := ParseMessage(consumedData)
	if err != nil {
		return nil, err
	}

	return &tryParseResult{
		msg: &MessageWithFds{
			Message:         msg,
			FileDescriptors: fds,
		},
	}, nil
}

func (r *Receiver) readMoreData() error {
	rawConn, err := r.conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to get syscall conn: %w", err)
	}

	var readErr error
	var bytesRead int
	var receivedFDs []*os.File

	err = rawConn.Read(func(fd uintptr) bool {
		bytesRead, receivedFDs, readErr = r.recvWithFDs(int(fd))
		// Return true to indicate we're done with this read operation
		// Return false only if we get EAGAIN/EWOULDBLOCK
		if readErr != nil {
			if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EWOULDBLOCK) {
				readErr = nil
				return false // Tell runtime to wait and retry
			}
		}
		return true
	})

	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}

	if bytesRead == 0 && len(receivedFDs) == 0 {
		return ErrConnectionClosed
	}

	// Append received FDs to queue
	r.fdQueue = append(r.fdQueue, receivedFDs...)

	return nil
}

func (r *Receiver) recvWithFDs(sockfd int) (int, []*os.File, error) {
	buf := make([]byte, ReadBufferSize)
	// Allocate space for control message (for up to MaxFDsPerRecvmsg FDs)
	// Each FD is 4 bytes (int32), use CmsgSpace to get properly aligned size
	oob := make([]byte, unix.CmsgSpace(MaxFDsPerRecvmsg*4))

	n, oobn, _, _, err := unix.Recvmsg(sockfd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return 0, nil, err
	}

	// Append data to buffer
	if n > 0 {
		r.buffer = append(r.buffer, buf[:n]...)
	}

	// Parse control messages for FDs
	var files []*os.File
	if oobn > 0 {
		scms, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return n, nil, fmt.Errorf("failed to parse control message: %w", err)
		}

		for _, scm := range scms {
			fds, err := unix.ParseUnixRights(&scm)
			if err != nil {
				continue
			}
			for _, fd := range fds {
				files = append(files, os.NewFile(uintptr(fd), ""))
			}
		}
	}

	return n, files, nil
}

// Close closes the receiver and any pending file descriptors in the queue.
func (r *Receiver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Close any FDs remaining in the queue to prevent leaks
	for _, f := range r.fdQueue {
		f.Close()
	}
	r.fdQueue = nil

	return nil
}
