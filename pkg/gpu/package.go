// Package gpu provides registration hooks through which external provider
// adapters plug into the GPU layer. The internal/gpu package owns the concrete
// device backends; this package owns the *adapter* seam so out-of-tree provider
// integrations (e.g. a cloud GPU provider) can register themselves and be
// enumerated by a pool layer without either side importing the other directly.
//
// The package is deliberately self-contained: it exposes a listing the pool
// layer can consume (see Adapters / DefaultRegistry) instead of reaching into
// pkg/pool, so reachability is provable in-package.
package gpu

import (
	"fmt"
	"sort"
	"sync"
)

// ProviderAdapter is implemented by an external provider integration that wants
// to participate in the GPU layer. It is intentionally minimal: the GPU layer
// only needs a stable identity (Name) and the ability to enumerate the device
// classes the provider can supply (Offerings). Richer per-provider behaviour
// lives behind the adapter on the provider side.
type ProviderAdapter interface {
	// Name returns the adapter's stable, unique identity. It MUST be non-empty
	// and unique within a Registry; duplicate names are rejected at Register.
	Name() string

	// Offerings returns the GPU device classes this provider can supply (for
	// example "nvidia-a100", "nvidia-h100"). The slice may be empty for a
	// provider that is registered but currently advertises no capacity.
	Offerings() []string
}

// Registry holds the set of registered ProviderAdapter implementations. It is
// safe for concurrent use by multiple goroutines.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]ProviderAdapter
	// order records registration order so List is deterministic.
	order []string
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]ProviderAdapter),
		order:    make([]string, 0),
	}
}

// Register adds a to the registry, keyed by a.Name(). It returns an error if
// the adapter is nil, reports an empty name, or a provider with the same name
// is already registered — duplicate registration is almost always a caller bug.
func (r *Registry) Register(a ProviderAdapter) error {
	if a == nil {
		return fmt.Errorf("gpu registry: nil provider adapter")
	}
	name := a.Name()
	if name == "" {
		return fmt.Errorf("gpu registry: provider adapter returned empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("gpu registry: provider adapter %q already registered", name)
	}
	r.adapters[name] = a
	r.order = append(r.order, name)
	return nil
}

// Get returns the registered adapter for name, or (nil, false) if none.
func (r *Registry) Get(name string) (ProviderAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// List returns all registered adapters in registration order. The returned
// slice is a fresh copy, so callers may mutate it without affecting the
// Registry.
func (r *Registry) List() []ProviderAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProviderAdapter, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, r.adapters[name])
	}
	return result
}

// Names returns the registered adapter names sorted lexicographically. This is
// a convenience for callers (such as a pool layer) that want a stable,
// order-independent view of which providers are present.
func (r *Registry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// defaultRegistry is the process-wide Registry that the package-level Register
// and Adapters helpers operate on. A pool layer can enumerate registered
// providers via Adapters without holding a reference to a specific Registry.
var defaultRegistry = NewRegistry()

// DefaultRegistry returns the process-wide Registry backing the package-level
// Register / Adapters / Lookup helpers.
func DefaultRegistry() *Registry { return defaultRegistry }

// Register adds a to the process-wide default Registry. It is the hook external
// provider adapters call (typically from an init function) to plug into the GPU
// layer.
func Register(a ProviderAdapter) error { return defaultRegistry.Register(a) }

// Adapters returns all provider adapters registered in the process-wide default
// Registry, in registration order. This is the exported accessor a pool layer
// calls to enumerate registered providers.
func Adapters() []ProviderAdapter { return defaultRegistry.List() }

// Lookup returns the adapter registered under name in the process-wide default
// Registry, or (nil, false) if none.
func Lookup(name string) (ProviderAdapter, bool) { return defaultRegistry.Get(name) }
