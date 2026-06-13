package ice_test

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/ice"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestComputePriority — RFC 8445 §5.1.2.1 exact integer formula.
// ─────────────────────────────────────────────────────────────────────────────

// TestComputePriority verifies the RFC 8445 priority formula against
// independently-computed expected values.
//
// Anti-bluff mutation: drop the <<24 term in ComputePriority (set typePref
// contribution to 0).  The independently-computed want values all rely on
// (2^24)*typePref being non-zero, so the assertions fail.  The mutated code
// still compiles.
func TestComputePriority(t *testing.T) {
	t.Parallel()

	type tc struct {
		name        string
		typePref    uint8
		localPref   uint16
		componentID uint16
		want        uint32
	}

	// Each want is computed from the formula independently:
	//   (1<<24)*typePref + (1<<8)*localPref + (256 - componentID)
	tests := []tc{
		{
			// host, max localPref, component 1
			// (1<<24)*126 + (1<<8)*65535 + 255
			// = 2113929216 + 16776960 + 255 = 2130706431
			name:        "host/maxLocalPref/component1",
			typePref:    126,
			localPref:   65535,
			componentID: 1,
			want:        2130706431,
		},
		{
			// srflx, localPref=0, component 1
			// (1<<24)*100 + 0 + 255
			// = 1677721600 + 255 = 1677721855
			name:        "srflx/zeroLocalPref/component1",
			typePref:    100,
			localPref:   0,
			componentID: 1,
			want:        1677721855,
		},
		{
			// prflx, localPref=1, component 2
			// (1<<24)*110 + (1<<8)*1 + 254
			// = 1845493760 + 256 + 254 = 1845494270
			name:        "prflx/localPref1/component2",
			typePref:    110,
			localPref:   1,
			componentID: 2,
			want:        1845494270,
		},
		{
			// relay, localPref=65535, component 1
			// (1<<24)*0 + (1<<8)*65535 + 255
			// = 0 + 16776960 + 255 = 16777215
			name:        "relay/maxLocalPref/component1",
			typePref:    0,
			localPref:   65535,
			componentID: 1,
			want:        16777215,
		},
		{
			// host, localPref=1000, component 1
			// (1<<24)*126 + (1<<8)*1000 + 255
			// = 2113929216 + 256000 + 255 = 2114185471
			name:        "host/localPref1000/component1",
			typePref:    126,
			localPref:   1000,
			componentID: 1,
			want:        2114185471,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ice.ComputePriority(tt.typePref, tt.localPref, tt.componentID)
			if got != tt.want {
				t.Errorf("ComputePriority(%d, %d, %d) = %d; want %d",
					tt.typePref, tt.localPref, tt.componentID, got, tt.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPairPriority — RFC 8445 §6.1.2.3 exact formula.
// ─────────────────────────────────────────────────────────────────────────────

// TestPairPriority verifies the pair-priority formula for several (G, D) pairs,
// including the tie-bit when G > D and when G < D.
//
// Anti-bluff mutation: drop the "+tiebit" ("+1") term in PairPriority.
// The test case with g>d has want that includes +1; removing it makes the
// assertion fail.  The mutated code still compiles.
func TestPairPriority(t *testing.T) {
	t.Parallel()

	type tc struct {
		name        string
		controlling uint32
		controlled  uint32
		want        uint64
	}

	tests := []tc{
		{
			// G=100, D=50: min=50, max=100, G>D → tiebit=1
			// 2^32 * 50 + 2*100 + 1 = 214748364800 + 200 + 1 = 214748365001
			name:        "G>D tiebit=1",
			controlling: 100,
			controlled:  50,
			want:        214748365001,
		},
		{
			// G=50, D=100: min=50, max=100, G<D → tiebit=0
			// 2^32 * 50 + 2*100 + 0 = 214748364800 + 200 = 214748365000
			name:        "G<D tiebit=0",
			controlling: 50,
			controlled:  100,
			want:        214748365000,
		},
		{
			// G=D=1000: min=1000, max=1000, G==D → tiebit=0
			// 2^32 * 1000 + 2*1000 + 0
			// = 4294967296 * 1000 + 2000 + 0 = 4294967298000
			name:        "G==D tiebit=0",
			controlling: 1000,
			controlled:  1000,
			want:        4294967298000,
		},
		{
			// Host priority at max localPref: 2130706431 (computed above).
			// G=2130706431, D=2130706430 → G>D → tiebit=1
			// min=2130706430, max=2130706431
			// 2^32*2130706430 + 2*2130706431 + 1
			// = 9151314434226913280 + 4261412862 + 1
			// = 9151314438488326143
			name:        "host-max vs host-max-1 tiebit=1",
			controlling: 2130706431,
			controlled:  2130706430,
			want:        9151314438488326143,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ice.PairPriority(tt.controlling, tt.controlled)
			if got != tt.want {
				t.Errorf("PairPriority(%d, %d) = %d; want %d",
					tt.controlling, tt.controlled, got, tt.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestSortPairs — strict descending-priority order.
// ─────────────────────────────────────────────────────────────────────────────

// TestSortPairs builds a hand-crafted set of CandidatePairs with known
// priorities and asserts the exact resulting order after SortPairs.
//
// Anti-bluff mutation: reverse the comparator in SortPairs (return piPrio <
// pjPrio).  The expected order inverts, so the first assertion fails.  The
// mutated code still compiles.
func TestSortPairs(t *testing.T) {
	t.Parallel()

	// Build three pairs with distinct, known priorities.
	// localIsControlling = true → PairPriority(local.Priority, remote.Priority)
	//
	// Pair A: local=300, remote=200 → G=300, D=200 → min=200, max=300, G>D tiebit=1
	//   pA = 2^32*200 + 2*300 + 1 = 858993459200 + 600 + 1 = 858993459801
	//
	// Pair B: local=100, remote=400 → G=100, D=400 → min=100, max=400, G<D tiebit=0
	//   pB = 2^32*100 + 2*400 + 0 = 429496729600 + 800 = 429496730400
	//
	// Pair C: local=500, remote=500 → G=500, D=500 → tiebit=0
	//   pC = 2^32*500 + 2*500 + 0 = 2147483648000 + 1000 = 2147483649000
	//
	// Descending order: C (highest) > A > B
	//   want: [C, A, B]

	addrA := netip.MustParseAddrPort("10.0.0.1:1234")
	addrB := netip.MustParseAddrPort("10.0.0.2:1234")
	addrC := netip.MustParseAddrPort("10.0.0.3:1234")
	addrR := netip.MustParseAddrPort("192.168.1.1:5678")

	pairA := ice.CandidatePair{
		Local:  ice.Candidate{Addr: addrA, Priority: 300, Foundation: "A"},
		Remote: ice.Candidate{Addr: addrR, Priority: 200, Foundation: "R"},
	}
	pairB := ice.CandidatePair{
		Local:  ice.Candidate{Addr: addrB, Priority: 100, Foundation: "B"},
		Remote: ice.Candidate{Addr: addrR, Priority: 400, Foundation: "R"},
	}
	pairC := ice.CandidatePair{
		Local:  ice.Candidate{Addr: addrC, Priority: 500, Foundation: "C"},
		Remote: ice.Candidate{Addr: addrR, Priority: 500, Foundation: "R"},
	}

	pairs := []ice.CandidatePair{pairA, pairB, pairC}
	ice.SortPairs(pairs, true)

	wantOrder := []ice.CandidatePair{pairC, pairA, pairB}
	for i, want := range wantOrder {
		if pairs[i].Local.Foundation != want.Local.Foundation {
			t.Errorf("pairs[%d].Local.Foundation = %q; want %q", i, pairs[i].Local.Foundation, want.Local.Foundation)
		}
	}

	// Also verify the actual pair-priority values are in non-increasing order.
	for i := 1; i < len(pairs); i++ {
		pi := ice.PairPriority(pairs[i-1].Local.Priority, pairs[i-1].Remote.Priority)
		pj := ice.PairPriority(pairs[i].Local.Priority, pairs[i].Remote.Priority)
		if pi < pj {
			t.Errorf("pairs[%d] has lower priority (%d) than pairs[%d] (%d); want non-increasing", i-1, pi, i, pj)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestGatherHostCandidates — oracle-driven, real interface enumeration.
// ─────────────────────────────────────────────────────────────────────────────

// buildOracleAddrs enumerates usable unicast IPs via net.Interfaces (walking
// interface unicast addrs directly) rather than net.InterfaceAddrs, giving an
// independent traversal path from GatherHostCandidates (which calls
// net.InterfaceAddrs).  The filtering predicate is derived from first
// principles (RFC 8445 §4.1.1 host candidate definition), NOT copied from
// ice.go:
//
//   - Skip loopback and link-local (127/8, ::1, fe80::/10): never ICE-usable.
//   - Skip unspecified (0.0.0.0, ::): not a real interface address.
//   - Skip multicast: not a unicast host address.
//
// Any bug in GatherHostCandidates that causes it to include or exclude a
// different set of addresses will surface as a mismatch between this oracle
// and the production output.
func buildOracleAddrs(t *testing.T) map[netip.Addr]struct{} {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("oracle: net.Interfaces: %v", err)
	}
	result := make(map[netip.Addr]struct{})
	for _, iface := range ifaces {
		ifAddrs, err := iface.Addrs()
		if err != nil {
			// Non-fatal: a single iface failure should not abort the oracle.
			continue
		}
		for _, a := range ifAddrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			if ip == nil {
				continue
			}

			// RFC 8445 §4.1.1: only globally-routable unicast addresses are
			// valid host candidates.  Reject: loopback, unspecified, multicast.
			if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}

			// Normalise to netip.Addr so we can test IsLinkLocalUnicast cleanly.
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

			// RFC 8445: link-local IPv6 (fe80::/10) requires a zone ID and is
			// not portable across peers — exclude.
			if nip.IsLinkLocalUnicast() {
				continue
			}

			result[nip] = struct{}{}
		}
	}
	return result
}

// TestGatherHostCandidates verifies that every returned candidate's IP is in
// the oracle set, that the count matches the oracle exactly, and that every
// candidate has ComponentID==1 and the exact RFC 8445 priority.
//
// The oracle uses net.Interfaces (not net.InterfaceAddrs) so the traversal
// path is independent from the production code.  The filtering predicates are
// derived from RFC 8445 first principles rather than copied from ice.go,
// ensuring filter bugs in production are not silently mirrored.
//
// Independent negative assertions (no loopback, no link-local) below catch
// filter regressions that would be invisible to a count-only check.
//
// Anti-bluff mutation: skip every other address in GatherHostCandidates
// (e.g., add "if i%2==0 { continue }").  The returned count differs from the
// oracle count, causing the count assertion to fail.  The mutated code still
// compiles.
func TestGatherHostCandidates(t *testing.T) {
	t.Parallel()

	oracle := buildOracleAddrs(t)
	candidates, err := ice.GatherHostCandidates(0)
	if err != nil {
		t.Fatalf("GatherHostCandidates: unexpected error: %v", err)
	}

	// Count must match oracle exactly.
	if len(candidates) != len(oracle) {
		t.Errorf("GatherHostCandidates returned %d candidates; oracle has %d usable IPs",
			len(candidates), len(oracle))
	}

	for _, c := range candidates {
		ip := c.Addr.Addr()

		// ── Independent negative assertions (MEDIUM fix) ──────────────────────
		// These checks are genuinely independent of the oracle: they test
		// specific filter properties that would be silently mirrored if we only
		// compared against a tautological oracle built from the same code path.
		//
		// Anti-bluff mutation for the loopback check: remove the IsLoopback guard
		// in GatherHostCandidates.  127.0.0.1 would appear in output; this
		// assertion catches it even when the count matches (the oracle
		// independently excludes loopback, so count would also diverge, but the
		// negative check provides an earlier and more specific failure message).
		if ip.IsLoopback() {
			t.Errorf("candidate IP %v is a loopback address; must be excluded per RFC 8445", ip)
		}

		// Anti-bluff mutation: remove the IsLinkLocalUnicast guard in
		// GatherHostCandidates.  fe80:: addresses would appear; caught here
		// independently of the oracle count.
		if ip.IsLinkLocalUnicast() {
			t.Errorf("candidate IP %v is link-local unicast; must be excluded per RFC 8445", ip)
		}

		// Every returned IP must be in the oracle (cross-source membership check).
		if _, ok := oracle[ip]; !ok {
			t.Errorf("candidate IP %v not found in independent oracle set", ip)
		}

		// ComponentID must be 1.
		if c.ComponentID != 1 {
			t.Errorf("candidate %v: ComponentID = %d; want 1", ip, c.ComponentID)
		}

		// Priority must be non-zero.
		if c.Priority == 0 {
			t.Errorf("candidate %v: Priority is 0; want non-zero", ip)
		}

		// Priority must equal ComputePriority(host.TypePref, 65535, 1).
		wantPrio := ice.ComputePriority(ice.CandidateHost.TypePreference(), 65535, 1)
		if c.Priority != wantPrio {
			t.Errorf("candidate %v: Priority = %d; want %d", ip, c.Priority, wantPrio)
		}

		// Type must be CandidateHost.
		if c.Type != ice.CandidateHost {
			t.Errorf("candidate %v: Type = %v; want host", ip, c.Type)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestGatherServerReflexive — must return ErrUnsupported (CLAUDE-2 gate).
// ─────────────────────────────────────────────────────────────────────────────

// TestGatherServerReflexive asserts that GatherServerReflexive returns an error
// that wraps ErrUnsupported and does NOT return a non-nil Candidate slice.
//
// Anti-bluff mutation: fabricate a srflx candidate and return nil error.
// errors.Is(err, ErrUnsupported) becomes false (err==nil), so the t.Error
// assertion fires.  The mutated code still compiles.
func TestGatherServerReflexive(t *testing.T) {
	t.Parallel()

	stunServer := netip.MustParseAddrPort("1.2.3.4:3478")
	candidates, err := ice.GatherServerReflexive(nil, stunServer)

	if !errors.Is(err, ice.ErrUnsupported) {
		t.Errorf("GatherServerReflexive: err = %v; want errors.Is(err, ErrUnsupported)==true", err)
	}
	if len(candidates) != 0 {
		t.Errorf("GatherServerReflexive: returned %d candidates; want 0 (must not fabricate)", len(candidates))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestConnectivityCheck — must return ErrUnsupported (CLAUDE-2 gate).
// ─────────────────────────────────────────────────────────────────────────────

// TestConnectivityCheck asserts that ConnectivityCheck returns an error that
// wraps ErrUnsupported.
//
// Anti-bluff mutation: return nil from ConnectivityCheck.
// errors.Is(nil, ErrUnsupported) is false, so the assertion fires.  The
// mutated code still compiles.
func TestConnectivityCheck(t *testing.T) {
	t.Parallel()

	pair := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.1:9000"), Priority: 100},
		Remote: ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.2:9000"), Priority: 200},
	}
	err := ice.ConnectivityCheck(nil, pair)

	if !errors.Is(err, ice.ErrUnsupported) {
		t.Errorf("ConnectivityCheck: err = %v; want errors.Is(err, ErrUnsupported)==true", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestCandidateTypeString — sanity-check String() coverage.
// ─────────────────────────────────────────────────────────────────────────────

func TestCandidateTypeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ct   ice.CandidateType
		want string
	}{
		{ice.CandidateHost, "host"},
		{ice.CandidateServerReflexive, "srflx"},
		{ice.CandidatePeerReflexive, "prflx"},
		{ice.CandidateRelay, "relay"},
	}
	for _, tt := range tests {
		if got := tt.ct.String(); got != tt.want {
			t.Errorf("CandidateType(%d).String() = %q; want %q", tt.ct, got, tt.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestCandidateTypeTypePreference — verify RFC 8445 type-preference constants.
// ─────────────────────────────────────────────────────────────────────────────

func TestCandidateTypeTypePreference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ct   ice.CandidateType
		want uint8
	}{
		{ice.CandidateHost, 126},
		{ice.CandidateServerReflexive, 100},
		{ice.CandidatePeerReflexive, 110},
		{ice.CandidateRelay, 0},
	}
	for _, tt := range tests {
		if got := tt.ct.TypePreference(); got != tt.want {
			t.Errorf("CandidateType(%v).TypePreference() = %d; want %d", tt.ct, got, tt.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestSortPairsDeterminism — tie-breaking produces a stable, deterministic order.
// ─────────────────────────────────────────────────────────────────────────────

// TestSortPairsDeterminism ensures that pairs with equal pair-priority are
// ordered deterministically by foundation/addr tie-break.
func TestSortPairsDeterminism(t *testing.T) {
	t.Parallel()

	// Two pairs with identical priorities, different foundations.
	pairX := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.1:1"), Priority: 100, Foundation: "X"},
		Remote: ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.9:1"), Priority: 100, Foundation: "RX"},
	}
	pairY := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.2:1"), Priority: 100, Foundation: "Y"},
		Remote: ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.9:1"), Priority: 100, Foundation: "RX"},
	}

	// Run sort twice; both orderings must produce the same result.
	run1 := []ice.CandidatePair{pairX, pairY}
	run2 := []ice.CandidatePair{pairY, pairX}
	ice.SortPairs(run1, true)
	ice.SortPairs(run2, true)

	if run1[0].Local.Foundation != run2[0].Local.Foundation ||
		run1[1].Local.Foundation != run2[1].Local.Foundation {
		t.Errorf("SortPairs is not deterministic: run1=%v, run2=%v",
			[]string{run1[0].Local.Foundation, run1[1].Local.Foundation},
			[]string{run2[0].Local.Foundation, run2[1].Local.Foundation})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestGatherHostCandidatesNoDuplicates — no two candidates share the same IP.
// ─────────────────────────────────────────────────────────────────────────────

func TestGatherHostCandidatesNoDuplicates(t *testing.T) {
	t.Parallel()

	candidates, err := ice.GatherHostCandidates(4567)
	if err != nil {
		t.Fatalf("GatherHostCandidates: %v", err)
	}

	seen := make(map[netip.Addr]bool)
	for _, c := range candidates {
		ip := c.Addr.Addr()
		if seen[ip] {
			t.Errorf("duplicate candidate for IP %v", ip)
		}
		seen[ip] = true
		// port must be what we passed
		if c.Addr.Port() != 4567 {
			t.Errorf("candidate %v: Port = %d; want 4567", ip, c.Addr.Port())
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TestPairPrioritySymmetry — swapping G and D changes only the tie bit.
// ─────────────────────────────────────────────────────────────────────────────

// TestPairPrioritySymmetry checks the RFC 8445 symmetry property: when G≠D,
// PairPriority(G,D) and PairPriority(D,G) differ by exactly 1 (the tie bit).
func TestPairPrioritySymmetry(t *testing.T) {
	t.Parallel()

	type tc struct {
		g, d uint32
	}
	cases := []tc{
		{100, 200},
		{500, 499},
		{2130706431, 1677721855},
	}
	for _, c := range cases {
		p1 := ice.PairPriority(c.g, c.d)
		p2 := ice.PairPriority(c.d, c.g)
		if p1 == p2 {
			t.Errorf("PairPriority(%d,%d)==PairPriority(%d,%d)=%d; expected them to differ by 1",
				c.g, c.d, c.d, c.g, p1)
		}
		diff := p1 - p2
		if p2 > p1 {
			diff = p2 - p1
		}
		if diff != 1 {
			t.Errorf("PairPriority(%d,%d) - PairPriority(%d,%d) = %d; want 1",
				c.g, c.d, c.d, c.g, diff)
		}
	}
}
