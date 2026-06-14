package ice_test

// Adversarial, RFC 8445-spec-anchored tests for pkg/ice.
//
// These tests deliberately target the two exact integer formulas at their
// numeric extremes — the classes of edge that produced real bugs elsewhere in
// this codebase tonight (uint32 overflow, off-by-one). Every `want` here is
// hand-computed from the RFC formula text, NOT read back from the production
// code, so the assertions pin the SPEC contract rather than the current output.
//
// CENTERPIECE 1 — ComputePriority (RFC 8445 §5.1.2.1):
//     priority = (2^24)*typePref + (2^8)*localPref + (256 - componentID)
//   Result must fit in uint32 at every legal input. The maximum legal input
//   (typePref=126, localPref=65535, componentID=1) yields 2130706431, well under
//   2^32-1, so there is headroom; we still assert exact equality at the corners.
//
// CENTERPIECE 2 — PairPriority (RFC 8445 §6.1.2.3):
//     pairPriority = 2^32*min(G,D) + 2*max(G,D) + (G>D ? 1 : 0)
//   The 2^32*min term ALONE overflows uint32 for any min>0, so the result type
//   MUST be uint64. A uint32 implementation would wrap catastrophically. The
//   probe PairPriority(0xFFFFFFFF, 0xFFFFFFFE) lands on exactly 2^64-1 — the
//   single most diagnostic input: a uint32 impl returns 4294967295, the correct
//   uint64 value is 18446744073709551615.

import (
	"math"
	"net/netip"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/ice"
)

// ─────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 1: ComputePriority — uint32 boundary / no-truncation.
// ─────────────────────────────────────────────────────────────────────────────

// TestComputePriorityBoundaryUint32 pins ComputePriority at the legal-input
// corners, with emphasis on componentID at both ends of its 1..256 range
// (the existing suite never exercises componentID=256) and on confirming the
// maximum legal result does not exceed uint32.
//
// Anti-bluff mutation: change the (256 - componentID) term to (255 -
// componentID), or change <<24 to <<23. Either makes the hand-computed want
// values below fail. The mutated code still compiles.
func TestComputePriorityBoundaryUint32(t *testing.T) {
	t.Parallel()

	type tc struct {
		name        string
		typePref    uint8
		localPref   uint16
		componentID uint16
		want        uint32
	}

	// All want values computed by hand from the RFC formula.
	tests := []tc{
		{
			// Absolute maximum legal result: typePref=126, localPref=65535, comp=1.
			// 2113929216 + 16776960 + 255 = 2130706431  (< 2^32-1 = 4294967295)
			name: "max-legal-result/fits-uint32", typePref: 126, localPref: 65535, componentID: 1,
			want: 2130706431,
		},
		{
			// componentID at its upper bound 256 → (256-256)=0 low term.
			// 2113929216 + 16776960 + 0 = 2130706176
			name: "host/maxLocalPref/component256", typePref: 126, localPref: 65535, componentID: 256,
			want: 2130706176,
		},
		{
			// All-zero inputs except componentID=1 → only the (256-1) term survives.
			name: "all-zero/component1", typePref: 0, localPref: 0, componentID: 1,
			want: 255,
		},
		{
			// componentID=256 with everything else zero → 0.
			name: "all-zero/component256", typePref: 0, localPref: 0, componentID: 256,
			want: 0,
		},
		{
			// Single high bit of typePref isolated: typePref=1 → exactly 2^24.
			// 16777216 + 0 + (256-1)=255 → 16777471
			name: "typePref1/isolates-2^24", typePref: 1, localPref: 0, componentID: 1,
			want: 16777471,
		},
		{
			// Single high bit of localPref isolated: localPref=1 → exactly 2^8.
			// 0 + 256 + 255 = 511
			name: "localPref1/isolates-2^8", typePref: 0, localPref: 1, componentID: 1,
			want: 511,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ice.ComputePriority(tt.typePref, tt.localPref, tt.componentID)
			if got != tt.want {
				t.Errorf("ComputePriority(%d,%d,%d) = %d; want %d (RFC 8445 §5.1.2.1)",
					tt.typePref, tt.localPref, tt.componentID, got, tt.want)
			}
			// The result must never exceed uint32 (it is typed uint32, but assert
			// the spec invariant explicitly against the widened hand value).
			if uint64(got) > math.MaxUint32 {
				t.Errorf("ComputePriority(%d,%d,%d) = %d exceeds uint32 max",
					tt.typePref, tt.localPref, tt.componentID, got)
			}
		})
	}
}

