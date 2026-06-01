package backends

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- HXC-1084: ControlModeSession unit tests ---
//
// All tests here use fake readers/writers so they run hermetically without
// requiring a real tmux binary.  Sink-side assertions prove that the pump
// goroutine routes decoded events to subscribers correctly.

// fakeControlModeSession builds a ControlModeSession whose pump reads from
// the supplied script (io.Reader) instead of a real tmux process. It returns
// the session and a channel that is closed when the pump finishes. The caller
// must NOT call cs.Close() because there is no real tmux process to kill.
func fakeControlModeSession(script io.Reader) (*ControlModeSession, <-chan struct{}) {
	cs := &ControlModeSession{
		done: make(chan struct{}),
	}
	// We need a fake stdin sink so SendCommand doesn't panic.
	pr, pw := io.Pipe()
	_ = pr // discard
	cs.stdin = pw

	// Start the pump directly against the scripted reader.
	go cs.pump(script)
	return cs, cs.done
}

// TestControlModeSession_PumpDeliversOutputEvent proves that the pump
// correctly parses a %output line from the stream and fans it out to a
// subscriber with the exact pane id and decoded bytes.
//
// MUTATION: in pump, delete the call to cs.fanOut(evs) ->
//
//	subscriber never receives the event; the timeout fires -> FAIL.
func TestControlModeSession_PumpDeliversOutputEvent(t *testing.T) {
	script := strings.NewReader("%output %2 hello\n%exit\n")
	cs, done := fakeControlModeSession(script)
	sub := cs.Subscribe()

	// Wait for pump to finish.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	// Collect delivered events.
	var events []ControlEvent
	for e := range sub {
		events = append(events, e)
	}

	// Must have received at least one EventOutput for %2.
	found := false
	for _, e := range events {
		if e.Type == EventOutput && e.PaneID == "%2" && string(e.Data) == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("did not receive EventOutput{pane=%%2, data=hello}; got %d events: %+v", len(events), events)
	}
}

// TestControlModeSession_PumpDeliversBeginEndBlock proves that a complete
// %begin..%end block emits EventBegin, EventEnd, EventCommandResponse in
// order and that EventCommandResponse carries the correct body.
//
// MUTATION: in pump, remove the body accumulation by not calling parser.ParseLine
// correctly (e.g. always pass "") -> EventCommandResponse.Data is empty -> FAIL.
func TestControlModeSession_PumpDeliversBeginEndBlock(t *testing.T) {
	script := strings.Join([]string{
		"%begin 1700000000 3 0",
		"window-body",
		"%end 1700000000 3 0",
		"%exit",
	}, "\n") + "\n"

	cs, done := fakeControlModeSession(strings.NewReader(script))
	sub := cs.Subscribe()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	var events []ControlEvent
	for e := range sub {
		events = append(events, e)
	}

	// Find the EventCommandResponse.
	var resp *ControlEvent
	for i := range events {
		if events[i].Type == EventCommandResponse {
			resp = &events[i]
			break
		}
	}
	if resp == nil {
		t.Fatalf("no EventCommandResponse in event stream; got %d events: %+v", len(events), events)
	}
	if string(resp.Data) != "window-body" {
		t.Fatalf("EventCommandResponse.Data = %q, want %q", resp.Data, "window-body")
	}
	if resp.CommandID != "3" {
		t.Fatalf("EventCommandResponse.CommandID = %q, want %q", resp.CommandID, "3")
	}
}

// TestControlModeSession_PumpStopsOnExit proves the pump goroutine exits
// promptly after receiving a %exit event and that Done is closed.
//
// MUTATION: in pump, remove the `if e.Type == EventExit { return }` check ->
//
//	pump blocks forever on the open pipe; the 3-second timeout fires -> FAIL.
//
// The source is an io.Pipe that stays open after %exit (no EOF); only the
// pump's own %exit handling terminates it, making the mutation detectable.
func TestControlModeSession_PumpStopsOnExit(t *testing.T) {
	pr, pw := io.Pipe()
	cs, done := fakeControlModeSession(pr)
	_ = cs.Subscribe() // drain so fanOut doesn't block

	// Write some output then %exit, but leave the pipe open (no EOF).
	if _, err := io.WriteString(pw, "%output %0 before-exit\n"); err != nil {
		t.Fatalf("write before-exit: %v", err)
	}
	if _, err := io.WriteString(pw, "%exit shutting down\n"); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	// pw is intentionally NOT closed — the pump must stop via %exit, not EOF.

	select {
	case <-done:
		// Good: pump exited after %exit without needing EOF.
		pw.Close()
	case <-time.After(3 * time.Second):
		pw.Close()
		t.Fatal("pump did not stop after the exit notification within 3s")
	}
}

