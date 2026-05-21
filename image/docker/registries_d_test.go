package docker

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.podman.io/image/v5/types"
)

func dockerRefFromString(t *testing.T, s string) dockerReference {
	ref, err := ParseReference(s)
	require.NoError(t, err, s)
	dockerRef, ok := ref.(dockerReference)
	require.True(t, ok, s)
	return dockerRef
}

func writeDockerLookaside(t *testing.T, dir, filename, registry, lookaside string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, filename), fmt.Appendf(nil, "docker:\n  %s:\n    lookaside: %s\n", registry, lookaside), 0o644))
}

func TestSignatureStorageBaseURL(t *testing.T) {
	emptyDir := t.TempDir()
	for _, c := range []struct {
		dir, ref string
		expected string // Or "" to expect failure
	}{
		{ // Error reading configuration directory (/dev/null is not a directory)
			"/dev/null", "//busybox",
			"",
		},
		{ // No match found: expect default user storage base
			emptyDir, "//this/is/not/in/the:configuration",
			"file://" + filepath.Join(os.Getenv("HOME"), defaultUserDockerDir, "//this/is/not/in/the"),
		},
		{ // Invalid URL
			"fixtures/registries.d", "//localhost/invalid/url/test",
			"",
		},
		// URLs without a scheme: This will be rejected by consumers, so we don't really care about
		// the returned value, but it should not crash at the very least.
		{ // Absolute path
			"fixtures/registries.d", "//localhost/file/path/test",
			"/no/scheme/just/a/path/file/path/test",
		},
		{ // Relative path
			"fixtures/registries.d", "//localhost/relative/path/test",
			"no/scheme/relative/path/relative/path/test",
		},
		{ // Success
			"fixtures/registries.d", "//example.com/my/project",
			"https://lookaside.example.com/my/project",
		},
	} {
		base, err := SignatureStorageBaseURL(&types.SystemContext{RegistriesDirPath: c.dir},
			dockerRefFromString(t, c.ref), false)
		if c.expected != "" {
			require.NoError(t, err, c.ref)
			require.NotNil(t, base, c.ref)
			assert.Equal(t, c.expected, base.String(), c.ref)
		} else {
			assert.Error(t, err, c.ref)
		}
	}
}

