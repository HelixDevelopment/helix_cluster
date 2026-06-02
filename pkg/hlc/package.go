// Package hlc provides a Hybrid Logical Clock (Kulkarni et al., 2014) for
// Helix Cluster OS. An HLC produces timestamps that are close to physical
// (wall-clock) time yet still capture happens-before causality the way a
// Lamport clock does, giving a total order that is monotonic even when the
// underlying physical clock stalls or runs backwards.
//
// A Timestamp combines a physical component (wall-clock nanoseconds) with a
// logical counter that breaks ties and compensates for physical-clock
// regression. The physical clock is injectable so the algorithm is fully
// deterministic under test: the core never calls time.Now() itself.
package hlc

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"
)

// Timestamp is a hybrid logical clock value: a wall-clock physical component
// (nanoseconds since the Unix epoch) plus a logical counter. The zero value is
// the smallest possible timestamp.
type Timestamp struct {
	// Physical is wall-clock time in nanoseconds since the Unix epoch.
	Physical int64
	// Logical is the tie-breaking / regression-compensating counter.
	Logical uint32
}

// Compare returns -1 if t < other, 0 if t == other, and +1 if t > other,
// imposing a total order: physical component first, logical counter second.
func (t Timestamp) Compare(other Timestamp) int {
	switch {
	case t.Physical < other.Physical:
		return -1
	case t.Physical > other.Physical:
		return 1
	case t.Logical < other.Logical:
		return -1
	case t.Logical > other.Logical:
		return 1
	default:
		return 0
	}
}

// Less reports whether t is strictly ordered before other.
func (t Timestamp) Less(other Timestamp) bool { return t.Compare(other) < 0 }

// Equal reports whether t and other are the same timestamp.
func (t Timestamp) Equal(other Timestamp) bool { return t.Compare(other) == 0 }

// encodedLen is the size in bytes of the lossless big-endian wire encoding:
// 8 bytes physical (int64) + 4 bytes logical (uint32).
const encodedLen = 12

// Encode serializes the timestamp into a lossless 12-byte big-endian form.
// Big-endian is chosen so that lexicographic byte order matches the timestamp
// total order, which is convenient for use as a key.
func (t Timestamp) Encode() []byte {
	b := make([]byte, encodedLen)
	binary.BigEndian.PutUint64(b[0:8], uint64(t.Physical))
	binary.BigEndian.PutUint32(b[8:12], t.Logical)
	return b
}

// ErrShortBuffer is returned by Decode when the input is not exactly the
// expected encoded length.
var ErrShortBuffer = errors.New("hlc: buffer is not a valid encoded timestamp")

// Decode parses a 12-byte encoding produced by Encode. It is the exact inverse
// of Encode.
func Decode(b []byte) (Timestamp, error) {
	if len(b) != encodedLen {
		return Timestamp{}, ErrShortBuffer
	}
	return Timestamp{
		Physical: int64(binary.BigEndian.Uint64(b[0:8])),
		Logical:  binary.BigEndian.Uint32(b[8:12]),
	}, nil
}

// DefaultMaxDrift is the default upper bound on how far a remote physical
// timestamp is allowed to drag the local clock ahead of wall-clock time.
// Remotes more than DefaultMaxDrift nanoseconds in the future are clamped to
// (physicalNow + DefaultMaxDrift) before the HLC merge, bounding the
// permanent skew that a faulty or malicious sender can inject. 500 ms is a
// conventional choice: well above typical NTP dispersion but low enough that
// forwarded timestamps cannot stray far.
const DefaultMaxDrift = 500_000_000 * time.Nanosecond // 500 ms

// Clock is a thread-safe hybrid logical clock. Construct one with New.
type Clock struct {
	now      func() time.Time
	maxDrift int64 // nanoseconds; 0 means DefaultMaxDrift

	mu   sync.Mutex
	last Timestamp
}

// New returns a Clock that reads physical time from now. now MUST be non-nil;
// pass time.Now for production use or a deterministic stub for tests. The core
// never calls time.Now() directly — all physical time flows through now.
// The drift clamp defaults to DefaultMaxDrift; use NewWithMaxDrift to override.
func New(now func() time.Time) *Clock {
	if now == nil {
		panic("hlc: New requires a non-nil now function")
	}
	return &Clock{now: now, maxDrift: int64(DefaultMaxDrift)}
}

