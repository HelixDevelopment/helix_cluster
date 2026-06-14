package gitops

// Adversarial test suite for pkg/gitops — probes the reconcile/diff/plan
// determinism, total-order-at-identity-edge, idempotency, and concurrency
// contracts SINK-SIDE (the actual emitted plan/diff bytes across many runs, the
// actual second-apply behavior), never merely "no panic".
//
// Tonight's classes:
//   (a) DETERMINISM / TOTAL-ORDER: a diff/plan computed from a Go map (resources
//       keyed by identity, apps grouped by tier) must emit byte-identical output
//       across MANY runs despite map-iteration randomization; equal-key /
//       equal-priority inputs must have a deterministic tiebreak.
//   (b) IDEMPOTENCY / COMPLETENESS: re-running the same reconcile twice is a
//       no-op; every input app appears in the plan exactly once (none silently
//       dropped).
//   (c) UNGUARDED SHARED MUTABLE STATE under concurrency (-race).
//   (d) DRIFT/CONFLICT: a both-changed / delete-vs-update edge resolved one way.
//
// Run: go test -race -count=2 github.com/HelixDevelopment/helix_cluster/pkg/gitops

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// adversRunID anchors forensic log lines for this suite.
const adversRunID = "1367f000-0000-4000-8000-00000000face"

// canonReport serializes a DriftReport to deterministic bytes for cross-run
// byte-identity comparison. The slices are compared in the exact order
// DetectDrift emitted them (NO re-sort here) — so if DetectDrift's output order
// is map-iteration-dependent, two runs produce different bytes and the test
// bites.
func canonReport(r DriftReport) string {
	b, _ := json.Marshal(struct {
		ToPrune []Resource
		Missing []Resource
	}{r.ToPrune, r.Missing})
	return string(b)
}

// ---------------------------------------------------------------------------
// (a) DETERMINISM — DetectDrift over map-sourced sets, MANY iterations.
// ---------------------------------------------------------------------------

// TestAdversarial_DetectDrift_DeterministicAcrossManyRuns is the load-bearing
// determinism guard. DetectDrift builds two Go maps and ranges over them (random
// iteration order each run). The contract (drift.go) promises: "Both result
// slices are sorted (by Group,Kind,Namespace,Name) for deterministic output."
//
// We feed a set whose members are deliberately ADVERSARIAL for the sort: many
// resources that collide on the first 1, 2, and 3 sort fields and only differ on
// the LAST field (Name) — the identity-edge case where a partial-order tiebreak
// would leave map-iteration order showing through. We run DetectDrift MANY times
// and assert every run is byte-identical to the first. Any nondeterminism (e.g.
// a sort that omitted the Name tiebreak) diverges within a handful of runs.
func TestAdversarial_DetectDrift_DeterministicAcrossManyRuns(t *testing.T) {
	// Build a large adversarial live set: all share Group="apps", Kind="X",
	// Namespace="ns" and differ ONLY in Name — they are equal on the first three
	// sort fields, so only the final Name tiebreak distinguishes them.
	var live []Resource
	for i := 0; i < 64; i++ {
		live = append(live, Resource{
			Group:     "apps",
			Kind:      "X",
			Namespace: "ns",
			Name:      fmt.Sprintf("res-%02d", i),
		})
	}
	// Add a second cluster colliding on first TWO fields (Group,Kind) but
	// differing on Namespace then Name, to also exercise the Namespace tiebreak.
	for i := 0; i < 32; i++ {
		live = append(live, Resource{
			Group:     "apps",
			Kind:      "X",
			Namespace: fmt.Sprintf("ns-%02d", i),
			Name:      "same-name",
		})
	}
	// Desired is empty -> ALL live resources land in ToPrune (the branch that
	// ranges the map). Also give a few desired-only entries -> Missing.
	desired := []Resource{
		{Group: "", Kind: "Aaa", Namespace: "z", Name: "m1"},
		{Group: "", Kind: "Aaa", Namespace: "z", Name: "m0"},
		{Group: "", Kind: "Aaa", Namespace: "a", Name: "m9"},
	}

	first := canonReport(DetectDrift(desired, live))
	const iters = 400
	for i := 0; i < iters; i++ {
		got := canonReport(DetectDrift(desired, live))
		if got != first {
			t.Fatalf("[%s] DetectDrift nondeterministic at iter %d:\n first=%s\n got  =%s",
				adversRunID, i, first, got)
		}
	}
	// Sanity: ToPrune holds every live resource, Missing every desired one.
	rep := DetectDrift(desired, live)
	if len(rep.ToPrune) != len(live) {
		t.Fatalf("[%s] ToPrune=%d want %d", adversRunID, len(rep.ToPrune), len(live))
	}
	if len(rep.Missing) != len(desired) {
		t.Fatalf("[%s] Missing=%d want %d", adversRunID, len(rep.Missing), len(desired))
	}
	t.Logf("[%s] DetectDrift byte-identical across %d runs over %d-resource adversarial map set",
		adversRunID, iters, len(live))
}

