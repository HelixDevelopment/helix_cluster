package infra

// infra_adversarial_test.go — adversarial, SINK-SIDE probes of pkg/infra.
//
// Package surface (read from orchestrator.go + container_orchestrator.go):
//
//   - simulatedOrchestrator: in-memory state machine over two maps
//     (services, vmNodes) guarded by a sync.RWMutex. NON-PRODUCTION fake.
//   - containerOrchestrator: REAL orchestrator backed by a container runtime
//     seam; guards vmNodes + partitioned maps under a sync.RWMutex.
//
// Probed classes (honest mapping):
//
//   (a) UNGUARDED SHARED STATE — both orchestrators mutate shared maps under a
//       mutex. Probed with -race + a conservation invariant on VM bookkeeping.
//       FINDING: locking is correct; -race clean + conservation holds. No bug.
//
//   (b) TOTAL-ORDER / DETERMINISM — Status/Health/Stop with an empty service
//       list build the result slice by `range`-ing the services map, so the
//       OUTPUT ORDER is map-iteration order (nondeterministic). There is NO
//       documented ordering contract on these methods, so this is a LATENT
//       RISK, not a promised-and-violated bug. Pinned below (the test asserts
//       the SET is conserved, and documents the order nondeterminism it
//       observed) rather than asserting a contract the code never made.
//
//   (c) NUMERIC / CAPACITY  and  (d) VALIDATION / FAIL-OPEN — Scale(service,
//       replicas) does NOT validate replicas >= 0. On the REAL
//       containerOrchestrator, a negative `replicas` reaches
//       `toStop := current[replicas:]` (container_orchestrator.go ~L544) which
//       is a negative slice low-bound → runtime panic
//       "slice bounds out of range [-1:]". A hostile/buggy caller passing a
//       negative desired replica count crashes the orchestrator goroutine.
//       FINDING: REAL BUG. The reproducer below FAILS (panics) on unfixed prod.

import (
	"context"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/runtime"
)

// ─── (a) UNGUARDED SHARED STATE — simulatedOrchestrator ──────────────────────

// TestAdversarial_SimVM_ConcurrentSpawnDestroy_RaceAndConservation hammers the
// simulated orchestrator's vmNodes map from many goroutines (spawn + list +
// status + destroy) and asserts, SINK-SIDE, that the surviving node set is
// exactly conserved: every spawned ID that was not destroyed is still listed,
// and nothing extra appears. Run under -race this also proves the map is not
// mutated without the lock.
func TestAdversarial_SimVM_ConcurrentSpawnDestroy_RaceAndConservation(t *testing.T) {
	o := NewOrchestrator(DefaultConfig())
	ctx := context.Background()

	const workers = 16
	const spawnsPerWorker = 8

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < spawnsPerWorker; i++ {
				_, _ = o.VMSpawn(ctx, 1)
				_, _ = o.VMList(ctx)
			}
		}()
	}
	wg.Wait()

	nodes, err := o.VMList(ctx)
	if err != nil {
		t.Fatalf("VMList: %v", err)
	}
	wantTotal := workers * spawnsPerWorker
	if len(nodes) != wantTotal {
		t.Fatalf("conservation violated: spawned %d nodes, list reports %d "+
			"(lost-update under concurrency)", wantTotal, len(nodes))
	}

	// IDs must be unique — a lost update / duplicate-key would collapse two
	// spawns onto one ID. Assert the set size matches the count.
	seen := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if _, dup := seen[n.ID]; dup {
			t.Fatalf("duplicate VM node ID %q — lost update under concurrent VMSpawn", n.ID)
		}
		seen[n.ID] = struct{}{}
	}

	// Now concurrently destroy half and confirm the survivor set is conserved.
	ids := make([]string, 0, len(nodes))
	for id := range seen {
		ids = append(ids, id)
	}
	half := len(ids) / 2
	var dwg sync.WaitGroup
	dwg.Add(half)
	for i := 0; i < half; i++ {
		id := ids[i]
		go func() {
			defer dwg.Done()
			_ = o.VMDestroy(ctx, id)
		}()
	}
	dwg.Wait()

	after, err := o.VMList(ctx)
	if err != nil {
		t.Fatalf("VMList after destroy: %v", err)
	}
	if len(after) != wantTotal-half {
		t.Fatalf("conservation violated after destroy: expected %d survivors, got %d",
			wantTotal-half, len(after))
	}
	destroyed := make(map[string]struct{}, half)
	for i := 0; i < half; i++ {
		destroyed[ids[i]] = struct{}{}
	}
	for _, n := range after {
		if _, gone := destroyed[n.ID]; gone {
			t.Fatalf("node %q was destroyed but still listed", n.ID)
		}
	}
}

// ─── (b) TOTAL-ORDER / DETERMINISM — Status over a map ───────────────────────

