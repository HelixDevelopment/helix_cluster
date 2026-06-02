// Package chaosexp defines the CE-01..CE-12 internal chaos experiment suite
// for HelixCluster. Each experiment exercises a distinct message-delivery
// behaviour of the [turmoil.Simulation]: partitions, latency injection, heal,
// intra-cluster immunity, multi-message ordering, and their combinations.
//
// HONEST SCOPE: turmoil models MESSAGE DELIVERY only. No consensus, leader
// election, or quorum logic is present in the simulation engine. Every
// experiment's SteadyState predicate therefore asserts ONLY observable
// delivery effects visible in [turmoil.EventLog]: whether messages were
// dropped, the tick at which they were delivered, ordering, reasons, etc.
//
// # Reproducibility contract
//
// [Run] calls [turmoil.NewSimulation](seed), runs e.Setup, then [turmoil.Simulation.Run].
// Because [turmoil.NewSimulation] is fully deterministic for a given seed and
// operation sequence, two independent [Run](e, seed) calls with the same
// (experiment, seed) pair always produce a byte-identical [Result.Canonical].
package chaosexp

import (
	"fmt"
	"regexp"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/turmoil"
)

// ── core types ────────────────────────────────────────────────────────────────

// ChaosExperiment describes a single chaos experiment executed against a
// [turmoil.Simulation].
//
//   - Setup     configures the simulation (clusters, partitions, latency).
//   - SteadyState evaluates the [turmoil.DeliveryEvent] log and returns true
//     when the experiment's delivery hypothesis is confirmed.
//   - BlastRadius is a human-readable description of the impacted scope.
type ChaosExperiment struct {
	// ID is the canonical identifier, e.g. "CE-01".
	ID string
	// Title is a short human-readable name.
	Title string
	// Hypothesis states what delivery behaviour is expected.
	Hypothesis string
	// Setup configures the simulation before Run is called.
	Setup func(*turmoil.Simulation)
	// SteadyState inspects the EventLog and returns true when the hypothesis holds.
	SteadyState func([]turmoil.DeliveryEvent) bool
	// BlastRadius describes which clusters/nodes are affected.
	BlastRadius string
}

// Result is the output of a single experiment run.
type Result struct {
	// ID is the experiment identifier.
	ID string
	// Passed is true when SteadyState returned true for the observed EventLog.
	Passed bool
	// Events is a copy of the EventLog produced by the simulation.
	Events []turmoil.DeliveryEvent
	// Canonical is the byte-stable encoding of Events via [turmoil.EncodeLog].
	// Two Run calls with the same (experiment, seed) always produce the same
	// Canonical string.
	Canonical string
}

// ── catalog ───────────────────────────────────────────────────────────────────

