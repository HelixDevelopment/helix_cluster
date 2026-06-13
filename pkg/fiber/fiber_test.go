package fiber

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// tcpLoopback spins up a 127.0.0.1:0 TCP listener and returns a connected
// client/server pair. It uses real kernel sockets (NO container) so the framing
// is exercised over an actual stream transport.
func tcpLoopback(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()

	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("accept: %v", r.err)
	}
	t.Cleanup(func() {
		client.Close()
		r.c.Close()
	})
	return client, r.c
}

// TestRoundTripExactBytesAndOrder proves multiple messages survive the framing
// with exact bytes and in order, over a real TCP loopback.
//
// MUTATION that makes this FAIL: in writeFrame, change
//
//	binary.BigEndian.PutUint32(buf[1:headerSize], uint32(len(payload)))
//
// to PutUint32(... , uint32(len(payload)+1) ) — the reader then expects one
// extra byte and the second message's bytes shift, breaking the exact-bytes
// and order assertions (and eventually blocking on a short read).
func TestRoundTripExactBytesAndOrder(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	cl := NewConn(cConn, 0)
	sv := NewConn(sConn, 0)

	msgs := [][]byte{
		[]byte("hello"),
		[]byte(""), // empty payload must round-trip
		[]byte("validator-block-#42"),
		bytes.Repeat([]byte{0xAB, 0x00, 0xFF}, 1000), // binary, with NULs
		[]byte("final"),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, m := range msgs {
			if err := cl.WriteMessage(m); err != nil {
				t.Errorf("WriteMessage: %v", err)
				return
			}
		}
	}()

	for i, want := range msgs {
		got, err := sv.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage[%d]: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("message[%d] mismatch:\n got=%q\nwant=%q", i, got, want)
		}
	}
	wg.Wait()
}

// TestOversizedFrameRejectedNoAlloc proves an inbound frame advertising a length
// greater than the configured max is rejected with ErrFrameTooLarge and that the
// huge payload buffer is never allocated. We hand-craft a header claiming a 4 GiB
// payload but send no payload bytes; if the code allocated make([]byte, n) it
// would OOM/hang, but the size check rejects first so ReadMessage returns fast.
//
// MUTATION that makes this FAIL: in readFrame, delete the
//
//	if int64(n) > int64(c.maxSize) { return 0, nil, ErrFrameTooLarge }
//
// guard. The reader then does make([]byte, n) for n=0xFFFFFFFF and blocks in
// io.ReadFull (and attempts a 4 GiB allocation), so this test times out instead
// of getting ErrFrameTooLarge.
func TestOversizedFrameRejectedNoAlloc(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	sv := NewConn(sConn, 1024) // small max

	// Forge a header: type=DATA, length=0xFFFFFFFF (~4 GiB), no payload follows.
	var hdr [headerSize]byte
	hdr[0] = byte(FrameData)
	binary.BigEndian.PutUint32(hdr[1:], 0xFFFFFFFF)
	go func() {
		cConn.Write(hdr[:])
	}()

	done := make(chan error, 1)
	go func() {
		_, err := sv.ReadMessage()
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("want ErrFrameTooLarge, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadMessage blocked/allocated on oversized frame (no size guard)")
	}
}

// TestWriteOversizedRejected proves the writer also refuses to emit a payload
// larger than the configured maximum.
//
// MUTATION that makes this FAIL: in writeFrame, delete the
//
//	if len(payload) > c.maxSize { return ErrFrameTooLarge }
//
// guard — WriteMessage would then succeed and this test's error assertion fails.
func TestWriteOversizedRejected(t *testing.T) {
	cConn, _ := tcpLoopback(t)
	cl := NewConn(cConn, 8)
	err := cl.WriteMessage([]byte("this is longer than eight bytes"))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge from WriteMessage, got %v", err)
	}
}

// TestTruncatedFrameUnexpectedEOF proves a frame whose payload is cut short
// returns io.ErrUnexpectedEOF (the io.ReadFull contract for a partial read).
//
// MUTATION that makes this FAIL: in readFrame, replace io.ReadFull(c.rw, payload)
// with c.rw.Read(payload) — a single Read may return (k<len, nil) on a short
// stream, so the function would return a truncated payload with a nil error and
// the io.ErrUnexpectedEOF assertion fails.
func TestTruncatedFrameUnexpectedEOF(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	sv := NewConn(sConn, 0)

	// Header claims 10 bytes, but we send only 3 then close.
	var hdr [headerSize]byte
	hdr[0] = byte(FrameData)
	binary.BigEndian.PutUint32(hdr[1:], 10)
	go func() {
		cConn.Write(hdr[:])
		cConn.Write([]byte("abc"))
		cConn.Close()
	}()

	_, err := sv.ReadMessage()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want io.ErrUnexpectedEOF on truncated payload, got %v", err)
	}
}

