package hxcregistry

// Adversarial probes for pkg/hxcregistry. Tonight's classes:
//   (a) duplicate-ID handling      — does a second CreateItem of the same id
//                                     corrupt the registry (two rows) or reject?
//   (b) total-order on List/query  — is ListItems deterministic for equal keys?
//   (c) unguarded shared state /   — concurrent writers: data race or lost update?
//       concurrent add-update
//   (d) status/enum validation     — invalid enum accepted? illegal transition or
//                                     Completed-losing-commit-sha silently stored?
//
// Every assertion is SINK-SIDE: actual stored row count, actual stored record,
// actual observed list order — never merely "no panic".

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

// countItems returns the true number of rows in items via a direct COUNT, so a
// duplicate-insert that corrupts the table is visible at the sink, not masked by
// GetItem returning only the first match.
func countItems(t *testing.T, r *Registry) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	return n
}

func countItemsWithID(t *testing.T, r *Registry, id string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(r.rebind(`SELECT COUNT(*) FROM items WHERE hxc_id = ?`), id).Scan(&n); err != nil {
		t.Fatalf("count items by id: %v", err)
	}
	return n
}

func newItem(id, status, loc string) *HXCItem {
	return &HXCItem{
		HXCID: id, Type: "Task", Status: status, Priority: "P1", Phase: 0,
		Title: id + " title", Description: "desc", CurrentLocation: loc,
	}
}

// ---------------------------------------------------------------------------
// (a) DUPLICATE-ID HANDLING
// ---------------------------------------------------------------------------

// TestAdversarial_DuplicateID_RejectedNoSecondRow proves the sink: inserting the
// same hxc_id twice MUST NOT create a second row. The PRIMARY KEY must reject the
// dup, and the registry must stay at exactly one row with the ORIGINAL record
// (no partial corruption from the second attempt).
func TestAdversarial_DuplicateID_RejectedNoSecondRow(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	first := newItem("HXC-100", "Queued", LocationIssues)
	first.Title = "ORIGINAL TITLE"
	if err := reg.CreateItem(first); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second insert, same id, DIFFERENT title (so we can detect if it overwrote).
	dup := newItem("HXC-100", "Completed", LocationFixed)
	dup.Title = "MALICIOUS OVERWRITE"
	err = reg.CreateItem(dup)
	if err == nil {
		t.Fatalf("SINK BUG: duplicate CreateItem(HXC-100) returned nil error; " +
			"registry now has %d rows for that id", countItemsWithID(t, reg, "HXC-100"))
	}
	t.Logf("duplicate create rejected with: %v", err)

	// Sink assertions: exactly one row, and it is the ORIGINAL, untouched.
	if n := countItemsWithID(t, reg, "HXC-100"); n != 1 {
		t.Fatalf("SINK BUG: %d rows for HXC-100 after dup insert, want 1", n)
	}
	if n := countItems(t, reg); n != 1 {
		t.Fatalf("SINK BUG: %d total rows after dup insert, want 1", n)
	}
	got, err := reg.GetItem("HXC-100")
	if err != nil {
		t.Fatalf("get after dup: %v", err)
	}
	if got.Title != "ORIGINAL TITLE" {
		t.Fatalf("SINK BUG: stored title = %q, want ORIGINAL TITLE (dup insert "+
			"partially overwrote the row)", got.Title)
	}
	if got.Status != "Queued" {
		t.Fatalf("SINK BUG: stored status = %q, want Queued (dup mutated status)", got.Status)
	}
}

