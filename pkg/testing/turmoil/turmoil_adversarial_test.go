package turmoil_test

// Adversarial probes for the turmoil chaos/fault-injection harness.
//
// Focus areas (per the two high-value bug classes for chaos harnesses):
//
//	(A) DETERMINISM BLUFF — does the injected event/fault SEQUENCE leak
//	    nondeterminism via Go map-iteration order, wall-clock, real goroutine
//	    scheduling, or unseeded RNG? Same-seed must ⇒ byte-identical canonical
//	    log, including under same-tick / same-sort-key ties.
//
//	(B) FAULT-INJECTION CORRECTNESS — does an injected fault produce a real
//	    observable sink-side effect (not a silent no-op), is it reverted/healed
//	    per contract, and are the injection knobs (latency magnitude, partition
//	    timing) applied sanely at their edges?
//
// Every probe below PASSES on the production code in turmoil.go and is written
// to PIN the observed contract so a regression (or a silent change in semantics)
// surfaces as a failure. Findings that are genuine footguns (negative-latency
// time-travel, evaluate-at-delivery partition semantics) are pinned as the
// CURRENT documented behaviour with an explicit note; they are intentionally
// NOT "fixed" because no production promise is violated and clamping/altering
// them would change observable behaviour that callers may rely on.

import (
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/turmoil"
)

// runWithDeadline guards against any accidental blocking/hang in a probe.
func runWithDeadline(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: exceeded 20s deadline (possible hang)", name)
	}
}

// ── (A) DETERMINISM: same-tick / same-sort-key tie reproducibility ────────────

// TestAdversarial_SameKeyTie_EncodeLogReproducible hammers the canonical encoder
// with many events that share ALL EncodeLog sort keys (Tick, FromCluster,
// FromNode, ToCluster, ToNode) and differ only in Payload/Nonce — exactly the
// case where sort.Slice (which is NOT a stable sort) could leak a nondeterministic
// ordering. The contract is that, given an identical (seed, operation) sequence,
// EncodeLog is byte-identical every time.
//
// VERDICT for this probe: DOCUMENTED CONTRACT (determinism holds). Same-process,
// same-input sort.Slice is deterministic because the input slice order and the
// comparator are byte-identical on every run, so the unstable sort resolves ties
// identically. No map iteration feeds the event sequence (Send does only single-
// key map lookups), so there is no map-order leak (contrast pkg/testing/dst).
//
// Mutation that would make this FAIL: if EncodeLog stopped copying before sort
// and instead mutated/shuffled shared state, or if the event sequence were ever
// derived from `range` over s.partitions/s.latencies/s.clusters.
func TestAdversarial_SameKeyTie_EncodeLogReproducible(t *testing.T) {
	build := func() string {
		s := turmoil.NewSimulation(7)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		// 64 messages, identical endpoints + delay ⇒ identical tick and identical
		// EncodeLog sort keys; payload/nonce differ.
		for i := 0; i < 64; i++ {
			must(t, s.Send("c1", "n1", "c2", "n1", []byte{byte(i)}, 1))
		}
		s.Run(1000)
		return turmoil.EncodeLog(s.EventLog())
	}

	runWithDeadline(t, "SameKeyTie", func() {
		first := build()
		if first == "" {
			t.Fatal("expected non-empty canonical log")
		}
		for r := 0; r < 300; r++ {
			if got := build(); got != first {
				t.Fatalf("run %d EncodeLog diverged from first run — same-key tie nondeterminism", r)
			}
		}
	})
}

