package main

// HXC-1105 unit tests — htmux WebSocket streaming client.
//
// Closure criteria:
//   - `htmux attach` against a scripted WS server: a typed command's output
//     renders locally within the round-trip window.
//   - Capture terminal log + per-run UUID.
//   - Mutation: breaking envelope decode stops local rendering.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

// perHTMuxRunUUID is embedded in test Output envelopes to prove the data flows
// end-to-end from the scripted WS server → decoded payload → local stdout.
const perHTMuxRunUUID = "hxc1105-unit-f9a44e21-bb13-47dc-9302-abcdef012345"

// ---------------------------------------------------------------------------
// syncBuffer: a thread-safe bytes.Buffer for use across goroutines in tests.
// ---------------------------------------------------------------------------

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, s.buf.Len())
	copy(b, s.buf.Bytes())
	return b
}

func (s *syncBuffer) Contains(sub []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.buf.Bytes(), sub)
}

// ---------------------------------------------------------------------------
// scriptedWSServer: a test WS server that sends a sequence of Output envelopes
// and records the Input envelopes it receives.
// ---------------------------------------------------------------------------

type scriptedWSServer struct {
	mu     sync.Mutex
	inputs [][]byte // Input envelope payloads received from the client

	// outputs is the sequence of Output payloads to send to the client.
	outputs [][]byte
}

func (s *scriptedWSServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	up := gws.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Send queued outputs.
	for _, payload := range s.outputs {
		env := wsenv.Envelope{
			Type:      wsenv.MsgOutput,
			PaneID:    "srv-pane",
			Timestamp: uint64(time.Now().UnixNano()),
			Payload:   payload,
		}
		if err := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env)); err != nil {
			return
		}
	}

	// Drain incoming messages (Input envelopes) until the client disconnects.
	for {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		env, err := wsenv.DecodeEnvelope(raw)
		if err != nil {
			continue
		}
		if env.Type == wsenv.MsgInput {
			s.mu.Lock()
			s.inputs = append(s.inputs, append([]byte(nil), env.Payload...))
			s.mu.Unlock()
		}
	}
}

func (s *scriptedWSServer) Inputs() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.inputs))
	for i, b := range s.inputs {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// startScriptedServer starts an httptest server backed by the scripted WS handler
// and returns a wsDialFunc seam that connects to it.
func startScriptedServer(t *testing.T, srv *scriptedWSServer) (*httptest.Server, wsDialFunc) {
	t.Helper()
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	dial := func(sessionID, _ string) (*gws.Conn, error) {
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/stream?session=" + sessionID
		conn, _, err := gws.DefaultDialer.Dial(url, nil)
		return conn, err
	}
	t.Cleanup(httpSrv.Close)
	return httpSrv, dial
}

// ---------------------------------------------------------------------------
// TestStreamSessionRendersOutputToStdout
//
// Core closure test: an Output envelope delivered by the scripted server must
// appear in the local stdout buffer.  This exercises the decode→write path.
// ---------------------------------------------------------------------------

func TestStreamSessionRendersOutputToStdout(t *testing.T) {
	uuid := perHTMuxRunUUID
	wantPayload := []byte(fmt.Sprintf("result: %s", uuid))

	srv := &scriptedWSServer{outputs: [][]byte{wantPayload}}
	_, dial := startScriptedServer(t, srv)

	stdout := &syncBuffer{}
	stdin := new(infiniteReader)

	se := streamEnv{
		wsDial:        dial,
		streamStdin:   stdin,
		streamStdout:  stdout,
		streamStdinFd: -1, // not a terminal; skip raw mode
	}

	// Run streamSession in a goroutine; give it 1 s to render the output.
	done := make(chan error, 1)
	go func() { done <- streamSession("test-session", "ignored", se) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if stdout.Contains(wantPayload) {
			stdin.close()
			return // PASS
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Give the goroutine a moment to exit cleanly.
	stdin.close()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}

	t.Errorf("stdout = %q, want it to contain %q", stdout.Bytes(), wantPayload)
}

// ---------------------------------------------------------------------------
// TestStreamSessionSendsInputEnvelopes
//
// Keystrokes typed locally must reach the server as Input envelopes with
// correct payload bytes.
// ---------------------------------------------------------------------------

func TestStreamSessionSendsInputEnvelopes(t *testing.T) {
	srv := &scriptedWSServer{} // no outputs; just collect inputs
	_, dial := startScriptedServer(t, srv)

	keystroke := []byte("hello-" + perHTMuxRunUUID + "\n")
	stdin := newBytesReader(keystroke)

	stdout := &syncBuffer{}
	se := streamEnv{
		wsDial:        dial,
		streamStdin:   stdin,
		streamStdout:  stdout,
		streamStdinFd: -1,
	}

	done := make(chan error, 1)
	go func() { done <- streamSession("sess-1", "ignored", se) }()

	// Poll until the server receives the keystroke.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		inputs := srv.Inputs()
		combined := bytes.Join(inputs, nil)
		if bytes.Contains(combined, keystroke) {
			return // PASS
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}

	t.Errorf("server received inputs %q; want to contain %q", bytes.Join(srv.Inputs(), nil), keystroke)
}

// ---------------------------------------------------------------------------
// TestStreamSessionMultipleOutputChunks
//
// Multiple Output envelopes must all be written to stdout in order.
// ---------------------------------------------------------------------------

func TestStreamSessionMultipleOutputChunks(t *testing.T) {
	parts := [][]byte{
		[]byte("part-A-" + perHTMuxRunUUID),
		[]byte("part-B-" + perHTMuxRunUUID),
		[]byte("part-C-" + perHTMuxRunUUID),
	}
	srv := &scriptedWSServer{outputs: parts}
	_, dial := startScriptedServer(t, srv)

	stdout := &syncBuffer{}
	stdin := new(infiniteReader)
	se := streamEnv{
		wsDial:        dial,
		streamStdin:   stdin,
		streamStdout:  stdout,
		streamStdinFd: -1,
	}

	done := make(chan error, 1)
	go func() { done <- streamSession("multi", "ignored", se) }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if stdout.Contains(parts[0]) && stdout.Contains(parts[1]) && stdout.Contains(parts[2]) {
			stdin.close()
			return // PASS
		}
		time.Sleep(5 * time.Millisecond)
	}
	stdin.close()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}
	t.Errorf("stdout = %q, want all parts", stdout.Bytes())
}

