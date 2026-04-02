package configfile

import (
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_getDropInPaths(t *testing.T) {
	tests := []struct {
		name string
		// Arguments for this function
		mainPath string
		suffix   string
		uid      int
		// Expected result
		want []string
	}{
		{
			name:     "basic rootful",
			mainPath: "/etc/containers/containers.conf",
			suffix:   ".conf",
			uid:      0,
			want:     []string{"/etc/containers/containers.conf.d", "/etc/containers/containers.rootful.conf.d"},
		},
		{
			name:     "basic rootless uid 500",
			mainPath: "/etc/containers/containers.conf",
			suffix:   ".conf",
			uid:      500,
			want:     []string{"/etc/containers/containers.conf.d", "/etc/containers/containers.rootless.conf.d", "/etc/containers/containers.rootless.conf.d/500"},
		},
		{
			name:     "basic rootless uid 1234",
			mainPath: "/etc/containers/containers.conf",
			suffix:   ".conf",
			uid:      1234,
			want:     []string{"/etc/containers/containers.conf.d", "/etc/containers/containers.rootless.conf.d", "/etc/containers/containers.rootless.conf.d/1234"},
		},
		{
			name:     "path with extra dots",
			mainPath: "/path.with.dots/containers.conf",
			suffix:   ".conf",
			uid:      0,
			want:     []string{"/path.with.dots/containers.conf.d", "/path.with.dots/containers.rootful.conf.d"},
		},
		{
			name:     "/usr rootful",
			mainPath: "/usr/share/containers/containers.conf",
			suffix:   ".conf",
			uid:      0,
			want:     []string{"/usr/share/containers/containers.conf.d", "/usr/share/containers/containers.rootful.conf.d"},
		},
		{
			name:     "storage.conf",
			mainPath: "/usr/share/containers/storage.conf",
			suffix:   ".conf",
			uid:      0,
			want:     []string{"/usr/share/containers/storage.conf.d", "/usr/share/containers/storage.rootful.conf.d"},
		},
		{
			name:     "storage.conf",
			mainPath: "/usr/share/containers/storage.conf",
			suffix:   ".conf",
			uid:      0,
			want:     []string{"/usr/share/containers/storage.conf.d", "/usr/share/containers/storage.rootful.conf.d"},
		},
		{
			name:     "registries.d",
			mainPath: "/usr/share/containers/registries",
			suffix:   ".yaml",
			uid:      0,
			want:     []string{"/usr/share/containers/registries.d", "/usr/share/containers/registries.rootful.d"},
		},
		{
			name:     "registries.d rootless",
			mainPath: "/usr/share/containers/registries",
			suffix:   ".yaml",
			uid:      99,
			want:     []string{"/usr/share/containers/registries.d", "/usr/share/containers/registries.rootless.d", "/usr/share/containers/registries.rootless.d/99"},
		},
	}
	t.Parallel()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getDropInPaths(tt.mainPath, tt.suffix, tt.uid)
			assert.Equal(t, tt.want, got)
		})
	}
}

// File layout of the test files, map key is filename/path and value is the content
type testfiles struct {
	usr  map[string]string
	etc  map[string]string
	home map[string]string
}

