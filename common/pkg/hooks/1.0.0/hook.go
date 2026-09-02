// Package hook is the 1.0.0 hook configuration structure.
package hook

import (
	"encoding/json"

	current "go.podman.io/common/pkg/hooks/1.1.0"
)

// Version is the hook configuration version defined in this package.
const Version = "1.0.0"

// Read reads hook JSON bytes, verifies them, and returns the hook configuration.
func Read(content []byte) (hook *current.Hook, err error) {
	if err = json.Unmarshal(content, &hook); err != nil {
		return nil, err
	}
	hook.Version = current.Version
	return hook, nil
}
