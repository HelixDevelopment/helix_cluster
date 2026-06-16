package main

// Adversarial reproducer for HXC-1896: runBridge spawns two goroutines that both
// write the same *gorilla/websocket.Conn — goroutine 1 (PTY output -> WS Output
// envelopes) and goroutine 2's heartbeat echo. gorilla/websocket forbids
// concurrent writers and panics "concurrent write to websocket connection"; the
// race detector flags the unsynchronized writes.
//
// This drives the SERVER's two writers simultaneously: the test feeds the fake
// PTY (so goroutine 1 keeps writing Output) and the client sends heartbeats (so
// goroutine 2 keeps echoing). httptest runs the server in-process, so -race
// instruments the server goroutines and reports the server-side write-write
// race. The client side is deliberately gorilla-safe: exactly ONE writer
// goroutine and ONE reader goroutine on the client conn (one concurrent reader +
// one concurrent writer is supported), so the ONLY race that can fire is the
// production server-side one.
//
// SINK-SIDE: on unfixed prod (no write mutex) this FAILS — "race detected"; with
// the writeMsg mutex it passes.

import (
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

func TestAdversarial_RunBridge_ConcurrentOutputAndHeartbeat_NoServerRace(t *testing.T) {
	fp := newFakePTY()
	srv, wsURL := startStreamServer(t, fp)
	defer srv.Close()

	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	stop := make(chan struct{})
	writerDone := make(chan struct{})

	// Client READER (single goroutine): drains server output. One concurrent
	// reader + one concurrent writer is gorilla-safe, so this never races the
	// client writer below — only the server's two writers can race.
	go func() {
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	}()

	// Client WRITER (single goroutine): spam heartbeats so the server's
	// goroutine 2 keeps echoing them (a server-side write) concurrently with
	// goroutine 1's PTY-output writes.
	go func() {
		defer close(writerDone)
		hb := wsenv.EncodeEnvelope(wsenv.Envelope{
			Type:      wsenv.MsgHeartbeat,
			PaneID:    "pane",
			Timestamp: uint64(time.Now().UnixNano()),
			Payload:   []byte("ping"),
		})
		for {
			select {
			case <-stop:
				return
			default:
				if werr := conn.WriteMessage(gws.BinaryMessage, hb); werr != nil {
					return
				}
			}
		}
	}()

	// Test goroutine drives the server's PTY-output writer (no conn access here).
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		fp.Feed([]byte("pty-output-chunk"))
	}

	close(stop)
	<-writerDone
	// If the two server writers collided, -race fails this test automatically
	// ("race detected during execution of test"); with the writeMsg mutex it is
	// clean. (This is a -race-dependent concurrency reproducer, like the sibling
	// cmd/htmux test.)
}
