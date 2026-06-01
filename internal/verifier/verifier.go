// Package verifier implements result verification for SEMI-trust nodes.
//
// Constitution §CLAUDE-1 (anti-bluff): SEMI-trust results are never accepted
// on face value; they must be proven correct by an independent trusted executor
// before the controller forwards them downstream.
package verifier

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/console"
)

// ErrMismatch is returned when a SEMI node's result does not match the trusted
// recomputation. It is a typed sentinel so callers can errors.Is-check it.
var ErrMismatch = errors.New("verifier: result mismatch — SEMI node output rejected")

// Job is a deterministic compute unit: a pure function from []byte input to
// []byte output whose result must be reproducible by any trusted executor.
//
// The executor registered with New receives the entire Job so it can use any
// field (ID, Input, or future extensions). Fn is optional; callers that wire
// a single shared executor can leave it nil.
type Job struct {
	// ID is an opaque label used in log records (e.g. "task-42").
	ID string
	// Input is the byte payload passed to the executor.
	Input []byte
}

// MismatchRecord is persisted to the log when a SEMI node's result is rejected.
// The RunID is a per-call UUID so replayed log records cannot be confused with
// the current rejection.
type MismatchRecord struct {
	RunID       string    // per-call UUID
	JobID       string    // job identifier
	TrustLevel  string    // always "SEMI" in current use
	NodeID      string    // reporting node identifier
	SemiHash    string    // sha256 of the SEMI-submitted result
	TrustedHash string    // sha256 of the trusted recomputation
	At          time.Time // wall-clock timestamp of the rejection
}

// String renders a human-readable mismatch record for log output.
func (m MismatchRecord) String() string {
	return fmt.Sprintf(
		"[MISMATCH run=%s job=%s trust=%s node=%s semi_sha256=%s trusted_sha256=%s at=%s]",
		m.RunID, m.JobID, m.TrustLevel, m.NodeID,
		m.SemiHash, m.TrustedHash, m.At.UTC().Format(time.RFC3339Nano),
	)
}

// Verifier gates results from SEMI-trust nodes using a trusted re-executor.
//
// Usage:
//
//	v := New(trustedExecutor)
//	if err := v.VerifyResult(job, semiResult, console.TrustSemi, "node-7"); err != nil {
//	    // err wraps ErrMismatch — the result is rejected
//	}
type Verifier struct {
	// executor is called to recompute the job on a trusted path.
	executor func(job Job) ([]byte, error)
	// logMismatch receives every MismatchRecord when a SEMI result is rejected.
	// Defaults to log.Printf if nil.
	logMismatch func(r MismatchRecord)
}

// Option is a functional option for Verifier.
type Option func(*Verifier)

// WithMismatchLogger replaces the default log.Printf mismatch sink with a
// custom receiver. Tests inject a recorder here to assert the logged UUID.
func WithMismatchLogger(fn func(r MismatchRecord)) Option {
	return func(v *Verifier) {
		if fn != nil {
			v.logMismatch = fn
		}
	}
}

// New creates a Verifier backed by the given trusted executor function.
// executor must be deterministic: the same Job must always produce the same
// output. Panics if executor is nil.
func New(executor func(job Job) ([]byte, error), opts ...Option) *Verifier {
	if executor == nil {
		panic("verifier.New: executor must not be nil")
	}
	v := &Verifier{
		executor: executor,
		logMismatch: func(r MismatchRecord) {
			log.Printf("verifier: mismatch %s", r)
		},
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// VerifyResult checks result according to trustLevel:
//
//   - TrustFull: accepted without verification (bypass).
//   - TrustSemi: result is verified against a trusted recomputation via sha256
//     checksum comparison. A mismatch emits a MismatchRecord (UUID-tagged) and
//     returns an error wrapping ErrMismatch.
//   - TrustUntrusted: always rejected (unconditional ErrMismatch).
//
// nodeID identifies the reporting node and is embedded in the MismatchRecord.
func (v *Verifier) VerifyResult(
	job Job,
	result []byte,
	trustLevel console.TrustLevel,
	nodeID string,
) error {
	switch trustLevel {
	case console.TrustFull:
		// FULL-trust nodes bypass verification — no recompute needed.
		return nil

	case console.TrustUntrusted:
		runID := mustUUID()
		rec := MismatchRecord{
			RunID:      runID,
			JobID:      job.ID,
			TrustLevel: string(trustLevel),
			NodeID:     nodeID,
			SemiHash:   checksum(result),
			At:         time.Now().UTC(),
		}
		v.logMismatch(rec)
		return fmt.Errorf("%w: node %q is UNTRUSTED (run=%s)", ErrMismatch, nodeID, runID)

	case console.TrustSemi:
		// Recompute via trusted executor.
		trusted, err := v.executor(job)
		if err != nil {
			return fmt.Errorf("verifier: trusted executor error for job %q: %w", job.ID, err)
		}
		semiHash := checksum(result)
		trustedHash := checksum(trusted)
		if semiHash == trustedHash {
			// Checksums match — result accepted.
			return nil
		}
		// Mismatch: log a UUID-tagged record and reject.
		runID := mustUUID()
		rec := MismatchRecord{
			RunID:       runID,
			JobID:       job.ID,
			TrustLevel:  string(trustLevel),
			NodeID:      nodeID,
			SemiHash:    semiHash,
			TrustedHash: trustedHash,
			At:          time.Now().UTC(),
		}
		v.logMismatch(rec)
		return fmt.Errorf("%w: job=%q node=%q semi_sha256=%s trusted_sha256=%s run=%s",
			ErrMismatch, job.ID, nodeID, semiHash, trustedHash, runID)

	default:
		return fmt.Errorf("verifier: unknown trust level %q", trustLevel)
	}
}

// checksum returns the lowercase hex sha256 digest of data.
func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// mustUUID generates a random 16-byte UUID as a hex string. Panics on
// crypto/rand failure (catastrophic — never silently swallowed).
func mustUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("verifier: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
