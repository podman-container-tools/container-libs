//go:build linux

package splitfdstreamserver

import (
	"context"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// maxFDsPerRecv is the maximum number of FDs we expect to receive in
// a single recvmsg call.
const maxFDsPerRecv = 253

// varlinkConn wraps a net.UnixConn and implements the varlink
// ReadWriterContext and FDReadWriter interfaces with SCM_RIGHTS
// fd passing support.
type varlinkConn struct {
	conn *net.UnixConn
	// fd is conn's descriptor, for the calls net.UnixConn cannot make.
	fd int

	// Read buffer: we need manual buffering because SCM_RIGHTS
	// ancillary data is attached to a specific recvmsg call.
	readBuf []byte
	readFDs []*os.File
}

func newVarlinkConn(conn *net.UnixConn) (*varlinkConn, error) {
	// ReadBytesWithFDs and WriteWithFDs call recvmsg/sendmsg on the
	// descriptor directly, because SCM_RIGHTS is not reachable through
	// net.UnixConn.  That bypasses the runtime poller, so the descriptor
	// has to be in blocking mode: left non-blocking, a read with nothing
	// buffered yet fails with EAGAIN instead of waiting.
	rc, err := conn.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("getting raw connection: %w", err)
	}
	var rawFD int
	var setErr error
	if err := rc.Control(func(fd uintptr) {
		rawFD = int(fd)
		setErr = unix.SetNonblock(rawFD, false)
	}); err != nil {
		return nil, fmt.Errorf("getting raw connection: %w", err)
	}
	if setErr != nil {
		return nil, fmt.Errorf("switching connection to blocking mode: %w", setErr)
	}
	return &varlinkConn{conn: conn, fd: rawFD}, nil
}

// Write implements varlink.ReadWriterContext.
func (c *varlinkConn) Write(_ context.Context, buf []byte) (int, error) {
	return c.conn.Write(buf)
}

// Read implements varlink.ReadWriterContext.
func (c *varlinkConn) Read(_ context.Context, buf []byte) (int, error) {
	return c.conn.Read(buf)
}

// ReadBytes implements varlink.ReadWriterContext.
func (c *varlinkConn) ReadBytes(_ context.Context, delim byte) ([]byte, error) {
	data, fds, err := c.readBytesWithFDsInternal(delim)
	closeFiles(fds)
	return data, err
}

// WriteWithFDs implements varlink.FDReadWriter.
func (c *varlinkConn) WriteWithFDs(_ context.Context, buf []byte, fds []*os.File) (int, error) {
	if len(fds) == 0 {
		return c.conn.Write(buf)
	}

	rawFDs := make([]int, len(fds))
	for i, f := range fds {
		rawFDs[i] = int(f.Fd())
	}

	rights := unix.UnixRights(rawFDs...)
	return len(buf), unix.Sendmsg(c.fd, buf, rights, nil, 0)
}

// ReadBytesWithFDs implements varlink.FDReadWriter.
func (c *varlinkConn) ReadBytesWithFDs(_ context.Context, delim byte) ([]byte, []*os.File, error) {
	return c.readBytesWithFDsInternal(delim)
}

func (c *varlinkConn) readBytesWithFDsInternal(delim byte) (_ []byte, _ []*os.File, retErr error) {
	var result []byte
	var fds []*os.File
	defer func() {
		if retErr != nil {
			closeFiles(fds)
		}
	}()

	for {
		for i, b := range c.readBuf {
			if b == delim {
				result = append(result, c.readBuf[:i+1]...)
				c.readBuf = c.readBuf[i+1:]
				if c.readFDs != nil {
					fds = c.readFDs
					c.readFDs = nil
				}
				return result, fds, nil
			}
		}
		result = append(result, c.readBuf...)
		c.readBuf = nil
		if c.readFDs != nil {
			fds = append(fds, c.readFDs...)
			c.readFDs = nil
		}

		buf := make([]byte, 8192)
		oob := make([]byte, unix.CmsgSpace(maxFDsPerRecv*4))

		n, oobn, _, _, err := unix.Recvmsg(c.fd, buf, oob, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("recvmsg: %w", err)
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("connection closed")
		}

		c.readBuf = buf[:n]

		if oobn > 0 {
			msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err != nil {
				return nil, nil, fmt.Errorf("ParseSocketControlMessage: %w", err)
			}
			for _, msg := range msgs {
				if msg.Header.Level != unix.SOL_SOCKET || msg.Header.Type != unix.SCM_RIGHTS {
					continue
				}
				rawFDs, err := unix.ParseUnixRights(&msg)
				if err != nil {
					return nil, nil, fmt.Errorf("parsing SCM_RIGHTS: %w", err)
				}
				for _, fd := range rawFDs {
					c.readFDs = append(c.readFDs, os.NewFile(uintptr(fd), "recvmsg-fd"))
				}
			}
		}
	}
}

// Close closes the connection, and any descriptors received but not yet
// handed out.
func (c *varlinkConn) Close() error {
	closeFiles(c.readFDs)
	c.readFDs = nil
	return c.conn.Close()
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}
