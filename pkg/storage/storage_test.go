package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryStorePutGet(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put("foo", []byte("bar")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get("foo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("bar")) {
		t.Errorf("get = %q, want bar", got)
	}
}

func TestMemoryStoreGetMissing(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Get("missing")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestMemoryStoreEmptyKey(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put("", []byte("x")); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	s := NewMemoryStore()
	s.Put("del", []byte("me"))
	if err := s.Delete("del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.Get("del")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryStoreList(t *testing.T) {
	s := NewMemoryStore()
	s.Put("a/1", []byte("1"))
	s.Put("a/2", []byte("2"))
	s.Put("b/1", []byte("3"))

	keys, err := s.List("a/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
	if keys[0] != "a/1" || keys[1] != "a/2" {
		t.Errorf("keys = %v", keys)
	}
}

func TestMemoryStorePutCopiesData(t *testing.T) {
	s := NewMemoryStore()
	data := []byte("original")
	s.Put("key", data)
	data[0] = 'X'
	got, _ := s.Get("key")
	if !bytes.Equal(got, []byte("original")) {
		t.Error("store should keep a copy")
	}
}

func TestMemoryStoreGetCopiesData(t *testing.T) {
	s := NewMemoryStore()
	s.Put("key", []byte("original"))
	got, _ := s.Get("key")
	got[0] = 'X'
	got2, _ := s.Get("key")
	if !bytes.Equal(got2, []byte("original")) {
		t.Error("get should return a copy")
	}
}

func TestFileStorePutGet(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if err := s.Put("foo", []byte("bar")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get("foo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("bar")) {
		t.Errorf("get = %q, want bar", got)
	}
}

func TestFileStoreGetMissing(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	_, err := s.Get("missing")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestFileStoreDelete(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	s.Put("del", []byte("me"))
	if err := s.Delete("del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.Get("del")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestFileStoreList(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	s.Put("a/1", []byte("1"))
	s.Put("a/2", []byte("2"))
	s.Put("b/1", []byte("3"))

	keys, err := s.List("a/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
	if keys[0] != "a/1" || keys[1] != "a/2" {
		t.Errorf("keys = %v", keys)
	}
}

func TestFileStoreNestedKey(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	s.Put("deep/nested/key", []byte("value"))
	got, err := s.Get("deep/nested/key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Errorf("get = %q, want value", got)
	}
	expectedPath := filepath.Join(dir, "deep", "nested", "key")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Error("expected nested file to exist")
	}
}

func TestFileStorePersistence(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewFileStore(dir)
	s1.Put("persist", []byte("data"))

	s2, _ := NewFileStore(dir)
	got, err := s2.Get("persist")
	if err != nil {
		t.Fatalf("get from new store: %v", err)
	}
	if !bytes.Equal(got, []byte("data")) {
		t.Error("data should persist across store instances")
	}
}

func TestFileStoreEmptyKey(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewFileStore(dir)
	if err := s.Put("", []byte("x")); err == nil {
		t.Error("expected error for empty key")
	}
}
