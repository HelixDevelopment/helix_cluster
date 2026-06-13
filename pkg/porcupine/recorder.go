package porcupine

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Recorder safely records operation begin/end events from many concurrent
// goroutines into a single history.
//
// WHY: a linearizability test must capture the *real* overlapping timing of
// concurrent client calls. The recorder hands out monotonically increasing
// timestamps from a shared atomic counter (a logical clock — wall-clock
// granularity is too coarse and non-monotonic for fast in-process ops) and
// pairs each Begin with its End under a mutex, so the resulting history is
// well-formed (every op has Call < Return) regardless of scheduling. It is
// designed to be exercised under the race detector.
type Recorder struct {
	clock int64 // atomic logical clock; ticks on every Begin and End.

	mu  sync.Mutex
	ops []Operation
	// pending maps a token to the partially built operation between Begin/End.
	pending map[int64]*Operation
	nextTok int64
}

// NewRecorder returns an empty Recorder ready for concurrent use.
func NewRecorder() *Recorder {
	return &Recorder{pending: make(map[int64]*Operation)}
}

// tick returns the next logical timestamp. Monotonic across all goroutines.
func (r *Recorder) tick() int64 { return atomic.AddInt64(&r.clock, 1) }

// Begin records the invocation of an operation by clientID with the given
// input and returns an opaque token that MUST be passed to End. The Call
// timestamp is taken now.
func (r *Recorder) Begin(clientID int, input interface{}) int64 {
	call := r.tick()
	r.mu.Lock()
	tok := r.nextTok
	r.nextTok++
	r.pending[tok] = &Operation{ClientID: clientID, Input: input, Call: call}
	r.mu.Unlock()
	return tok
}

// End records the response (output) for the operation identified by token and
// finalizes it into the history. The Return timestamp is taken now and is, by
// construction of the logical clock, strictly greater than the matching Call.
// Calling End with an unknown or already-finalized token is a no-op.
func (r *Recorder) End(token int64, output interface{}) {
	ret := r.tick()
	r.mu.Lock()
	op, ok := r.pending[token]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.pending, token)
	op.Output = output
	op.Return = ret
	r.ops = append(r.ops, *op)
	r.mu.Unlock()
}

// History returns a copy of all finalized operations, sorted by Call time.
// Operations whose End was never called are omitted (they have no Return and
// would make the history ill-formed).
func (r *Recorder) History() []Operation {
	r.mu.Lock()
	out := make([]Operation, len(r.ops))
	copy(out, r.ops)
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Call < out[j].Call })
	return out
}
