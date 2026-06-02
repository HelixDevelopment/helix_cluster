// Package ice implements RFC 8445 ICE candidate gathering, prioritization, and
// connectivity-check scaffolding for the HelixCluster NAT-traversal subsystem.
//
// CLAUDE-2 boundary: candidate gathering from REAL local network interfaces is
// fully exercisable in-process without any STUN or relay server. Functions that
// require a real STUN or relay server return [ErrUnsupported] instead of
// fabricating candidates — per the CLAUDE-2 "no-fake-success" mandate.
package ice

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel error (CLAUDE-2).
// ─────────────────────────────────────────────────────────────────────────────

// ErrUnsupported is returned by [GatherServerReflexive] and
// [ConnectivityCheck] to signal that the operation cannot be performed without
// a real STUN server or real network path.  It is a distinct sentinel defined
// in this package; callers MUST test with errors.Is.
var ErrUnsupported = errors.New("ice: operation unsupported in this environment (no real STUN/relay available)")

// ─────────────────────────────────────────────────────────────────────────────
// CandidateType and RFC 8445 type preference values.
// ─────────────────────────────────────────────────────────────────────────────

// CandidateType identifies an ICE candidate category as defined by RFC 8445 §5.
type CandidateType uint8

const (
	// CandidateHost is a host candidate (address is a local interface address).
	CandidateHost CandidateType = iota
	// CandidateServerReflexive is a server-reflexive candidate (address mapped
	// by a STUN server).
	CandidateServerReflexive
	// CandidatePeerReflexive is a peer-reflexive candidate (address observed in
	// a peer's STUN Binding Request during connectivity checks).
	CandidatePeerReflexive
	// CandidateRelay is a relay candidate (address allocated on a TURN relay).
	CandidateRelay
)

// String returns a human-readable label for the CandidateType.
func (ct CandidateType) String() string {
	switch ct {
	case CandidateHost:
		return "host"
	case CandidateServerReflexive:
		return "srflx"
	case CandidatePeerReflexive:
		return "prflx"
	case CandidateRelay:
		return "relay"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(ct))
	}
}

// TypePreference returns the RFC 8445 §5.1.2.1 type preference value for the
// candidate type.  The values are:
//
//	host  = 126, prflx = 110, srflx = 100, relay = 0
func (ct CandidateType) TypePreference() uint8 {
	switch ct {
	case CandidateHost:
		return 126
	case CandidatePeerReflexive:
		return 110
	case CandidateServerReflexive:
		return 100
	case CandidateRelay:
		return 0
	default:
		return 0
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Candidate.
// ─────────────────────────────────────────────────────────────────────────────

// Candidate is a single ICE candidate as defined by RFC 8445 §5.
type Candidate struct {
	// Type is the candidate category.
	Type CandidateType
	// Addr is the transport address (IP + port) of the candidate.
	Addr netip.AddrPort
	// ComponentID is the RTP/RTCP component identifier (1 for RTP, 2 for RTCP).
	ComponentID uint16
	// Priority is the RFC 8445 §5.1.2.1 candidate priority.
	Priority uint32
	// Foundation is a string identifier shared by candidates gathered from the
	// same base address and via the same gathering technique (RFC 8445 §5.1.1.3).
	Foundation string
}

// ─────────────────────────────────────────────────────────────────────────────
// Priority computation — RFC 8445 §5.1.2.1.
// ─────────────────────────────────────────────────────────────────────────────

// ComputePriority computes the RFC 8445 §5.1.2.1 candidate priority:
//
//	priority = (2^24) * typePref  +  (2^8) * localPref  +  (256 - componentID)
//
// typePref:    0–126 (candidate type preference, from [CandidateType.TypePreference])
// localPref:   0–65535 (local interface preference; 65535 = highest)
// componentID: 1–256 (1 = RTP)
func ComputePriority(typePref uint8, localPref uint16, componentID uint16) uint32 {
	return (uint32(typePref) << 24) + (uint32(localPref) << 8) + (256 - uint32(componentID))
}

// ─────────────────────────────────────────────────────────────────────────────
// Host candidate gathering from REAL local interfaces.
// ─────────────────────────────────────────────────────────────────────────────

// GatherHostCandidates enumerates REAL local network interface addresses using
// [net.InterfaceAddrs] and returns one host [Candidate] per usable unicast IP.
//
// A usable address is:
//   - a unicast IP (not multicast, not unspecified, not loopback)
//   - both IPv4 and IPv6 global-scope addresses are included
//
// Each candidate has ComponentID 1 and a Priority computed with
// [ComputePriority] using the host type preference (126) and localPref 65535.
// The Foundation is derived from the IP address string and candidate type so
// that candidates from the same interface share a stable foundation.
//
// port is embedded in the Addr field; pass 0 if the port is not yet known.
func GatherHostCandidates(port uint16) ([]Candidate, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("ice: enumerate interface addrs: %w", err)
	}

	const (
		localPref   uint16 = 65535
		componentID uint16 = 1
	)

	var candidates []Candidate
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}

		// Only unicast, non-loopback, non-unspecified, non-multicast.
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}

		// Map to a 16-byte form and derive a netip.Addr.
		var nip netip.Addr
		if ip4 := ip.To4(); ip4 != nil {
			var b [4]byte
			copy(b[:], ip4)
			nip = netip.AddrFrom4(b)
		} else if ip6 := ip.To16(); ip6 != nil {
			var b [16]byte
			copy(b[:], ip6)
			nip = netip.AddrFrom16(b)
		} else {
			continue
		}

		// Skip link-local IPv6 (fe80::/10) — typically not ICE-usable without
		// a zone ID, and zone IDs are not portable.
		if nip.IsLinkLocalUnicast() {
			continue
		}

		ap := netip.AddrPortFrom(nip, port)
		prio := ComputePriority(CandidateHost.TypePreference(), localPref, componentID)
		foundation := fmt.Sprintf("h/%s", nip.String())

		candidates = append(candidates, Candidate{
			Type:        CandidateHost,
			Addr:        ap,
			ComponentID: componentID,
			Priority:    prio,
			Foundation:  foundation,
		})
	}

	return candidates, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ErrUnsupported stubs for STUN-dependent operations (CLAUDE-2).
