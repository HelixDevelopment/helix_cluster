package netutil

import (
	"fmt"
	"net"
)

// CIDR wraps a parsed *net.IPNet and provides convenience helpers for subnet
// math (network/broadcast computation, containment, bounded host enumeration).
type CIDR struct {
	// IPNet is the canonical network (host bits cleared), as returned by
	// net.ParseCIDR.
	IPNet *net.IPNet
	// IP is the original address supplied to ParseCIDR (host bits intact).
	IP net.IP
}

// ParseCIDR parses a CIDR string such as "192.168.1.10/24". Unlike the bare
// stdlib call it returns a CIDR helper bundling both the original IP and the
// canonical network.
func ParseCIDR(s string) (*CIDR, error) {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	return &CIDR{IPNet: ipnet, IP: ip}, nil
}

// Contains reports whether ip falls within the network. It returns false for a
// nil/unparseable ip.
func (c *CIDR) Contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return c.IPNet.Contains(ip)
}

// Network returns the network (base) address with host bits cleared.
func (c *CIDR) Network() net.IP {
	return c.IPNet.IP
}

// Broadcast returns the broadcast (all-host-bits-set) address. For IPv6 there
// is no true broadcast, but the all-ones host address is still well defined and
// useful as the upper bound of the range.
func (c *CIDR) Broadcast() net.IP {
	ipnet := c.IPNet
	ip := ipnet.IP
	mask := ipnet.Mask
	// Normalise to the natural width: use 4-byte form for IPv4 networks.
	if v4 := ip.To4(); v4 != nil && len(mask) == net.IPv4len {
		ip = v4
	}
	out := make(net.IP, len(ip))
	for i := range ip {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}

// NextIP returns the IP numerically following ip, without mutating ip. It
// returns a copy in the same width as the input. On overflow (all bits set) it
// wraps to all-zeros.
func NextIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			// no carry
			return out
		}
		// carry into next more-significant byte
	}
	// overflowed → all zeros (already the case)
	return out
}

// Hosts enumerates the usable host addresses within the network (excluding the
// network and broadcast addresses for IPv4 nets wider than /31). The result is
// bounded: if the number of usable hosts would exceed limit, an error is
// returned and no slice is produced, protecting callers from enumerating huge
// ranges.
func (c *CIDR) Hosts(limit int) ([]net.IP, error) {
	ones, bits := c.IPNet.Mask.Size()
	hostBits := bits - ones

	// /31 and /32 (and /127, /128) have no "usable" hosts in the classic model.
	if hostBits <= 1 {
		return []net.IP{}, nil
	}

	// Total addresses = 2^hostBits; usable = that minus network + broadcast.
	// Guard against overflow / oversized ranges before allocating.
	if hostBits >= 31 { // 2^31 hosts is already far beyond any sane limit
		return nil, fmt.Errorf("netutil: /%d has too many hosts to enumerate (limit %d)", ones, limit)
	}
	usable := (1 << uint(hostBits)) - 2
	if usable > limit {
		return nil, fmt.Errorf("netutil: %d hosts exceeds limit %d", usable, limit)
	}

	out := make([]net.IP, 0, usable)
	// Start one past the network address.
	ip := NextIP(c.Network())
	broadcast := c.Broadcast()
	for !ip.Equal(broadcast) {
		// store a copy
		cp := make(net.IP, len(ip))
		copy(cp, ip)
		out = append(out, cp)
		ip = NextIP(ip)
	}
	return out, nil
}
