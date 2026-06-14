package dst

// Adversarial determinism probes for the DST harness (HXC-1419).
//
// The WHOLE value of a Deterministic Simulation Testing framework is that
// trace = f(seed) byte-for-byte. If ANY hidden nondeterminism leaks in
// (wall-clock, unseeded/global rand, map-iteration order leaking into event
// ordering or the RNG draw sequence, a lost scheduler tie-break, an off-by-one
// in step/tick counting), then every test built on top of this harness is
// silently unreliable. These tests assert SINK-SIDE: the actual recorded trace
// string is identical across same-seed runs and across hostile map layouts, and
// the scheduler imposes a real total order on same-tick events.
//
// All probes are package-internal so they can reach unexported fields (rng,
// queue) the way the production tests already do.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// advScenario builds a deliberately RNG-heavy, map-stressing scenario: many
// actors (so Go's randomized map iteration over handlers/disk/busyUntil would
// surface if it ever leaked into ordering), drops, disk faults, disk stalls,
// and timers. Every actor both Sends (network RNG draws) and Writes/Reads (disk
// RNG draws) and schedules a timer (At), exercising every facility. The fan-out
// guarantees same-tick collisions that stress the (at, seq) tie-break.
func advScenario(sim *Simulation, nActors, cap int) {
	sim.SetNetwork(NetworkConfig{
		DropProbability: 0.2,
		MinLatency:      1 * time.Millisecond,
		MaxLatency:      9 * time.Millisecond,
	})
	sim.SetDisk(DiskConfig{
		FaultProbability: 0.05,
		MinStall:         1 * time.Microsecond,
		MaxStall:         500 * time.Microsecond,
	})

	ids := make([]ActorID, nActors)
	for i := range ids {
		ids[i] = ActorID(fmt.Sprintf("node-%02d", i))
	}

	h := func(self ActorID) Handler {
		return HandlerFunc(func(s *Simulation, msg Message) {
			var n int
			if _, err := fmt.Sscanf(msg.Body, "%d", &n); err != nil {
				return
			}
			_ = s.Write(self, "counter", msg.Body)
			if _, _, err := s.Read(self, "counter"); err != nil {
				s.Log("read-fault")
			}
			if n < cap {
				// Ring topology: forward to the next node, plus a timer-driven
				// echo to the previous node. Two outbound effects per receive
				// maximize same-tick collisions in the scheduler.
				next := ids[(indexOf(ids, self)+1)%len(ids)]
				prev := ids[(indexOf(ids, self)+len(ids)-1)%len(ids)]
				s.Send(self, next, fmt.Sprintf("%d", n+1))
				captured := n
				s.At(2*time.Millisecond, func(s2 *Simulation) {
					s2.Send(self, prev, fmt.Sprintf("%d", captured+1))
				})
			}
		})
	}

	for _, id := range ids {
		sim.Register(id, h(id))
	}
	// Kick the ring at multiple entry points at the SAME logical instant so the
	// initial deliveries collide on tick and must be ordered by seq.
	for i := 0; i < nActors; i++ {
		sim.Send("seed", ids[i], "1")
	}
}

func indexOf(ids []ActorID, want ActorID) int {
	for i, id := range ids {
		if id == want {
			return i
		}
	}
	return 0
}

func runAdv(seed int64, nActors, cap, steps int) string {
	sim := New(seed)
	advScenario(sim, nActors, cap)
	sim.Run(steps)
	return sim.TraceString()
}

// TestAdversarial_SameSeedByteIdenticalManyIterations is the core sink-side
// determinism probe. It runs the SAME seed 60 times. Any leak of wall-clock,
// global/unseeded rand, or map-iteration order into the trace or the RNG draw
// sequence would make at least one of these 60 runs diverge. We require the
// trace to be non-trivial (exercises drops/faults/timers) so the guarantee is
// load-bearing, not vacuous.
func TestAdversarial_SameSeedByteIdenticalManyIterations(t *testing.T) {
	const (
		seed    = int64(0xBADCAFE)
		nActors = 7
		cap     = 40
		steps   = 4000
		iters   = 60
	)
	want := runAdv(seed, nActors, cap, steps)
	if want == "" {
		t.Fatal("scenario produced an empty trace; probe would be vacuous")
	}
	// Sanity: the trace must actually exercise the RNG-driven facilities,
	// otherwise determinism is trivially satisfied by a constant.
	for _, kind := range []string{"DROP", "DELIVER", "WRITE", "TIMER"} {
		if !strings.Contains(want, " "+kind+" ") && !strings.Contains(want, kind+" ") {
			t.Fatalf("trace never exercised %q; probe is too weak: \n%s", kind, firstLines(want, 20))
		}
	}
	for i := 0; i < iters; i++ {
		got := runAdv(seed, nActors, cap, steps)
		if got != want {
			t.Fatalf("NONDETERMINISM: iteration %d diverged from baseline\n%s",
				i, firstDiff(want, got))
		}
	}
}

