package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// path is the path to an example hook executable.
var path string

// unavoidableEnvironmentKeys may be injected even if the hook
// executable is executed with a requested empty environment.
var unavoidableEnvironmentKeys []string

func TestRun(t *testing.T) {
	ctx := t.Context()
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "cat"},
	}
	var stderr, stdout bytes.Buffer
	err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), Stdout: &stdout, Stderr: &stderr, PostKillTimeout: DefaultPostKillTimeout})
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "{}", stdout.String())
	assert.Equal(t, "", stderr.String())
}

func TestRunIgnoreOutput(t *testing.T) {
	ctx := t.Context()
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "cat"},
	}
	err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), PostKillTimeout: DefaultPostKillTimeout})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunFailedStart(t *testing.T) {
	ctx := t.Context()
	hook := &rspec.Hook{
		Path: "/does/not/exist",
	}
	err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), PostKillTimeout: DefaultPostKillTimeout})
	if err == nil {
		t.Fatal("unexpected success")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func parseEnvironment(input string) (env map[string]string, err error) {
	env = map[string]string{}
	input = strings.TrimSuffix(input, "\n") // strip the terminal newline, if any
	for line := range strings.SplitSeq(input, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return env, fmt.Errorf("no = in environment line: %q", line)
		}
		env[key] = value
	}
	for _, key := range unavoidableEnvironmentKeys {
		delete(env, key)
	}
	return env, nil
}

func TestRunEnvironment(t *testing.T) {
	ctx := t.Context()
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "env"},
	}
	for _, tt := range []struct {
		name     string
		env      []string
		expected map[string]string
	}{
		{
			name:     "unset",
			expected: map[string]string{},
		},
		{
			name:     "set empty",
			env:      []string{},
			expected: map[string]string{},
		},
		{
			name: "set",
			env: []string{
				"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			},
			expected: map[string]string{
				"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			},
		},
	} {
		test := tt
		t.Run(test.name, func(t *testing.T) {
			var stderr, stdout bytes.Buffer
			hook.Env = test.env
			err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), Stdout: &stdout, Stderr: &stderr, PostKillTimeout: DefaultPostKillTimeout})
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, "", stderr.String())

			env, err := parseEnvironment(stdout.String())
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, test.expected, env)
		})
	}
}

func TestRunCwd(t *testing.T) {
	ctx := t.Context()
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "pwd"},
	}
	cwd := t.TempDir()
	var stderr, stdout bytes.Buffer
	err := RunWithOptions(ctx, RunOptions{Hook: hook, Dir: cwd, State: []byte("{}"), Stdout: &stdout, Stderr: &stderr, PostKillTimeout: DefaultPostKillTimeout})
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "", stderr.String())
	assert.Equal(t, strings.TrimSuffix(stdout.String(), "\n"), cwd)
}

func TestRunCancel(t *testing.T) {
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "echo waiting; sleep 2; echo done"},
	}
	for _, tt := range []struct {
		name               string
		contextTimeout     time.Duration
		hookTimeout        *int
		expectedHookString string
		expectedErrorIs    error
		expectedStdout     string
	}{
		{
			name:           "no timeouts",
			expectedStdout: "waiting\ndone\n",
		},
		{
			name:               "context timeout",
			contextTimeout:     time.Duration(1) * time.Second,
			expectedStdout:     "waiting\n",
			expectedHookString: "executing [sh -c echo waiting; sleep 2; echo done]: signal: killed, context deadline exceeded",
			expectedErrorIs:    context.DeadlineExceeded,
		},
		{
			name:               "hook timeout",
			hookTimeout:        new(1),
			expectedStdout:     "waiting\n",
			expectedHookString: "executing [sh -c echo waiting; sleep 2; echo done]: signal: killed, context deadline exceeded",
			expectedErrorIs:    context.DeadlineExceeded,
		},
	} {
		test := tt
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			var stderr, stdout bytes.Buffer
			if test.contextTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.contextTimeout)
				defer cancel()
			}
			hook.Timeout = test.hookTimeout
			err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), Stdout: &stdout, Stderr: &stderr, PostKillTimeout: DefaultPostKillTimeout})
			if test.expectedErrorIs != nil {
				assert.ErrorIs(t, err, test.expectedErrorIs)
			} else {
				assert.NoError(t, err)
			}
			if test.expectedHookString != "" {
				require.Error(t, err)
				assert.Equal(t, test.expectedHookString, err.Error())
			}

			assert.Equal(t, "", stderr.String())
			assert.Equal(t, test.expectedStdout, stdout.String())
		})
	}
}

func TestRunKillTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(500)*time.Millisecond)
	defer cancel()
	hook := &rspec.Hook{
		Path: path,
		Args: []string{"sh", "-c", "sleep 1"},
	}
	err := RunWithOptions(ctx, RunOptions{Hook: hook, State: []byte("{}"), PostKillTimeout: time.Duration(0)})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Regexp(t, "^(failed to reap process within 0s of the kill signal|executing \\[sh -c sleep 1]: signal: killed), context deadline exceeded$", err)
}

func init() {
	if runtime.GOOS != "windows" {
		path = "/bin/sh"
		unavoidableEnvironmentKeys = []string{"PWD", "SHLVL", "_"}
	} else {
		panic("we need a reliable executable path on Windows")
	}
}