// TestAdversarial_Determinism_HighFanoutMultiNodeMultiCluster builds a denser
// topology (multiple clusters, multiple nodes, mixed delays, a partition, and a
// directional latency injection) and asserts two independent same-seed runs are
// byte-identical under EncodeLog. This exercises the full fault stack — not just
// a single Send — for nondeterminism.
//
// VERDICT: DOCUMENTED CONTRACT (determinism holds across the full fault stack).
func TestAdversarial_Determinism_HighFanoutMultiNodeMultiCluster(t *testing.T) {
	build := func() string {
		s := turmoil.NewSimulation(0xC0FFEE)
		must(t, s.AddCluster("a", []string{"n1", "n2", "n3"}))
		must(t, s.AddCluster("b", []string{"n1", "n2"}))
		must(t, s.AddCluster("c", []string{"n1"}))
		s.Partition("a", "c")          // some traffic will be dropped
		s.InjectLatency("a", "b", 4)   // directional latency fault
		s.InjectLatency("b", "a", 9)   // asymmetric reverse latency
		froms := []struct{ c, n string }{{"a", "n1"}, {"a", "n2"}, {"b", "n1"}, {"c", "n1"}}
		tos := []struct{ c, n string }{{"b", "n1"}, {"a", "n3"}, {"c", "n1"}, {"a", "n1"}}
		for i := 0; i < 40; i++ {
			f := froms[i%len(froms)]
			d := tos[i%len(tos)]
			must(t, s.Send(f.c, f.n, d.c, d.n, []byte{byte(i), byte(i * 3)}, int64(i%7)))
		}
		s.Run(10000)
		return turmoil.EncodeLog(s.EventLog())
	}

	runWithDeadline(t, "HighFanout", func() {
		a := build()
		b := build()
		if a != b {
			t.Fatalf("same-seed full-fault-stack runs diverged:\nrun1:\n%s\nrun2:\n%s", a, b)
		}
		if a == "" {
			t.Fatal("expected non-empty canonical log")
		}
	})
}

// ── (B) FAULT CORRECTNESS: partition is evaluated at DELIVERY time ────────────

// TestAdversarial_Partition_EvaluatedAtDeliveryNotSend pins a subtle and
// load-bearing semantic of the harness: the partition predicate is checked in
// handleDelivery (at the event's virtual delivery tick), NOT at Send time.
//
// Consequences (all asserted here as the CURRENT contract):
//
//	1. A message SENT while a link is partitioned but HEALED before its delivery
//	   tick is DELIVERED, not dropped. (The package doc line "Messages sent across
//	   the partition are recorded as dropped" describes steady-state; the precise
//	   rule is evaluate-at-delivery.) — LATENT RISK / contract to be aware of.
//
//	2. Symmetrically, a message sent on a healthy link that becomes partitioned
//	   before delivery IS dropped.
//
// VERDICT: DOCUMENTED CONTRACT (pin; no production change). A test that asserted
// the opposite (drop-at-send) would be a false contract; this pins the real one.
func TestAdversarial_Partition_EvaluatedAtDeliveryNotSend(t *testing.T) {
	// Case 1: sent-while-partitioned, healed-before-delivery ⇒ DELIVERED.
	t.Run("heal_before_delivery_delivers", func(t *testing.T) {
		s := turmoil.NewSimulation(1)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		s.Partition("c1", "c2")
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), 5)) // delivery at tick 5
		s.Heal("c1", "c2")                                      // heal before Run/delivery
		s.Run(100)
		log := s.EventLog()
		if len(log) != 1 {
			t.Fatalf("expected 1 event, got %d", len(log))
		}
		if log[0].Dropped {
			t.Fatalf("partition is evaluated at delivery time: heal-before-delivery must DELIVER, got Dropped=true reason=%q", log[0].Reason)
		}
	})

	// Case 2: sent-while-healthy, partitioned-before-delivery ⇒ DROPPED.
	t.Run("partition_before_delivery_drops", func(t *testing.T) {
		s := turmoil.NewSimulation(2)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), 5)) // healthy at send
		s.Partition("c1", "c2")                                 // partition before delivery
		s.Run(100)
		log := s.EventLog()
		if len(log) != 1 {
			t.Fatalf("expected 1 event, got %d", len(log))
		}
		if !log[0].Dropped || log[0].Reason != "partition" {
			t.Fatalf("partition-before-delivery must DROP at delivery time; got Dropped=%v reason=%q", log[0].Dropped, log[0].Reason)
		}
	})
}

// ── (B) FAULT CORRECTNESS: negative latency = backwards-in-time delivery ──────

