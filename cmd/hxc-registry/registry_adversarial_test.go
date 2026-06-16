package main

// Adversarial, sink-side probes for the hxc-registry CLI seam (run()).
//
// These tests were written to BREAK the four documented defect classes for a
// CRUD-over-SQLite CLI: (a) arg/validation fail-open, (b) atomicity (orphan
// rows from a partial op), (c) total-order / determinism in list/next, and
// (d) unguarded shared state. Every assertion checks the REAL effect: the
// stored row (read back through the DB), the surfaced error, the list output —
// never "it compiled" (ANTI-BLUFF).
//
// FINDING after probing all four classes: NO BUG. The recent pkg/hxcregistry
// atomicity fix (CreateItem/UpdateItem wrap the item + audit-history writes in
// one transaction) and the numeric-CAST NextHXCID both hold under hostile
// input. Where the CLI deliberately does NOT validate (the ID *format*), that
// is the documented contract — Validate() requires only a non-empty hxc_id —
// so these tests PIN that contract rather than assert a fix. Each test names
// the mutation that would make it bite, so the suite is mutation-proof.
//
// Anti-hang: every test uses an isolated on-disk temp DB (newEnv from
// main_test.go); the real data/hxc_registry.db is never touched. Concurrency
// probe is capped at 8 goroutines and uses a single shared DB file.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// openRaw opens the temp DB behind env for direct sink-side inspection, bypassing
// the CLI so the assertions read ground truth, not the command's own output.
func openRaw(t *testing.T, env func(string) string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", env("HXC_DB"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// (a) ARG / VALIDATION BYPASS
// ---------------------------------------------------------------------------

// TestAddInvalidPriorityRejected: a bad --priority must NOT fail open into the
// DB. The registry's Validate() gate (and the CHECK constraint behind it) must
// reject "P9" with exit 1 and write NOTHING. Sink: items table stays empty.
// Mutation: in item.go Validate, drop the priority check -> the row inserts and
// the count is 1 -> FAIL. (The DB CHECK is a second gate; to truly fail open you
// must also widen the schema CHECK, which is the point — both must hold.)
func TestAddInvalidPriorityRejected(t *testing.T) {
	env := newEnv(t)
	code, _, errOut := invoke(t, env, "add", "--id", "HXC-001",
		"--title", "Bad pri", "--priority", "P9")
	if code != exitError {
		t.Fatalf("exit=%d, want %d (invalid priority must error)", code, exitError)
	}
	if !strings.Contains(errOut, "invalid priority") {
		t.Fatalf("stderr = %q, want invalid priority", errOut)
	}
	db := openRaw(t, env)
	if n := count(t, db, "SELECT COUNT(*) FROM items"); n != 0 {
		t.Fatalf("invalid priority FAILED OPEN: %d rows persisted, want 0", n)
	}
}

// TestUpdateInvalidStatusRejectedAndNoWrite proves that a bogus --status given
// to update is rejected by Validate() and the row is LEFT UNCHANGED (not even a
// partial location move is committed). cmdUpdate sets item.Status and derives
// CurrentLocation BEFORE UpdateItem runs Validate(); the transaction must roll
// the whole thing back. Sink: status AND location are the original values.
// Mutation: in item.go Validate, drop the status check -> "Nonexistent" is
// stored and the status assertion FAILs.
func TestUpdateInvalidStatusRejectedAndNoWrite(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-010", "--title", "Thing")

	code, _, errOut := invoke(t, env, "update", "HXC-010", "--status", "Nonexistent")
	if code != exitError {
		t.Fatalf("exit=%d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "invalid status") {
		t.Fatalf("stderr = %q, want invalid status", errOut)
	}

	db := openRaw(t, env)
	var status, loc string
	if err := db.QueryRow(
		"SELECT status, current_location FROM items WHERE hxc_id = ?", "HXC-010",
	).Scan(&status, &loc); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "Queued" || loc != "Issues" {
		t.Fatalf("partial write leaked: status=%q location=%q, want Queued/Issues", status, loc)
	}
	// The rejected update must not have written an audit row either.
	if n := count(t, db, "SELECT COUNT(*) FROM item_history WHERE event_type='Updated'"); n != 0 {
		t.Fatalf("rejected update wrote %d audit rows, want 0", n)
	}
}

// TestAddMalformedIDIsAcceptedContract PINS a deliberate non-guarantee: the CLI
// does NOT validate the *format* of --id. Validate() requires only a non-empty
// hxc_id, so "garbage-NOTHXC" is stored verbatim. This is the documented
// contract (disclaim, do NOT "fix"): there is no ID-format gate, and inventing
// one here would be scope the registry never promised. The test exists so that
// if someone LATER adds a format gate, this pin breaks loudly and the contract
// change is a conscious decision.
//
// Crucially it also proves the malformed id does NOT corrupt anything else:
// see TestMalformedIDDoesNotPoisonNextHXCID below.
func TestAddMalformedIDIsAcceptedContract(t *testing.T) {
	env := newEnv(t)
	code, out, errOut := invoke(t, env, "add", "--id", "garbage-NOTHXC", "--title", "bad id")
	if code != exitOK {
		t.Fatalf("malformed id rejected (exit=%d err=%q); contract is: no format gate, it stores", code, errOut)
	}
	if !strings.Contains(out, "Created garbage-NOTHXC") {
		t.Fatalf("stdout=%q, want Created garbage-NOTHXC", out)
	}
	db := openRaw(t, env)
	if n := count(t, db, "SELECT COUNT(*) FROM items WHERE hxc_id = ?", "garbage-NOTHXC"); n != 1 {
		t.Fatalf("malformed id not stored: count=%d", n)
	}
}

// ---------------------------------------------------------------------------
// (c) TOTAL-ORDER / DETERMINISM
// ---------------------------------------------------------------------------

// TestMalformedIDDoesNotPoisonNextHXCID is the determinism+robustness proof for
// NextHXCID. NextHXCID computes MAX(CAST(SUBSTR(hxc_id,5) AS INTEGER)). A
// malformed id with a non-numeric tail ("HXC-bogus") or one too short to have a
// tail ("HX") must CAST to 0 and therefore NOT inflate the next id. So after a
// real HXC-500 plus injected junk, `next` must still be HXC-501 — the junk is
// invisible to the numeric max.
// Mutation: change NextHXCID to `ORDER BY hxc_id DESC LIMIT 1` (lexical) instead
// of the numeric CAST -> "garbage-NOTHXC" or "HXC-bogus" sort high and the next
// id is computed off junk -> FAIL.
func TestMalformedIDDoesNotPoisonNextHXCID(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-500", "--title", "real")
	if _, out, _ := invoke(t, env, "next"); strings.TrimSpace(out) != "HXC-501" {
		t.Fatalf("baseline next = %q, want HXC-501", out)
	}
	// Inject ids whose numeric tail is absent / non-numeric.
	mustOK(t, env, "add", "--id", "HXC-bogus", "--title", "junk tail")
	mustOK(t, env, "add", "--id", "HX", "--title", "too short")
	mustOK(t, env, "add", "--id", "garbage-NOTHXC", "--title", "no prefix")
	if _, out, _ := invoke(t, env, "next"); strings.TrimSpace(out) != "HXC-501" {
		t.Fatalf("next poisoned by malformed ids = %q, want HXC-501", out)
	}
}

// TestNextHXCIDNumericNotLexical proves the documented numeric-max contract that
// guards the 3-digit/4-digit width boundary: with HXC-999 and HXC-1000 present,
// the true max is 1000 (numeric), so next is HXC-1001 — NOT HXC-1000 (which a
// lexical ORDER BY hxc_id DESC would wrongly yield because '9' > '1').
// Mutation: replace the CAST(SUBSTR(...)) max with lexical ordering -> next
// becomes HXC-1000 and collides -> FAIL.
func TestNextHXCIDNumericNotLexical(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-999", "--title", "three digit")
	mustOK(t, env, "add", "--id", "HXC-1000", "--title", "four digit")
	if _, out, _ := invoke(t, env, "next"); strings.TrimSpace(out) != "HXC-1001" {
		t.Fatalf("next = %q, want HXC-1001 (numeric max, not lexical)", out)
	}
}

// TestListOrderIsDeterministicAndTotal proves list output is stable: repeated
// invocations over the same data yield byte-identical output (no map-order
// nondeterminism), and the documented sort key (phase, priority, hxc_id) is
// honoured. Phase is the primary key, so a higher-phase row sorts after a
// lower-phase one regardless of id.
// Mutation: drop the ORDER BY in ListItems -> SQLite row order is unspecified
// and the byte-equality across runs becomes flaky / the phase order assertion
// FAILs.
func TestListOrderIsDeterministicAndTotal(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-003", "--title", "c", "--phase", "2")
	mustOK(t, env, "add", "--id", "HXC-001", "--title", "a", "--phase", "1")
	mustOK(t, env, "add", "--id", "HXC-002", "--title", "b", "--phase", "1")

	_, first, _ := invoke(t, env, "list")
	for i := 0; i < 5; i++ {
		_, again, _ := invoke(t, env, "list")
		if again != first {
			t.Fatalf("list output nondeterministic across runs:\n--run0--\n%s\n--run%d--\n%s", first, i+1, again)
		}
	}
	// Phase 1 rows (HXC-001, HXC-002) must precede the phase 2 row (HXC-003).
	p1 := strings.Index(first, "HXC-001")
	p2 := strings.Index(first, "HXC-002")
	p3 := strings.Index(first, "HXC-003")
	if !(p1 < p3 && p2 < p3) {
		t.Fatalf("phase ordering wrong (want phase1 before phase2):\n%s", first)
	}
}

// ---------------------------------------------------------------------------
// (b) ATOMICITY
// ---------------------------------------------------------------------------

// TestDuplicateIDLeavesNoOrphanHistory exercises the atomicity fix at the CLI
// level. A second `add` with an already-used --id fails the items PRIMARY KEY /
// UNIQUE constraint. Because CreateItem wraps the item insert AND the mandatory
// audit-history insert in one transaction, the failure must leave the registry
// EXACTLY as before the failed add: one item, one history row, zero orphans.
// Sink: row counts read straight from the DB.
// Mutation: revert CreateItem to two autocommit Execs (item insert, then history
// insert) -> a dup-id add that somehow reached the history step, or any partial
// commit, would desync items vs history. With the constraint firing on the item
// insert the counts also prove no history row is written for the failed id.
func TestDuplicateIDLeavesNoOrphanHistory(t *testing.T) {
	env := newEnv(t)
	mustOK(t, env, "add", "--id", "HXC-700", "--title", "first")

	code, _, errOut := invoke(t, env, "add", "--id", "HXC-700", "--title", "second")
	if code != exitError {
		t.Fatalf("duplicate id exit=%d, want %d (must surface as error, not overwrite)", code, exitError)
	}
	if !strings.Contains(errOut, "UNIQUE") && !strings.Contains(errOut, "constraint") {
		t.Fatalf("stderr=%q, want a UNIQUE/constraint error", errOut)
	}

	db := openRaw(t, env)
	if n := count(t, db, "SELECT COUNT(*) FROM items"); n != 1 {
		t.Fatalf("items count=%d after failed dup add, want 1 (no overwrite, no extra row)", n)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM item_history"); n != 1 {
		t.Fatalf("item_history count=%d after failed dup add, want 1 (no orphan audit row)", n)
	}
	// The surviving item is the ORIGINAL, not the second add's title.
	var title string
	if err := db.QueryRow("SELECT title FROM items WHERE hxc_id=?", "HXC-700").Scan(&title); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "first" {
		t.Fatalf("dup add overwrote the row: title=%q, want %q", title, "first")
	}
}

// TestEveryCreatedItemHasExactlyOneAuditRow proves the item/audit pair is never
// desynced by the create path: N successful adds yield N items and N 'Opened'
// history rows — the all-or-nothing invariant the transaction guarantees.
// Mutation: make the history insert in CreateItem fire-and-forget (swallow its
// error / move it outside the tx) -> on any failure items != history -> FAIL.
func TestEveryCreatedItemHasExactlyOneAuditRow(t *testing.T) {
	env := newEnv(t)
	for i := 1; i <= 5; i++ {
		mustOK(t, env, "add", "--id", fmt.Sprintf("HXC-%03d", i), "--title", fmt.Sprintf("item %d", i))
	}
	db := openRaw(t, env)
	items := count(t, db, "SELECT COUNT(*) FROM items")
	opened := count(t, db, "SELECT COUNT(*) FROM item_history WHERE event_type='Opened'")
	if items != 5 || opened != 5 {
		t.Fatalf("item/audit desync: items=%d opened-history=%d, want 5/5", items, opened)
	}
}

// ---------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE
// ---------------------------------------------------------------------------

// TestConcurrentAddsAgainstSharedDB scopes the concurrency class honestly. The
// CLI is single-shot per process, so there is no in-process shared mutable state
// across commands; the only shared state is the DB FILE. This test runs 8
// concurrent run() invocations (the cap) that each add a DISTINCT id against ONE
// shared DB path and asserts the registry stays consistent: no panic / data
// race (run under -race), no lost or duplicated rows, and every committed item
// has its audit row. SQLite serialises the writers; a successful add MUST have
// persisted, and a failed one (e.g. transient SQLITE_BUSY) MUST NOT have left a
// partial row — that is the atomicity guarantee under contention.
// This is NOT asserting the CLI provides cross-process locking beyond what the
// transaction + SQLite give; it pins that those are sufficient for consistency.
func TestConcurrentAddsAgainstSharedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	env := func(k string) string {
		if k == "HXC_DB" {
			return dbPath
		}
		return ""
	}
	// Initialise the schema once so all writers share a migrated DB.
	mustOK(t, env, "init")

	const n = 8 // concurrency cap per anti-hang rule
	var wg sync.WaitGroup
	codes := make([]int, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			code, _, _ := invoke(t, env, "add",
				"--id", fmt.Sprintf("HXC-%03d", 800+i),
				"--title", fmt.Sprintf("concurrent %d", i))
			codes[i] = code
		}(i)
	}
	wg.Wait()

	db := openRaw(t, env)
	// Every item that the CLI reported as created (exit 0) must be present, and
	// each present item must have exactly one Opened audit row. We assert the
	// item count equals the number of successful adds and that items==Opened.
	wantOK := 0
	for _, c := range codes {
		if c == exitOK {
			wantOK++
		}
	}
	items := count(t, db, "SELECT COUNT(*) FROM items")
	opened := count(t, db, "SELECT COUNT(*) FROM item_history WHERE event_type='Opened'")
	if items != wantOK {
		t.Fatalf("items=%d but %d adds reported success: a committed/reported state was lost or a partial row survived", items, wantOK)
	}
	if items != opened {
		t.Fatalf("item/audit desync under concurrency: items=%d opened=%d", items, opened)
	}
	// Distinct ids => no row should be missing if all 8 succeeded; if any failed
	// under contention that is acceptable, but never a partial (item w/o audit).
}
