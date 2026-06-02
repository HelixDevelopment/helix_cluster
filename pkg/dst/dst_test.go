package dst

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// pingPongActor is a small REAL distributed actor used as the model under test.
// It is not a mock of the harness logic: it exercises Send, Write, Read, At, Now
// exactly as production code would. Two actors bounce a counter back and forth,
// persisting the highest value each has seen to its simulated disk. The
// invariant we assert across thousands of seeds is: the persisted counter on
// each actor is monotonic non-decreasing and never exceeds the cap.
type pingPongActor struct {
	peer ActorID
	cap  int
}

func (a *pingPongActor) Receive(sim *Simulation, msg Message) {
	var n int
	if _, err := fmt.Sscanf(msg.Body, "%d", &n); err != nil {
		return
	}
	// Persist the value we just observed (sink-side state, asserted later).
	_ = sim.Write(msg.To, "counter", msg.Body)
	if n < a.cap {
		// Bounce an incremented value back to the peer.
		sim.Send(msg.To, a.peer, fmt.Sprintf("%d", n+1))
	}
}

// setupPingPong wires a two-actor ping/pong scenario with a configurable
// network. It is shared by determinism, invariant, and replay tests so the
// scenario is identical across them.
func setupPingPong(sim *Simulation, cap int) {
	sim.SetNetwork(NetworkConfig{
		MinLatency: 1 * time.Millisecond,
		MaxLatency: 5 * time.Millisecond,
	})
	a := ActorID("A")
	b := ActorID("B")
	sim.Register(a, &pingPongActor{peer: b, cap: cap})
	sim.Register(b, &pingPongActor{peer: a, cap: cap})
	// Kick off: deliver "1" to A at t0 via a self-send from a synthetic source.
	sim.Send("seed", a, "1")
}

// TestDeterminism_SameSeed proves CLOSURE #1: the same seed yields a
// byte-for-byte identical trace, and different seeds yield different traces.
//
// MUTATION GUARD: if the scheduler read wall-clock time (time.Now) or used an
// unseeded RNG, or if the (at, seq) tie-break in eventQueue.Less were removed,
// the two same-seed traces below would differ and this test would FAIL.
func TestDeterminism_SameSeed(t *testing.T) {
	const seed = int64(424242)
	const steps = 200

	// Use a dropping, wide-latency network so the RNG stream is exercised on
	// every send; this makes the determinism guarantee non-trivial.
	run := func(s int64) string {
		sim := New(s)
		sim.SetNetwork(NetworkConfig{
			DropProbability: 0.2,
			MinLatency:      1 * time.Millisecond,
			MaxLatency:      9 * time.Millisecond,
		})
		sim.Register("A", &pingPongActor{peer: "B", cap: 50})
		sim.Register("B", &pingPongActor{peer: "A", cap: 50})
		sim.Send("seed", "A", "1")
		sim.Run(steps)
		return sim.TraceString()
	}

	t1 := run(seed)
	t2 := run(seed)
	if t1 != t2 {
		t.Fatalf("same seed produced different traces:\n--- run1 ---\n%s\n--- run2 ---\n%s", t1, t2)
	}
	if t1 == "" {
		t.Fatal("trace was empty; scenario produced no events (test would be vacuous)")
	}

	// Different seed must (very likely) produce a different trace. With drops and
	// a wide latency band this is effectively guaranteed; assert it to prove the
	// RNG actually influences execution (not a constant).
	tDiff := run(seed + 1)
	if tDiff == t1 {
		t.Fatalf("different seed produced an identical trace; RNG is not influencing execution")
	}
}