// TestTruncatedHeaderUnexpectedEOF proves a partial header is also reported as
// io.ErrUnexpectedEOF rather than a silent short read.
//
// MUTATION that makes this FAIL: in readFrame, replace
// io.ReadFull(c.rw, hdr[:]) with c.rw.Read(hdr[:]); a 2-byte write then yields
// (2, nil) and the function proceeds to decode garbage instead of erroring.
func TestTruncatedHeaderUnexpectedEOF(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	sv := NewConn(sConn, 0)
	go func() {
		cConn.Write([]byte{byte(FrameData), 0x00}) // only 2 of 5 header bytes
		cConn.Close()
	}()
	_, err := sv.ReadMessage()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want io.ErrUnexpectedEOF on truncated header, got %v", err)
	}
}

// TestHeartbeatDeliveredAsHeartbeatNotData proves a Ping frame is observable as
// a PING via ReadFrame and is NOT surfaced by ReadMessage as application data.
// It asserts the exact FrameType and the exact echoed payload.
//
// MUTATION that makes this FAIL: in WritePing, change the frame type from
// FramePing to FrameData. ReadFrame would then report FrameData (assertion
// `gotType != FramePing` fires) AND ReadMessage would deliver the ping payload
// as data, so the "heartbeat must not be data" half also fails.
func TestHeartbeatDeliveredAsHeartbeatNotData(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	cl := NewConn(cConn, 0)
	sv := NewConn(sConn, 0)

	pingPayload := []byte("are-you-alive")

	// Part 1: a raw Ping is seen as a PING frame, not data, with exact payload.
	go func() {
		if err := cl.WritePing(pingPayload); err != nil {
			t.Errorf("WritePing: %v", err)
		}
	}()
	gotType, gotPayload, err := sv.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if gotType != FramePing {
		t.Fatalf("want FramePing, got %v", gotType)
	}
	if !bytes.Equal(gotPayload, pingPayload) {
		t.Fatalf("ping payload mismatch: got=%q want=%q", gotPayload, pingPayload)
	}

	// Part 2: ReadMessage must skip a Ping (answering it with Pong) and only
	// return the subsequent DATA frame.
	go func() {
		if err := cl.WritePing([]byte("hb")); err != nil {
			t.Errorf("WritePing: %v", err)
			return
		}
		if err := cl.WriteMessage([]byte("real-data")); err != nil {
			t.Errorf("WriteMessage: %v", err)
		}
	}()

	// The Pong auto-reply from sv (emitted by sv.ReadMessage when it skips the
	// "hb" ping below) must be drained so it does not block sv's writer. We
	// synchronize on a channel + select/timeout so this goroutine is guaranteed
	// to have completed before the test function returns: calling t.Errorf from a
	// goroutine that outlives the test panics ("Errorf called after test
	// finished"), so the drain result is asserted inline on the test goroutine.
	type drainRes struct {
		ft FrameType
		e  error
	}
	drainCh := make(chan drainRes, 1)
	go func() {
		ft, _, e := cl.ReadFrame()
		drainCh <- drainRes{ft, e}
	}()

	data, err := sv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage after ping: %v", err)
	}
	if !bytes.Equal(data, []byte("real-data")) {
		t.Fatalf("ReadMessage returned %q, want real-data (ping leaked as data?)", data)
	}

	// Now collect the drained Pong on the test goroutine (not in the goroutine),
	// so any assertion runs before the test returns. The Pong is auto-sent by
	// sv.ReadMessage above, so it must already be available.
	select {
	case r := <-drainCh:
		if r.e != nil {
			t.Fatalf("draining auto Pong reply: %v", r.e)
		}
		if r.ft != FramePong {
			t.Fatalf("expected auto Pong reply, got %v", r.ft)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto Pong reply not drained within 2s")
	}
}

