package nattraversal

import "net/netip"

// ────────────────────────────────────────────────────────────────────────────
// NAT-type classification (RFC 3489 §10.2 algorithmic model).
// ────────────────────────────────────────────────────────────────────────────

// NATType enumerates the RFC 3489 NAT behaviour categories that
// [ClassifyNAT] can detect from a set of [Observation] records.
type NATType uint8

const (
	// NATUnknown means insufficient observations to classify.
	NATUnknown NATType = iota
	// NATOpen means no NAT: the local address equals the mapped address.
	NATOpen
	// NATFullCone means all external endpoints see the same mapped address
	// regardless of destination.
	NATFullCone
	// NATRestrictedCone means the mapped port is stable across servers but
	// we cannot distinguish Host- from Port-Restricted with these
	// observations alone; for simplicity this value represents the
	// restricted-cone family (same mapped IP, same mapped port across all
	// observations, at least two distinct servers).
	NATRestrictedCone
	// NATPortRestricted means the mapped port differs per destination server
	// (same mapped IP, varying mapped port).
	NATPortRestricted
	// NATSymmetric means both the mapped IP and mapped port vary per
	// destination server.
	NATSymmetric
)

// String returns a human-readable label for the NATType.
func (n NATType) String() string {
	switch n {
	case NATOpen:
		return "Open"
	case NATFullCone:
		return "FullCone"
	case NATRestrictedCone:
		return "RestrictedCone"
	case NATPortRestricted:
		return "PortRestricted"
	case NATSymmetric:
		return "Symmetric"
	default:
		return "Unknown"
	}
}

// Observation captures one completed STUN Binding exchange: the STUN server
// contacted, the local socket endpoint used, and the mapped (server-reflexive)
// address the server observed.
type Observation struct {
	// Server is the STUN server that was contacted.
	Server netip.AddrPort
	// Local is the local UDP socket endpoint from which the request was sent.
	Local netip.AddrPort
	// Mapped is the server-reflexive address reported by Server.
	Mapped netip.AddrPort
}

// ClassifyNAT derives the RFC 3489-style NAT category from a slice of
// Observations. The algorithm is a pure function: it requires no I/O and is
// deterministic given the same input set.
//
// Rules (applied in order):
//
//  1. Fewer than 1 observation → NATUnknown.
//  2. Any observation where Mapped == Local (same IP + port) → NATOpen.
//  3. All mapped addresses (IP + port) are identical across all observations
//     → NATFullCone (or NATRestrictedCone for ≥ 2 distinct servers; we
//     unify them as NATFullCone when mapped IPs also match).
//     Note: to differentiate FullCone from RestrictedCone we would need
//     destination-address filtering data; without it we classify as
//     NATFullCone when both IP and port are invariant.
//  4. Mapped IPs are identical but mapped ports vary → NATPortRestricted.
//  5. Mapped IPs vary across observations → NATSymmetric.
//
// If only one observation is available, only NATOpen and NATSymmetric (via
// Local≠Mapped) can be detected; that single-observation case returns
// NATFullCone as a conservative guess (indistinguishable without a second
// server).
func ClassifyNAT(obs []Observation) NATType {
	if len(obs) == 0 {
		return NATUnknown
	}

	// Rule 2: any observation where local == mapped → open internet.
	for _, o := range obs {
		if o.Mapped == o.Local {
			return NATOpen
		}
	}

	// Gather the set of distinct mapped IPs and the set of distinct mapped
	// AddrPort values.
	mappedIPs := make(map[netip.Addr]struct{})
	mappedPorts := make(map[netip.AddrPort]struct{})
	for _, o := range obs {
		mappedIPs[o.Mapped.Addr()] = struct{}{}
		mappedPorts[o.Mapped] = struct{}{}
	}

	// Rule 5: multiple distinct IPs → symmetric.
	if len(mappedIPs) > 1 {
		return NATSymmetric
	}

	// Single distinct IP from here.

	// Rule 4: same IP but different ports → port-restricted cone.
	if len(mappedPorts) > 1 {
		return NATPortRestricted
	}

	// Single distinct IP, single distinct mapped AddrPort.
	if len(obs) >= 2 {
		// Two or more distinct servers all see the same mapped endpoint.
		return NATRestrictedCone
	}

	// Only one observation, mapped != local, can't distinguish cone types.
	return NATFullCone
}
