package federation

// federation_adversarial_test.go — adversarial / sink-side probes for the
// federation trust-and-replica boundary surfaces.
//
// Probed vectors (per the recurring bug classes):
//   (a) TOTAL-ORDER AT A REPLICA/IDENTITY EDGE — CrossCellSuspicion.Observe
//       resolves (incarnation, state) conflicts; the relay (cross-cell sink) is
//       the authoritative downstream. We probe whether the order in which the
//       relay observes forwards matches the order in which the local table
//       commits them when two goroutines race on the SAME (cell,member) key.
//   (b) UNGUARDED SHARED MUTABLE STATE — concurrent Observe/State on one table
//       and concurrent registry mutation, under -race, many goroutines.
//   (c) NUMERIC POISON — there is no float score on this surface (incarnation is
//       uint32, state is an enum). Documented below; no float guard to poison.
//   (d) FAIL-OPEN / HOSTILE INPUT — malformed SuspicionEvent admission; duplicate
//       cell-id registration; equal-incarnation tiebreak determinism.
//
// Every assertion below is SINK-SIDE: it asserts the actual resolved
// (state, incarnation) the table reports, the actual sequence the relay received,
// or the actual stored cell — never merely "no panic".

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// recordingRelay records, in order, every event Forward observed. It is itself
// mutex-guarded so the test harness does not introduce its own race.
type recordingRelay struct {
	mu      sync.Mutex
	events  []SuspicionEvent
	failNext bool
}

func (r *recordingRelay) Forward(_ context.Context, ev SuspicionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		r.events = append(r.events, ev)
		return fmt.Errorf("recordingRelay: injected transport failure")
	}
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingRelay) snapshot() []SuspicionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SuspicionEvent, len(r.events))
	copy(out, r.events)
	return out
}

// ---------------------------------------------------------------------------
// (a) TOTAL-ORDER AT A REPLICA/IDENTITY EDGE
// ---------------------------------------------------------------------------

// TestAdversarial_EqualIncarnationTiebreakIsDeterministic pins the documented
// equal-incarnation rule: state may only STRICTLY advance
// Alive(0) < Suspect(1) < Dead(2) < Left(3); a lower-or-equal state at the same
// incarnation is a no-op. Because the state enum is a TOTAL ORDER, the winner of
// an equal-incarnation conflict is deterministic regardless of arrival order.
//
// This is the contract-pinning counterpart: it proves there is NO nondeterministic
// equal-version tiebreak on this surface (unlike the antientropy/offlinesync bugs).
func TestAdversarial_EqualIncarnationTiebreakIsDeterministic(t *testing.T) {
	// Two orders of the SAME pair of equal-incarnation observations must resolve
	// to the SAME final state (the strictly-greater one), proving order-independence.
	resolve := func(first, second swim.MemberState) (swim.MemberState, uint32) {
		rel := &recordingRelay{}
		c := NewCrossCellSuspicion("cellA", rel)
		_, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: first, Incarnation: 7})
		_, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: second, Incarnation: 7})
		st, inc, _ := c.State("cellA", "m1")
		return st, inc
	}

	// Suspect then Dead, vs Dead then Suspect — both must land on Dead@7.
	st1, inc1 := resolve(swim.StateSuspect, swim.StateDead)
	st2, inc2 := resolve(swim.StateDead, swim.StateSuspect)

	if st1 != swim.StateDead || inc1 != 7 {
		t.Fatalf("Suspect-then-Dead resolved to (%v,%d), want (Dead,7)", st1, inc1)
	}
	if st2 != swim.StateDead || inc2 != 7 {
		t.Fatalf("Dead-then-Suspect resolved to (%v,%d), want (Dead,7) — order-dependent tiebreak!", st2, inc2)
	}
	if st1 != st2 {
		t.Fatalf("equal-incarnation resolution is ORDER-DEPENDENT: %v vs %v", st1, st2)
	}
}

