package main

// htmux_adversarial_test.go — adversarial probes for cmd/htmux.
//
// NOTE ON SCOPE: the task brief described cmd/htmux as an "HTTP mux/router/
// proxy" with a route/connection map, longest-prefix routing, and path-
// traversal surface. That premise does NOT match this package. cmd/htmux is
// the Helix Terminal Multiplexer: a thin gRPC *client* CLI for SessionService
// (main.go/client.go) plus a WebSocket terminal-streaming bridge
// (wsstream.go). There is no HTTP server, no route table, no longest-prefix
// matcher, and no per-connection map. The abstract adversarial classes are
// re-mapped onto the real surfaces that DO exist:
//
//   (a) ROUTING CORRECTNESS / BYPASS  -> command dispatch in run() (a fixed
//       switch on args[0]) and the WS URL builder defaultWSDial. Pinned below
//       as a contract (deterministic, closed default -> usage error).
//   (b) UNGUARDED SHARED STATE / TEARDOWN RACE -> streamSession runs three
//       goroutines; TWO of them (keystroke input + SIGWINCH resize) call
//       conn.WriteMessage on the SAME gorilla/websocket.Conn with NO
//       serializing mutex. gorilla forbids concurrent writers. THIS IS THE
//       REAL BUG. Proven sink-side under -race below.
//   (c) FAIL-OPEN -> run() rejects unknown commands (exit 2); DecodeEnvelope
//       rejects malformed bytes. Pinned as contracts.
//   (d) TOTAL-ORDER / DETERMINISM -> command dispatch is a fixed switch, not a
//       map iteration; resolution is deterministic. Pinned below.
//
// ANTI-HANG: httptest only, no real ports/network egress; goroutine fan-out is
// capped; every test has its own deadline and unblocks streamSession via a
// closeable stdin. `go test -timeout 90s` completes.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// (b) TEARDOWN / CONCURRENT-WRITE RACE  —  the real bug.
//
// streamSession's input goroutine and resize goroutine both call
// conn.WriteMessage on the same *gws.Conn without a write mutex. gorilla's
// WriteMessage does best-effort concurrent-write detection and PANICS with
// "concurrent write to websocket connection"; under -race the unsynchronized
// access to conn's internal write state is also flagged.
//
// SINK-SIDE: we drive real keystroke writes (tight stdin loop) concurrently
// with real SIGWINCH-driven resize writes (raised in-process via syscall.Kill)
// against a real httptest WS server that drains input. With streamStdinFd=-1,
// getTermSize returns (80,24) so the resize goroutine genuinely writes an
// envelope on every SIGWINCH — overlapping the input writer.
//
// On UNFIXED prod this FAILS: either gorilla panics (crashing the streamSession
// goroutine -> the test's recover-guard records it) or `-race` reports a data
// race on the shared conn. With a write mutex serializing the two writers, it
// PASSES.
// ---------------------------------------------------------------------------

// loopingReader emits a fixed keystroke on every Read until closed, so the
// input goroutine writes to the conn continuously (one envelope per Read).
type loopingReader struct {
	mu     sync.Mutex
	closed bool
	ch     chan struct{}
	once   sync.Once
}

func (r *loopingReader) initCh() chan struct{} {
	r.once.Do(func() { r.ch = make(chan struct{}) })
	return r.ch
}

func (r *loopingReader) Read(p []byte) (int, error) {
	ch := r.initCh()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		<-ch // block until released, then report closed
		return 0, errClosedLooping
	}
	// Emit a small keystroke burst; yield so the resize goroutine interleaves.
	n := copy(p, []byte("x"))
	time.Sleep(time.Millisecond)
	return n, nil
}

func (r *loopingReader) close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	defer func() { _ = recover() }()
	close(r.initCh())
}

type closedLoopingErr struct{}

func (closedLoopingErr) Error() string { return "looping reader closed" }

var errClosedLooping = closedLoopingErr{}

// drainWSServer upgrades and reads (drains) every client message, so the
// client's writes actually traverse the gorilla write path.
func drainWSServer(t *testing.T) wsDialFunc {
	t.Helper()
	up := gws.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(httpSrv.Close)
	return func(sessionID, _ string) (*gws.Conn, error) {
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/stream?session=" + sessionID
		conn, _, err := gws.DefaultDialer.Dial(url, nil)
		return conn, err
	}
}