// Catalog returns all 12 chaos experiments, CE-01..CE-12, each exercising a
// distinct delivery-level scenario. The slice is freshly allocated on every
// call; callers may iterate or mutate it freely.
func Catalog() []ChaosExperiment {
	return []ChaosExperiment{
		// CE-01 ─────────────────────────────────────────────────────────────────
		// Single partition: a cross-cluster message is dropped with reason
		// "partition".  No delivered events may cross the c1↔c2 boundary.
		{
			ID:          "CE-01",
			Title:       "Single partition drops cross-cluster message",
			Hypothesis:  "A c1↔c2 partition causes a c1→c2 message to be logged as Dropped=true with Reason=partition.",
			BlastRadius: "c1↔c2",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.Partition("c1", "c2")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("ce01-msg"), 1)
				sim.Run(20)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				// Exactly one event; it must be dropped with reason "partition".
				if len(log) != 1 {
					return false
				}
				return log[0].Dropped && log[0].Reason == "partition" &&
					log[0].FromCluster == "c1" && log[0].ToCluster == "c2"
			},
		},

		// CE-02 ─────────────────────────────────────────────────────────────────
		// Heal after partition: first message dropped; after Heal second message
		// is delivered.  Both events must appear; the post-heal one must NOT be
		// dropped.
		{
			ID:          "CE-02",
			Title:       "Heal restores delivery after partition",
			Hypothesis:  "After Heal, a new Send across the formerly-partitioned link is delivered (Dropped=false).",
			BlastRadius: "c1↔c2 (transient)",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.Partition("c1", "c2")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("before-heal"), 1)
				sim.Run(10)
				sim.Heal("c1", "c2")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("after-heal"), 1)
				sim.Run(10)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 2 {
					return false
				}
				// First message was sent before Heal → must be dropped.
				if !log[0].Dropped || log[0].Reason != "partition" {
					return false
				}
				// Second message was sent after Heal → must be delivered.
				return !log[1].Dropped && string(log[1].Payload) == "after-heal"
			},
		},

		// CE-03 ─────────────────────────────────────────────────────────────────
		// Symmetric partition: Partition("c1","c2") also blocks the reverse
		// direction c2→c1.
		{
			ID:          "CE-03",
			Title:       "Partition is symmetric (reverse direction also blocked)",
			Hypothesis:  "Partition(c1,c2) drops a c2→c1 message with Dropped=true and Reason=partition.",
			BlastRadius: "c1↔c2",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.Partition("c1", "c2")
				// Send in the *reverse* direction to prove symmetry.
				mustSend(sim, "c2", "n1", "c1", "n1", []byte("reverse-msg"), 1)
				sim.Run(20)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 1 {
					return false
				}
				return log[0].Dropped && log[0].Reason == "partition" &&
					log[0].FromCluster == "c2" && log[0].ToCluster == "c1"
			},
		},

		// CE-04 ─────────────────────────────────────────────────────────────────
		// Intra-cluster messages are unaffected by a cross-cluster partition.
		// A c1↔c2 partition must not drop a c1/n1→c1/n2 message.
		{
			ID:          "CE-04",
			Title:       "Intra-cluster message unaffected by cross-cluster partition",
			Hypothesis:  "A c1↔c2 partition does not drop a message between two nodes inside c1.",
			BlastRadius: "c2 isolated from c1; c1-internal unaffected",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1", "n2"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.Partition("c1", "c2")
				mustSend(sim, "c1", "n1", "c1", "n2", []byte("intra-ok"), 1)
				sim.Run(20)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 1 {
					return false
				}
				return !log[0].Dropped &&
					log[0].FromCluster == "c1" && log[0].ToCluster == "c1"
			},
		},

		// CE-05 ─────────────────────────────────────────────────────────────────
		// Double partition isolating a node: c2 is partitioned from both c1 and
		// c3.  Messages from c1→c2 and c3→c2 are both dropped; c1→c3 (no
		// partition between them) is delivered.
		{
			ID:          "CE-05",
			Title:       "Double partition isolates c2 from c1 and c3",
			Hypothesis:  "c2 partitioned from both c1 and c3: c1→c2 and c3→c2 dropped; c1→c3 delivered.",
			BlastRadius: "c2 fully isolated",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				mustAdd(sim, "c3", []string{"n1"})
				sim.Partition("c1", "c2")
				sim.Partition("c2", "c3")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("c1-to-c2"), 1)
				mustSend(sim, "c3", "n1", "c2", "n1", []byte("c3-to-c2"), 1)
				mustSend(sim, "c1", "n1", "c3", "n1", []byte("c1-to-c3"), 1)
				sim.Run(30)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 3 {
					return false
				}
				// Find events by payload to be order-independent.
				var c1c2, c3c2, c1c3 *turmoil.DeliveryEvent
				for i := range log {
					ev := &log[i]
					switch string(ev.Payload) {
					case "c1-to-c2":
						c1c2 = ev
					case "c3-to-c2":
						c3c2 = ev
					case "c1-to-c3":
						c1c3 = ev
					}
				}
				if c1c2 == nil || c3c2 == nil || c1c3 == nil {
					return false
				}
				return c1c2.Dropped && c1c2.Reason == "partition" &&
					c3c2.Dropped && c3c2.Reason == "partition" &&
					!c1c3.Dropped
			},
		},

		// CE-06 ─────────────────────────────────────────────────────────────────
		// Unidirectional latency: InjectLatency(c1,c2,+5) delays c1→c2 but
		// c2→c1 is delivered at the base tick (no extra delay).
		{
			ID:          "CE-06",
			Title:       "Unidirectional latency delays only the configured direction",
			Hypothesis:  "InjectLatency(c1,c2,+5) delays c1→c2 by 5 ticks; c2→c1 (base=1) arrives at tick 1.",
			BlastRadius: "c1→c2 link",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.InjectLatency("c1", "c2", 5)
				// Forward: should arrive at tick 1+5=6.
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("forward"), 1)
				// Reverse: no extra latency → should arrive at tick 1.
				mustSend(sim, "c2", "n1", "c1", "n1", []byte("reverse"), 1)
				sim.Run(50)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 2 {
					return false
				}
				var fwd, rev *turmoil.DeliveryEvent
				for i := range log {
					ev := &log[i]
					switch string(ev.Payload) {
					case "forward":
						fwd = ev
					case "reverse":
						rev = ev
					}
				}
				if fwd == nil || rev == nil {
					return false
				}
				// Forward arrives at tick 6 (1 base + 5 extra); reverse at tick 1.
				return !fwd.Dropped && fwd.Tick == 6 &&
					!rev.Dropped && rev.Tick == 1
			},
		},

		// CE-07 ─────────────────────────────────────────────────────────────────
		// Bidirectional latency: both c1→c2 and c2→c1 have extra latency
		// injected independently.  Each message arrives at base+extra for its
		// own direction.
		{
			ID:          "CE-07",
			Title:       "Bidirectional latency delays both directions independently",
			Hypothesis:  "InjectLatency(c1,c2,+3) and InjectLatency(c2,c1,+7) cause c1→c2 at tick 4 and c2→c1 at tick 8 (base=1).",
			BlastRadius: "c1↔c2 links",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.InjectLatency("c1", "c2", 3)
				sim.InjectLatency("c2", "c1", 7)
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("c1-fwd"), 1)
				mustSend(sim, "c2", "n1", "c1", "n1", []byte("c2-fwd"), 1)
				sim.Run(100)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 2 {
					return false
				}
				var c1fwd, c2fwd *turmoil.DeliveryEvent
				for i := range log {
					ev := &log[i]
					switch string(ev.Payload) {
					case "c1-fwd":
						c1fwd = ev
					case "c2-fwd":
						c2fwd = ev
					}
				}
				if c1fwd == nil || c2fwd == nil {
					return false
				}
				// c1→c2: base 1 + extra 3 = tick 4.
				// c2→c1: base 1 + extra 7 = tick 8.
				return !c1fwd.Dropped && c1fwd.Tick == 4 &&
					!c2fwd.Dropped && c2fwd.Tick == 8
			},
		},

		// CE-08 ─────────────────────────────────────────────────────────────────
		// Latency exact tick: base=2, extra=10 → must arrive at exactly tick 12.
		// This is the tightest latency assertion, verifying the addition formula
		// total = delayTicks + latencies[fromCluster→toCluster].
		{
			ID:          "CE-08",
			Title:       "Latency exact tick: base+extra = delivered tick",
			Hypothesis:  "With base delayTicks=2 and InjectLatency(+10) the message is delivered at tick 12.",
			BlastRadius: "c1→c2 link",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.InjectLatency("c1", "c2", 10)
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("exact-tick"), 2)
				sim.Run(100)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 1 {
					return false
				}
				return !log[0].Dropped && log[0].Tick == 12
			},
		},

		// CE-09 ─────────────────────────────────────────────────────────────────
		// Partition takes precedence over latency: even with a large extra
		// latency injected, a partitioned message is dropped rather than
		// delivered late.
		{
			ID:          "CE-09",
			Title:       "Partition takes precedence over latency injection",
			Hypothesis:  "A message on a partitioned + latency-injected link is Dropped=true with Reason=partition.",
			BlastRadius: "c1↔c2",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				sim.InjectLatency("c1", "c2", 100)
				sim.Partition("c1", "c2")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("combo-msg"), 1)
				sim.Run(1000)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 1 {
					return false
				}
				return log[0].Dropped && log[0].Reason == "partition"
			},
		},

		// CE-10 ─────────────────────────────────────────────────────────────────
		// Multi-message ordering: five messages sent with distinct base delays
		// (5,2,8,1,3) arrive in ascending tick order: 1,2,3,5,8.  No dropped
		// events.
		{
			ID:          "CE-10",
			Title:       "Multi-message virtual-time ordering",
			Hypothesis:  "Five messages with base delays {5,2,8,1,3} appear in EventLog in ascending tick order {1,2,3,5,8}.",
			BlastRadius: "c1→c2",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				for _, d := range []int64{5, 2, 8, 1, 3} {
					mustSend(sim, "c1", "n1", "c2", "n1",
						[]byte(fmt.Sprintf("d%d", d)), d)
				}
				sim.Run(100)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 5 {
					return false
				}
				wantTicks := []int64{1, 2, 3, 5, 8}
				for i, wt := range wantTicks {
					if log[i].Tick != wt || log[i].Dropped {
						return false
					}
				}
				return true
			},
		},

		// CE-11 ─────────────────────────────────────────────────────────────────
		// Multi-cluster: three clusters with only one partitioned pair.
		// Messages within the non-partitioned pair are delivered; only the
		// partitioned pair's messages are dropped.
		{
			ID:          "CE-11",
			Title:       "Selective partition: only the configured cluster pair is blocked",
			Hypothesis:  "Partition(c1,c2) drops c1→c2 and c2→c1 but delivers c1→c3 and c3→c1.",
			BlastRadius: "c1↔c2 only",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1"})
				mustAdd(sim, "c2", []string{"n1"})
				mustAdd(sim, "c3", []string{"n1"})
				sim.Partition("c1", "c2")
				mustSend(sim, "c1", "n1", "c2", "n1", []byte("blocked-fwd"), 1)
				mustSend(sim, "c2", "n1", "c1", "n1", []byte("blocked-rev"), 1)
				mustSend(sim, "c1", "n1", "c3", "n1", []byte("delivered-fwd"), 1)
				mustSend(sim, "c3", "n1", "c1", "n1", []byte("delivered-rev"), 1)
				sim.Run(30)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 4 {
					return false
				}
				results := make(map[string]bool) // payload → Dropped
				for _, ev := range log {
					results[string(ev.Payload)] = ev.Dropped
				}
				return results["blocked-fwd"] == true &&
					results["blocked-rev"] == true &&
					results["delivered-fwd"] == false &&
					results["delivered-rev"] == false
			},
		},

		// CE-12 ─────────────────────────────────────────────────────────────────
		// Intra-cluster messages unaffected by a partition between two OTHER
		// clusters: c2↔c3 partitioned; c1's intra-cluster messages still
		// deliver.
		{
			ID:          "CE-12",
			Title:       "Intra-cluster messages unaffected by an unrelated partition",
			Hypothesis:  "A c2↔c3 partition does not affect c1/n1→c1/n2 intra-cluster delivery.",
			BlastRadius: "c2↔c3; c1 unaffected",
			Setup: func(sim *turmoil.Simulation) {
				mustAdd(sim, "c1", []string{"n1", "n2"})
				mustAdd(sim, "c2", []string{"n1"})
				mustAdd(sim, "c3", []string{"n1"})
				sim.Partition("c2", "c3")
				mustSend(sim, "c1", "n1", "c1", "n2", []byte("intra-c1"), 1)
				sim.Run(20)
			},
			SteadyState: func(log []turmoil.DeliveryEvent) bool {
				if len(log) != 1 {
					return false
				}
				return !log[0].Dropped &&
					log[0].FromCluster == "c1" && log[0].ToCluster == "c1" &&
					string(log[0].Payload) == "intra-c1"
			},
		},
	}
}

