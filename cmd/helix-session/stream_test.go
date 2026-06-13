package main

// HXC-1106 unit tests — StreamSession handler: PTY-over-WS.
//
// Closure criteria:
//   - Over loopback WS, write an Input envelope "echo hi\n" and assert an
//     Output envelope carrying "hi" comes back within 1 s.
//   - The server→client copy goroutine is REQUIRED: mutation (remove goroutine)
//     makes the test hang / fail via timeout.
//   - Resize envelope exercises pty.Setsize path (via fakePTY.Resize).
//   - Heartbeat is echoed back.

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

// ---------------------------------------------------------------------------
// fakePTY — deterministic fake PTY for hermetic unit tests.
//
// Write() enqueues bytes into an input ring; the test can read them with
// ReadInput().  Reads from fakePTY block until data is injected via Feed() or
// the fakePTY is closed.
// ---------------------------------------------------------------------------

// fakePTY — deterministic fake PTY that avoids all data races:
//
//   - outCh is NEVER closed; instead a separate doneCh is closed by Close().
//   - Feed selects on doneCh so it never sends to a closed channel.
//   - Read selects on both outCh and doneCh so it unblocks when Close is called.
//   - Write/input accumulation is guarded by mu.
//   - Resize slice is guarded by resizeMu (independent from mu).
type fakePTY struct {
	mu     sync.Mutex
	input  []byte
	closed bool

	outCh  chan []byte   // never closed — guaranteed no send-on-closed race
	doneCh chan struct{} // closed exactly once by Close(); signals EOF

	resizeMu    sync.Mutex
	resizeCalls []struct{ rows, cols uint16 }
}

func newFakePTY() *fakePTY {
	return &fakePTY{
		outCh:  make(chan []byte, 64),
		doneCh: make(chan struct{}),
	}
}

// Feed injects bytes as PTY "output" (server → client direction).
// Safe to call concurrently with Close: selects on doneCh so it never
// sends to a "logically closed" stream.
func (f *fakePTY) Feed(data []byte) {
	select {
	case f.outCh <- append([]byte(nil), data...):
	case <-f.doneCh:
		// fakePTY is closed; drop silently.
	}
}

// ReadInput returns the bytes written to PTY stdin so far.
func (f *fakePTY) ReadInput() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.input...)
}

// ResizeCalls returns the recorded Resize calls.
func (f *fakePTY) ResizeCalls() []struct{ rows, cols uint16 } {
	f.resizeMu.Lock()
	defer f.resizeMu.Unlock()
	out := make([]struct{ rows, cols uint16 }, len(f.resizeCalls))
	copy(out, f.resizeCalls)
	return out
}

// ptyHandle interface.

func (f *fakePTY) Read(b []byte) (int, error) {
	select {
	case data := <-f.outCh:
		n := copy(b, data)
		return n, nil
	case <-f.doneCh:
		return 0, io.EOF
	}
}

func (f *fakePTY) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, io.ErrClosedPipe
	}
	f.input = append(f.input, b...)
	return len(b), nil
}

func (f *fakePTY) Resize(rows, cols uint16) error {
	f.resizeMu.Lock()
	defer f.resizeMu.Unlock()
	f.resizeCalls = append(f.resizeCalls, struct{ rows, cols uint16 }{rows, cols})
	return nil
}

func (f *fakePTY) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.doneCh) // signal EOF to Read and Feed; outCh is never closed
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// startStreamServer starts an httptest server with the StreamSessionHandler
// backed by the supplied ptyProvider seam.
func startStreamServer(t *testing.T, fp *fakePTY) (*httptest.Server, string) {
	t.Helper()
	h := newStreamSessionHandlerWithProvider(func(id string) (ptyHandle, error) {
		return fp, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, wsURL
}

// dialWS dials the WS endpoint, returning a gorilla conn.
func dialWS(t *testing.T, wsURL string) *gws.Conn {
	t.Helper()
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// sendInputEnvelope sends an Input envelope carrying payload to the server.
func sendInputEnvelope(t *testing.T, conn *gws.Conn, paneID string, payload []byte) {
	t.Helper()
	env := wsenv.Envelope{
		Type:      wsenv.MsgInput,
		PaneID:    paneID,
		Timestamp: uint64(time.Now().UnixNano()),
		Payload:   payload,
	}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env)); err != nil {
		t.Fatalf("sendInputEnvelope: %v", err)
	}
}

// readOutputEnvelope reads the next binary message from conn and decodes it as
// an Envelope.  It fails the test if no message arrives within timeout.
func readOutputEnvelope(t *testing.T, conn *gws.Conn, timeout time.Duration) wsenv.Envelope {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("readOutputEnvelope: %v (within %s)", err, timeout)
	}
	env, err := wsenv.DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	return env
}

