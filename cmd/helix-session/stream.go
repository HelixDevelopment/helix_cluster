// stream.go — HXC-1106: StreamSession WebSocket handler.
//
// Bridges a PTY (via the ptyProvider seam) to a WebSocket connection using
// pkg/websocket envelopes:
//
//   - Input envelope  → PTY stdin
//   - PTY output      → Output envelope → WS client
//   - Resize envelope → pty.Setsize
//
// The handler is registered under /stream?session=<id> and is exercised
// directly by the hermetic unit tests via httptest.Server + gorilla dialer.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	gws "github.com/gorilla/websocket"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

// ptyHandle is the interface the handler uses to interact with the PTY.
// Production code uses a real creack/pty; tests inject a fakePTY.
type ptyHandle interface {
	// Read reads PTY output bytes.
	io.Reader
	// Write sends input bytes to the PTY.
	io.Writer
	// Resize adjusts PTY dimensions.
	Resize(rows, cols uint16) error
	// Close releases the PTY resources.
	Close() error
}

// ptyProvider is a factory seam: called once per StreamSession request.
// The sessionID is the ?session= query parameter.
type ptyProvider func(sessionID string) (ptyHandle, error)

// realPTYProvider starts a real shell process under a PTY.
func realPTYProvider(sessionID string) (ptyHandle, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell) //gosec:disable G702 -- launches the operator's own $SHELL interactively (intended terminal behavior); no request-controlled command/args, sessionID only flows into an env var
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HELIX_SESSION="+sessionID)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	return &realPTY{ptmx: ptmx, cmd: cmd}, nil
}

// realPTY wraps a creack/pty file with the ptyHandle interface.
type realPTY struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (p *realPTY) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *realPTY) Write(b []byte) (int, error) { return p.ptmx.Write(b) }
func (p *realPTY) Resize(rows, cols uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}
func (p *realPTY) Close() error {
	_ = p.cmd.Process.Kill()
	_ = p.ptmx.Close()
	return nil
}

// wsUpgrader is the package-level gorilla upgrader used by StreamSession.
var wsUpgrader = gws.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// StreamSessionHandler is the http.Handler for /stream.
// It accepts a WebSocket upgrade, spins up a PTY (via provider), and bridges
// the two bidirectionally using Envelope messages.
type StreamSessionHandler struct {
	provider ptyProvider
}

// NewStreamSessionHandler returns a handler using the real PTY provider.
func NewStreamSessionHandler() *StreamSessionHandler {
	return &StreamSessionHandler{provider: realPTYProvider}
}

// newStreamSessionHandlerWithProvider returns a handler using the given provider seam.
func newStreamSessionHandlerWithProvider(p ptyProvider) *StreamSessionHandler {
	return &StreamSessionHandler{provider: p}
}

// ServeHTTP upgrades the connection, acquires the PTY, and runs the bridge.
func (h *StreamSessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		sessionID = "default"
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("stream: upgrade error: %v", err)
		return
	}
	defer conn.Close()

	ptm, err := h.provider(sessionID)
	if err != nil {
		log.Printf("stream: pty provider error: %v", err)
		sendErrorEnvelope(conn, sessionID, fmt.Sprintf("pty start failed: %v", err))
		return
	}
	defer ptm.Close()

	runBridge(conn, ptm, sessionID)
}

// runBridge runs the bidirectional copy loop between conn and ptm.
// It spawns two goroutines:
//  1. PTY → WS  (Output envelopes)
//  2. WS  → PTY (Input / Resize envelopes)
//
// The function returns when either goroutine exits or the WS connection closes.
func runBridge(conn *gws.Conn, ptm ptyHandle, paneID string) {
	done := make(chan struct{})

	// goroutine 1: PTY output → WS Output envelopes
	// MUTATION GUARD: removing this goroutine makes TestStreamSessionEchoHi hang
	// because no Output envelope is ever sent to the client.
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
				encoded := wsenv.EncodeEnvelope(env)
				if werr := conn.WriteMessage(gws.BinaryMessage, encoded); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// goroutine 2: WS messages → PTY input / resize
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			env, err := wsenv.DecodeEnvelope(msg)
			if err != nil {
				log.Printf("stream: decode envelope: %v", err)
				continue
			}
			switch env.Type {
			case wsenv.MsgInput:
				if len(env.Payload) > 0 {
					if _, werr := ptm.Write(env.Payload); werr != nil {
						return
					}
				}
			case wsenv.MsgResize:
				// Payload: 4 bytes: uint16 rows (big-endian) + uint16 cols (big-endian)
				if len(env.Payload) >= 4 {
					rows := binary.BigEndian.Uint16(env.Payload[0:2])
					cols := binary.BigEndian.Uint16(env.Payload[2:4])
					_ = ptm.Resize(rows, cols)
				}
			case wsenv.MsgHeartbeat:
				// echo heartbeat back
				hb := wsenv.EncodeEnvelope(wsenv.Envelope{
					Type:      wsenv.MsgHeartbeat,
					PaneID:    paneID,
					Timestamp: uint64(time.Now().UnixNano()),
					Payload:   env.Payload,
				})
				_ = conn.WriteMessage(gws.BinaryMessage, hb)
			}
		}
	}()

	<-done
}

// sendErrorEnvelope sends an Error envelope to the client and closes the WS connection.
func sendErrorEnvelope(conn *gws.Conn, paneID, msg string) {
	env := wsenv.Envelope{
		Type:      wsenv.MsgError,
		PaneID:    paneID,
		Timestamp: uint64(time.Now().UnixNano()),
		Payload:   []byte(msg),
	}
	_ = conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env))
	_ = conn.Close()
}

// ResizePayload encodes rows and cols into the 4-byte resize envelope payload
// (big-endian uint16 pairs).
func ResizePayload(rows, cols uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], rows)
	binary.BigEndian.PutUint16(b[2:4], cols)
	return b
}