func TestLoadRegistryConfiguration(t *testing.T) {
	type testcase struct {
		setup              func(t *testing.T) *types.SystemContext
		wantLookaside      map[string]string
		forbiddenDockerKey string
		expectErr          bool
	}
	tests := []testcase{
		{ // Explicit override directory: only load from there.
			setup: func(t *testing.T) *types.SystemContext {
				dir := t.TempDir()
				writeDockerLookaside(t, dir, "01.yaml", "example.com", "https://override.example.com")
				return &types.SystemContext{RegistriesDirPath: dir}
			},
			wantLookaside: map[string]string{
				"example.com": "https://override.example.com",
			},
		},
		{ // Default configfile search: drop-ins from /usr + /etc (under RootForImplicitAbsolutePaths); main registries.yaml ignored.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

				usrRegistriesD := filepath.Join(root, "usr/share/containers/registries.d")
				etcRegistriesD := filepath.Join(root, "etc/containers/registries.d")

				writeDockerLookaside(t, usrRegistriesD, "10-vendor.yaml", "example.com", "https://vendor.example.com")
				writeDockerLookaside(t, etcRegistriesD, "10-vendor.yaml", "example.com", "https://admin.example.com")
				writeDockerLookaside(t, filepath.Join(root, "etc/containers"), "registries.yaml", "should.not.be.loaded", "https://wrong.example.com")

				return &types.SystemContext{RootForImplicitAbsolutePaths: root}
			},
			wantLookaside: map[string]string{
				"example.com": "https://admin.example.com",
			},
			forbiddenDockerKey: "should.not.be.loaded",
		},
		{ // Explicit RegistriesDirPath bypasses configfile search completely.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
				userRegistriesD := filepath.Join(root, "xdg/containers/registries.d")
				writeDockerLookaside(t, userRegistriesD, "10-user.yaml", "example.com", "https://user.example.com")

				overrideDir := t.TempDir()
				writeDockerLookaside(t, overrideDir, "01.yaml", "example.com", "https://explicit.example.com")

				return &types.SystemContext{
					RegistriesDirPath:            overrideDir,
					RootForImplicitAbsolutePaths: root, // should not matter for the explicit override
				}
			},
			wantLookaside: map[string]string{
				"example.com": "https://explicit.example.com",
			},
		},
		{ // RootForImplicitAbsolutePaths does not affect explicit RegistriesDirPath.
			setup: func(t *testing.T) *types.SystemContext {
				overrideDir := t.TempDir()
				writeDockerLookaside(t, overrideDir, "01.yaml", "example.com", "https://explicit.example.com")

				root := t.TempDir()
				// If RootForImplicitAbsolutePaths were incorrectly applied to RegistriesDirPath,
				// we'd look under root+overrideDir which doesn't exist.
				return &types.SystemContext{RegistriesDirPath: overrideDir, RootForImplicitAbsolutePaths: root}
			},
			wantLookaside: map[string]string{
				"example.com": "https://explicit.example.com",
			},
		},
		{ // Explicit RegistriesDirPath is not env-expanded.
			setup: func(t *testing.T) *types.SystemContext {
				parent := t.TempDir()
				literalHomeDir := filepath.Join(parent, "$HOME")
				writeDockerLookaside(t, literalHomeDir, "01.yaml", "example.com", "https://literal.example.com")
				return &types.SystemContext{RegistriesDirPath: literalHomeDir}
			},
			wantLookaside: map[string]string{
				"example.com": "https://literal.example.com",
			},
		},
		{ // user XDG_CONFIG_HOME/.../registries.d has higher priority than /etc for same filename.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

				etcRegistriesD := filepath.Join(root, "etc/containers/registries.d")
				writeDockerLookaside(t, etcRegistriesD, "10-same.yaml", "example.com", "https://etc.example.com")

				userRegistriesD := filepath.Join(root, "xdg/containers/registries.d")
				writeDockerLookaside(t, userRegistriesD, "10-same.yaml", "example.com", "https://user.example.com")

				return &types.SystemContext{RootForImplicitAbsolutePaths: root}
			},
			wantLookaside: map[string]string{
				"example.com": "https://user.example.com",
			},
		},
		{ // Distinct filenames in user and /etc are both processed.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))

				etcRegistriesD := filepath.Join(root, "etc/containers/registries.d")
				writeDockerLookaside(t, etcRegistriesD, "10-etc-only.yaml", "example.com", "https://etc.example.com")

				userRegistriesD := filepath.Join(root, "xdg/containers/registries.d")
				writeDockerLookaside(t, userRegistriesD, "20-user-only.yaml", "example.net", "https://user.example.net")

				return &types.SystemContext{RootForImplicitAbsolutePaths: root}
			},
			wantLookaside: map[string]string{
				"example.com": "https://etc.example.com",
				"example.net": "https://user.example.net",
			},
		},
		{ // Duplicate docker namespace across distinct filenames errors.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
				etcRegistriesD := filepath.Join(root, "etc/containers/registries.d")
				writeDockerLookaside(t, etcRegistriesD, "10-a.yaml", "example.com", "https://a.example.com")
				writeDockerLookaside(t, etcRegistriesD, "20-b.yaml", "example.com", "https://b.example.com")
				return &types.SystemContext{RootForImplicitAbsolutePaths: root}
			},
			expectErr: true,
		},
		{ // Duplicate default-docker across distinct filenames errors.
			setup: func(t *testing.T) *types.SystemContext {
				root := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
				etcRegistriesD := filepath.Join(root, "etc/containers/registries.d")
				require.NoError(t, os.MkdirAll(etcRegistriesD, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(etcRegistriesD, "10-a.yaml"), []byte("default-docker:\n    lookaside: https://a.example.com\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(etcRegistriesD, "20-b.yaml"), []byte("default-docker:\n    lookaside: https://b.example.com\n"), 0o644))
				return &types.SystemContext{RootForImplicitAbsolutePaths: root}
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			sys := tt.setup(t)
			cfg, err := loadRegistryConfiguration(sys)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			for ns, lookaside := range tt.wantLookaside {
				assert.Equal(t, lookaside, cfg.Docker[ns].Lookaside)
			}
			if tt.forbiddenDockerKey != "" {
				_, ok := cfg.Docker[tt.forbiddenDockerKey]
				assert.False(t, ok)
			}
		})
	}
}

func TestLoadRegistryConfigurationFromRegistriesDirPath(t *testing.T) {
	tmpDir := t.TempDir()

	// No registries.d exists
	config, err := loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: filepath.Join(tmpDir, "thisdoesnotexist")})
	require.NoError(t, err)
	assert.Equal(t, &registryConfiguration{Docker: map[string]registryNamespace{}}, config)

	// Empty registries.d directory
	emptyDir := filepath.Join(tmpDir, "empty")
	err = os.Mkdir(emptyDir, 0o755)
	require.NoError(t, err)
	config, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: emptyDir})
	require.NoError(t, err)
	assert.Equal(t, &registryConfiguration{Docker: map[string]registryNamespace{}}, config)

	// Unreadable registries.d directory
	unreadableDir := filepath.Join(tmpDir, "unreadable")
	err = os.Mkdir(unreadableDir, 0o000)
	require.NoError(t, err)
	_, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: unreadableDir})
	assert.Error(t, err)

	// An unreadable file in a registries.d directory
	unreadableFileDir := filepath.Join(tmpDir, "unreadableFile")
	err = os.Mkdir(unreadableFileDir, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(unreadableFileDir, "0.yaml"), []byte("{}"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(unreadableFileDir, "1.yaml"), nil, 0o000)
	require.NoError(t, err)
	_, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: unreadableFileDir})
	assert.Error(t, err)

	// Invalid YAML
	invalidYAMLDir := filepath.Join(tmpDir, "invalidYAML")
	err = os.Mkdir(invalidYAMLDir, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(invalidYAMLDir, "0.yaml"), []byte("}"), 0o644)
	require.NoError(t, err)
	_, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: invalidYAMLDir})
	assert.Error(t, err)

	// Duplicate DefaultDocker
	duplicateDefault := filepath.Join(tmpDir, "duplicateDefault")
	err = os.Mkdir(duplicateDefault, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(duplicateDefault, "0.yaml"),
		[]byte("default-docker:\n lookaside: file:////tmp/something"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(duplicateDefault, "1.yaml"),
		[]byte("default-docker:\n lookaside: file:////tmp/different"), 0o644)
	require.NoError(t, err)
	_, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: duplicateDefault})
	assert.ErrorContains(t, err, "0.yaml")
	assert.ErrorContains(t, err, "1.yaml")

	// Duplicate DefaultDocker
	duplicateNS := filepath.Join(tmpDir, "duplicateNS")
	err = os.Mkdir(duplicateNS, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(duplicateNS, "0.yaml"),
		[]byte("docker:\n example.com:\n  lookaside: file:////tmp/something"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(duplicateNS, "1.yaml"),
		[]byte("docker:\n example.com:\n  lookaside: file:////tmp/different"), 0o644)
	require.NoError(t, err)
	_, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: duplicateNS})
	assert.ErrorContains(t, err, "0.yaml")
	assert.ErrorContains(t, err, "1.yaml")

	// A fully worked example, including an empty-dictionary file and a non-.yaml file
	config, err = loadRegistryConfiguration(&types.SystemContext{RegistriesDirPath: "fixtures/registries.d"})
	require.NoError(t, err)
	assert.Equal(t, &registryConfiguration{
		DefaultDocker: &registryNamespace{Lookaside: "file:///mnt/companywide/signatures/for/other/repositories"},
		Docker: map[string]registryNamespace{
			"example.com":                    {Lookaside: "https://lookaside.example.com"},
			"registry.test.example.com":      {Lookaside: "http://registry.test.example.com/lookaside"},
			"registry.test.example.com:8888": {Lookaside: "http://registry.test.example.com:8889/lookaside", LookasideStaging: "https://registry.test.example.com:8889/lookaside/specialAPIserverWhichDoesNotExist"},
			"localhost":                      {Lookaside: "file:///home/mitr/mydevelopment1"},
			"localhost:8080":                 {Lookaside: "file:///home/mitr/mydevelopment2"},
			"localhost/invalid/url/test":     {Lookaside: ":emptyscheme"},
			"localhost/file/path/test":       {Lookaside: "/no/scheme/just/a/path"},
			"localhost/relative/path/test":   {Lookaside: "no/scheme/relative/path"},
			"docker.io/contoso":              {Lookaside: "https://lookaside.contoso.com/fordocker"},
			"docker.io/centos":               {Lookaside: "https://lookaside.centos.org/"},
			"docker.io/centos/mybetaproduct": {
				Lookaside:        "http://localhost:9999/mybetaWIP/lookaside",
				LookasideStaging: "file:///srv/mybetaWIP/lookaside",
			},
			"docker.io/centos/mybetaproduct:latest": {Lookaside: "https://lookaside.centos.org/"},
		},
	}, config)
}

