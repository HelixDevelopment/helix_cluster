// Package snapshot provides golden snapshot management for deterministic test output validation.
package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manager handles creation, restoration, comparison, and listing of golden snapshots.
type Manager struct {
	baseDir string
	mu      sync.RWMutex
}

// NewManager creates a snapshot manager rooted at baseDir.
func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// resolve maps a snapshot name to its on-disk .golden path, guaranteeing the
// result stays within baseDir. A name that escapes the base directory (e.g.
// via ".." or an absolute path) is rejected: such a snapshot would be written
// outside the managed tree, invisible to List, and could clobber or delete
// unrelated files. Returning an error here keeps Create/Restore/Compare/Delete
// confined to baseDir so the create/list/restore round-trip stays consistent.
func (m *Manager) resolve(name string) (string, error) {
	path := filepath.Join(m.baseDir, name+".golden")
	base := filepath.Clean(m.baseDir)
	cleaned := filepath.Clean(path)
	if cleaned != base && !strings.HasPrefix(cleaned, base+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot name %q escapes base directory", name)
	}
	return path, nil
}

// Create writes data to a golden snapshot file. If update is true, it overwrites existing files.
func (m *Manager) Create(name string, data []byte, update bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	path, err := m.resolve(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	if !update {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("snapshot %q already exists (use update=true to overwrite)", name)
		}
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	return path, nil
}

// Restore reads a golden snapshot file.
func (m *Manager) Restore(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	return data, nil
}

// Compare reads the golden snapshot and compares it with data.
// It returns an error if the snapshot is missing or the contents differ.
func (m *Manager) Compare(name string, data []byte) error {
	golden, err := m.Restore(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(golden, data) {
		return fmt.Errorf("snapshot %q mismatch: expected %d bytes, got %d bytes", name, len(golden), len(data))
	}
	return nil
}

// List returns the names of all golden snapshots under the base directory.
func (m *Manager) List() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	err := filepath.Walk(m.baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".golden") {
			rel, err := filepath.Rel(m.baseDir, path)
			if err != nil {
				return err
			}
			name := strings.TrimSuffix(rel, ".golden")
			name = filepath.ToSlash(name)
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk snapshots: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// Delete removes a golden snapshot file.
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	path, err := m.resolve(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	return nil
}
