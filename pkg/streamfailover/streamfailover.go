// Package streamfailover implements streaming-safe failover that retries ONLY
// before the first byte is emitted.
//
// The core safety property: once a backend has emitted its first chunk, the
// stream is COMMITTED. A mid-stream failure after that point MUST NOT be
// retried on another backend, because doing so would either duplicate the
// already-emitted output or re-issue a non-idempotent request. Failover is
// therefore restricted to the pre-stream window: a first-byte timeout or any
// pre-stream error (before ANY chunk is yielded) is SAFE to fail over.
//
// The package is self-contained, deterministic, pure Go, and performs no
// network/host/LLM work itself: the backend call is an injected AttemptFunc
// that streams chunks via the supplied Emit callback.
package streamfailover

import (
	"errors"
	"fmt"
)

// ErrAllFailed is returned by Run when every backend failed in its pre-stream
// window (first-byte timeout or pre-stream error) and no backend ever committed
// a stream. It is a typed sentinel so callers can errors.Is against it.
var ErrAllFailed = errors.New("streamfailover: all backends failed before first byte")

// Backend identifies a single backend in the ordered failover list.
type Backend struct {
	// Name is a stable identifier used to report which backend served the
	// stream (and in failure messages).
	Name string
}

// Emit is the callback an AttemptFunc invokes to yield a single stream chunk.
// The first call to Emit marks the stream as COMMITTED: from that point on the
// attempt can no longer be failed over. Emit returns the number of chunks
// emitted so far on this attempt (1 after the first chunk), which lets an
// AttemptFunc reason about whether it has crossed the commit boundary.
type Emit func(chunk []byte) int

// AttemptResult is the terminal signal an AttemptFunc returns for one backend.
type AttemptResult int

const (
	// AttemptSuccess means the backend produced a complete stream. It is only
	// meaningful once at least one chunk was emitted; a "success" with zero
	// chunks is treated as an empty committed stream.
	AttemptSuccess AttemptResult = iota

	// AttemptFirstByteTimeout means the backend timed out BEFORE emitting any
	// chunk. This is a pre-stream failure and is SAFE to fail over.
	AttemptFirstByteTimeout

	// AttemptPreStreamError means the backend errored BEFORE emitting any
	// chunk (e.g. connection refused at dial). Pre-stream, SAFE to fail over.
	AttemptPreStreamError

	// AttemptMidStreamError means the backend failed AFTER emitting at least
	// one chunk. The stream is committed; this is NOT safe to fail over.
	AttemptMidStreamError
)

func (r AttemptResult) String() string {
	switch r {
	case AttemptSuccess:
		return "Success"
	case AttemptFirstByteTimeout:
		return "FirstByteTimeout"
	case AttemptPreStreamError:
		return "PreStreamError"
	case AttemptMidStreamError:
		return "MidStreamError"
	default:
		return fmt.Sprintf("AttemptResult(%d)", int(r))
	}
}

// AttemptFunc performs a single backend attempt. It must emit each produced
// chunk via emit (in order) and return a terminal AttemptResult plus an
// optional underlying error. Implementations inject their own clocks/streams;
// the package itself does no I/O.
//
// Contract the runner relies on:
//   - It MUST NOT call emit before producing a real chunk.
//   - If it returns AttemptFirstByteTimeout or AttemptPreStreamError, it MUST
//     NOT have called emit (zero chunks). The runner enforces this and treats a
//     contract violation as a committed stream to preserve the no-duplicate
//     guarantee.
//   - AttemptMidStreamError is only valid after >=1 emit.
type AttemptFunc func(b Backend, emit Emit) (AttemptResult, error)

// MidStreamError wraps the underlying error of a committed stream that failed
// after the first byte. The partial output already emitted is preserved on the
// Outcome; the error is never retried.
type MidStreamError struct {
	Backend string
	Err     error
}

func (e *MidStreamError) Error() string {
	return fmt.Sprintf("streamfailover: mid-stream failure on backend %q after first byte: %v", e.Backend, e.Err)
}

func (e *MidStreamError) Unwrap() error { return e.Err }