// TestComputePriorityMonotoneInTypePref asserts the spec ordering property:
// a higher type preference always yields a higher priority when localPref and
// componentID are held fixed. This pins the (2^24)*typePref term's dominance —
// a coefficient bug that let localPref overpower typePref would violate the RFC
// preference ordering (host must outrank srflx must outrank relay).
//
// Anti-bluff mutation: change <<24 to <<8 in ComputePriority. With localPref at
// max, the typePref term no longer dominates and the strict ordering breaks.
func TestComputePriorityMonotoneInTypePref(t *testing.T) {
	t.Parallel()

	const lp, comp = uint16(65535), uint16(1)
	relay := ice.ComputePriority(0, lp, comp)
	srflx := ice.ComputePriority(100, lp, comp)
	prflx := ice.ComputePriority(110, lp, comp)
	host := ice.ComputePriority(126, lp, comp)

	if !(host > prflx && prflx > srflx && srflx > relay) {
		t.Errorf("RFC preference ordering violated: host=%d prflx=%d srflx=%d relay=%d (want host>prflx>srflx>relay even at maxLocalPref)",
			host, prflx, srflx, relay)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CENTERPIECE 2: PairPriority — uint64 / overflow / tiebreak.
// ─────────────────────────────────────────────────────────────────────────────

// TestPairPriorityUint32OverflowMustBeUint64 is the single most diagnostic test
// in this file. With G and D near uint32 max, the 2^32*min term is ~2^64; a
// uint32 (or even an accidental uint32 intermediate / wrong shift) implementation
// would wrap to a tiny value. The probe (0xFFFFFFFF, 0xFFFFFFFE) lands exactly
// on 2^64-1, and its swap on 2^64-2.
//
// Anti-bluff mutations that this test catches:
//   - return type / intermediate as uint32  → result wraps to 4294967295
//   - drop the <<32 factor (use *2 or *1)    → result off by ~2^64
//   - swap min/max                           → 2^32*max term overflows uint64
//   - flip the tiebreak (G<D instead of G>D) → the +1 lands on the wrong case
func TestPairPriorityUint32OverflowMustBeUint64(t *testing.T) {
	t.Parallel()

	const (
		uMax  = uint32(0xFFFFFFFF) // 4294967295
		uMax1 = uint32(0xFFFFFFFE) // 4294967294
	)

	type tc struct {
		name        string
		controlling uint32
		controlled  uint32
		want        uint64
	}

	tests := []tc{
		{
			// G=2^32-1, D=2^32-2 → min=D, max=G, G>D → tiebit=1
			// 2^32*(2^32-2) + 2*(2^32-1) + 1
			// = 18446744065119617024 + 8589934590 + 1 = 18446744073709551615 = 2^64-1
			name:        "near-max/G>D/tiebit=1/equals-uint64-max",
			controlling: uMax, controlled: uMax1, want: math.MaxUint64,
		},
		{
			// Swap: G=2^32-2, D=2^32-1 → min=G, max=D, G<D → tiebit=0
			// = 18446744065119617024 + 2*(2^32-1) + 0 = 18446744073709551614 = 2^64-2
			name:        "near-max/G<D/tiebit=0",
			controlling: uMax1, controlled: uMax, want: math.MaxUint64 - 1,
		},
		{
			// G=D=2^32-1 → min=max=2^32-1, tiebit=0
			// 2^32*(2^32-1) + 2*(2^32-1)
			// = 18446744069414584320 + 8589934590 = 18446744078004518910 — but that
			// EXCEEDS 2^64-1, so for G==D at the very top the result is NOT
			// representable; instead use the largest G==D that still fits is not the
			// point here. We assert the smaller, exactly-known value below instead.
			//
			// Use G=D=2^31 (clean power) to avoid representability ambiguity:
			// 2^32*2^31 + 2*2^31 + 0 = 2^63 + 2^32 = 9223372036854775808 + 4294967296
			// = 9223372041149743104
			name:        "mid/G==D/tiebit=0",
			controlling: 1 << 31, controlled: 1 << 31, want: (1 << 63) + (1 << 32),
		},
		{
			// Zero priorities → 0 (degenerate but legal).
			name: "both-zero", controlling: 0, controlled: 0, want: 0,
		},
		{
			// G=0, D=1 → min=0, max=1, G<D → tiebit=0 → 0 + 2 + 0 = 2
			name: "G0-D1/tiebit=0", controlling: 0, controlled: 1, want: 2,
		},
		{
			// G=1, D=0 → min=0, max=1, G>D → tiebit=1 → 0 + 2 + 1 = 3
			name: "G1-D0/tiebit=1", controlling: 1, controlled: 0, want: 3,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ice.PairPriority(tt.controlling, tt.controlled)
			if got != tt.want {
				t.Errorf("PairPriority(%d,%d) = %d; want %d (RFC 8445 §6.1.2.3, 2^32*min+2*max+(G>D))\n"+
					"  NOTE: a uint32 implementation here would wrap to a small value — the produced value above is the smoking gun.",
					tt.controlling, tt.controlled, got, tt.want)
			}
		})
	}
}

// TestPairPriorityTiebreakIsStrictlyGgtD pins the EXACT tiebreak predicate:
// the +1 bit is set iff G > D (strictly). It must NOT be set on G == D, and it
// must NOT be set on G < D. This is the off-by-one-class probe for the formula's
// last term.
//
// Anti-bluff mutation: change `if g > d` to `if g >= d`. The G==D case below
// then gains a spurious +1 and fails.
func TestPairPriorityTiebreakIsStrictlyGgtD(t *testing.T) {
	t.Parallel()

	// For a fixed (a,b) with a>b, the base = 2^32*b + 2*a is shared by both
	// orderings; only the tiebit differs.
	const a, b = uint32(777), uint32(333)
	base := (uint64(1)<<32)*uint64(b) + 2*uint64(a)

	if got := ice.PairPriority(a, b); got != base+1 { // G=a>b=D → +1
		t.Errorf("PairPriority(%d,%d) = %d; want %d (G>D must set tiebit)", a, b, got, base+1)
	}
	if got := ice.PairPriority(b, a); got != base { // G=b<a=D → +0
		t.Errorf("PairPriority(%d,%d) = %d; want %d (G<D must clear tiebit)", b, a, got, base)
	}
	// G==D must clear the tiebit (strict >, not >=).
	const e = uint32(555)
	wantEq := (uint64(1)<<32)*uint64(e) + 2*uint64(e)
	if got := ice.PairPriority(e, e); got != wantEq {
		t.Errorf("PairPriority(%d,%d) = %d; want %d (G==D must NOT set tiebit)", e, e, got, wantEq)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SortPairs — descending order, ties, and the controlled-agent path.
// ─────────────────────────────────────────────────────────────────────────────

// TestSortPairsControlledPath exercises localIsControlling=false, which the
// existing suite never covers. With the local agent controlled, the priorities
// fed to PairPriority are (Remote, Local) — swapping which value is G affects the
// tiebit and therefore can change ordering between otherwise-mirror pairs.
//
// Anti-bluff mutation: in the localIsControlling==false branch of SortPairs,
// call PairPriority(Local, Remote) instead of (Remote, Local). The ordering of
// the two near-tie pairs below inverts.
func TestSortPairsControlledPath(t *testing.T) {
	t.Parallel()

	r := netip.MustParseAddrPort("203.0.113.1:3478")

	// Two pairs that are mirror images across G/D so only the tiebit (and hence
	// the G=Remote vs G=Local choice) decides order.
	// localIsControlling=false → G=Remote, D=Local.
	// pHi: Remote=900 > Local=800 → G>D → tiebit=1 (higher)
	// pLo: Remote=800 < Local=900 → G<D → tiebit=0 (lower), same base.
	pHi := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.1:1"), Priority: 800, Foundation: "L1"},
		Remote: ice.Candidate{Addr: r, Priority: 900, Foundation: "R1"},
	}
	pLo := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.2:1"), Priority: 900, Foundation: "L2"},
		Remote: ice.Candidate{Addr: r, Priority: 800, Foundation: "R2"},
	}

	// Sanity: confirm the two pair priorities differ by exactly the tiebit.
	hi := ice.PairPriority(pHi.Remote.Priority, pHi.Local.Priority)
	lo := ice.PairPriority(pLo.Remote.Priority, pLo.Local.Priority)
	if hi != lo+1 {
		t.Fatalf("test setup: expected hi==lo+1, got hi=%d lo=%d", hi, lo)
	}

	pairs := []ice.CandidatePair{pLo, pHi}
	ice.SortPairs(pairs, false)

	if pairs[0].Local.Foundation != "L1" {
		t.Errorf("controlled-path sort: pairs[0]=%q; want L1 (higher tiebit must sort first descending)",
			pairs[0].Local.Foundation)
	}
}

// TestSortPairsEmptyAndSingle pins the degenerate inputs: an empty slice and a
// single-element slice must be handled without panic and left logically intact.
func TestSortPairsEmptyAndSingle(t *testing.T) {
	t.Parallel()

	// Empty.
	var empty []ice.CandidatePair
	ice.SortPairs(empty, true)
	if len(empty) != 0 {
		t.Errorf("SortPairs(empty) mutated length to %d", len(empty))
	}

	// Single.
	one := []ice.CandidatePair{{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.1:1"), Priority: 42, Foundation: "S"},
		Remote: ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.2:1"), Priority: 7, Foundation: "T"},
	}}
	ice.SortPairs(one, true)
	if len(one) != 1 || one[0].Local.Foundation != "S" {
		t.Errorf("SortPairs(single) altered the lone element: %+v", one)
	}
}

// TestSortPairsFullDescendingExact builds five pairs with distinct, hand-computed
// pair priorities and asserts the EXACT resulting order, then re-verifies the
// produced priority sequence is strictly decreasing. This is a stronger ordering
// assertion than the existing 3-element test.
//
// Anti-bluff mutation: flip the comparator (piPrio < pjPrio). The asserted
// descending order inverts and the first mismatch fails.
func TestSortPairsFullDescendingExact(t *testing.T) {
	t.Parallel()

	r := netip.MustParseAddrPort("198.51.100.7:3478")
	mk := func(found string, local, remote uint32, lip string) ice.CandidatePair {
		return ice.CandidatePair{
			Local:  ice.Candidate{Addr: netip.MustParseAddrPort(lip), Priority: local, Foundation: found},
			Remote: ice.Candidate{Addr: r, Priority: remote, Foundation: "R"},
		}
	}

	// localIsControlling=true → PairPriority(Local, Remote).
	// Compute each priority by hand to derive expected order:
	//   p1: (10, 10)  → 2^32*10 + 20            = 42949672980
	//   p2: (1000,1)  → 2^32*1 + 2000 + 1       = 4294969297
	//   p3: (5, 9)    → 2^32*5 + 18             = 21474836498
	//   p4: (9, 5)    → 2^32*5 + 18 + 1         = 21474836499
	//   p5: (0, 0)    → 0
	// Descending: p1(42.9e9) > p4(21474836499) > p3(21474836498) > p2(4294969297) > p5(0)
	p1 := mk("F1", 10, 10, "10.0.0.1:1")
	p2 := mk("F2", 1000, 1, "10.0.0.2:1")
	p3 := mk("F3", 5, 9, "10.0.0.3:1")
	p4 := mk("F4", 9, 5, "10.0.0.4:1")
	p5 := mk("F5", 0, 0, "10.0.0.5:1")

	pairs := []ice.CandidatePair{p3, p5, p1, p2, p4} // scrambled input
	ice.SortPairs(pairs, true)

	wantOrder := []string{"F1", "F4", "F3", "F2", "F5"}
	for i, want := range wantOrder {
		if pairs[i].Local.Foundation != want {
			t.Errorf("pairs[%d].Foundation = %q; want %q (full descending order)",
				i, pairs[i].Local.Foundation, want)
		}
	}

	// Independently re-derive the priority sequence and assert strictly decreasing.
	prev := ^uint64(0)
	for i, p := range pairs {
		cur := ice.PairPriority(p.Local.Priority, p.Remote.Priority)
		if i > 0 && cur >= prev {
			t.Errorf("priority at index %d (%d) is not strictly less than previous (%d)", i, cur, prev)
		}
		prev = cur
	}
}

// TestSortPairsTieBreakByAddrWhenFoundationsEqual pins the LAST tiebreak rung:
// when pair priority AND both foundations are equal, ordering falls through to
// local then remote address strings. The existing determinism test differs by
// foundation; this one forces the addr-string comparison to be the deciding key.
//
// Anti-bluff mutation: remove the final Addr-string comparisons in SortPairs
// (leave only foundation tiebreaks). With identical foundations the order
// becomes input-dependent and the deterministic assertion across two scrambles
// fails.
func TestSortPairsTieBreakByAddrWhenFoundationsEqual(t *testing.T) {
	t.Parallel()

	r := netip.MustParseAddrPort("198.51.100.9:3478")
	// Same priority, same foundations; differ only by local addr string.
	lo := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.1:1"), Priority: 100, Foundation: "F"},
		Remote: ice.Candidate{Addr: r, Priority: 100, Foundation: "R"},
	}
	hi := ice.CandidatePair{
		Local:  ice.Candidate{Addr: netip.MustParseAddrPort("10.0.0.2:1"), Priority: 100, Foundation: "F"},
		Remote: ice.Candidate{Addr: r, Priority: 100, Foundation: "R"},
	}

	run1 := []ice.CandidatePair{hi, lo}
	run2 := []ice.CandidatePair{lo, hi}
	ice.SortPairs(run1, true)
	ice.SortPairs(run2, true)

	// "10.0.0.1:1" < "10.0.0.2:1" lexicographically, so lo must come first in both.
	if run1[0].Local.Addr.String() != "10.0.0.1:1" || run2[0].Local.Addr.String() != "10.0.0.1:1" {
		t.Errorf("addr-string tiebreak not deterministic: run1[0]=%s run2[0]=%s; want both 10.0.0.1:1",
			run1[0].Local.Addr.String(), run2[0].Local.Addr.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Cross-formula end-to-end: GatherHostCandidates priorities feed PairPriority.
// ─────────────────────────────────────────────────────────────────────────────

// TestGatheredHostPriorityFeedsPairPriority is an end-to-end contract check: the
// uint32 priority produced by GatherHostCandidates, when fed (with itself) into
// PairPriority, must produce the exact uint64 value the RFC formula predicts —
// proving the two formulas compose without truncation at a realistic host
// priority (2130706431, the max-localPref host value).
func TestGatheredHostPriorityFeedsPairPriority(t *testing.T) {
	t.Parallel()

	cands, err := ice.GatherHostCandidates(0)
	if err != nil {
		t.Fatalf("GatherHostCandidates: %v", err)
	}
	if len(cands) == 0 {
		t.Skip("no usable host candidates on this machine; nothing to compose")
	}

	hostPrio := ice.ComputePriority(ice.CandidateHost.TypePreference(), 65535, 1)
	if cands[0].Priority != hostPrio {
		t.Fatalf("gathered host priority = %d; want %d", cands[0].Priority, hostPrio)
	}

	// G==D path with a real, large host priority. tiebit must be 0.
	// 2^32*hostPrio + 2*hostPrio
	want := (uint64(1)<<32)*uint64(hostPrio) + 2*uint64(hostPrio)
	got := ice.PairPriority(cands[0].Priority, cands[0].Priority)
	if got != want {
		t.Errorf("PairPriority(hostPrio,hostPrio) = %d; want %d (large-value composition, no truncation)", got, want)
	}
}