// TestAdversarial_DuplicateHeadingHash_Rejected proves the secondary uniqueness
// guard: two DIFFERENT ids with the SAME title (=> same heading_hash) must not
// both land, because heading_hash is UNIQUE and the renderer binds on it. If the
// second slipped in, two registry rows would share a re-sync anchor.
func TestAdversarial_DuplicateHeadingHash_Rejected(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	a := newItem("HXC-200", "Queued", LocationIssues)
	a.Title = "Identical Heading"
	b := newItem("HXC-201", "Queued", LocationIssues)
	b.Title = "Identical Heading" // same title => same ComputeHeadingHash

	if err := reg.CreateItem(a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := reg.CreateItem(b); err == nil {
		t.Fatalf("SINK BUG: second item with duplicate heading_hash accepted; "+
			"rows now = %d (heading_hash UNIQUE not enforced)", countItems(t, reg))
	}
	if n := countItems(t, reg); n != 1 {
		t.Fatalf("SINK BUG: %d rows after dup heading_hash, want 1", n)
	}
}

// TestAdversarial_CreateItem_AtomicWithHistory proves CreateItem is ATOMIC: an
// items row and its mandatory 'Opened' audit-history row are committed together or
// not at all. The prod contract (registry.go: "a silently-dropped audit row means
// the history feature is broken for end users ... Do NOT swallow it") means an
// item must NEVER persist without its history row.
//
// Deterministic trigger: we force the SECOND insert (item_history) to fail by
// renaming the item_history table out from under CreateItem, then call it. The
// items INSERT will succeed; the history INSERT will error. The sink contract:
// because the two are one unit of work, the items row MUST be rolled back, so
// after the failed call there are ZERO item rows (no orphan committed without an
// audit trail).
//
// On UNFIXED prod (two autocommit db.Exec calls, no transaction) this FAILS: the
// items row stays committed (1 orphan row) even though CreateItem returned an
// error — exactly the half-written record observed under real write contention.
func TestAdversarial_CreateItem_AtomicWithHistory(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	// Force the history INSERT to fail deterministically: make item_history
	// unwriteable by renaming it away. The items INSERT still succeeds.
	if _, err := reg.db.Exec(`ALTER TABLE item_history RENAME TO item_history_gone`); err != nil {
		t.Fatalf("rename history table: %v", err)
	}

	err = reg.CreateItem(newItem("HXC-ATOMIC", "Queued", LocationIssues))
	if err == nil {
		t.Fatalf("expected CreateItem to fail (history insert must error), got nil")
	}
	t.Logf("CreateItem failed as expected: %v", err)

	// SINK: the failed create must leave NO orphan item row. Under non-atomic prod
	// the items row is already committed in autocommit mode => this is 1, the bug.
	var n int
	if err := reg.db.QueryRow(`SELECT COUNT(*) FROM items WHERE hxc_id = ?`, "HXC-ATOMIC").Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("ATOMICITY BUG: CreateItem returned an error but left %d orphan "+
			"items row(s) with no audit-history row; create must be all-or-nothing", n)
	}
}

// TestAdversarial_UpdateItem_AtomicWithHistory is the UpdateItem counterpart to
// the create-atomicity probe: an UPDATE to items and its 'Updated' history row are
// one unit of work. We seed a row, force the history table away, then update. The
// sink contract: the visible item columns MUST be unchanged (the whole update
// rolled back), never a half-applied mutation with a missing audit row.
//
// On UNFIXED prod the items UPDATE commits in autocommit mode before the history
// INSERT fails, so the mutation persists with no audit row — this FAILS.
func TestAdversarial_UpdateItem_AtomicWithHistory(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-UATOMIC", "Queued", LocationIssues)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := reg.db.Exec(`ALTER TABLE item_history RENAME TO item_history_gone`); err != nil {
		t.Fatalf("rename history table: %v", err)
	}

	it, _ := reg.GetItem("HXC-UATOMIC")
	it.Status = "In progress"
	it.Title = "MUTATED-SHOULD-ROLL-BACK"
	if err := reg.UpdateItem(it); err == nil {
		t.Fatalf("expected UpdateItem to fail (history insert must error), got nil")
	}

	got, err := reg.GetItem("HXC-UATOMIC")
	if err != nil {
		t.Fatalf("get after failed update: %v", err)
	}
	if got.Status != "Queued" || got.Title != "HXC-UATOMIC title" {
		t.Fatalf("ATOMICITY BUG: UpdateItem failed (history insert errored) but the "+
			"items mutation persisted anyway: status=%q title=%q — update must be "+
			"all-or-nothing", got.Status, got.Title)
	}
}

// ---------------------------------------------------------------------------
// (b) TOTAL-ORDER ON List/query
// ---------------------------------------------------------------------------

