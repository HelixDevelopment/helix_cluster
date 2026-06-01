//go:build integration

package main

// HXC-1106 integration test: real tmux pane + per-run UUID echo.
//
// This test is guarded by //go:build integration and is NOT run by the
// hermetic unit command.  The orchestrator runs it with -tags=integration.
//
// Closure criteria:
//   - Over a loopback WebSocket connected to a real StreamSession handler
//     backed by a real shell PTY, write "echo <UUID>\n" as an Input envelope
//     and assert that an Output envelope carrying "<UUID>" echoes back within
//     the round-trip window.
//   - t.Skip is used ONLY when the real shell is genuinely absent.
//   - A per-run UUID is embedded and asserted on the sink side.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

func TestStreamSession_RealPTY_EchoUUID(t *testing.T) {
	// Skip only if /bin/sh is genuinely absent.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found; skipping real PTY integration test")
	}

	// Per-run UUID — asserted on the sink side.
	runUUID := "hxc1106-integ-" + uuid.New().String()

	// Start the StreamSession handler using the real PTY provider.
	h := NewStreamSessionHandler()
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Allow the shell to start.
	time.Sleep(200 * time.Millisecond)

	// Send "echo <UUID>\n" as an Input envelope.
	cmd := fmt.Sprintf("echo %s\n", runUUID)
	inputEnv := wsenv.Envelope{
		Type:      wsenv.MsgInput,
		PaneID:    "integ-pane-0",
		Timestamp: uint64(time.Now().UnixNano()),
		Payload:   []byte(cmd),
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(inputEnv)); err != nil {
		t.Fatalf("send input: %v", err)
	}

	// Drain Output envelopes until we find one containing the UUID, or time out.
	deadline := time.Now().Add(5 * time.Second)
	var lastPayload []byte
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			// Timeout on read — keep trying until the overall deadline.
			continue
		}
		env, err := wsenv.DecodeEnvelope(raw)
		if err != nil {
			t.Logf("decode error (ignoring): %v", err)
			continue
		}
		if env.Type != wsenv.MsgOutput {
			continue
		}
		lastPayload = append(lastPayload, env.Payload...)
		if bytes.Contains(lastPayload, []byte(runUUID)) {
			return // PASS: UUID found on sink side.
		}
	}

	t.Fatalf("UUID %q not found in any Output envelope within 5 s; last accumulated output: %q", runUUID, lastPayload)
}
