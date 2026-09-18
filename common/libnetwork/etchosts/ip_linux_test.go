//go:build linux

package etchosts

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vishvananda/netlink"
)

func TestIsWSLMirroredMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     string
		err      error
		expected bool
	}{
		{
			name:     "mirrored mode lowercase",
			mode:     "mirrored",
			err:      nil,
			expected: true,
		},
		{
			name:     "mirrored mode uppercase",
			mode:     "MIRRORED",
			err:      nil,
			expected: true,
		},
		{
			name:     "mirrored mode with surrounding whitespace and newlines",
			mode:     "  mirrored \r\n",
			err:      nil,
			expected: true,
		},
		{
			name:     "nat mode",
			mode:     "nat",
			err:      nil,
			expected: false,
		},
		{
			name:     "virtioproxy mode",
			mode:     "virtioproxy",
			err:      nil,
			expected: false,
		},
		{
			name:     "invalid mode string",
			mode:     "unknown-mode",
			err:      nil,
			expected: false,
		},
		{
			name:     "empty output",
			mode:     "",
			err:      nil,
			expected: false,
		},
		{
			name:     "command error (wslinfo unavailable)",
			mode:     "",
			err:      errors.New("executable not found"),
			expected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, isWSLMirroredMode(tt.mode, tt.err))
		})
	}
}

func mockIPNet(cidr string) *net.IPNet {
	ip, ipNet, _ := net.ParseCIDR(cidr)
	ipNet.IP = ip
	return ipNet
}

func TestResolveMirroredHostIP(t *testing.T) {
	t.Parallel()

	gwIP := net.ParseIP("192.168.1.1")
	validHostIP := net.ParseIP("192.168.1.50")
	loopbackIP := net.ParseIP("127.0.0.1")
	unspecifiedIP := net.ParseIP("0.0.0.0")

	t.Run("valid route source address", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return []netlink.Route{{Src: validHostIP}}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, nil)
		assert.Equal(t, "192.168.1.50", ip)
	})

	t.Run("route source missing falls back to interface address", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return []netlink.Route{{Src: nil}}, nil
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return []net.Addr{mockIPNet("192.168.1.50/24")}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, ifaceAddrsGetter)
		assert.Equal(t, "192.168.1.50", ip)
	})

	t.Run("route source returns loopback falls back to interface address", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return []netlink.Route{{Src: loopbackIP}}, nil
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return []net.Addr{mockIPNet("192.168.1.50/24")}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, ifaceAddrsGetter)
		assert.Equal(t, "192.168.1.50", ip)
	})

	t.Run("route source returns unspecified falls back to interface address", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return []netlink.Route{{Src: unspecifiedIP}}, nil
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return []net.Addr{mockIPNet("192.168.1.50/24")}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, ifaceAddrsGetter)
		assert.Equal(t, "192.168.1.50", ip)
	})

	t.Run("route getter error falls back to interface address", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return nil, errors.New("netlink error")
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return []net.Addr{mockIPNet("192.168.1.50/24")}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, ifaceAddrsGetter)
		assert.Equal(t, "192.168.1.50", ip)
	})

	t.Run("interface only has loopback address returns empty", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return nil, errors.New("no route")
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return []net.Addr{mockIPNet("127.0.0.1/8")}, nil
		}
		ip := resolveMirroredHostIP(gwIP, 1, routeSrcGetter, ifaceAddrsGetter)
		assert.Empty(t, ip)
	})

	t.Run("nil gateway and invalid link index returns empty", func(t *testing.T) {
		t.Parallel()
		ip := resolveMirroredHostIP(nil, -1, nil, nil)
		assert.Empty(t, ip)
	})
}

func TestResolveWSLHostIP(t *testing.T) {
	t.Parallel()

	_, defaultDst, _ := net.ParseCIDR(defaultWSLRoute)
	natGateway := net.ParseIP("172.28.0.1")
	mirroredRouter := net.ParseIP("192.168.1.1")
	windowsHostIP := net.ParseIP("192.168.1.100")

	natRoutes := []netlink.Route{
		{
			Dst:       defaultDst,
			Gw:        natGateway,
			LinkIndex: 2,
		},
	}

	mirroredRoutes := []netlink.Route{
		{
			Dst:       defaultDst,
			Gw:        mirroredRouter,
			LinkIndex: 2,
		},
	}

	t.Run("preserves NAT mode gateway behavior", func(t *testing.T) {
		t.Parallel()
		ip := resolveWSLHostIP(natRoutes, false, nil, nil)
		assert.Equal(t, "172.28.0.1", ip)
	})

	t.Run("mirrored mode resolves Windows host IP", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return []netlink.Route{{Src: windowsHostIP}}, nil
		}
		ip := resolveWSLHostIP(mirroredRoutes, true, routeSrcGetter, nil)
		assert.Equal(t, "192.168.1.100", ip)
		assert.NotEqual(t, mirroredRouter.String(), ip)
	})

	t.Run("mirrored mode with unresolvable host returns empty string rather than router gateway", func(t *testing.T) {
		t.Parallel()
		routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
			return nil, errors.New("unresolvable")
		}
		ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
			return nil, errors.New("no addresses")
		}
		ip := resolveWSLHostIP(mirroredRoutes, true, routeSrcGetter, ifaceAddrsGetter)
		assert.Empty(t, ip, "in mirrored mode, failure to resolve host IP must return empty string, never the router gateway")
	})

	t.Run("empty routing table returns empty string", func(t *testing.T) {
		t.Parallel()
		ip := resolveWSLHostIP(nil, false, nil, nil)
		assert.Empty(t, ip)
	})
}

func TestWSLHostIP(t *testing.T) {
	// In an environment with a default route, wslHostIP should return a valid IP or empty string without crashing
	ip := wslHostIP()
	if ip != "" {
		parsed := net.ParseIP(ip)
		assert.NotNil(t, parsed, "returned IP should be valid")
		assert.NotNil(t, parsed.To4(), "returned IP should be IPv4")
	}
}