// TestManySeeds_InvariantHolds proves CLOSURE #2: run >=1000 seeded simulations
// in-process without flakiness and assert an invariant holds across all of them.
//
// Invariant under test: across the whole run, the persisted "counter" value on
// each actor must be monotonic non-decreasing in the order it was written, and
// must never exceed the cap. We reconstruct the write history from the trace and
// verify monotonicity per actor. A buggy actor that wrote a smaller value after
// a larger one (regression) would be caught.
func TestManySeeds_InvariantHolds(t *testing.T) {
	const n = 1500
	const cap = 30
	for seed := int64(0); seed < n; seed++ {
		sim := New(seed)
		setupPingPong(sim, cap)
		sim.Run(5000)

		// Reconstruct per-actor monotonic history from WRITE trace entries.
		last := map[string]int{}
		for _, e := range sim.Trace() {
			if e.Kind != "WRITE" {
				continue
			}
			// WRITE text form: "<actor> counter=<n>"
			var actor string
			var val int
			if _, err := fmt.Sscanf(e.Text, "%s counter=%d", &actor, &val); err != nil {
				t.Fatalf("seed %d: unparsable WRITE entry %q: %v", seed, e.Text, err)
			}
			if val > cap {
				t.Fatalf("seed %d: invariant violated: persisted %d exceeds cap %d", seed, val, cap)
			}
			if prev, ok := last[actor]; ok && val < prev {
				t.Fatalf("seed %d: invariant violated: actor %s counter went backwards %d -> %d", seed, actor, prev, val)
			}
			last[actor] = val
		}
	}
}

// TestManySeeds_BuggyActorCaught proves the OTHER half of CLOSURE #2: a
// deliberately-buggy actor is actually caught by an invariant check (so the
// invariant is load-bearing, not vacuous). The buggy actor decrements instead
// of incrementing under a seed-dependent condition; we assert that at least one
// seed triggers the bug and that our checker flags it.
func TestManySeeds_BuggyActorCaught(t *testing.T) {
	const n = 1000
	caught := 0
	for seed := int64(0); seed < n; seed++ {
		sim := New(seed)
		sim.SetNetwork(NetworkConfig{MinLatency: 1 * time.Millisecond, MaxLatency: 4 * time.Millisecond})
		buggy := HandlerFunc(func(s *Simulation, msg Message) {
			var v int
			if _, err := fmt.Sscanf(msg.Body, "%d", &v); err != nil {
				t.Fatalf("seed %d: unparsable body %q: %v", seed, msg.Body, err)
			}
			// BUG: occasionally regress the persisted value below the max seen.
			next := v + 1
			if s.rng.Float64() < 0.1 {
				next = v - 5 // regression
			}
			_ = s.Write(msg.To, "v", fmt.Sprintf("%d", next))
			if next < 20 && next >= 0 {
				s.Send(msg.To, "X", fmt.Sprintf("%d", next))
			}
		})
		sim.Register("X", buggy)
		sim.Send("seed", "X", "1")
		sim.Run(2000)

		// Checker: did the persisted value ever drop below a previously seen max?
		var prevMax int
		bug := false
		for _, e := range sim.Trace() {
			if e.Kind != "WRITE" {
				continue
			}
			var val int
			if _, err := fmt.Sscanf(e.Text, "X v=%d", &val); err != nil {
				t.Fatalf("seed %d: unparsable WRITE entry %q: %v", seed, e.Text, err)
			}
			if val > prevMax {
				prevMax = val
			}
			if val < prevMax {
				bug = true
				break
			}
		}
		if bug {
			caught++
		}
	}
	if caught == 0 {
		t.Fatal("buggy actor was never caught across 1000 seeds; invariant check is vacuous")
	}
	t.Logf("buggy actor caught under %d/%d seeds", caught, n)
}

