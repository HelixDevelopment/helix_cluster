// Package build provides build orchestration primitives for Helix Cluster OS.
package build

import (
	"fmt"
	"sort"
	"sync"
)

// Manifest maps platforms to content digests for multi-arch image manifests.
type Manifest struct {
	mu      sync.RWMutex
	entries map[string]string // platform string -> digest
}

// NewManifest creates an empty manifest.
func NewManifest() *Manifest {
	return &Manifest{entries: make(map[string]string)}
}

// Add registers a platform→digest mapping.
func (m *Manifest) Add(platform Platform, digest string) error {
	if digest == "" {
		return fmt.Errorf("digest cannot be empty")
	}
	key := platform.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = digest
	return nil
}

// Get looks up the digest for a given platform.
func (m *Manifest) Get(platform Platform) (string, bool) {
	key := platform.String()
	m.mu.RLock()
	defer m.mu.RUnlock()
	digest, ok := m.entries[key]
	return digest, ok
}

// Platforms returns a sorted list of all supported platform strings.
func (m *Manifest) Platforms() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.entries))
	for p := range m.entries {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Remove deletes a platform entry from the manifest.
func (m *Manifest) Remove(platform Platform) {
	key := platform.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
}