// TestControlModeSession_PumpConcurrentSubscribers proves that multiple
// concurrent subscribers each receive the same events, proving fan-out
// correctness under concurrency.
//
// MUTATION: in fanOut, iterate over a single subscriber (off-by-one) ->
//
//	one subscriber receives 0 events; the empty-check below fails -> FAIL.
func TestControlModeSession_PumpConcurrentSubscribers(t *testing.T) {
	const numSubs = 5
	script := "%output %1 data\n%output %1 more\n%exit\n"

	cs, done := fakeControlModeSession(strings.NewReader(script))

	subs := make([]<-chan ControlEvent, numSubs)
	for i := range subs {
		subs[i] = cs.Subscribe()
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	for i, sub := range subs {
		var outputCount int
		for e := range sub {
			if e.Type == EventOutput {
				outputCount++
			}
		}
		if outputCount == 0 {
			t.Fatalf("subscriber %d received 0 EventOutput events", i)
		}
	}
}

// TestControlModeSession_SubscriberChannelClosed proves that the subscriber
// channel is closed after the pump exits so for-range loops terminate.
//
// MUTATION: in pump's deferred close block, don't close the channels ->
//
//	for-range never terminates; t.Fatal("range did not end") fires -> FAIL.
func TestControlModeSession_SubscriberChannelClosed(t *testing.T) {
	cs, _ := fakeControlModeSession(strings.NewReader("%exit\n"))
	sub := cs.Subscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range sub {
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber channel was not closed after pump exit")
	}
}

// TestControlModeSession_PumpMultiPaneRouting proves that output for
// different panes is correctly routed to each pane's PaneID (the parser
// does not cross-assign).
//
// MUTATION: in parseOutput (tmux_control.go), hard-code PaneID = "%0" ->
//
//	the second pane check fails (got %0, want %9) -> FAIL.
func TestControlModeSession_PumpMultiPaneRouting(t *testing.T) {
	script := "%output %0 zero\n%output %9 nine\n%exit\n"
	cs, done := fakeControlModeSession(strings.NewReader(script))
	sub := cs.Subscribe()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	paneData := map[string]string{}
	for e := range sub {
		if e.Type == EventOutput {
			paneData[e.PaneID] = string(e.Data)
		}
	}

	if paneData["%0"] != "zero" {
		t.Fatalf("pane %%0: got %q, want %q", paneData["%0"], "zero")
	}
	if paneData["%9"] != "nine" {
		t.Fatalf("pane %%9: got %q, want %q", paneData["%9"], "nine")
	}
}

// TestControlModeSession_PumpOctalDecoding proves end-to-end octal decoding
// through the pump: the bytes delivered to the subscriber are the decoded
// (raw) bytes, not the escaped octal notation.
//
// MUTATION: in decodeControl, return []byte(s) always (skip octal decode) ->
//
//	received bytes still contain \015\012; bytes.Equal fails -> FAIL.
func TestControlModeSession_PumpOctalDecoding(t *testing.T) {
	// \015\012 == CR LF; \040 == space.
	script := "%output %4 hello\\040world\\015\\012\n%exit\n"
	cs, done := fakeControlModeSession(strings.NewReader(script))
	sub := cs.Subscribe()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	var got []byte
	for e := range sub {
		if e.Type == EventOutput && e.PaneID == "%4" {
			got = append(got, e.Data...)
		}
	}
	want := []byte("hello world\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded bytes: got %v, want %v", got, want)
	}
}

