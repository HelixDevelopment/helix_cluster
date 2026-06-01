// Package websocket – envelope.go
// Binary MessagePack-format envelope for the Helix Cluster remote-attach
// vertical.  This file is deliberately dependency-free: it hand-codes a
// minimal subset of MessagePack (fixmap, bin8/bin16/bin32, uint8/16/32/64,
// fixstr) using encoding/binary only. No external msgpack package is used.
//
// MessagePack layout used for Envelope:
//
//	fixmap of 5 entries { 0xd5 }
//	"t"        -> uint8  (MsgType)
//	"p"        -> str    (PaneID)
//	"ts"       -> uint64 (Timestamp, Unix nanoseconds)
//	"pl"       -> bin    (Payload)
//
// We use a 4-key fixmap (0x84) with fixed key names.

package websocket

import (
	"encoding/binary"
	"fmt"
)

// MsgType classifies the content of a WebSocket envelope.
type MsgType uint8

const (
	// MsgOutput carries PTY output bytes from server → client.
	MsgOutput MsgType = 1
	// MsgInput carries keystroke bytes from client → server.
	MsgInput MsgType = 2
	// MsgResize carries terminal resize dimensions from client → server.
	MsgResize MsgType = 3
	// MsgHeartbeat is a keepalive ping/pong at the envelope layer.
	MsgHeartbeat MsgType = 4
	// MsgSessionEvt carries session lifecycle events.
	MsgSessionEvt MsgType = 5
	// MsgError carries an error message from server → client.
	MsgError MsgType = 6
)

// Envelope is the binary MessagePack-encoded message exchanged over the
// WebSocket connection between helix-session and htmux.
type Envelope struct {
	// Type classifies the message.
	Type MsgType
	// PaneID is the target / source pane identifier.
	PaneID string
	// Timestamp is Unix nanoseconds at encode time.
	Timestamp uint64
	// Payload carries the message body (PTY bytes, resize dims, etc.).
	Payload []byte
}

// ---------------------------------------------------------------------------
// MessagePack constants used by the hand-coded codec
// ---------------------------------------------------------------------------

// fixmap4 = 0x84: fixmap with 4 entries
const mpFixmap4 byte = 0x84

// uint8 format: 0xcc <value>
const mpUint8 byte = 0xcc

// uint16 format: 0xcd <big-endian uint16>
const mpUint16 byte = 0xcd

// uint32 format: 0xce <big-endian uint32>
const mpUint32 byte = 0xce

// uint64 format: 0xcf <big-endian uint64>
const mpUint64 byte = 0xcf

// bin8 format: 0xc4 <len uint8> <bytes>
const mpBin8 byte = 0xc4

// bin16 format: 0xc5 <len uint16> <bytes>
const mpBin16 byte = 0xc5

// bin32 format: 0xc6 <len uint32> <bytes>
const mpBin32 byte = 0xc6

// fixstr base: 0xa0 | len (for len <= 31)
const mpFixstrBase byte = 0xa0

// str8 format: 0xd9 <len uint8> <bytes>
const mpStr8 byte = 0xd9

// ---------------------------------------------------------------------------
// Encoder
// ---------------------------------------------------------------------------

