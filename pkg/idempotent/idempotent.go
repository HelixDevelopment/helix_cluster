// Package idempotent implements an idempotent producer/broker that achieves
// exactly-once delivery using a (ProducerID, Seq) pair, modeled entirely
// in-memory and deterministically (no clock, no network, no host access).
//
// Semantics for Broker.Append(msg) given lastSeq = the last accepted Seq for
// msg.ProducerID (or the producer's Base-1 when no message has been accepted):
//
//   - Seq == lastSeq+1  -> ACCEPTED: appended to the committed log exactly once,
//     lastSeq advances.
//   - Seq <= lastSeq     -> DUPLICATE/retry: idempotent no-op. The message is
//     NOT appended again and AlreadyApplied is returned (nil error).
//   - Seq >  lastSeq+1   -> out-of-order GAP: rejected with ErrOutOfSequence and
//     NOT appended; lastSeq is unchanged.
//
// This models a producer that retries on lost acks (re-sending an already
// accepted Seq) without the broker ever committing a duplicate.
package idempotent

import (
	"errors"
	"fmt"
)

// ErrOutOfSequence is returned by Append when a message arrives with a Seq that
// skips ahead of the expected next Seq (a gap), i.e. Seq > lastSeq+1.
var ErrOutOfSequence = errors.New("idempotent: out-of-sequence message (gap detected)")

// Result describes the outcome of an Append call for an accepted or duplicate
// message. (Gaps return a non-nil error instead.)
type Result int

const (
	// Accepted means the message was committed to the log for the first time.
	Accepted Result = iota
	// AlreadyApplied means the message (Seq <= lastSeq) was a retry/duplicate
	// and was idempotently ignored — not committed a second time.
	AlreadyApplied
)

func (r Result) String() string {
	switch r {
	case Accepted:
		return "Accepted"
	case AlreadyApplied:
		return "AlreadyApplied"
	default:
		return fmt.Sprintf("Result(%d)", int(r))
	}
}

// Message is a unit of data carrying its producer identity and per-producer
// monotonic sequence number. Payload is opaque and used only for log evidence.
type Message struct {
	ProducerID string
	Seq        int
	Payload    string
}

// Broker is an in-memory idempotent broker. It maintains one committed,
// deduplicated, ordered log and a per-producer lastSeq watermark.
//
// The zero value is not ready for use; construct one with New.
type Broker struct {
	base    int            // first valid Seq for every producer
	lastSeq map[string]int // producerID -> last accepted Seq
	seen    map[string]int // producerID -> count of messages committed (for invariants)
	log     []Message      // committed, deduplicated, in-order log
}

// New returns a Broker where each producer's first valid Seq is base. A
// producer with no committed messages has an effective lastSeq of base-1, so
// the first accepted message must carry Seq == base.
func New(base int) *Broker {
	return &Broker{
		base:    base,
		lastSeq: make(map[string]int),
		seen:    make(map[string]int),
	}
}

// expected returns the next-expected Seq for a producer (lastSeq+1). For a
// producer with no committed messages this is base.
func (b *Broker) expected(pid string) int {
	if last, ok := b.lastSeq[pid]; ok {
		return last + 1
	}
	return b.base
}

// LastSeq returns the last accepted Seq for pid, and whether any message from
// pid has been committed. When ok is false the returned seq is meaningless.
func (b *Broker) LastSeq(pid string) (seq int, ok bool) {
	seq, ok = b.lastSeq[pid]
	return seq, ok
}

// Append applies msg under exactly-once semantics. See package docs for the
// full Seq decision table.
func (b *Broker) Append(msg Message) (Result, error) {
	exp := b.expected(msg.ProducerID)

	switch {
	case msg.Seq == exp:
		// In-order: commit exactly once and advance the watermark.
		b.log = append(b.log, msg)
		b.lastSeq[msg.ProducerID] = msg.Seq
		b.seen[msg.ProducerID]++
		return Accepted, nil

	case msg.Seq < exp:
		// Seq <= lastSeq: a retry/duplicate of an already-committed (or pre-base)
		// Seq. Idempotent no-op — do NOT append again.
		return AlreadyApplied, nil

	default: // msg.Seq > exp
		// Gap: a future Seq arrived before its predecessor. Reject; do not append.
		return AlreadyApplied, fmt.Errorf("%w: producer=%q seq=%d expected=%d",
			ErrOutOfSequence, msg.ProducerID, msg.Seq, exp)
	}
}

// Log returns a copy of the committed, deduplicated, in-order log so callers
// cannot mutate broker state.
func (b *Broker) Log() []Message {
	out := make([]Message, len(b.log))
	copy(out, b.log)
	return out
}

// LogFor returns a copy of the committed messages for a single producer, in
// commit order.
func (b *Broker) LogFor(pid string) []Message {
	var out []Message
	for _, m := range b.log {
		if m.ProducerID == pid {
			out = append(out, m)
		}
	}
	return out
}
