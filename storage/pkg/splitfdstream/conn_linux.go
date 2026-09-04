//go:build linux

package splitfdstream

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
	conn    *net.UnixConn
	rawConn int

	// Read buffer: we need manual buffering because SCM_RIGHTS
	// ancillary data is attached to a specific recvmsg call.
	readBuf []byte
	readFDs []*os.File
}

func newVarlinkConn(conn *net.UnixConn) *varlinkConn {
	raw, _ := conn.File()
	fd := int(raw.Fd())
	raw.Close() // FileConn already duped the fd; drop the extra
	return &varlinkConn{
		conn:    conn,
		rawConn: fd,
	}
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
	for _, f := range fds {
		f.Close()
	}
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
	return len(buf), unix.Sendmsg(c.rawFD(), buf, rights, nil, 0)
}

// ReadBytesWithFDs implements varlink.FDReadWriter.
func (c *varlinkConn) ReadBytesWithFDs(_ context.Context, delim byte) ([]byte, []*os.File, error) {
	return c.readBytesWithFDsInternal(delim)
}

func (c *varlinkConn) rawFD() int {
	rawConn, _ := c.conn.SyscallConn()
	var fd int
	_ = rawConn.Control(func(f uintptr) {
		fd = int(f)
	})
	return fd
}

func (c *varlinkConn) readBytesWithFDsInternal(delim byte) ([]byte, []*os.File, error) {
	var result []byte
	var fds []*os.File

	rawFD := c.rawFD()

	for {
		// Check buffered data first.
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
		// Consume all buffered data.
		result = append(result, c.readBuf...)
		c.readBuf = nil
		if c.readFDs != nil {
			fds = append(fds, c.readFDs...)
			c.readFDs = nil
		}

		// Receive more data.
		buf := make([]byte, 8192)
		oob := make([]byte, unix.CmsgSpace(maxFDsPerRecv*4))

		n, oobn, _, _, err := unix.Recvmsg(rawFD, buf, oob, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("recvmsg: %w", err)
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("connection closed")
		}

		c.readBuf = buf[:n]

		// Parse any SCM_RIGHTS ancillary data.
		if oobn > 0 {
			msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
			if err != nil {
				return nil, nil, fmt.Errorf("ParseSocketControlMessage: %w", err)
			}
			for _, msg := range msgs {
				rawFDs, err := unix.ParseUnixRights(&msg)
				if err != nil {
					continue
				}
				for _, fd := range rawFDs {
					c.readFDs = append(c.readFDs, os.NewFile(uintptr(fd), "recvmsg-fd"))
				}
			}
		}
	}
}

// Close closes the connection.
func (c *varlinkConn) Close() error {
	for _, f := range c.readFDs {
		f.Close()
	}
	c.readFDs = nil
	return c.conn.Close()
}
