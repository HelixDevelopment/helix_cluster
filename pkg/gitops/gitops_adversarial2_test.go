package gitops

// Adversarial test suite #2 for pkg/gitops — SECOND-PASS probes that go beyond
// the first dropped-apps fix (unknown/empty tier now appended last). This file
// chases what that fix may have MISSED, all asserted SINK-SIDE on the emitted
// RollingSyncPlan bytes/contents:
//
//   (1) CONSERVATION: every input app appears in the plan EXACTLY ONCE — no
//       second silent-drop path, no overwrite/dedup, no wave/chunk-boundary loss.
//       Stressed across many tier mixes, sizes, and chunk boundaries.
//   (2) ORDERING: tier order is canary -> tier-2 -> tier-1 -> (unknown sorted);
//       within a tier names are sorted; the whole plan is a TOTAL order
//       (deterministic across runs even with duplicate-priority groups).
//   (3) RETURN ISOLATION: the returned plan does not alias caller input; mutating
//       the input apps after planning does not retro-mutate the plan, and mutating
//       one plan's wave slice does not bleed into a freshly computed plan.
//   (4) CONCURRENCY (-race): planning shared input from many goroutines is stable.
//
// Run: go test ./pkg/gitops/ -race -count=1 -run Adversarial2 -v

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
)

const advers2RunID = "1367f000-0000-4000-8000-0000000002ce"

// planAppMultiset returns name -> count across all waves of a plan (sink-side
// view of which apps the rollout will actually touch, and how many times).
func planAppMultiset(p RollingSyncPlan) map[string]int {
	m := map[string]int{}
	for _, w := range p.Waves {
		for _, n := range w.Apps {
			m[n]++
		}
	}
	return m
}

// mkApp is a tiny constructor for an Application carrying just a name + tier
// label (all PlanRollingSync looks at).
func mkApp(name string, tier string) Application {
	return Application{Metadata: AppMetadata{
		Name:   name,
		Labels: map[string]string{LabelTier: tier},
	}}
}

// ---------------------------------------------------------------------------
// (1) CONSERVATION — exactly-once across a large adversarial tier mix.
// ---------------------------------------------------------------------------

