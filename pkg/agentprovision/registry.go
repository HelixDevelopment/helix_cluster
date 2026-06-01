package agentprovision

import "sync"

// ContextRegistry is a concurrency-safe store that maps an agent ID to a set
// of key-value pairs (e.g., environment variables, secrets, session tokens).
//
// Isolation guarantee: Lookup always returns a fresh copy of the stored map.
// A caller mutating the returned map cannot corrupt the stored state.
type ContextRegistry struct {
	mu   sync.RWMutex
	data map[string]map[string]string
}

// NewContextRegistry creates an empty ContextRegistry.
func NewContextRegistry() *ContextRegistry {
	return &ContextRegistry{
		data: make(map[string]map[string]string),
	}
}

// Register stores a copy of shared as the context for agentID, replacing any
// previously stored context.
func (r *ContextRegistry) Register(agentID string, shared map[string]string) {
	cp := copyMap(shared)
	r.mu.Lock()
	r.data[agentID] = cp
	r.mu.Unlock()
}

// Lookup returns a copy of the context for agentID.
// The second return value is false if agentID has not been registered.
func (r *ContextRegistry) Lookup(agentID string) (map[string]string, bool) {
	r.mu.RLock()
	m, ok := r.data[agentID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return copyMap(m), true
}

// Deregister removes the context for agentID.  It is a no-op if agentID was
// not registered.
func (r *ContextRegistry) Deregister(agentID string) {
	r.mu.Lock()
	delete(r.data, agentID)
	r.mu.Unlock()
}

// copyMap returns a shallow copy of m.  For string→string maps this is a
// complete, independent copy.
func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
