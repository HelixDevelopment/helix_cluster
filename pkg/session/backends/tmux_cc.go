package backends

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// ControlModeSession manages a tmux control-mode (-CC) session: it spawns the
// tmux process, runs the ControlParser on its stdout in a background goroutine,
// and fans out structured ControlEvent values to registered subscribers. PTY
// bytes from %output events are also available through a per-pane channel.
//
// Lifecycle:
//
//	cs, err := NewControlModeSession(ctx, "my-session")
//	defer cs.Close()
//	events := cs.Subscribe()
//	cs.SendCommand("list-windows")
//
// ControlModeSession is safe for concurrent use by multiple goroutines.
type ControlModeSession struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	ptmx   *os.File
	socket string
	closed bool

	// subs is the fan-out set of subscriber channels.
	subs   []chan ControlEvent
	subsMu sync.Mutex
	// pending buffers events fanned out BEFORE the first subscriber registers,
	// so the initial control-mode burst (handshake / %layout / early %output)
	// is not lost in the window between session start and the first Subscribe.
	// It is flushed into — and cleared by — the first Subscribe, after which
	// fanOut delivers live (and full subscribers drop, as documented).
	pending       []ControlEvent
	hadSubscriber bool
	// terminated is set true (under subsMu) by the pump's deferred close, in the
	// SAME critical section that closes existing subscriber channels. Subscribe
	// reads it under subsMu to decide, atomically, whether the pump will close a
	// freshly-registered channel (terminated=false) or whether Subscribe must
	// close it itself (terminated=true) — eliminating a lost-close hang.
	terminated bool

	// done is closed when the reader loop exits.
	done chan struct{}
	// err holds the terminal error from the reader loop (nil on clean %exit).
	err error
}

// NewControlModeSession starts a new tmux control-mode session for the
// named session. tmux is invoked as:
//
//	tmux -CC new-session -d -s <name>   (if the session does not exist)
//	tmux -CC attach-session -t <name>
//
// The returned session is already running its event-pump goroutine. Call
// Close to terminate the tmux process and release resources.
//
// The ctx is used only to provide a cancellation hook for the underlying
// exec.Cmd; event delivery continues until Close is called.
func NewControlModeSession(ctx context.Context, sessionName string) (*ControlModeSession, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH: %w", err)
	}

	// Control mode (-CC) communicates over stdin/stdout pipes and must stay
	// ATTACHED. Do NOT pass -d (detached): with -CC, -d makes tmux create the
	// session and exit immediately, closing the control pipe (the first
	// SendCommand then fails with "broken pipe"). Use a private server socket
	// (-L) so this session is isolated from any user tmux server, and unset
	// $TMUX so tmux does not refuse to nest when run inside another tmux.
	socket := "helix-cc-" + sessionName
	cmd := exec.CommandContext(ctx, "tmux", "-L", socket, "-CC", "new-session", "-A", "-s", sessionName)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env

	// tmux control mode (-CC) calls tcgetattr on its terminal and exits with
	// "tcgetattr failed: Inappropriate ioctl for device" if attached to plain
	// pipes. It REQUIRES a PTY. Allocate one with creack/pty: the returned
	// master file is both the command stream (write) and the control-protocol
	// output (read).
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("tmux -CC start (pty): %w", err)
	}

	cs := &ControlModeSession{
		cmd:    cmd,
		stdin:  ptmx,
		ptmx:   ptmx,
		socket: socket,
		done:   make(chan struct{}),
	}

	go cs.pump(ptmx)
	return cs, nil
}

// Subscribe returns a channel on which the caller receives ControlEvent values
// emitted by the parser. The channel is buffered (capacity 64) to tolerate
// burst; slow consumers may drop events once the buffer fills — check for
// channel overflow by selecting with a default. The channel is closed when
// the control-mode session terminates (Clean %exit or process death).
func (cs *ControlModeSession) Subscribe() <-chan ControlEvent {
	ch := make(chan ControlEvent, 64)
	cs.subsMu.Lock()
	cs.subs = append(cs.subs, ch)
	// Flush any events buffered before the first subscriber existed, then mark
	// the session as having had a subscriber so fanOut switches to live (drop)
	// delivery. Late subscribers do NOT get history (pending is already empty).
	if !cs.hadSubscriber {
		cs.hadSubscriber = true
		for _, e := range cs.pending {
			select {
			case ch <- e:
			default:
				// First subscriber's buffer is full; drop the overflow, matching
				// the documented full-subscriber drop semantics.
			}
		}
		cs.pending = nil
	}
	// If the pump already terminated, it will never close this newly-registered
	// channel. Close it here (atomically w.r.t. the pump via terminated+subsMu)
	// so a range loop over a post-termination Subscribe still sees EOF after
	// draining any flushed events.
	if cs.terminated {
		close(ch)
	}
	cs.subsMu.Unlock()
	return ch
}

