package config

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.podman.io/storage/pkg/configfile"
)

func testSetModulePaths() *configfile.File {
	wd, err := os.Getwd()
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	file := defaultConfigFileOpts()
	file.UserId = 1000
	file.RootForImplicitAbsolutePaths = filepath.Join(wd, "testdata/modules")
	GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(file.RootForImplicitAbsolutePaths, "/home/.config"))

	return file
}

var _ = Describe("Config Modules", func() {
	It("new config with modules", func() {
		file := testSetModulePaths()

		wd, err := os.Getwd()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		options := &Options{Modules: []string{"none.conf"}}
		_, err = newLocked(options, file)
		gomega.Expect(err).To(gomega.HaveOccurred()) // must error out

		options = &Options{}
		c, err := newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c).NotTo(gomega.BeNil())
		gomega.Expect(c.LoadedModules()).To(gomega.BeEmpty()) // no module is getting loaded!

		options = &Options{Modules: []string{"fourth.conf"}}
		c, err = newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.Containers.InitPath).To(gomega.Equal("etc four"))
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(1)) // 1 module is getting loaded!
		// Make sure the returned module path is absolute.
		gomega.Expect(c.LoadedModules()).To(gomega.Equal([]string{filepath.Join(wd, "testdata/modules/etc/containers/containers.conf.modules/fourth.conf")}))

		options = &Options{Modules: []string{"fourth.conf"}}
		c, err = newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.Containers.InitPath).To(gomega.Equal("etc four"))
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(1)) // 1 module is getting loaded!

		options = &Options{Modules: []string{"fourth.conf", "sub/share-only.conf", "sub/etc-only.conf"}}
		c, err = newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.Containers.InitPath).To(gomega.Equal("etc four"))
		gomega.Expect(c.Containers.Env.Get()).To(gomega.Equal([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "usr share only"}))
		gomega.Expect(c.Containers.EnvMerge.Get()).To(gomega.Equal([]string{
			"PATH=/base/bin:${PATH}",
			"PATH=/usr/local/share/bin:${PATH}",
			"etc only env_merge conf",
		}))
		gomega.Expect(c.Network.DefaultNetwork).To(gomega.Equal("etc only conf"))
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(3)) // 3 modules are getting loaded!

		options = &Options{Modules: []string{"third.conf"}}
		c, err = newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(1)) // 1 module is getting loaded!
		gomega.Expect(c.Network.DefaultNetwork).To(gomega.Equal("home third"))

		// Even when running as root we now (Podman 6 config rewrite) lookup in home for modules.
		file.UserId = 0
		c, err = newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(1)) // 1 module is getting loaded!
		gomega.Expect(c.Network.DefaultNetwork).To(gomega.Equal("home third"))
	})

	It("new config with modules and env variables", func() {
		file := testSetModulePaths()

		t := GinkgoT()
		t.Setenv(containersConfOverrideEnv, "testdata/modules/override.conf")

		// Also make sure that absolute paths are loaded as is.
		wd, err := os.Getwd()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		absConf := filepath.Join(wd, "testdata/modules/home/.config/containers/containers.conf.modules/second.conf")

		options := &Options{Modules: []string{"fourth.conf", "sub/share-only.conf", absConf}}
		c, err := newLocked(options, file)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(c.LoadedModules()).To(gomega.HaveLen(3)) // 2 modules + abs path
		gomega.Expect(c.Containers.InitPath).To(gomega.Equal("etc four"))
		gomega.Expect(c.Containers.Env.Get()).To(gomega.Equal([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "usr share only", "override conf always wins"}))
		gomega.Expect(c.Containers.Volumes.Get()).To(gomega.Equal([]string{"volume four", "home second"}))
	})

	It("new config with modules and env variables", func() {
		paths, err := ModuleDirectories()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(paths).To(gomega.HaveLen(3))
		for _, path := range paths {
			gomega.Expect(path).To(gomega.HaveSuffix("containers.conf.modules"))
		}
	})
})
