package health

// Adversarial probes for pkg/health.
//
// Tonight's classes:
//   (a) UNGUARDED SHARED MUTABLE STATE — concurrent register/deregister/read of
//       the DependencyRollup deps map and the StartupGate latch map, under -race,
//       with a conservation assertion (every registered check is observable).
//   (b) FAIL-OPEN THRESHOLD — does an aggregate ever report HEALTHY when an input
//       is malformed / unknown-status, or is a boundary misclassified? Asserts the
//       actual reported overall Status (sink-side), not "no panic".
//   (c) TOTAL-ORDER / IDENTITY — equal/duplicate keys, deterministic aggregation
//       and report ordering.
//
// Every probe asserts SINK-SIDE behaviour: the actual reported overall Status,
// the actual count of observable checks, or the actual ordering — never merely
// the absence of a panic.

import (
	"context"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) UNGUARDED SHARED MUTABLE STATE — DependencyRollup.deps map under -race.
//
// Contract (rollup.go): DependencyRollup guards deps with sync.RWMutex; Register
// write-locks, check() snapshots under RLock. Run -race with many goroutines
// concurrently registering and reading. CONSERVATION: after the storm, every
// distinct name we registered must be observable via CheckAll. A data race or a
// lost update would surface as a -race report or a short count.
// ---------------------------------------------------------------------------

func TestAdversarial_RollupConcurrentRegisterRead_Conservation(t *testing.T) {
	t.Parallel()
	r := NewRollup()

	const writers = 16
	const namesPerWriter = 25
	okCheck := func(context.Context) error { return nil }

	var wg sync.WaitGroup
	// Writers register distinct names concurrently.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < namesPerWriter; i++ {
				r.Register(Dependency{
					Name:     name(w, i),
					Kind:     Readiness,
					Critical: false,
					Check:    okCheck,
				})
			}
		}(w)
	}
	// Concurrent readers race the writers (must not race on the map).
	for rdr := 0; rdr < 8; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = r.CheckReadiness(context.Background())
			}
		}()
	}
	wg.Wait()

	// CONSERVATION (sink-side): every registered name is present exactly once.
	final := r.CheckReadiness(context.Background())
	seen := make(map[string]int, len(final.Checks))
	for _, c := range final.Checks {
		seen[c.Name]++
	}
	want := writers * namesPerWriter
	if len(final.Checks) != want {
		t.Fatalf("conservation: got %d distinct checks, want %d", len(final.Checks), want)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < namesPerWriter; i++ {
			n := name(w, i)
			if seen[n] != 1 {
				t.Fatalf("conservation: check %q observed %d times, want 1", n, seen[n])
			}
		}
	}
	// Sink-side: all healthy ⇒ overall Healthy.
	if final.Status != Healthy {
		t.Fatalf("overall status = %q, want %q", final.Status, Healthy)
	}
}

// (a') StartupGate concurrent MarkComplete/IsComplete — doc: "safe for concurrent
// use" + latched. Concurrent latching of distinct + shared keys under -race;
// conservation: every key marked is observed complete.
func TestAdversarial_StartupGateConcurrentLatch_Conservation(t *testing.T) {
	t.Parallel()
	g := NewStartupGate()
	const keys = 100
	var wg sync.WaitGroup
	for k := 0; k < keys; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			n := name(0, k)
			g.MarkComplete(n)
			_ = g.IsComplete(n)
		}(k)
	}
	// Readers hammering a shared key concurrently with the writer of that key.
	for rdr := 0; rdr < 16; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < keys; i++ {
				_ = g.IsComplete(name(0, i))
			}
		}()
	}
	wg.Wait()
	for k := 0; k < keys; k++ {
		if !g.IsComplete(name(0, k)) {
			t.Fatalf("latch lost: key %q not complete after MarkComplete", name(0, k))
		}
	}
}

// (a'') CompositeChecker concurrent AddCheck/RemoveCheck/Check under -race.
// package.go guards checks with sync.RWMutex.
func TestAdversarial_CompositeCheckerConcurrent_Race(t *testing.T) {
	t.Parallel()
	cc := NewCompositeChecker()
	var wg sync.WaitGroup
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				n := name(w, i%10)
				cc.AddCheck(n, func() Status { return Healthy })
				if i%3 == 0 {
					cc.RemoveCheck(n)
				}
			}
		}(w)
	}
	for rdr := 0; rdr < 6; rdr++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = cc.Check()
			}
		}()
	}
	wg.Wait()
	// Sink-side: a final deterministic state is reportable without a race.
	cc.AddCheck("final", func() Status { return Unhealthy })
	overall, _ := cc.Check()
	if overall != Unhealthy {
		t.Fatalf("after adding an Unhealthy check, overall = %q, want %q", overall, Unhealthy)
	}
}