// TestAdversarial_ConcurrentSameKeyRelayOrderVsTableOrder probes the trust
// boundary directly. Observe commits the local table UNDER the lock but calls
// relay.Forward AFTER releasing the lock (suspicion.go: "Forward ... outside the
// lock"). When two goroutines race on the SAME (cell,member) key with two
// strictly-advancing incarnations, the relay (the cross-cell SINK, the
// authoritative downstream) can observe the forwards in an order that disagrees
// with the order the local table committed them.
//
// SINK-SIDE INVARIANT under test: the relay must NEVER observe a strictly-older
// (lower-incarnation) accepted event AFTER a strictly-newer one for the same key.
// If it does, a downstream cell that trusts arrival order would regress to a
// stale state even though THIS cell's table is correct.
//
// NOTE: this reproducer is reported, not env-gated. If it FAILS it documents a
// real reorder-at-the-sink window; if it PASSES across many iterations the
// window is not observable with this harness (latent at worst). Read the final
// report for the three-way discrimination.
func TestAdversarial_ConcurrentSameKeyRelayOrderVsTableOrder(t *testing.T) {
	const iterations = 400

	for it := 0; it < iterations; it++ {
		rel := &recordingRelay{}
		c := NewCrossCellSuspicion("cellA", rel)

		// Seed so both racing observations are strictly-advancing accepts.
		_, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: swim.StateAlive, Incarnation: 1})

		var wg sync.WaitGroup
		wg.Add(2)
		// G1: incarnation 2 (Suspect). G2: incarnation 3 (Dead). Both accept.
		go func() {
			defer wg.Done()
			_, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: swim.StateSuspect, Incarnation: 2})
		}()
		go func() {
			defer wg.Done()
			_, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: swim.StateDead, Incarnation: 3})
		}()
		wg.Wait()

		// Local table must always converge to the highest incarnation. (This part
		// is protected by the lock and should always hold.)
		st, inc, _ := c.State("cellA", "m1")
		if inc != 3 || st != swim.StateDead {
			t.Fatalf("iter %d: local table did not converge to highest incarnation: got (%v,%d)", it, st, inc)
		}

		// SINK-SIDE: examine the order the relay (downstream cell) observed the two
		// racing forwards. The seed (inc=1) is always first. Among inc=2 and inc=3,
		// if the relay saw inc=3 BEFORE inc=2, a downstream that trusts arrival
		// order would accept Dead@3 then be asked to regress to Suspect@2.
		//
		// IMPORTANT: if inc=3 commits to the table FIRST, then inc=2 sees a newer
		// incarnation and is correctly DROPPED as stale (never forwarded). That is
		// correct behaviour, not a reorder. The reorder bug only manifests when
		// BOTH were accepted (both appear in the relay log) yet the relay observed
		// them newest-first. So we only assert the invariant in that case.
		evs := rel.snapshot()
		var posInc2, posInc3 = -1, -1
		for i, e := range evs {
			if e.Incarnation == 2 {
				posInc2 = i
			}
			if e.Incarnation == 3 {
				posInc3 = i
			}
		}
		// inc=3 (the highest) must ALWAYS be forwarded (it always wins the table).
		if posInc3 == -1 {
			t.Fatalf("iter %d: highest incarnation inc=3 was never forwarded: events=%+v", it, evs)
		}
		// Only when inc=2 was ALSO accepted+forwarded do we have two ordered
		// forwards to compare. If present, the relay must have seen them oldest-first.
		if posInc2 != -1 && posInc3 < posInc2 {
			t.Fatalf("iter %d: SINK REORDER — relay observed newer inc=3 (Dead) at pos %d BEFORE older inc=2 (Suspect) at pos %d; "+
				"both were accepted yet forwarded newest-first. A downstream cell trusting arrival order would regress. "+
				"forward happens outside the lock in Observe.",
				it, posInc3, posInc2)
		}
	}
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED MUTABLE STATE
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentObserveState hammers one CrossCellSuspicion table
// with many writers (Observe) and readers (State) across many keys. Run under
// -race: the members map must be fully mutex-guarded. Sink-side: every key must
// end at the highest incarnation observed for it.
func TestAdversarial_ConcurrentObserveState(t *testing.T) {
	rel := &recordingRelay{}
	c := NewCrossCellSuspicion("cellA", rel)

	const keys = 32
	const writersPerKey = 8
	const maxInc = 50

	var wg sync.WaitGroup
	for k := 0; k < keys; k++ {
		mid := fmt.Sprintf("m%d", k)
		for w := 0; w < writersPerKey; w++ {
			wg.Add(1)
			go func(mid string, base int) {
				defer wg.Done()
				for inc := uint32(base + 1); inc <= maxInc; inc += uint32(writersPerKey) {
					_, _ = c.Observe(context.Background(), SuspicionEvent{
						CellID: "cellA", MemberID: mid, State: swim.StateSuspect, Incarnation: inc,
					})
				}
			}(mid, w)
		}
		// Concurrent readers.
		wg.Add(1)
		go func(mid string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _, _ = c.State("cellA", mid)
			}
		}(mid)
	}
	wg.Wait()

	for k := 0; k < keys; k++ {
		mid := fmt.Sprintf("m%d", k)
		_, inc, known := c.State("cellA", mid)
		if !known {
			t.Fatalf("key %s never recorded", mid)
		}
		if inc != maxInc {
			t.Fatalf("key %s converged to inc=%d, want highest %d (lost update under concurrency)", mid, inc, maxInc)
		}
	}
}