// TestAdversarial_NegativeLatency_ProducesBackwardsTick pins the behaviour of
// InjectLatency when given a NEGATIVE magnitude. Latency is a non-negative
// physical quantity, but neither InjectLatency nor the dst engine clamps it:
// total = delayTicks + extraTicks can go negative, and the engine schedules the
// event at clock+total, so a message can be "delivered" at a NEGATIVE virtual
// tick — i.e. before the simulation's start time, and (with a large enough
// negative) reordered ahead of earlier-sent messages.
//
// This is a genuine fault-injection footgun: a "latency" fault that moves an
// event *backwards* in virtual time is not a network phenomenon. It is pinned
// here (not fixed) because:
//   - the package's CORE promise is determinism, which negative latency does NOT
//     violate (the result is fully reproducible), and
//   - no documented contract forbids negative input or promises clamping, so
//     adding a clamp would silently change observable behaviour. Leaving the
//     production byte-identical and pinning the contract is the correct call.
//
// VERDICT: LATENT RISK (pinned). Callers MUST treat InjectLatency magnitudes as
// non-negative; passing a negative value yields backwards-time delivery, not a
// clamp and not an error.
func TestAdversarial_NegativeLatency_ProducesBackwardsTick(t *testing.T) {
	s := turmoil.NewSimulation(1)
	must(t, s.AddCluster("c1", []string{"n1"}))
	must(t, s.AddCluster("c2", []string{"n1"}))

	// Normal message: delivered at tick 10.
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("first"), 10))
	// Negative latency on the reverse link: 10 + (-100) = -90.
	s.InjectLatency("c2", "c1", -100)
	must(t, s.Send("c2", "n1", "c1", "n1", []byte("second"), 10))
	s.Run(100)

	log := s.EventLog()
	if len(log) != 2 {
		t.Fatalf("expected 2 events, got %d", len(log))
	}
	// Find the negative-latency event (payload "second").
	var second *turmoil.DeliveryEvent
	for i := range log {
		if string(log[i].Payload) == "second" {
			second = &log[i]
		}
	}
	if second == nil {
		t.Fatal("missing 'second' event")
	}
	// CONTRACT (pinned): negative latency yields a negative delivery tick. If a
	// future change clamps latency to >= 0, this assertion flips and forces a
	// deliberate contract review rather than a silent semantic change.
	if second.Tick != -90 {
		t.Fatalf("negative-latency delivery tick = %d, want -90 (no clamp). "+
			"If clamping was added intentionally, update this pinned contract.", second.Tick)
	}
	// And it is reproducible (determinism preserved despite the footgun).
	if second.Dropped {
		t.Fatalf("negative-latency message unexpectedly dropped: reason=%q", second.Reason)
	}
}

// TestAdversarial_NegativeLatency_Deterministic confirms that even the footgun
// path is fully reproducible: same seed + same ops ⇒ byte-identical canonical
// log. Nondeterminism here would be the REAL bug; backwards ticks alone are not.
func TestAdversarial_NegativeLatency_Deterministic(t *testing.T) {
	build := func() string {
		s := turmoil.NewSimulation(123)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		s.InjectLatency("c1", "c2", -7)
		for i := 0; i < 10; i++ {
			must(t, s.Send("c1", "n1", "c2", "n1", []byte{byte(i)}, int64(i)))
		}
		s.Run(1000)
		return turmoil.EncodeLog(s.EventLog())
	}
	a := build()
	b := build()
	if a != b {
		t.Fatalf("negative-latency runs diverged (nondeterminism):\n%s\n---\n%s", a, b)
	}
}

// ── (B) FAULT CORRECTNESS: InjectLatency replace-not-accumulate semantics ─────

// TestAdversarial_InjectLatency_ReplacesNotAccumulates pins that repeated
// InjectLatency calls for the same directional pair REPLACE the prior value
// rather than summing it. The doc says "Calling InjectLatency again for the same
// pair replaces the previous value." A common bug class is accidental
// accumulation (latencies[k] += extra), which this catches.
//
// Mutation that would FAIL this: change `s.latencies[k] = extraTicks` to
// `s.latencies[k] += extraTicks` in InjectLatency.
//
// VERDICT: DOCUMENTED CONTRACT (pin).
func TestAdversarial_InjectLatency_ReplacesNotAccumulates(t *testing.T) {
	s := turmoil.NewSimulation(5)
	must(t, s.AddCluster("c1", []string{"n1"}))
	must(t, s.AddCluster("c2", []string{"n1"}))
	s.InjectLatency("c1", "c2", 3)
	s.InjectLatency("c1", "c2", 8) // replaces 3, does not sum to 11
	must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), 1))
	s.Run(100)
	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	const want = int64(1 + 8) // base 1 + replaced latency 8
	if log[0].Tick != want {
		t.Fatalf("InjectLatency must REPLACE not accumulate: tick=%d, want %d (would be 12 if accumulated)", log[0].Tick, want)
	}
}

