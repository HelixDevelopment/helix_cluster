// Package storage provides a unified storage abstraction for Helix Cluster OS.
package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store is the unified key-value storage interface.
type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
	List(prefix string) ([]string, error)
}

// MemoryStore is an in-memory Store implementation.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStore creates a new MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string][]byte)}
}

// Put stores data under key.
func (s *MemoryStore) Put(key string, data []byte) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copyData := make([]byte, len(data))
	copy(copyData, data)
	s.data[key] = copyData
	return nil
}

// Get retrieves data by key.
func (s *MemoryStore) Get(key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

// Delete removes a key.
func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// List returns all keys matching the given prefix, sorted.
func (s *MemoryStore) List(prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// FileStore is a filesystem-backed Store implementation.
type FileStore struct {
	mu   sync.RWMutex
	root string
}

// NewFileStore creates a new FileStore rooted at the given directory.
func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	return &FileStore{root: root}, nil
}

// Put stores data under key as a file.
func (s *FileStore) Put(key string, data []byte) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	path := s.keyPath(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// Get retrieves data by key.
func (s *FileStore) Get(key string) ([]byte, error) {
	path := s.keyPath(key)
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("key not found: %s", key)
		}
		return nil, fmt.Errorf("read file: %w", err)
	}
	return data, nil
}

// Delete removes a key file.
func (s *FileStore) Delete(key string) error {
	path := s.keyPath(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}
	return nil
}

// List returns all keys matching the given prefix, sorted.
func (s *FileStore) List(prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []string
	walkPrefix := filepath.Join(s.root, prefix)
	// If prefix ends with a path separator, walk that directory.
	info, err := os.Stat(walkPrefix)
	if err == nil && info.IsDir() {
		walkPrefix = filepath.Join(walkPrefix, "")
	}

	err = filepath.Walk(s.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

func (s *FileStore) keyPath(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}
