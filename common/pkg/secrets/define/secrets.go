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

// classifiedError attaches a kind (ErrInvalidArg/ErrSecretExists/etc) to err
// without changing its message so that errors.Is(err, kind) returns true.
type classifiedError struct {
	err  error
	kind error
}

func (e *classifiedError) Error() string {
	return e.err.Error()
}

func (e *classifiedError) Unwrap() error {
	return e.err
}

func (e *classifiedError) Is(target error) bool {
	return target == e.kind
}

// InvalidArg marks err as caused by invalid caller input
func InvalidArg(err error) error {
	return &classifiedError{
		err:  err,
		kind: ErrInvalidArg,
	}
}

// SecretExists marks err as a conflict with an existing secret
func SecretExists(err error) error {
	return &classifiedError{
		err:  err,
		kind: ErrSecretExists,
	}
}