// TestReproduceFailure_ByteForByte proves CLOSURE #3: induce a failure under one
// seed, capture the seed+trace, then REPLAY that seed and assert byte-for-byte
// identical trace (the failure reproduces exactly).
//
// We use InjectFaultAtStep to deterministically corrupt an actor's disk at a
// fixed step, causing a downstream "FAULT"/divergent path. The captured trace
// from the first run is compared to a fresh Replay of the same seed.
//
// MUTATION GUARD: any nondeterminism (wall-clock, unseeded RNG, lost tie-break)
// makes original != replay and FAILS this test.
func TestReproduceFailure_ByteForByte(t *testing.T) {
	const seed = int64(987654321)
	const steps = 300

	// setup builds the failing scenario; reused for the capture run and replay.
	setup := func(sim *Simulation) {
		sim.SetNetwork(NetworkConfig{
			DropProbability: 0.15,
			MinLatency:      1 * time.Millisecond,
			MaxLatency:      7 * time.Millisecond,
		})
		sim.SetDisk(DiskConfig{
			FaultProbability: 0.05,
			MinStall:         1 * time.Microsecond,
			MaxStall:         50 * time.Microsecond,
		})
		failing := HandlerFunc(func(s *Simulation, msg Message) {
			var v int
			if _, err := fmt.Sscanf(msg.Body, "%d", &v); err != nil {
				t.Fatalf("unparsable body %q: %v", msg.Body, err)
			}
			if err := s.Write(msg.To, "k", msg.Body); err != nil {
				s.Log(fmt.Sprintf("write-failed v=%d err=%v", v, err))
			}
			if _, _, err := s.Read(msg.To, "k"); err != nil {
				s.Log(fmt.Sprintf("read-failed v=%d", v))
			}
			if v < 40 {
				s.Send(msg.To, peerOf(msg.To), fmt.Sprintf("%d", v+1))
			}
		})
		sim.Register("N1", failing)
		sim.Register("N2", failing)
		sim.Send("seed", "N1", "1")
		// Deterministically inject a fault: wipe N2's disk at step 7.
		sim.InjectFaultAtStep(7, func(s *Simulation) {
			s.Log("INJECTED: wiping N2 disk")
			s.disk["N2"] = map[string]string{}
		})
	}

	// Capture run.
	cap := New(seed)
	setup(cap)
	cap.Run(steps)
	captured := cap.TraceString()
	capturedSeed := cap.Seed()

	if capturedSeed != seed {
		t.Fatalf("Seed() mismatch: got %d want %d", capturedSeed, seed)
	}
	if captured == "" {
		t.Fatal("captured trace empty; scenario did nothing")
	}

	// Sanity: the injected fault must actually appear in the trace, proving the
	// "inject a fault at a deterministic step" feature really fired.
	if !strings.Contains(captured, "INJECTED: wiping N2 disk") {
		t.Fatalf("injected fault did not appear in trace; InjectFaultAtStep not working")
	}

	// Replay from the captured seed with the identical setup.
	replayed := Replay(capturedSeed, steps, setup)

	if captured != replayed {
		t.Fatalf("REPLAY DIVERGED (nondeterminism!):\n--- captured ---\n%s\n--- replayed ---\n%s", captured, replayed)
	}
}

// TestReplay_DifferentSeedDiverges guards against a vacuous replay test: a
// DIFFERENT seed with the same setup must produce a DIFFERENT trace, proving the
// replay equality above is meaningful (it is matching the seed, not matching a
// constant).
func TestReplay_DifferentSeedDiverges(t *testing.T) {
	setup := func(sim *Simulation) {
		sim.SetNetwork(NetworkConfig{DropProbability: 0.3, MinLatency: 1 * time.Millisecond, MaxLatency: 9 * time.Millisecond})
		h := HandlerFunc(func(s *Simulation, msg Message) {
			var v int
			if _, err := fmt.Sscanf(msg.Body, "%d", &v); err != nil {
				t.Fatalf("unparsable body %q: %v", msg.Body, err)
			}
			_ = s.Write(msg.To, "k", msg.Body)
			if v < 50 {
				s.Send(msg.To, peerOf(msg.To), fmt.Sprintf("%d", v+1))
			}
		})
		sim.Register("N1", h)
		sim.Register("N2", h)
		sim.Send("seed", "N1", "1")
	}
	a := Replay(1, 300, setup)
	b := Replay(2, 300, setup)
	if a == b {
		t.Fatal("two different seeds produced identical traces; RNG not load-bearing")
	}
}

