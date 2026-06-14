package hlc

import (
	"math"
	"testing"
	"time"
)

// advClock is a deterministic, settable physical clock for the adversarial
// suite. It is independent of stubClock in package_test.go so this file is
// self-contained.
type advClock struct{ ns int64 }

func (a *advClock) now() time.Time { return time.Unix(0, a.ns) }

// ---------------------------------------------------------------------------
// CENTERPIECE 1: MONOTONICITY
// ---------------------------------------------------------------------------

// TestAdv_Now_StrictlyIncreasing_AcrossPhysicalRegime hammers Now() while the
// injected physical clock advances, freezes, and runs backwards in an
// interleaved pattern, asserting EVERY emitted timestamp is strictly greater
// than the immediately prior one AND than the running maximum. This is the
// whole purpose of an HLC: a monotonic causal clock regardless of wall-clock
// behaviour.
//
// Mutation that bites: in Now(), change `if phys > c.last.Physical` to
// `if true` (regression overwrites with a smaller physical, zero logical) ->
// the regression segment produces a timestamp < the running max and the
// assertion fails.
func TestAdv_Now_StrictlyIncreasing_AcrossPhysicalRegime(t *testing.T) {
	clk := &advClock{ns: 1_000_000}
	c := New(clk.now)

	// A path of physical-clock readings: advance, freeze (repeat), big jump
	// back, freeze, small forward, back again. Every Now() must still climb.
	path := []int64{
		1_000_000, 1_000_000, 1_000_000, // frozen: logical must carry
		2_000_000,        // forward
		500_000,          // big regression
		500_000, 500_000, // frozen low
		3_000_000, // forward again, past everything
		2_999_999, // tiny regression
		3_000_000, // equal to a prior winner
	}

	prev := c.Now()
	max := prev
	for i, ns := range path {
		clk.ns = ns
		got := c.Now()
		if !prev.Less(got) {
			t.Fatalf("step %d (ns=%d): not strictly > previous: %+v !< %+v", i, ns, prev, got)
		}
		if !max.Less(got) {
			t.Fatalf("step %d (ns=%d): not strictly > running max: max=%+v got=%+v", i, ns, max, got)
		}
		prev = got
		max = got
	}
}

