//go:build integration

package hxcregistry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
)

// sharedDSN is the DSN of the ONE real ephemeral PostgreSQL booted for the whole
// package run. Booting a single container (rather than one per test) honors the
// "one Postgres at a time" constraint, guarantees teardown via TestMain even if
// a test fails, and keeps container churn (and host memory pressure per §12)
// minimal. Tests isolate themselves with unique HXC-id prefixes and TRUNCATE.
var sharedDSN string

// TestMain boots a single REAL PostgreSQL via brokertest, runs every integration
// test against it, then tears the container down. No mocks — every assertion in
// this file traverses the project's OWN registry code (OpenPostgres → the same
// CRUD/audit-history statements end users run) against a real Postgres server.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	dsn, stop, err := brokertest.StartPostgres(ctx)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "brokertest.StartPostgres: %v\n", err)
		os.Exit(1)
	}
	sharedDSN = dsn
	fmt.Fprintf(os.Stderr, "real postgres broker up at %s\n", dsn)

	code := m.Run()

	stop()
	cancel()
	os.Exit(code)
}

// newPGRegistry opens THIS package's own Postgres-backed Registry against the
// shared live server and wipes both tables so each test starts from a clean
// slate. The wipe runs through database/sql against the real server.
func newPGRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := OpenPostgres(sharedDSN)
	if err != nil {
		t.Fatalf("OpenPostgres(%q): %v", sharedDSN, err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	// item_history references items, so truncate it first (or CASCADE).
	if _, err := reg.db.Exec(`TRUNCATE item_history, items RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return reg
}

func sampleItem(id, title string) *HXCItem {
	return &HXCItem{
		HXCID:           id,
		Type:            "Feature",
		Status:          "Queued",
		Priority:        "P0",
		Phase:           4,
		Title:           title,
		Description:     "Implement Bazel RBE protocol for distributed builds",
		CurrentLocation: "Issues",
	}
}

// countHistory returns the number of item_history rows of a given event_type for
// an id — the sink-side query the audit bluff demands. It reads through the real
// Postgres, not a Go-side cache.
func countHistory(t *testing.T, reg *Registry, id, eventType string) int {
	t.Helper()
	var n int
	err := reg.db.QueryRow(
		`SELECT count(*) FROM item_history WHERE hxc_id = $1 AND event_type = $2`,
		id, eventType,
	).Scan(&n)
	if err != nil {
		t.Fatalf("count history (%s,%s): %v", id, eventType, err)
	}
	return n
}

// TestPG_CreateWritesOpenedHistory is the CLAUDE-1 sink-side proof for the core
// audit bluff: after CreateItem, an item_history row with event_type='Opened'
// MUST exist in the REAL database. Before the production fix swallowed this
// write's error; now a broken audit sink cannot pass silently.
func TestPG_CreateWritesOpenedHistory(t *testing.T) {
	reg := newPGRegistry(t)

	item := sampleItem("HXC-001", "Build Service Core")
	if err := reg.CreateItem(item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	if got := countHistory(t, reg, "HXC-001", "Opened"); got != 1 {
		t.Fatalf("item_history 'Opened' rows = %d, want 1 (audit sink broken)", got)
	}

	// Verify the row's full content sink-side, not merely its existence.
	var byEntity, reason string
	if err := reg.db.QueryRow(
		`SELECT by_entity, reason FROM item_history WHERE hxc_id = $1 AND event_type = 'Opened'`,
		"HXC-001",
	).Scan(&byEntity, &reason); err != nil {
		t.Fatalf("read Opened history row: %v", err)
	}
	if byEntity != "System" {
		t.Errorf("Opened by_entity = %q, want System", byEntity)
	}
	if reason != "Created via registry" {
		t.Errorf("Opened reason = %q, want 'Created via registry'", reason)
	}
	t.Logf("real postgres: CreateItem wrote 1 'Opened' audit row (by=%s reason=%q)", byEntity, reason)
}

// TestPG_UpdateWritesUpdatedHistory proves UpdateItem writes an 'Updated'
// item_history row to the REAL database, and that the items row actually changed
// (sink-side re-read), against real Postgres.
func TestPG_UpdateWritesUpdatedHistory(t *testing.T) {
	reg := newPGRegistry(t)

	item := sampleItem("HXC-002", "Omega Scheduler")
	if err := reg.CreateItem(item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	got, err := reg.GetItem("HXC-002")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	got.Status = "In progress"
	if err := reg.UpdateItem(got); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}

	if n := countHistory(t, reg, "HXC-002", "Updated"); n != 1 {
		t.Fatalf("item_history 'Updated' rows = %d, want 1 (audit sink broken)", n)
	}
	// The 'Opened' row from create must still be present — history is append-only.
	if n := countHistory(t, reg, "HXC-002", "Opened"); n != 1 {
		t.Fatalf("item_history 'Opened' rows = %d, want 1 after update", n)
	}

	// Sink-side: the status change actually landed in items.
	reread, err := reg.GetItem("HXC-002")
	if err != nil {
		t.Fatalf("GetItem after update: %v", err)
	}
	if reread.Status != "In progress" {
		t.Fatalf("status = %q, want 'In progress' (update did not persist)", reread.Status)
	}
	t.Log("real postgres: UpdateItem wrote 'Updated' audit row; status change persisted")
}

// TestPG_ComputeHeadingHashStablePinned pins the EXACT expected hash so a future
// change to the hashing logic (or a non-deterministic hash) FAILs. The previous
// SQLite test only asserted hash != "" — a tautology the audit flagged.
func TestPG_ComputeHeadingHashStablePinned(t *testing.T) {
	reg := newPGRegistry(t)

	const (
		titleA = "Build Service Core"
		titleB = "Omega Scheduler"
		wantA  = "922645bcd711534f"
		wantB  = "220b0f7a09f0f0f7"
	)

	a := &HXCItem{Title: titleA}
	if got := a.ComputeHeadingHash(); got != wantA {
		t.Fatalf("ComputeHeadingHash(%q) = %q, want pinned %q", titleA, got, wantA)
	}
	// Stable across repeated calls.
	if a.ComputeHeadingHash() != wantA {
		t.Fatalf("ComputeHeadingHash not stable across calls")
	}
	// Different titles produce different hashes (collision-discrimination).
	b := &HXCItem{Title: titleB}
	if got := b.ComputeHeadingHash(); got != wantB {
		t.Fatalf("ComputeHeadingHash(%q) = %q, want pinned %q", titleB, got, wantB)
	}
	if a.ComputeHeadingHash() == b.ComputeHeadingHash() {
		t.Fatalf("distinct titles produced identical hashes")
	}

	// And the pinned hash is what actually lands in the REAL items row.
	item := sampleItem("HXC-003", titleA)
	if err := reg.CreateItem(item); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	var stored string
	if err := reg.db.QueryRow(
		`SELECT heading_hash FROM items WHERE hxc_id = $1`, "HXC-003",
	).Scan(&stored); err != nil {
		t.Fatalf("read heading_hash: %v", err)
	}
	if stored != wantA {
		t.Fatalf("stored heading_hash = %q, want pinned %q", stored, wantA)
	}
	t.Logf("real postgres: pinned heading_hash %q persisted for %q", stored, titleA)
}

// TestPG_DuplicatePrimaryKeyErrors proves a second CreateItem with the same
// HXC-id is rejected by the REAL Postgres PRIMARY KEY constraint — a failure
// path the audit found untested.
func TestPG_DuplicatePrimaryKeyErrors(t *testing.T) {
	reg := newPGRegistry(t)

	if err := reg.CreateItem(sampleItem("HXC-004", "First")); err != nil {
		t.Fatalf("first CreateItem: %v", err)
	}
	err := reg.CreateItem(sampleItem("HXC-004", "Second different title"))
	if err == nil {
		t.Fatal("duplicate HXC-id CreateItem returned nil, want a PK-violation error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "insert item") {
		t.Fatalf("duplicate error not wrapped as insert failure: %v", err)
	}
	// Sink-side: exactly one row, and exactly one 'Opened' history row — the
	// failed second insert must NOT have written a phantom audit row.
	var rows int
	if err := reg.db.QueryRow(`SELECT count(*) FROM items WHERE hxc_id = $1`, "HXC-004").Scan(&rows); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if rows != 1 {
		t.Fatalf("items rows for HXC-004 = %d, want 1", rows)
	}
	if n := countHistory(t, reg, "HXC-004", "Opened"); n != 1 {
		t.Fatalf("'Opened' history rows = %d, want 1 (failed insert leaked a history row)", n)
	}
	t.Logf("real postgres: duplicate PK rejected (%v); no phantom row or audit entry", err)
}

// TestPG_GetMissingReturnsNotFound proves GetItem on an absent id returns the
// not-found error against real Postgres (sql.ErrNoRows → "not found").
func TestPG_GetMissingReturnsNotFound(t *testing.T) {
	reg := newPGRegistry(t)

	_, err := reg.GetItem("HXC-999")
	if err == nil {
		t.Fatal("GetItem(missing) returned nil error, want not-found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("GetItem(missing) error = %v, want 'not found'", err)
	}
	t.Logf("real postgres: GetItem on missing id correctly errored: %v", err)
}

// TestPG_UpdateMissingErrors proves UpdateItem on a non-existent id now ERRORS
// (RowsAffected==0) instead of being a silent no-op — the exact gap the audit
// flagged. Also proves no 'Updated' audit row is written for the no-op.
func TestPG_UpdateMissingErrors(t *testing.T) {
	reg := newPGRegistry(t)

	ghost := sampleItem("HXC-777", "Ghost item")
	ghost.EnsureHash()
	err := reg.UpdateItem(ghost)
	if err == nil {
		t.Fatal("UpdateItem(non-existent) returned nil, want not-found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("UpdateItem(non-existent) error = %v, want 'not found'", err)
	}
	if n := countHistory(t, reg, "HXC-777", "Updated"); n != 0 {
		t.Fatalf("'Updated' history rows for ghost = %d, want 0 (no-op leaked audit row)", n)
	}
	t.Logf("real postgres: UpdateItem on missing id errored (%v) with no audit row", err)
}

// TestPG_ConcurrentWrites replaces the reads-only "ConcurrentAccess" misnomer
// with REAL concurrent WRITES against the real Postgres: N goroutines each
// Create a distinct item then Update it. Afterwards every row and every audit
// entry MUST be present (no lost writes), proving honest behaviour under write
// contention.
func TestPG_ConcurrentWrites(t *testing.T) {
	reg := newPGRegistry(t)

	const workers = 16

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := fmt.Sprintf("HXC-%03d", 100+n)
			item := sampleItem(id, fmt.Sprintf("Concurrent item %d", n))
			if err := reg.CreateItem(item); err != nil {
				errs <- fmt.Errorf("%s create: %w", id, err)
				return
			}
			got, err := reg.GetItem(id)
			if err != nil {
				errs <- fmt.Errorf("%s get: %w", id, err)
				return
			}
			got.Status = "In progress"
			if err := reg.UpdateItem(got); err != nil {
				errs <- fmt.Errorf("%s update: %w", id, err)
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent worker: %v", err)
	}

	// Sink-side: all rows landed, and each has its 'Opened' + 'Updated' audit pair.
	var itemCount, openedCount, updatedCount int
	if err := reg.db.QueryRow(`SELECT count(*) FROM items`).Scan(&itemCount); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if err := reg.db.QueryRow(`SELECT count(*) FROM item_history WHERE event_type = 'Opened'`).Scan(&openedCount); err != nil {
		t.Fatalf("count opened: %v", err)
	}
	if err := reg.db.QueryRow(`SELECT count(*) FROM item_history WHERE event_type = 'Updated'`).Scan(&updatedCount); err != nil {
		t.Fatalf("count updated: %v", err)
	}
	if itemCount != workers {
		t.Fatalf("items count = %d, want %d (lost writes)", itemCount, workers)
	}
	if openedCount != workers {
		t.Fatalf("'Opened' audit rows = %d, want %d (lost audit writes)", openedCount, workers)
	}
	if updatedCount != workers {
		t.Fatalf("'Updated' audit rows = %d, want %d (lost audit writes)", updatedCount, workers)
	}
	// Every item must show the persisted status change.
	var inProgress int
	if err := reg.db.QueryRow(`SELECT count(*) FROM items WHERE status = 'In progress'`).Scan(&inProgress); err != nil {
		t.Fatalf("count in-progress: %v", err)
	}
	if inProgress != workers {
		t.Fatalf("'In progress' items = %d, want %d (lost update writes)", inProgress, workers)
	}
	t.Logf("real postgres: %d concurrent create+update writers all landed; %d items, %d Opened, %d Updated audit rows",
		workers, itemCount, openedCount, updatedCount)
}
