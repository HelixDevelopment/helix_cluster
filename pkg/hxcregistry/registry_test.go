package hxcregistry

import (
	"testing"
)

func TestRegistryCRUD(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	item := &HXCItem{
		HXCID:           "HXC-001",
		Type:            "Feature",
		Status:          "Queued",
		Priority:        "P0",
		Phase:           4,
		Title:           "Build Service Core",
		Description:     "Implement Bazel RBE protocol for distributed builds",
		CurrentLocation: "Issues",
	}

	// Create
	if err := reg.CreateItem(item); err != nil {
		t.Fatalf("create item: %v", err)
	}

	// Get
	got, err := reg.GetItem("HXC-001")
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got.Title != "Build Service Core" {
		t.Errorf("title = %q, want %q", got.Title, "Build Service Core")
	}
	if got.HeadingHash == "" {
		t.Error("heading hash not set")
	}

	// Update
	got.Status = "In progress"
	if err := reg.UpdateItem(got); err != nil {
		t.Fatalf("update item: %v", err)
	}

	got2, err := reg.GetItem("HXC-001")
	if err != nil {
		t.Fatalf("get updated item: %v", err)
	}
	if got2.Status != "In progress" {
		t.Errorf("status = %q, want %q", got2.Status, "In progress")
	}

	// List
	items, err := reg.ListItems("")
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("len(items) = %d, want 1", len(items))
	}

	// List by status
	items, err = reg.ListItems("Queued")
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("len(queued) = %d, want 0", len(items))
	}
}

func TestRegistryNextID(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	next, err := reg.NextHXCID()
	if err != nil {
		t.Fatalf("next id: %v", err)
	}
	if next != "HXC-001" {
		t.Errorf("next id = %q, want HXC-001", next)
	}

	item := &HXCItem{
		HXCID:           "HXC-001",
		Type:            "Task",
		Status:          "Queued",
		Priority:        "P1",
		Phase:           2,
		Title:           "Test item",
		Description:     "Description of test item",
		CurrentLocation: "Issues",
	}
	if err := reg.CreateItem(item); err != nil {
		t.Fatalf("create: %v", err)
	}

	next, err = reg.NextHXCID()
	if err != nil {
		t.Fatalf("next id after create: %v", err)
	}
	if next != "HXC-002" {
		t.Errorf("next id = %q, want HXC-002", next)
	}
}