// TestAdv_Now_FrozenTick_DistinctOrderedLogical proves SAME-TICK ordering:
// many events within one physical nanosecond each get a distinct, strictly
// increasing logical counter (events in the same tick must be ordered).
//
// Mutation that bites: drop the logical bump in Now()'s else branch
// (`Logical: c.last.Logical` instead of `+1`) -> consecutive timestamps are
// equal, the strict-increase and uniqueness checks fail.
func TestAdv_Now_FrozenTick_DistinctOrderedLogical(t *testing.T) {
	clk := &advClock{ns: 7}
	c := New(clk.now)

	const n = 2048
	seen := make(map[uint32]bool, n)
	prev := c.Now()
	seen[prev.Logical] = true
	for i := 1; i < n; i++ {
		got := c.Now()
		if got.Physical != 7 {
			t.Fatalf("i=%d: frozen physical drifted to %d", i, got.Physical)
		}
		if got.Logical != prev.Logical+1 {
			t.Fatalf("i=%d: logical not +1: prev=%d got=%d", i, prev.Logical, got.Logical)
		}
		if seen[got.Logical] {
			t.Fatalf("i=%d: duplicate logical in same tick: %d", i, got.Logical)
		}
		seen[got.Logical] = true
		prev = got
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct logicals in one tick, got %d", n, len(seen))
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 2: UPDATE DOMINATES (happens-before preserved)
// ---------------------------------------------------------------------------

// TestAdv_Update_DominatesBoth_Matrix runs Update across a matrix of
// local-vs-remote relationships (remote ahead / behind / equal physical, with
// various logical counters) and asserts the merged result strictly dominates
// BOTH the prior local timestamp and the remote, and that a following Now()
// keeps climbing. Domination of both inputs is exactly the happens-before
// edge remote -> local that this clock must encode.
//
// Mutation that bites: make Update use min instead of max for the physical
// component (e.g. `if remotePhys < maxPhys { maxPhys = remotePhys }`) -> a
// remote-ahead case yields merged.Physical < remote.Physical and
// remote.Less(merged) fails. Also: drop any branch's logical bump -> equal-
// physical cases stop dominating.
func TestAdv_Update_DominatesBoth_Matrix(t *testing.T) {
	// localPhys is the wall-clock reading; we also seed a prior local timestamp
	// via Now()/Update so c.last has a meaningful logical counter.
	type tc struct {
		name       string
		localPhys  int64
		seedRemote Timestamp // applied first to set up c.last
		remote     Timestamp // the remote under test
	}
	cases := []tc{
		{"remote ahead, fresh local", 100, Timestamp{}, Timestamp{Physical: 10_000, Logical: 7}},
		{"remote behind local", 10_000, Timestamp{}, Timestamp{Physical: 100, Logical: 99}},
		{"remote equal physical, higher logical", 500, Timestamp{Physical: 500, Logical: 2}, Timestamp{Physical: 500, Logical: 50}},
		{"remote equal physical, lower logical", 500, Timestamp{Physical: 500, Logical: 40}, Timestamp{Physical: 500, Logical: 3}},
		{"remote equal physical, equal logical", 500, Timestamp{Physical: 500, Logical: 9}, Timestamp{Physical: 500, Logical: 9}},
		{"remote behind, local has high logical", 800, Timestamp{Physical: 800, Logical: 123}, Timestamp{Physical: 200, Logical: 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &advClock{ns: c.localPhys}
			clock := New(clk.now)
			// Seed prior local state if requested.
			if (c.seedRemote != Timestamp{}) {
				clock.Update(c.seedRemote)
			}
			priorLocal := clock.Last()

			merged := clock.Update(c.remote)

			if !priorLocal.Less(merged) {
				t.Fatalf("merged %+v does not dominate prior local %+v", merged, priorLocal)
			}
			if !c.remote.Less(merged) {
				t.Fatalf("merged %+v does not dominate remote %+v (happens-before broken)", merged, c.remote)
			}
			// merged physical is the max of the three inputs (no clamp in range here).
			wantPhysAtLeast := c.localPhys
			if c.remote.Physical > wantPhysAtLeast {
				wantPhysAtLeast = c.remote.Physical
			}
			if priorLocal.Physical > wantPhysAtLeast {
				wantPhysAtLeast = priorLocal.Physical
			}
			if merged.Physical != wantPhysAtLeast {
				t.Fatalf("merged.Physical = %d, want max-of-inputs %d", merged.Physical, wantPhysAtLeast)
			}
			// A subsequent local event must still be strictly greater.
			next := clock.Now()
			if !merged.Less(next) {
				t.Fatalf("post-Update Now() not monotonic: %+v !< %+v", merged, next)
			}
		})
	}
}

// TestAdv_Update_RepeatedSameRemote_StaysAbove proves replaying the SAME remote
// message repeatedly (a common real scenario: retries / gossip re-delivery)
// never makes the local clock go backwards and each merge stays >= the remote.
//
// Mutation that bites: Update using min for the logical tie-break or dropping
// the bump -> a replayed equal-physical remote stops being dominated.
func TestAdv_Update_RepeatedSameRemote_StaysAbove(t *testing.T) {
	clk := &advClock{ns: 1000}
	c := New(clk.now)
	remote := Timestamp{Physical: 1000, Logical: 5}

	prev := c.Update(remote)
	for i := 0; i < 100; i++ {
		got := c.Update(remote)
		if !prev.Less(got) {
			t.Fatalf("replay i=%d: clock not strictly advancing: %+v !< %+v", i, prev, got)
		}
		if !remote.Less(got) && remote.Compare(got) != 0 {
			// got must be > remote (strictly, since same physical and we bump).
		}
		if got.Compare(remote) <= 0 {
			t.Fatalf("replay i=%d: merged %+v not > remote %+v", i, got, remote)
		}
		prev = got
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE 3: DECODE ROBUSTNESS (never panic on hostile bytes)
// ---------------------------------------------------------------------------

// TestAdv_Decode_NeverPanics_HostileBytes feeds nil, empty, every prefix of a
// valid encoding, over-long buffers, and adversarial byte patterns (all-0xFF,
// alternating) to Decode. Contract: it returns ErrShortBuffer for any non-12
// length and NEVER panics; a panic on hostile input is a DoS bug.
//
// Mutation that bites: change Decode's `if len(b) != encodedLen` guard to
// `if false` -> a short/nil buffer indexes out of range and panics; the
// recover-based failer fires.
func TestAdv_Decode_NeverPanics_HostileBytes(t *testing.T) {
	valid := Timestamp{Physical: 1_700_000_000_000_000_000, Logical: 12345}.Encode()

	var inputs [][]byte
	inputs = append(inputs, nil, []byte{})
	// Every prefix of a valid encoding (lengths 0..12) and a couple over-long.
	for i := 0; i <= encodedLen+4; i++ {
		inputs = append(inputs, make([]byte, i))
	}
	// Every prefix of an actual valid encoding (preserves real bytes).
	for i := 0; i <= len(valid); i++ {
		cp := make([]byte, i)
		copy(cp, valid[:i])
		inputs = append(inputs, cp)
	}
	// Adversarial patterns at and around the boundary length.
	for _, n := range []int{11, 12, 13, 24, 100} {
		all1 := make([]byte, n)
		for j := range all1 {
			all1[j] = 0xFF
		}
		inputs = append(inputs, all1)
		alt := make([]byte, n)
		for j := range alt {
			if j%2 == 0 {
				alt[j] = 0xAA
			} else {
				alt[j] = 0x55
			}
		}
		inputs = append(inputs, alt)
	}

	for idx, b := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decode panicked on hostile input idx=%d len=%d: %v", idx, len(b), r)
				}
			}()
			ts, err := Decode(b)
			switch len(b) {
			case encodedLen:
				if err != nil {
					t.Fatalf("idx=%d: valid-length buffer rejected: %v", idx, err)
				}
				// Must round-trip back to identical bytes.
				re := ts.Encode()
				for k := range b {
					if re[k] != b[k] {
						t.Fatalf("idx=%d: re-encode mismatch at byte %d: %x vs %x", idx, k, re[k], b[k])
					}
				}
			default:
				if err != ErrShortBuffer {
					t.Fatalf("idx=%d len=%d: got err %v, want ErrShortBuffer", idx, len(b), err)
				}
				if (ts != Timestamp{}) {
					t.Fatalf("idx=%d: error path returned non-zero ts %+v", idx, ts)
				}
			}
		}()
	}
}

