package snapshot

import (
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	m := NewManager(t.TempDir())
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestManager_Create(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	path, err := m.Create("test1", []byte("hello"), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}

	// Duplicate without update should fail.
	_, err = m.Create("test1", []byte("world"), false)
	if err == nil {
		t.Error("expected error for duplicate snapshot")
	}

	// Update should succeed.
	_, err = m.Create("test1", []byte("world"), true)
	if err != nil {
		t.Fatalf("unexpected error on update: %v", err)
	}
}

func TestManager_Restore(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	if _, err := m.Create("restore", []byte("golden-data"), false); err != nil {
		t.Fatal(err)
	}

	data, err := m.Restore("restore")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "golden-data" {
		t.Errorf("expected golden-data, got %s", string(data))
	}

	_, err = m.Restore("missing")
	if err == nil {
		t.Error("expected error for missing snapshot")
	}
}

func TestManager_Compare(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	if _, err := m.Create("cmp", []byte("expected"), false); err != nil {
		t.Fatal(err)
	}

	if err := m.Compare("cmp", []byte("expected")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := m.Compare("cmp", []byte("different")); err == nil {
		t.Error("expected mismatch error")
	}
	if err := m.Compare("missing", []byte("")); err == nil {
		t.Error("expected error for missing snapshot")
	}
}

func TestManager_List(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	for _, name := range []string{"a/foo", "b/bar", "c/baz"} {
		if _, err := m.Create(name, []byte("data"), false); err != nil {
			t.Fatal(err)
		}
	}

	names, err := m.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(names))
	}
	if names[0] != "a/foo" || names[1] != "b/bar" || names[2] != "c/baz" {
		t.Errorf("unexpected order: %v", names)
	}
}

func TestManager_List_Empty(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	names, err := m.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(names))
	}
}

func TestManager_Delete(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	if _, err := m.Create("del", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("del"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := m.Delete("del"); err == nil {
		t.Error("expected error deleting missing snapshot")
	}
}

func TestManager_CreateNested(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	name := filepath.Join("sub", "dir", "snapshot")
	path, err := m.Create(name, []byte("nested"), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(filepath.Dir(path)) != "dir" {
		t.Errorf("expected nested directory creation")
	}
}