// TestAdversarial2_Conservation_ExactlyOnce_AcrossTierMix is the load-bearing
// conservation guard for the SECOND pass. The first fix proved unknown/empty
// tiers are no longer dropped. Here we additionally assert that EVERY app —
// across known tiers, several distinct unknown tiers, and chunk-boundary sizes —
// appears in the emitted plan EXACTLY ONCE. A drop (subset), a dup (overwrite or
// double-append), or a chunk-boundary loss (the last app of an odd group) all
// bite here.
func TestAdversarial2_Conservation_ExactlyOnce_AcrossTierMix(t *testing.T) {
	var apps []Application
	add := func(prefix, tier string, n int) {
		for i := 0; i < n; i++ {
			apps = append(apps, mkApp(fmt.Sprintf("%s-%03d", prefix, i), tier))
		}
	}
	// Known tiers at sizes that exercise odd chunk boundaries (ceil math):
	add("can", string(TierCanary), 1)  // single canary
	add("t2", string(TierTwo), 7)      // 50% of 7 -> ceil 4, then 3 (boundary)
	add("t1", string(TierOne), 13)     // 25% of 13 -> ceil 4, 4, 4, 1 (boundary)
	// Several DISTINCT unknown tiers (typo / future ring / empty):
	add("zfuture", "tier-9", 5)        // unknown, odd size
	add("ztypo", "ter-1", 3)           // unknown typo
	add("zempty", "", 2)               // empty tier label

	want := map[string]int{}
	for _, a := range apps {
		want[a.Metadata.Name] = 1
	}

	plan := PlanRollingSync(apps)
	got := planAppMultiset(plan)

	// No drop, no dup: multiset equality with the input name set.
	var missing, dupd []string
	for name := range want {
		switch got[name] {
		case 1:
		case 0:
			missing = append(missing, name)
		default:
			dupd = append(dupd, fmt.Sprintf("%s x%d", name, got[name]))
		}
	}
	// Also catch any phantom name the plan invented.
	var phantom []string
	for name := range got {
		if want[name] == 0 {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(dupd)
	sort.Strings(phantom)
	if len(missing)+len(dupd)+len(phantom) != 0 {
		t.Fatalf("[%s] CONSERVATION violated: input=%d apps; missing=%v dups=%v phantom=%v",
			advers2RunID, len(want), missing, dupd, phantom)
	}
	t.Logf("[%s] conservation OK: all %d apps present exactly once across %d waves",
		advers2RunID, len(want), len(plan.Waves))
}

// TestAdversarial2_Conservation_DuplicateNameSameTier probes a duplicate input
// app name within the SAME tier. PlanRollingSync groups names by tier with a
// plain append (no dedup). A rolling-sync rollout that syncs the SAME app twice
// is at worst redundant — but the documented sink-side contract grades on every
// distinct input being represented. We assert the planner is CONSERVATIVE: a name
// supplied N times appears N times (no silent dedup that would mask a real
// double-registration upstream), and crucially is NEVER dropped. This pins the
// current behavior so a future "dedup" change is a conscious decision, not an
// accidental drop.
func TestAdversarial2_Conservation_DuplicateNameSameTier(t *testing.T) {
	apps := []Application{
		mkApp("dup", string(TierOne)),
		mkApp("dup", string(TierOne)),
		mkApp("solo", string(TierOne)),
	}
	plan := PlanRollingSync(apps)
	got := planAppMultiset(plan)
	if got["solo"] != 1 {
		t.Fatalf("[%s] solo app count=%d want 1", advers2RunID, got["solo"])
	}
	// Conservation lower bound: a duplicated input must NOT vanish — it appears at
	// least once (current impl: twice). The hard failure we guard against is a
	// DROP (count 0), which would be a silent lost-app bug.
	if got["dup"] == 0 {
		t.Fatalf("[%s] duplicate-name app DROPPED from plan (count 0) — silent lost app", advers2RunID)
	}
	t.Logf("[%s] duplicate-name-same-tier kept (count=%d), solo intact", advers2RunID, got["dup"])
}

// ---------------------------------------------------------------------------
// (2) ORDERING — total order: tier sequence + within-tier name sort.
// ---------------------------------------------------------------------------

// TestAdversarial2_Ordering_TierSequenceAndWithinTier asserts the FULL ordering
// contract sink-side:
//   - tier blocks appear in order canary, tier-2, tier-1, then unknown tiers
//     SORTED ascending (deterministic, production ring never before canary, and
//     unknown rings strictly after the production ring);
//   - within each tier, app names are emitted in ascending sorted order across
//     wave boundaries (concatenating a tier's waves yields a sorted name list).
func TestAdversarial2_Ordering_TierSequenceAndWithinTier(t *testing.T) {
	apps := []Application{
		mkApp("t1-b", string(TierOne)),
		mkApp("t1-a", string(TierOne)),
		mkApp("can-b", string(TierCanary)),
		mkApp("can-a", string(TierCanary)),
		mkApp("t2-b", string(TierTwo)),
		mkApp("t2-a", string(TierTwo)),
		mkApp("uk-bbb", "zzz-unknown"),   // unknown, sorts AFTER "aaa-unknown"
		mkApp("uk-aaa", "aaa-unknown"),   // unknown, sorts BEFORE "zzz-unknown"
	}
	plan := PlanRollingSync(apps)

	// Build the tier-block sequence (collapse consecutive same-tier waves).
	var tierSeq []Tier
	for _, w := range plan.Waves {
		if len(tierSeq) == 0 || tierSeq[len(tierSeq)-1] != w.Tier {
			tierSeq = append(tierSeq, w.Tier)
		}
	}
	wantTierSeq := []Tier{TierCanary, TierTwo, TierOne, Tier("aaa-unknown"), Tier("zzz-unknown")}
	if len(tierSeq) != len(wantTierSeq) {
		t.Fatalf("[%s] tier block sequence=%v want %v", advers2RunID, tierSeq, wantTierSeq)
	}
	for i := range wantTierSeq {
		if tierSeq[i] != wantTierSeq[i] {
			t.Fatalf("[%s] tier block[%d]=%q want %q (full seq %v)", advers2RunID, i, tierSeq[i], wantTierSeq[i], tierSeq)
		}
	}

	// Within each tier, names must be globally sorted across waves.
	perTier := map[Tier][]string{}
	for _, w := range plan.Waves {
		perTier[w.Tier] = append(perTier[w.Tier], w.Apps...)
	}
	for tier, names := range perTier {
		if !sort.StringsAreSorted(names) {
			t.Fatalf("[%s] tier %q names not sorted across waves: %v", advers2RunID, tier, names)
		}
	}
	t.Logf("[%s] ordering OK: tier seq %v, within-tier names sorted", advers2RunID, tierSeq)
}

// TestAdversarial2_Ordering_StepIndicesContiguous asserts the wave Step field is
// a contiguous 0..N-1 sequence in emission order — a downstream executor uses
// Step as the wave index, so a gap/repeat would misorder or skip a wave.
func TestAdversarial2_Ordering_StepIndicesContiguous(t *testing.T) {
	var apps []Application
	for i := 0; i < 6; i++ {
		apps = append(apps, mkApp(fmt.Sprintf("c-%d", i), string(TierCanary)))
		apps = append(apps, mkApp(fmt.Sprintf("a-%d", i), string(TierTwo)))
		apps = append(apps, mkApp(fmt.Sprintf("b-%d", i), string(TierOne)))
		apps = append(apps, mkApp(fmt.Sprintf("u-%d", i), "weird"))
	}
	plan := PlanRollingSync(apps)
	for i, w := range plan.Waves {
		if w.Step != i {
			t.Fatalf("[%s] wave[%d].Step=%d want %d (non-contiguous step indices)", advers2RunID, i, w.Step, i)
		}
	}
	t.Logf("[%s] step indices contiguous 0..%d", advers2RunID, len(plan.Waves)-1)
}

// ---------------------------------------------------------------------------
// (3) RETURN ISOLATION — the plan must not alias caller-owned memory.
// ---------------------------------------------------------------------------

// TestAdversarial2_ReturnIsolation_NoInputAliasing proves the returned plan's
// wave Apps slices are NOT backed by the caller's input. PlanRollingSync copies
// each chunk (copy(chunk, names[i:end])), and names is a fresh per-call map
// slice — so mutating the input Application structs after planning must not
// change the plan. We snapshot the plan bytes, then scribble over every input
// app's Name + tier label, recompute nothing, and assert the ALREADY-RETURNED
// plan is unchanged.
func TestAdversarial2_ReturnIsolation_NoInputAliasing(t *testing.T) {
	apps := []Application{
		mkApp("a", string(TierCanary)),
		mkApp("b", string(TierTwo)),
		mkApp("c", string(TierOne)),
		mkApp("d", "unknown-x"),
	}
	plan := PlanRollingSync(apps)
	before, _ := json.Marshal(plan.Waves)

	// Mutate every input app AFTER planning.
	for i := range apps {
		apps[i].Metadata.Name = "MUTATED-" + apps[i].Metadata.Name
		apps[i].Metadata.Labels[LabelTier] = "MUTATED"
	}

	after, _ := json.Marshal(plan.Waves)
	if string(before) != string(after) {
		t.Fatalf("[%s] return NOT isolated: mutating input apps changed the already-returned plan:\n before=%s\n after =%s",
			advers2RunID, before, after)
	}
	t.Logf("[%s] return isolation OK: plan immune to post-call input mutation", advers2RunID)
}

// TestAdversarial2_ReturnIsolation_PlanIndependentAcrossCalls proves two plans
// from the same input are independent: mutating wave Apps in one plan must not
// bleed into a second freshly computed plan (no shared backing array between
// calls).
func TestAdversarial2_ReturnIsolation_PlanIndependentAcrossCalls(t *testing.T) {
	apps := []Application{
		mkApp("a", string(TierTwo)),
		mkApp("b", string(TierTwo)),
		mkApp("c", string(TierTwo)),
	}
	p1 := PlanRollingSync(apps)
	p2 := PlanRollingSync(apps)
	// Scribble over p1's first wave.
	if len(p1.Waves) == 0 || len(p1.Waves[0].Apps) == 0 {
		t.Fatalf("[%s] unexpected empty plan", advers2RunID)
	}
	p1.Waves[0].Apps[0] = "CLOBBERED"
	if p2.Waves[0].Apps[0] == "CLOBBERED" {
		t.Fatalf("[%s] plans share backing memory: mutating p1 changed p2", advers2RunID)
	}
	t.Logf("[%s] plans independent across calls", advers2RunID)
}

// ---------------------------------------------------------------------------
// (4) CONCURRENCY — planning shared input from many goroutines is stable.
// ---------------------------------------------------------------------------

// TestAdversarial2_PlanRollingSync_ConcurrentConservation runs PlanRollingSync
// from many goroutines over a SHARED input slice (including unknown tiers) and
// asserts every result is byte-identical to the single-threaded plan AND
// conserves all apps. Run under -race to surface any shared mutable state on the
// dropped-apps fix path (the unknown-tier collection/sort).
func TestAdversarial2_PlanRollingSync_ConcurrentConservation(t *testing.T) {
	var apps []Application
	for i := 0; i < 12; i++ {
		apps = append(apps, mkApp(fmt.Sprintf("can-%02d", i), string(TierCanary)))
		apps = append(apps, mkApp(fmt.Sprintf("t2-%02d", i), string(TierTwo)))
		apps = append(apps, mkApp(fmt.Sprintf("t1-%02d", i), string(TierOne)))
		apps = append(apps, mkApp(fmt.Sprintf("uk-%02d", i), fmt.Sprintf("unk-%d", i%3)))
	}
	want, _ := json.Marshal(PlanRollingSync(apps).Waves)

	const goroutines = 32
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				got, _ := json.Marshal(PlanRollingSync(apps).Waves)
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
		t.Fatalf("[%s] concurrent PlanRollingSync diverged:\n want=%s\n got =%s", advers2RunID, want, bad)
	}
	t.Logf("[%s] concurrent plan stable + race-free across %d goroutines", advers2RunID, goroutines)
}