// ---------------------------------------------------------------------------
// ENCODE/DECODE BIJECTION over boundary values
// ---------------------------------------------------------------------------

// TestAdv_EncodeDecode_Bijective_Boundaries asserts Encode then Decode is the
// exact identity over the corners of the value space, including the full int64
// range (negative physical -> high bit set) and full uint32 logical range.
//
// Mutation that bites: change Encode/Decode to LittleEndian on one side only,
// or mask the logical to 16 bits -> a boundary value fails to round-trip.
func TestAdv_EncodeDecode_Bijective_Boundaries(t *testing.T) {
	vals := []Timestamp{
		{Physical: 0, Logical: 0},
		{Physical: math.MaxInt64, Logical: math.MaxUint32},
		{Physical: math.MinInt64, Logical: 0},
		{Physical: math.MinInt64, Logical: math.MaxUint32},
		{Physical: -1, Logical: 1},
		{Physical: 1, Logical: math.MaxUint32},
		{Physical: math.MaxInt64, Logical: 0},
	}
	for _, want := range vals {
		got, err := Decode(want.Encode())
		if err != nil {
			t.Fatalf("Decode(Encode(%+v)) error: %v", want, err)
		}
		if got != want {
			t.Fatalf("not bijective: got %+v, want %+v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// LOGICAL-COUNTER OVERFLOW — DOCUMENTS A REAL CAUSALITY DEFECT (see FINDING)
// ---------------------------------------------------------------------------

// TestAdv_Update_RemoteMaxLogical_MustDominate encodes the contract that
// Update's own doc promises: "The result is strictly greater than both the
// prior local timestamp and remote." A REMOTE timestamp is attacker/peer
// controlled wire data; a peer can legally send Logical == math.MaxUint32 at a
// winning physical time. Production computes `remote.Logical + 1`, which wraps
// to 0, so the merged result does NOT dominate the remote — a real, externally
// reachable happens-before violation.
//
// This test asserts the CORRECT contract (domination must hold). It currently
// FAILS against production, which is the bug. It is skipped with a written
// justification so the suite stays green while the defect is tracked; remove
// the Skip once the saturating-increment fix lands (see FINDING in report).
func TestAdv_Update_RemoteMaxLogical_MustDominate(t *testing.T) {
	// FIXED (HXC-1753): Update now carries a logical-counter overflow into the
	// physical component (bumpLogical) instead of wrapping, so a remote with
	// Logical=math.MaxUint32 at the winning physical time is still dominated.
	clk := &advClock{ns: 100} // local behind remote
	c := New(clk.now)
	remote := Timestamp{Physical: 10_000, Logical: math.MaxUint32}
	merged := c.Update(remote)
	if !remote.Less(merged) {
		t.Fatalf("causality violated: merged %+v does not dominate remote %+v", merged, remote)
	}
}

// TestAdv_Now_LogicalOverflow_MustStayMonotonic documents the same wrapping
// hazard for Now() when the logical counter is saturated under a frozen clock.
// In practice this is unreachable with a real time.Now() (it would require
// ~4.29e9 Now() calls inside a single physical nanosecond), so it is a lower-
// severity theoretical edge than the remote-controlled Update case, but the
// correct contract is still strict monotonicity.
//
// Skipped with justification; un-skip alongside the overflow fix.
func TestAdv_Now_LogicalOverflow_MustStayMonotonic(t *testing.T) {
	// FIXED (HXC-1753): Now() carries a logical-counter overflow into the
	// physical component (bumpLogical), so strict monotonicity holds even at
	// Logical=math.MaxUint32 under a frozen clock.
	clk := &advClock{ns: 42}
	c := New(clk.now)
	c.mu.Lock()
	c.last = Timestamp{Physical: 42, Logical: math.MaxUint32}
	prev := c.last
	c.mu.Unlock()
	got := c.Now()
	if !prev.Less(got) {
		t.Fatalf("monotonicity violated at overflow: %+v !< %+v", prev, got)
	}
}
