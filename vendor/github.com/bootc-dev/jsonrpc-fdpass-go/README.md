# jsonrpc-fdpass-go

A Go implementation of JSON-RPC 2.0 with file descriptor passing over Unix domain sockets.

This library implements the protocol specified in [jsonrpc-fdpass](https://github.com/bootc-dev/jsonrpc-fdpass).

## Protocol Overview

- **Transport**: Unix domain sockets (SOCK_STREAM)
- **Framing**: Self-delimiting JSON (streaming parser)
- **FD Passing**: Via sendmsg/recvmsg with SCM_RIGHTS ancillary data
- **FD Count**: Top-level `fds` field indicates the number of file descriptors attached

When file descriptors are attached to a message, the `fds` field is automatically
set to the count of FDs. File descriptors are passed positionally—the application
layer defines the semantic mapping between FD positions and parameters.

## Installation

```bash
go get github.com/bootc-dev/jsonrpc-fdpass-go
```

## Usage

```go
package main

import (
    "net"
    "os"

    fdpass "github.com/bootc-dev/jsonrpc-fdpass-go"
)

func main() {
    // Connect to a Unix socket
    conn, _ := net.DialUnix("unix", nil, &net.UnixAddr{Name: "/tmp/socket.sock", Net: "unix"})

    // Create sender and receiver
    sender := fdpass.NewSender(conn)
    receiver := fdpass.NewReceiver(conn)

    // Send a request with a file descriptor
    file, _ := os.Open("example.txt")
    defer file.Close()

    req := fdpass.NewRequest("readFile", map[string]interface{}{
        "path": "example.txt",
    }, 1)

    msg := &fdpass.MessageWithFds{
        Message:         req,
        FileDescriptors: []*os.File{file},
    }

    // The sender automatically sets the "fds" field to 1
    sender.Send(msg)

    // Receive response
    resp, _ := receiver.Receive()
    // Handle resp.Message and resp.FileDescriptors
}
```

## License

MIT