func TestStreamSession_ConcurrentInputAndResizeWrites_NoRaceNoPanic(t *testing.T) {
	dial := drainWSServer(t)

	stdin := &loopingReader{}
	se := streamEnv{
		wsDial:        dial,
		streamStdin:   stdin,
		streamStdout:  &syncBuffer{},
		streamStdinFd: -1, // not a terminal: getTermSize -> (80,24); resize still writes
	}

	// Capture a panic from streamSession's writer goroutines. gorilla's
	// "concurrent write to websocket connection" panic surfaces here on
	// unfixed prod.
	panicCh := make(chan interface{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				select {
				case panicCh <- p:
				default:
				}
			}
		}()
		_ = streamSession("race-sess", "ignored", se)
	}()

	// Hammer SIGWINCH to the current process so the resize goroutine writes
	// envelopes concurrently with the input goroutine's keystroke writes.
	stop := make(chan struct{})
	var sigWG sync.WaitGroup
	sigWG.Add(1)
	go func() {
		defer sigWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = syscall.Kill(syscall.Getpid(), syscall.SIGWINCH)
				time.Sleep(200 * time.Microsecond)
			}
		}
	}()

	// Let the two writers contend for ~1s.
	deadline := time.After(1200 * time.Millisecond)
	select {
	case p := <-panicCh:
		close(stop)
		sigWG.Wait()
		stdin.close()
		t.Fatalf("streamSession writer goroutine panicked under concurrent input+resize: %v "+
			"(gorilla forbids concurrent WriteMessage on one conn; needs a write mutex)", p)
	case <-deadline:
	}

	close(stop)
	sigWG.Wait()
	stdin.close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// streamSession is blocked closing; don't hang the suite.
	}

	select {
	case p := <-panicCh:
		t.Fatalf("streamSession writer goroutine panicked on teardown under concurrent writes: %v", p)
	default:
	}
}

// ---------------------------------------------------------------------------
// (a)+(d) ROUTING DISPATCH: deterministic, closed default. CONTRACT PIN.
//
// run()'s command dispatch is a fixed switch — not a map — so resolution is
// total-ordered and deterministic, and an unrecognized command is a closed
// usage error (exit 2), never a fail-open into a handler. No bug surface here;
// pinned so a regression to a map-based or fall-through dispatch is caught.
// ---------------------------------------------------------------------------

func TestDispatch_UnknownCommandIsClosedNotFailOpen(t *testing.T) {
	e, out, errBuf := testEnv("")
	// A path-traversal-flavored / adversarial command string must NOT reach any
	// handler. Closed default => exit 2, no stdout.
	for _, cmd := range []string{"../create", "Create", "CREATE", "create/", "kill;rm", "..%2f"} {
		out.Reset()
		errBuf.Reset()
		code := run(t.Context(), []string{cmd}, e)
		if code != 2 {
			t.Fatalf("command %q: got exit %d, want 2 (closed default)", cmd, code)
		}
		if out.Len() != 0 {
			t.Fatalf("command %q: handler produced stdout %q; default must not reach a handler", cmd, out.String())
		}
		if !strings.Contains(errBuf.String(), "unknown command") {
			t.Fatalf("command %q: stderr = %q, want 'unknown command'", cmd, errBuf.String())
		}
	}
}

// ---------------------------------------------------------------------------
// (c) FAIL-OPEN on the streaming path: the WS read goroutine must NOT render
// malformed/oversized envelopes. This re-pins the decode guard from the
// adversarial angle: a binary frame with a valid fixmap header but a bin
// length prefix far larger than the actual data must be dropped (decode error),
// never partially written to stdout. CONTRACT PIN (decode already hardened).
// ---------------------------------------------------------------------------

func TestStreamSession_OversizedBinLengthNotRendered(t *testing.T) {
	up := gws.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// fixmap(4) + key "t"=Output, key "p"="", key "ts"=0, key "pl" bin32
		// claiming 0xFFFFFFFF bytes but supplying only a few — readN must error.
		frame := []byte{
			0x84,             // fixmap4
			0xa1, 't', 0x01,  // "t" -> 1 (MsgOutput)
			0xa1, 'p', 0xa0,  // "p" -> "" (fixstr len 0)
			0xa2, 't', 's', 0x00, // "ts" -> 0 (positive fixint)
			0xa2, 'p', 'l',   // "pl"
			0xc6, 0xff, 0xff, 0xff, 0xff, // bin32 len = 4294967295
			'L', 'E', 'A', 'K', // only 4 bytes actually present
		}
		_ = conn.WriteMessage(gws.BinaryMessage, frame)
		time.Sleep(300 * time.Millisecond)
	}))
	defer httpSrv.Close()

	dial := func(sessionID, _ string) (*gws.Conn, error) {
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/stream"
		conn, _, err := gws.DefaultDialer.Dial(url, nil)
		return conn, err
	}

	stdout := &syncBuffer{}
	stdin := new(infiniteReader)
	se := streamEnv{wsDial: dial, streamStdin: stdin, streamStdout: stdout, streamStdinFd: -1}

	done := make(chan error, 1)
	go func() { done <- streamSession("oversize", "ignored", se) }()

	time.Sleep(350 * time.Millisecond)
	if stdout.Contains([]byte("LEAK")) {
		t.Fatalf("fail-open: oversized-bin envelope payload rendered to stdout: %q", stdout.Bytes())
	}
	stdin.close()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
}