// TestAdversarial_RegistryConcurrentMutationRace runs Register / Deregister /
// Advance / SetHealth / List / Lookup concurrently on one registry. Under -race
// this proves the registry's RWMutex actually guards the cells map and listeners.
func TestAdversarial_RegistryConcurrentMutationRace(t *testing.T) {
	r := NewCellRegistry()

	// Listener that appends under its own lock — exercises the post-unlock fan-out.
	var lmu sync.Mutex
	var fired int
	r.OnTransition(func(TransitionEvent) {
		lmu.Lock()
		fired++
		lmu.Unlock()
	})

	const ids = 24
	var wg sync.WaitGroup
	for i := 0; i < ids; i++ {
		id := fmt.Sprintf("cell-%d", i)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			cell, _ := NewCell(id)
			_ = r.Register(cell)
			_ = r.Advance(id, PhaseActive)
			_ = r.SetHealth(id, HealthHealthy, 3)
			_ = r.Advance(id, PhaseDraining)
			_ = r.Advance(id, PhaseLeft)
			_ = r.Deregister(id)
		}(id)
	}
	// Concurrent readers.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.List()
				_ = r.Len()
			}
		}()
	}
	wg.Wait()
	// Sink-side: after every cell deregistered, the registry must be empty.
	if got := r.Len(); got != 0 {
		t.Fatalf("registry not drained: Len=%d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// (c) NUMERIC POISON — documented absence
// ---------------------------------------------------------------------------

// TestAdversarial_NoFloatScoreSurface documents that the federation conflict-
// resolution surface carries NO float score/weight: ordering is by uint32
// incarnation and a totally-ordered state enum. There is therefore no NaN/Inf
// poison vector here (unlike a float-ranked surface). This is a pinning test,
// not a bug reproducer.
func TestAdversarial_NoFloatScoreSurface(t *testing.T) {
	// Max incarnation must still be accepted and reported exactly (no float
	// rounding / overflow-to-NaN possible on a uint32 path).
	rel := &recordingRelay{}
	c := NewCrossCellSuspicion("cellA", rel)
	const maxU32 = ^uint32(0)
	ok, err := c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: swim.StateLeft, Incarnation: maxU32})
	if err != nil || !ok {
		t.Fatalf("max-incarnation observe rejected: ok=%v err=%v", ok, err)
	}
	_, inc, _ := c.State("cellA", "m1")
	if inc != maxU32 {
		t.Fatalf("max incarnation not preserved: got %d want %d", inc, maxU32)
	}
	// A subsequent lower incarnation must be a no-op (stale), never wrap.
	ok, _ = c.Observe(context.Background(), SuspicionEvent{CellID: "cellA", MemberID: "m1", State: swim.StateAlive, Incarnation: 0})
	if ok {
		t.Fatalf("incarnation=0 after max must be stale no-op, was accepted")
	}
}

// ---------------------------------------------------------------------------
// (d) FAIL-OPEN / HOSTILE INPUT
// ---------------------------------------------------------------------------