// ── validation ────────────────────────────────────────────────────────────────

// idPattern is the compiled form of the CE-\d\d regular expression used by
// [Validate].
var idPattern = regexp.MustCompile(`^CE-\d\d$`)

// Validate returns a non-nil error if e fails any structural constraint:
//   - ID must match CE-\d\d
//   - Title must be non-empty
//   - Hypothesis must be non-empty
//   - Setup must be non-nil
//   - SteadyState must be non-nil
func Validate(e ChaosExperiment) error {
	if !idPattern.MatchString(e.ID) {
		return fmt.Errorf("experiment ID %q does not match CE-\\d\\d", e.ID)
	}
	if e.Title == "" {
		return fmt.Errorf("experiment %s: Title is empty", e.ID)
	}
	if e.Hypothesis == "" {
		return fmt.Errorf("experiment %s: Hypothesis is empty", e.ID)
	}
	if e.Setup == nil {
		return fmt.Errorf("experiment %s: Setup is nil", e.ID)
	}
	if e.SteadyState == nil {
		return fmt.Errorf("experiment %s: SteadyState is nil", e.ID)
	}
	return nil
}

// ValidateCatalog validates every experiment returned by [Catalog]:
//   - Exactly 12 entries.
//   - All IDs are unique.
//   - Each entry passes [Validate].
//   - IDs are exactly the set {CE-01, CE-02, …, CE-12}.
func ValidateCatalog() error {
	cat := Catalog()
	if len(cat) != 12 {
		return fmt.Errorf("catalog has %d experiments, want 12", len(cat))
	}
	seen := make(map[string]struct{}, 12)
	for _, e := range cat {
		if err := Validate(e); err != nil {
			return fmt.Errorf("catalog validation failed: %w", err)
		}
		if _, dup := seen[e.ID]; dup {
			return fmt.Errorf("duplicate experiment ID %q", e.ID)
		}
		seen[e.ID] = struct{}{}
	}
	// Verify the exact required set CE-01..CE-12.
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("CE-%02d", i)
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("catalog missing required experiment %s", id)
		}
	}
	return nil
}