// SendCommand writes a tmux command string followed by a newline to the
// control-mode stdin. Commands must be valid tmux control-mode commands
// (e.g. "list-windows", "send-keys -t %0 hello").
func (cs *ControlModeSession) SendCommand(cmd string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return fmt.Errorf("control-mode session closed")
	}
	_, err := fmt.Fprintf(cs.stdin, "%s\n", cmd)
	return err
}

// Close terminates the control-mode session: it sends "q" on stdin, waits
// for the reader loop to finish, then kills the tmux process.
func (cs *ControlModeSession) Close() error {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil
	}
	cs.closed = true
	cs.mu.Unlock()

	// Graceful: send kill-server on the control channel, then close the PTY
	// master (EOF/SIGHUP to tmux).
	_, _ = fmt.Fprintf(cs.stdin, "kill-server\n")
	_ = cs.ptmx.Close()

	// Wait for the pump goroutine to finish.
	<-cs.done

	// Reap the process.
	_ = cs.cmd.Wait()

	// Best-effort teardown of the private tmux server so test/agent sessions
	// do not accumulate detached servers on the -L socket.
	if cs.socket != "" {
		_ = exec.Command("tmux", "-L", cs.socket, "kill-server").Run()
	}
	return nil
}

// Done returns a channel that is closed when the session terminates.
func (cs *ControlModeSession) Done() <-chan struct{} {
	return cs.done
}

// Err returns the terminal error from the reader loop, available once Done
// is closed. Nil means clean termination (or %exit without error).
func (cs *ControlModeSession) Err() error {
	<-cs.done
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.err
}

// pump reads the control-mode stream line-by-line, feeds each line to a
// ControlParser and fans out the resulting events to all subscribers.
// It runs in a dedicated goroutine and closes cs.done when it exits.
func (cs *ControlModeSession) pump(r io.Reader) {
	defer func() {
		// Close all subscriber channels so receivers see EOF. Set terminated
		// under the SAME lock so a concurrent Subscribe either registers before
		// this (and gets closed here) or observes terminated and self-closes.
		cs.subsMu.Lock()
		cs.terminated = true
		for _, ch := range cs.subs {
			close(ch)
		}
		cs.subsMu.Unlock()
		close(cs.done)
	}()

	parser := NewControlParser()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Text()
		evs, err := parser.ParseLine(line)
		if err != nil {
			// Preserve error but continue pumping: protocol violations
			// should not crash the pump; we surface the error via Err().
			cs.mu.Lock()
			if cs.err == nil {
				cs.err = err
			}
			cs.mu.Unlock()
		}
		cs.fanOut(evs)

		// If the server sent %exit, stop reading.
		for _, e := range evs {
			if e.Type == EventExit {
				return
			}
		}
	}

	if err := sc.Err(); err != nil && !isClosedPipe(err) {
		cs.mu.Lock()
		if cs.err == nil {
			cs.err = fmt.Errorf("control-mode read: %w", err)
		}
		cs.mu.Unlock()
	}
}

// fanOut delivers a batch of events to all current subscribers.
// A full subscriber channel is skipped (non-blocking send) rather than
// blocking the pump.
func (cs *ControlModeSession) fanOut(evs []ControlEvent) {
	if len(evs) == 0 {
		return
	}
	cs.subsMu.Lock()
	// Before any subscriber has registered, buffer events instead of dropping
	// them so the initial burst survives the start->Subscribe window. The first
	// Subscribe flushes and clears this buffer.
	if !cs.hadSubscriber {
		cs.pending = append(cs.pending, evs...)
		cs.subsMu.Unlock()
		return
	}
	subs := cs.subs
	cs.subsMu.Unlock()

	for _, e := range evs {
		for _, ch := range subs {
			select {
			case ch <- e:
			default:
				// Subscriber is full; drop the event rather than stall the pump.
			}
		}
	}
}

