package inferenceproxy

import "sync"

// MemAudit is an in-memory AuditSink that retains every recorded entry in
// order. It is safe for concurrent use and intended for tests and small
// embedded deployments where audit history fits in memory.
type MemAudit struct {
	mu      sync.Mutex
	entries []AuditEntry
}

// NewMemAudit returns an empty in-memory audit sink.
func NewMemAudit() *MemAudit { return &MemAudit{} }

// Record implements AuditSink by appending a copy of the entry.
func (m *MemAudit) Record(entry AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
}

// Entries returns a snapshot copy of all recorded entries in record order.
func (m *MemAudit) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Len returns the number of recorded entries.
func (m *MemAudit) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// compile-time assertion.
var _ AuditSink = (*MemAudit)(nil)