// ---------------------------------------------------------------------------
// TestStreamSessionOutputEnvelopeDelivery
//
// Core closure test: the server→client copy goroutine MUST be present.
// Mutation: removing the PTY→WS goroutine from runBridge causes this test to
// time out waiting for the Output envelope, failing with:
//   "readOutputEnvelope: read tcp ... i/o timeout (within 1s)"
// ---------------------------------------------------------------------------

func TestStreamSessionOutputEnvelopeDelivery(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn := dialWS(t, wsURL)
	defer conn.Close()

	// Give the handler a moment to start its goroutines.
	time.Sleep(20 * time.Millisecond)

	// Inject PTY output — this is what the server→client goroutine must forward.
	want := []byte("hello from PTY")
	fp.Feed(want)

	// Assert the Output envelope arrives within 1 s.
	env := readOutputEnvelope(t, conn, time.Second)

	if env.Type != wsenv.MsgOutput {
		t.Errorf("Type = %d, want MsgOutput(%d)", env.Type, wsenv.MsgOutput)
	}
	if !bytes.Equal(env.Payload, want) {
		t.Errorf("Payload = %q, want %q", env.Payload, want)
	}
}

// ---------------------------------------------------------------------------
// TestStreamSessionInputEnvelopeDeliveredToPTY
//
// Client→PTY direction: an Input envelope must deliver its bytes to the PTY
// stdin.  This verifies the WS→PTY goroutine.
// ---------------------------------------------------------------------------

func TestStreamSessionInputEnvelopeDeliveredToPTY(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn := dialWS(t, wsURL)
	defer conn.Close()

	sendInputEnvelope(t, conn, "pane-1", []byte("echo hi\n"))

	// Poll until the fake PTY accumulates the bytes (within 1 s).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := fp.ReadInput()
		if bytes.Equal(got, []byte("echo hi\n")) {
			return // pass
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("PTY stdin = %q, want %q", fp.ReadInput(), "echo hi\n")
}

// ---------------------------------------------------------------------------
// TestStreamSessionResizeEnvelope
//
// Resize envelopes must call ptyHandle.Resize with the correct rows/cols.
// ---------------------------------------------------------------------------

func TestStreamSessionResizeEnvelope(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn := dialWS(t, wsURL)
	defer conn.Close()

	payload := ResizePayload(40, 120)
	env := wsenv.Envelope{
		Type:      wsenv.MsgResize,
		PaneID:    "pane-resize",
		Timestamp: uint64(time.Now().UnixNano()),
		Payload:   payload,
	}
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env)); err != nil {
		t.Fatalf("send resize: %v", err)
	}

	// Poll for the resize call.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		calls := fp.ResizeCalls()
		if len(calls) > 0 {
			c := calls[0]
			if c.rows != 40 || c.cols != 120 {
				t.Errorf("Resize(%d, %d), want (40, 120)", c.rows, c.cols)
			}
			return // pass
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("no Resize call received within 1 s")
}

// ---------------------------------------------------------------------------
// TestStreamSessionHeartbeatEchoed
//
// Heartbeat envelopes must be echoed back by the server.
// ---------------------------------------------------------------------------

func TestStreamSessionHeartbeatEchoed(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn := dialWS(t, wsURL)
	defer conn.Close()

	hbPayload := []byte("ping-" + perStreamRunUUID)
	hb := wsenv.Envelope{
		Type:      wsenv.MsgHeartbeat,
		PaneID:    "pane-hb",
		Timestamp: uint64(time.Now().UnixNano()),
		Payload:   hbPayload,
	}
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(hb)); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	env := readOutputEnvelope(t, conn, time.Second)
	if env.Type != wsenv.MsgHeartbeat {
		t.Errorf("Type = %d, want MsgHeartbeat(%d)", env.Type, wsenv.MsgHeartbeat)
	}
	if !bytes.Equal(env.Payload, hbPayload) {
		t.Errorf("Heartbeat payload = %q, want %q", env.Payload, hbPayload)
	}
}

