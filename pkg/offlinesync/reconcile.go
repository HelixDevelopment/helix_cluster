package offlinesync

import (
	"bytes"
	"sync"
)

// Server is the authoritative store that merges offline delta records.
// Reconcile is idempotent: re-submitting the same records does not create
// duplicates and does not alter the high-water mark beyond the maximum Seq
// already seen.
//
// Server is safe for concurrent use.
type Server struct {
	mu   sync.Mutex
	jobs map[string]CompletedJob // keyed by JobID for idempotent merge
	hwm  uint64                  // highest Seq ever reconciled
}

// NewServer returns an empty authoritative server.
func NewServer() *Server {
	return &Server{jobs: make(map[string]CompletedJob)}
}

// HighWaterMark returns the highest Seq the server has ever reconciled.
// A device should pass this value to BuildDelta so that only new records
// are included in the delta.
func (s *Server) HighWaterMark() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hwm
}

// Reconcile merges records into the authoritative set.  The merge is
// idempotent: if a record with the same JobID is submitted more than once,
// only the first submission is kept (the one with the lower Seq wins in the
// rare case of a duplicate that disagrees on Seq, which should not happen in
// practice but is handled defensively).  The high-water mark is advanced to
// the maximum Seq seen across all submitted records.
func (s *Server) Reconcile(records []CompletedJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range records {
		// Idempotent, order-independent merge: keep the record with the LOWER
		// Seq for a given JobID. On an EQUAL-Seq conflict (the same JobID minted
		// at the same device-local Seq with a DIFFERING Output — reachable when
		// two devices collide on a JobID), break the tie deterministically by
		// the lexicographically-lower Output, then (when Output is also equal but
		// the records still differ) by the EARLIER CompletedAt. Without a full
		// tie-break the conflicting case kept the first-arrived record, making the
		// winner order-DEPENDENT (two servers reconciling the same pair in
		// different orders permanently diverge) — contradicting this merge's own
		// order-independence guarantee. (Same class as HXC-1759 in
		// pkg/antientropy.) Equal Seq + equal Output but DIFFERING CompletedAt is
		// reachable (two devices producing byte-identical deterministic output at
		// the same device-local Seq but with different wall clocks); comparing
		// only (Seq, Output) left CompletedAt order-dependent, so the
		// CompletedAt discriminator below completes the total order. The full
		// (Seq, Output, CompletedAt) key is now a total order, so the winner is
		// identical regardless of arrival order.
		if existing, exists := s.jobs[rec.JobID]; !exists || recLess(rec, existing) {
			s.jobs[rec.JobID] = rec
		}
		if rec.Seq > s.hwm {
			s.hwm = rec.Seq
		}
	}
}

// recLess reports whether record a should win over record b for the same JobID
// under the merge's deterministic precedence: LOWER Seq wins; on equal Seq the
// lexicographically-lower Output wins; on equal Seq AND equal Output the EARLIER
// CompletedAt wins. This makes (Seq, Output, CompletedAt) a total order so the
// reconciled winner is a function of the SET, not the arrival ORDER.
func recLess(a, b CompletedJob) bool {
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	if c := bytes.Compare(a.Output, b.Output); c != 0 {
		return c < 0
	}
	return a.CompletedAt.Before(b.CompletedAt)
}

// CountReconciled returns the number of distinct jobs the server has accepted.
func (s *Server) CountReconciled() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// HasJob reports whether a job with the given ID has been reconciled.
func (s *Server) HasJob(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobs[jobID]
	return ok
}

// GetJob returns the reconciled job for the given ID, if present.
func (s *Server) GetJob(jobID string) (CompletedJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	return j, ok
}
