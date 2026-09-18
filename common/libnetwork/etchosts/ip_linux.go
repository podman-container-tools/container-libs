package etchosts

import (
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const defaultWSLRoute = "0.0.0.0/0"

// findWSLInfo returns the path to wslinfo. It checks PATH first,
// and falls back to /usr/sbin/wslinfo (the standard symlink to /init in WSL)
// in case /usr/sbin is not present in non-root or minimal PATH environments.
func findWSLInfo() string {
	if p, err := exec.LookPath("wslinfo"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/sbin/wslinfo"); err == nil {
		return "/usr/sbin/wslinfo"
	}
	return ""
}

// defaultWSLNetworkingMode queries the active networking mode using wslinfo.
func defaultWSLNetworkingMode() (string, error) {
	bin := findWSLInfo()
	if bin == "" {
		return "", os.ErrNotExist
	}
	out, err := exec.Command(bin, "--networking-mode").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isWSLMirroredMode parses mode output from wslinfo and checks if it equals "mirrored".
func isWSLMirroredMode(mode string, err error) bool {
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(mode), "mirrored")
}

// resolveMirroredHostIP resolves the Windows host IP in WSL mirrored mode.
// In mirrored mode, Windows network interfaces are mirrored into Linux.
// Rather than using the default route gateway (which points to the upstream LAN router),
// we resolve the local host IP associated with the default route interface.
//
// routeSrcGetter retrieves the route source for defaultGw (defaults to netlink.RouteGet).
// ifaceAddrsGetter retrieves addresses on linkIndex (defaults to net.InterfaceByIndex).
func resolveMirroredHostIP(
	defaultGw net.IP,
	linkIndex int,
	routeSrcGetter func(gw net.IP) ([]netlink.Route, error),
	ifaceAddrsGetter func(index int) ([]net.Addr, error),
) string {
	// Strategy 1: Ask the kernel routing table for the preferred source IP
	// used to reach the default gateway.
	if len(defaultGw) > 0 && routeSrcGetter != nil {
		routes, err := routeSrcGetter(defaultGw)
		if err == nil && len(routes) > 0 && routes[0].Src != nil {
			src := routes[0].Src
			if !src.IsLoopback() && !src.IsUnspecified() && src.To4() != nil && src.IsGlobalUnicast() {
				return src.String()
			}
		}
	}

	// Strategy 2: Fallback to the first global unicast IPv4 address assigned to the
	// interface of the default route.
	if linkIndex > 0 && ifaceAddrsGetter != nil {
		addrs, err := ifaceAddrsGetter(linkIndex)
		if err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsGlobalUnicast() {
					return ipNet.IP.String()
				}
			}
		}
	}

	return ""
}

// resolveWSLHostIP encapsulates the IP selection logic for WSL.
func resolveWSLHostIP(
	routes []netlink.Route,
	isMirrored bool,
	routeSrcGetter func(gw net.IP) ([]netlink.Route, error),
	ifaceAddrsGetter func(index int) ([]net.Addr, error),
) string {
	for _, r := range routes {
		if (r.Dst == nil || r.Dst.String() == defaultWSLRoute) && r.Gw != nil {
			if isMirrored {
				hostIP := resolveMirroredHostIP(r.Gw, r.LinkIndex, routeSrcGetter, ifaceAddrsGetter)
				if hostIP != "" {
					return hostIP
				}
				// In mirrored mode, r.Gw is the upstream router gateway, NOT the Windows host.
				// Returning r.Gw here would connect container traffic to the external network
				// and cause port conflict (WSAEADDRINUSE) on Windows. Return empty string instead.
				logrus.Warnf("Unable to determine Windows host IP in WSL mirrored mode")
				return ""
			}
			// In WSL NAT mode, the default route gateway represents the Windows host.
			return r.Gw.String()
		}
	}
	logrus.Warnf("No default route found in the WSL machine")
	return ""
}

// wslHostIP returns the Windows host's IP address. It only makes
// sense to execute it when running in a WSL distribution.
//
// In WSL NAT mode, instructions to retrieve the IP address are section "Identify IP address"
// (scenario 2) of the WSL networking documentation:
// https://learn.microsoft.com/en-us/windows/wsl/networking#identify-ip-address
// where the default route gateway represents the Windows host.
//
// In WSL mirrored mode, the Windows host's network interfaces are mirrored into Linux,
// and the default route gateway points to the upstream LAN router instead of Windows.
// In that mode, we determine the Windows host IP from the mirrored interface.
func wslHostIP() string {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		logrus.Warnf("Failed getting routes in the WSL machine: %v", err)
		return ""
	}

	mode, modeErr := defaultWSLNetworkingMode()
	isMirrored := isWSLMirroredMode(mode, modeErr)

	routeSrcGetter := func(gw net.IP) ([]netlink.Route, error) {
		return netlink.RouteGet(gw)
	}
	ifaceAddrsGetter := func(index int) ([]net.Addr, error) {
		iface, err := net.InterfaceByIndex(index)
		if err != nil {
			return nil, err
		}
		return iface.Addrs()
	}

	return resolveWSLHostIP(routes, isMirrored, routeSrcGetter, ifaceAddrsGetter)
}
