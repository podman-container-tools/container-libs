package copy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	chunkedToc "go.podman.io/storage/pkg/chunked/toc"
)

func TestAnnotationsAfterCompressionChange(t *testing.T) {
	var chunkedKey string
	for key := range chunkedToc.ChunkedAnnotations {
		chunkedKey = key
		break
	}
	tests := []struct {
		original map[string]string
		expected map[string]string
	}{
		{nil, nil},
		{map[string]string{"org.example.content": "value"}, map[string]string{"org.example.content": "value"}},
		{map[string]string{"org.example.content": "value", chunkedKey: "stale"}, map[string]string{"org.example.content": "value"}},
	}
	for _, test := range tests {
		actual := annotationsAfterCompressionChange(test.original)
		assert.Equal(t, test.expected, actual)
	}
}