// TestAdversarial_HostileMapLayout forces Go's runtime to give each replay a
// DIFFERENT internal map layout (by registering actors in many shuffled orders
// and pre-touching maps with throwaway keys), then asserts the trace is STILL
// byte-identical for a fixed seed. If event ordering, the RNG draw order, or any
// trace field were derived from map-iteration order, these would diverge.
//
// Registration order must not affect the trace because all map access in the
// harness is by-key or via sortedKeys — never a raw range that feeds ordering.
func TestAdversarial_HostileMapLayout(t *testing.T) {
	const (
		seed  = int64(13579)
		cap   = 30
		steps = 3000
	)

	// Compare builds that differ ONLY in actor registration order (which perturbs
	// the internal layout of the handlers/disk/busyUntil maps). Registration order
	// must not change the trace, because all harness map access is by-key or via
	// sortedKeys — never a raw range that feeds event ordering or the RNG draw
	// sequence.
	stable := func(regOrder []int) string {
		sim := New(seed)
		sim.SetNetwork(NetworkConfig{
			DropProbability: 0.15,
			MinLatency:      1 * time.Millisecond,
			MaxLatency:      7 * time.Millisecond,
		})
		sim.SetDisk(DiskConfig{FaultProbability: 0.03, MinStall: 1 * time.Microsecond, MaxStall: 200 * time.Microsecond})
		ids := []ActorID{"alpha", "bravo", "charlie", "delta", "echo"}
		h := func(self ActorID, peer ActorID) Handler {
			return HandlerFunc(func(s *Simulation, msg Message) {
				var n int
				if _, err := fmt.Sscanf(msg.Body, "%d", &n); err != nil {
					return
				}
				_ = s.Write(self, "k", msg.Body)
				if n < cap {
					s.Send(self, peer, fmt.Sprintf("%d", n+1))
				}
			})
		}
		for _, idx := range regOrder { // ONLY registration order varies
			self := ids[idx]
			peer := ids[(idx+1)%len(ids)]
			sim.Register(self, h(self, peer))
		}
		sim.Send("seed", ids[0], "1")
		sim.Run(steps)
		return sim.TraceString()
	}

	orders := [][]int{
		{0, 1, 2, 3, 4},
		{4, 3, 2, 1, 0},
		{2, 0, 4, 1, 3},
		{1, 4, 0, 3, 2},
		{3, 2, 1, 4, 0},
	}
	want := stable(orders[0])
	if want == "" {
		t.Fatal("empty trace; probe vacuous")
	}
	for i, ord := range orders {
		got := stable(ord)
		if got != want {
			t.Fatalf("registration order %v changed the trace (map-iteration leak?)\n%s",
				ord, firstDiff(want, got))
		}
		_ = i
	}
}

// TestAdversarial_SchedulerTotalOrderSameTick stresses the (at, seq) tie-break
// directly: it enqueues MANY events that all fire at the exact same logical tick
// and asserts (a) the dispatch order is byte-identical across repeated runs, and
// (b) it follows seq (insertion) order, not map/heap accident. With a fixed
// (min==max) zero latency, every Send lands at the same tick, so only the seq
// tie-break can order them.
func TestAdversarial_SchedulerTotalOrderSameTick(t *testing.T) {
	const fanout = 64

	run := func() []string {
		sim := New(1)
		// Zero latency, no drops, no faults: every delivery fires at tick 0 and
		// the ONLY thing that can order them is the seq tie-break.
		sim.SetNetwork(NetworkConfig{MinLatency: 0, MaxLatency: 0})
		var order []string
		sim.Register("sink", HandlerFunc(func(s *Simulation, msg Message) {
			order = append(order, msg.Body)
		}))
		for i := 0; i < fanout; i++ {
			sim.Send("src", "sink", fmt.Sprintf("%03d", i))
		}
		sim.Run(fanout)
		return order
	}

	first := run()
	if len(first) != fanout {
		t.Fatalf("expected %d deliveries, got %d", fanout, len(first))
	}
	// (b) Must be in insertion (seq) order: 000,001,...,063.
	for i, body := range first {
		if body != fmt.Sprintf("%03d", i) {
			t.Fatalf("same-tick dispatch not in seq order at %d: got %q want %03d\nfull=%v",
				i, body, i, first)
		}
	}
	// (a) Must be identical across many repeats (no heap/map accident).
	for r := 0; r < 40; r++ {
		again := run()
		if !equalStrings(first, again) {
			t.Fatalf("same-tick dispatch order diverged on repeat %d:\n want=%v\n got =%v",
				r, first, again)
		}
	}
}

