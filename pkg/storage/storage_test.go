package storage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound for missing key, got %v", err)
	}
}

func TestMemoryStoreEmptyKey(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put("", []byte("x")); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("expected ErrEmptyKey for empty key, got %v", err)
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	s := NewMemoryStore()
	s.Put("del", []byte("me"))
	if err := s.Delete("del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := s.Get("del")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
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
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound for missing key, got %v", err)
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
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
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
	if err := s.Put("", []byte("x")); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("expected ErrEmptyKey for empty key, got %v", err)
	}
}

// newStores returns both Store implementations so contract tests run against
// each, guaranteeing interface-level parity (not just per-impl behavior).
func newStores(t *testing.T) map[string]Store {
	t.Helper()
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	return map[string]Store{
		"memory": NewMemoryStore(),
		"file":   fs,
	}
}

// TestStoreOverwrite proves Put(k,v2) after Put(k,v1) yields v2 for both
// implementations. A Put that no-ops on existing keys would fail here.
func TestStoreOverwrite(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Put("k", []byte("v1")); err != nil {
				t.Fatalf("put v1: %v", err)
			}
			if err := s.Put("k", []byte("v2")); err != nil {
				t.Fatalf("put v2: %v", err)
			}
			got, err := s.Get("k")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !bytes.Equal(got, []byte("v2")) {
				t.Errorf("after overwrite get = %q, want v2", got)
			}
		})
	}
}

// TestStoreDeleteMissingIsNoError guards the intentional idempotent-delete
// contract: deleting a non-existent key returns nil for both implementations.
func TestStoreDeleteMissingIsNoError(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Delete("never-existed"); err != nil {
				t.Errorf("delete of missing key should be nil, got %v", err)
			}
			// Delete after a real delete must also be idempotent.
			if err := s.Put("k", []byte("v")); err != nil {
				t.Fatalf("put: %v", err)
			}
			if err := s.Delete("k"); err != nil {
				t.Fatalf("first delete: %v", err)
			}
			if err := s.Delete("k"); err != nil {
				t.Errorf("second delete should be nil, got %v", err)
			}
		})
	}
}

// TestStoreListEmptyPrefixReturnsAll proves List("") returns every key.
func TestStoreListEmptyPrefixReturnsAll(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			s.Put("a/1", []byte("1"))
			s.Put("a/2", []byte("2"))
			s.Put("b/1", []byte("3"))

			keys, err := s.List("")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			want := []string{"a/1", "a/2", "b/1"}
			if len(keys) != len(want) {
				t.Fatalf("List(\"\") = %v, want %v", keys, want)
			}
			for i := range want {
				if keys[i] != want[i] {
					t.Errorf("List(\"\")[%d] = %q, want %q", i, keys[i], want[i])
				}
			}
		})
	}
}

// TestStoreListNoMatchReturnsEmpty proves a non-matching prefix yields an empty
// result with no error (not an I/O error) for both implementations.
func TestStoreListNoMatchReturnsEmpty(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			s.Put("a/1", []byte("1"))
			s.Put("b/1", []byte("2"))

			keys, err := s.List("nomatch")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(keys) != 0 {
				t.Errorf("List(\"nomatch\") = %v, want empty", keys)
			}
		})
	}
}

// TestStorePutCopiesData proves the observable value-copy contract holds for
// BOTH implementations: mutating the caller's slice after Put must not change
// what the store returns. (MemoryStore copies on Put; FileStore persists bytes
// to disk so the caller's slice is no longer aliased.)
func TestStorePutCopiesData(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("original")
			if err := s.Put("key", data); err != nil {
				t.Fatalf("put: %v", err)
			}
			data[0] = 'X' // mutate caller's buffer after Put
			got, err := s.Get("key")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !bytes.Equal(got, []byte("original")) {
				t.Errorf("store corrupted by caller mutation: got %q, want original", got)
			}
		})
	}
}

// TestStoreGetCopiesData proves the observable read-copy contract holds for
// BOTH implementations: mutating a returned slice must not corrupt the store.
// (MemoryStore copies on Get; FileStore returns a fresh slice per ReadFile.)
func TestStoreGetCopiesData(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.Put("key", []byte("original")); err != nil {
				t.Fatalf("put: %v", err)
			}
			got, err := s.Get("key")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			got[0] = 'X' // mutate returned slice
			got2, err := s.Get("key")
			if err != nil {
				t.Fatalf("get2: %v", err)
			}
			if !bytes.Equal(got2, []byte("original")) {
				t.Errorf("store corrupted by mutating returned slice: got %q, want original", got2)
			}
		})
	}
}

// TestStoreConcurrentAccess hammers Put/Get/Delete from many goroutines.
// Run with -race this fails if the RWMutex is removed (data race on the map or
// concurrent file ops); it also catches any panic from concurrent map writes.
func TestStoreConcurrentAccess(t *testing.T) {
	for name, s := range newStores(t) {
		t.Run(name, func(t *testing.T) {
			const goroutines = 16
			const iterations = 200
			var wg sync.WaitGroup
			wg.Add(goroutines)
			for g := 0; g < goroutines; g++ {
				go func(g int) {
					defer wg.Done()
					for i := 0; i < iterations; i++ {
						key := fmt.Sprintf("g%d/k%d", g, i%8)
						if err := s.Put(key, []byte("v")); err != nil {
							t.Errorf("put: %v", err)
							return
						}
						if _, err := s.Get(key); err != nil && !errors.Is(err, ErrKeyNotFound) {
							t.Errorf("get: %v", err)
							return
						}
						if _, err := s.List(fmt.Sprintf("g%d/", g)); err != nil {
							t.Errorf("list: %v", err)
							return
						}
						if err := s.Delete(key); err != nil {
							t.Errorf("delete: %v", err)
							return
						}
					}
				}(g)
			}
			wg.Wait()
		})
	}
}
