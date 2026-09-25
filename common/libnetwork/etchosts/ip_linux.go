package etchosts

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const (
	defaultWSLRoute = "0.0.0.0/0"
	wslinfoTimeout  = 2 * time.Second
)

// findWSLInfo returns the path to wslinfo. It checks PATH first,
// and falls back to /bin/wslinfo (created by WSL's /init) in case
// PATH is minimal or does not include standard binary directories.
func findWSLInfo() string {
	if p, err := exec.LookPath("wslinfo"); err == nil {
		return p
	}
	if _, err := os.Stat("/bin/wslinfo"); err == nil {
		return "/bin/wslinfo"
	}
	return ""
}

// currentWSLNetworkingMode queries the active networking mode using wslinfo.
func currentWSLNetworkingMode() (string, error) {
	bin := findWSLInfo()
	if bin == "" {
		return "", os.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), wslinfoTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--networking-mode").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isMirroredMode checks if the given mode string and error indicate mirrored mode.
func isMirroredMode(mode string, err error) bool {
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(mode), "mirrored")
}

// resolveMirroredHostIP resolves the Windows host IP in WSL mirrored mode.
// In WSL mirrored networking mode, the Windows host network interfaces and addresses
// are mirrored into the Linux environment. The default route gateway points to the
// upstream LAN router rather than the Windows host.
//
// To identify the Windows host IP:
//  1. We query the Linux kernel routing table via RouteGet(defaultGw) for the
//     preferred source IP (Src) selected to reach the default gateway. In mirrored
//     mode, this source address corresponds to the local mirrored Windows host IP
//     configured on that interface.
//  2. If RouteGet source is unavailable, we inspect the default route interface
//     and fall back to a global-unicast IPv4 address (preferring one on the gateway subnet).
func resolveMirroredHostIP(
	defaultGw net.IP,
	linkIndex int,
	routeSrcGetter func(destination net.IP) ([]netlink.Route, error),
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

	// Strategy 2: Fallback to an interface address on the default route's link.
	// Prefer an address on the same subnet as the default gateway to remain route-consistent.
	if linkIndex > 0 && ifaceAddrsGetter != nil {
		addrs, err := ifaceAddrsGetter(linkIndex)
		if err == nil {
			var firstGlobalUnicast string
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && ipNet.IP.IsGlobalUnicast() {
					if len(defaultGw) > 0 && ipNet.Contains(defaultGw) {
						return ipNet.IP.String()
					}
					if firstGlobalUnicast == "" {
						firstGlobalUnicast = ipNet.IP.String()
					}
				}
			}
			if firstGlobalUnicast != "" {
				return firstGlobalUnicast
			}
		}
	}

	return ""
}

// resolveWSLRouteHostIP selects the host IP for a default route based on the WSL networking mode.
func resolveWSLRouteHostIP(
	r netlink.Route,
	mode string,
	routeSrcGetter func(destination net.IP) ([]netlink.Route, error),
	ifaceAddrsGetter func(index int) ([]net.Addr, error),
) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "nat":
		return r.Gw.String()

	case "mirrored":
		hostIP := resolveMirroredHostIP(r.Gw, r.LinkIndex, routeSrcGetter, ifaceAddrsGetter)
		if hostIP != "" {
			return hostIP
		}
		// In mirrored mode, r.Gw is the upstream router gateway, NOT the Windows host.
		// Returning r.Gw here would connect container traffic to the external network
		// and cause port conflict (WSAEADDRINUSE) on Windows. Return empty string instead.
		logrus.Warnf("Unable to determine Windows host IP in WSL mirrored mode")
		return ""

	default:
		logrus.Warnf("Unsupported WSL networking mode: %q", mode)
		return ""
	}
}

// resolveWSLHostIPWithMode encapsulates route inspection and IP selection for a specific networking mode.
func resolveWSLHostIPWithMode(
	routes []netlink.Route,
	mode string,
	routeSrcGetter func(destination net.IP) ([]netlink.Route, error),
	ifaceAddrsGetter func(index int) ([]net.Addr, error),
) string {
	for _, r := range routes {
		if (r.Dst == nil || r.Dst.String() == defaultWSLRoute) && r.Gw != nil {
			return resolveWSLRouteHostIP(r, mode, routeSrcGetter, ifaceAddrsGetter)
		}
	}
	logrus.Warnf("No default route found in the WSL machine")
	return ""
}

// resolveWSLHostIP determines the Windows host IP from routes and active WSL networking mode.
func resolveWSLHostIP(routes []netlink.Route) string {
	mode, err := currentWSLNetworkingMode()
	if err != nil {
		logrus.Warnf("Failed to get WSL networking mode: %v", err)
		return ""
	}
	return resolveWSLHostIPWithMode(routes, mode, netlink.RouteGet, defaultInterfaceAddrs)
}

func defaultInterfaceAddrs(index int) ([]net.Addr, error) {
	iface, err := net.InterfaceByIndex(index)
	if err != nil {
		return nil, err
	}
	return iface.Addrs()
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
	return resolveWSLHostIP(routes)
}
