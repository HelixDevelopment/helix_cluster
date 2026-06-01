package sandbox

import (
	"sync"
)

// Decision describes the outcome of a Guard capability check.
type Decision string

const (
	// Allow indicates the capability was granted and limits were not exceeded.
	Allow Decision = "allow"
	// Deny indicates the capability was denied or a limit was exceeded.
	Deny Decision = "deny"
)

// AuditEntry records a single capability decision made by a Guard.
type AuditEntry struct {
	// Cap is the capability that was checked.
	Cap Capability
	// Decision is Allow or Deny.
	Decision Decision
	// Reason is a human-readable explanation for the decision (empty for Allow).
	Reason string
	// Seq is the monotonically increasing sequence number within an AuditSink,
	// starting at 1. It preserves the temporal order of entries.
	Seq int
}

// AuditSink receives audit entries from a Guard. Implementations must be safe
// for concurrent use from multiple goroutines.
type AuditSink interface {
	Record(entry AuditEntry)
}

// MemAudit is a thread-safe, in-memory AuditSink that accumulates entries in
// arrival order. It is suitable for testing and short-lived workloads.
type MemAudit struct {
	mu      sync.Mutex
	entries []AuditEntry
	seq     int
}

// Record appends entry to the sink, assigning the next sequence number. It is
// safe for concurrent use.
func (m *MemAudit) Record(entry AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	entry.Seq = m.seq
	m.entries = append(m.entries, entry)
}

// Entries returns a snapshot of all recorded entries in arrival order. The
// returned slice is a copy; modifications do not affect the sink.
func (m *MemAudit) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil
	}
	out := make([]AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Len returns the number of entries recorded so far.
func (m *MemAudit) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// nopAudit is a no-op AuditSink used when no sink is provided to NewGuard.
type nopAudit struct{}

func (nopAudit) Record(AuditEntry) {}
