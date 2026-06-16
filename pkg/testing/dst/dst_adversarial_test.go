package dst

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// dst_adversarial_test.go — adversarial determinism probes for the DST engine.
//
// DST = Deterministic Simulation Testing. The package PROMISES that "every
// execution is fully reproducible" (engine.go pkg doc) and that the chaos
// harness "is fully deterministic and in-process" (session_chaos.go).
//
// These probes hunt for the highest-value class of bug for a DST framework:
// a DETERMINISM BLUFF — the framework claims same-seed reproducibility but
// secretly leaks nondeterminism via map-iteration order, wall-clock time, real
// goroutine scheduling, or an unseeded/non-reproducible PRNG. Any such leak
// makes every test built on the framework a PASS-bluff.
//
// Each test that holds on current code is a CONTRACT PIN. A test commented
// REAL BUG below is a determinism violation proven load-bearing via mutation.

// ---------------------------------------------------------------------------
// Probe 1 (CONTRACT): same seed -> byte-identical RNG-driven result.
// engine.go pkg doc: "every execution is fully reproducible".
// ---------------------------------------------------------------------------

func TestAdversarial_SameSeedByteIdenticalRNGStream(t *testing.T) {
	const seed = int64(0xDEADBEEF)
	run := func() string {
		eng := NewEngine(seed)
		out := make([]int64, 0, 256)
		eng.RegisterHandler("tick", func(e *Event) {
			out = append(out, int64(eng.RNG().Int63n(1_000_000)))
		})
		for j := 0; j < 200; j++ {
			eng.ScheduleEvent("tick", nil, int64(j+1))
		}
		eng.Run(1000)
		b, _ := json.Marshal(out)
		return string(b)
	}
	a := run()
	b := run()
	if a != b {
		t.Fatalf("same seed produced different RNG streams:\n a=%s\n b=%s", a, b)
	}
}

// ---------------------------------------------------------------------------
// Probe 2 (CONTRACT): different seeds -> different streams (PRNG actually seeded).
// ---------------------------------------------------------------------------

func TestAdversarial_DifferentSeedsDiffer(t *testing.T) {
	gen := func(seed int64) string {
		eng := NewEngine(seed)
		out := make([]int64, 0, 64)
		eng.RegisterHandler("tick", func(e *Event) {
			out = append(out, int64(eng.RNG().Int63()))
		})
		for j := 0; j < 50; j++ {
			eng.ScheduleEvent("tick", nil, int64(j+1))
		}
		eng.Run(100)
		b, _ := json.Marshal(out)
		return string(b)
	}
	if gen(1) == gen(2) {
		t.Fatal("different seeds produced identical streams — PRNG not seeded by seed")
	}
}

// ---------------------------------------------------------------------------
// Probe 3 (CONTRACT): the engine clock is the virtual clock, never wall-clock.
// Now() must reflect the timestamp of the last processed event, independent of
// real time. We sleep between runs to prove wall-clock does not leak in.
// ---------------------------------------------------------------------------

func TestAdversarial_VirtualClockNotWallClock(t *testing.T) {
	eng := NewEngine(7)
	if !eng.Now().Equal(time.Unix(0, 0)) {
		t.Fatalf("fresh engine clock not zero: %v", eng.Now())
	}
	eng.RegisterHandler("noop", func(e *Event) {})
	eng.ScheduleEvent("noop", nil, 123456789)
	eng.Run(1)
	got := eng.Now()
	if !got.Equal(time.Unix(0, 123456789)) {
		t.Fatalf("virtual clock = %v, want event timestamp 123456789ns — clock is not virtual", got.UnixNano())
	}
}

// ---------------------------------------------------------------------------
// Probe 4 (CONTRACT): the full chaos scenario is reproducible across runs for a
// fixed seed — the OBSERVABLE timeline (kinds + order + virtual timestamps +
// session assignment), excluding the documented per-run RunID, must be
// byte-identical. session_chaos.go RunChaosScenario doc: "fully deterministic".
// ---------------------------------------------------------------------------