func TestRegistryConfigurationSignatureTopLevel(t *testing.T) {
	config := registryConfiguration{
		DefaultDocker: &registryNamespace{Lookaside: "=default", LookasideStaging: "=default+w"},
		Docker:        map[string]registryNamespace{},
	}
	for _, ns := range []string{
		"localhost",
		"localhost:5000",
		"example.com",
		"example.com/ns1",
		"example.com/ns1/ns2",
		"example.com/ns1/ns2/repo",
		"example.com/ns1/ns2/repo:notlatest",
	} {
		config.Docker[ns] = registryNamespace{Lookaside: ns, LookasideStaging: ns + "+w"}
	}

	for _, c := range []struct{ input, expected string }{
		{"example.com/ns1/ns2/repo:notlatest", "example.com/ns1/ns2/repo:notlatest"},
		{"example.com/ns1/ns2/repo:unmatched", "example.com/ns1/ns2/repo"},
		{"example.com/ns1/ns2/notrepo:notlatest", "example.com/ns1/ns2"},
		{"example.com/ns1/notns2/repo:notlatest", "example.com/ns1"},
		{"example.com/notns1/ns2/repo:notlatest", "example.com"},
		{"unknown.example.com/busybox", "=default"},
		{"localhost:5000/busybox", "localhost:5000"},
		{"localhost/busybox", "localhost"},
		{"localhost:9999/busybox", "=default"},
	} {
		dr := dockerRefFromString(t, "//"+c.input)

		res := config.signatureTopLevel(dr, false)
		assert.Equal(t, c.expected, res, c.input)
		res = config.signatureTopLevel(dr, true) // test that forWriting is correctly propagated
		assert.Equal(t, c.expected+"+w", res, c.input)
	}

	config = registryConfiguration{
		Docker: map[string]registryNamespace{
			"unmatched": {Lookaside: "a", LookasideStaging: "b"},
		},
	}
	dr := dockerRefFromString(t, "//thisisnotmatched")
	res := config.signatureTopLevel(dr, false)
	assert.Equal(t, "", res)
	res = config.signatureTopLevel(dr, true)
	assert.Equal(t, "", res)
}