func Test_Read(t *testing.T) {
	type testcase struct {
		name string
		// Arguments for this function
		arg File
		// Layout of the actual files we try to parse
		files testfiles
		// setup is extra setup that must be run before the test
		setup func(t *testing.T, tc *testcase)
		// Expected result, file content in right order.
		want []string
		// wantErr is the error type matched with errors.Is() is the function should error instead
		wantErr error
	}

	tests := []testcase{
		{
			name: "no files",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			want: nil,
		},
		{
			name: "simple main file",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "content1",
					// file with different name should not be read
					"storage.conf": "content2",
				},
			},
			want: []string{"content1"},
		},
		{
			name: "etc overrides usr file",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "content1",
				},
				etc: map[string]string{
					"containers.conf": "file2",
				},
			},
			want: []string{"file2"},
		},
		{
			name: "home overrides etc and usr file",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "content1",
				},
				etc: map[string]string{
					"containers.conf": "file2",
				},
				home: map[string]string{
					"containers.conf": "home",
				},
			},
			want: []string{"home"},
		},
		{
			name: "single drop in",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf.d/10-myconf.conf": "content1",
				},
			},
			want: []string{"content1"},
		},
		{
			name: "drop in and main file",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},

			files: testfiles{
				usr: map[string]string{
					"containers.conf":                  "file1",
					"containers.conf.d/10-myconf.conf": "file2",
				},
			},
			want: []string{"file1", "file2"},
		},
		{
			name: "drop in and main file on different paths",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},

			files: testfiles{
				usr: map[string]string{
					"containers.conf.d/10-myconf.conf": "usr",
				},
				etc: map[string]string{
					"containers.conf": "etc",
				},
			},
			want: []string{"etc", "usr"},
		},
		{
			name: "drop in order",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},

			files: testfiles{
				usr: map[string]string{
					"containers.conf.d/20-conf2.conf": "2",
					"containers.conf.d/40-conf4.conf": "4",
				},
				etc: map[string]string{
					"containers.conf.d/10-conf1.conf": "1",
				},
				home: map[string]string{
					"containers.conf.d/30-conf3.conf": "3",
				},
			},
			want: []string{"1", "2", "3", "4"},
		},
		{
			name: "drop in override",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					// This should be ignored because etc has the same filename
					"containers.conf.d/10-settings.conf": "usr-content",
					"containers.conf.d/20-settings.conf": "usr-content-2",
				},
				etc: map[string]string{
					// This should win
					"containers.conf.d/10-settings.conf": "etc-override",
				},
			},
			want: []string{"etc-override", "usr-content-2"},
		},
		{
			name: "drop in ignores wrong extensions",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf.d/10-valid.conf": "valid",
					"containers.conf.d/README.md":     "ignore-me",
					"containers.conf.d/backup.conf~":  "ignore-me-too",
				},
			},
			want: []string{"valid"},
		},
		{
			name: "policy.json main files only (ignore drop-ins)",
			arg: File{
				Name:                 "policy",
				Extension:            "json",
				DoNotLoadDropInFiles: true,
			},
			files: testfiles{
				usr: map[string]string{
					"policy.json":                 "main",
					"policy.json.d/10-extra.json": "drop-in",
				},
			},
			want: []string{"main"},
		},
		{
			name: "registries.d drop ins only (ignore main)",
			arg: File{
				Name:                           "registries",
				Extension:                      "yaml",
				DoNotLoadMainFiles:             true,
				DoNotUseExtensionForConfigName: true,
			},
			files: testfiles{
				usr: map[string]string{
					"registries.yaml":            "main",
					"registries.d/10-extra.yaml": "drop-in",
				},
			},
			want: []string{"drop-in"},
		},
		{
			name: "rootless specific drop-ins",
			arg: File{
				Name:      "containers",
				Extension: "conf",
				UserId:    1000,
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf.d/01-global.conf":        "global",
					"containers.rootless.conf.d/02-user.conf": "rootless-specific",
					"containers.rootful.conf.d/02-root.conf":  "rootful-ignored",
				},
			},
			want: []string{"global", "rootless-specific"},
		},
		{
			name: "rootless uid specific drop-ins",
			arg: File{
				Name:      "containers",
				Extension: "conf",
				UserId:    1000,
			},
			files: testfiles{
				usr: map[string]string{
					"containers.rootless.conf.d/1000/settings.conf": "uid-1000",
					"containers.rootless.conf.d/99/settings.conf":   "uid-99",
				},
			},
			want: []string{"uid-1000"},
		},
		{
			name: "containers.conf env var not being set",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "content1",
				},
			},
			want: []string{"content1"},
		},
		{
			name: "containers.conf env var must override all files",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":           "content1",
					"containers.conf.d/01.conf": "01",
				},
			},
			setup: func(t *testing.T, tc *testcase) {
				// filename does not need to end in .conf
				file := filepath.Join(t.TempDir(), "somepath")
				err := os.WriteFile(file, []byte("env"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF", file)
			},
			want: []string{"env"},
		},
		{
			name: "containers.conf override env var should be appended",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":           "content1",
					"containers.conf.d/01.conf": "01",
				},
			},
			setup: func(t *testing.T, tc *testcase) {
				file := filepath.Join(t.TempDir(), "somepath")
				err := os.WriteFile(file, []byte("env"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF_OVERRIDE", file)
			},
			want: []string{"content1", "01", "env"},
		},
		{
			name: "containers.conf both env var should be appended",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":           "content1",
					"containers.conf.d/01.conf": "01",
				},
			},
			setup: func(t *testing.T, tc *testcase) {
				file1 := filepath.Join(t.TempDir(), "path1")
				err := os.WriteFile(file1, []byte("env1"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF", file1)

				file2 := filepath.Join(t.TempDir(), "path1")
				err = os.WriteFile(file2, []byte("env2"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF_OVERRIDE", file2)
			},
			want: []string{"env1", "env2"},
		},
		{
			name: "env var should error on non existing file",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			setup: func(t *testing.T, tc *testcase) {
				file := filepath.Join(t.TempDir(), "123")
				t.Setenv("CONTAINERS_CONF", file)
			},
			wantErr: fs.ErrNotExist,
		},
		{
			name: "override env var should error on non existing file",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
			},
			setup: func(t *testing.T, tc *testcase) {
				file := filepath.Join(t.TempDir(), "123")
				t.Setenv("CONTAINERS_CONF_OVERRIDE", file)
			},
			wantErr: fs.ErrNotExist,
		},
		{
			name: "containers.conf with modules",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
				Modules:         []string{"module.abc"},
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "content1",
				},
				home: map[string]string{
					// file extension should not matter for modules
					"containers.conf.modules/module.abc": "relative module",
				},
			},
			setup: func(t *testing.T, tc *testcase) {
				file := filepath.Join(t.TempDir(), "somepath")
				err := os.WriteFile(file, []byte("absolute module"), 0o600)
				require.NoError(t, err)
				tc.arg.Modules = append(tc.arg.Modules, file)
			},
			want: []string{"content1", "relative module", "absolute module"},
		},
		{
			name: "containers.conf with module override",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
				Modules:         []string{"module.conf", "different.conf"},
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf.modules/module.conf": "usr",
				},
				etc: map[string]string{
					"containers.conf.modules/different.conf": "etc",
				},
				home: map[string]string{
					"containers.conf.modules/module.conf": "home",
				},
			},
			want: []string{"home", "etc"},
		},
		{
			// same as above except we switch the module order to ensure we read the files in the proper order as given
			name: "containers.conf with module override inverse module order",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
				Modules:         []string{"different.conf", "module.conf"},
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf.modules/module.conf": "usr",
				},
				etc: map[string]string{
					"containers.conf.modules/different.conf": "etc",
				},
				home: map[string]string{
					"containers.conf.modules/module.conf": "home",
				},
			},
			want: []string{"etc", "home"},
		},
		{
			name: "containers.conf env and modules order",
			arg: File{
				Name:            "containers",
				Extension:       "conf",
				EnvironmentName: "CONTAINERS_CONF",
				Modules:         []string{"module.conf"},
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":                     "content1",
					"containers.conf.d/01.conf":           "01",
					"containers.conf.modules/module.conf": "mod",
				},
			},

			setup: func(t *testing.T, tc *testcase) {
				file1 := filepath.Join(t.TempDir(), "path1")
				err := os.WriteFile(file1, []byte("env1"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF", file1)

				file2 := filepath.Join(t.TempDir(), "path1")
				err = os.WriteFile(file2, []byte("env2"), 0o600)
				require.NoError(t, err)
				t.Setenv("CONTAINERS_CONF_OVERRIDE", file2)
			},
			// CONTAINERS_CONF, then modules, then CONTAINERS_CONF_OVERRIDE
			want: []string{"env1", "mod", "env2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.arg.RootForImplicitAbsolutePaths = t.TempDir()
			writeTestFiles(t, tt.arg.RootForImplicitAbsolutePaths, tt.files)
			if tt.setup != nil {
				tt.setup(t, &tt)
			}
			seq := Read(&tt.arg)
			if tt.wantErr == nil {
				confs := collectConfigs(t, seq)
				assert.Equal(t, tt.want, confs)

				// ensure the modules all get resolves to absolute paths and are valid
				for _, module := range tt.arg.Modules {
					assert.FileExists(t, module)
					assert.True(t, filepath.IsAbs(module))
				}
			} else {
				next, stop := iter.Pull2(seq)
				defer stop()

				_, err, ok := next()
				assert.True(t, ok)
				assert.ErrorIs(t, err, tt.wantErr)

				// end of iterator
				_, _, ok = next()
				assert.False(t, ok)
			}
		})
	}
}