// TestAdversarial_Generate_DeterministicAcrossManyRuns probes ApplicationSet
// generation determinism. Generate builds apps then sorts by Metadata.Name. We
// run it MANY times over a fixed cell set and assert byte-identical JSON every
// run (the apps slice order + content must be stable).
func TestAdversarial_Generate_DeterministicAcrossManyRuns(t *testing.T) {
	var cells []Cell
	// Names chosen so that sort order != input order; also tiers interleaved.
	for i := 0; i < 40; i++ {
		cells = append(cells, Cell{
			Name:   fmt.Sprintf("cell-%02d", (i*7)%40), // permuted insertion order
			Server: fmt.Sprintf("https://s%02d", i),
			Tier:   []Tier{TierCanary, TierTwo, TierOne}[i%3],
		})
	}
	// Deduplicate names that the permutation may have collided on (Generate
	// rejects dups); keep first occurrence.
	seen := map[string]bool{}
	var uniq []Cell
	for _, c := range cells {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		uniq = append(uniq, c)
	}

	canon := func() string {
		apps, err := baseTemplate().Generate(uniq)
		if err != nil {
			t.Fatalf("[%s] Generate: %v", adversRunID, err)
		}
		b, _ := json.Marshal(apps)
		return string(b)
	}
	first := canon()
	const iters = 300
	for i := 0; i < iters; i++ {
		if got := canon(); got != first {
			t.Fatalf("[%s] Generate nondeterministic at iter %d", adversRunID, i)
		}
	}
	t.Logf("[%s] Generate byte-identical across %d runs over %d cells", adversRunID, iters, len(uniq))
}

// TestAdversarial_PlanRollingSync_DeterministicAcrossManyRuns probes the rollout
// planner. PlanRollingSync groups apps into a map keyed by Tier, sorts each
// tier's names, then iterates a FIXED tier slice. We run MANY times and assert
// byte-identical plans.
func TestAdversarial_PlanRollingSync_DeterministicAcrossManyRuns(t *testing.T) {
	var cells []Cell
	for i := 0; i < 24; i++ {
		cells = append(cells, Cell{
			Name:   fmt.Sprintf("c-%02d", (i*5)%24),
			Server: "s",
			Tier:   []Tier{TierCanary, TierTwo, TierOne}[i%3],
		})
	}
	seen := map[string]bool{}
	var uniq []Cell
	for _, c := range cells {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		uniq = append(uniq, c)
	}
	apps, err := baseTemplate().Generate(uniq)
	if err != nil {
		t.Fatalf("[%s] Generate: %v", adversRunID, err)
	}
	canon := func() string {
		b, _ := json.Marshal(PlanRollingSync(apps).Waves)
		return string(b)
	}
	first := canon()
	const iters = 300
	for i := 0; i < iters; i++ {
		if got := canon(); got != first {
			t.Fatalf("[%s] PlanRollingSync nondeterministic at iter %d:\n first=%s\n got=%s",
				adversRunID, i, first, got)
		}
	}
	t.Logf("[%s] PlanRollingSync byte-identical across %d runs", adversRunID, iters)
}

// ---------------------------------------------------------------------------
// (b) COMPLETENESS / IDEMPOTENCY — every app must appear in the plan exactly
//     once; an unknown tier must not be silently dropped.
// ---------------------------------------------------------------------------

// TestAdversarial_PlanRollingSync_UnknownTierNotSilentlyDropped probes the
// completeness contract. rollingsync.go's tierStepFraction documents that an
// UNRECOGNIZED tier returns 1.0 "so an unknown tier is never silently skipped".
// That promise is only honored if PlanRollingSync actually EMITS a wave for the
// unknown tier. But PlanRollingSync iterates a FIXED slice
// []Tier{TierCanary, TierTwo, TierOne} — so an app carrying any OTHER tier label
// (e.g. a typo "tier-3", or an empty/missing label) is grouped in byTier but
// never emitted, vanishing from the rollout.
//
// SINK-SIDE assertion: every input app name must appear in exactly one wave. If
// an unknown-tier app is dropped, the union of wave apps is a strict subset of
// the input and this bites.
func TestAdversarial_PlanRollingSync_UnknownTierNotSilentlyDropped(t *testing.T) {
	apps := []Application{
		{Metadata: AppMetadata{Name: "canary-1", Labels: map[string]string{LabelTier: string(TierCanary)}}},
		{Metadata: AppMetadata{Name: "prod-1", Labels: map[string]string{LabelTier: string(TierOne)}}},
		// Adversarial: a real app whose tier label is a typo / future ring. The
		// documented contract for an unrecognized tier (tierStepFraction) is "never
		// silently skipped" — so it MUST still be rolled out, not vanish.
		{Metadata: AppMetadata{Name: "rogue-typo", Labels: map[string]string{LabelTier: "tier-3"}}},
		// Adversarial: an app with NO tier label at all (empty tier).
		{Metadata: AppMetadata{Name: "rogue-empty", Labels: map[string]string{}}},
	}

	plan := PlanRollingSync(apps)

	inPlan := map[string]int{}
	for _, w := range plan.Waves {
		for _, name := range w.Apps {
			inPlan[name]++
		}
	}
	var dropped []string
	for _, a := range apps {
		if inPlan[a.Metadata.Name] == 0 {
			dropped = append(dropped, a.Metadata.Name)
		}
	}
	if len(dropped) != 0 {
		t.Fatalf("[%s] PlanRollingSync silently DROPPED %d app(s) with unrecognized/empty tier: %v — "+
			"tierStepFraction documents unknown tiers are 'never silently skipped', but the planner's "+
			"fixed canary/tier-2/tier-1 loop never emits them. Sink: union of wave apps (%d) < input apps (%d).",
			adversRunID, len(dropped), dropped, len(inPlan), len(apps))
	}
	// Also assert exactly-once (no double-apply).
	for name, n := range inPlan {
		if n != 1 {
			t.Fatalf("[%s] app %q appears %d times in plan want exactly 1 (double-apply)", adversRunID, name, n)
		}
	}
	t.Logf("[%s] all %d apps (incl. unknown/empty tier) present exactly once in plan", adversRunID, len(apps))
}

