package rootlessnetns

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_refCount(t *testing.T) {
	tests := []struct {
		name    string
		content string
		inc     int
		want    int
	}{
		{
			name: "init counter",
			inc:  1,
			want: 1,
		},
		{
			name: "simple add",
			inc:  5,
			want: 5,
		},
		{
			name:    "add multiple with content",
			content: "0",
			inc:     5,
			want:    5,
		},
		{
			name:    "add multiple with high number content",
			content: "5500",
			inc:     2,
			want:    5502,
		},
		{
			name:    "simple dec",
			content: "5",
			inc:     -5,
			want:    0,
		},
		{
			name:    "dec negative should not go below 0",
			content: "0",
			inc:     -5,
			want:    0,
		},
		{
			name:    "dec multiple with high number content",
			content: "9800",
			inc:     -100,
			want:    9700,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, refCountFile)
			if tt.content != "" {
				err := os.WriteFile(file, []byte(tt.content), 0o700)
				assert.NoError(t, err, "write file error")
			}

			got, err := refCount(dir, tt.inc)
			assert.NoError(t, err, "refCount() error")
			assert.Equal(t, tt.want, got, "counter is equal")
			content, err := os.ReadFile(file)
			assert.NoError(t, err, "read file error")
			assert.Equal(t, strconv.Itoa(tt.want), string(content), "file content after refCount()")
		})
	}
}

func TestReadPidFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		create  bool
		wantErr error
		wantPid int
	}{
		{
			name:    "valid pid",
			content: "1234\n",
			create:  true,
			wantPid: 1234,
		},
		{
			name:    "valid pid no newline",
			content: "5678",
			create:  true,
			wantPid: 5678,
		},
		{
			name:    "empty pid file",
			content: "",
			create:  true,
			wantErr: errEmptyPIDFile,
		},
		{
			name:    "only whitespace",
			content: "  \n ",
			create:  true,
			wantErr: errEmptyPIDFile,
		},
		{
			name:    "non-existent file",
			create:  false,
			wantErr: os.ErrNotExist,
		},
		{
			name:    "invalid content",
			content: "abc",
			create:  true,
			wantErr: strconv.ErrSyntax,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pidfile")
			if tt.create {
				err := os.WriteFile(path, []byte(tt.content), 0o600)
				assert.NoError(t, err)
			}
			pid, err := readPidFile(path)
			if tt.wantErr != nil {
				assert.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantPid, pid)
			}
		})
	}
}
