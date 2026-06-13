package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	selinux "github.com/opencontainers/selinux/go-selinux"
	"github.com/sirupsen/logrus"
	"go.podman.io/common/pkg/apparmor"
	"go.podman.io/common/pkg/capabilities"
	"go.podman.io/storage/pkg/configfile"
)

func testConfigPath(path string) *configfile.File {
	file := defaultConfigFileOpts()
	t := GinkgoT()
	// always force empty dirs here to never read the actual system defaults so we have a clean state.
	file.RootForImplicitAbsolutePaths = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(file.RootForImplicitAbsolutePaths, "/home/.config"))

	if path != "" {
		t.Setenv(containersConfEnv, path)
	}

	return file
}

var _ = Describe("Config", func() {
	Describe("ValidateConfig", func() {
		It("should succeed with default config", func() {
			// Given
			// When
			defaultConfig, err := newLocked(&Options{}, testConfigPath(""))

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defaultConfig.Containers.ApparmorProfile).To(gomega.Equal(apparmor.Profile))
			gomega.Expect(defaultConfig.Containers.BaseHostsFile).To(gomega.Equal(""))
			gomega.Expect(defaultConfig.Containers.InterfaceName).To(gomega.Equal(""))
			gomega.Expect(defaultConfig.Containers.PidsLimit).To(gomega.BeEquivalentTo(2048))
			gomega.Expect(defaultConfig.Containers.Privileged).To(gomega.BeFalse())
			gomega.Expect(defaultConfig.Containers.ReadOnly).To(gomega.BeFalse())
			gomega.Expect(defaultConfig.Engine.ServiceTimeout).To(gomega.BeEquivalentTo(5))
			gomega.Expect(defaultConfig.Engine.CompressionFormat).To(gomega.BeEquivalentTo("gzip"))
			gomega.Expect(defaultConfig.Engine.CompressionLevel).To(gomega.BeNil())
			gomega.Expect(defaultConfig.NetNS()).To(gomega.BeEquivalentTo("private"))
			gomega.Expect(defaultConfig.IPCNS()).To(gomega.BeEquivalentTo("shareable"))
			gomega.Expect(defaultConfig.Engine.InfraImage).To(gomega.BeEquivalentTo(""))
			gomega.Expect(defaultConfig.Engine.ImageVolumeMode).To(gomega.BeEquivalentTo("anonymous"))
			gomega.Expect(defaultConfig.Engine.SSHConfig).To(gomega.ContainSubstring("/.ssh/config"))
			gomega.Expect(defaultConfig.Engine.EventsContainerCreateInspectData).To(gomega.BeFalse())
			gomega.Expect(defaultConfig.Engine.DBBackend).To(gomega.Equal(""))
			gomega.Expect(defaultConfig.Engine.PodmanshTimeout).To(gomega.BeEquivalentTo(30))
			gomega.Expect(defaultConfig.Engine.AddCompression.Get()).To(gomega.BeEmpty())
			gomega.Expect(defaultConfig.Podmansh.Container).To(gomega.Equal("podmansh"))
			gomega.Expect(defaultConfig.Podmansh.Shell).To(gomega.Equal("/bin/sh"))
			gomega.Expect(defaultConfig.Podmansh.Timeout).To(gomega.BeEquivalentTo(0))

			path, err := defaultConfig.ImageCopyTmpDir()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(path).To(gomega.BeEquivalentTo("/var/tmp"))
			gomega.Expect(defaultConfig.Engine.Retry).To(gomega.BeEquivalentTo(3))
			gomega.Expect(defaultConfig.Engine.RetryDelay).To(gomega.Equal(""))
		})

		It("should succeed with devices", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			// Given
			defConf.Containers.Devices.Set([]string{
				"/dev/null:/dev/null:rw",
				"/dev/sdc/",
				"/dev/sdc:/dev/xvdc",
				"/dev/sdc:rm",
			})

			// When
			err = defConf.Containers.Validate()

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		It("should fail wrong max log size", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			// Given
			defConf.Containers.LogSizeMax = 1

			// When
			err = defConf.Validate()

			// Then
			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		It("should succeed with valid shm size", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			// Given
			defConf.Containers.ShmSize = "1024"

			// When
			err = defConf.Validate()

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Given
			defConf.Containers.ShmSize = "64m"

			// When
			err = defConf.Validate()

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		It("should fail wrong shm size", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			// Given
			defConf.Containers.ShmSize = "-2"

			// When
			err = defConf.Validate()

			// Then
			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		It("Check SELinux settings", func() {
			defaultConfig, _ := newLocked(&Options{}, testConfigPath(""))
			// EnableLabeling should match whether or not SELinux is enabled on the host
			gomega.Expect(defaultConfig.Containers.EnableLabeling).To(gomega.Equal(selinux.GetEnabled()))
			gomega.Expect(defaultConfig.Containers.EnableLabeledUsers).To(gomega.BeFalse())
		})

		It("Check podmansh timeout settings", func() {
			// Note: Podmansh.Timeout must be preferred over Engine.PodmanshTimeout

			// Given
			defaultConfig, _ := newLocked(&Options{}, testConfigPath(""))
			// When
			defaultConfig.Engine.PodmanshTimeout = 30
			defaultConfig.Podmansh.Timeout = 0

			// Then
			gomega.Expect(defaultConfig.PodmanshTimeout()).To(gomega.Equal(uint(30)))

			// When
			defaultConfig.Engine.PodmanshTimeout = 0
			defaultConfig.Podmansh.Timeout = 42

			// Then
			gomega.Expect(defaultConfig.PodmanshTimeout()).To(gomega.Equal(uint(42)))

			// When
			defaultConfig.Engine.PodmanshTimeout = 300
			defaultConfig.Podmansh.Timeout = 42

			// Then
			gomega.Expect(defaultConfig.PodmanshTimeout()).To(gomega.Equal(uint(42)))
		})
	})

	Describe("ValidateNetworkConfig", func() {
		It("should succeed with default config", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			// Given
			// When
			err = defConf.Network.Validate()

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})
	})

	Describe("readStorageTmp", func() {
		It("test image_copy_tmp_dir='storage'", func() {
			t := GinkgoT()
			// Reload from new configuration file
			testFile := t.TempDir() + "/temp.conf"
			content := `[engine]
image_copy_tmp_dir="storage"`
			err := os.WriteFile(testFile, []byte(content), os.ModePerm)
			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			config, _ := newLocked(&Options{}, testConfigPath(testFile))
			path, err := config.ImageCopyTmpDir()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(path).To(gomega.ContainSubstring("containers/storage/tmp"))
			// Given we do
			t.Setenv("TMPDIR", "/var/tmp/foobar")
			path, err = config.ImageCopyTmpDir()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(path).To(gomega.BeEquivalentTo("/var/tmp/foobar"))
		})
	})

	Describe("NewConfig", func() {
		It("should success with default config", func() {
			// Given
			OCIRuntimeMap := map[string][]string{
				"runc": {
					"/usr/bin/runc",
					"/usr/sbin/runc",
					"/usr/local/bin/runc",
					"/usr/local/sbin/runc",
					"/sbin/runc",
					"/bin/runc",
					"/usr/lib/cri-o-runc/sbin/runc",
					"/run/current-system/sw/bin/runc",
				},
				"crun": {
					"/usr/bin/crun",
					"/usr/sbin/crun",
					"/usr/local/bin/crun",
					"/usr/local/sbin/crun",
					"/sbin/crun",
					"/bin/crun",
					"/run/current-system/sw/bin/crun",
				},
			}

			var defCaps []string
			if runtime.GOOS == "linux" {
				defCaps = []string{
					"CAP_CHOWN",
					"CAP_DAC_OVERRIDE",
					"CAP_FOWNER",
					"CAP_FSETID",
					"CAP_KILL",
					"CAP_NET_BIND_SERVICE",
					"CAP_SETFCAP",
					"CAP_SETGID",
					"CAP_SETPCAP",
					"CAP_SETUID",
					"CAP_SYS_CHROOT",
				}
			}

			envs := []string{
				"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			}

			// Given we do
			GinkgoT().Setenv(containersConfEnv, "/dev/null")
			// When
			config, err := newLocked(&Options{}, testConfigPath(""))
			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(config.Containers.ApparmorProfile).To(gomega.Equal(apparmor.Profile))
			gomega.Expect(config.Containers.PidsLimit).To(gomega.BeEquivalentTo(2048))
			gomega.Expect(config.Containers.Env.Get()).To(gomega.BeEquivalentTo(envs))
			gomega.Expect(config.Containers.UserNS).To(gomega.BeEquivalentTo(""))
			gomega.Expect(config.Network.NetavarkPluginDirs.Get()).To(gomega.Equal(DefaultNetavarkPluginDirs))
			gomega.Expect(config.Engine.NumLocks).To(gomega.BeEquivalentTo(2048))
			gomega.Expect(config.Engine.OCIRuntimes["runc"]).To(gomega.Equal(OCIRuntimeMap["runc"]))
			gomega.Expect(config.Containers.CgroupConf.Get()).To(gomega.BeEmpty())

			caps, err := config.Capabilities("", nil, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(caps).Should(gomega.Equal(defCaps))

			if useSystemd() {
				gomega.Expect(config.Engine.CgroupManager).To(gomega.BeEquivalentTo("systemd"))
			} else {
				gomega.Expect(config.Engine.CgroupManager).To(gomega.BeEquivalentTo("cgroupfs"))
			}
			if useJournald() {
				gomega.Expect(config.Engine.EventsLogger).To(gomega.BeEquivalentTo("journald"))
				gomega.Expect(config.Containers.LogDriver).To(gomega.BeEquivalentTo("journald"))
			} else {
				gomega.Expect(config.Engine.EventsLogger).To(gomega.BeEquivalentTo("file"))
				gomega.Expect(config.Containers.LogDriver).To(gomega.BeEquivalentTo("k8s-file"))
			}
			gomega.Expect(config.Engine.EventsLogFilePath).To(gomega.BeEquivalentTo(""))
			gomega.Expect(uint64(config.Engine.EventsLogFileMaxSize)).To(gomega.Equal(DefaultEventsLogSizeMax))
			gomega.Expect(config.Engine.PodExitPolicy).To(gomega.Equal(PodExitPolicyContinue))
			gomega.Expect(config.Engine.KubeGenerateType).To(gomega.Equal("pod"))
		})

		It("should success with valid user file path", func() {
			// Given
			// When
			config, err := newLocked(&Options{}, testConfigPath("testdata/containers_default.conf"))
			// Then
			cgroupConf := []string{
				"memory.high=1073741824",
			}
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(config.Containers.ApparmorProfile).To(gomega.Equal("container-default"))
			gomega.Expect(config.Containers.PidsLimit).To(gomega.BeEquivalentTo(2048))
			gomega.Expect(config.Containers.BaseHostsFile).To(gomega.BeEquivalentTo("/etc/hosts2"))
			gomega.Expect(config.Containers.HostContainersInternalIP).To(gomega.BeEquivalentTo("1.2.3.4"))
			gomega.Expect(config.Engine.ImageVolumeMode).To(gomega.BeEquivalentTo("tmpfs"))
			gomega.Expect(config.Engine.SSHConfig).To(gomega.Equal("/foo/bar/.ssh/config"))

			gomega.Expect(config.Engine.DBBackend).To(gomega.Equal(stringSQLite))
			gomega.Expect(config.Containers.CgroupConf.Get()).To(gomega.Equal(cgroupConf))
			gomega.Expect(*config.Containers.OOMScoreAdj).To(gomega.Equal(int(750)))
			gomega.Expect(config.Engine.KubeGenerateType).To(gomega.Equal("pod"))
		})

		It("contents of passed-in file should override others", func() {
			config, err := newLocked(&Options{}, testConfigPath("testdata/containers_override.conf"))

			crunWasm := "crun-wasm"
			PlatformToOCIRuntimeMap := map[string]string{
				"hello":       "world",
				"wasi/wasm":   crunWasm,
				"wasi/wasm32": crunWasm,
				"wasi/wasm64": crunWasm,
			}

			// Also test `ImagePlatformToRuntimes
			runtimes := config.Engine.ImagePlatformToRuntime("wasi", "wasm")
			gomega.Expect(runtimes).To(gomega.Equal(crunWasm))

			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(config).ToNot(gomega.BeNil())
			gomega.Expect(config.Containers.ApparmorProfile).To(gomega.Equal("overridden-default"))
			gomega.Expect(config.Containers.LogDriver).To(gomega.Equal("journald"))
			gomega.Expect(config.Containers.LogTag).To(gomega.Equal("{{.Name}}|{{.ID}}"))
			gomega.Expect(config.Containers.LogSizeMax).To(gomega.Equal(int64(100000)))
			gomega.Expect(config.Containers.Privileged).To(gomega.BeTrue())
			gomega.Expect(config.Containers.ReadOnly).To(gomega.BeTrue())
			gomega.Expect(config.Engine.ImageParallelCopies).To(gomega.Equal(uint(10)))
			gomega.Expect(config.Engine.Platform).To(gomega.Equal("linux/amd64"))
			gomega.Expect(config.Engine.PlatformToOCIRuntime).To(gomega.Equal(PlatformToOCIRuntimeMap))
			gomega.Expect(config.Engine.ImageDefaultFormat).To(gomega.Equal("v2s2"))
			gomega.Expect(config.Engine.CompressionFormat).To(gomega.BeEquivalentTo("zstd:chunked"))
			gomega.Expect(config.Engine.EventsLogFilePath).To(gomega.BeEquivalentTo("/tmp/events.log"))
			gomega.Expect(config.Engine.EventsContainerCreateInspectData).To(gomega.BeTrue())
			path, err := config.ImageCopyTmpDir()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(path).To(gomega.BeEquivalentTo("/tmp/foobar"))
			gomega.Expect(uint64(config.Engine.EventsLogFileMaxSize)).To(gomega.Equal(uint64(500)))
			gomega.Expect(config.Engine.PodExitPolicy).To(gomega.BeEquivalentTo(PodExitPolicyStop))
		})

		It("should fail with invalid value", func() {
			// Given
			// When
			config, err := newLocked(&Options{}, testConfigPath("testdata/containers_invalid.conf"))
			// Then
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(config).To(gomega.BeNil())
		})

		It("Test Capabilities call", func() {
			// Given
			if runtime.GOOS != "linux" {
				Skip(fmt.Sprintf("capabilities not supported on %s", runtime.GOOS))
			}
			// When
			config, err := newLocked(&Options{}, testConfigPath(""))
			// Then
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			var addcaps, dropcaps []string
			caps, err := config.Capabilities("0", addcaps, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			defaultCaps := config.Containers.DefaultCapabilities.Get()
			sort.Strings(defaultCaps)
			gomega.Expect(caps).To(gomega.BeEquivalentTo(defaultCaps))

			// Add all caps
			addcaps = []string{"all"}
			caps, err = config.Capabilities("root", addcaps, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			boundingSet, err := capabilities.BoundingSet()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(caps).To(gomega.BeEquivalentTo(boundingSet))

			// Drop all caps
			dropcaps = []string{"all"}
			caps, err = config.Capabilities("", boundingSet, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			gomega.Expect(caps).ToNot(gomega.BeEquivalentTo([]string{}))

			config.Containers.DefaultCapabilities.Set([]string{
				"CAP_AUDIT_WRITE",
				"CAP_CHOWN",
				"CAP_DAC_OVERRIDE",
				"CAP_FOWNER",
			})

			expectedCaps := []string{
				"CAP_AUDIT_WRITE",
				"CAP_DAC_OVERRIDE",
				"CAP_NET_ADMIN",
				"CAP_SYS_ADMIN",
			}

			// Add all caps
			addcaps = []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}
			dropcaps = []string{"CAP_FOWNER", "CAP_CHOWN"}
			caps, err = config.Capabilities("", addcaps, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			gomega.Expect(caps).To(gomega.BeEquivalentTo(expectedCaps))

			addcaps = []string{"NET_ADMIN", "cap_sys_admin"}
			dropcaps = []string{"FOWNER", "chown"}
			caps, err = config.Capabilities("", addcaps, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			gomega.Expect(caps).To(gomega.BeEquivalentTo(expectedCaps))

			expectedCaps = []string{"CAP_NET_ADMIN", "CAP_SYS_ADMIN"}
			caps, err = config.Capabilities("notroot", addcaps, dropcaps)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sort.Strings(caps)
			gomega.Expect(caps).To(gomega.BeEquivalentTo(expectedCaps))
		})

		It("should succeed with default pull_policy", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			err = defConf.Engine.Validate()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf.Engine.PullPolicy).To(gomega.Equal("missing"))

			defConf.Engine.PullPolicy = DefaultPullPolicy
			err = defConf.Engine.Validate()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		It("should succeed case-insensitive", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			defConf.Engine.PullPolicy = "NeVer"
			err = defConf.Engine.Validate()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		It("should fail with invalid pull_policy", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			defConf.Engine.PullPolicy = "invalidPullPolicy"
			err = defConf.Engine.Validate()
			gomega.Expect(err).To(gomega.HaveOccurred())
		})

		It("should fail with invalid database_backend", func() {
			defConf, err := defaultConfig()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(defConf).NotTo(gomega.BeNil())

			defConf.Engine.DBBackend = "blah"
			err = defConf.Engine.Validate()
			gomega.Expect(err).To(gomega.HaveOccurred())
		})
	})

	Describe("Service Destinations", func() {
		BeforeEach(func() {
			t := GinkgoT()
			name := t.TempDir() + "/containersconf"
			err := os.WriteFile(name, []byte{}, os.ModePerm)
			gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
			t.Setenv(containersConfEnv, name)
		})

		It("test config errors", func() {
			content := bytes.NewBufferString("")
			logrus.SetOutput(content)
			logrus.SetLevel(logrus.DebugLevel)
			defer logrus.SetLevel(logrus.InfoLevel)
			conf, err := newLocked(&Options{}, testConfigPath("testdata/containers_broken.conf"))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(conf.Containers.NetNS).To(gomega.Equal("bridge"))
			gomega.Expect(conf.Containers.Umask).To(gomega.Equal("0002"))
			gomega.Expect(content).To(gomega.ContainSubstring("Failed to decode the keys [\\\"foo\\\" \\\"containers.image_default_transport\\\"] from \\\"testdata/containers_broken.conf\\\""))
			logrus.SetOutput(os.Stderr)
		})

		It("test default config errors", func() {
			content := bytes.NewBufferString("")
			logrus.SetOutput(content)
			logrus.SetLevel(logrus.DebugLevel)
			defer logrus.SetLevel(logrus.InfoLevel)
			_, err := newLocked(&Options{}, testConfigPath("containers.conf"))
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(content.String()).ToNot(gomega.ContainSubstring("Failed to decode the keys"))
			logrus.SetOutput(os.Stderr)
		})
	})

	Describe("Reload", func() {
		It("test new config from reload", func() {
			t := GinkgoT()
			// Default configuration
			defaultTestFile := "testdata/containers_default.conf"
			// Note this must be defined before the Setenv as cleanup happens LIFO
			// and Reload() must be called without the custom env to reset the default config
			DeferCleanup(func() {
				// Reload change back to default global configuration
				_, err := Reload()
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			})
			t.Setenv(containersConfEnv, defaultTestFile)

			cfg, err := Default()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Reload from new configuration file
			testFile := t.TempDir() + "/temp.conf"
			content := `[containers]
env=["foo=bar"]`
			err = os.WriteFile(testFile, []byte(content), os.ModePerm)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			t.Setenv(containersConfEnv, testFile)
			_, err = Reload()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			newCfg, err := Default()
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			expectOldEnv := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
			expectNewEnv := []string{"foo=bar"}
			gomega.Expect(cfg.Containers.Env.Get()).To(gomega.Equal(expectOldEnv))
			gomega.Expect(newCfg.Containers.Env.Get()).To(gomega.Equal(expectNewEnv))
		})
	})

	It("validate ImageVolumeMode", func() {
		for _, mode := range append(validImageVolumeModes, "") {
			err := ValidateImageVolumeMode(mode)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		}
		err := ValidateImageVolumeMode("bogus")
		gomega.Expect(err).To(gomega.HaveOccurred())
	})

	It("CONTAINERS_CONF_OVERRIDE", func() {
		t := GinkgoT()
		t.Setenv("CONTAINERS_CONF_OVERRIDE", "testdata/containers_override.conf")
		config, err := newLocked(&Options{}, testConfigPath(""))
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(config.Containers.ApparmorProfile).To(gomega.Equal("overridden-default"))

		// Make sure that _OVERRIDE is loaded even when CONTAINERS_CONF is set.
		t.Setenv(containersConfEnv, "testdata/containers_default.conf")
		config, err = newLocked(&Options{}, testConfigPath(""))
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(config.Containers.ApparmorProfile).To(gomega.Equal("overridden-default"))
		gomega.Expect(config.Containers.BaseHostsFile).To(gomega.Equal("/etc/hosts2"))
		gomega.Expect(config.Containers.EnableLabeledUsers).To(gomega.BeTrue())
	})

	It("ParsePullPolicy", func() {
		for _, test := range []struct {
			value  string
			policy PullPolicy
			fail   bool
		}{
			{
				value:  "always",
				policy: PullPolicyAlways,
			},
			{
				value:  "alWays",
				policy: PullPolicyAlways,
			},
			{
				value:  "ALWAYS",
				policy: PullPolicyAlways,
			},
			{
				value:  "never",
				policy: PullPolicyNever,
			},
			{
				value:  "NEVER",
				policy: PullPolicyNever,
			},
			{
				value:  "newer",
				policy: PullPolicyNewer,
			},
			{
				value:  "ifnewer",
				policy: PullPolicyNewer,
			},
			{
				value:  "NEWER",
				policy: PullPolicyNewer,
			},
			{
				value:  "",
				policy: PullPolicyMissing,
			},
			{
				value:  "missing",
				policy: PullPolicyMissing,
			},
			{
				value:  "MISSING",
				policy: PullPolicyMissing,
			},
			{
				value:  "IFMISSING",
				policy: PullPolicyMissing,
			},
			{
				value:  "ifnotpresent",
				policy: PullPolicyMissing,
			},
			{
				value: "bogus",
				fail:  true,
			},
		} {
			p, err := ParsePullPolicy(test.value)
			if test.fail {
				gomega.Expect(err.Error()).To(gomega.Equal(fmt.Sprintf("unsupported pull policy %q", test.value)))
			} else {
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(p).To(gomega.Equal(test.policy))
			}
		}
	})
})
