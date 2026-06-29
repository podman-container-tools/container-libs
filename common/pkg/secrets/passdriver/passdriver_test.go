package passdriver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.podman.io/common/pkg/secrets/define"
)

const gpgTestID = "testing@passdriver"

func setupDriver(t *testing.T) *Driver {
	base := t.TempDir()
	gpghomedir := t.TempDir()

	driver, err := NewDriver(map[string]string{
		"root":       base,
		"key":        gpgTestID,
		"gpghomedir": gpghomedir,
	})
	require.NoError(t, err)

	err = driver.gpg(context.TODO(), nil, nil, "--batch", "--passphrase", "--quick-generate-key", "testing@passdriver")
	require.NoError(t, err)

	return driver
}

func TestStoreAndLookup(t *testing.T) {
	cases := []struct {
		name         string
		key          string
		value        []byte
		expStoreErr  error
		expLookupErr error
	}{
		{
			name:  "store and lookup work for a simple key",
			key:   "simple",
			value: []byte("abc"),
		},
		{
			name:  "store and lookup work for a multiline string",
			key:   "long",
			value: []byte("abc\n123\ndef\n"),
		},
		{
			name:  "store and lookup work for non-utf8 data",
			key:   "long",
			value: []byte{0, 1, 2, 3, 0, 1, 2, 3},
		},
		{
			name:        "storing into a sneaky key fails",
			key:         "../../../sneaky",
			value:       []byte("abc"),
			expStoreErr: define.ErrInvalidKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := setupDriver(t)
			err := driver.Store(tc.key, tc.value)
			if tc.expStoreErr != nil {
				require.Error(t, err)
				require.Equal(t, tc.expStoreErr.Error(), err.Error())
			} else {
				require.NoError(t, err)
				val, err := driver.Lookup(tc.key)
				if tc.expLookupErr != nil {
					require.Error(t, err)
					require.Equal(t, tc.expLookupErr.Error(), err.Error())
				} else {
					require.NoError(t, err)
					require.Equal(t, tc.value, val)
				}
			}
		})
	}
}

func TestLookup(t *testing.T) {
	driver := setupDriver(t)

	// prepare a valid lookup target
	err := driver.Store("valid", []byte("abc"))
	require.NoError(t, err)

	cases := []struct {
		name     string
		key      string
		expValue []byte
		expErr   error
	}{
		{
			name:     "lookup of an existing key works",
			key:      "valid",
			expValue: []byte("abc"),
		},
		{
			name:   "lookup of a non-existing key fails",
			key:    "invalid",
			expErr: fmt.Errorf("invalid: %w", define.ErrNoSuchSecret),
		},
		{
			name:   "lookup of a sneaky key fails",
			key:    "../../../etc/shadow",
			expErr: define.ErrInvalidKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := driver.Lookup(tc.key)
			if tc.expErr == nil {
				require.Equal(t, tc.expValue, val)
			} else {
				require.EqualError(t, err, tc.expErr.Error())
			}
		})
	}
}

func TestList(t *testing.T) {
	driver := setupDriver(t)
	require.NoError(t, driver.Store("a", []byte("abc")))
	require.NoError(t, driver.Store("b", []byte("abc")))
	require.NoError(t, driver.Store("c", []byte("abc")))

	list, err := driver.List()
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, list)
}

func TestDelete(t *testing.T) {
	driver := setupDriver(t)
	require.NoError(t, driver.Store("a", []byte("abc")))

	cases := []struct {
		name   string
		key    string
		expErr error
	}{
		{
			name: "deleting an existing item works",
			key:  "a",
		},
		{
			name:   "deleting an non-existing item fails",
			key:    "wrong",
			expErr: fmt.Errorf("wrong: %w", define.ErrNoSuchSecret),
		},
		{
			name:   "using a sneaky path fails",
			key:    "../../../etc/shadow",
			expErr: define.ErrInvalidKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := driver.Delete(tc.key)
			if tc.expErr != nil {
				require.EqualError(t, err, tc.expErr.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestLookupDecryptFailureNotMasked reproduces the core bug from issue 28938:
// a GPG decrypt failure must not be reported as ErrNoSuchSecret.
func TestLookupDecryptFailureNotMasked(t *testing.T) {
	driver := setupDriver(t)

	err := driver.Store("corrupted", []byte("secret-data"))
	require.NoError(t, err)

	// Corrupt the .gpg file so GPG cannot decrypt it.
	key, err := driver.getPath("corrupted")
	require.NoError(t, err)
	err = os.WriteFile(key, []byte("not-valid-gpg-data"), 0o644)
	require.NoError(t, err)

	// Lookup must fail with a decrypt error, not ErrNoSuchSecret.
	_, err = driver.Lookup("corrupted")
	require.Error(t, err)
	require.NotErrorIs(t, err, define.ErrNoSuchSecret,
		"Lookup must not report 'no such secret' when the .gpg file exists but decryption fails")
}

// TestListFiltersNonGpgEntries verifies that List skips directories and
// non-.gpg files instead of panicking or returning garbage IDs.
func TestListFiltersNonGpgEntries(t *testing.T) {
	driver := setupDriver(t)
	require.NoError(t, driver.Store("valid", []byte("data")))

	// Add a non-.gpg file and a subdirectory to the store root.
	err := os.WriteFile(filepath.Join(driver.Root, "readme.txt"), []byte("hi"), 0o644)
	require.NoError(t, err)
	err = os.Mkdir(filepath.Join(driver.Root, "subdir"), 0o755)
	require.NoError(t, err)

	list, err := driver.List()
	require.NoError(t, err)
	require.Equal(t, []string{"valid"}, list,
		"List should only return IDs from .gpg files, skipping directories and other files")
}

// TestStoreExistenceCheckWithoutGpg verifies that Store detects duplicate IDs
// via os.Stat rather than a GPG decrypt, and returns ErrSecretIDExists.
func TestStoreExistenceCheckWithoutGpg(t *testing.T) {
	driver := setupDriver(t)

	err := driver.Store("exists", []byte("data"))
	require.NoError(t, err)

	// Storing the same ID again must fail with ErrSecretIDExists.
	err = driver.Store("exists", []byte("other"))
	require.Error(t, err)
	require.ErrorIs(t, err, define.ErrSecretIDExists)
}
