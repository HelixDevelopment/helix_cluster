// Package cache provides content-addressable build artifact caching for Helix Cluster OS.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Cache is a content-addressable storage cache for build artifacts.
type Cache interface {
	Put(digest string, data []byte) error
	Get(digest string) ([]byte, error)
	Has(digest string) bool
	Delete(digest string) error
}

// Digest computes the SHA-256 digest of data.
func Digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// MemoryCache is an in-memory Cache implementation.
type MemoryCache struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryCache creates a new MemoryCache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{data: make(map[string][]byte)}
}

// Put stores data under the given digest.
func (c *MemoryCache) Put(digest string, data []byte) error {
	if digest == "" {
		return fmt.Errorf("digest cannot be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copyData := make([]byte, len(data))
	copy(copyData, data)
	c.data[digest] = copyData
	return nil
}

// Get retrieves data by digest.
func (c *MemoryCache) Get(digest string) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, ok := c.data[digest]
	if !ok {
		return nil, fmt.Errorf("digest not found: %s", digest)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// Has checks if a digest exists.
func (c *MemoryCache) Has(digest string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.data[digest]
	return ok
}

// Delete removes a digest.
func (c *MemoryCache) Delete(digest string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, digest)
	return nil
}

// DiskCache is a filesystem-backed Cache implementation.
type DiskCache struct {
	mu   sync.RWMutex
	root string
}

// NewDiskCache creates a new DiskCache rooted at the given directory.
func NewDiskCache(root string) (*DiskCache, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create cache root: %w", err)
	}
	return &DiskCache{root: root}, nil
}

// Put stores data under the given digest.
func (c *DiskCache) Put(digest string, data []byte) error {
	if digest == "" {
		return fmt.Errorf("digest cannot be empty")
	}
	path := c.digestPath(digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create digest dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write digest file: %w", err)
	}
	return nil
}

// Get retrieves data by digest.
func (c *DiskCache) Get(digest string) ([]byte, error) {
	path := c.digestPath(digest)
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("digest not found: %s", digest)
		}
		return nil, fmt.Errorf("read digest file: %w", err)
	}
	return data, nil
}

// Has checks if a digest exists.
func (c *DiskCache) Has(digest string) bool {
	path := c.digestPath(digest)
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, err := os.Stat(path)
	return err == nil
}

// Delete removes a digest.
func (c *DiskCache) Delete(digest string) error {
	path := c.digestPath(digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove digest file: %w", err)
	}
	return nil
}

func (c *DiskCache) digestPath(digest string) string {
	// Use first 2 chars as prefix directory for distribution.
	if len(digest) >= 2 {
		return filepath.Join(c.root, digest[:2], digest)
	}
	return filepath.Join(c.root, digest)
}