func Test_ContainersResourceDirs(t *testing.T) {
	type testcase struct {
		name string
		arg  Directory
		// Layout of files under usr / etc / home (see writeTestFiles).
		files testfiles
		// setup runs after writeTestFiles; use for directories that are not file-backed.
		setup func(t *testing.T, root string)
		want  func(root string) []string
	}

	tests := []testcase{
		{
			name: "no matching directories",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			want: func(root string) []string { return []string{} },
		},
		{
			name: "system drop-in directory only",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.d/10-trust": "",
				},
			},
			want: func(root string) []string {
				base := filepath.Join(root, systemConfigPath, "certs")
				return []string{base + ".d"}
			},
		},
		{
			name: "system rootful drop-in and general drop-in",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.d/10-a":         "",
					"certs.rootful.d/20-b": "",
				},
			},
			want: func(root string) []string {
				base := filepath.Join(root, systemConfigPath, "certs")
				return []string{base + ".rootful.d", base + ".d"}
			},
		},
		{
			name: "admin override before system paths in search order",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.d/sys": "",
				},
				etc: map[string]string{
					"certs.d/etc": "",
				},
			},
			want: func(root string) []string {
				defBase := filepath.Join(root, systemConfigPath, "certs")
				ovBase := filepath.Join(root, adminOverrideConfigPath, "certs")
				return []string{ovBase + ".d", defBase + ".d"}
			},
		},
		{
			name: "user config drop-in before override and system",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{"certs.d/s": ""},
				etc: map[string]string{"certs.d/e": ""},
				home: map[string]string{
					"certs.d/h": "",
				},
			},
			want: func(root string) []string {
				defBase := filepath.Join(root, systemConfigPath, "certs")
				ovBase := filepath.Join(root, adminOverrideConfigPath, "certs")
				userBase := filepath.Join(root, "home", "containers", "certs")
				return []string{userBase + ".d", ovBase + ".d", defBase + ".d"}
			},
		},
		{
			name: "rootless uid-specific drop-in directory",
			arg: Directory{
				Name:   "certs",
				UserId: 500,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.rootless.d/500/10-x": "",
				},
			},
			want: func(root string) []string {
				base := filepath.Join(root, systemConfigPath, "certs")
				less := base + ".rootless.d"
				return []string{filepath.Join(less, "500"), less}
			},
		},
		{
			name: "extra directories appended",
			arg: Directory{
				Name:      "certs",
				UserId:    0,
				ExtraDirs: []string{"/var/extra/certs"},
			},
			setup: func(t *testing.T, root string) {
				p := filepath.Join(root, "var", "extra", "certs")
				require.NoError(t, os.MkdirAll(p, 0o755))
			},
			want: func(root string) []string {
				return []string{filepath.Join(root, "var", "extra", "certs")}
			},
		},
		{
			name: "non-directory path is skipped",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.d": "not-a-directory",
				},
			},
			want: func(root string) []string { return []string{} },
		},
		{
			name: "user config path resolution failure is ignored",
			arg: Directory{
				Name:   "certs",
				UserId: 0,
			},
			files: testfiles{
				usr: map[string]string{
					"certs.d/sys": "",
				},
			},
			setup: func(t *testing.T, root string) {
				t.Helper()
				old := userConfigPathForResourceDirs
				t.Cleanup(func() { userConfigPathForResourceDirs = old })
				userConfigPathForResourceDirs = func() (string, error) {
					return "", fmt.Errorf("synthetic failure")
				}
			},
			want: func(root string) []string {
				defBase := filepath.Join(root, systemConfigPath, "certs")
				return []string{defBase + ".d"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.arg.RootForImplicitAbsolutePaths = root
			writeTestFiles(t, root, tt.files)
			if tt.setup != nil {
				tt.setup(t, root)
			}
			got, err := ContainersResourceDirs(&tt.arg)
			require.NoError(t, err)
			assert.Equal(t, tt.want(root), got)
		})
	}
}