// TestAdversarial_SimStatus_EmptyList_SetConservedOrderLatent pins the REAL
// contract of Status(empty): it returns the FULL set of known services, exactly
// once each. It does NOT promise an order — the implementation ranges the
// services map, so output order is map-iteration order (nondeterministic).
//
// This test asserts the conserved invariant the code DOES guarantee (set
// membership + no dups), and records — without failing — whether the order
// varied across runs, so the latent nondeterminism is documented rather than
// fabricated into a contract the code never made.
func TestAdversarial_SimStatus_EmptyList_SetConservedOrderLatent(t *testing.T) {
	o := NewOrchestrator(DefaultConfig())
	ctx := context.Background()

	names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	if _, err := o.Boot(ctx, names); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	const runs = 40
	var firstOrder []string
	orderVaried := false
	for r := 0; r < runs; r++ {
		got, err := o.Status(ctx, nil)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if len(got) != len(names) {
			t.Fatalf("set conservation violated: want %d services, got %d", len(names), len(got))
		}
		seen := make(map[string]struct{}, len(got))
		order := make([]string, 0, len(got))
		for _, s := range got {
			if _, ok := want[s.Name]; !ok {
				t.Fatalf("Status returned unknown service %q", s.Name)
			}
			if _, dup := seen[s.Name]; dup {
				t.Fatalf("Status returned duplicate service %q", s.Name)
			}
			seen[s.Name] = struct{}{}
			order = append(order, s.Name)
		}
		if r == 0 {
			firstOrder = order
		} else if !equalStr(firstOrder, order) {
			orderVaried = true
		}
	}
	// LATENT-RISK note only — Status makes no ordering promise, so a varying
	// order is NOT a failure. We log it so the latent nondeterminism is on the
	// record for any caller that wrongly assumes stable order.
	if orderVaried {
		t.Logf("LATENT: Status(empty) output order is map-iteration order and "+
			"varied across %d runs (no ordering contract; callers must not "+
			"assume a stable order)", runs)
	} else {
		t.Logf("note: Status(empty) order happened to be stable across %d runs "+
			"this execution (still NOT contractually guaranteed)", runs)
	}
}

func equalStr(a, b []string) bool {
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

// ─── (c)/(d) NUMERIC / VALIDATION — Scale with NEGATIVE replicas ─────────────

// TestAdversarial_ContainerScale_NegativeReplicas_NoPanic drives the REAL
// containerOrchestrator's Scale with a NEGATIVE desired replica count.
//
// SINK-SIDE expectation: Scale must NOT panic, and must NOT silently scale to a
// negative/garbage count. Either it rejects the input (error) or it clamps to a
// sane non-negative replica count. A negative `replicas` must never reach
// `current[replicas:]`.
//
// On unfixed prod this test FAILS by PANICKING with
// "slice bounds out of range [-1:]" raised inside Scale
// (container_orchestrator.go: `toStop := current[replicas:]`). It is left
// un-gated and clearly named so it bites on prod.
func TestAdversarial_ContainerScale_NegativeReplicas_NoPanic(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	// Pre-populate two running replicas so `current` is non-empty: this makes
	// the negative low-bound `current[-1:]` unambiguously a bad-bound panic
	// rather than an empty-slice edge case.
	for i := 1; i <= 2; i++ {
		name := scaleReplicaName("svc", i)
		rt.containers[name] = &runtime.ContainerStatus{
			ID: name, Name: name, State: runtime.StateRunning,
		}
	}

	status, err := o.Scale(ctx, "svc", -1)
	if err != nil {
		// Rejecting a negative request is an acceptable, correct outcome.
		t.Logf("Scale(-1) correctly rejected: %v", err)
		return
	}
	// Accepted: the result must be sane and non-negative.
	if status.Replicas < 0 {
		t.Fatalf("Scale(-1) produced negative replica count %d (fail-open numeric)", status.Replicas)
	}
}

// TestAdversarial_SimScale_NegativeReplicas documents the simulated
// orchestrator's behaviour for the SAME negative input. The sim does NOT panic
// (it only writes the field), but it ALSO does not validate — it stores the
// negative count verbatim. This is a fail-open VALIDATION gap, but on the
// NON-PRODUCTION fake it is a LATENT note, not a release bug. Pinned so the
// asymmetry with the real orchestrator is on the record.
func TestAdversarial_SimScale_NegativeReplicas(t *testing.T) {
	o := NewOrchestrator(DefaultConfig())
	ctx := context.Background()

	st, err := o.Scale(ctx, "redis-master", -5)
	if err != nil {
		t.Logf("sim Scale(-5) rejected: %v (good)", err)
		return
	}
	if st.Replicas < 0 {
		t.Logf("LATENT: simulatedOrchestrator.Scale accepts negative replicas "+
			"verbatim (stored %d); fake-only, no real effect, but no input "+
			"validation either", st.Replicas)
	}
}

// ─── (a) UNGUARDED SHARED STATE — containerOrchestrator partition map ────────

// TestAdversarial_ContainerPartition_ConcurrentRace exercises the real
// orchestrator's partitioned map + vmNodes map concurrently (spawn, simulate
// partition with a short auto-heal, query IsPartitioned/VMList) to surface any
// unsynchronised access under -race.
func TestAdversarial_ContainerPartition_ConcurrentRace(t *testing.T) {
	rt := newFakeRuntime()
	o := newTestOrchestrator(t, rt, nil)
	ctx := context.Background()

	nodes, err := o.VMSpawn(ctx, 4)
	if err != nil {
		t.Fatalf("VMSpawn: %v", err)
	}

	var wg sync.WaitGroup
	for _, n := range nodes {
		id := n.ID
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = o.VMSimulatePartition(ctx, id, 20*time.Millisecond)
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = o.IsPartitioned(id)
				_, _ = o.VMList(ctx)
			}
		}()
	}
	wg.Wait()

	// Allow auto-heal timers to fire and re-acquire the lock.
	time.Sleep(60 * time.Millisecond)
	for _, n := range nodes {
		if o.IsPartitioned(n.ID) {
			t.Fatalf("node %q still partitioned after auto-heal window", n.ID)
		}
	}
}

// ─── small local helpers (kept out of prod) ──────────────────────────────────

func scaleReplicaName(service string, n int) string {
	return service + "-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