// TestAdversarial_DetectDrift_Idempotent proves reconcile idempotency: once the
// live state matches desired, a second DetectDrift over the converged state is a
// no-op (InSync, empty ToPrune+Missing). Models "apply, converge, re-reconcile".
func TestAdversarial_DetectDrift_Idempotent(t *testing.T) {
	desired := []Resource{
		{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "a"},
		{Group: "", Kind: "Service", Namespace: "ns", Name: "b"},
	}
	// Round 1: live is missing 'b'. ToPrune empty, Missing=[b].
	r1 := DetectDrift(desired, desired[:1])
	if r1.InSync() {
		t.Fatalf("[%s] round1 unexpectedly in-sync", adversRunID)
	}
	// Apply -> live now equals desired. Round 2 MUST be a no-op.
	r2 := DetectDrift(desired, desired)
	if !r2.InSync() {
		t.Fatalf("[%s] reconcile NOT idempotent: second pass over converged state still reports drift: %s",
			adversRunID, canonReport(r2))
	}
	// Round 3 over the same converged state: still a no-op, byte-identical to r2.
	r3 := DetectDrift(desired, desired)
	if canonReport(r2) != canonReport(r3) {
		t.Fatalf("[%s] reconcile not stable: r2=%s r3=%s", adversRunID, canonReport(r2), canonReport(r3))
	}
	t.Logf("[%s] DetectDrift idempotent: converged state re-reconciles to a no-op", adversRunID)
}

// ---------------------------------------------------------------------------
// (c) CONCURRENCY — DetectDrift / Generate / PlanRollingSync called from many
//     goroutines must be race-free and produce stable output (run under -race).
// ---------------------------------------------------------------------------

// TestAdversarial_DetectDrift_Concurrent_Race hammers DetectDrift from many
// goroutines over shared input slices and asserts every result equals the
// single-threaded canonical report. The pure functions must not share mutable
// state across calls. Run under -race to catch any latent shared map/slice.
func TestAdversarial_DetectDrift_Concurrent_Race(t *testing.T) {
	var live []Resource
	for i := 0; i < 50; i++ {
		live = append(live, Resource{Group: "apps", Kind: "X", Namespace: "ns", Name: fmt.Sprintf("r-%02d", i)})
	}
	desired := live[:10]
	want := canonReport(DetectDrift(desired, live))

	const goroutines = 32
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if got := canonReport(DetectDrift(desired, live)); got != want {
					errCh <- got
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if bad, ok := <-errCh; ok {
		t.Fatalf("[%s] concurrent DetectDrift produced divergent report:\n want=%s\n got =%s",
			adversRunID, want, bad)
	}
	t.Logf("[%s] DetectDrift race-free + stable across %d goroutines", adversRunID, goroutines)
}

// TestAdversarial_PlanRollingSync_Concurrent_Race hammers the planner + Generate
// concurrently to surface any shared mutable state (the per-tier map is built
// fresh per call; this proves it).
func TestAdversarial_PlanRollingSync_Concurrent_Race(t *testing.T) {
	cells := threeCells()
	apps, err := baseTemplate().Generate(cells)
	if err != nil {
		t.Fatalf("[%s] Generate: %v", adversRunID, err)
	}
	want, _ := json.Marshal(PlanRollingSync(apps).Waves)

	const goroutines = 32
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// Re-Generate AND re-plan to exercise both map-building paths.
				a, e := baseTemplate().Generate(cells)
				if e != nil {
					errCh <- "generate error: " + e.Error()
					return
				}
				got, _ := json.Marshal(PlanRollingSync(a).Waves)
				if string(got) != string(want) {
					errCh <- string(got)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if bad, ok := <-errCh; ok {
		t.Fatalf("[%s] concurrent Generate+Plan diverged: got=%s want=%s", adversRunID, bad, string(want))
	}
	t.Logf("[%s] Generate+PlanRollingSync race-free + stable across %d goroutines", adversRunID, goroutines)
}