func writeTestFiles(t *testing.T, tmpdir string, files testfiles) {
	t.Helper()
	usr := filepath.Join(tmpdir, systemConfigPath)
	require.NoError(t, os.MkdirAll(usr, 0o755))
	writeTestFileMap(t, usr, files.usr)

	etc := filepath.Join(tmpdir, adminOverrideConfigPath)
	require.NoError(t, os.MkdirAll(etc, 0o755))
	writeTestFileMap(t, etc, files.etc)

	home := filepath.Join(tmpdir, "home")
	t.Setenv("XDG_CONFIG_HOME", home)
	homeContainers := filepath.Join(home, "containers")
	require.NoError(t, os.MkdirAll(homeContainers, 0o755))
	writeTestFileMap(t, homeContainers, files.home)
}

func writeTestFileMap(t *testing.T, path string, files map[string]string) {
	t.Helper()
	for name, value := range files {
		fullPath := filepath.Join(path, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o755))
		err := os.WriteFile(fullPath, []byte(value), 0o600)
		require.NoError(t, err)
	}
}

// collectConfigs consumes the iterator and returns the content of the files read
func collectConfigs(t *testing.T, seq iter.Seq2[*Item, error]) []string {
	var contents []string
	for item, err := range seq {
		require.NoError(t, err)
		require.NotNil(t, item)
		data, err := io.ReadAll(item.Reader)
		require.NoError(t, err)

		contents = append(contents, string(data))
	}
	return contents
}