// ---------------------------------------------------------------------------
// (b) FAIL-OPEN THRESHOLD — CompositeChecker.Check().
//
// DOCUMENTED CONTRACT (package.go): "Unhealthy if any check is Unhealthy;
// Degraded if any check is Degraded and none are Unhealthy; Healthy otherwise."
//
// The "Healthy otherwise" clause means an UNRECOGNISED status value (e.g. the
// Starting status, or an empty/garbage Status) is folded to Healthy. This pins
// that contract HONESTLY: it is a disclaim ("otherwise ⇒ Healthy"), so we do not
// call it a bug — but we DOCUMENT the sink-side fail-open so a caller cannot be
// surprised. If a future change tightens the rule (unknown ⇒ not-Healthy) this
// test will flip and force a doc update.
// ---------------------------------------------------------------------------

func TestAdversarial_CompositeChecker_UnknownStatusFoldsToHealthy_PinsContract(t *testing.T) {
	t.Parallel()
	cc := NewCompositeChecker()
	cc.AddCheck("known-healthy", func() Status { return Healthy })
	cc.AddCheck("garbage", func() Status { return Status("not-a-real-status") })
	cc.AddCheck("starting", func() Status { return Starting })
	cc.AddCheck("empty", func() Status { return Status("") })

	overall, results := cc.Check()
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}
	// SINK-SIDE: per the documented "Healthy otherwise" clause, none of the
	// unknown statuses raise the overall above Healthy.
	if overall != Healthy {
		t.Fatalf("documented contract: unknown statuses fold to Healthy; got overall %q", overall)
	}
	// But the individual non-Healthy statuses ARE preserved in the per-check
	// results — the fold is only in the AGGREGATE, so a caller inspecting checks
	// can still see them.
	byName := map[string]Status{}
	for _, c := range results {
		byName[c.Name] = c.Status
	}
	if byName["starting"] != Starting {
		t.Fatalf("per-check status for 'starting' = %q, want %q", byName["starting"], Starting)
	}
}

// (b') DependencyRollup is FAIL-CLOSED on unknown/non-Healthy status: a check
// returning a non-nil error (any failure) drives Degraded/Unhealthy, never
// Healthy. This contrasts with CompositeChecker and pins the safer behaviour.
func TestAdversarial_Rollup_NonNilErrorNeverFoldsToHealthy(t *testing.T) {
	t.Parallel()
	r := NewRollup()
	r.Register(Dependency{
		Name:     "ok",
		Kind:     Readiness,
		Critical: false,
		Check:    func(context.Context) error { return nil },
	})
	r.Register(Dependency{
		Name:     "flaky-noncritical",
		Kind:     Readiness,
		Critical: false,
		Check:    func(context.Context) error { return context.Canceled },
	})
	got := r.CheckReadiness(context.Background())
	// SINK-SIDE: a non-critical failure must DEGRADE, never stay Healthy.
	if got.Status != Degraded {
		t.Fatalf("non-critical failure: overall = %q, want %q (fail-open detected if Healthy)", got.Status, Degraded)
	}

	// Now a critical failure must drive Unhealthy (absorbing).
	r.Register(Dependency{
		Name:     "critical-down",
		Kind:     Readiness,
		Critical: true,
		Check:    func(context.Context) error { return context.Canceled },
	})
	got = r.CheckReadiness(context.Background())
	if got.Status != Unhealthy {
		t.Fatalf("critical failure: overall = %q, want %q", got.Status, Unhealthy)
	}
}

