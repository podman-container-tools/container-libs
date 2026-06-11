//go:build linux

package netavark

import (
	"bytes"
	"fmt"
	"net"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.podman.io/common/libnetwork/types"
	"go.podman.io/common/pkg/config"
)

var _ = Describe("IPAM", func() {
	var (
		networkInterface *netavarkNetwork
		networkConfDir   string
		logBuffer        bytes.Buffer
	)

	BeforeEach(func() {
		t := GinkgoT()
		networkConfDir = t.TempDir()
		logBuffer = bytes.Buffer{}
		logrus.SetOutput(&logBuffer)
	})

	JustBeforeEach(func() {
		netavarkBinary := os.Getenv("NETAVARK_BINARY")
		libpodNet, err := NewNetworkInterface(&InitConfig{
			Config:           &config.Config{},
			NetworkConfigDir: networkConfDir,
			NetworkRunDir:    networkConfDir,
			NetavarkBinary:   netavarkBinary,
		})
		if err != nil {
			Fail("Failed to create NewNetworkInterface")
		}

		networkInterface = libpodNet.(*netavarkNetwork) //nolint:errcheck // It is always *netavarkNetwork here.
		// run network list to force a network load
		_, err = networkInterface.NetworkList()
		Expect(err).ToNot(HaveOccurred())
	})

	It("simple ipam alloc", func() {
		netName := types.DefaultNetworkName
		for i := 2; i < 100; i++ {
			opts := &types.NetworkOptions{
				ContainerID: "someContainerID",
				Networks: []types.NamedPerNetworkOptions{{
					Name: netName,
				}},
			}

			err := networkInterface.allocIPs(opts)
			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Networks).To(HaveLen(1))
			Expect(opts.Networks[0].Name).To(Equal(netName))
			Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
			Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.88.0.%d", i)).To4()))
		}
	})

	It("ipam try to alloc same ip", func() {
		netName := types.DefaultNetworkName
		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		err := networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.88.0.2").To4()))

		opts = &types.NetworkOptions{
			ContainerID: "otherID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
				PerNetworkOptions: types.PerNetworkOptions{
					StaticIPs: []net.IP{net.ParseIP("10.88.0.2")},
				},
			}},
		}
		err = networkInterface.allocIPs(opts)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("IPAM error: requested ip address 10.88.0.2 is already allocated to container ID someContainerID"))
	})

	It("ipam static ip matching gateway should fail", func() {
		s, _ := types.ParseCIDR("10.0.0.1/24")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		// 10.0.0.1 is the gateway for this subnet — requesting it as a static IP should fail
		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
				PerNetworkOptions: types.PerNetworkOptions{
					StaticIPs: []net.IP{net.ParseIP("10.0.0.1")},
				},
			}},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("IPAM error: address already in use: requested static IP 10.0.0.1 matches the subnet gateway"))
	})

	It("ipam try to alloc more ips as in range", func() {
		s, _ := types.ParseCIDR("10.0.0.1/24")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
						LeaseRange: &types.LeaseRange{
							StartIP: net.ParseIP("10.0.0.10"),
							EndIP:   net.ParseIP("10.0.0.20"),
						},
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		for i := 10; i < 21; i++ {
			opts := &types.NetworkOptions{
				ContainerID: fmt.Sprintf("someContainerID-%d", i),
				Networks: []types.NamedPerNetworkOptions{{
					Name: netName,
				}},
			}

			err = networkInterface.allocIPs(opts)
			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Networks).To(HaveLen(1))
			Expect(opts.Networks[0].Name).To(Equal(netName))
			Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
			Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.0.0.%d", i)).To4()))
		}

		opts := &types.NetworkOptions{
			ContainerID: "someContainerID-22",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		// now this should fail because all free ips are already assigned
		err = networkInterface.allocIPs(opts)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("IPAM error: failed to find free IP in range: 10.0.0.10 - 10.0.0.20"))
	})

	It("ipam basic setup", func() {
		netName := types.DefaultNetworkName
		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		expectedIP := net.ParseIP("10.88.0.2").To4()

		err := networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(expectedIP))

		// remove static ips from opts
		netPerNetworkOptions := opts.Networks[0].PerNetworkOptions
		netPerNetworkOptions.StaticIPs = nil
		opts.Networks[0].PerNetworkOptions = netPerNetworkOptions

		err = networkInterface.getAssignedIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(expectedIP))

		err = networkInterface.allocIPs(opts)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Equal("IPAM error: requested ip address 10.88.0.2 is already allocated to container ID someContainerID"))

		// dealloc the ip
		err = networkInterface.deallocIPs(opts)
		Expect(err).ToNot(HaveOccurred())

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(expectedIP))
	})

	It("ipam dual stack", func() {
		s1, _ := types.ParseCIDR("10.0.0.0/26")
		s2, _ := types.ParseCIDR("fd80::/24")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s1,
					},
					{
						Subnet: s2,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(2))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[0].StaticIPs[1]).To(Equal(net.ParseIP("fd80::2")))

		// remove static ips from opts
		netPerNetworkOptions := opts.Networks[0].PerNetworkOptions
		netPerNetworkOptions.StaticIPs = nil
		opts.Networks[0].PerNetworkOptions = netPerNetworkOptions

		err = networkInterface.getAssignedIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(2))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[0].StaticIPs[1]).To(Equal(net.ParseIP("fd80::2")))

		err = networkInterface.deallocIPs(opts)
		Expect(err).ToNot(HaveOccurred())

		// try to alloc the same again
		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(2))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[0].StaticIPs[1]).To(Equal(net.ParseIP("fd80::2")))
	})

	It("ipam with two networks", func() {
		s, _ := types.ParseCIDR("10.0.0.0/24")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName1 := network.Name

		s, _ = types.ParseCIDR("10.0.1.0/24")
		network, err = networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName2 := network.Name

		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{
				{
					Name: netName1,
				},
				{
					Name: netName2,
				},
			},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(2))
		Expect(opts.Networks[0].Name).To(Equal(netName1))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[1].Name).To(Equal(netName2))
		Expect(opts.Networks[1].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[1].StaticIPs[0]).To(Equal(net.ParseIP("10.0.1.2").To4()))

		// remove static ips from opts
		netPerNetworkOptions := opts.Networks[0].PerNetworkOptions
		netPerNetworkOptions.StaticIPs = nil
		opts.Networks[0].PerNetworkOptions = netPerNetworkOptions
		netPerNetworkOptions = opts.Networks[1].PerNetworkOptions
		netPerNetworkOptions.StaticIPs = nil
		opts.Networks[1].PerNetworkOptions = netPerNetworkOptions

		err = networkInterface.getAssignedIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(2))
		Expect(opts.Networks[0].Name).To(Equal(netName1))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[1].Name).To(Equal(netName2))
		Expect(opts.Networks[1].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[1].StaticIPs[0]).To(Equal(net.ParseIP("10.0.1.2").To4()))

		err = networkInterface.deallocIPs(opts)
		Expect(err).ToNot(HaveOccurred())

		// try to alloc the same again
		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(2))
		Expect(opts.Networks[0].Name).To(Equal(netName1))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))
		Expect(opts.Networks[1].Name).To(Equal(netName2))
		Expect(opts.Networks[1].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[1].StaticIPs[0]).To(Equal(net.ParseIP("10.0.1.2").To4()))
	})

	It("ipam alloc more ips as in subnet", func() {
		s, _ := types.ParseCIDR("10.0.0.0/26")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		for i := 2; i < 64; i++ {
			opts := &types.NetworkOptions{
				ContainerID: fmt.Sprintf("id-%d", i),
				Networks: []types.NamedPerNetworkOptions{{
					Name: netName,
				}},
			}
			err = networkInterface.allocIPs(opts)
			if i < 63 {
				Expect(err).ToNot(HaveOccurred())
				Expect(opts.Networks).To(HaveLen(1))
				Expect(opts.Networks[0].Name).To(Equal(netName))
				Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
				Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.0.0.%d", i)).To4()))
			} else {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("IPAM error: failed to find free IP in range: 10.0.0.1 - 10.0.0.62"))
			}
		}
	})

	It("ipam alloc -> dealloc -> alloc", func() {
		s, _ := types.ParseCIDR("10.0.0.0/27")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		for i := 2; i < 10; i++ {
			opts := types.NetworkOptions{
				ContainerID: fmt.Sprintf("id-%d", i),
				Networks: []types.NamedPerNetworkOptions{{
					Name: netName,
				}},
			}
			err = networkInterface.allocIPs(&opts)
			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Networks).To(HaveLen(1))
			Expect(opts.Networks[0].Name).To(Equal(netName))
			Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
			Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.0.0.%d", i)).To4()))

			err = networkInterface.deallocIPs(&opts)
			Expect(err).ToNot(HaveOccurred())
		}

		for i := range 30 {
			opts := types.NetworkOptions{
				ContainerID: fmt.Sprintf("id-%d", i),
				Networks: []types.NamedPerNetworkOptions{{
					Name: netName,
				}},
			}
			err = networkInterface.allocIPs(&opts)
			if i < 29 {
				Expect(err).ToNot(HaveOccurred())
				Expect(opts.Networks).To(HaveLen(1))
				Expect(opts.Networks[0].Name).To(Equal(netName))
				Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
				// The (i+8)%29+2 part looks cryptic but it is actually simple, we already have 8 ips allocated above
				// so we expect the 8 available ip. We have 29 assignable ip addresses in this subnet because "i"+8 can
				// be greater than 30 we have to modulo by 29 to go back to the beginning. Also the first free ip is
				// network address + 2, so we have to add 2 to the result
				Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.0.0.%d", (i+8)%29+2)).To4()))
			} else {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("IPAM error: failed to find free IP in range: 10.0.0.1 - 10.0.0.30"))
			}
		}
	})

	It("ipam with none driver should not set ips", func() {
		network, err := networkInterface.NetworkCreate(
			types.Network{
				IPAMOptions: map[string]string{
					"driver": types.NoneIPAMDriver,
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(BeEmpty())

		err = networkInterface.getAssignedIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(BeEmpty())

		// dealloc the ip
		err = networkInterface.deallocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
	})

	for _, leaseRange := range []types.LeaseRange{{
		StartIP: net.ParseIP("10.0.0.0"),
		EndIP:   net.ParseIP("10.0.0.63"),
	}, {
		EndIP: net.ParseIP("10.0.0.63"),
	}, {
		StartIP: net.ParseIP("10.0.0.0"),
	}} {
		lease := leaseRange
		It(fmt.Sprintf("ipam alloc with lease range as big as subnet: %v", lease), func() {
			s, _ := types.ParseCIDR("10.0.0.0/26")
			network, err := networkInterface.NetworkCreate(
				types.Network{
					Subnets: []types.Subnet{
						{
							Subnet:     s,
							LeaseRange: &lease,
						},
					},
				},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			netName := network.Name

			for i := 2; i < 64; i++ {
				opts := &types.NetworkOptions{
					ContainerID: fmt.Sprintf("id-%d", i),
					Networks: []types.NamedPerNetworkOptions{{
						Name: netName,
					}},
				}
				err = networkInterface.allocIPs(opts)
				if i < 63 {
					Expect(err).ToNot(HaveOccurred())
					Expect(opts.Networks).To(HaveLen(1))
					Expect(opts.Networks[0].Name).To(Equal(netName))
					Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
					Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP(fmt.Sprintf("10.0.0.%d", i)).To4()))
				} else {
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(Equal("IPAM error: failed to find free IP in range: 10.0.0.1 - 10.0.0.62"))
				}
			}
		})
	}

	It("ipam delete network then recreate with different LeaseRange", func() {
		s, _ := types.ParseCIDR("10.0.0.0/26")
		network, err := networkInterface.NetworkCreate(
			types.Network{
				Subnets: []types.Subnet{
					{
						Subnet: s,
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		netName := network.Name

		opts := &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.2").To4()))

		// dealloc the ip
		err = networkInterface.deallocIPs(opts)
		Expect(err).ToNot(HaveOccurred())

		// delete the network
		err = networkInterface.NetworkRemove(netName)
		Expect(err).ToNot(HaveOccurred())

		network, err = networkInterface.NetworkCreate(
			types.Network{
				Name: netName,
				Subnets: []types.Subnet{
					{
						Subnet: s,
						LeaseRange: &types.LeaseRange{
							StartIP: net.ParseIP("10.0.0.10"),
							EndIP:   net.ParseIP("10.0.0.20"),
						},
					},
				},
			},
			nil,
		)
		Expect(err).ToNot(HaveOccurred())

		opts = &types.NetworkOptions{
			ContainerID: "someContainerID",
			Networks: []types.NamedPerNetworkOptions{{
				Name: netName,
			}},
		}

		err = networkInterface.allocIPs(opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(opts.Networks).To(HaveLen(1))
		Expect(opts.Networks[0].Name).To(Equal(netName))
		Expect(opts.Networks[0].StaticIPs).To(HaveLen(1))
		Expect(opts.Networks[0].StaticIPs[0]).To(Equal(net.ParseIP("10.0.0.10").To4()))
	})

	DescribeTable("ipam alloc and dealloc multiple static IPs",
		func(subnetStrs []string, staticStrs []string, expectedTotal int) {
			subnets := make([]types.Subnet, 0, len(subnetStrs))
			for _, s := range subnetStrs {
				sn, _ := types.ParseCIDR(s)
				subnets = append(subnets, types.Subnet{Subnet: sn})
			}

			network, err := networkInterface.NetworkCreate(
				types.Network{Subnets: subnets},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			netName := network.Name

			staticIPs := make([]net.IP, 0, len(staticStrs))
			for _, ip := range staticStrs {
				staticIPs = append(staticIPs, net.ParseIP(ip))
			}

			opts := &types.NetworkOptions{
				ContainerID: "someContainerID",
				Networks: []types.NamedPerNetworkOptions{{
					Name:              netName,
					PerNetworkOptions: types.PerNetworkOptions{StaticIPs: staticIPs},
				}},
			}

			err = networkInterface.allocIPs(opts)
			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Networks).To(HaveLen(1))
			Expect(opts.Networks[0].Name).To(Equal(netName))
			Expect(opts.Networks[0].StaticIPs).To(HaveLen(expectedTotal))
			for _, ip := range staticIPs {
				Expect(opts.Networks[0].StaticIPs).To(ContainElement(WithTransform(func(i net.IP) string { return i.String() }, Equal(ip.String()))))
			}

			err = networkInterface.deallocIPs(opts)
			Expect(err).ToNot(HaveOccurred())
		},
		Entry("ipv4 single subnet",
			[]string{"10.89.0.0/16"},
			[]string{"10.89.0.5", "10.89.0.10", "10.89.0.15"},
			3,
		),
		Entry("ipv4 two subnets",
			[]string{"10.89.0.0/16", "10.90.0.0/16"},
			[]string{"10.89.0.5", "10.90.0.10", "10.89.0.15"},
			3,
		),
		Entry("ipv6 single subnet",
			[]string{"fd80::/64"},
			[]string{"fd80::5", "fd80::10", "fd80::15"},
			3,
		),
		Entry("ipv6 two subnets",
			[]string{"fd80::/64", "fd81::/64"},
			[]string{"fd80::5", "fd81::10", "fd80::15"},
			3,
		),
		Entry("mixed ipv6 static and automatic ipv4",
			[]string{"10.200.0.0/24", "fd82::/64"},
			[]string{"fd82::5", "fd82::15"},
			3,
		),
	)
})
