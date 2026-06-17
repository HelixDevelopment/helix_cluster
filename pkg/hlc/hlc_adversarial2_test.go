package hlc

import (
	"math"
	"sync"
	"testing"
	"time"
)

// adv2Clock is a deterministic, settable physical clock for THIS adversarial
// wave. Kept independent of advClock/stubClock so the file is self-contained.
type adv2Clock struct {
	mu sync.Mutex
	ns int64
}

func (a *adv2Clock) now() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Unix(0, a.ns)
}

func (a *adv2Clock) set(ns int64) {
	a.mu.Lock()
	a.ns = ns
	a.mu.Unlock()
}

// ---------------------------------------------------------------------------
// FINDING (HXC second-overflow): the logical-counter-overflow fix carries a
// uint32 ceiling into the PHYSICAL component (bumpLogical: physical+1, 0). But
// it never guarded physical == math.MaxInt64, so when BOTH components are
// saturated the carry overflows int64 and WRAPS to math.MinInt64 — a
// catastrophic backward jump. `physical` is reachable from peer/attacker wire
// data (a remote sending {MaxInt64, MaxUint32} to Update with clamping
// disabled), so this is an externally reachable monotonicity / happens-before
// defect: exactly the SECOND overflow the first pass missed.
//
// CONTRACT these tests pin (and the applied fix satisfies):
//   - Update/Now MUST NEVER return a timestamp LESS than a prior/observed one
//     (no int64 wrap). At the absolute ceiling {MaxInt64, MaxUint32} the clock
//     SATURATES (stalls at the max) rather than wrapping — monotone non-regress.
//   - Below the ceiling, strict increase still holds.
// ---------------------------------------------------------------------------

// TestAdv2_Update_SaturatedRemote_NoInt64Wrap is the centerpiece. A remote
// sends the largest representable timestamp {MaxInt64, MaxUint32} with the
// drift clamp DISABLED (negative maxDrift — a documented mode). Pre-fix,
// bumpLogical computed MaxInt64+1 == MinInt64 and Update returned a timestamp
// FAR BELOW both inputs. Contract: the merged result must NEVER be < the
// remote (no backward wrap); it saturates at the ceiling instead.
//
// Mutation that bites (reverts the fix): drop the physical==MaxInt64 guard in
// bumpLogical so it returns (physical+1, 0) -> merged.Physical wraps to
// math.MinInt64, merged < remote, and BOTH assertions below fire.
func TestAdv2_Update_SaturatedRemote_NoInt64Wrap(t *testing.T) {
	clk := &adv2Clock{ns: 100}        // local far behind remote
	c := NewWithMaxDrift(clk.now, -1) // negative disables clamp (documented)

	remote := Timestamp{Physical: math.MaxInt64, Logical: math.MaxUint32}
	merged := c.Update(remote)

	// Non-regression: the merged timestamp must not be smaller than the remote
	// it just observed. A wrap to MinInt64 would make merged catastrophically
	// less than remote.
	if merged.Compare(remote) < 0 {
		t.Fatalf("int64 overflow wrap: merged %+v < observed remote %+v (clock jumped BACKWARD)", merged, remote)
	}
	// At the absolute ceiling there is no larger representable value, so the
	// honest result is exactly the ceiling (saturation), never a wrapped value.
	if merged.Physical < 0 {
		t.Fatalf("merged.Physical is negative (%d): physical component wrapped", merged.Physical)
	}
	if merged != (Timestamp{Physical: math.MaxInt64, Logical: math.MaxUint32}) {
		t.Fatalf("expected saturation at ceiling {MaxInt64,MaxUint32}, got %+v", merged)
	}
}