// (b'') Boundary: a nil Check is misconfiguration. runOne reports it Unhealthy
// with a named error rather than treating the absent probe as Healthy (a classic
// fail-open). Pin the sink: a critical nil-check ⇒ overall Unhealthy.
func TestAdversarial_Rollup_NilCheckIsUnhealthyNotFailOpen(t *testing.T) {
	t.Parallel()
	r := NewRollup()
	r.Register(Dependency{
		Name:     "misconfigured",
		Kind:     Readiness,
		Critical: true,
		Check:    nil, // no probe registered
	})
	got := r.CheckReadiness(context.Background())
	if got.Status != Unhealthy {
		t.Fatalf("nil-check critical dep: overall = %q, want %q (fail-open if Healthy)", got.Status, Unhealthy)
	}
	if len(got.Checks) != 1 || got.Checks[0].Status != Unhealthy {
		t.Fatalf("per-check status not Unhealthy for nil check: %+v", got.Checks)
	}
}

// ---------------------------------------------------------------------------
// (c) TOTAL-ORDER / IDENTITY.
//
// (c1) DependencyRollup output ordering: with many equal-severity checks, the
// Rollup.Checks slice must be DETERMINISTICALLY ordered (by name) regardless of
// map iteration order. Run repeatedly; the order must be identical every time.
// ---------------------------------------------------------------------------

func TestAdversarial_Rollup_DeterministicOrderingUnderEqualSeverity(t *testing.T) {
	t.Parallel()
	r := NewRollup()
	const n = 30
	for i := 0; i < n; i++ {
		r.Register(Dependency{
			Name:     name(0, i),
			Kind:     Readiness,
			Critical: false,
			Check:    func(context.Context) error { return nil },
		})
	}
	var prev []string
	for iter := 0; iter < 25; iter++ {
		got := r.CheckReadiness(context.Background())
		order := make([]string, len(got.Checks))
		for i, c := range got.Checks {
			order[i] = c.Name
		}
		// Must be sorted ascending.
		for i := 1; i < len(order); i++ {
			if order[i-1] >= order[i] {
				t.Fatalf("iter %d: results not strictly name-sorted: %v", iter, order)
			}
		}
		if prev != nil {
			for i := range order {
				if order[i] != prev[i] {
					t.Fatalf("iter %d: nondeterministic order at %d: %q vs %q", iter, i, order[i], prev[i])
				}
			}
		}
		prev = order
	}
}

// (c2) DUPLICATE / IDENTITY: Register is documented to "add or replace by name".
// Registering the same name twice must NOT produce two entries (no clobbering
// into duplicate rows) — the second registration replaces the first, and the
// replacing Check is the one that runs (sink-side).
func TestAdversarial_Rollup_DuplicateNameReplacesNotDuplicates(t *testing.T) {
	t.Parallel()
	r := NewRollup()
	r.Register(Dependency{
		Name:     "dup",
		Kind:     Readiness,
		Critical: true,
		Check:    func(context.Context) error { return context.Canceled }, // would be Unhealthy
	})
	// Replace with a healthy critical check of the same name.
	r.Register(Dependency{
		Name:     "dup",
		Kind:     Readiness,
		Critical: true,
		Check:    func(context.Context) error { return nil }, // Healthy
	})
	got := r.CheckReadiness(context.Background())
	if len(got.Checks) != 1 {
		t.Fatalf("duplicate name produced %d rows, want 1 (replace, not duplicate): %+v", len(got.Checks), got.Checks)
	}
	// SINK-SIDE: the REPLACING (healthy) check is the one that ran ⇒ overall Healthy.
	if got.Status != Healthy {
		t.Fatalf("replacing check did not take effect: overall = %q, want %q", got.Status, Healthy)
	}
}

// (c3) CompositeChecker duplicate name: AddCheck twice with same name replaces;
// the last-added function is the one evaluated.
func TestAdversarial_CompositeChecker_DuplicateNameReplaces(t *testing.T) {
	t.Parallel()
	cc := NewCompositeChecker()
	cc.AddCheck("svc", func() Status { return Unhealthy })
	cc.AddCheck("svc", func() Status { return Healthy }) // replaces
	overall, results := cc.Check()
	if len(results) != 1 {
		t.Fatalf("duplicate AddCheck produced %d results, want 1: %+v", len(results), results)
	}
	if overall != Healthy {
		t.Fatalf("last-write-wins broken: overall = %q, want %q", overall, Healthy)
	}
}

// name builds a stable, unique check name for writer w / index i.
func name(w, i int) string {
	return "chk-" + itoa(w) + "-" + itoa(i)
}

// itoa avoids importing strconv into the adversarial file's hot loops and keeps
// names lexicographically meaningful only by (w,i) identity, which the ordering
// test does not depend on (it only requires a deterministic total order).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