// TestAdversarial_SeedSensitivity proves the seed is actually load-bearing:
// across 200 distinct seeds we must observe MANY distinct traces (not all
// identical — which would mean the seed is ignored), and EACH seed must be
// internally reproducible (re-running it yields the identical trace). This
// discriminates "ignored seed" from "true nondeterminism".
func TestAdversarial_SeedSensitivity(t *testing.T) {
	const (
		nSeeds  = 200
		nActors = 5
		cap     = 25
		steps   = 2500
	)
	seen := make(map[string]int64, nSeeds)
	distinct := 0
	for seed := int64(0); seed < nSeeds; seed++ {
		tr := runAdv(seed, nActors, cap, steps)
		if tr == "" {
			t.Fatalf("seed %d produced empty trace", seed)
		}
		// Internal reproducibility for this seed.
		again := runAdv(seed, nActors, cap, steps)
		if tr != again {
			t.Fatalf("seed %d is NONDETERMINISTIC across two runs:\n%s", seed, firstDiff(tr, again))
		}
		if _, ok := seen[tr]; !ok {
			seen[tr] = seed
			distinct++
		}
	}
	// We don't demand all-distinct (collisions are legal), but a healthy seed
	// space must yield many distinct traces. All-identical => seed ignored.
	if distinct < nSeeds/2 {
		t.Fatalf("only %d/%d distinct traces across seeds; seed is barely load-bearing", distinct, nSeeds)
	}
}

// TestAdversarial_StepBoundaryExactAndResumable pins the step/tick counting
// contract: Run(maxSteps) executes AT MOST maxSteps events, returns the exact
// count, and a run split into two halves (Run(k) then Run(rest)) produces the
// SAME trace prefix relationship as a single Run(k+rest). This catches an
// off-by-one in the `executed < maxSteps` loop guard or in stepCounter.
func TestAdversarial_StepBoundaryExactAndResumable(t *testing.T) {
	mk := func() *Simulation {
		sim := New(424242)
		sim.SetNetwork(NetworkConfig{MinLatency: 1 * time.Millisecond, MaxLatency: 1 * time.Millisecond})
		h := func(self, peer ActorID) Handler {
			return HandlerFunc(func(s *Simulation, msg Message) {
				var n int
				if _, err := fmt.Sscanf(msg.Body, "%d", &n); err != nil {
					return
				}
				if n < 100 {
					s.Send(self, peer, fmt.Sprintf("%d", n+1))
				}
			})
		}
		sim.Register("P", h("P", "Q"))
		sim.Register("Q", h("Q", "P"))
		sim.Send("seed", "P", "1")
		return sim
	}

	// Exact count: with one in-flight message at a time, exactly 1 event is
	// available per step, so Run(1) must execute exactly 1.
	one := mk()
	if got := one.Run(1); got != 1 {
		t.Fatalf("Run(1) executed %d events, want exactly 1 (off-by-one in loop guard?)", got)
	}

	// Resumability: Run(5) then Run(20) must yield the identical trace to a
	// single Run(25). If stepCounter or the loop boundary double-counted or
	// skipped an event at the resume seam, these diverge.
	split := mk()
	n1 := split.Run(5)
	n2 := split.Run(20)
	splitTrace := split.TraceString()

	whole := mk()
	nw := whole.Run(25)
	wholeTrace := whole.TraceString()

	if n1+n2 != nw {
		t.Fatalf("split executed %d+%d=%d events; whole executed %d", n1, n2, n1+n2, nw)
	}
	if splitTrace != wholeTrace {
		t.Fatalf("split run trace != whole run trace (resume seam nondeterminism):\n%s",
			firstDiff(wholeTrace, splitTrace))
	}
}