// ── runner ────────────────────────────────────────────────────────────────────

// Run executes a single chaos experiment with the given seed and returns the
// [Result]. The simulation is seeded with seed so that two independent Run
// calls with the same (e, seed) always produce the same [Result.Canonical].
//
// The flow is:
//  1. [turmoil.NewSimulation](seed) — fresh, deterministic sim.
//  2. e.Setup(sim) — configure clusters, faults, send messages, and call sim.Run.
//  3. Collect EventLog and evaluate e.SteadyState.
//  4. Encode log via [turmoil.EncodeLog] for the Canonical field.
func Run(e ChaosExperiment, seed int64) Result {
	sim := turmoil.NewSimulation(seed)
	e.Setup(sim)
	events := sim.EventLog()
	passed := e.SteadyState(events)
	canonical := turmoil.EncodeLog(events)
	return Result{
		ID:        e.ID,
		Passed:    passed,
		Events:    events,
		Canonical: canonical,
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

// mustAdd calls sim.AddCluster and panics on error. Used inside Setup funcs
// where an error indicates a programming mistake in the experiment definition.
func mustAdd(sim *turmoil.Simulation, clusterID string, nodes []string) {
	if err := sim.AddCluster(clusterID, nodes); err != nil {
		panic(fmt.Sprintf("chaosexp Setup: AddCluster(%q): %v", clusterID, err))
	}
}

// mustSend calls sim.Send and panics on error. Used inside Setup funcs where
// an error indicates a programming mistake in the experiment definition.
func mustSend(sim *turmoil.Simulation, fromC, fromN, toC, toN string, payload []byte, delayTicks int64) {
	if err := sim.Send(fromC, fromN, toC, toN, payload, delayTicks); err != nil {
		panic(fmt.Sprintf("chaosexp Setup: Send(%q/%q→%q/%q): %v", fromC, fromN, toC, toN, err))
	}
}
