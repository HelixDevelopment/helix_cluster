package quic

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Length-prefixed message framing for the Helix QUIC transport.
//
// A single message on a stream is encoded as:
//
//	[uint32 big-endian length][length bytes of payload]
//
// This mirrors the edge/gateway QUIC adapter's wire format (quicFrameMaxLen
// 8 MiB) so framing is parity-consistent across the Helix QUIC transports.
//
// SECURITY (DoS / resource exhaustion): the inbound read path MUST bound the
// declared length BEFORE allocating. A hostile or corrupt peer can advertise a
// 4 GiB length in 4 bytes; without a cap the reader would attempt to allocate
// (and io.ReadFull-fill) that buffer, an instant OOM / memory-amplification
// vector. ReadFramedMessage rejects any length above the cap WITHOUT
// allocating the payload buffer, and rejects a zero length as malformed.

// ErrMessageTooLarge is returned by ReadFramedMessage / WriteFramedMessage when
// a frame's declared (or actual) length exceeds the configured cap. It is a
// typed sentinel so callers can distinguish a policy rejection from an I/O error.
var ErrMessageTooLarge = errors.New("quic: framed message exceeds max size")

// ErrZeroLengthFrame is returned when a frame advertises a zero-byte payload,
// which is treated as malformed (matching the gateway adapter).
var ErrZeroLengthFrame = errors.New("quic: zero-length frame")

// frameHeaderLen is the size of the big-endian uint32 length prefix.
const frameHeaderLen = 4

// WriteFramedMessage writes payload to w as a single length-prefixed frame.
// It refuses (without writing anything) to emit a frame whose payload exceeds
// maxBytes, so a local bug cannot push an over-cap frame a conforming peer
// would then reject mid-stream. maxBytes <= 0 falls back to DefaultMaxMessageBytes.
func WriteFramedMessage(w io.Writer, payload []byte, maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	if len(payload) == 0 {
		return ErrZeroLengthFrame
	}
	if len(payload) > maxBytes {
		return fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(payload), maxBytes)
	}
	var hdr [frameHeaderLen]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("quic: write frame header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("quic: write frame payload: %w", err)
	}
	return nil
}

// ReadFramedMessage reads one length-prefixed frame from r, enforcing maxBytes
// on the DECLARED length before allocating the payload buffer. This is the
// anti-OOM guard: an advertised length above the cap returns ErrMessageTooLarge
// immediately and no large allocation occurs. maxBytes <= 0 falls back to
// DefaultMaxMessageBytes. A zero declared length returns ErrZeroLengthFrame.
//
// io.EOF / io.ErrUnexpectedEOF from the header or payload read are returned
// verbatim (wrapped) so callers can detect a closed stream.
func ReadFramedMessage(r io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	var hdr [frameHeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err //nolint:wrapcheck // EOF passthrough is intentional for callers.
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, ErrZeroLengthFrame
	}
	// Bound BEFORE allocating. Compare as uint64 to avoid any platform-int
	// overflow when maxBytes is near math.MaxInt.
	if uint64(n) > uint64(maxBytes) {
		return nil, fmt.Errorf("%w: declared %d > %d", ErrMessageTooLarge, n, maxBytes)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("quic: read frame payload (%d bytes): %w", n, err)
	}
	return buf, nil
}