// TestLogicalClock_NeverWallClock asserts the clock is purely logical: with a
// fixed latency network, the final logical time is an exact, predictable
// function of the number of hops — independent of how long the test took on the
// wall clock. If the harness consulted time.Now, this exact-equality check would
// fail.
//
// MUTATION GUARD: replacing logical time advancement with wall-clock time breaks
// this exact-equality assertion.
func TestLogicalClock_NeverWallClock(t *testing.T) {
	sim := New(7)
	// Fixed latency (min==max) => deterministic, no RNG latency jitter.
	sim.SetNetwork(NetworkConfig{MinLatency: 2 * time.Millisecond, MaxLatency: 2 * time.Millisecond})
	hops := 0
	h := HandlerFunc(func(s *Simulation, msg Message) {
		hops++
		var v int
		if _, err := fmt.Sscanf(msg.Body, "%d", &v); err != nil {
			t.Fatalf("unparsable body %q: %v", msg.Body, err)
		}
		if v < 5 {
			s.Send(msg.To, peerOf(msg.To), fmt.Sprintf("%d", v+1))
		}
	})
	sim.Register("N1", h)
	sim.Register("N2", h)
	sim.Send("seed", "N1", "1") // first delivery at t=2ms, then +2ms per hop

	steps := sim.Run(100)
	// Deliveries: v=1..5 => 5 deliveries, each 2ms apart starting at 2ms => last
	// at 10ms. Logical time must be EXACTLY 10ms regardless of wall time.
	want := Time(10 * time.Millisecond)
	if sim.Now() != want {
		t.Fatalf("logical clock not exact: got %d ns want %d ns (steps=%d)", int64(sim.Now()), int64(want), steps)
	}
}

// TestDiskFault_TypedError asserts disk faults surface a typed *DiskFaultError
// (honesty: a fault is never a fake success).
func TestDiskFault_TypedError(t *testing.T) {
	sim := New(3)
	sim.SetDisk(DiskConfig{FaultProbability: 1.0}) // always fault
	err := sim.Write("A", "k", "v")
	if err == nil {
		t.Fatal("expected disk fault, got nil")
	}
	var dfe *DiskFaultError
	if !errors.As(err, &dfe) {
		t.Fatalf("expected *DiskFaultError, got %T: %v", err, err)
	}
	if dfe.Op != "write" || dfe.Actor != "A" || dfe.Key != "k" {
		t.Fatalf("unexpected fault fields: %+v", dfe)
	}
	// A faulted write must NOT have persisted anything (sink-side check).
	if _, ok := sim.DiskValue("A", "k"); ok {
		t.Fatal("faulted write must not persist a value")
	}
}