// isClosedPipe reports whether an error came from writing to a closed pipe.
// We use string containment as a portable approximation — the exact error
// type differs between OSes.
func isClosedPipe(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "closed") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "file already closed") ||
		strings.Contains(s, "use of closed")
}

// ControlModeAttach is a ControlModeSession-backed ReadWriteCloser that
// satisfies the Backend.Attach contract. Writes are forwarded as send-keys
// commands; Reads surface %output bytes from the named pane (paneID, e.g.
// "%0") in order.
//
// This is the REAL implementation that replaces the stub in tmux.go.
type ControlModeAttach struct {
	cs     *ControlModeSession
	paneID string

	// outputBuf buffers decoded %output bytes for the target pane.
	outputBuf []byte
	bufMu     sync.Mutex
	bufCond   *sync.Cond
	// deliverDone is set true (under bufMu) when the deliver goroutine's range
	// over the subscription channel EXITS — i.e. the channel is closed AND every
	// buffered event has been drained into outputBuf. Read must only return EOF
	// once this is true (or closed), never merely when cs.Done() fires: the pump
	// closes cs.done right after %exit, but events it already fanned out may still
	// be sitting in the channel undrained — EOFing on cs.Done() loses them.
	deliverDone bool
	closed      bool

	sub <-chan ControlEvent

	cancel context.CancelFunc
	once   sync.Once
}

// NewControlModeAttach creates a ControlModeAttach for paneID (e.g. "%0")
// backed by cs. It starts a delivery goroutine that reads events from cs
// and appends %output bytes for the target pane to the internal buffer.
func NewControlModeAttach(cs *ControlModeSession, paneID string) *ControlModeAttach {
	cma := &ControlModeAttach{
		cs:     cs,
		paneID: paneID,
		sub:    cs.Subscribe(),
	}
	cma.bufCond = sync.NewCond(&cma.bufMu)
	go cma.deliver()
	return cma
}

// deliver reads events from the subscription channel and appends %output
// bytes for our pane to outputBuf.
func (cma *ControlModeAttach) deliver() {
	for e := range cma.sub {
		if e.Type == EventOutput && e.PaneID == cma.paneID && len(e.Data) > 0 {
			cma.bufMu.Lock()
			cma.outputBuf = append(cma.outputBuf, e.Data...)
			cma.bufCond.Signal()
			cma.bufMu.Unlock()
		}
	}
	// Channel closed AND fully drained: mark done and wake any blocked Read so
	// it returns EOF only now that no further pane bytes can arrive.
	cma.bufMu.Lock()
	cma.deliverDone = true
	cma.bufCond.Broadcast()
	cma.bufMu.Unlock()
}

// Read blocks until at least one byte is available from the pane output
// buffer, then copies up to len(p) bytes into p.
func (cma *ControlModeAttach) Read(p []byte) (int, error) {
	cma.bufMu.Lock()
	defer cma.bufMu.Unlock()
	for len(cma.outputBuf) == 0 {
		// EOF only once the deliver goroutine has fully drained the (closed)
		// subscription into outputBuf, or this attach was explicitly closed.
		// Do NOT EOF merely on cs.Done(): undrained pane bytes may still be in
		// flight in the subscription channel.
		if cma.deliverDone || cma.closed {
			return 0, io.EOF
		}
		cma.bufCond.Wait()
	}
	n := copy(p, cma.outputBuf)
	cma.outputBuf = cma.outputBuf[n:]
	return n, nil
}

// Write sends the bytes as keystrokes to the pane via tmux send-keys.
func (cma *ControlModeAttach) Write(p []byte) (int, error) {
	if err := cma.cs.SendCommand(fmt.Sprintf("send-keys -t %s %q", cma.paneID, string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close releases the ControlModeAttach without closing the underlying session.
func (cma *ControlModeAttach) Close() error {
	cma.once.Do(func() {
		// Mark closed and wake any blocked Read so it returns EOF promptly.
		cma.bufMu.Lock()
		cma.closed = true
		cma.bufCond.Broadcast()
		cma.bufMu.Unlock()
	})
	return nil
}
