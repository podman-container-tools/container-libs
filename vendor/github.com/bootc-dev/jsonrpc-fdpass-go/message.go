// Package fdpass implements JSON-RPC 2.0 with file descriptor passing over Unix domain sockets.
package fdpass

import (
	"encoding/json"
	"os"
)

// JSONRPCVersion is the JSON-RPC protocol version.
const JSONRPCVersion = "2.0"

// FDsKey is the JSON key for the file descriptor count field.
const FDsKey = "fds"

// FileDescriptorErrorCode is the error code for FD-related protocol errors.
const FileDescriptorErrorCode = -32050

// Request represents a JSON-RPC 2.0 request.
type Request struct {
	JsonRpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id"`
	// Fds is the number of file descriptors attached to this message.
	Fds *int `json:"fds,omitempty"`
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JsonRpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
	ID      interface{} `json:"id"`
	// Fds is the number of file descriptors attached to this message.
	Fds *int `json:"fds,omitempty"`
}

// Notification represents a JSON-RPC 2.0 notification (a request without an ID).
type Notification struct {
	JsonRpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	// Fds is the number of file descriptors attached to this message.
	Fds *int `json:"fds,omitempty"`
}

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return e.Message
}

// MessageWithFds wraps a JSON-RPC message with associated file descriptors.
type MessageWithFds struct {
	// Message is the JSON-RPC message (Request, Response, or Notification).
	Message interface{}
	// FileDescriptors are the file descriptors to pass with this message.
	// The order corresponds to indices 0..N-1 matching the message's fds count.
	FileDescriptors []*os.File
}

// NewRequest creates a new JSON-RPC request.
func NewRequest(method string, params interface{}, id interface{}) *Request {
	return &Request{
		JsonRpc: JSONRPCVersion,
		Method:  method,
		Params:  params,
		ID:      id,
	}
}

// NewResponse creates a new successful JSON-RPC response.
func NewResponse(result interface{}, id interface{}) *Response {
	return &Response{
		JsonRpc: JSONRPCVersion,
		Result:  result,
		ID:      id,
	}
}

// NewErrorResponse creates a new error JSON-RPC response.
func NewErrorResponse(err *Error, id interface{}) *Response {
	return &Response{
		JsonRpc: JSONRPCVersion,
		Error:   err,
		ID:      id,
	}
}

// NewNotification creates a new JSON-RPC notification.
func NewNotification(method string, params interface{}) *Notification {
	return &Notification{
		JsonRpc: JSONRPCVersion,
		Method:  method,
		Params:  params,
	}
}

// GetFDCount reads the file descriptor count from a JSON value.
// Returns 0 if the `fds` field is absent or not a valid number.
func GetFDCount(value map[string]interface{}) int {
	if fds, ok := value[FDsKey]; ok {
		switch v := fds.(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	return 0
}

// FileDescriptorError creates a standard FD error for protocol violations.
func FileDescriptorError() *Error {
	return &Error{
		Code:    FileDescriptorErrorCode,
		Message: "File Descriptor Error",
	}
}

// SetFDs sets the fds count on a Request.
func (r *Request) SetFDs(count int) {
	if count > 0 {
		r.Fds = &count
	} else {
		r.Fds = nil
	}
}

// GetFDs returns the fds count from a Request.
func (r *Request) GetFDs() int {
	if r.Fds != nil {
		return *r.Fds
	}
	return 0
}

// SetFDs sets the fds count on a Response.
func (r *Response) SetFDs(count int) {
	if count > 0 {
		r.Fds = &count
	} else {
		r.Fds = nil
	}
}

// GetFDs returns the fds count from a Response.
func (r *Response) GetFDs() int {
	if r.Fds != nil {
		return *r.Fds
	}
	return 0
}

// SetFDs sets the fds count on a Notification.
func (n *Notification) SetFDs(count int) {
	if count > 0 {
		n.Fds = &count
	} else {
		n.Fds = nil
	}
}

// GetFDs returns the fds count from a Notification.
func (n *Notification) GetFDs() int {
	if n.Fds != nil {
		return *n.Fds
	}
	return 0
}

// ParseMessage parses a raw JSON message into the appropriate type.
// It returns one of *Request, *Response, or *Notification.
func ParseMessage(data []byte) (interface{}, error) {
	// First parse as a generic map to determine type
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	// Determine message type based on fields present
	_, hasMethod := raw["method"]
	_, hasID := raw["id"]
	_, hasResult := raw["result"]
	_, hasError := raw["error"]

	if hasMethod && hasID {
		// Request
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, err
		}
		return &req, nil
	} else if hasResult || hasError {
		// Response
		var resp Response
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	} else if hasMethod {
		// Notification
		var notif Notification
		if err := json.Unmarshal(data, &notif); err != nil {
			return nil, err
		}
		return &notif, nil
	}

	return nil, &Error{Code: -32600, Message: "Invalid JSON-RPC message"}
}
