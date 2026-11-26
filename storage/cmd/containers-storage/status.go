package main

import (
	"fmt"
	"os"

	"go.podman.io/storage"
	"go.podman.io/storage/pkg/mflag"
)

func status(flags *mflag.FlagSet, action string, m storage.Store, args []string) (int, error) {
	status, err := m.Status()
	if err != nil {
		return 1, fmt.Errorf("status: %w", err)
	}
	basics := make([][2]string, 0, 4+len(status))
	basics = append(basics,
		[2]string{"Root", m.GraphRoot()},
		[2]string{"Run Root", m.RunRoot()},
		[2]string{"Driver Name", m.GraphDriverName()},
		[2]string{"Driver Options", fmt.Sprintf("%v", m.GraphOptions())},
	)
	status = append(basics, status...)
	if jsonOutput {
		return outputJSON(status)
	}
	for _, pair := range status {
		fmt.Fprintf(os.Stderr, "%s: %s\n", pair[0], pair[1])
	}
	return 0, nil
}

func init() {
	commands = append(commands, command{
		names:   []string{"status"},
		usage:   "Check on graph driver status",
		minArgs: 0,
		maxArgs: 0,
		action:  status,
		addFlags: func(flags *mflag.FlagSet, cmd *command) {
			flags.BoolVar(&jsonOutput, []string{"-json", "j"}, jsonOutput, "Prefer JSON output")
		},
	})
}