func Test_ParseTOML(t *testing.T) {
	type Config struct {
		Field1 bool
		Field2 string
		Field3 int
	}

	tests := []struct {
		name string
		// Arguments for this function
		arg File
		// Layout of the actual files we try to parse
		files testfiles
		// Expected result
		want *Config
		// wantErr set to the expected error message
		wantErr string
	}{
		{
			name: "simple parse",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "field1 = true\n",
				},
			},
			want: &Config{
				Field1: true,
			},
		},
		{
			name: "drop in parse",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":           "field1 = true\n",
					"containers.conf.d/10.conf": "field2 = \"abc\"",
				},
			},
			want: &Config{
				Field1: true,
				Field2: "abc",
			},
		},
		{
			name: "main file override",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf":           "field1 = true\n",
					"containers.conf.d/10.conf": "field2 = \"abc\"",
				},
				etc: map[string]string{
					"containers.conf": "field3 = 1\n",
				},
			},
			want: &Config{
				Field2: "abc",
				Field3: 1,
			},
		},
		{
			name: "invalid toml",
			arg: File{
				Name:      "containers",
				Extension: "conf",
			},
			files: testfiles{
				usr: map[string]string{
					"containers.conf": "blah\n",
				},
			},
			wantErr: "toml: line 1: expected '.' or '='",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.arg.RootForImplicitAbsolutePaths = t.TempDir()
			writeTestFiles(t, tt.arg.RootForImplicitAbsolutePaths, tt.files)

			conf := new(Config)
			err := ParseTOML(conf, &tt.arg)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, conf)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
