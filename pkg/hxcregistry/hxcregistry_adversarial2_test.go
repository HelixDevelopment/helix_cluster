package hxcregistry

// Second-pass adversarial probes for pkg/hxcregistry.
//
// The first atomicity fix wrapped CreateItem/UpdateItem (item row + audit-history
// row) in a transaction. This file hunts what that pass MISSED, focusing on OTHER
// multi-step mutations and on injection/conservation invariants:
//
//   (e) Reconcile atomicity   — Reconcile issues TWO separate db.Exec UPDATEs in
//                               autocommit mode with NO surrounding transaction. If
//                               the second statement errors after the first commits,
//                               the registry is left in a PARTIAL state: half the
//                               rows moved, half not. A reconcile is conceptually one
//                               unit of work and must be all-or-nothing.
//   (f) conservation          — every CreateItem yields EXACTLY one item row and
//                               EXACTLY one 'Opened' audit row; every UpdateItem adds
//                               EXACTLY one 'Updated' row (no orphans, no doubles).
//   (g) SQL injection          — a hostile hxc_id / title / status flows through
//                               parameterized statements and CANNOT inject SQL.
//
// Every assertion is SINK-SIDE: real stored row counts / real stored locations.

import (
	"fmt"
	"sync"
	"testing"
)

// countLoc returns the number of item rows currently in the given location.
func countLoc(t *testing.T, r *Registry, loc string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(r.rebind(`SELECT COUNT(*) FROM items WHERE current_location = ?`), loc).Scan(&n); err != nil {
		t.Fatalf("count loc %q: %v", loc, err)
	}
	return n
}