func TestRegistryNamespaceSignatureTopLevel(t *testing.T) {
	for _, c := range []struct {
		ns         registryNamespace
		forWriting bool
		expected   string
	}{
		{registryNamespace{LookasideStaging: "a", Lookaside: "b"}, true, "a"},
		{registryNamespace{LookasideStaging: "a", Lookaside: "b"}, false, "b"},
		{registryNamespace{Lookaside: "b"}, true, "b"},
		{registryNamespace{Lookaside: "b"}, false, "b"},
		{registryNamespace{LookasideStaging: "a"}, true, "a"},
		{registryNamespace{LookasideStaging: "a"}, false, ""},
		{registryNamespace{}, true, ""},
		{registryNamespace{}, false, ""},

		{registryNamespace{LookasideStaging: "a", Lookaside: "b", SigStoreStaging: "c", SigStore: "d"}, true, "a"},
		{registryNamespace{Lookaside: "b", SigStoreStaging: "c", SigStore: "d"}, true, "c"},
		{registryNamespace{Lookaside: "b", SigStore: "d"}, true, "b"},
		{registryNamespace{SigStore: "d"}, true, "d"},

		{registryNamespace{LookasideStaging: "a", Lookaside: "b", SigStoreStaging: "c", SigStore: "d"}, false, "b"},
		{registryNamespace{Lookaside: "b", SigStoreStaging: "c", SigStore: "d"}, false, "b"},
		{registryNamespace{Lookaside: "b", SigStore: "d"}, false, "b"},
		{registryNamespace{SigStore: "d"}, false, "d"},
	} {
		res := c.ns.signatureTopLevel(c.forWriting)
		assert.Equal(t, c.expected, res, fmt.Sprintf("%#v %v", c.ns, c.forWriting))
	}
}

func TestLookasideStorageURL(t *testing.T) {
	const mdInput = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const mdMapped = "sha256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, c := range []struct {
		base     string
		index    int
		expected string
	}{
		{"file:///tmp", 0, "file:///tmp@" + mdMapped + "/signature-1"},
		{"file:///tmp", 1, "file:///tmp@" + mdMapped + "/signature-2"},
		{"https://localhost:5555/root", 0, "https://localhost:5555/root@" + mdMapped + "/signature-1"},
		{"https://localhost:5555/root", 1, "https://localhost:5555/root@" + mdMapped + "/signature-2"},
		{"http://localhost:5555/root", 0, "http://localhost:5555/root@" + mdMapped + "/signature-1"},
		{"http://localhost:5555/root", 1, "http://localhost:5555/root@" + mdMapped + "/signature-2"},
	} {
		baseURL, err := url.Parse(c.base)
		require.NoError(t, err)
		expectedURL, err := url.Parse(c.expected)
		require.NoError(t, err)
		res, err := lookasideStorageURL(baseURL, mdInput, c.index)
		require.NoError(t, err)
		assert.Equal(t, expectedURL, res, c.expected)
	}

	baseURL, err := url.Parse("file:///tmp")
	require.NoError(t, err)
	_, err = lookasideStorageURL(baseURL, digest.Digest("sha256:../hello"), 0)
	assert.Error(t, err)
}

func TestBuiltinDefaultLookasideStorageDir(t *testing.T) {
	base := builtinDefaultLookasideStorageDir(0)
	assert.NotNil(t, base)
	assert.Equal(t, "file://"+defaultDockerDir, base.String())

	base = builtinDefaultLookasideStorageDir(1000)
	assert.NotNil(t, base)
	assert.Equal(t, "file://"+filepath.Join(os.Getenv("HOME"), defaultUserDockerDir), base.String())
}
