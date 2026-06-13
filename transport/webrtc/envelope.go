package webrtc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// envelopeMagic identifies a Helix WorkEnvelope frame on the data channel.
// It guards against decoding arbitrary/garbage bytes as an envelope.
const envelopeMagic uint16 = 0x4858 // "HX"

// envelopeVersion is the wire-format version of the envelope encoding.
const envelopeVersion uint8 = 1

// maxFieldLen bounds every length-prefixed field to keep decoding safe against
// hostile or corrupt input (no unbounded allocation).
const maxFieldLen = 16 << 20 // 16 MiB

// WorkEnvelope is the unit of work dispatched peer-to-peer over the WebRTC
// data channel between Helix edge devices.
//
// It is encoded with a compact, deterministic length-prefixed binary codec
// (see EncodeEnvelope) rather than a reflective format, so the wire bytes are
// stable and field-exact across encode/decode round-trips.
type WorkEnvelope struct {
	JobID    string // unique job identifier
	Kind     string // task kind, e.g. "infer", "sense"
	Priority uint8  // 0 = lowest
	Payload  []byte // opaque task payload
	Deadline int64  // unix millis; 0 = none
}

var (
	// ErrShortBuffer is returned when a buffer ends before a full envelope is
	// decoded.
	ErrShortBuffer = errors.New("webrtc: short buffer decoding work envelope")
	// ErrBadMagic is returned when the frame does not start with the envelope
	// magic/version header.
	ErrBadMagic = errors.New("webrtc: bad work-envelope magic/version")
	// ErrFieldTooLong is returned when a length prefix exceeds maxFieldLen.
	ErrFieldTooLong = errors.New("webrtc: work-envelope field exceeds max length")
)

// EncodeEnvelope serialises a WorkEnvelope to its compact binary wire form.
//
// Layout (all integers big-endian):
//
//	magic   uint16
//	version uint8
//	priority uint8
//	deadline int64
//	jobID   : uint32 len + bytes
//	kind    : uint32 len + bytes
//	payload : uint32 len + bytes
func EncodeEnvelope(e WorkEnvelope) ([]byte, error) {
	if len(e.JobID) > maxFieldLen || len(e.Kind) > maxFieldLen || len(e.Payload) > maxFieldLen {
		return nil, ErrFieldTooLong
	}
	size := 2 + 1 + 1 + 8 + 4 + len(e.JobID) + 4 + len(e.Kind) + 4 + len(e.Payload)
	buf := make([]byte, 0, size)

	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[0:2], envelopeMagic)
	hdr[2] = envelopeVersion
	hdr[3] = e.Priority
	binary.BigEndian.PutUint64(hdr[4:12], uint64(e.Deadline))
	buf = append(buf, hdr[:]...)

	buf = appendBytes(buf, []byte(e.JobID))
	buf = appendBytes(buf, []byte(e.Kind))
	buf = appendBytes(buf, e.Payload)
	return buf, nil
}

// DecodeEnvelope parses a WorkEnvelope from its binary wire form. The returned
// Payload is a copy and does not alias b.
func DecodeEnvelope(b []byte) (WorkEnvelope, error) {
	var e WorkEnvelope
	if len(b) < 12 {
		return e, ErrShortBuffer
	}
	if binary.BigEndian.Uint16(b[0:2]) != envelopeMagic || b[2] != envelopeVersion {
		return e, ErrBadMagic
	}
	e.Priority = b[3]
	e.Deadline = int64(binary.BigEndian.Uint64(b[4:12]))
	off := 12

	jobID, off, err := readBytes(b, off)
	if err != nil {
		return WorkEnvelope{}, fmt.Errorf("jobID: %w", err)
	}
	kind, off, err := readBytes(b, off)
	if err != nil {
		return WorkEnvelope{}, fmt.Errorf("kind: %w", err)
	}
	payload, _, err := readBytes(b, off)
	if err != nil {
		return WorkEnvelope{}, fmt.Errorf("payload: %w", err)
	}
	e.JobID = string(jobID)
	e.Kind = string(kind)
	e.Payload = payload
	return e, nil
}

func appendBytes(buf, p []byte) []byte {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(p)))
	buf = append(buf, l[:]...)
	return append(buf, p...)
}

// readBytes reads a uint32-length-prefixed byte slice at off, returning a copy
// and the new offset.
func readBytes(b []byte, off int) ([]byte, int, error) {
	if off+4 > len(b) {
		return nil, off, ErrShortBuffer
	}
	n := int(binary.BigEndian.Uint32(b[off : off+4]))
	off += 4
	if n > maxFieldLen {
		return nil, off, ErrFieldTooLong
	}
	if off+n > len(b) {
		return nil, off, ErrShortBuffer
	}
	out := make([]byte, n)
	copy(out, b[off:off+n])
	return out, off + n, nil
}
