// EXPERIMENTAL: This package is experimental and subject to breaking changes.
// The APIs may change in incompatible ways without notice. Use with caution
// in production environments.
package jsonproxy

import (
	"os"

	"github.com/sirupsen/logrus"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/types"
)

// splitFDStreamStore is the subset of storage.SplitFDStreamStore needed
// by the json-proxy.  Keeping a local interface avoids a hard dependency
// on go.podman.io/storage for consumers that do not use splitfdstream.
type splitFDStreamStore interface {
	SplitFDStreamSocket() (*os.File, error)
}

// options holds the internal configuration for a Manager.
type options struct {
	getSystemContext func() (*types.SystemContext, error)
	getPolicyContext func() (*signature.PolicyContext, error)
	logger           logrus.FieldLogger
}

// Option configures a Manager. Use the With* functions to create Options.
//
// EXPERIMENTAL: This type is experimental and subject to breaking changes.
type Option func(*options)

// WithSystemContext sets the function used to obtain a SystemContext for image operations.
//
// EXPERIMENTAL: This function is experimental and subject to breaking changes.
func WithSystemContext(fn func() (*types.SystemContext, error)) Option {
	return func(o *options) {
		o.getSystemContext = fn
	}
}

// WithPolicyContext sets the function used to obtain a PolicyContext for signature verification.
//
// EXPERIMENTAL: This function is experimental and subject to breaking changes.
func WithPolicyContext(fn func() (*signature.PolicyContext, error)) Option {
	return func(o *options) {
		o.getPolicyContext = fn
	}
}

// WithLogger sets the logger for the Manager. If not provided, the logrus
// standard logger is used.
//
// EXPERIMENTAL: This function is experimental and subject to breaking changes.
func WithLogger(logger logrus.FieldLogger) Option {
	return func(o *options) {
		o.logger = logger
	}
}
