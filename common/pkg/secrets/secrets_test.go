package secrets

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"go.podman.io/common/pkg/secrets/filedriver"
	"go.podman.io/common/pkg/secrets/shelldriver"
)

var drivertype = "file"

func setup(t *testing.T) (manager *SecretsManager, opts map[string]string) {
	testpath := t.TempDir()
	manager, err := NewManager(testpath)
	require.NoError(t, err)
	return manager, map[string]string{"path": testpath}
}

func TestAddSecretAndLookupData(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
		Metadata:   map[string]string{"immutable": "true"},
		Labels: map[string]string{
			"foo":     "bar",
			"another": "label",
		},
	}

	id1, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.lookupSecret("mysecret")
	require.NoError(t, err)

	s, data, err := manager.LookupSecretData("mysecret")
	require.NoError(t, err)
	if !bytes.Equal(data, []byte("mydata")) {
		t.Errorf("error: secret data not equal")
	}
	if val, ok := s.Metadata["immutable"]; !ok || val != "true" {
		t.Errorf("error: no metadata")
	}
	if val, ok := s.Labels["foo"]; !ok || val != "bar" {
		t.Errorf("error: label incorrect")
	}
	if len(s.Labels) != 2 {
		t.Errorf("error: incorrect number of labels")
	}
	if s.CreatedAt != s.UpdatedAt {
		t.Errorf("error: secret CreatedAt should equal UpdatedAt when first created")
	}

	_, err = manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.Error(t, err)

	storeOpts.Replace = true
	id2, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)
	if id1 == id2 {
		t.Errorf("error: secret id after Replace should be different")
	}

	s, _, err = manager.LookupSecretData("mysecret")
	require.NoError(t, err)
	if s.CreatedAt.Equal(s.UpdatedAt) {
		t.Errorf("error: secret CreatedAt should not equal UpdatedAt after a Replace")
	}

	_, _, err = manager.LookupSecretData(id2)
	require.NoError(t, err)

	_, _, err = manager.LookupSecretData(id1)
	require.Error(t, err)
}

func TestAddSecretName(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	longstring := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

	for _, value := range []string{"a", "user@mail.com", longstring[:253]} {
		// test one char secret name
		_, err := manager.Store(value, []byte("mydata"), drivertype, storeOpts)
		require.NoError(t, err)

		_, err = manager.lookupSecret(value)
		require.NoError(t, err)
	}

	for _, value := range []string{"", "chocolate,vanilla", "file/path", "foo=bar", "bad\000Null", longstring[:254]} {
		_, err := manager.Store(value, []byte("mydata"), drivertype, storeOpts)
		require.Error(t, err)
	}
}

func TestAddMultipleSecrets(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	id, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	id2, err := manager.Store("mysecret2", []byte("mydata2"), drivertype, storeOpts)
	require.NoError(t, err)

	secrets, err := manager.List()
	require.NoError(t, err)
	require.Len(t, secrets, 2)

	_, err = manager.lookupSecret("mysecret")
	require.NoError(t, err)

	_, err = manager.lookupSecret("mysecret2")
	require.NoError(t, err)

	_, data, err := manager.LookupSecretData(id)
	require.NoError(t, err)
	if !bytes.Equal(data, []byte("mydata")) {
		t.Errorf("error: secret data not equal")
	}

	_, data2, err := manager.LookupSecretData(id2)
	require.NoError(t, err)
	if !bytes.Equal(data2, []byte("mydata2")) {
		t.Errorf("error: secret data not equal")
	}
}

func TestAddSecretDupName(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	_, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.Error(t, err)

	storeOpts.Replace = true
	_, err = manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	storeOpts.IgnoreIfExists = true
	_, err = manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.Error(t, err)

	storeOpts.Replace = false
	_, err = manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)
}

func TestAddReplaceSecretName(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
		Replace:    true,
	}

	_, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.Store("mysecret", []byte("mydata.diff"), drivertype, storeOpts)
	require.NoError(t, err)

	_, data, err := manager.LookupSecretData("mysecret")
	require.NoError(t, err)
	require.Equal(t, string(data), "mydata.diff")

	_, err = manager.Store("nonexistingsecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	storeOpts.Replace = false
	_, err = manager.Store("nonexistingsecret", []byte("newdata"), drivertype, storeOpts)
	require.Error(t, err)

	_, err = manager.Delete("nonexistingsecret")
	require.NoError(t, err)
}

func TestAddSecretPrefix(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	// If the randomly generated secret id is something like "abcdeiuoergnadufigh"
	// we should still allow someone to store a secret with the name "abcd" or "a"
	secretID, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.Store(secretID[0:5], []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)
}

func TestRemoveSecret(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	_, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.lookupSecret("mysecret")
	require.NoError(t, err)

	_, err = manager.Delete("mysecret")
	require.NoError(t, err)

	_, err = manager.lookupSecret("mysecret")
	require.Error(t, err)

	_, _, err = manager.LookupSecretData("mysecret")
	require.Error(t, err)
}