// TestDiskWriteRead_RoundTrip proves the happy-path durable store actually works
// end-to-end (CLAUDE-1 sink-side evidence): a written value is readable and the
// persisted state is exactly what was written.
func TestDiskWriteRead_RoundTrip(t *testing.T) {
	sim := New(11)
	if err := sim.Write("A", "x", "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok, err := sim.Read("A", "x")
	if err != nil || !ok || got != "hello" {
		t.Fatalf("read round-trip failed: got=%q ok=%v err=%v", got, ok, err)
	}
	if v, ok := sim.DiskValue("A", "x"); !ok || v != "hello" {
		t.Fatalf("DiskValue mismatch: %q,%v", v, ok)
	}
	keys := sim.SortedDiskKeys("A")
	if len(keys) != 1 || keys[0] != "x" {
		t.Fatalf("SortedDiskKeys: %v", keys)
	}
}

// assertTraceMonotonic fails t if any TraceEntry.At is strictly less than the
// previous entry's At. This mechanically enforces the package's documented
// "Time is MONOTONIC" invariant for the canonical observable output.
func assertTraceMonotonic(t *testing.T, trace []TraceEntry, ctx string) {
	t.Helper()
	for i := 1; i < len(trace); i++ {
		if trace[i].At < trace[i-1].At {
			t.Fatalf("%s: trace time went BACKWARD at index %d: %s (At=%d) followed %s (At=%d)",
				ctx, i, trace[i].String(), int64(trace[i].At),
				trace[i-1].String(), int64(trace[i-1].At))
		}
	}
}

// TestTraceTime_MonotonicUnderDiskStall is the regression test for the disk-stall
// logical-time bug: a per-actor disk stall must delay only that actor's
// subsequent work and MUST NOT warp the global clock backward, which would make
// the trace timestamps non-monotonic and contradict the documented invariant.
//
// It reproduces the exact reported scenario: a large (100ms) fixed disk stall on
// the actor's Write while two messages are queued for delivery at ~1ms. Before
// the fix, the WRITE/continuation timeline pushed s.now to ~101ms and then the
// loop popped the second 1ms-scheduled delivery and snapped s.now back to 1ms,
// producing a DELIVER at 1ms AFTER an entry at 101ms.
//
// MUTATION GUARD: reinstating `s.now += stall` inside Write/Read (the original
// bug) makes this assertion FAIL. Removing the monotonic clamp in appendTrace
// also makes it FAIL.
func TestTraceTime_MonotonicUnderDiskStall(t *testing.T) {
	sim := New(99)
	// Deliver both messages to S at ~1ms (fixed latency so both land at 1ms).
	sim.SetNetwork(NetworkConfig{MinLatency: 1 * time.Millisecond, MaxLatency: 1 * time.Millisecond})
	// A large, fixed (min==max) disk stall: every Write costs exactly 100ms of
	// the stalling actor's local time.
	sim.SetDisk(DiskConfig{MinStall: 100 * time.Millisecond, MaxStall: 100 * time.Millisecond})

	// Actor S persists each delivered body. With two messages queued at 1ms, the
	// first handler's 100ms stall must NOT cause the second delivery (still at
	// 1ms) to appear earlier in time than the first WRITE.
	sim.Register("S", HandlerFunc(func(s *Simulation, msg Message) {
		if err := s.Write(msg.To, "k", msg.Body); err != nil {
			t.Fatalf("write: %v", err)
		}
	}))
	// Two independent sends to S; both delivered at logical t=1ms (seq tie-break).
	sim.Send("src", "S", "a")
	sim.Send("src", "S", "b")

	sim.Run(100)

	trace := sim.Trace()
	if len(trace) == 0 {
		t.Fatal("empty trace; scenario produced no events")
	}
	assertTraceMonotonic(t, trace, "disk-stall scenario")

	// Sink-side: the second WRITE must be timestamped at or after the first
	// (proving the stall delayed S's own subsequent work rather than warping the
	// clock backward), and both deliveries must have actually fired.
	var writes []TraceEntry
	for _, e := range trace {
		if e.Kind == "WRITE" {
			writes = append(writes, e)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("expected 2 WRITE entries, got %d: %v", len(writes), writes)
	}
	if writes[1].At < writes[0].At {
		t.Fatalf("second WRITE earlier than first: %d < %d", int64(writes[1].At), int64(writes[0].At))
	}
}

// TestTraceTime_MonotonicAcrossManySeeds asserts the monotonicity invariant holds
// across the same broad seed space (with faults, stalls, drops, jittered latency)
// used by the determinism/invariant tests — not just the one crafted scenario.
//
// MUTATION GUARD: the original `s.now += stall` warp would fail this under at
// least one seed because a stalled handler's later sends/writes interleave with
// other actors' earlier-scheduled deliveries.
func TestTraceTime_MonotonicAcrossManySeeds(t *testing.T) {
	const n = 500
	for seed := int64(0); seed < n; seed++ {
		sim := New(seed)
		sim.SetNetwork(NetworkConfig{
			DropProbability: 0.1,
			MinLatency:      1 * time.Millisecond,
			MaxLatency:      9 * time.Millisecond,
		})
		sim.SetDisk(DiskConfig{
			FaultProbability: 0.02,
			MinStall:         10 * time.Microsecond,
			MaxStall:         5 * time.Millisecond,
		})
		setupPingPong(sim, 30)
		sim.Run(5000)
		assertTraceMonotonic(t, sim.Trace(), fmt.Sprintf("seed %d", seed))
	}
}

// --- small test helpers ---

func peerOf(a ActorID) ActorID {
	if a == "N1" {
		return "N2"
	}
	return "N1"
}
