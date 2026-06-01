// Package offlinesync implements a pure-Go offline-sync PROTOCOL with delta
// compression.  A Device accumulates CompletedJob records while offline; when
// connectivity is restored it calls BuildDelta to obtain only the records the
// server has not yet seen, compresses that delta with compress/flate, and hands
// the decompressed records to Server.Reconcile which merges them idempotently.
//
// The package is stdlib-only (compress/flate + encoding/json).
// It does NOT import pkg/edge.
package offlinesync

import (
	"sync"
	"time"
)

// CompletedJob is the unit of offline work that a device records while
// disconnected. Seq is a monotonically increasing, device-local sequence
// number that serves as the high-water-mark cursor used by BuildDelta.
type CompletedJob struct {
	JobID       string    `json:"job_id"`
	Output      []byte    `json:"output"`
	CompletedAt time.Time `json:"completed_at"`
	Seq         uint64    `json:"seq"`
}

// Device is an edge device that accumulates CompletedJob records while
// offline.  The in-memory ledger survives a simulated long outage because
// outage duration is modelled purely through CompletedAt timestamps — no
// real sleep is ever needed.
//
// Device is safe for concurrent use.
type Device struct {
	mu        sync.Mutex
	connected bool
	ledger    []CompletedJob
	nextSeq   uint64
}

// NewDevice returns a new Device in disconnected state.
func NewDevice() *Device {
	return &Device{connected: false, nextSeq: 1}
}

// SetConnected marks the device as online (true) or offline (false).
// Setting connected=false does not clear the ledger.
func (d *Device) SetConnected(online bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.connected = online
}

// Connected reports the current connectivity state.
func (d *Device) Connected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected
}

// RecordCompleted appends a CompletedJob to the offline ledger regardless of
// connectivity state (the device continues to buffer jobs while disconnected).
// completedAt allows the caller to supply the logical timestamp so that a
// 72-hour outage can be expressed as data (no real sleep required).
func (d *Device) RecordCompleted(jobID string, output []byte, completedAt time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	seq := d.nextSeq
	d.nextSeq++
	d.ledger = append(d.ledger, CompletedJob{
		JobID:       jobID,
		Output:      output,
		CompletedAt: completedAt,
		Seq:         seq,
	})
}

// BuildDelta returns all records whose Seq is strictly greater than
// serverHighWaterMark.  The caller should pass server.HighWaterMark() to
// obtain exactly the jobs the server has not yet seen.
func (d *Device) BuildDelta(serverHighWaterMark uint64) []CompletedJob {
	d.mu.Lock()
	defer d.mu.Unlock()
	var delta []CompletedJob
	for _, job := range d.ledger {
		if job.Seq > serverHighWaterMark {
			delta = append(delta, job)
		}
	}
	return delta
}

// LedgerLen returns the total number of jobs in the offline ledger (for
// test introspection).
func (d *Device) LedgerLen() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ledger)
}