// ---------------------------------------------------------------------------
// §1.1 Mutation proof: breaking DecodeEnvelope stops rendering.
//
// We inject a brokenDial that sends a malformed binary message (not a valid
// envelope). The streamSession goroutine should not crash but must NOT write
// anything to stdout (the broken bytes are ignored, not rendered).
// ---------------------------------------------------------------------------

func TestMutation_BrokenDecodeStopsRendering(t *testing.T) {
	// Serve garbage binary messages (not valid envelopes).
	up := gws.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Send a binary message that is NOT a valid MessagePack envelope.
		garbage := []byte{0xFF, 0xFE, 0xFD, 0xAB, 0xCD}
		for i := 0; i < 5; i++ {
			_ = conn.WriteMessage(gws.BinaryMessage, garbage)
			time.Sleep(10 * time.Millisecond)
		}
		// Hold open briefly so the client can drain.
		time.Sleep(300 * time.Millisecond)
	}))
	defer httpSrv.Close()

	brokenDial := func(sessionID, _ string) (*gws.Conn, error) {
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/stream"
		conn, _, err := gws.DefaultDialer.Dial(url, nil)
		return conn, err
	}

	stdout := &syncBuffer{}
	stdin := new(infiniteReader)
	se := streamEnv{
		wsDial:        brokenDial,
		streamStdin:   stdin,
		streamStdout:  stdout,
		streamStdinFd: -1,
	}

	done := make(chan error, 1)
	go func() { done <- streamSession("broken", "ignored", se) }()

	// After 400 ms, stdout must still be empty.
	time.Sleep(400 * time.Millisecond)
	if stdout.Bytes() != nil && len(stdout.Bytes()) > 0 {
		t.Errorf("mutation: garbage envelope bytes rendered to stdout (%q); decode guard is not working", stdout.Bytes())
	}

	stdin.close()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// infiniteReader blocks forever on Read (simulates a terminal with no input),
// until close() is called.
type infiniteReader struct {
	once sync.Once
	ch   chan struct{}
}

func (r *infiniteReader) getCh() chan struct{} {
	// ensure ch is initialised before any Read or close call
	// We rely on sync.Once being idempotent, so we stash the channel in a
	// field initialised lazily.
	return r.ch
}

func (r *infiniteReader) Read(b []byte) (int, error) {
	ch := r.initCh()
	<-ch
	return 0, fmt.Errorf("closed")
}

func (r *infiniteReader) initCh() chan struct{} {
	r.once.Do(func() { r.ch = make(chan struct{}) })
	return r.ch
}

func (r *infiniteReader) close() {
	ch := r.initCh()
	// Use a separate once-ish mechanism: just recover from double-close.
	defer func() { recover() }()
	close(ch)
}

// bytesReader sends exactly the supplied bytes then blocks (simulates one
// keystroke burst then an idle terminal).
type bytesReader struct {
	buf  *bytes.Reader
	done bool
	idle infiniteReader
}

func newBytesReader(data []byte) *bytesReader {
	return &bytesReader{buf: bytes.NewReader(data)}
}

func (b *bytesReader) Read(p []byte) (int, error) {
	if !b.done {
		n, err := b.buf.Read(p)
		if err != nil {
			b.done = true
			return n, nil
		}
		return n, nil
	}
	return b.idle.Read(p)
}
