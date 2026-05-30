// Package discovery provides service discovery for Helix Cluster OS.
package discovery

import "sync"

// Registry is a simple in-memory service registry.
type Registry struct {
	mu       sync.RWMutex
	services map[string][]string
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{services: make(map[string][]string)}
}

// Register registers an instance for a service.
func (r *Registry) Register(service, instance string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[service] = append(r.services[service], instance)
}

// Lookup returns instances for a service.
func (r *Registry) Lookup(service string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.services[service]
}