// TestPingAutoAnsweredWithPong proves ReadMessage answers an inbound Ping with a
// Pong frame carrying the SAME payload, so a peer can detect liveness.
//
// MUTATION that makes this FAIL: in ReadMessage's FramePing case, delete the
// `c.WritePong(payload)` call (replace with a no-op). No Pong is then emitted
// and the ReadFrame below blocks until the 2s timeout fires.
func TestPingAutoAnsweredWithPong(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	cl := NewConn(cConn, 0)
	sv := NewConn(sConn, 0)

	ping := []byte("ping-token-7")

	// Drive sv's ReadMessage so it processes the ping and emits a pong, then
	// blocks waiting for data (which never comes — that's fine).
	go func() {
		sv.ReadMessage() //nolint:errcheck // blocks until conn closes at cleanup
	}()
	if err := cl.WritePing(ping); err != nil {
		t.Fatalf("WritePing: %v", err)
	}

	type res struct {
		ft FrameType
		p  []byte
		e  error
	}
	ch := make(chan res, 1)
	go func() {
		ft, p, e := cl.ReadFrame()
		ch <- res{ft, p, e}
	}()

	select {
	case r := <-ch:
		if r.e != nil {
			t.Fatalf("ReadFrame for pong: %v", r.e)
		}
		if r.ft != FramePong {
			t.Fatalf("want FramePong auto-reply, got %v", r.ft)
		}
		if !bytes.Equal(r.p, ping) {
			t.Fatalf("pong payload %q != ping payload %q", r.p, ping)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Pong auto-reply within 2s (ReadMessage did not answer Ping)")
	}
}

// TestUnknownFrameTypeRejected proves a frame with an undefined type byte is
// rejected with ErrUnknownFrameType rather than being treated as data.
//
// MUTATION that makes this FAIL: in readFrame, replace the type switch
// default branch (return ErrUnknownFrameType) with a fallthrough that accepts
// the type — the forged 0x7F frame would then be returned and the error
// assertion fails.
func TestUnknownFrameTypeRejected(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	sv := NewConn(sConn, 0)
	go func() {
		var hdr [headerSize]byte
		hdr[0] = 0x7F // not DATA/PING/PONG
		binary.BigEndian.PutUint32(hdr[1:], 0)
		cConn.Write(hdr[:])
	}()
	_, _, err := sv.ReadFrame()
	if !errors.Is(err, ErrUnknownFrameType) {
		t.Fatalf("want ErrUnknownFrameType, got %v", err)
	}
}

// TestConcurrentSingleReaderSingleWriter exercises one writer + one reader on
// each end concurrently over a full-duplex pipe and verifies the multiset of
// delivered messages is exactly the multiset sent. Run under -race this catches
// any data race on the Conn's reader/writer paths.
//
// MUTATION that makes this FAIL: in writeFrame, remove `c.wmu.Lock()` /
// `defer c.wmu.Unlock()`. Two writers (here one per side, but the lock also
// guards the close re-check) plus the single buffered Write keep frames intact;
// removing the per-side serialization lets the buffered header+payload writes
// race with the close-state read, which -race flags. (Functionally, with a
// single writer per side the bytes assertion may still pass, but `go test
// -race` reports the unsynchronized access — so this guards the documented
// contract.)
func TestConcurrentSingleReaderSingleWriter(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	ca := NewConn(a, 0)
	cb := NewConn(b, 0)

	const n = 200
	sent := make([][]byte, n)
	for i := range sent {
		sent[i] = []byte("msg-" + itoa(i))
	}

	var wg sync.WaitGroup

	// A writes to B.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, m := range sent {
			if err := ca.WriteMessage(m); err != nil {
				t.Errorf("A WriteMessage: %v", err)
				return
			}
		}
	}()

	// B reads from A.
	got := make([][]byte, 0, n)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			m, err := cb.ReadMessage()
			if err != nil {
				t.Errorf("B ReadMessage: %v", err)
				return
			}
			got = append(got, m)
		}
	}()

	wg.Wait()

	if len(got) != n {
		t.Fatalf("received %d messages, want %d", len(got), n)
	}
	for i := range sent {
		if !bytes.Equal(got[i], sent[i]) {
			t.Fatalf("order/byte mismatch at %d: got=%q want=%q", i, got[i], sent[i])
		}
	}
}

// TestClosePreventsFurtherWrites proves Close is idempotent and that writes
// after Close return ErrClosed.
//
// MUTATION that makes this FAIL: in writeFrame, remove the post-lock
// `if c.isClosed() { return ErrClosed }` re-check AND the pre-lock one. On a
// net.Pipe the write would then block instead of returning ErrClosed, timing
// out the assertion. (On TCP it could also succeed; the explicit closed-channel
// check is what makes the contract deterministic.)
func TestClosePreventsFurtherWrites(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { b.Close() })
	ca := NewConn(a, 0)

	if err := ca.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := ca.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- ca.WriteMessage([]byte("after close")) }()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("want ErrClosed after Close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WriteMessage after Close blocked instead of returning ErrClosed")
	}
}

// TestCloseClosesUnderlyingCloser proves Close propagates to an io.Closer
// underlying transport so a blocked reader is unblocked.
//
// MUTATION that makes this FAIL: in Close, delete the
// `if cl, ok := c.rw.(io.Closer); ok { err = cl.Close() }` block. The blocked
// ReadMessage below would never return and the test times out.
func TestCloseClosesUnderlyingCloser(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { b.Close() })
	ca := NewConn(a, 0)

	readErr := make(chan error, 1)
	go func() {
		_, err := ca.ReadMessage()
		readErr <- err
	}()
	// Give the reader a moment to block on Read.
	time.Sleep(50 * time.Millisecond)
	if err := ca.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("expected non-nil read error after Close, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadMessage did not unblock after Close (underlying Closer not called)")
	}
}

// TestFrameTypeString locks the human-readable rendering used in diagnostics.
//
// MUTATION that makes this FAIL: in FrameType.String, swap the FrameData and
// FramePing return strings — the table assertions for "DATA"/"PING" fail.
func TestFrameTypeString(t *testing.T) {
	cases := []struct {
		t    FrameType
		want string
	}{
		{FrameData, "DATA"},
		{FramePing, "PING"},
		{FramePong, "PONG"},
		{FrameType(0x55), "FrameType(0x55)"},
	}
	for _, tc := range cases {
		if got := tc.t.String(); got != tc.want {
			t.Errorf("FrameType(%#x).String() = %q, want %q", byte(tc.t), got, tc.want)
		}
	}
}

// itoa is a tiny base-10 formatter to avoid importing strconv in the test body
// for a single use (keeps the message generation allocation-light).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
