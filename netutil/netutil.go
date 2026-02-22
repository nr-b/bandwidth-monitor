// Package netutil provides shared IP classification helpers used across
// multiple packages (talkers, conntrack, topology, handler).
package netutil

import "net"

// CGNAT is the RFC 6598 Carrier-Grade NAT range (100.64.0.0/10).
var CGNAT = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// IsLocal returns true if ip falls within any of the given local networks.
// If localNets is empty, falls back to heuristic: RFC1918 + link-local, excluding CGNAT.
func IsLocal(ip net.IP, localNets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if len(localNets) > 0 {
		for _, n := range localNets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if CGNAT.Contains(ip) {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// IsLocalStr is a convenience wrapper that parses the IP string first.
func IsLocalStr(ipStr string, localNets []*net.IPNet) bool {
	return IsLocal(net.ParseIP(ipStr), localNets)
}

// IsGlobalUnicast returns true if the IP is globally routable unicast.
// Returns false for private, loopback, link-local, CGNAT, and ULA addresses.
func IsGlobalUnicast(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if CGNAT.Contains(ip) {
		return false
	}
	// IPv6 ULA (fc00::/7)
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return ip.IsGlobalUnicast()
}
