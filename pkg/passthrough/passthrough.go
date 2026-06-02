// Package passthrough implements a disconnect-aware streaming passthrough.
//
// A passthrough pumps chunks from an upstream Producer to a client Sink. When
// the CLIENT disconnects mid-stream (modeled by cancelling the ctx passed to
// Run), the passthrough STOPS pulling chunks and SIGNALS the upstream to ABORT
// (via the AbortSignal it observes) so the upstream producer stops producing.
// This prevents wasted upstream work and goroutine leaks. On normal completion
// the upstream finishes cleanly without any abort being signalled.
//
// The package is self-contained, pure-Go and deterministic: no network, no
// host access, no real clocks. Concurrency is driven entirely by the injected
// Producer and Sink callbacks plus the caller-supplied context.
package passthrough

import (
	"context"
	"errors"
)

// Chunk is a single unit of streamed data handed from the upstream Producer to
// the client Sink.
type Chunk struct {
	Seq  int
	Data []byte
}

// AbortSignal is the upstream-facing handle a Producer observes to learn that
// the client has disconnected. Its Done channel closes exactly once when the
// passthrough decides the upstream must stop producing. A Producer MUST select
// on Done() between (or while) yielding chunks and stop producing once it
// fires.
type AbortSignal struct {
	done   chan struct{}
	closed chan struct{} // closed after done is closed, to make Observed() race-free
}

func newAbortSignal() *AbortSignal {
	return &AbortSignal{
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

// Done returns a channel that is closed when the upstream must abort.
func (a *AbortSignal) Done() <-chan struct{} { return a.done }

// fire closes the abort channel exactly once.
func (a *AbortSignal) fire() {
	select {
	case <-a.done:
		// already fired
	default:
		close(a.done)
	}
}

// Emit is the chunk-delivery handle the Producer uses to yield a chunk toward
// the client. It returns an error if the chunk could not be accepted because
// the passthrough is shutting down (e.g. the client disconnected); the Producer
// MUST stop producing when Emit returns an error.
type Emit func(c Chunk) error

// Producer is the injected upstream. It yields chunks via emit and observes the
// abort signal. It MUST return promptly once abort.Done() fires or once emit
// returns an error. It returns nil on clean completion of the stream, or a
// non-nil error if it failed.
type Producer func(abort *AbortSignal, emit Emit) error

// Sink is the injected client. Write is called for each chunk in order. If the
// client has disconnected, Write is not called again. Write returning an error
// also tears the stream down (treated like a client-side failure).
type Sink interface {
	Write(c Chunk) error
}

// SinkFunc adapts a function to a Sink.
type SinkFunc func(c Chunk) error

// Write implements Sink.
func (f SinkFunc) Write(c Chunk) error { return f(c) }

// Result reports observable outcomes of a Run for assertions/telemetry.
type Result struct {
	// UpstreamAborted reports whether the upstream Producer observed the abort
	// signal (i.e. the passthrough propagated the client disconnect upstream).
	UpstreamAborted bool
	// ChunksDelivered is the number of chunks that reached the client Sink.
	ChunksDelivered int
	// ProducerErr is the error returned by the Producer, if any.
	ProducerErr error
}

// ErrClientDisconnected is returned by Run when the client context is cancelled
// mid-stream.
var ErrClientDisconnected = errors.New("passthrough: client disconnected")

// Run pumps chunks from upstream to client until the stream completes, the
// client disconnects (ctx cancelled), or an error occurs.
//
// Behaviour:
//   - The Producer runs in its own goroutine and yields chunks via emit.
//   - Each emitted chunk is forwarded to client.Write in arrival order by the
//     calling goroutine (Run itself), so Sink.Write is never called
//     concurrently.
//   - If ctx is cancelled (client disconnect), Run fires the AbortSignal so the
//     Producer observes it and stops; Run returns a cancellation error.
//   - On clean completion Run returns the Producer's error (nil on success).
//
// Run does not return until the Producer goroutine has fully exited, so no
// goroutine is leaked.
func Run(ctx context.Context, upstream Producer, client Sink) (Result, error) {
	abort := newAbortSignal()

	// emitReq carries one chunk from the producer goroutine plus a reply
	// channel used to unblock the producer's Emit call.
	type emitReq struct {
		c     Chunk
		reply chan error
	}
	reqs := make(chan emitReq)

	prodDone := make(chan error, 1)

	go func() {
		emit := func(c Chunk) error {
			req := emitReq{c: c, reply: make(chan error, 1)}
			select {
			case reqs <- req:
				return <-req.reply
			case <-abort.done:
				return ErrClientDisconnected
			}
		}
		err := upstream(abort, emit)
		close(abort.closed)
		prodDone <- err
	}()

	var res Result
	var runErr error

	// Drain loop: forward chunks until the producer finishes or the client
	// disconnects.
loop:
	for {
		select {
		case <-ctx.Done():
			// Client disconnected: signal upstream to abort and stop pulling.
			abort.fire()
			runErr = ErrClientDisconnected
			break loop
		case req := <-reqs:
			// Forward the chunk to the client.
			if werr := client.Write(req.c); werr != nil {
				// Client-side failure: tear down the upstream too.
				abort.fire()
				req.reply <- werr
				runErr = werr
				break loop
			}
			res.ChunksDelivered++
			req.reply <- nil
		case err := <-prodDone:
			// Producer finished before any disconnect.
			res.ProducerErr = err
			runErr = err
			res.UpstreamAborted = abortObserved(abort)
			return res, runErr
		}
	}

	// We broke out due to disconnect or client error. Wait for the producer
	// goroutine to fully exit so nothing leaks. While waiting, keep unblocking
	// any in-flight Emit calls with the disconnect error.
	for {
		select {
		case req := <-reqs:
			req.reply <- ErrClientDisconnected
		case err := <-prodDone:
			res.ProducerErr = err
			res.UpstreamAborted = abortObserved(abort)
			return res, runErr
		}
	}
}

// abortObserved reports whether the abort signal was fired and the producer has
// fully exited (closed bookkeeping channel), which together mean the upstream
// observed the abort.
func abortObserved(a *AbortSignal) bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}