// TestAdversarial_ListItems_DeterministicTotalOrder runs ListItems many times
// over rows that DELIBERATELY collide on the leading sort keys (same phase, same
// priority) so the tie-breaker (hxc_id) is what decides order. If the query had
// no total-order tie-break, the equal-key rows could come back in unstable order
// across runs. We assert: (1) the order equals the deterministic phase,priority,
// hxc_id sort, and (2) it is byte-identical across many repetitions.
func TestAdversarial_ListItems_DeterministicTotalOrder(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	// All P1, all phase 0 => only hxc_id breaks the tie. Insert in shuffled order.
	ids := []string{"HXC-050", "HXC-007", "HXC-099", "HXC-013", "HXC-031", "HXC-002"}
	insertOrder := []string{"HXC-031", "HXC-099", "HXC-002", "HXC-050", "HXC-013", "HXC-007"}
	for _, id := range insertOrder {
		if err := reg.CreateItem(newItem(id, "Queued", LocationIssues)); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	want := append([]string(nil), ids...)
	sort.Strings(want) // deterministic phase,priority,hxc_id == hxc_id here

	var firstOrder []string
	for run := 0; run < 50; run++ {
		items, err := reg.ListItems("")
		if err != nil {
			t.Fatalf("list run %d: %v", run, err)
		}
		got := make([]string, len(items))
		for i, it := range items {
			got[i] = it.HXCID
		}
		if run == 0 {
			firstOrder = got
			if !equalSlice(got, want) {
				t.Fatalf("SINK BUG: list order = %v, want deterministic %v", got, want)
			}
			continue
		}
		if !equalSlice(got, firstOrder) {
			t.Fatalf("SINK BUG: list order unstable across runs: run %d = %v, run 0 = %v",
				run, got, firstOrder)
		}
	}
}

func equalSlice(a, b []string) bool {
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

// ---------------------------------------------------------------------------
// (c) UNGUARDED SHARED MUTABLE STATE / concurrent add + update
// ---------------------------------------------------------------------------

// TestAdversarial_ConcurrentCreate_NoDuplicateNoRace hammers CreateItem of the
// SAME id from many goroutines. The sink contract is dedup, NOT liveness: AT MOST
// ONE create may produce a row, and the stored row count for that id must EQUAL
// the number of reported successes. Two outcomes are both legitimate:
//   - exactly one writer commits (the rest get a UNIQUE-violation error), or
//   - zero writers commit (pure SQLITE_BUSY contention failed them all transiently).
// What is FORBIDDEN — the bug this bites — is TWO rows for one id, or a success
// count that disagrees with the stored row count (a phantom/duplicate write). Run
// under -race; a data race is also a failure.
func TestAdversarial_ConcurrentCreate_NoDuplicateNoRace(t *testing.T) {
	tmp := t.TempDir() + "/concurrent_create.db"
	reg, err := Open(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	const goroutines = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successes int
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			it := newItem("HXC-300", "Queued", LocationIssues)
			if err := reg.CreateItem(it); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Dedup: never more than one row, never more than one reported success.
	if successes > 1 {
		t.Fatalf("SINK BUG: %d concurrent creates of HXC-300 succeeded, want at most 1 "+
			"(duplicate-create dedup violated)", successes)
	}
	n := countItemsWithID(t, reg, "HXC-300")
	if n > 1 {
		t.Fatalf("SINK BUG: %d rows for HXC-300 after concurrent create, want at most 1", n)
	}
	// Truthfulness: a reported success MUST correspond to a stored row, and vice
	// versa. successes==1 <=> exactly one row; successes==0 <=> zero rows.
	if n != successes {
		t.Fatalf("SINK BUG: %d rows for HXC-300 but %d creates reported success "+
			"(phantom write or lost commit)", n, successes)
	}
}

// TestAdversarial_ConcurrentUpdate_LostUpdate documents the read-modify-write
// window. Many goroutines each do GetItem -> set a distinct phase -> UpdateItem.
// UpdateItem performs a full-row overwrite with NO optimistic-concurrency guard
// (no version/last_modified predicate in the WHERE), so this is last-writer-wins
// at the row level. We assert the SINK still holds a coherent single row whose
// phase is ONE of the values some writer actually set (no torn/corrupt row, no
// extra rows). This pins the documented serialization behaviour and would catch a
// data race under -race or a corrupted/duplicated row.
func TestAdversarial_ConcurrentUpdate_CoherentSingleRow(t *testing.T) {
	tmp := t.TempDir() + "/concurrent_update.db"
	reg, err := Open(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-400", "Queued", LocationIssues)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const goroutines = 12
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		phase := i + 1
		go func() {
			defer wg.Done()
			it, err := reg.GetItem("HXC-400")
			if err != nil {
				return
			}
			it.Phase = phase
			it.Title = fmt.Sprintf("writer-%d", phase)
			_ = reg.UpdateItem(it) // last-writer-wins; ignore serialization errors
		}()
	}
	wg.Wait()

	if n := countItemsWithID(t, reg, "HXC-400"); n != 1 {
		t.Fatalf("SINK BUG: %d rows for HXC-400 after concurrent updates, want 1", n)
	}
	got, err := reg.GetItem("HXC-400")
	if err != nil {
		t.Fatalf("get after concurrent update: %v", err)
	}
	// Coherence contract: the final row must be EITHER the untouched seed (phase 0,
	// seeded title — legitimate when SQLite write contention causes every writer to
	// hit SQLITE_BUSY and fail, so none committed) OR a row whose phase and title
	// come from the SAME writer. What is FORBIDDEN is a TORN row: a phase from one
	// writer fused with a title from another, which would prove UpdateItem's
	// phase+title write was not atomic / a data race corrupted the row.
	var wantTitle string
	if got.Phase == 0 {
		wantTitle = "HXC-400 title" // seed survived; no writer won
	} else if got.Phase >= 1 && got.Phase <= goroutines {
		wantTitle = fmt.Sprintf("writer-%d", got.Phase)
	} else {
		t.Fatalf("SINK BUG: final phase = %d outside {0}∪[1,%d]; row is corrupt",
			got.Phase, goroutines)
	}
	if got.Title != wantTitle {
		t.Fatalf("SINK BUG: torn row — phase=%d implies title %q but stored title=%q "+
			"(UpdateItem writes phase+title atomically, so they must agree)",
			got.Phase, wantTitle, got.Title)
	}
}

// ---------------------------------------------------------------------------
// (d) STATUS-TRANSITION / ENUM VALIDATION
// ---------------------------------------------------------------------------

// TestAdversarial_InvalidEnums_RejectedAtSink proves that invalid type/status/
// priority are rejected and NEVER reach the table. Validate() is the gate; the DB
// CHECK constraints are the backstop. Either way the sink must hold zero rows.
func TestAdversarial_InvalidEnums_RejectedAtSink(t *testing.T) {
	cases := []struct {
		name string
		item *HXCItem
	}{
		{"bad type", &HXCItem{HXCID: "HXC-501", Type: "Epic", Status: "Queued", Priority: "P0", Title: "T", Description: "D", CurrentLocation: LocationIssues}},
		{"bad status", &HXCItem{HXCID: "HXC-502", Type: "Task", Status: "Open", Priority: "P0", Title: "T", Description: "D", CurrentLocation: LocationIssues}},
		{"bad priority", &HXCItem{HXCID: "HXC-503", Type: "Task", Status: "Queued", Priority: "P9", Title: "T", Description: "D", CurrentLocation: LocationIssues}},
		{"bad location", &HXCItem{HXCID: "HXC-504", Type: "Task", Status: "Queued", Priority: "P0", Title: "T", Description: "D", CurrentLocation: "Limbo"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, err := Open(":memory:")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer reg.Close()
			if err := reg.CreateItem(c.item); err == nil {
				t.Fatalf("SINK BUG: CreateItem accepted invalid item %+v; rows=%d",
					c.item, countItems(t, reg))
			}
			if n := countItems(t, reg); n != 0 {
				t.Fatalf("SINK BUG: %d rows after rejected invalid create, want 0", n)
			}
		})
	}
}

// TestAdversarial_UpdateToInvalidStatus_Rejected proves an UpdateItem cannot move
// a row to an invalid status. The PRE-update row must survive unchanged at the
// sink (no partial mutation of the other columns).
func TestAdversarial_UpdateToInvalidStatus_Rejected(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-600", "Queued", LocationIssues)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	bad, _ := reg.GetItem("HXC-600")
	bad.Status = "Done" // not a valid enum
	bad.Title = "should-not-persist"
	if err := reg.UpdateItem(bad); err == nil {
		t.Fatalf("SINK BUG: UpdateItem to invalid status accepted")
	}
	got, _ := reg.GetItem("HXC-600")
	if got.Status != "Queued" || got.Title != "HXC-600 title" {
		t.Fatalf("SINK BUG: rejected update still mutated row: status=%q title=%q",
			got.Status, got.Title)
	}
}

// TestAdversarial_CompletedKeepsCommitSHA proves a Completed update does NOT lose
// the commit sha — the closure evidence the renderer needs. We set a status of
// Completed plus a commit sha and assert the sink stored BOTH.
func TestAdversarial_CompletedKeepsCommitSHA(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-700", "In progress", LocationIssues)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	it, _ := reg.GetItem("HXC-700")
	it.Status = "Completed"
	it.CommitSHA = "deadbeefcafe1234"
	it.CurrentLocation = LocationFixed
	if err := reg.UpdateItem(it); err != nil {
		t.Fatalf("update to completed: %v", err)
	}
	got, err := reg.GetItem("HXC-700")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "Completed" {
		t.Fatalf("SINK BUG: status = %q, want Completed", got.Status)
	}
	if got.CommitSHA != "deadbeefcafe1234" {
		t.Fatalf("SINK BUG: Completed item lost commit sha: got %q", got.CommitSHA)
	}
}

// TestAdversarial_StatusTransition_NoGuard_LATENT documents (does NOT fail) that
// the registry permits ANY enum->enum transition, including Completed -> Queued
// ("reopening" a closed item with no audit gate). Validate() checks only enum
// membership, never transition legality; there is no documented transition
// contract to enforce, so this is a LATENT RISK pinned as observed behaviour, not
// a bug. If a transition guard is ever added, this test will flip and must be
// revisited.
func TestAdversarial_StatusTransition_NoGuard_LATENT(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-800", "Completed", LocationFixed)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	it, _ := reg.GetItem("HXC-800")
	it.Status = "Queued" // illegal "reopen" of a terminal item — accepted today
	if err := reg.UpdateItem(it); err != nil {
		t.Fatalf("LATENT NOTE changed: Completed->Queued now rejected (%v); a "+
			"transition guard was added — revisit this probe", err)
	}
	got, _ := reg.GetItem("HXC-800")
	if got.Status != "Queued" {
		t.Fatalf("expected Completed->Queued to be accepted (no guard), got status=%q", got.Status)
	}
	t.Logf("LATENT RISK observed: Completed->Queued accepted with no transition guard")
}

// TestAdversarial_UpdateDoesNotAutoMoveLocation_LATENT documents (does NOT fail)
// that UpdateItem stores current_location verbatim and does NOT derive it from
// status via CanonicalLocation. A caller that flips Status to Completed but leaves
// CurrentLocation=Issues persists an inconsistent row until Reconcile runs. This
// is a LATENT RISK, not a bug: Reconcile is the documented owner of location
// correctness (its doc calls itself the backfill path), and no contract promises
// UpdateItem auto-moves. If UpdateItem ever starts auto-moving, this flips.
func TestAdversarial_UpdateDoesNotAutoMoveLocation_LATENT(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	if err := reg.CreateItem(newItem("HXC-900", "In progress", LocationIssues)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	it, _ := reg.GetItem("HXC-900")
	it.Status = "Completed"          // terminal => canonical location is Fixed
	it.CurrentLocation = LocationIssues // caller leaves it stale
	if err := reg.UpdateItem(it); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := reg.GetItem("HXC-900")
	canonical := CanonicalLocation(got.Status)
	if got.CurrentLocation == canonical {
		t.Fatalf("LATENT NOTE changed: UpdateItem now auto-moved location to %q "+
			"(canonical); the update path gained auto-move — revisit this probe", got.CurrentLocation)
	}
	// Sink truth: stored location is the stale Issues, inconsistent with status.
	if got.CurrentLocation != LocationIssues {
		t.Fatalf("expected stale Issues persisted verbatim, got %q", got.CurrentLocation)
	}
	// And Reconcile (the documented owner) fixes it.
	if _, err := reg.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	fixed, _ := reg.GetItem("HXC-900")
	if fixed.CurrentLocation != canonical {
		t.Fatalf("SINK BUG: Reconcile failed to fix stranded row: loc=%q want %q",
			fixed.CurrentLocation, canonical)
	}
	t.Logf("LATENT RISK observed: UpdateItem persists stale location %q for status %q; "+
		"Reconcile corrects it to %q", LocationIssues, got.Status, canonical)
}
