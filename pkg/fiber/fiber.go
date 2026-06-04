// Package fiber provides a clean-room, stdlib-only reliable framed message
// transport for miner<->validator style links (Phase 8C "fiber" concept).
//
// The transport is layered over any io.ReadWriter so that a post-quantum
// encrypted stream (e.g. an mlkem/hkdf-derived AEAD stream) can be slotted
// underneath it later without touching this layer. The wire format is a simple
// length-prefixed framing:
//
//	+--------+------------------+--------------------------+
//	| type   | length (uint32)  | payload (length bytes)   |
//	| 1 byte | 4 bytes, big-end |                          |
//	+--------+------------------+--------------------------+
//
// The single type byte distinguishes application data frames from
// heartbeat/keepalive control frames (Ping/Pong), so a dead peer is detectable
// without confusing keepalive traffic with application messages.
//
// # Concurrency contract
//
// A *Conn is safe for exactly ONE reader goroutine and ONE writer goroutine
// running concurrently. ReadMessage / readFrame form the reader side and
// WriteMessage / WritePing / WritePong / writeFrame form the writer side; each
// side serializes itself with its own mutex. It is NOT safe to call
// ReadMessage from two goroutines at once, nor WriteMessage from two goroutines
// at once. Close is safe to call from any goroutine at any time.
package fiber

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// FrameType identifies the kind of a fiber frame. It occupies the first byte on
// the wire so the reader can route control frames (heartbeats) separately from
// application data without parsing the payload.
type FrameType byte

const (
	// FrameData carries an application-level message payload.
	FrameData FrameType = 0x01
	// FramePing is a heartbeat request; a healthy peer answers with FramePong.
	FramePing FrameType = 0x02
	// FramePong is a heartbeat response to a FramePing.
	FramePong FrameType = 0x03
)

// String renders a FrameType for diagnostics.
func (t FrameType) String() string {
	switch t {
	case FrameData:
		return "DATA"
	case FramePing:
		return "PING"
	case FramePong:
		return "PONG"
	default:
		return fmt.Sprintf("FrameType(0x%02x)", byte(t))
	}
}

// headerSize is the fixed on-wire header: 1 type byte + 4 length bytes.
const headerSize = 5

// DefaultMaxFrameSize is the default upper bound on a single frame payload
// (16 MiB). It is deliberately finite so a hostile peer cannot induce a huge
// allocation by advertising an enormous length prefix.
const DefaultMaxFrameSize = 16 << 20

// Sentinel errors surfaced by the fiber transport.
var (
	// ErrFrameTooLarge is returned when an inbound frame advertises a payload
	// length greater than the configured max, or when WriteMessage is asked to
	// send a payload larger than the max. The oversized buffer is never
	// allocated for inbound frames.
	ErrFrameTooLarge = errors.New("fiber: frame exceeds maximum allowed size")
	// ErrClosed is returned by operations on a Conn whose Close has been called.
	ErrClosed = errors.New("fiber: connection closed")
	// ErrUnknownFrameType is returned when a frame carries a type byte that is
	// not one of the defined FrameType values.
	ErrUnknownFrameType = errors.New("fiber: unknown frame type")
)

// Conn is a framed message transport over an io.ReadWriter. See the package
// documentation for the one-reader/one-writer concurrency contract.
type Conn struct {
	rw      io.ReadWriter
	maxSize int

	rmu sync.Mutex // serializes the reader side
	wmu sync.Mutex // serializes the writer side

	closeOnce sync.Once
	closed    chan struct{} // closed exactly once by Close
}

// NewConn wraps rw in a fiber Conn. maxFrameSize bounds inbound and outbound
// payload sizes; a value <= 0 selects DefaultMaxFrameSize.
func NewConn(rw io.ReadWriter, maxFrameSize int) *Conn {
	if maxFrameSize <= 0 {
		maxFrameSize = DefaultMaxFrameSize
	}
	// The on-wire length prefix is a uint32; cap maxSize so a valid payload
	// length always fits the header field (writeFrame relies on this).
	if maxFrameSize > 0xffffffff {
		maxFrameSize = 0xffffffff
	}
	return &Conn{
		rw:      rw,
		maxSize: maxFrameSize,
		closed:  make(chan struct{}),
	}
}

// MaxFrameSize reports the configured maximum payload size in bytes.
func (c *Conn) MaxFrameSize() int { return c.maxSize }