func TestRemoveSecretNoExist(t *testing.T) {
	manager, _ := setup(t)

	_, err := manager.Delete("mysecret")
	require.Error(t, err)
}

func TestLookupAllSecrets(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	id, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	// inspect using secret name
	lookup, err := manager.Lookup("mysecret")
	require.NoError(t, err)
	require.Equal(t, lookup.ID, id)
}

func TestInspectSecretId(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	id, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)

	_, err = manager.lookupSecret("mysecret")
	require.NoError(t, err)

	// inspect using secret id
	lookup, err := manager.Lookup(id)
	require.NoError(t, err)
	require.Equal(t, lookup.ID, id)

	// inspect using id prefix
	short := id[0:5]
	lookupshort, err := manager.Lookup(short)
	require.NoError(t, err)
	require.Equal(t, lookupshort.ID, id)
}

func TestInspectSecretBogus(t *testing.T) {
	manager, _ := setup(t)

	_, err := manager.Lookup("bogus")
	require.Error(t, err)
}

func TestSecretList(t *testing.T) {
	manager, opts := setup(t)

	storeOpts := StoreOptions{
		DriverOpts: opts,
	}

	_, err := manager.Store("mysecret", []byte("mydata"), drivertype, storeOpts)
	require.NoError(t, err)
	_, err = manager.Store("mysecret2", []byte("mydata2"), drivertype, storeOpts)
	require.NoError(t, err)

	allSecrets, err := manager.List()
	require.NoError(t, err)
	require.Len(t, allSecrets, 2)
}

// Creating a new secret with Replace=true should not remove other existing secrets.
func TestReplaceNewSecretDoesNotDeleteOthers(t *testing.T) {
	manager, _ := setup(t)

	// file driver storage
	pathFile := t.TempDir()
	fileOpts := StoreOptions{DriverOpts: map[string]string{"path": pathFile}}

	// shell driver storage (different directory)
	shellBase := t.TempDir()
	shellOptsMap := map[string]string{
		"store":  fmt.Sprintf("cat - > %s/${SECRET_ID}", shellBase),
		"lookup": fmt.Sprintf("cat %s/${SECRET_ID}", shellBase),
		"delete": "true", // emulate a non-zero exit code
		"list":   "true",
	}
	shellOpts := StoreOptions{DriverOpts: shellOptsMap, Replace: true}

	// Store first secret using file driver
	id1, err := manager.Store("test1", []byte("data1"), "file", fileOpts)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// Store a new secret using shell driver and Replace=true (should not affect test1)
	id2, err := manager.Store("test2", []byte("data2"), "shell", shellOpts)
	require.NoError(t, err)
	require.NotEmpty(t, id2)

	// Both secrets should exist in the manager
	all, err := manager.List()
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Verify file driver data still contains id1
	fd, err := filedriver.NewDriver(pathFile)
	require.NoError(t, err)
	d1, err := fd.Lookup(id1)
	require.NoError(t, err)
	require.Equal(t, []byte("data1"), d1)

	// Verify shell storage contains id2
	sd, err := shelldriver.NewDriver(shellOptsMap)
	require.NoError(t, err)
	d2, err := sd.Lookup(id2)
	require.NoError(t, err)
	require.Equal(t, []byte("data2"), d2)
}

func TestReplaceUsesOriginalDriverToDelete(t *testing.T) {
	manager, _ := setup(t)

	// Two separate filedriver storage roots
	path1 := t.TempDir()
	path2 := t.TempDir()

	// Store secret initially in path1
	storeOpts1 := StoreOptions{DriverOpts: map[string]string{"path": path1}}
	id1, err := manager.Store("mysecret", []byte("orig"), "file", storeOpts1)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// Ensure the secret entry exists in path1 data file
	fd1, err := filedriver.NewDriver(path1)
	require.NoError(t, err)
	dOrig, err := fd1.Lookup(id1)
	require.NoError(t, err)
	require.Equal(t, []byte("orig"), dOrig)

	// Replace the secret using a different driver storage path (path2)
	storeOpts2 := StoreOptions{DriverOpts: map[string]string{"path": path2}, Replace: true}
	id2, err := manager.Store("mysecret", []byte("new"), "file", storeOpts2)
	require.NoError(t, err)
	require.NotEmpty(t, id2)
	require.NotEqual(t, id1, id2)

	// Old id should no longer exist in path1 (deleted via previous driver)
	_, err = fd1.Lookup(id1)
	require.Error(t, err, "old secret id should no longer exist in path1")

	// New id should exist in path2
	fd2, err := filedriver.NewDriver(path2)
	require.NoError(t, err)
	dNew, err := fd2.Lookup(id2)
	require.NoError(t, err)
	require.Equal(t, []byte("new"), dNew)
}
