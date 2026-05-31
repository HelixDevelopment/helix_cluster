package websocket

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// helpers to build raw RFC 6455 frames as a real client would (masked).

// buildClientFrame builds a masked client frame (FIN set, given opcode/payload).
func buildClientFrame(opcode byte, payload []byte, mask [4]byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x80 | (opcode & 0x0f)) // FIN + opcode
	n := len(payload)
	switch {
	case n <= 125:
		buf.WriteByte(0x80 | byte(n)) // MASK bit set + len
	case n <= 0xFFFF:
		buf.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(n))
		buf.Write(ext[:])
	default:
		buf.WriteByte(0x80 | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		buf.Write(ext[:])
	}
	buf.Write(mask[:])
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	buf.Write(masked)
	return buf.Bytes()
}

// buildUnmaskedClientFrame builds a client frame WITHOUT the mask bit (illegal per RFC 6455).
func buildUnmaskedClientFrame(opcode byte, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x80 | (opcode & 0x0f))
	buf.WriteByte(byte(len(payload))) // no MASK bit, len <= 125 assumed
	buf.Write(payload)
	return buf.Bytes()
}

func TestMaskXORRoundTrip(t *testing.T) {
	key := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
	orig := []byte("the quick brown fox jumps over the lazy dog")
	data := make([]byte, len(orig))
	copy(data, orig)

	maskBytes(data, key, 0)
	if bytes.Equal(data, orig) {
		t.Fatal("masking produced identical bytes; XOR not applied")
	}
	// unmask is the same operation
	maskBytes(data, key, 0)
	if !bytes.Equal(data, orig) {
		t.Fatalf("XOR round-trip failed: got %q want %q", data, orig)
	}
}

func TestMaskXOROffsetContinuation(t *testing.T) {
	// Masking in two chunks with continued offset must equal masking in one pass.
	key := [4]byte{0x01, 0x02, 0x03, 0x04}
	orig := []byte("abcdefghij")
	oneShot := append([]byte(nil), orig...)
	maskBytes(oneShot, key, 0)

	chunked := append([]byte(nil), orig...)
	maskBytes(chunked[:3], key, 0)
	maskBytes(chunked[3:], key, 3)
	if !bytes.Equal(oneShot, chunked) {
		t.Fatalf("offset continuation mismatch: %v vs %v", oneShot, chunked)
	}
}

func TestReadClientFrameUnmasksPayload(t *testing.T) {
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	payload := []byte("hello server")
	raw := buildClientFrame(opText, payload, mask)

	r := bufio.NewReader(bytes.NewReader(raw))
	fr, err := readFrame(r, true, 1024)
	if err != nil {
		t.Fatalf("readFrame error: %v", err)
	}
	if fr.opcode != opText {
		t.Errorf("opcode = %d, want %d", fr.opcode, opText)
	}
	if !fr.fin {
		t.Error("expected FIN")
	}
	if !bytes.Equal(fr.payload, payload) {
		t.Fatalf("unmasked payload = %q, want %q", fr.payload, payload)
	}
}

func TestServerRejectsUnmaskedClientFrame(t *testing.T) {
	raw := buildUnmaskedClientFrame(opText, []byte("nope"))
	r := bufio.NewReader(bytes.NewReader(raw))
	_, err := readFrame(r, true, 1024)
	if !errors.Is(err, ErrUnmaskedClientFrame) {
		t.Fatalf("expected ErrUnmaskedClientFrame, got %v", err)
	}
}

// Mutation: a *masked* frame with the same payload must be accepted — proving the
// rejection is specifically about the mask bit, not the payload.
func TestServerAcceptsMaskedClientFrameMutation(t *testing.T) {
	mask := [4]byte{0xAA, 0xBB, 0xCC, 0xDD}
	raw := buildClientFrame(opText, []byte("nope"), mask)
	r := bufio.NewReader(bytes.NewReader(raw))
	fr, err := readFrame(r, true, 1024)
	if err != nil {
		t.Fatalf("masked frame should be accepted, got %v", err)
	}
	if string(fr.payload) != "nope" {
		t.Fatalf("payload = %q, want nope", fr.payload)
	}
}

func TestMaxMessageSizeEnforced(t *testing.T) {
	mask := [4]byte{0x01, 0x02, 0x03, 0x04}
	payload := bytes.Repeat([]byte("x"), 200)
	raw := buildClientFrame(opBinary, payload, mask)
	r := bufio.NewReader(bytes.NewReader(raw))
	_, err := readFrame(r, true, 100) // limit below payload
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}

// Mutation: a payload that fits under the limit must be accepted — proving the
// limit boundary is enforced, not a blanket rejection.
func TestMaxMessageSizeBoundaryMutation(t *testing.T) {
	mask := [4]byte{0x01, 0x02, 0x03, 0x04}
	payload := bytes.Repeat([]byte("x"), 100)
	raw := buildClientFrame(opBinary, payload, mask)
	r := bufio.NewReader(bytes.NewReader(raw))
	fr, err := readFrame(r, true, 100) // exactly at limit
	if err != nil {
		t.Fatalf("payload at limit should pass, got %v", err)
	}
	if len(fr.payload) != 100 {
		t.Fatalf("len = %d, want 100", len(fr.payload))
	}
}