// TestAdversarial_MalformedEventRejected proves Observe rejects (does NOT admit)
// events with empty CellID or MemberID — the documented "zero value is invalid"
// contract. A fail-OPEN here would silently create a phantom member entry.
func TestAdversarial_MalformedEventRejected(t *testing.T) {
	rel := &recordingRelay{}
	c := NewCrossCellSuspicion("cellA", rel)

	cases := []SuspicionEvent{
		{CellID: "", MemberID: "m1", State: swim.StateSuspect, Incarnation: 1},
		{CellID: "cellA", MemberID: "", State: swim.StateSuspect, Incarnation: 1},
		{CellID: "", MemberID: "", State: swim.StateDead, Incarnation: 9},
	}
	for i, ev := range cases {
		ok, err := c.Observe(context.Background(), ev)
		if ok || err == nil {
			t.Fatalf("case %d: malformed event admitted (ok=%v err=%v) — FAIL-OPEN", i, ok, err)
		}
	}
	// Sink-side: the relay was never called for a rejected event, and no phantom
	// entry exists.
	if got := rel.snapshot(); len(got) != 0 {
		t.Fatalf("relay forwarded a rejected event: %+v", got)
	}
	if _, _, known := c.State("", "m1"); known {
		t.Fatalf("phantom entry created for empty CellID")
	}
}

// TestAdversarial_DuplicateCellIDRejected proves the registry treats a cell id as
// a unique identity edge: a second Register of the same id is rejected and does
// NOT overwrite the stored cell (which would silently swap a cell's endpoint /
// reset its lifecycle — an identity-collision fail-open).
func TestAdversarial_DuplicateCellIDRejected(t *testing.T) {
	r := NewCellRegistry()
	c1, _ := NewCell("dup", WithEndpoint("ep-1"))
	if err := r.Register(c1); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	// Advance the stored cell so we can detect a silent reset.
	if err := r.Advance("dup", PhaseActive); err != nil {
		t.Fatalf("advance failed: %v", err)
	}

	c2, _ := NewCell("dup", WithEndpoint("ep-2"))
	if err := r.Register(c2); err != ErrCellExists {
		t.Fatalf("duplicate register returned %v, want ErrCellExists", err)
	}
	// Sink-side: the original cell is intact — endpoint ep-1, phase Active — not
	// silently replaced by the ep-2 / Joining impostor.
	got, ok := r.Lookup("dup")
	if !ok {
		t.Fatalf("cell vanished after duplicate register")
	}
	if got.Endpoint != "ep-1" {
		t.Fatalf("duplicate register overwrote endpoint: got %q want ep-1", got.Endpoint)
	}
	if got.Phase() != PhaseActive {
		t.Fatalf("duplicate register reset lifecycle: phase=%v want Active", got.Phase())
	}
}

// TestAdversarial_AggregateDeterministicCellIDOrder pins that the aggregator
// merges Items in cell-id order regardless of which goroutine finished first —
// the identity-edge total order on the fan-out side.
func TestAdversarial_AggregateDeterministicCellIDOrder(t *testing.T) {
	r := NewCellRegistry()
	for _, id := range []string{"zeta", "alpha", "mike"} {
		cell, _ := NewCell(id)
		if err := r.Register(cell); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	client := orderedFakeClient{}
	agg := NewAggregator(r, client)

	// Run several times; merged order must be byte-identical every time.
	var prev []any
	for run := 0; run < 20; run++ {
		out := agg.AllCells(context.Background(), nil)
		if !out.OK() {
			t.Fatalf("run %d: unexpected errors %+v", run, out.Errors)
		}
		if prev != nil && fmt.Sprint(prev) != fmt.Sprint(out.Items) {
			t.Fatalf("run %d: nondeterministic merge order: %v vs %v", run, prev, out.Items)
		}
		prev = out.Items
	}
	// Sink-side: items must be in cell-id-sorted order: alpha, mike, zeta.
	want := []any{"alpha-item", "mike-item", "zeta-item"}
	if fmt.Sprint(prev) != fmt.Sprint(want) {
		t.Fatalf("merge not cell-id ordered: got %v want %v", prev, want)
	}
}

// orderedFakeClient returns one item per cell tagged with the cell id.
type orderedFakeClient struct{}

func (orderedFakeClient) Query(_ context.Context, cell Cell, _ any) (CellResult, error) {
	return CellResult{Items: []any{cell.ID + "-item"}}, nil
}

// helper so a sorted []string compare reads clearly if extended later.
var _ = sort.Strings
