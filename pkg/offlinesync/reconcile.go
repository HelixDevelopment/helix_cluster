package offlinesync

import (
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
		// Seq for a given JobID (the documented tie-break for a duplicate that
		// disagrees on Seq). Selecting by min(Seq) — rather than by arrival
		// order — makes the winner deterministic regardless of the order in
		// which records appear in the batch or across re-syncs.
		if existing, exists := s.jobs[rec.JobID]; !exists || rec.Seq < existing.Seq {
			s.jobs[rec.JobID] = rec
		}
		if rec.Seq > s.hwm {
			s.hwm = rec.Seq
		}
	}
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
