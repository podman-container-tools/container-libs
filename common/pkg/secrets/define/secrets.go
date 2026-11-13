package define

import (
	"errors"
)

var (
	// ErrNoSuchSecret indicates that the secret does not exist.
	ErrNoSuchSecret = errors.New("no such secret")

	// ErrSecretIDExists indicates that there is secret data already associated with an id.
	ErrSecretIDExists = errors.New("secret data with ID already exists")

	// ErrInvalidKey indicates that something about your key is wrong.
	ErrInvalidKey = errors.New("invalid key")

	// ErrInvalidArg indicates that at least one of given arguments is invalid.
	ErrInvalidArg = errors.New("invalid arguments")

	// ErrSecretExists indicates that the secret was already created.
	ErrSecretExists = errors.New("secret already exists")
)
