package websocket

// This file provides a stdlib-only RFC 6455 frame codec and a hardened server-side
// connection wrapper. It is additive to the gorilla-based Upgrader in package.go and
// shares no state with it. It exists to give Helix Cluster OS a dependency-free,
// auditable framing path with explicit control over:
//
//   - client-frame UNMASKING (servers MUST reject unmasked client frames; RFC 6455 §5.1)
//   - mask/unmask XOR correctness (RFC 6455 §5.3)
//   - Ping/Pong keepalive (respond to Ping with Pong; RFC 6455 §5.5.2/§5.5.3)
//   - the Close handshake with status codes (RFC 6455 §5.5.1, §7.4)
//   - max-message-size enforcement (reject oversize, close with 1009)

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// MessageType identifies the type of a WebSocket application message.
type MessageType int

const (
	// TextMessage denotes a UTF-8 text data message.
	TextMessage MessageType = 1
	// BinaryMessage denotes a binary data message.
	BinaryMessage MessageType = 2
)

// RFC 6455 opcodes.
const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA
)

// RFC 6455 §7.4.1 status codes.
const (
	CloseNormalClosure   = 1000
	CloseGoingAway       = 1001
	CloseProtocolError   = 1002
	CloseUnsupportedData = 1003
	CloseNoStatusRcvd    = 1005
	CloseAbnormalClosure = 1006
	CloseInvalidPayload  = 1007
	ClosePolicyViolation = 1008
	CloseMessageTooBig   = 1009
)

// Sentinel errors surfaced by the frame codec and connection wrapper.
var (
	// ErrUnmaskedClientFrame is returned when a server reads a client frame whose
	// MASK bit is not set (RFC 6455 §5.1 requires all client→server frames to be masked).
	ErrUnmaskedClientFrame = errors.New("websocket: client frame is not masked")
	// ErrMaskedServerFrame is returned when a client reads a server frame that is masked
	// (RFC 6455 §5.1 forbids masking on server→client frames).
	ErrMaskedServerFrame = errors.New("websocket: server frame must not be masked")
	// ErrMessageTooLarge is returned when a frame or aggregated message exceeds the limit.
	ErrMessageTooLarge = errors.New("websocket: message exceeds maximum allowed size")
	// ErrControlFrameTooLarge is returned for control frames whose payload exceeds 125 bytes.
	ErrControlFrameTooLarge = errors.New("websocket: control frame payload exceeds 125 bytes")
	// ErrReservedBitsSet is returned when reserved RSV bits are set without negotiated extensions.
	ErrReservedBitsSet = errors.New("websocket: reserved bits set")
)

// CloseError carries the status code and reason from a received Close frame.
type CloseError struct {
	Code int
	Text string
}

func (e *CloseError) Error() string {
	if e.Text == "" {
		return fmt.Sprintf("websocket: close %d", e.Code)
	}
	return fmt.Sprintf("websocket: close %d: %s", e.Code, e.Text)
}

// frame is a single decoded WebSocket frame.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// maskBytes applies the WebSocket masking transform in place: data[i] ^= key[(offset+i)%4].
// XOR is its own inverse, so the same call masks and unmasks. offset allows masking a
// payload across multiple buffers while keeping the key phase aligned (RFC 6455 §5.3).
func maskBytes(data []byte, key [4]byte, offset int) {
	for i := range data {
		data[i] ^= key[(offset+i)&3]
	}
}

