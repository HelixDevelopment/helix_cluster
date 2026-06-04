// wsstream.go — HXC-1105: raw-mode terminal client streaming session I/O
// over WebSocket envelopes.
//
// This file adds the WebSocket streaming layer to htmux:
//
//   - Dial the helix-session /stream endpoint via a wsDialFunc seam.
//   - Decode Output envelopes from the server and write the payload to the
//     local terminal's stdout.
//   - Read local keypresses and encode them as Input envelopes sent to the
//     server.
//   - Forward SIGWINCH as Resize envelopes.
//
// The wsDialFunc seam allows hermetic unit tests to inject a scripted WS
// server without real network I/O.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	gws "github.com/gorilla/websocket"
	"golang.org/x/term"

	wsenv "github.com/HelixDevelopment/helix_cluster/pkg/websocket"
)

// wsDialFunc is the seam for dialing the helix-session WebSocket endpoint.
// The production implementation uses gorilla/websocket; tests inject a fake.
type wsDialFunc func(sessionID, addr string) (*gws.Conn, error)

// defaultWSDial dials ws://<addr>/stream?session=<sessionID>.
func defaultWSDial(sessionID, addr string) (*gws.Conn, error) {
	wsAddr := addr
	if !strings.HasPrefix(wsAddr, "ws://") && !strings.HasPrefix(wsAddr, "wss://") {
		wsAddr = "ws://" + wsAddr
	}
	url := fmt.Sprintf("%s/stream?session=%s", wsAddr, sessionID)
	conn, _, err := gws.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, fmt.Errorf("wsstream dial %s: %w", url, err)
	}
	return conn, nil
}

// streamEnv carries the injectable dependencies for the WS streaming path.
// It is embedded in the top-level env for htmux commands that need streaming.
type streamEnv struct {
	// wsDial dials the session service WebSocket endpoint.
	// If nil, defaultWSDial is used.
	wsDial wsDialFunc
	// streamStdin is the reader for local keyboard input (defaults to os.Stdin).
	streamStdin io.Reader
	// streamStdout is the writer for remote PTY output (defaults to os.Stdout).
	streamStdout io.Writer
	// streamStdinFd is the file descriptor for raw-mode setup (defaults to stdin fd).
	// -1 means "not a terminal; skip raw mode".
	streamStdinFd int
}

// defaultStreamEnv returns a streamEnv wired to the real stdin/stdout.
func defaultStreamEnv() streamEnv {
	return streamEnv{
		wsDial:        defaultWSDial,
		streamStdin:   os.Stdin,
		streamStdout:  os.Stdout,
		streamStdinFd: int(os.Stdin.Fd()),
	}
}

// streamSession dials the helix-session WebSocket endpoint and runs the
// bidirectional I/O bridge:
//
//   - Server Output envelopes → local terminal stdout
//   - Local keystrokes        → Input envelopes → server
//   - SIGWINCH                → Resize envelopes → server
//
// The function blocks until the connection is closed or the context is done.
func streamSession(sessionID, wsAddr string, se streamEnv) error {
	dial := se.wsDial
	if dial == nil {
		dial = defaultWSDial
	}
	stdout := se.streamStdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stdin := se.streamStdin
	if stdin == nil {
		stdin = os.Stdin
	}

	conn, err := dial(sessionID, wsAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Enter raw mode if stdin is a real terminal.
	var oldState *term.State
	if se.streamStdinFd >= 0 && term.IsTerminal(se.streamStdinFd) {
		oldState, err = term.MakeRaw(se.streamStdinFd)
		if err != nil {
			return fmt.Errorf("raw mode: %w", err)
		}
		defer term.Restore(se.streamStdinFd, oldState)
	}

	ctx, cancel := newCancelCtx()
	defer cancel()

	var wg sync.WaitGroup

	// goroutine 1: server Output envelopes → local stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			conn.SetReadDeadline(time.Time{}) // no deadline on read
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			env, err := wsenv.DecodeEnvelope(raw)
			if err != nil {
				continue
			}
			if env.Type == wsenv.MsgOutput && len(env.Payload) > 0 {
				_, _ = stdout.Write(env.Payload)
			}
		}
	}()

	// goroutine 2: local keystrokes → Input envelopes
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, 256)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				env := wsenv.Envelope{
					Type:      wsenv.MsgInput,
					PaneID:    sessionID,
					Timestamp: uint64(time.Now().UnixNano()),
					Payload:   append([]byte(nil), buf[:n]...),
				}
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if werr := conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env)); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// goroutine 3: SIGWINCH → Resize envelopes
	wg.Add(1)
	go func() {
		defer wg.Done()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGWINCH)
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				cols, rows, err := getTermSize(se.streamStdinFd)
				if err != nil {
					continue
				}
				payload := make([]byte, 4)
				// Terminal dims fit in uint16; clamp so an out-of-range size can't wrap.
				binary.BigEndian.PutUint16(payload[0:2], clampTermDim(rows))
				binary.BigEndian.PutUint16(payload[2:4], clampTermDim(cols))
				env := wsenv.Envelope{
					Type:      wsenv.MsgResize,
					PaneID:    sessionID,
					Timestamp: uint64(time.Now().UnixNano()),
					Payload:   payload,
				}
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				_ = conn.WriteMessage(gws.BinaryMessage, wsenv.EncodeEnvelope(env))
			}
		}
	}()

	<-ctx.Done()
	_ = conn.Close()
	wg.Wait()
	return nil
}

// getTermSize returns (cols, rows) for the given fd, or (80, 24) if not a terminal.
// clampTermDim narrows a terminal dimension to uint16 without wrapping:
// negatives become 0, values above 0xffff are capped.
func clampTermDim(v int) uint16 {
	if v < 0 {
		return 0
	}
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v) //gosec:disable G115 -- bounds-checked to [0,0xffff] above
}

func getTermSize(fd int) (cols, rows int, err error) {
	if fd < 0 || !term.IsTerminal(fd) {
		return 80, 24, nil
	}
	cols, rows, err = term.GetSize(fd)
	return
}

// cancelCtx is a minimal cancellable context used to coordinate goroutines.
// It avoids importing context just for a done channel.
type cancelCtx struct {
	done chan struct{}
	once sync.Once
}

func newCancelCtx() (*cancelCtx, func()) {
	c := &cancelCtx{done: make(chan struct{})}
	return c, func() { c.once.Do(func() { close(c.done) }) }
}

func (c *cancelCtx) Done() <-chan struct{} { return c.done }
