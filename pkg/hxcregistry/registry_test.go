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