func countHist(t *testing.T, r *Registry, id, event string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(r.rebind(`SELECT COUNT(*) FROM item_history WHERE hxc_id = ? AND event_type = ?`), id, event).Scan(&n); err != nil {
		t.Fatalf("count hist %s/%s: %v", id, event, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// (e) RECONCILE ATOMICITY — the multi-step mutation the first fix did not cover
// ---------------------------------------------------------------------------

// TestAdversarial_Reconcile_AtomicAcrossBothPartitions proves Reconcile is a single
// all-or-nothing unit of work across its TWO update statements.
//
// Reconcile moves rows in two passes:
//   pass 1: terminal-status rows whose location <> 'Fixed'  -> set location 'Fixed'
//   pass 2: active-status   rows whose location <> 'Issues' -> set location 'Issues'
//
// We seed rows that BOTH passes must touch, then force pass 2 to FAIL deterministically
// while letting pass 1 succeed, using a BEFORE UPDATE trigger that raises only when a
// row is being moved TO 'Issues'. The sink contract: because a reconcile is one unit
// of work, a failure in pass 2 MUST roll back pass 1 too — so NO row's location may
// have changed. On the current code (two autocommit db.Exec calls, no transaction)
// pass 1 has already committed by the time pass 2 errors, leaving a PARTIAL reconcile:
// the terminal rows moved to Fixed while the active rows are stranded. That partial
// state is the bug this bites.
func TestAdversarial_Reconcile_AtomicAcrossBothPartitions(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	// pass-1 victims: terminal status stranded in Issues -> should move to Fixed.
	if err := reg.CreateItem(newItem("HXC-E01", "Completed", LocationIssues)); err != nil {
		t.Fatalf("seed E01: %v", err)
	}
	if err := reg.CreateItem(newItem("HXC-E02", "Obsolete", LocationIssues)); err != nil {
		t.Fatalf("seed E02: %v", err)
	}
	// pass-2 victim: active status stranded in Fixed -> should move to Issues (this
	// move is what the trigger blocks, forcing pass 2 to error).
	if err := reg.CreateItem(newItem("HXC-E03", "In progress", LocationFixed)); err != nil {
		t.Fatalf("seed E03: %v", err)
	}

	// Snapshot the pre-reconcile locations so we can assert "nothing moved" on rollback.
	beforeFixed := countLoc(t, reg, LocationFixed)   // {E03}
	beforeIssues := countLoc(t, reg, LocationIssues) // {E01, E02}

	// Trigger: any UPDATE that sets current_location to 'Issues' aborts. This makes
	// Reconcile's SECOND statement (active -> Issues) fail, while the FIRST
	// statement (terminal -> Fixed) is unaffected and succeeds.
	if _, err := reg.db.Exec(`
		CREATE TRIGGER block_move_to_issues
		BEFORE UPDATE ON items
		FOR EACH ROW WHEN NEW.current_location = 'Issues' AND OLD.current_location <> 'Issues'
		BEGIN
			SELECT RAISE(ABORT, 'pass-2 forced failure');
		END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	_, rerr := reg.Reconcile()
	if rerr == nil {
		t.Fatalf("expected Reconcile to fail (pass-2 update must error via trigger), got nil")
	}
	t.Logf("Reconcile failed as expected: %v", rerr)

	// SINK: a failed reconcile must leave the location distribution byte-identical to
	// before (full rollback). Under the non-atomic current code, pass 1 already
	// committed E01+E02 to Fixed, so afterFixed = beforeFixed + 2 and afterIssues =
	// beforeIssues - 2 — a partial, half-applied reconcile.
	afterFixed := countLoc(t, reg, LocationFixed)
	afterIssues := countLoc(t, reg, LocationIssues)
	if afterFixed != beforeFixed || afterIssues != beforeIssues {
		t.Fatalf("RECONCILE ATOMICITY BUG: a failed Reconcile left a PARTIAL state. "+
			"Fixed: %d -> %d (want unchanged %d); Issues: %d -> %d (want unchanged %d). "+
			"pass 1 committed before pass 2 failed; the two UPDATEs must be one transaction.",
			beforeFixed, afterFixed, beforeFixed, beforeIssues, afterIssues, beforeIssues)
	}
}

// ---------------------------------------------------------------------------
// (f) CONSERVATION — one create == one item row + one 'Opened' audit row
// ---------------------------------------------------------------------------

// TestAdversarial_Conservation_CreateUpdateAuditCounts proves the audit ledger
// conserves exactly: a single CreateItem yields exactly one item row and exactly one
// 'Opened' history row; each subsequent UpdateItem yields exactly one additional
// 'Updated' row and never a second 'Opened'. A double-write (audit insert outside the
// dedup/atomic boundary) or a swallowed insert would show up as the wrong count here.
func TestAdversarial_Conservation_CreateUpdateAuditCounts(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	const id = "HXC-F01"
	if err := reg.CreateItem(newItem(id, "Queued", LocationIssues)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := countItemsWithID(t, reg, id); got != 1 {
		t.Fatalf("conservation: %d item rows after one create, want 1", got)
	}
	if got := countHist(t, reg, id, "Opened"); got != 1 {
		t.Fatalf("conservation: %d 'Opened' rows after one create, want exactly 1", got)
	}

	const updates = 4
	for i := 0; i < updates; i++ {
		it, gerr := reg.GetItem(id)
		if gerr != nil {
			t.Fatalf("get %d: %v", i, gerr)
		}
		it.Phase = i + 1
		if err := reg.UpdateItem(it); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	// Still exactly one 'Opened'; exactly one 'Updated' per UpdateItem; still one row.
	if got := countItemsWithID(t, reg, id); got != 1 {
		t.Fatalf("conservation: %d item rows after %d updates, want 1", got, updates)
	}
	if got := countHist(t, reg, id, "Opened"); got != 1 {
		t.Fatalf("conservation: %d 'Opened' rows after updates, want exactly 1 (a "+
			"create-history insert leaked into the update path)", got)
	}
	if got := countHist(t, reg, id, "Updated"); got != updates {
		t.Fatalf("conservation: %d 'Updated' rows after %d updates, want %d", got, updates, updates)
	}
}

// ---------------------------------------------------------------------------
// (g) SQL INJECTION — hostile id/title/status cannot escape the placeholders
// ---------------------------------------------------------------------------

// TestAdversarial_Injection_HostileIDTitleAreData proves the CRUD statements are
// parameterized: an hxc_id / title containing SQL metacharacters is stored and read
// back VERBATIM as data and cannot drop tables or inject a second row. A classic
// "Robert'); DROP TABLE items;--" payload must survive as a literal id, and the
// items table must still exist with exactly the rows we created.
func TestAdversarial_Injection_HostileIDTitleAreData(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	hostileID := "HXC-'); DROP TABLE items;--"
	hostileTitle := "evil'); DELETE FROM items; SELECT('"
	it := newItem(hostileID, "Queued", LocationIssues)
	it.Title = hostileTitle
	if err := reg.CreateItem(it); err != nil {
		t.Fatalf("create hostile: %v", err)
	}

	// Table still exists and holds exactly the one row.
	if n := countItems(t, reg); n != 1 {
		t.Fatalf("INJECTION BUG: %d rows after hostile create, want 1 (a payload executed)", n)
	}
	// Round-trips verbatim — proving it was bound as a parameter, not interpolated.
	got, gerr := reg.GetItem(hostileID)
	if gerr != nil {
		t.Fatalf("INJECTION BUG: hostile id not retrievable verbatim: %v", gerr)
	}
	if got.HXCID != hostileID || got.Title != hostileTitle {
		t.Fatalf("INJECTION BUG: stored id/title not verbatim: id=%q title=%q", got.HXCID, got.Title)
	}

	// ListItems with a hostile status filter must treat it as a literal value: it
	// matches nothing and does not error or inject.
	hostileStatus := "Queued' OR '1'='1"
	items, lerr := reg.ListItems(hostileStatus)
	if lerr != nil {
		t.Fatalf("INJECTION BUG: hostile status filter errored: %v", lerr)
	}
	if len(items) != 0 {
		t.Fatalf("INJECTION BUG: hostile status filter %q matched %d rows; the OR '1'='1' "+
			"was interpreted as SQL, not bound as a literal", hostileStatus, len(items))
	}
	// And the real row is still reachable by its true status.
	real, _ := reg.ListItems("Queued")
	if len(real) != 1 {
		t.Fatalf("INJECTION BUG: %d rows for real status filter, want 1", len(real))
	}
}

// TestAdversarial_Reconcile_ConcurrentNoTornLocations runs Reconcile concurrently
// with itself and with updates, asserting the sink converges to fully-canonical
// locations with no row left in a non-canonical location and no extra/lost rows.
// Run under -race. This guards the reconcile path against a concurrent interleave
// that strands a row in the wrong partition.
func TestAdversarial_Reconcile_ConcurrentNoTornLocations(t *testing.T) {
	tmp := t.TempDir() + "/reconcile_concurrent.db"
	reg, err := Open(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	const n = 10
	for i := 0; i < n; i++ {
		// Half terminal-in-Issues, half active-in-Fixed: both partitions have work.
		if i%2 == 0 {
			if err := reg.CreateItem(newItem(fmt.Sprintf("HXC-G%02d", i), "Completed", LocationIssues)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		} else {
			if err := reg.CreateItem(newItem(fmt.Sprintf("HXC-G%02d", i), "In progress", LocationFixed)); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = reg.Reconcile() // SQLITE_BUSY-tolerant: errors are fine, torn state is not
		}()
	}
	wg.Wait()

	// One final reconcile on a quiescent DB must converge everything.
	if _, err := reg.Reconcile(); err != nil {
		t.Fatalf("final reconcile: %v", err)
	}

	// SINK: every row sits in its canonical location for its status.
	items, err := reg.ListItems("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != n {
		t.Fatalf("conservation: %d rows after concurrent reconcile, want %d", len(items), n)
	}
	for _, it := range items {
		if want := CanonicalLocation(it.Status); it.CurrentLocation != want {
			t.Fatalf("SINK BUG: %s status=%q location=%q, want canonical %q",
				it.HXCID, it.Status, it.CurrentLocation, want)
		}
	}
}