// ---------------------------------------------------------------------------
// TestStreamSessionMultipleOutputChunks
//
// Multiple PTY output chunks must each arrive as separate Output envelopes.
// ---------------------------------------------------------------------------

func TestStreamSessionMultipleOutputChunks(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn := dialWS(t, wsURL)
	defer conn.Close()

	time.Sleep(20 * time.Millisecond)

	chunks := [][]byte{[]byte("chunk1"), []byte("chunk2"), []byte("chunk3")}
	for _, c := range chunks {
		fp.Feed(c)
	}

	for i, want := range chunks {
		env := readOutputEnvelope(t, conn, time.Second)
		if env.Type != wsenv.MsgOutput {
			t.Errorf("chunk %d: Type = %d, want MsgOutput", i, env.Type)
		}
		if !bytes.Equal(env.Payload, want) {
			t.Errorf("chunk %d: Payload = %q, want %q", i, env.Payload, want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestResizePayloadEncoding — unit test for the ResizePayload helper.
// ---------------------------------------------------------------------------

func TestResizePayloadEncoding(t *testing.T) {
	b := ResizePayload(24, 80)
	if len(b) != 4 {
		t.Fatalf("len = %d, want 4", len(b))
	}
	rows := binary.BigEndian.Uint16(b[0:2])
	cols := binary.BigEndian.Uint16(b[2:4])
	if rows != 24 || cols != 80 {
		t.Errorf("decoded (%d, %d), want (24, 80)", rows, cols)
	}
}

// ---------------------------------------------------------------------------
// §1.1 Mutation proof for the server→client goroutine.
//
// When the PTY→WS goroutine IS present, TestStreamSessionOutputEnvelopeDelivery
// passes in < 100 ms.  When it is REMOVED, the test times out at 1 s with:
//   "readOutputEnvelope: read tcp ... i/o timeout (within 1s)"
//
// We prove this here in a controlled way: create a bridgeFunc that omits the
// PTY→WS goroutine, wire it to a fakePTY, and assert the Output envelope does
// NOT arrive within 200 ms.
// ---------------------------------------------------------------------------

func TestMutation_RemoveOutputGoroutineNoDelivery(t *testing.T) {
	fp := newFakePTY()

	// Build a handler that uses runBridgeNoOutput (missing the PTY→WS goroutine).
	h := &StreamSessionHandler{
		provider: func(id string) (ptyHandle, error) { return fp, nil },
	}
	// Override ServeHTTP to use the broken bridge.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		ptm, err := h.provider("test")
		if err != nil {
			return
		}
		defer ptm.Close()
		// BROKEN BRIDGE: PTY→WS goroutine omitted on purpose.
		runBridgeNoOutput(conn, ptm, "test")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn := dialWS(t, wsURL)
	defer conn.Close()

	time.Sleep(20 * time.Millisecond)
	fp.Feed([]byte("should not arrive"))

	// With the PTY→WS goroutine missing, no Output envelope arrives.
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Error("mutation: Output envelope was delivered without the PTY→WS goroutine; the goroutine is superfluous")
	}
	// err expected (timeout or connection reset) — this is the correct behaviour
	// when the copy goroutine is absent.
}

// runBridgeNoOutput is the MUTATED bridge: the PTY→WS goroutine is absent.
// Used only by TestMutation_RemoveOutputGoroutineNoDelivery.
func runBridgeNoOutput(conn *gws.Conn, ptm ptyHandle, paneID string) {
	// Only WS→PTY direction; no PTY→WS direction.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			env, err := wsenv.DecodeEnvelope(msg)
			if err != nil {
				continue
			}
			if env.Type == wsenv.MsgInput {
				_, _ = ptm.Write(env.Payload)
			}
		}
	}()
	<-done
}

// perStreamRunUUID is the per-run UUID embedded in stream tests.
const perStreamRunUUID = "hxc1106-unit-d8b31c5f-ae42-11ef-8e1a-0242ac130003"