// readFrame reads exactly one frame from r. isServer selects masking expectations:
// when true (server reading a client), the MASK bit MUST be set and the payload is
// unmasked; when false (client reading a server), the MASK bit MUST NOT be set.
// maxPayload bounds the data payload length; control frames are always bounded at 125.
func readFrame(r *bufio.Reader, isServer bool, maxPayload int) (frame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return frame{}, err
	}

	fin := hdr[0]&0x80 != 0
	rsv := hdr[0] & 0x70
	opcode := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	lenCode := int(hdr[1] & 0x7f)

	if rsv != 0 {
		return frame{}, ErrReservedBitsSet
	}

	isControl := opcode&0x08 != 0
	if isControl {
		// Control frames must not be fragmented and must be <= 125 bytes (RFC 6455 §5.5).
		if !fin {
			return frame{}, ErrControlFrameTooLarge
		}
		if lenCode > 125 {
			return frame{}, ErrControlFrameTooLarge
		}
	}

	// Mask-bit direction enforcement.
	if isServer && !masked {
		return frame{}, ErrUnmaskedClientFrame
	}
	if !isServer && masked {
		return frame{}, ErrMaskedServerFrame
	}

	// Resolve payload length.
	var payloadLen int
	switch lenCode {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		payloadLen = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		v := binary.BigEndian.Uint64(ext[:])
		if v > uint64(^uint(0)>>1) { // overflow guard for int
			return frame{}, ErrMessageTooLarge
		}
		payloadLen = int(v)
	default:
		payloadLen = lenCode
	}

	// Enforce the size limit for data frames before allocating the payload buffer.
	if !isControl && maxPayload > 0 && payloadLen > maxPayload {
		// Drain the mask key (if present) is unnecessary — caller will close the conn.
		return frame{}, ErrMessageTooLarge
	}

	var key [4]byte
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, err
		}
	}

	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return frame{}, err
		}
	}
	if masked {
		maskBytes(payload, key, 0)
	}

	return frame{fin: fin, opcode: opcode, payload: payload}, nil
}