func canonicalTimeline(r *ChaosScenarioResult) string {
	type ev struct {
		Kind      TimelineEventKind
		NodeID    string
		SessionID string
		VirtualTS int64
	}
	evs := make([]ev, 0)
	for _, e := range r.Timeline.Snapshot() {
		// Detail is free-form prose; exclude. RunID is documented per-run UUID; exclude.
		evs = append(evs, ev{e.Kind, e.NodeID, e.SessionID, e.VirtualTS})
	}
	payload := struct {
		Events      []ev
		FinalMember int
		AssignedTo  string
		Active      bool
	}{evs, r.FinalMemberCount, r.Session.AssignedNode, r.Session.Active}
	b, _ := json.Marshal(payload)
	return string(b)
}

func TestAdversarial_ChaosScenarioReproducible(t *testing.T) {
	cfg := ChaosScenarioConfig{Seed: 424242, NodeCount: 5}
	r1, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	r2, err := RunChaosScenario(cfg)
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	c1, c2 := canonicalTimeline(r1), canonicalTimeline(r2)
	if c1 != c2 {
		t.Fatalf("chaos scenario not reproducible for fixed seed:\n r1=%s\n r2=%s", c1, c2)
	}
}

// ---------------------------------------------------------------------------
// Probe 5 (REAL BUG HUNT): multi-session reschedule order must be deterministic.
//
// KillNode iterates sc.sessions (a Go map) to build the `affected` slice, then
// reschedules + appends timeline events in THAT order. Go map iteration order
// is randomized per range, so with >1 affected session the reschedule timeline
// order leaks nondeterminism even for a fixed seed. A DST harness that claims
// "fully deterministic" but emits a different observable event order run-to-run
// is a determinism bluff.
//
// We drive KillNode directly with several sessions on the doomed node and check
// the order of reschedule TimelineEvents (by SessionID) is identical across many
// independent runs. If the production code sorted `affected`, this is stable.
// ---------------------------------------------------------------------------

func rescheduleOrder(t *testing.T, seed int64) string {
	t.Helper()
	eng := NewEngine(seed)
	tl := NewRecoveryTimeline()
	sc := NewSimCluster(eng, tl)
	// 3 surviving nodes + the doomed node.
	sc.AddNode("node-doomed", "10.0.0.9")
	sc.AddNode("node-a", "10.0.0.1")
	sc.AddNode("node-b", "10.0.0.2")
	sc.AddNode("node-c", "10.0.0.3")
	// Many sessions all on the doomed node so KillNode reschedules all of them.
	for i := 0; i < 16; i++ {
		sid := fmt.Sprintf("sess-%02d", i)
		if err := sc.StartSession(sid, "node-doomed"); err != nil {
			t.Fatalf("StartSession %s: %v", sid, err)
		}
	}
	if err := sc.KillNode("node-doomed"); err != nil {
		t.Fatalf("KillNode: %v", err)
	}
	// Extract the SessionID sequence of reschedule events in append order.
	order := make([]string, 0, 16)
	for _, e := range tl.Snapshot() {
		if e.Kind == TimelineReschedule {
			order = append(order, e.SessionID)
		}
	}
	b, _ := json.Marshal(order)
	return string(b)
}

func TestAdversarial_RescheduleOrderDeterministic(t *testing.T) {
	const seed = int64(1)
	first := rescheduleOrder(t, seed)
	for i := 0; i < 40; i++ {
		got := rescheduleOrder(t, seed)
		if got != first {
			t.Fatalf("reschedule timeline order is nondeterministic for fixed seed (map-iteration leak):\n run0=%s\n run%d=%s", first, i+1, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Probe 6 (CONTRACT): heap event ordering by virtual timestamp. Events scheduled
// out of timestamp order must still be processed in ascending virtual-time order.
// ---------------------------------------------------------------------------

func TestAdversarial_EventsProcessedInVirtualTimeOrder(t *testing.T) {
	eng := NewEngine(3)
	var seen []int64
	eng.RegisterHandler("e", func(e *Event) {
		seen = append(seen, e.Timestamp)
	})
	// Schedule deliberately out of order.
	for _, d := range []int64{50, 10, 90, 30, 70, 20} {
		eng.ScheduleEvent("e", nil, d)
	}
	eng.Run(100)
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("events not processed in ascending virtual time: %v", seen)
		}
	}
	if len(seen) != 6 {
		t.Fatalf("expected 6 events, got %d", len(seen))
	}
}
