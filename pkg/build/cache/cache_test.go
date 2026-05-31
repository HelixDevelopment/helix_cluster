package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDigest(t *testing.T) {
	d1 := Digest([]byte("hello"))
	d2 := Digest([]byte("hello"))
	if d1 != d2 {
		t.Error("same data should produce same digest")
	}
	d3 := Digest([]byte("world"))
	if d1 == d3 {
		t.Error("different data should produce different digest")
	}
	if len(d1) != 64 {
		t.Errorf("expected sha256 hex length 64, got %d", len(d1))
	}
}

func TestMemoryCachePutGet(t *testing.T) {
	c := NewMemoryCache()
	data := []byte("artifact data")
	digest := Digest(data)
	if err := c.Put(digest, data); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.Get(digest)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("retrieved data mismatch")
	}
}

func TestMemoryCacheHas(t *testing.T) {
	c := NewMemoryCache()
	digest := Digest([]byte("x"))
	if c.Has(digest) {
		t.Error("should not have missing digest")
	}
	c.Put(digest, []byte("x"))
	if !c.Has(digest) {
		t.Error("should have existing digest")
	}
}

func TestMemoryCacheDelete(t *testing.T) {
	c := NewMemoryCache()
	digest := Digest([]byte("y"))
	c.Put(digest, []byte("y"))
	if err := c.Delete(digest); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if c.Has(digest) {
		t.Error("should not have deleted digest")
	}
}

func TestMemoryCacheEmptyDigest(t *testing.T) {
	c := NewMemoryCache()
	if err := c.Put("", []byte("x")); err == nil {
		t.Error("expected error for empty digest")
	}
}

func TestMemoryCacheGetMissing(t *testing.T) {
	c := NewMemoryCache()
	_, err := c.Get("nonexistent")
	if err == nil {
		t.Error("expected error for missing digest")
	}
}

func TestDiskCachePutGet(t *testing.T) {
	dir := t.TempDir()
	c, err := NewDiskCache(dir)
	if err != nil {
		t.Fatalf("new disk cache: %v", err)
	}
	data := []byte("disk artifact")
	digest := Digest(data)
	if err := c.Put(digest, data); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.Get(digest)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("retrieved data mismatch")
	}
}

func TestDiskCacheHas(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	digest := Digest([]byte("z"))
	if c.Has(digest) {
		t.Error("should not have missing digest")
	}
	c.Put(digest, []byte("z"))
	if !c.Has(digest) {
		t.Error("should have existing digest")
	}
}

func TestDiskCacheDelete(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	digest := Digest([]byte("del"))
	c.Put(digest, []byte("del"))
	if err := c.Delete(digest); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if c.Has(digest) {
		t.Error("should not have deleted digest")
	}
}

func TestDiskCacheEmptyDigest(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	if err := c.Put("", []byte("x")); err == nil {
		t.Error("expected error for empty digest")
	}
}

func TestDiskCacheGetMissing(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	_, err := c.Get("nonexistent")
	if err == nil {
		t.Error("expected error for missing digest")
	}
}

func TestDiskCacheCreatesPrefixDir(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	digest := Digest([]byte("pref"))
	c.Put(digest, []byte("pref"))
	prefixDir := filepath.Join(dir, digest[:2])
	if _, err := os.Stat(prefixDir); os.IsNotExist(err) {
		t.Error("expected prefix directory to be created")
	}
}

func TestDiskCacheDeleteMissing(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	if err := c.Delete("missing"); err != nil {
		t.Error("deleting missing digest should not error")
	}
}

// --- Mutation tests ---

func TestMemoryCachePutCopiesData_Mutation(t *testing.T) {
	c := NewMemoryCache()
	data := []byte("original")
	digest := Digest(data)
	c.Put(digest, data)
	data[0] = 'X'
	got, _ := c.Get(digest)
	if !bytes.Equal(got, []byte("original")) {
		t.Error("mutation: cache should store a copy, not reference")
	}
}

func TestMemoryCacheGetCopiesData_Mutation(t *testing.T) {
	c := NewMemoryCache()
	data := []byte("original")
	digest := Digest(data)
	c.Put(digest, data)
	got, _ := c.Get(digest)
	got[0] = 'X'
	got2, _ := c.Get(digest)
	if !bytes.Equal(got2, []byte("original")) {
		t.Error("mutation: get should return a copy")
	}
}

func TestDiskCachePersistence_Mutation(t *testing.T) {
	dir := t.TempDir()
	c, _ := NewDiskCache(dir)
	data := []byte("persist")
	digest := Digest(data)
	c.Put(digest, data)
	// Create new cache instance pointing to same dir.
	c2, _ := NewDiskCache(dir)
	if !c2.Has(digest) {
		t.Error("mutation: disk cache should persist across instances")
	}
}