// writeFrame writes a single unmasked server frame to w (server→client frames are
// never masked per RFC 6455 §5.1). It handles the three length encodings.
func writeFrame(w io.Writer, fin bool, opcode byte, payload []byte) error {
	var hdr []byte
	b0 := opcode & 0x0f
	if fin {
		b0 |= 0x80
	}
	n := len(payload)
	switch {
	case n <= 125:
		hdr = []byte{b0, byte(n)}
	case n <= 0xFFFF:
		hdr = make([]byte, 4)
		hdr[0] = b0
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		hdr = make([]byte, 10)
		hdr[0] = b0
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if n > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// Conn is a hardened server-side WebSocket connection over an established net.Conn.
// It enforces client masking, size limits, and transparently answers Ping with Pong
// and Close with a reciprocal Close (the RFC 6455 close handshake).
type Conn struct {
	raw    net.Conn
	br     *bufio.Reader
	maxMsg int

	wmu       sync.Mutex // serializes all writes to raw
	closeOnce sync.Once
	closeSent bool // guarded by wmu
}

// newServerConn wraps an established server-side net.Conn. maxMsg <= 0 disables the
// data-message size limit. The handshake itself is assumed already complete; this type
// owns only the framing layer.
func newServerConn(raw net.Conn, maxMsg int) *Conn {
	return &Conn{
		raw:    raw,
		br:     bufio.NewReader(raw),
		maxMsg: maxMsg,
	}
}

// NewServerConn exposes the hardened framing layer for callers that have already
// completed the HTTP upgrade handshake and hold the raw connection. maxMsg bounds the
// size of inbound data messages (<=0 disables the limit).
func NewServerConn(raw net.Conn, maxMsg int) *Conn {
	return newServerConn(raw, maxMsg)
}

// ReadMessage reads the next application data message, transparently handling control
// frames: Ping is answered with Pong, Pong is ignored, and Close triggers a reciprocal
// Close and returns a *CloseError. Oversize messages return ErrMessageTooLarge after
// sending a 1009 Close. Fragmented data messages are reassembled.
func (c *Conn) ReadMessage() (MessageType, []byte, error) {
	var (
		msg          []byte
		msgType      MessageType
		fragmented   bool
		haveDataType bool
	)
	for {
		fr, err := readFrame(c.br, true, c.maxMsg)
		if err != nil {
			if errors.Is(err, ErrMessageTooLarge) {
				c.sendClose(CloseMessageTooBig, "message too big")
			} else if errors.Is(err, ErrUnmaskedClientFrame) || errors.Is(err, ErrReservedBitsSet) || errors.Is(err, ErrControlFrameTooLarge) {
				c.sendClose(CloseProtocolError, "protocol error")
			}
			return 0, nil, err
		}

		switch fr.opcode {
		case opPing:
			if err := c.writeControl(opPong, fr.payload); err != nil {
				return 0, nil, err
			}
		case opPong:
			// keepalive acknowledgement; nothing to deliver
		case opClose:
			code := CloseNoStatusRcvd
			reason := ""
			if len(fr.payload) >= 2 {
				code = int(binary.BigEndian.Uint16(fr.payload[:2]))
				reason = string(fr.payload[2:])
			}
			// Echo the close per RFC 6455 §5.5.1 then report it to the caller.
			c.sendClose(code, "")
			return 0, nil, &CloseError{Code: code, Text: reason}
		case opText, opBinary:
			if fragmented {
				// A new data frame while a fragmented message is in progress is a protocol error.
				c.sendClose(CloseProtocolError, "unexpected data frame")
				return 0, nil, ErrReservedBitsSet
			}
			if fr.opcode == opText {
				msgType = TextMessage
			} else {
				msgType = BinaryMessage
			}
			haveDataType = true
			msg = append(msg, fr.payload...)
			if c.maxMsg > 0 && len(msg) > c.maxMsg {
				c.sendClose(CloseMessageTooBig, "message too big")
				return 0, nil, ErrMessageTooLarge
			}
			if fr.fin {
				return msgType, msg, nil
			}
			fragmented = true
		case opContinuation:
			if !haveDataType {
				c.sendClose(CloseProtocolError, "continuation without start")
				return 0, nil, ErrReservedBitsSet
			}
			msg = append(msg, fr.payload...)
			if c.maxMsg > 0 && len(msg) > c.maxMsg {
				c.sendClose(CloseMessageTooBig, "message too big")
				return 0, nil, ErrMessageTooLarge
			}
			if fr.fin {
				fragmented = false
				return msgType, msg, nil
			}
		default:
			c.sendClose(CloseProtocolError, "unknown opcode")
			return 0, nil, fmt.Errorf("websocket: unknown opcode 0x%x", fr.opcode)
		}
	}
}

// WriteMessage writes a single unfragmented application data message as a server
// (unmasked) frame.
func (c *Conn) WriteMessage(mt MessageType, data []byte) error {
	var opcode byte
	switch mt {
	case TextMessage:
		opcode = opText
	case BinaryMessage:
		opcode = opBinary
	default:
		return fmt.Errorf("websocket: invalid message type %d", mt)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.raw, true, opcode, data)
}

// WritePing sends a Ping control frame with the given (<=125 byte) payload.
func (c *Conn) WritePing(payload []byte) error {
	return c.writeControl(opPing, payload)
}

// WritePong sends a Pong control frame with the given (<=125 byte) payload.
func (c *Conn) WritePong(payload []byte) error {
	return c.writeControl(opPong, payload)
}

// writeControl writes a control frame, enforcing the 125-byte payload cap.
func (c *Conn) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		return ErrControlFrameTooLarge
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return writeFrame(c.raw, true, opcode, payload)
}

// Close performs the closing handshake: it sends a Close frame with the given status
// code and reason (if not already sent). It does not close the underlying net.Conn;
// callers own the lifetime of raw. Use CloseNow to also tear down the transport.
func (c *Conn) Close(code int, reason string) error {
	return c.sendClose(code, reason)
}

// CloseNow sends a Close frame (if not already sent) and then closes the transport.
func (c *Conn) CloseNow(code int, reason string) error {
	err := c.sendClose(code, reason)
	if cerr := c.raw.Close(); err == nil {
		err = cerr
	}
	return err
}

// sendClose writes a single Close frame at most once per connection. Subsequent calls
// are no-ops so an echoed close after a self-initiated close cannot emit a second frame.
func (c *Conn) sendClose(code int, reason string) error {
	var sendErr error
	c.closeOnce.Do(func() {
		body := make([]byte, 2+len(reason))
		binary.BigEndian.PutUint16(body[:2], uint16(code))
		copy(body[2:], reason)
		if len(body) > 125 {
			body = body[:125]
		}
		c.wmu.Lock()
		c.closeSent = true
		sendErr = writeFrame(c.raw, true, opClose, body)
		c.wmu.Unlock()
	})
	return sendErr
}
