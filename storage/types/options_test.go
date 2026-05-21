package types

import (
	"bytes"
	"os"
	"strconv"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/storage/pkg/unshare"
)

func TestInvalidKeyFile(t *testing.T) {
	t.Setenv(storageConfEnv, "./storage_broken.conf")
	content := bytes.NewBufferString("")
	logrus.SetOutput(content)
	defer logrus.SetOutput(os.Stderr)
	logrus.SetLevel(logrus.DebugLevel)
	defer logrus.SetLevel(logrus.InfoLevel)
	var storageOpts StoreOptions
	storageOpts, err := LoadStoreOptions(LoadOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/run/containers/test", storageOpts.RunRoot)

	assert.Contains(t, content.String(), "Failed to decode the keys [\\\"foo\\\" \\\"storage.options.graphroot\\\"] from \\\"./storage_broken.conf\\\"\"")
}

func TestLoadStoreOptions(t *testing.T) {
	t.Setenv(storageConfEnv, "./storage_test.conf")
	var storageOpts StoreOptions
	storageOpts, err := LoadStoreOptions(LoadOptions{})
	require.NoError(t, err)

	assert.Equal(t, "/run/"+strconv.Itoa(unshare.GetRootlessUID())+"/containers/storage", storageOpts.RunRoot)
	assert.Equal(t, os.Getenv("HOME")+"/"+strconv.Itoa(unshare.GetRootlessUID())+"/containers/storage", storageOpts.GraphRoot)
}