// isClosed reports whether Close has been called.
func (c *Conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// writeFrame writes one framed message. It serializes concurrent writers and
// enforces the max payload size. A single buffered write of the header+payload
// is used so that the header and payload cannot be interleaved with another
// writer's bytes (the wmu lock already guarantees that, but the single Write
// also keeps small frames to one syscall on a net.Conn).
func (c *Conn) writeFrame(t FrameType, payload []byte) error {
	if len(payload) > c.maxSize {
		return ErrFrameTooLarge
	}
	if c.isClosed() {
		return ErrClosed
	}

	buf := make([]byte, headerSize+len(payload))
	buf[0] = byte(t)
	binary.BigEndian.PutUint32(buf[1:headerSize], uint32(len(payload))) //gosec:disable G115 -- len(payload) <= c.maxSize, which NewConn caps at 0xffffffff; fits uint32
	copy(buf[headerSize:], payload)

	c.wmu.Lock()
	defer c.wmu.Unlock()
	// Re-check after acquiring the lock so a Close racing with this write is
	// still reported (best effort; the underlying rw may also error).
	if c.isClosed() {
		return ErrClosed
	}
	if _, err := c.rw.Write(buf); err != nil {
		return err
	}
	return nil
}

// readFrame reads exactly one frame, returning its type and payload. It rejects
// oversized frames BEFORE allocating the payload buffer, so a malicious length
// prefix cannot cause a large allocation. A truncated header or payload yields
// io.ErrUnexpectedEOF (via io.ReadFull).
func (c *Conn) readFrame() (FrameType, []byte, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return 0, nil, err
	}
	t := FrameType(hdr[0])
	switch t {
	case FrameData, FramePing, FramePong:
	default:
		return 0, nil, ErrUnknownFrameType
	}

	n := binary.BigEndian.Uint32(hdr[1:headerSize])
	if int64(n) > int64(c.maxSize) {
		// Reject before allocating: do NOT make([]byte, n) for attacker-chosen n.
		return 0, nil, ErrFrameTooLarge
	}
	if n == 0 {
		return t, nil, nil
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return 0, nil, err
	}
	return t, payload, nil
}

// WriteMessage sends data as a single application data frame. It returns
// ErrFrameTooLarge if len(data) exceeds the configured maximum and ErrClosed if
// the connection has been closed.
func (c *Conn) WriteMessage(data []byte) error {
	return c.writeFrame(FrameData, data)
}

// WritePing sends a heartbeat request. payload may be nil; if non-nil it is
// echoed by a conforming peer in the corresponding Pong.
func (c *Conn) WritePing(payload []byte) error {
	return c.writeFrame(FramePing, payload)
}

// WritePong sends a heartbeat response, typically echoing a received Ping's
// payload.
func (c *Conn) WritePong(payload []byte) error {
	return c.writeFrame(FramePong, payload)
}

// ReadMessage reads the next application DATA message, transparently answering
// inbound Ping frames with a Pong and silently discarding Pong frames. It only
// returns to the caller when a data frame arrives or an error occurs, so
// heartbeat traffic is never delivered as application data.
//
// To observe heartbeats directly (e.g. to drive liveness timers), use
// ReadFrame instead.
func (c *Conn) ReadMessage() ([]byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		t, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch t {
		case FrameData:
			return payload, nil
		case FramePing:
			// Answer keepalive without surfacing it as data.
			if err := c.WritePong(payload); err != nil {
				return nil, err
			}
		case FramePong:
			// Liveness acknowledgement; nothing to deliver.
		}
	}
}

// ReadFrame reads the next frame of any type and returns it without any
// automatic Ping/Pong handling. This is the low-level entry point for callers
// that want to observe heartbeats themselves. It holds the reader mutex for the
// duration of the call, so it is subject to the same one-reader contract as
// ReadMessage.
func (c *Conn) ReadFrame() (FrameType, []byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	return c.readFrame()
}

// Close releases the connection. It is idempotent and safe to call from any
// goroutine. If the underlying io.ReadWriter implements io.Closer, its Close is
// invoked so that a blocked reader/writer is unblocked. Subsequent writes
// return ErrClosed; an in-flight or subsequent read returns whatever error the
// underlying transport produces once closed.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		if cl, ok := c.rw.(io.Closer); ok {
			err = cl.Close()
		}
	})
	return err
}
