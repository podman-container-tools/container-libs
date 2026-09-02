package hook

import (
	"testing"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	current "go.podman.io/common/pkg/hooks/1.1.0"
)

func TestGoodRead(t *testing.T) {
	hook, err := Read([]byte("{\"version\": \"1.0.0\", \"hook\": {\"path\": \"/a/b/c\"}, \"when\": {\"always\": true}, \"stages\": [\"prestart\"]}"))
	if err != nil {
		t.Fatal(err)
	}
	always := true
	assert.Equal(t, &current.Hook{
		Version: current.Version,
		Hook: rspec.Hook{
			Path: "/a/b/c",
		},
		When: current.When{
			Always: &always,
		},
		Stages: []string{"prestart"},
	}, hook)
}

func TestInvalidJSON(t *testing.T) {
	_, err := Read([]byte("{"))
	if err == nil {
		t.Fatal("unexpected success")
	}
	assert.Regexp(t, "^unexpected end of JSON input$", err.Error())
}
