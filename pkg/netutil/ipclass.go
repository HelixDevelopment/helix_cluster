package netutil

import "net"

// privateV4Blocks are the RFC1918 private IPv4 ranges.
var privateV4Blocks = func() []*net.IPNet {
	blocks := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	out := make([]*net.IPNet, 0, len(blocks))
	for _, b := range blocks {
		_, n, _ := net.ParseCIDR(b)
		out = append(out, n)
	}
	return out
}()

// ulaV6Block is the IPv6 Unique Local Address range (RFC4193), fc00::/7.
var ulaV6Block = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("fc00::/7")
	return n
}()

// IsPrivateIP reports whether ip is in an RFC1918 IPv4 range or the IPv6 ULA
// (fc00::/7) range. Returns false for nil or public addresses.
func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		for _, block := range privateV4Blocks {
			if block.Contains(v4) {
				return true
			}
		}
		return false
	}
	return ulaV6Block.Contains(ip)
}

// IsLoopbackIP reports whether ip is a loopback address (127.0.0.0/8 or ::1).
func IsLoopbackIP(ip net.IP) bool {
	return ip != nil && ip.IsLoopback()
}

// IsLinkLocalIP reports whether ip is link-local (169.254.0.0/16 or fe80::/10).
func IsLinkLocalIP(ip net.IP) bool {
	return ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
}

// IsIPv4 reports whether ip is an IPv4 address.
func IsIPv4(ip net.IP) bool {
	return ip != nil && ip.To4() != nil
}

// IsIPv6 reports whether ip is an IPv6 address (and not an IPv4 address in
// 16-byte form).
func IsIPv6(ip net.IP) bool {
	return ip != nil && ip.To4() == nil && ip.To16() != nil
}