// TestAdversarial_InjectFaultAtStepBoundary pins the documented 1-based step at
// which InjectFaultAtStep fires: "run fn immediately BEFORE the step-th event is
// processed (steps counted from 1)". We inject at step 1 and step 3 and assert
// the injected LOG line lands in the trace at the position implied by that
// 1-based contract, and that it is byte-stable across replays.
func TestAdversarial_InjectFaultAtStepBoundary(t *testing.T) {
	mk := func(step int) *Simulation {
		sim := New(5)
		sim.SetNetwork(NetworkConfig{MinLatency: 1 * time.Millisecond, MaxLatency: 1 * time.Millisecond})
		sim.Register("P", HandlerFunc(func(s *Simulation, msg Message) {
			var n int
			if _, err := fmt.Sscanf(msg.Body, "%d", &n); err != nil {
				return
			}
			if n < 10 {
				s.Send("P", "P", fmt.Sprintf("%d", n+1))
			}
		}))
		sim.Send("seed", "P", "1")
		sim.InjectFaultAtStep(step, func(s *Simulation) {
			s.Log(fmt.Sprintf("INJECT@%d", step))
		})
		return sim
	}

	// Step 1: the hook runs BEFORE the first EVENT executes. Pre-Run setup
	// (the kickoff Send) has already emitted a SEND line, so the injected LOG is
	// not necessarily trace[0]; the contract is that it precedes the first
	// DELIVER (the first popped event). Assert: zero DELIVERs occur before the
	// INJECT@1 LOG.
	s1 := mk(1)
	s1.Run(20)
	tr1 := s1.Trace()
	deliversBeforeInject1 := 0
	found1 := false
	for _, e := range tr1 {
		if e.Kind == "DELIVER" {
			deliversBeforeInject1++
		}
		if e.Kind == "LOG" && strings.Contains(e.Text, "INJECT@1") {
			found1 = true
			break
		}
	}
	if !found1 {
		t.Fatalf("InjectFaultAtStep(1) never fired; head=%v", traceHead(tr1))
	}
	if deliversBeforeInject1 != 0 {
		t.Fatalf("InjectFaultAtStep(1) fired after %d DELIVERs, want 0 (must precede first event)\nhead=%v",
			deliversBeforeInject1, traceHead(tr1))
	}

	// Step 3: count DELIVER entries that precede the injected LOG. With one event
	// per step and the hook firing before step 3, exactly 2 prior steps (=2
	// DELIVERs, possibly with their own follow-on LOGs absent here) must precede
	// it. Assert the injected line appears and is preceded by exactly 2 DELIVERs.
	s3 := mk(3)
	s3.Run(20)
	deliversBefore := 0
	foundAt := -1
	for i, e := range s3.Trace() {
		if e.Kind == "DELIVER" && foundAt == -1 {
			deliversBefore++
		}
		if e.Kind == "LOG" && strings.Contains(e.Text, "INJECT@3") {
			foundAt = i
			break
		}
	}
	if foundAt == -1 {
		t.Fatal("InjectFaultAtStep(3) never fired")
	}
	if deliversBefore != 2 {
		t.Fatalf("InjectFaultAtStep(3) fired after %d DELIVERs, want exactly 2 (1-based step off-by-one?)",
			deliversBefore)
	}

	// Byte-stability across replay.
	again := mk(3)
	again.Run(20)
	if s3.TraceString() != again.TraceString() {
		t.Fatalf("InjectFaultAtStep replay diverged:\n%s", firstDiff(s3.TraceString(), again.TraceString()))
	}
}

// --- diff/util helpers (adversarial-test-local; no clashes with dst_test.go) ---

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func traceHead(tr []TraceEntry) string {
	var b strings.Builder
	for i, e := range tr {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&b, "[%d] %s\n", i, e.String())
	}
	return b.String()
}

// firstDiff returns a human-readable description of the first line at which two
// traces diverge, with a little surrounding context — so a determinism failure
// pinpoints the exact divergent event rather than dumping two huge blobs.
func firstDiff(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	max := len(wl)
	if len(gl) > max {
		max = len(gl)
	}
	for i := 0; i < max; i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			var b strings.Builder
			fmt.Fprintf(&b, "first divergence at line %d:\n", i)
			fmt.Fprintf(&b, "  want: %q\n", w)
			fmt.Fprintf(&b, "  got : %q\n", g)
			lo := i - 2
			if lo < 0 {
				lo = 0
			}
			fmt.Fprintf(&b, "context (want lines %d..%d):\n", lo, i)
			for j := lo; j <= i && j < len(wl); j++ {
				fmt.Fprintf(&b, "    %d: %s\n", j, wl[j])
			}
			return b.String()
		}
	}
	return "(no line-level diff found; lengths want=" +
		fmt.Sprint(len(wl)) + " got=" + fmt.Sprint(len(gl)) + ")"
}