// TestRegistryNextIDNumericNotLexical pins that NextHXCID computes the maximum
// id NUMERICALLY, not by TEXT ordering of hxc_id. Once ids span both 3-digit
// (HXC-999) and 4-digit (HXC-1000+) widths, a plain `ORDER BY hxc_id DESC`
// sorts lexically and reports "HXC-999" as the max (because '9' > '1' at the
// first digit), which would hand back HXC-1000 forever and collide with every
// already-allocated 1000+ id. This is the bite: with HXC-1614 present, the next
// id MUST be HXC-1615, never HXC-1000.
func TestRegistryNextIDNumericNotLexical(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	// Seed ids whose lexical max ("HXC-999") differs from their numeric max
	// ("HXC-1614"). Include 4-digit ids so the lexical-vs-numeric gap is real.
	for _, id := range []string{"HXC-001", "HXC-999", "HXC-1000", "HXC-1001", "HXC-1614"} {
		if err := reg.CreateItem(&HXCItem{
			HXCID: id, Type: "Task", Status: "Completed", Priority: "P1",
			Phase: 0, Title: "seed " + id, Description: "seed", CurrentLocation: "Fixed",
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	next, err := reg.NextHXCID()
	if err != nil {
		t.Fatalf("next id: %v", err)
	}
	if next != "HXC-1615" {
		t.Errorf("next id = %q, want HXC-1615 (numeric max 1614 + 1). A lexical "+
			"ORDER BY would wrongly return HXC-1000 (max=\"HXC-999\")", next)
	}
}

func TestItemValidation(t *testing.T) {
	cases := []struct {
		name    string
		item    *HXCItem
		wantErr bool
	}{
		{
			name: "valid",
			item: &HXCItem{HXCID: "HXC-001", Type: "Feature", Status: "Queued", Priority: "P0", Title: "T", Description: "D", CurrentLocation: "Issues"},
		},
		{
			name:    "missing id",
			item:    &HXCItem{Type: "Feature", Status: "Queued", Priority: "P0", Title: "T", Description: "D", CurrentLocation: "Issues"},
			wantErr: true,
		},
		{
			name:    "invalid type",
			item:    &HXCItem{HXCID: "HXC-001", Type: "Unknown", Status: "Queued", Priority: "P0", Title: "T", Description: "D", CurrentLocation: "Issues"},
			wantErr: true,
		},
		{
			name:    "invalid status",
			item:    &HXCItem{HXCID: "HXC-001", Type: "Feature", Status: "Done", Priority: "P0", Title: "T", Description: "D", CurrentLocation: "Issues"},
			wantErr: true,
		},
		{
			name:    "invalid priority",
			item:    &HXCItem{HXCID: "HXC-001", Type: "Feature", Status: "Queued", Priority: "P5", Title: "T", Description: "D", CurrentLocation: "Issues"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.item.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	// Use file-based DB for concurrent access (modernc.org/sqlite :memory: is per-connection)
	tmpFile := t.TempDir() + "/test.db"
	reg, err := Open(tmpFile)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	// Create initial item
	if err := reg.CreateItem(&HXCItem{
		HXCID: "HXC-001", Type: "Task", Status: "Queued",
		Priority: "P0", Phase: 1, Title: "Concurrent test", Description: "D",
		CurrentLocation: "Issues",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Concurrent reads
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := reg.GetItem("HXC-001")
			if err != nil {
				t.Errorf("concurrent get: %v", err)
			}
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestCanonicalLocationMapping pins the status->location mapping that both the
// update path and Reconcile depend on. If this table drifts, doc-sync breaks.
func TestCanonicalLocationMapping(t *testing.T) {
	cases := []struct {
		status   string
		wantLoc  string
		terminal bool
	}{
		{"Queued", LocationIssues, false},
		{"In progress", LocationIssues, false},
		{"Ready for testing", LocationIssues, false},
		{"In testing", LocationIssues, false},
		{"Completed", LocationFixed, true},
		{"Obsolete", LocationFixed, true},
	}
	for _, c := range cases {
		if got := CanonicalLocation(c.status); got != c.wantLoc {
			t.Errorf("CanonicalLocation(%q) = %q, want %q", c.status, got, c.wantLoc)
		}
		if got := IsTerminalStatus(c.status); got != c.terminal {
			t.Errorf("IsTerminalStatus(%q) = %v, want %v", c.status, got, c.terminal)
		}
	}
}

// seedItem creates a row with an explicitly chosen status+location so a test can
// reproduce the historical "stranded" state (Completed yet still in Issues).
func seedItem(t *testing.T, reg *Registry, id, status, loc string) {
	t.Helper()
	it := &HXCItem{
		HXCID: id, Type: "Task", Status: status, Priority: "P1", Phase: 0,
		Title: id + " title", Description: "desc", CurrentLocation: loc,
	}
	if err := reg.CreateItem(it); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func locOf(t *testing.T, reg *Registry, id string) string {
	t.Helper()
	it, err := reg.GetItem(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return it.CurrentLocation
}

// TestReconcileMovesStrandedRowsAndIsIdempotent proves Reconcile fixes rows whose
// location drifted from their status and that a second run moves nothing. This is
// the registry-level anti-bluff proof for the backfill path.
func TestReconcileMovesStrandedRowsAndIsIdempotent(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	// Two Completed rows stranded in Issues, one Obsolete stranded in Issues,
	// one already-correct Completed-in-Fixed, and one correct active row.
	seedItem(t, reg, "HXC-001", "Completed", LocationIssues)
	seedItem(t, reg, "HXC-002", "Completed", LocationIssues)
	seedItem(t, reg, "HXC-003", "Obsolete", LocationIssues)
	seedItem(t, reg, "HXC-004", "Completed", LocationFixed)
	seedItem(t, reg, "HXC-005", "Queued", LocationIssues)

	moved, err := reg.Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if moved != 3 {
		t.Fatalf("first reconcile moved %d, want 3", moved)
	}
	for _, id := range []string{"HXC-001", "HXC-002", "HXC-003", "HXC-004"} {
		if loc := locOf(t, reg, id); loc != LocationFixed {
			t.Errorf("%s location = %q, want Fixed", id, loc)
		}
	}
	if loc := locOf(t, reg, "HXC-005"); loc != LocationIssues {
		t.Errorf("HXC-005 location = %q, want Issues", loc)
	}

	// Idempotent: second run moves nothing.
	moved, err = reg.Reconcile()
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if moved != 0 {
		t.Fatalf("second reconcile moved %d, want 0 (idempotent)", moved)
	}
}

// TestReconcileReopensFixedToIssues proves the reverse direction: a row whose
// status is active but is wrongly in Fixed is moved back to Issues.
func TestReconcileReopensFixedToIssues(t *testing.T) {
	reg, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reg.Close()

	seedItem(t, reg, "HXC-010", "In progress", LocationFixed)
	moved, err := reg.Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d, want 1", moved)
	}
	if loc := locOf(t, reg, "HXC-010"); loc != LocationIssues {
		t.Fatalf("location = %q, want Issues", loc)
	}
}