// appendUint appends the most compact MessagePack integer encoding for v.
func appendUint(dst []byte, v uint64) []byte {
	switch {
	case v <= 0x7f: // positive fixint
		return append(dst, byte(v))
	case v <= 0xff:
		return append(dst, mpUint8, byte(v))
	case v <= 0xffff:
		return append(dst, mpUint16, byte(v>>8), byte(v))
	case v <= 0xffffffff:
		b := [5]byte{mpUint32, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		return append(dst, b[:]...)
	default:
		b := [9]byte{mpUint64,
			byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
			byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
		}
		return append(dst, b[:]...)
	}
}

// appendStr appends a MessagePack str encoding for s.
func appendStr(dst []byte, s string) []byte {
	n := len(s)
	switch {
	case n <= 31:
		dst = append(dst, mpFixstrBase|byte(n))
	default:
		// str8 (we only need this for PaneID > 31 chars, unlikely but correct)
		dst = append(dst, mpStr8, byte(n))
	}
	return append(dst, s...)
}

// appendBin appends a MessagePack bin encoding for b.
func appendBin(dst []byte, b []byte) []byte {
	n := len(b)
	switch {
	case n <= 0xff:
		dst = append(dst, mpBin8, byte(n))
	case n <= 0xffff:
		dst = append(dst, mpBin16, byte(n>>8), byte(n))
	default:
		be := [5]byte{mpBin32, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
		dst = append(dst, be[:]...)
	}
	return append(dst, b...)
}

// EncodeEnvelope serializes e into a MessagePack-format byte slice.
// The format is a fixmap(4) with keys "t", "p", "ts", "pl".
func EncodeEnvelope(e Envelope) []byte {
	// Rough capacity estimate to avoid most reallocations.
	capacity := 1 + // fixmap4
		3 + 2 + // key "t" + uint value
		3 + 2 + len(e.PaneID) + // key "p" + str
		4 + 9 + // key "ts" + uint64
		4 + 5 + len(e.Payload) // key "pl" + bin
	dst := make([]byte, 0, capacity)

	dst = append(dst, mpFixmap4) // fixmap 4 entries

	// entry 1: "t" -> type
	dst = appendStr(dst, "t")
	dst = appendUint(dst, uint64(e.Type))

	// entry 2: "p" -> pane id
	dst = appendStr(dst, "p")
	dst = appendStr(dst, e.PaneID)

	// entry 3: "ts" -> timestamp
	dst = appendStr(dst, "ts")
	dst = appendUint(dst, e.Timestamp)

	// entry 4: "pl" -> payload
	dst = appendStr(dst, "pl")
	dst = appendBin(dst, e.Payload)

	return dst
}

// ---------------------------------------------------------------------------
// Decoder
// ---------------------------------------------------------------------------

// msgpackReader is a tiny cursor over a []byte for MessagePack decoding.
type msgpackReader struct {
	buf []byte
	pos int
}

func (r *msgpackReader) remaining() int { return len(r.buf) - r.pos }

func (r *msgpackReader) readByte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, fmt.Errorf("msgpack: unexpected EOF at position %d", r.pos)
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *msgpackReader) readN(n int) ([]byte, error) {
	if r.pos+n > len(r.buf) {
		return nil, fmt.Errorf("msgpack: need %d bytes at pos %d, have %d", n, r.pos, r.remaining())
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// readUint decodes a MessagePack integer (positive fixint, uint8..uint64).
func (r *msgpackReader) readUint() (uint64, error) {
	b, err := r.readByte()
	if err != nil {
		return 0, err
	}
	switch {
	case b <= 0x7f: // positive fixint
		return uint64(b), nil
	case b == mpUint8:
		v, err := r.readByte()
		return uint64(v), err
	case b == mpUint16:
		bs, err := r.readN(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(bs)), nil
	case b == mpUint32:
		bs, err := r.readN(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(bs)), nil
	case b == mpUint64:
		bs, err := r.readN(8)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(bs), nil
	default:
		return 0, fmt.Errorf("msgpack: expected integer, got 0x%02x at pos %d", b, r.pos-1)
	}
}

// readStr decodes a MessagePack string (fixstr or str8).
func (r *msgpackReader) readStr() (string, error) {
	b, err := r.readByte()
	if err != nil {
		return "", err
	}
	var n int
	switch {
	case b&0xe0 == 0xa0: // fixstr: 0xa0..0xbf
		n = int(b & 0x1f)
	case b == mpStr8:
		lenB, err := r.readByte()
		if err != nil {
			return "", err
		}
		n = int(lenB)
	default:
		return "", fmt.Errorf("msgpack: expected str, got 0x%02x at pos %d", b, r.pos-1)
	}
	bs, err := r.readN(n)
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

// readBin decodes a MessagePack bin (bin8, bin16, bin32) and returns the bytes.
func (r *msgpackReader) readBin() ([]byte, error) {
	b, err := r.readByte()
	if err != nil {
		return nil, err
	}
	var n int
	switch b {
	case mpBin8:
		lenB, err := r.readByte()
		if err != nil {
			return nil, err
		}
		n = int(lenB)
	case mpBin16:
		bs, err := r.readN(2)
		if err != nil {
			return nil, err
		}
		n = int(binary.BigEndian.Uint16(bs))
	case mpBin32:
		bs, err := r.readN(4)
		if err != nil {
			return nil, err
		}
		n = int(binary.BigEndian.Uint32(bs))
	default:
		return nil, fmt.Errorf("msgpack: expected bin, got 0x%02x at pos %d", b, r.pos-1)
	}
	if n == 0 {
		return []byte{}, nil
	}
	return r.readN(n)
}

// DecodeEnvelope deserializes a MessagePack-encoded Envelope from data.
// It is strict: the top-level must be a fixmap(4) with keys "t", "p", "ts", "pl"
// in any order.
func DecodeEnvelope(data []byte) (Envelope, error) {
	r := &msgpackReader{buf: data}

	// Expect fixmap(4).
	b, err := r.readByte()
	if err != nil {
		return Envelope{}, fmt.Errorf("msgpack decode: %w", err)
	}
	if b != mpFixmap4 {
		return Envelope{}, fmt.Errorf("msgpack decode: expected fixmap(4) 0x84, got 0x%02x", b)
	}

	var e Envelope
	seen := [4]bool{} // t, p, ts, pl
	for i := 0; i < 4; i++ {
		key, err := r.readStr()
		if err != nil {
			return Envelope{}, fmt.Errorf("msgpack decode key %d: %w", i, err)
		}
		switch key {
		case "t":
			v, err := r.readUint()
			if err != nil {
				return Envelope{}, fmt.Errorf("msgpack decode 't': %w", err)
			}
			e.Type = MsgType(v)
			seen[0] = true
		case "p":
			s, err := r.readStr()
			if err != nil {
				return Envelope{}, fmt.Errorf("msgpack decode 'p': %w", err)
			}
			e.PaneID = s
			seen[1] = true
		case "ts":
			v, err := r.readUint()
			if err != nil {
				return Envelope{}, fmt.Errorf("msgpack decode 'ts': %w", err)
			}
			e.Timestamp = v
			seen[2] = true
		case "pl":
			b, err := r.readBin()
			if err != nil {
				return Envelope{}, fmt.Errorf("msgpack decode 'pl': %w", err)
			}
			e.Payload = b
			seen[3] = true
		default:
			return Envelope{}, fmt.Errorf("msgpack decode: unexpected key %q", key)
		}
	}

	for i, k := range []string{"t", "p", "ts", "pl"} {
		if !seen[i] {
			return Envelope{}, fmt.Errorf("msgpack decode: missing key %q", k)
		}
	}

	return e, nil
}