// Outcome is the assembled result of a Run.
type Outcome struct {
	// Output is the concatenation of all emitted chunks (complete on success,
	// partial on a mid-stream failure).
	Output []byte

	// Chunks is the ordered list of emitted chunks (defensive copies).
	Chunks [][]byte

	// Backend is the name of the backend that committed/served the stream.
	// Empty when no backend ever emitted a byte (all pre-stream failures).
	Backend string

	// Committed reports whether a first byte was ever emitted.
	Committed bool

	// Attempts records, per backend name, how many times the runner invoked
	// its AttemptFunc. By construction this is at most 1 for every backend,
	// and 0 for any backend never reached (because an earlier backend
	// committed). It is the primary observable proving "no second attempt".
	Attempts map[string]int
}

// Run executes the ordered backends with streaming-safe failover.
//
// Behaviour:
//   - It invokes attemptFn for the first backend. While the attempt has emitted
//     zero chunks, a FirstByteTimeout or PreStreamError result causes failover
//     to the NEXT backend.
//   - The instant the attempt emits its first chunk, the stream is committed:
//     the current attempt's result is final. A later AttemptMidStreamError
//     yields a *MidStreamError together with the partial output; NO further
//     backend is tried.
//   - On AttemptSuccess the assembled output and serving backend are returned.
//   - If every backend fails pre-stream, ErrAllFailed is returned (joined with
//     the per-backend underlying errors for diagnostics).
//
// attemptFn is never invoked for a backend once any earlier backend has
// committed a stream — this is what Outcome.Attempts[next] == 0 proves.
func Run(backends []Backend, attemptFn AttemptFunc) (Outcome, error) {
	out := Outcome{Attempts: make(map[string]int, len(backends))}
	if attemptFn == nil {
		return out, errors.New("streamfailover: nil attemptFn")
	}
	if len(backends) == 0 {
		return out, ErrAllFailed
	}

	var preStreamErrs []error

	for _, b := range backends {
		// Per-attempt streaming state. emitted counts chunks for THIS attempt;
		// it is the commit boundary detector.
		var (
			chunks  [][]byte
			emitted int
		)
		emit := func(chunk []byte) int {
			cp := make([]byte, len(chunk))
			copy(cp, chunk)
			chunks = append(chunks, cp)
			emitted++
			return emitted
		}

		out.Attempts[b.Name]++ // record that we actually issued this backend
		res, aerr := attemptFn(b, emit)

		committed := emitted > 0

		// Enforce the contract: a pre-stream result with chunks already emitted
		// is impossible to honor safely, so treat as committed (mid-stream).
		if (res == AttemptFirstByteTimeout || res == AttemptPreStreamError) && committed {
			res = AttemptMidStreamError
			if aerr == nil {
				aerr = errors.New("attempt reported pre-stream result after emitting chunks")
			}
		}

		switch {
		case !committed && (res == AttemptFirstByteTimeout || res == AttemptPreStreamError):
			// SAFE pre-stream failure: fall through to the next backend. We
			// deliberately discard the (empty) chunk buffer; nothing was shown
			// to the end user, so no duplication risk.
			preStreamErrs = append(preStreamErrs,
				fmt.Errorf("%s: %s: %w", b.Name, res, errOrNil(aerr)))
			continue

		case res == AttemptMidStreamError:
			// COMMITTED then failed: return partial, never retry.
			out.assemble(chunks, b.Name, true)
			return out, &MidStreamError{Backend: b.Name, Err: errOrNil(aerr)}

		default:
			// AttemptSuccess (committed or empty-but-successful). Done.
			out.assemble(chunks, b.Name, committed)
			return out, nil
		}
	}

	// Every backend failed pre-stream.
	return out, errors.Join(append([]error{ErrAllFailed}, preStreamErrs...)...)
}

func (o *Outcome) assemble(chunks [][]byte, backend string, committed bool) {
	o.Chunks = chunks
	o.Backend = backend
	o.Committed = committed
	var total int
	for _, c := range chunks {
		total += len(c)
	}
	o.Output = make([]byte, 0, total)
	for _, c := range chunks {
		o.Output = append(o.Output, c...)
	}
}

func errOrNil(err error) error {
	if err == nil {
		return errors.New("unspecified")
	}
	return err
}