// TestAdv2_Now_SaturatedLast_NoInt64Wrap drives the same defect through Now():
// with c.last already at the value-space ceiling and the physical clock frozen,
// Now() takes the else branch -> bumpLogical(MaxInt64, MaxUint32). Pre-fix this
// wrapped to MinInt64, sending the local clock backward by ~2^63 ns.
//
// Mutation that bites (reverts the fix): without the guard, got.Physical wraps
// to math.MinInt64 and the non-regression assertion fires.
func TestAdv2_Now_SaturatedLast_NoInt64Wrap(t *testing.T) {
	clk := &adv2Clock{ns: 42}
	c := New(clk.now)

	c.mu.Lock()
	c.last = Timestamp{Physical: math.MaxInt64, Logical: math.MaxUint32}
	prev := c.last
	c.mu.Unlock()

	got := c.Now()
	if got.Compare(prev) < 0 {
		t.Fatalf("Now() wrapped backward at ceiling: %+v < prior %+v", got, prev)
	}
	if got.Physical < 0 {
		t.Fatalf("Now().Physical negative (%d): int64 carry wrapped", got.Physical)
	}
	// Repeated calls at the ceiling must remain pinned at the ceiling, never
	// regress. (Strict increase is genuinely impossible here — no larger value
	// exists — so the contract is non-regression / saturation.)
	for i := 0; i < 100; i++ {
		next := c.Now()
		if next.Compare(got) < 0 {
			t.Fatalf("Now() regressed at saturation i=%d: %+v < %+v", i, next, got)
		}
		got = next
	}
}

// TestAdv2_Update_NearCeilingPhysical_CarriesNotWraps checks the boundary just
// BELOW the int64 ceiling: physical == MaxInt64-1 with a saturated logical must
// CARRY correctly into physical (MaxInt64, 0) — strictly greater, no wrap. This
// pins that the fix did not over-correct (i.e. it still carries normally one
// step below the ceiling) and that the carry lands on a strictly larger value.
//
// Mutation that bites: change bumpLogical's guard to `physical >= MaxInt64-1`
// (over-eager saturation) -> the carry stalls one step early and the strict
// `>` assertion fails.
func TestAdv2_Update_NearCeilingPhysical_CarriesNotWraps(t *testing.T) {
	clk := &adv2Clock{ns: 1}
	c := NewWithMaxDrift(clk.now, -1)

	remote := Timestamp{Physical: math.MaxInt64 - 1, Logical: math.MaxUint32}
	merged := c.Update(remote)

	if !remote.Less(merged) {
		t.Fatalf("near-ceiling carry failed to dominate: merged %+v !> remote %+v", merged, remote)
	}
	want := Timestamp{Physical: math.MaxInt64, Logical: 0}
	if merged != want {
		t.Fatalf("near-ceiling carry: got %+v, want %+v", merged, want)
	}
	if merged.Physical < 0 {
		t.Fatalf("near-ceiling carry wrapped to negative: %d", merged.Physical)
	}
}

// ---------------------------------------------------------------------------
// MONOTONICITY across a FUTURE remote then local Now (clock skew) — and a PAST
// remote must not regress. These are the prompt's primary monotonicity probes
// and are NOT covered by the existing suite (which never sequences a clamped
// far-future Update followed by a backward physical reading into Now).
// ---------------------------------------------------------------------------