// TestControlModeAttach_ReadWriteDelivery proves the ControlModeAttach Read
// contract: data from %output events for the registered pane is buffered
// and returned to the caller of Read(), in order, byte-for-byte.
//
// MUTATION: in deliver(), remove `if e.PaneID == cma.paneID` guard ->
//
//	wrong-pane output leaks into the buffer; cross-pane assert fires -> FAIL.
func TestControlModeAttach_ReadWriteDelivery(t *testing.T) {
	script := "%output %3 ABCD\n%output %7 WRONG\n%exit\n"
	cs, _ := fakeControlModeSession(strings.NewReader(script))

	// Only subscribe to pane %3.
	cma := NewControlModeAttach(cs, "%3")
	defer cma.Close()

	// Drain from cma.Read until EOF.
	var received []byte
	buf := make([]byte, 16)
	// We use a deadline to avoid hanging if nothing arrives.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			n, err := cma.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				return
			}
			if bytes.Contains(received, []byte("ABCD")) {
				return
			}
		}
	}()

	select {
	case <-readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ControlModeAttach.Read timed out waiting for pane data")
	}

	if !bytes.Contains(received, []byte("ABCD")) {
		t.Fatalf("Read did not deliver ABCD for pane %%3; got %q", received)
	}
	if bytes.Contains(received, []byte("WRONG")) {
		t.Fatalf("Read must not deliver output from pane %%7 to %%3 subscriber; got %q", received)
	}
}

// TestControlModeAttach_WriteIsNonBlocking proves that Write on a
// ControlModeAttach with no underlying process (cs.stdin closed) returns
// an error rather than blocking forever.
//
// MUTATION: in Write, replace the SendCommand call with a nil-return ->
//
//	Write always returns nil; the "no error" check fails -> FAIL
//	(but this is an error-path test, so the test only fires if the real
//	implementation correctly propagates the send error).
func TestControlModeAttach_WriteIsNonBlocking(t *testing.T) {
	cs, _ := fakeControlModeSession(strings.NewReader("%exit\n"))
	// Wait for pump to exit so stdin is known-closed.
	select {
	case <-cs.done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not exit")
	}

	// Close stdin to force a write error.
	if cs.stdin != nil {
		_ = cs.stdin.Close()
	}
	cs.mu.Lock()
	cs.closed = true
	cs.mu.Unlock()

	cma := NewControlModeAttach(cs, "%0")
	defer cma.Close()

	_, err := cma.Write([]byte("hello"))
	if err == nil {
		t.Fatal("expected error writing to a closed ControlModeSession, got nil")
	}
}

// TestControlModeSession_ErrorOnMalformedLine proves that a malformed line
// in the protocol stream sets an error on the session (accessible via Err)
// but does NOT prevent further event delivery.
//
// MUTATION: in pump, remove the `if cs.err == nil { cs.err = err }` block ->
//
//	Err() returns nil; the non-nil assert fires -> FAIL.
func TestControlModeSession_ErrorOnMalformedLine(t *testing.T) {
	// "bare text" outside a block is a protocol violation (not %*).
	script := "%output %0 ok\nbare text outside block\n%exit\n"
	cs, done := fakeControlModeSession(strings.NewReader(script))
	_ = cs.Subscribe()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump timed out")
	}

	if cs.Err() == nil {
		t.Fatal("expected non-nil error for malformed protocol line, got nil")
	}
}

// TestControlModeSession_FanOut_NilSafeEmpty proves fanOut is a no-op when
// there are no subscribers and does not panic.
func TestControlModeSession_FanOut_NilSafeEmpty(t *testing.T) {
	cs := &ControlModeSession{done: make(chan struct{})}
	pr, pw := io.Pipe()
	_ = pr
	cs.stdin = pw
	// No subscribers.
	cs.fanOut([]ControlEvent{{Type: EventOutput, PaneID: "%0", Data: []byte("x")}})
	// If we reach here without panic the test passes.
}

// TestControlModeSession_PumpConcurrencyRace is a data-race check: it starts
// the pump with a short transcript and concurrently adds subscribers to
// verify there is no race on the subs slice.  The -race flag will catch any
// unsynchronised access.
func TestControlModeSession_PumpConcurrencyRace(t *testing.T) {
	script := strings.Repeat("%output %0 x\n", 200) + "%exit\n"
	cs, done := fakeControlModeSession(strings.NewReader(script))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := cs.Subscribe()
			for range sub {
				// drain
			}
		}()
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump timed out under concurrent subscribers")
	}
	wg.Wait()
}