// ─────────────────────────────────────────────────────────────────────────────

// GatherServerReflexive returns [ErrUnsupported] unconditionally.
//
// Gathering server-reflexive candidates requires a real STUN server reachable
// over the real network. Because no such server is guaranteed to be present in
// this environment, fabricating a candidate is FORBIDDEN per CLAUDE-2. Callers
// that have a real STUN server available should implement this operation
// themselves using the nattraversal package.
//
// stunServer is accepted for API symmetry but is NOT contacted.
func GatherServerReflexive(_ interface{}, stunServer netip.AddrPort) ([]Candidate, error) {
	_ = stunServer
	return nil, ErrUnsupported
}

// ConnectivityCheck returns [ErrUnsupported] unconditionally.
//
// RFC 8445 §7 connectivity checks require a live bidirectional network path
// between peers. No such path is guaranteed to exist in this environment, so
// fabricating a successful check is FORBIDDEN per CLAUDE-2.
func ConnectivityCheck(_ interface{}, pair CandidatePair) error {
	_ = pair
	return ErrUnsupported
}

// ─────────────────────────────────────────────────────────────────────────────
// CandidatePair and pair priority.
// ─────────────────────────────────────────────────────────────────────────────

// CandidatePair groups a local and a remote [Candidate] for connectivity
// checking (RFC 8445 §6.1.2).
type CandidatePair struct {
	// Local is the local candidate contributed by this agent.
	Local Candidate
	// Remote is the remote candidate contributed by the peer.
	Remote Candidate
}

// PairPriority computes the RFC 8445 §6.1.2.3 pair priority from the
// controlling and controlled agent's individual candidate priorities:
//
//	G = controlling priority, D = controlled priority
//	pair priority = 2^32 * min(G,D) + 2 * max(G,D) + (G > D ? 1 : 0)
func PairPriority(controlling, controlled uint32) uint64 {
	var g, d uint64 = uint64(controlling), uint64(controlled)
	minVal := g
	maxVal := d
	if d < g {
		minVal = d
		maxVal = g
	}
	var tiebit uint64
	if g > d {
		tiebit = 1
	}
	return (uint64(1)<<32)*minVal + 2*maxVal + tiebit
}

// ─────────────────────────────────────────────────────────────────────────────
// Pair sorting — RFC 8445 §6.1.2.6.
// ─────────────────────────────────────────────────────────────────────────────

// SortPairs sorts pairs in-place by descending pair priority (highest first).
// localIsControlling specifies which candidate priority to pass as the
// "controlling" argument to [PairPriority].
//
// Ties are broken deterministically by Foundation strings (Local then Remote)
// and finally by address strings to ensure a stable, reproducible order.
func SortPairs(pairs []CandidatePair, localIsControlling bool) {
	sort.SliceStable(pairs, func(i, j int) bool {
		var piPrio, pjPrio uint64
		if localIsControlling {
			piPrio = PairPriority(pairs[i].Local.Priority, pairs[i].Remote.Priority)
			pjPrio = PairPriority(pairs[j].Local.Priority, pairs[j].Remote.Priority)
		} else {
			piPrio = PairPriority(pairs[i].Remote.Priority, pairs[i].Local.Priority)
			pjPrio = PairPriority(pairs[j].Remote.Priority, pairs[j].Local.Priority)
		}
		if piPrio != pjPrio {
			return piPrio > pjPrio // descending
		}
		// Deterministic tie-break: foundation strings then addr strings.
		if pairs[i].Local.Foundation != pairs[j].Local.Foundation {
			return pairs[i].Local.Foundation < pairs[j].Local.Foundation
		}
		if pairs[i].Remote.Foundation != pairs[j].Remote.Foundation {
			return pairs[i].Remote.Foundation < pairs[j].Remote.Foundation
		}
		if pairs[i].Local.Addr.String() != pairs[j].Local.Addr.String() {
			return pairs[i].Local.Addr.String() < pairs[j].Local.Addr.String()
		}
		return pairs[i].Remote.Addr.String() < pairs[j].Remote.Addr.String()
	})
}