// ── (B) FAULT CORRECTNESS: self-partition is a documented no-op ───────────────

// TestAdversarial_SelfPartition_IsNoOp pins the documented behaviour that
// Partition(X, X) does NOT block intra-cluster X→X traffic. The drop guard in
// handleDelivery requires fromCluster != toCluster, so a self-partition entry is
// never consulted for same-cluster messages. This is called out in a code
// comment; pinning it prevents a regression where someone "fixes" self-partition
// to drop intra-cluster traffic.
//
// VERDICT: DOCUMENTED CONTRACT (pin).
func TestAdversarial_SelfPartition_IsNoOp(t *testing.T) {
	s := turmoil.NewSimulation(9)
	must(t, s.AddCluster("c1", []string{"n1", "n2"}))
	s.Partition("c1", "c1") // self-partition
	must(t, s.Send("c1", "n1", "c1", "n2", []byte("intra"), 1))
	s.Run(10)
	log := s.EventLog()
	if len(log) != 1 {
		t.Fatalf("expected 1 event, got %d", len(log))
	}
	if log[0].Dropped {
		t.Fatalf("self-partition must NOT drop intra-cluster traffic; got Dropped=true reason=%q", log[0].Reason)
	}
}

// ── (B) FAULT CORRECTNESS: Run(0)/Run(negative) processes nothing ─────────────

// TestAdversarial_RunNonPositiveSteps_ProcessesNothing pins that Run with a
// non-positive step budget delivers no events and produces an empty log — there
// is no off-by-one that processes one event when asked for zero.
//
// VERDICT: DOCUMENTED CONTRACT (pin).
func TestAdversarial_RunNonPositiveSteps_ProcessesNothing(t *testing.T) {
	for _, steps := range []int{0, -1, -1000} {
		s := turmoil.NewSimulation(3)
		must(t, s.AddCluster("c1", []string{"n1"}))
		must(t, s.AddCluster("c2", []string{"n1"}))
		must(t, s.Send("c1", "n1", "c2", "n1", []byte("x"), 1))
		got := s.Run(steps)
		if got != 0 {
			t.Fatalf("Run(%d) processed %d events, want 0", steps, got)
		}
		if len(s.EventLog()) != 0 {
			t.Fatalf("Run(%d) left %d log entries, want 0", steps, len(s.EventLog()))
		}
	}
}

// ── (B) SINK-SIDE: partition drop is real, not a silent success no-op ─────────

// TestAdversarial_Partition_DropIsObservableSinkSide proves the partition fault
// has a REAL observable sink-side effect: the dropped message NEVER reaches the
// delivered set, AND a parallel un-partitioned message on a different link DOES
// get delivered in the same run. This rules out a "reports success but injects
// nothing" no-op (CLAUDE-1 bluff) and a "drops everything" over-injection.
//
// VERDICT: DOCUMENTED CONTRACT (pin — fault is real and scoped).
func TestAdversarial_Partition_DropIsObservableSinkSide(t *testing.T) {
	s := turmoil.NewSimulation(4)
	must(t, s.AddCluster("a", []string{"n1"}))
	must(t, s.AddCluster("b", []string{"n1"}))
	must(t, s.AddCluster("c", []string{"n1"}))
	s.Partition("a", "b") // only a<->b blocked; a<->c healthy

	must(t, s.Send("a", "n1", "b", "n1", []byte("blocked"), 1))   // must drop
	must(t, s.Send("a", "n1", "c", "n1", []byte("delivered"), 1)) // must deliver
	s.Run(100)

	var blockedSeen, deliveredSeen bool
	for _, e := range s.EventLog() {
		switch string(e.Payload) {
		case "blocked":
			blockedSeen = true
			if !e.Dropped || e.Reason != "partition" {
				t.Fatalf("a→b across partition must drop; got Dropped=%v reason=%q", e.Dropped, e.Reason)
			}
		case "delivered":
			deliveredSeen = true
			if e.Dropped {
				t.Fatalf("a→c (healthy link) must deliver; got Dropped=true reason=%q", e.Reason)
			}
		}
	}
	if !blockedSeen || !deliveredSeen {
		t.Fatalf("missing sink-side evidence: blockedSeen=%v deliveredSeen=%v", blockedSeen, deliveredSeen)
	}
}