// NewWithMaxDrift returns a Clock like New but with an explicit drift clamp.
// maxDrift is the maximum nanosecond distance by which a remote physical
// timestamp may exceed the local wall-clock before it is clamped. Use 0 to
// restore the DefaultMaxDrift default. A negative value disables clamping.
func NewWithMaxDrift(now func() time.Time, maxDrift time.Duration) *Clock {
	if now == nil {
		panic("hlc: NewWithMaxDrift requires a non-nil now function")
	}
	d := int64(maxDrift)
	if d == 0 {
		d = int64(DefaultMaxDrift)
	}
	return &Clock{now: now, maxDrift: d}
}

// physicalNow returns the injected physical clock reading in nanoseconds.
func (c *Clock) physicalNow() int64 { return c.now().UnixNano() }

// Now returns a fresh timestamp for a local event and advances the clock.
//
// It guarantees strict monotonicity: each call returns a timestamp strictly
// greater than every prior result from this Clock, even if the physical clock
// is frozen or runs backwards. When physical time does not advance past the
// last-seen physical component, the logical counter is incremented to
// compensate; otherwise the logical counter resets to zero.
func (c *Clock) Now() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()

	phys := c.physicalNow()
	if phys > c.last.Physical {
		c.last = Timestamp{Physical: phys, Logical: 0}
	} else {
		// Physical clock did not advance: keep the (larger) last physical
		// component and bump the logical counter so the result is strictly
		// greater than the previous one.
		c.last = Timestamp{Physical: c.last.Physical, Logical: c.last.Logical + 1}
	}
	return c.last
}

// Update merges a timestamp received from a remote node and returns a new
// local timestamp suitable for the event that observed the message. The result
// is strictly greater than both the prior local timestamp and remote, encoding
// the happens-before relationship (remote -> local).
//
// Per the HLC algorithm, the merged physical component is the maximum of the
// local physical clock, the last-seen physical component, and remote's
// physical component. The logical counter is then chosen so that the result
// strictly dominates whichever input(s) share that maximal physical component.
//
// Drift clamp: if c.maxDrift > 0 and remote.Physical > physicalNow+maxDrift,
// the remote physical component is clamped to (physicalNow+maxDrift) before
// the merge. This bounds the skew a faulty or malicious sender can inject; it
// does not reject the message — it limits how far it can drag the local clock.
func (c *Clock) Update(remote Timestamp) Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()

	phys := c.physicalNow()

	// Apply drift clamp: prevent a far-future remote from permanently advancing
	// the local clock beyond what the wall-clock supports.
	remotePhys := remote.Physical
	if c.maxDrift > 0 {
		ceiling := phys + c.maxDrift
		if remotePhys > ceiling {
			remotePhys = ceiling
		}
	}

	maxPhys := phys
	if c.last.Physical > maxPhys {
		maxPhys = c.last.Physical
	}
	if remotePhys > maxPhys {
		maxPhys = remotePhys
	}

	var logical uint32
	switch {
	case maxPhys == c.last.Physical && maxPhys == remotePhys:
		// Both local and remote (possibly clamped) sit at the winning physical
		// time: take the larger logical and bump past it.
		logical = c.last.Logical
		if remote.Logical > logical {
			logical = remote.Logical
		}
		logical++
	case maxPhys == c.last.Physical:
		// Only the prior local timestamp is at the winning physical time.
		logical = c.last.Logical + 1
	case maxPhys == remotePhys:
		// Only the remote (possibly clamped) timestamp is at the winning
		// physical time; bump past it so the local event is causally after the
		// received message.
		logical = remote.Logical + 1
	default:
		// The physical clock strictly advanced past both: fresh logical run.
		logical = 0
	}

	c.last = Timestamp{Physical: maxPhys, Logical: logical}
	return c.last
}

// Last returns the most recent timestamp emitted by this Clock without
// advancing it. The zero Timestamp is returned before the first event.
func (c *Clock) Last() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}