// End-to-end over net.Pipe: a real Conn handles Ping by replying Pong,
// and ReadMessage returns application data.
func TestConnPingYieldsPong(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)

	// client writes a masked Ping then a masked Text message
	mask := [4]byte{0x09, 0x08, 0x07, 0x06}
	go func() {
		clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
		clientEnd.Write(buildClientFrame(opPing, []byte("ka"), mask))
		clientEnd.Write(buildClientFrame(opText, []byte("payload"), mask))
	}()

	// reader on the client side to capture the server's Pong response
	pong := make(chan []byte, 1)
	go func() {
		cr := bufio.NewReader(clientEnd)
		for {
			clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
			fr, err := readFrame(cr, false, 4096) // server->client frames are unmasked
			if err != nil {
				return
			}
			if fr.opcode == opPong {
				pong <- fr.payload
				return
			}
		}
	}()

	serverEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage error: %v", err)
	}
	if mt != TextMessage {
		t.Errorf("messageType = %d, want %d", mt, TextMessage)
	}
	if string(data) != "payload" {
		t.Errorf("data = %q, want payload", data)
	}

	select {
	case p := <-pong:
		if string(p) != "ka" {
			t.Errorf("pong payload = %q, want ka", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive Pong in response to Ping")
	}
}

// Mutation: with no Ping sent, no Pong must be emitted before the text message.
func TestConnNoPongWithoutPingMutation(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)
	mask := [4]byte{0x09, 0x08, 0x07, 0x06}
	go func() {
		clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
		clientEnd.Write(buildClientFrame(opText, []byte("hi"), mask))
	}()

	gotControl := make(chan byte, 1)
	go func() {
		cr := bufio.NewReader(clientEnd)
		clientEnd.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		fr, err := readFrame(cr, false, 4096)
		if err != nil {
			return
		}
		gotControl <- fr.opcode
	}()

	serverEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("ReadMessage error: %v", err)
	}

	select {
	case op := <-gotControl:
		t.Fatalf("unexpected control frame opcode %d emitted without a Ping", op)
	case <-time.After(600 * time.Millisecond):
		// good: no Pong emitted
	}
}

// Close handshake: client sends Close with a status code, server echoes Close
// with the same code; ReadMessage returns a CloseError carrying that code.
func TestConnCloseHandshakeRoundTrip(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)
	mask := [4]byte{0x42, 0x43, 0x44, 0x45}

	closeBody := make([]byte, 2)
	binary.BigEndian.PutUint16(closeBody, uint16(CloseGoingAway))
	closeBody = append(closeBody, []byte("bye")...)

	go func() {
		clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
		clientEnd.Write(buildClientFrame(opClose, closeBody, mask))
	}()

	gotCode := make(chan int, 1)
	go func() {
		cr := bufio.NewReader(clientEnd)
		clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		fr, err := readFrame(cr, false, 4096)
		if err != nil {
			gotCode <- -1
			return
		}
		if fr.opcode != opClose || len(fr.payload) < 2 {
			gotCode <- -2
			return
		}
		gotCode <- int(binary.BigEndian.Uint16(fr.payload[:2]))
	}()

	serverEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CloseError, got %v", err)
	}
	if ce.Code != CloseGoingAway {
		t.Errorf("close code = %d, want %d", ce.Code, CloseGoingAway)
	}

	select {
	case code := <-gotCode:
		if code != CloseGoingAway {
			t.Errorf("server echoed close code %d, want %d", code, CloseGoingAway)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not echo a Close frame")
	}
}

// Mutation: a different incoming close code must round-trip as that code,
// proving the code is actually read from the frame, not hardcoded.
func TestConnCloseCodeMutation(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)
	mask := [4]byte{0x42, 0x43, 0x44, 0x45}

	closeBody := make([]byte, 2)
	binary.BigEndian.PutUint16(closeBody, uint16(ClosePolicyViolation))

	go func() {
		clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
		clientEnd.Write(buildClientFrame(opClose, closeBody, mask))
	}()
	go func() {
		cr := bufio.NewReader(clientEnd)
		clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		readFrame(cr, false, 4096) // drain server's echoed close
	}()

	serverEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CloseError, got %v", err)
	}
	if ce.Code != ClosePolicyViolation {
		t.Errorf("close code = %d, want %d (mutation: must reflect actual frame)", ce.Code, ClosePolicyViolation)
	}
}

// WriteMessage produces a valid server (unmasked) frame the client can read.
func TestConnWriteMessageUnmasked(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)

	done := make(chan []byte, 1)
	go func() {
		cr := bufio.NewReader(clientEnd)
		clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		fr, err := readFrame(cr, false, 4096)
		if err != nil {
			done <- nil
			return
		}
		done <- fr.payload
	}()

	serverEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(TextMessage, []byte("from server")); err != nil {
		t.Fatalf("WriteMessage error: %v", err)
	}

	select {
	case p := <-done:
		if string(p) != "from server" {
			t.Errorf("payload = %q, want 'from server'", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive message")
	}
}

// Clean bidirectional Close initiated by the server via Close().
func TestConnCloseInitiated(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	conn := newServerConn(serverEnd, 4096)

	got := make(chan int, 1)
	go func() {
		cr := bufio.NewReader(clientEnd)
		clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		fr, err := readFrame(cr, false, 4096)
		if err != nil || fr.opcode != opClose || len(fr.payload) < 2 {
			got <- -1
			return
		}
		got <- int(binary.BigEndian.Uint16(fr.payload[:2]))
		// reply with our own close so the server's read side unblocks
		mask := [4]byte{1, 2, 3, 4}
		body := make([]byte, 2)
		binary.BigEndian.PutUint16(body, uint16(CloseNormalClosure))
		clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
		clientEnd.Write(buildClientFrame(opClose, body, mask))
	}()

	serverEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.Close(CloseNormalClosure, "shutting down"); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	select {
	case code := <-got:
		if code != CloseNormalClosure {
			t.Errorf("client saw close code %d, want %d", code, CloseNormalClosure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive server-initiated Close")
	}
}

// Ensure readFrame surfaces io.EOF cleanly on a closed reader.
func TestReadFrameEOF(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader(nil))
	_, err := readFrame(r, true, 1024)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}
