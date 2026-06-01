//go:build integration

package main

// HXC-1105 integration test: htmux streamSession against a real shell PTY
// served over a loopback WebSocket.
//
// This test is guarded by //go:build integration and is NOT run by the
// hermetic unit command. The orchestrator runs it with -tags=integration.
//
// Design: rather than importing cmd/helix-session (a separate main package),
// this test embeds a minimal real-PTY WS bridge inline.  The bridge mirrors
// exactly what cmd/helix-session/stream.go does, so the test covers the full
// client path without a cross-package import.
//
// Closure criteria:
//   - htmux streamSession dials the loopback WS, sends "echo <UUID>\n" as an
//     Input envelope, and receives the UUID in an Output envelope which is
//     rendered to the local stdout capture buffer within 5 s.
//   - t.Skip ONLY when sh is genuinely absent.
//   - Per-run UUID is embedded and asserted on the sink side.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

// integPTY wraps a creack/pty for the inline integration bridge.
type integPTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (p *integPTY) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *integPTY) Write(b []byte) (int, error) { return p.ptmx.Write(b) }
func (p *integPTY) Resize(rows, cols uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}
func (p *integPTY) Close() error {
	_ = p.cmd.Process.Kill()
	return p.ptmx.Close()
}

// startRealPTY starts /bin/sh in a real PTY.
func startRealPTY(t *testing.T) *integPTY {
	t.Helper()
	cmd := exec.Command("/bin/sh")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	return &integPTY{ptmx: ptmx, cmd: cmd}
}

// integBridge runs a bidirectional bridge between a gorilla WS connection and
// a PTY — the same logic as runBridge in cmd/helix-session/stream.go.
func integBridge(conn *gws.Conn, ptm *integPTY, paneID string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptm.Read(buf)
			if n > 0 {
				env := wsenv.Envelope{
					Type:      wsenv.MsgOutput,
					PaneID:    paneID,
					Timestamp: uint64(time.Now().UnixNano()),
					Payload:   append([]byte(nil), buf[:n]...),
				}
				if werr := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env)); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			env, err := wsenv.DecodeEnvelope(msg)
			if err != nil {
				continue
			}
			switch env.Type {
			case wsenv.MsgInput:
				_, _ = ptm.Write(env.Payload)
			case wsenv.MsgResize:
				if len(env.Payload) >= 4 {
					rows := binary.BigEndian.Uint16(env.Payload[0:2])
					cols := binary.BigEndian.Uint16(env.Payload[2:4])
					_ = ptm.Resize(rows, cols)
				}
			}
		}
	}()
	<-done
}

func TestHTMuxStreamSession_RealShell_EchoUUID(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found; skipping real shell integration test")
	}

	runUUID := "hxc1105-integ-" + uuid.New().String()

	// Start a real PTY WS bridge server.
	up := gws.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		ptm := startRealPTY(t)
		defer ptm.Close()

		integBridge(conn, ptm, "integ-pane")
	}))
	defer httpSrv.Close()

	// htmux client: wsDialFunc seam pointing at the local test server.
	wsDialSeam := func(sessionID, _ string) (*gws.Conn, error) {
		url := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/stream?session=" + sessionID
		conn, _, err := gws.DefaultDialer.Dial(url, nil)
		return conn, err
	}

	// Give the shell a moment to initialise.
	time.Sleep(300 * time.Millisecond)

	// Inject "echo <UUID>\n" via controlled stdin.
	cmd := fmt.Sprintf("echo %s\n", runUUID)
	stdin := newBytesReader([]byte(cmd))
	stdout := &syncBuffer{}

	se := streamEnv{
		wsDial:        wsDialSeam,
		streamStdin:   stdin,
		streamStdout:  stdout,
		streamStdinFd: -1,
	}

	done := make(chan error, 1)
	go func() { done <- streamSession("integ-sess", "ignored", se) }()

	// Assert UUID appears in stdout within 5 s — sink-side evidence.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stdout.Contains([]byte(runUUID)) {
			return // PASS
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}

	t.Fatalf("UUID %q not found in stdout within 5 s; accumulated output: %q",
		runUUID, bytes.TrimSpace(stdout.Bytes()))
}