// TestAdv2_FutureRemoteThenLocalNow_NeverBackward: a remote with a far-future
// physical time (clock skew / malicious peer) is merged, then the LOCAL
// physical clock is read for Now() at its true (smaller) value. Now() must NOT
// go backward or stall below the merged timestamp — the logical counter has to
// climb because physical regressed relative to c.last.
//
// Mutation that bites: in Now(), change the else-branch to overwrite c.last
// with the raw (smaller) phys (e.g. `c.last = Timestamp{Physical: phys}`) ->
// the post-skew Now() drops below merged and the assertion fires.
func TestAdv2_FutureRemoteThenLocalNow_NeverBackward(t *testing.T) {
	const localT int64 = 1_000_000_000
	clk := &adv2Clock{ns: localT}
	maxDrift := 50 * time.Millisecond
	c := NewWithMaxDrift(clk.now, maxDrift)

	// Remote far in the future (will be clamped to localT+maxDrift).
	future := Timestamp{Physical: localT + 100*int64(maxDrift), Logical: 9}
	merged := c.Update(future)

	// Now local Now() runs at true wall-clock (still localT, < merged.Physical).
	clk.set(localT)
	prev := merged
	for i := 0; i < 5000; i++ {
		got := c.Now()
		if !prev.Less(got) {
			t.Fatalf("post-skew Now() not strictly increasing at i=%d: %+v !< %+v", i, prev, got)
		}
		// The merged physical (clamped ceiling) must hold until wall-clock passes it.
		if got.Physical < merged.Physical {
			t.Fatalf("Now() regressed below merged physical at i=%d: %d < %d", i, got.Physical, merged.Physical)
		}
		prev = got
	}

	// Finally advance wall-clock past the clamped ceiling: Now() must track it
	// forward and reset the logical run.
	clk.set(merged.Physical + 10)
	got := c.Now()
	if !prev.Less(got) {
		t.Fatalf("Now() not increasing once wall-clock overtakes: %+v !< %+v", prev, got)
	}
	if got.Physical != merged.Physical+10 {
		t.Fatalf("Now() did not adopt advanced wall-clock: got physical %d, want %d", got.Physical, merged.Physical+10)
	}
}

// TestAdv2_PastRemote_NeverRegressesLocalClock: a remote with a PAST physical
// time (older than the local clock) must never drag the local clock backward.
// The merged result must dominate the prior local timestamp and a following
// Now() must keep climbing.
//
// Mutation that bites: make Update take the remote physical unconditionally
// (`maxPhys = remotePhys`) -> a past remote yields merged.Physical < prior
// local and the domination assertion fails.
func TestAdv2_PastRemote_NeverRegressesLocalClock(t *testing.T) {
	const localT int64 = 5_000_000_000
	clk := &adv2Clock{ns: localT}
	c := New(clk.now)

	prior := c.Now() // establishes c.last at localT
	past := Timestamp{Physical: 1, Logical: math.MaxUint32}
	merged := c.Update(past)

	if merged.Compare(prior) <= 0 {
		t.Fatalf("past remote regressed/stalled local clock: merged %+v <= prior %+v", merged, prior)
	}
	if merged.Physical < prior.Physical {
		t.Fatalf("past remote dragged physical backward: %d < %d", merged.Physical, prior.Physical)
	}
	next := c.Now()
	if !merged.Less(next) {
		t.Fatalf("Now() after past-remote merge not monotonic: %+v !< %+v", merged, next)
	}
}

// ---------------------------------------------------------------------------
// COMPARE is a TOTAL ORDER, including across the int64 sign boundary and the
// uint32 logical boundary — no wraparound mis-order. The existing suite tests
// domination but never asserts the trichotomy/antisymmetry/transitivity laws
// directly across the negative-physical (high-bit-set) region.
// ---------------------------------------------------------------------------

