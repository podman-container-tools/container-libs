package types

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func expandEnvPath(path string, rootlessUID int) string {
	path = strings.ReplaceAll(path, "$UID", strconv.Itoa(rootlessUID))
	path = os.ExpandEnv(path)
	newpath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return newpath
}