// TestAdv2_Compare_TotalOrderLaws verifies trichotomy, antisymmetry, and
// transitivity over a value set that straddles the int64 sign boundary and the
// uint32 logical ceiling, ensuring physical is compared as SIGNED int64 (a
// negative physical must order BELOW a positive one) and logical as the
// tie-breaker.
//
// Mutation that bites: compare Physical as unsigned (e.g. via the encoded
// bytes) -> MinInt64 (0x8000...) would sort ABOVE positive values and
// antisymmetry/transitivity break.
func TestAdv2_Compare_TotalOrderLaws(t *testing.T) {
	vals := []Timestamp{
		{Physical: math.MinInt64, Logical: 0},
		{Physical: math.MinInt64, Logical: math.MaxUint32},
		{Physical: -1, Logical: 0},
		{Physical: 0, Logical: 0},
		{Physical: 0, Logical: 1},
		{Physical: 1, Logical: math.MaxUint32},
		{Physical: math.MaxInt64 - 1, Logical: math.MaxUint32},
		{Physical: math.MaxInt64, Logical: 0},
		{Physical: math.MaxInt64, Logical: math.MaxUint32},
	}

	// Trichotomy + antisymmetry.
	for i := range vals {
		for j := range vals {
			cij := vals[i].Compare(vals[j])
			cji := vals[j].Compare(vals[i])
			if cij != -cji {
				t.Fatalf("antisymmetry broken: Compare(%+v,%+v)=%d but reverse=%d", vals[i], vals[j], cij, cji)
			}
			if i == j && cij != 0 {
				t.Fatalf("reflexivity broken: Compare(x,x)=%d for %+v", cij, vals[i])
			}
		}
	}

	// Signed ordering sanity: the negative-physical values must be the smallest.
	if vals[0].Compare(vals[3]) >= 0 {
		t.Fatalf("signed order broken: MinInt64 not < 0 (%+v vs %+v)", vals[0], vals[3])
	}
	if vals[8].Compare(vals[0]) <= 0 {
		t.Fatalf("signed order broken: MaxInt64 not > MinInt64")
	}

	// Transitivity over all ordered triples.
	for i := range vals {
		for j := range vals {
			for k := range vals {
				if vals[i].Compare(vals[j]) < 0 && vals[j].Compare(vals[k]) < 0 {
					if vals[i].Compare(vals[k]) >= 0 {
						t.Fatalf("transitivity broken: %+v < %+v < %+v but first !< last", vals[i], vals[j], vals[k])
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// CONCURRENCY: concurrent Now()/Update() must stay monotone and UNIQUE. Run
// this one package with -race. The existing suite has no concurrency probe that
// asserts global uniqueness of emitted timestamps.
// ---------------------------------------------------------------------------

// TestAdv2_Concurrent_UniqueAndMonotone fires many goroutines doing interleaved
// Now() and Update() against one Clock with a (mostly) frozen physical clock so
// the logical counter is the only thing separating events. EVERY emitted
// timestamp must be globally unique (no two events share a value) — the HLC's
// promise that distinct events get distinct, orderable timestamps.
//
// Mutation that bites: drop the mutex (remove c.mu.Lock/Unlock in Now) -> the
// race detector fires and/or two goroutines read the same c.last and emit a
// duplicate, tripping the uniqueness check.
func TestAdv2_Concurrent_UniqueAndMonotone(t *testing.T) {
	clk := &adv2Clock{ns: 777}
	c := New(clk.now)

	const goroutines = 16
	const perG = 400

	var wg sync.WaitGroup
	results := make([][]Timestamp, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			local := make([]Timestamp, 0, perG)
			for i := 0; i < perG; i++ {
				var ts Timestamp
				if i%3 == 0 {
					// Occasionally feed a remote to exercise Update's path too.
					ts = c.Update(Timestamp{Physical: 777, Logical: uint32(i)})
				} else {
					ts = c.Now()
				}
				local = append(local, ts)
			}
			results[g] = local
		}(g)
	}
	wg.Wait()

	seen := make(map[Timestamp]bool, goroutines*perG)
	for _, batch := range results {
		for _, ts := range batch {
			if seen[ts] {
				t.Fatalf("duplicate timestamp emitted under concurrency: %+v", ts)
			}
			seen[ts] = true
		}
	}
	if len(seen) != goroutines*perG {
		t.Fatalf("expected %d unique timestamps, got %d", goroutines*perG, len(seen))
	}

	// The final Last() must dominate every emitted timestamp (global max).
	last := c.Last()
	for ts := range seen {
		if last.Compare(ts) < 0 {
			t.Fatalf("Last() %+v is below an emitted timestamp %+v", last, ts)
		}
	}
}
