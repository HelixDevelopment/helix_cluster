package chutes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cannedStream is an SSE body with three content frames, a keep-alive comment,
// a multi-line data field, and a [DONE] terminator. The expected ordered tokens
// are "Hel", "lo", " world".
const cannedStream = `: keep-alive

data: {"model":"m","choices":[{"index":0,"delta":{"content":"Hel"}}]}

data: {"model":"m","choices":[{"index":0,"delta":{"content":"lo"}}]}

: ping

data: {"model":"m","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}

data: [DONE]

`

// TestDecodeStreamExactOrderedTokens proves the SSE decoder yields the EXACT
// ordered token deltas and stops at [DONE].
//
// MUTATION: in scanSSE, replace the in-order append with prepend (e.g. build
// the events in reverse, or in DecodeStream do `events = append([]StreamEvent{ev},
// events...)`). The ordered ["Hel","lo"," world"] assertion fails because order
// is dropped.
func TestDecodeStreamExactOrderedTokens(t *testing.T) {
	t.Parallel()
	events, err := DecodeStream(strings.NewReader(cannedStream))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	got := make([]string, len(events))
	for i, e := range events {
		got[i] = e.Content
	}
	want := []string{"Hel", "lo", " world"}
	if len(got) != len(want) {
		t.Fatalf("got %d events %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token[%d] = %q, want %q (order: %v)", i, got[i], want[i], got)
		}
	}
	// Joined content is the full message.
	if joined := strings.Join(got, ""); joined != "Hello world" {
		t.Fatalf("joined = %q, want %q", joined, "Hello world")
	}
	// Last frame carries the finish reason.
	if events[len(events)-1].FinishReason != "stop" {
		t.Fatalf("finish reason = %q, want stop", events[len(events)-1].FinishReason)
	}
}

// TestDecodeStreamStopsAtDone proves frames AFTER [DONE] are not decoded.
//
// MUTATION: in scanSSE, change the [DONE] handler to `return false, nil`
// (don't stop). The "leaked-after-done" frame would be decoded and the
// len(events)==1 assertion fails.
func TestDecodeStreamStopsAtDone(t *testing.T) {
	t.Parallel()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"only\"}}]}\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"leaked-after-done\"}}]}\n\n"
	events, err := DecodeStream(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (decoder did not stop at [DONE])", len(events))
	}
	if events[0].Content != "only" {
		t.Fatalf("content = %q, want only", events[0].Content)
	}
}

// TestDecodeStreamMultiLineData proves multi-line "data:" fields are joined per
// the SSE spec with "\n" between segments before JSON decoding.
//
// Case A: a clean structural split that the "\n" join reassembles into valid
// JSON decoding to content "mid", model "m".
//
// Case B: an integer token folded across two data lines. The SSE "\n" join puts
// "1" and "7" on separate textual lines inside the JSON ("index":1\n7), which is
// INVALID JSON, so a spec-correct decoder errors. An "" -joining decoder would
// instead fuse them into "index":17 and NOT error.
//
// MUTATION: in scanSSE, join dataLines with "" instead of "\n". Case B's
// DecodeStream then succeeds (index becomes 17) and the "want decode error"
// assertion fails.
func TestDecodeStreamMultiLineData(t *testing.T) {
	t.Parallel()

	// Case A: a clean structural split that a "\n" join reassembles into valid
	// JSON decoding to content "mid" and model "m".
	bodyA := "data: {\"choices\":[{\"delta\":{\"content\":\"mid\"}}],\n" +
		"data: \"model\":\"m\"}\n\n" +
		"data: [DONE]\n\n"
	events, err := DecodeStream(strings.NewReader(bodyA))
	if err != nil {
		t.Fatalf("DecodeStream A: %v", err)
	}
	if len(events) != 1 || events[0].Content != "mid" || events[0].Model != "m" {
		t.Fatalf("A events = %+v, want one {content:mid, model:m}", events)
	}

	// Case B: an integer token folded across two data lines. The SSE "\n" join
	// places "1" and "7" on separate textual lines inside the JSON, producing
	// "index":1\n7 which is INVALID JSON. A correct "\n"-joining decoder reports
	// a decode error here; an "" -joining decoder would silently produce 17.
	bodyB := "data: {\"choices\":[{\"index\":1\n" +
		"data: 7,\"delta\":{\"content\":\"x\"}}]}\n\n" +
		"data: [DONE]\n\n"
	_, errB := DecodeStream(strings.NewReader(bodyB))
	if errB == nil {
		t.Fatalf("B: expected decode error from newline-folded numeric token, got nil")
	}
}

// TestDecodeStreamTruncatedNoDone proves a stream that ends without [DONE]
// surfaces ErrStreamClosed while still returning the decoded prefix.
//
// MUTATION: in scanSSE, return nil instead of ErrStreamClosed at EOF. The
// errors.Is(ErrStreamClosed) assertion fails.
func TestDecodeStreamTruncatedNoDone(t *testing.T) {
	t.Parallel()
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	events, err := DecodeStream(strings.NewReader(body))
	if !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("error = %v, want ErrStreamClosed", err)
	}
	if len(events) != 1 || events[0].Content != "partial" {
		t.Fatalf("events = %+v, want one 'partial' event", events)
	}
}

// TestStreamChannelOrdered proves the incremental channel API yields the same
// exact ordered tokens and a nil terminal error on clean [DONE].
//
// MUTATION: in StreamChannel, send events on a buffered slice flushed in reverse
// (drop order). The ordered assertion fails.
func TestStreamChannelOrdered(t *testing.T) {
	t.Parallel()
	events, errc := StreamChannel(context.Background(), strings.NewReader(cannedStream))
	var got []string
	for e := range events {
		got = append(got, e.Content)
	}
	if err := <-errc; err != nil {
		t.Fatalf("terminal error = %v, want nil", err)
	}
	want := []string{"Hel", "lo", " world"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
}

// TestStreamCompletionOverHTTP proves the end-to-end streaming path: a fake
// server streams SSE and the client decodes ordered tokens.
//
// MUTATION: in StreamCompletion, send Stream=false; the fake server below only
// streams when Stream==true, so it would return a non-SSE 200 and decoding
// yields zero tokens — the token assertion fails.
func TestStreamCompletionOverHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			http.Error(w, "expected stream=true", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(cannedStream))
	}))
	t.Cleanup(srv.Close)

	c, _ := NewClient(Config{Endpoints: []Endpoint{{Name: "s", BaseURL: srv.URL}}})
	events, err := c.StreamCompletion(context.Background(), Endpoint{Name: "s", BaseURL: srv.URL},
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Content)
	}
	if strings.Join(got, "") != "Hello world" {
		t.Fatalf("streamed content = %q, want %q", strings.Join(got, ""), "Hello world")
	}
}

// blockingReader is an io.ReadCloser whose Read blocks until unblockCh is
// closed. It tracks whether Close has been called (used by the closer tests).
type blockingReader struct {
	unblockCh chan struct{} // closed to unblock pending Read calls
	closedMu  sync.Mutex
	closed    bool
}

func newBlockingReader() *blockingReader {
	return &blockingReader{unblockCh: make(chan struct{})}
}

// Read blocks until unblockCh is closed, then returns io.EOF so the caller
// (e.g. bufio.Scanner) stops. This simulates a slow/stalled network body.
func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.unblockCh
	return 0, io.EOF
}

// Close records the call and unblocks any pending Read so goroutines exit
// cleanly, preventing test goroutine leaks under -race.
func (b *blockingReader) Close() error {
	b.closedMu.Lock()
	b.closed = true
	b.closedMu.Unlock()
	select {
	case <-b.unblockCh: // already closed
	default:
		close(b.unblockCh)
	}
	return nil
}

// isClosed reports whether Close was called.
func (b *blockingReader) isClosed() bool {
	b.closedMu.Lock()
	defer b.closedMu.Unlock()
	return b.closed
}

// TestStreamChannelCancelUnblocksPromptly proves that cancelling ctx causes
// StreamChannel to close its channels promptly even when the io.Reader is
// blocking — i.e., the goroutine does not leak.
//
// MUTATION GUARD: removing the ctx-cancellation pipe-close path in StreamChannel
// (the watcher goroutine that calls pipeW.CloseWithError(ctx.Err())) makes the
// goroutine block forever on the io.Copy, so the channels are never closed and
// this test times out — proving the branch is load-bearing.
func TestStreamChannelCancelUnblocksPromptly(t *testing.T) {
	t.Parallel()
	r := newBlockingReader()

	ctx, cancel := context.WithCancel(context.Background())

	events, errc := StreamChannel(ctx, r)

	// Cancel the context after a brief startup delay. The reader is blocked so
	// no events will arrive before the cancel.
	cancel()

	// Both channels MUST close within a generous but bounded time. Under -race
	// the scheduler may be slower, so we use 2 s. A goroutine leak would cause
	// this select to hit the timeout branch and fail the test.
	const deadline = 2 * time.Second
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	// Drain the events channel until it closes.
	eventsClosed := false
	errcClosed := false
	for !eventsClosed || !errcClosed {
		select {
		case _, ok := <-events:
			if !ok {
				eventsClosed = true
			}
		case _, ok := <-errc:
			if !ok {
				errcClosed = true
			}
		case <-timer.C:
			t.Fatal("StreamChannel channels did not close within 2 s after ctx cancel " +
				"(possible goroutine leak — mutation guard: is the ctx-watcher pipe-close present?)")
		}
	}
}

// TestStreamChannelClosesReader proves that StreamChannel calls Close on the
// io.Reader (when it is also an io.Closer) both on a clean [DONE] completion
// and on ctx cancellation.
//
// MUTATION GUARD: removing the `defer rc.Close()` call in StreamChannel causes
// isClosed() to return false and both sub-tests fail.
func TestStreamChannelClosesReader(t *testing.T) {
	t.Parallel()

	// makeRC builds an io.ReadCloser from a plain io.Reader and a close flag.
	makeRC := func(r io.Reader, closed *atomic.Bool) io.ReadCloser {
		return struct {
			io.Reader
			io.Closer
		}{
			Reader: r,
			Closer: closerFunc(func() error { closed.Store(true); return nil }),
		}
	}

	t.Run("on clean DONE", func(t *testing.T) {
		t.Parallel()
		var closedFlag atomic.Bool
		rc := makeRC(strings.NewReader(cannedStream), &closedFlag)

		events, errc := StreamChannel(context.Background(), rc)
		// Drain all events.
		for range events {
		}
		if err := <-errc; err != nil {
			t.Fatalf("terminal error = %v, want nil", err)
		}
		if !closedFlag.Load() {
			t.Fatal("StreamChannel did not call Close() on the ReadCloser after clean [DONE]")
		}
	})

	t.Run("on ctx cancel", func(t *testing.T) {
		t.Parallel()
		r := newBlockingReader()

		ctx, cancel := context.WithCancel(context.Background())
		events, errc := StreamChannel(ctx, r)

		cancel()

		// Drain channels.
		const deadline = 2 * time.Second
		timer := time.NewTimer(deadline)
		defer timer.Stop()
		eventsClosed := false
		errcClosed := false
		for !eventsClosed || !errcClosed {
			select {
			case _, ok := <-events:
				if !ok {
					eventsClosed = true
				}
			case _, ok := <-errc:
				if !ok {
					errcClosed = true
				}
			case <-timer.C:
				t.Fatal("channels did not close within 2 s")
			}
		}

		if !r.isClosed() {
			t.Fatal("StreamChannel did not call Close() on the ReadCloser after ctx cancel")
		}
	})
}

// trailingReadCloser wraps a base reader and, once the base returns io.EOF,
// blocks until Close() is called. It implements io.ReadCloser to simulate an
// http.Response.Body that keeps the connection open after the SSE [DONE]
// sentinel — the exact scenario that previously deadlocked StreamChannel when
// neither r.Close() nor pipeR.Close() was called on the [DONE] exit path.
type trailingReadCloser struct {
	base     io.Reader
	br       *blockingReader // unblocked by Close()
	usedBase bool
	mu       sync.Mutex
	closed   bool
}

func newTrailingReadCloser(base string) *trailingReadCloser {
	return &trailingReadCloser{
		base: strings.NewReader(base),
		br:   newBlockingReader(),
	}
}

func (t *trailingReadCloser) Read(p []byte) (int, error) {
	if !t.usedBase {
		n, err := t.base.Read(p)
		if err == io.EOF {
			t.usedBase = true
			if n > 0 {
				return n, nil
			}
		} else {
			return n, err
		}
	}
	// Block until Close() is called, simulating a kept-alive HTTP body.
	return t.br.Read(p)
}

func (t *trailingReadCloser) Close() error {
	return t.br.Close()
}

func (t *trailingReadCloser) isClosed() bool {
	return t.br.isClosed()
}

// TestStreamChannelDoneWithTrailingData is a regression test for the
// deadlock/goroutine-leak that occurred when the underlying reader had trailing
// data (or an open connection) after emitting [DONE].
//
// Scenario: an http.Response.Body (which is always an io.ReadCloser) emits the
// [DONE] sentinel and then blocks in its next Read() because the server has not
// yet closed the TCP connection. Before the fix, scanSSE returned stop=true on
// [DONE], but the copy goroutine was stuck in r.Read() waiting for more data,
// and the watcher goroutine never called closeReader() for the clean-[DONE]
// path, so <-copyDone blocked forever.
//
// The fix adds a scanDone channel: after scanSSE returns, the watcher is
// signalled and calls closeReader() + pipeW.CloseWithError, which unblocks the
// copy goroutine from its pending r.Read().
//
// This test uses context.Background() (no ctx timeout) so the ONLY escape is
// the watcher's scanDone path — not a ctx timeout acting as a silent escape
// valve. The test-level timer detects the deadlock as a hard failure.
//
// MUTATION GUARD: removing the `close(scanDone)` call (or the corresponding
// watcher case) from StreamChannel causes this test to time out — proving the
// scanDone signal is load-bearing.
func TestStreamChannelDoneWithTrailingData(t *testing.T) {
	t.Parallel()

	// trailingReadCloser emits the SSE body (including [DONE]) then blocks in
	// Read until Close() is called — simulating an http.Response.Body that keeps
	// the TCP connection open. Because it implements io.Closer, StreamChannel
	// will call Close() on it, which unblocks the Read and lets the copy
	// goroutine exit.
	r := newTrailingReadCloser(
		"data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\ndata: [DONE]\n\n",
	)

	// Use context.Background() — no ctx timeout. Only the scanDone watcher path
	// can terminate this; if it is absent, the test-level timer fires.
	events, errc := StreamChannel(context.Background(), r)

	const deadline = 3 * time.Second
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	var got []string
	var termErr error
	eventsDone := false
	errcDone := false

	for !eventsDone || !errcDone {
		select {
		case e, ok := <-events:
			if !ok {
				eventsDone = true
			} else {
				got = append(got, e.Content)
			}
		case e, ok := <-errc:
			if !ok {
				errcDone = true
			} else {
				termErr = e
			}
		case <-timer.C:
			t.Fatal("StreamChannel channels did not close within 3 s after [DONE] with trailing data " +
				"(deadlock: copy goroutine stuck in r.Read() — MUTATION GUARD: is close(scanDone) + watcher case present?)")
		}
	}

	// A nil terminal error means clean [DONE] — the trailing-body-close must NOT
	// surface as an error to the caller.
	if termErr != nil {
		t.Fatalf("terminal error = %v, want nil", termErr)
	}
	if len(got) != 1 || got[0] != "tok" {
		t.Fatalf("tokens = %v, want [tok]", got)
	}
	// r.Close() must have been called by the watcher to unblock the copy goroutine.
	if !r.isClosed() {
		t.Fatal("StreamChannel did not call Close() on the ReadCloser on clean [DONE] path")
	}
}

// TestStreamChannelMidStreamCancel proves that cancelling the context while
// events are already pending in the channel causes StreamChannel to stop
// promptly — i.e. it does not require all enqueued events to be drained before
// it honours the cancellation. This exercises the `case <-ctx.Done()` branch
// inside the scanSSE callback that the existing cancel-before-read test misses.
//
// MUTATION GUARD: replacing the select inside the scanSSE callback with a plain
// `events <- ev` (removing the ctx.Done() case) causes this test to hang: the
// goroutine blocks waiting for the caller to drain the unbuffered events channel
// while the caller (this test) has already stopped reading after the cancel.
func TestStreamChannelMidStreamCancel(t *testing.T) {
	t.Parallel()

	// Use a synchronised reader that lets us pace delivery: we feed one frame,
	// then cancel the context, then attempt to feed more frames. The goroutine
	// must stop without requiring us to drain the channel.
	readyCh := make(chan struct{})  // closed after the first frame is queued
	cancelCh := make(chan struct{}) // closed when ctx is cancelled (signals reader to continue)

	// pausingReader delivers the first SSE frame synchronously, then pauses
	// until cancelCh is closed to simulate a slow body arriving mid-stream.
	pr, pw := io.Pipe()
	go func() {
		// Write the first event immediately.
		_, _ = pw.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n"))
		close(readyCh)
		// Wait until the test cancels the ctx before writing more, so the cancel
		// always races with a pending (or in-flight) event send.
		<-cancelCh
		// Write a second frame that the goroutine should NOT deliver because ctx
		// is already cancelled.
		_, _ = pw.Write([]byte("data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"B\"}}]}\n\ndata: [DONE]\n\n"))
		pw.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	events, errc := StreamChannel(ctx, pr)

	// Wait until at least one frame has been written to the pipe (so the
	// goroutine has had a chance to parse it and attempt to send it on events).
	<-readyCh
	// Cancel the context; this must cause StreamChannel to stop promptly even
	// if the events channel is not being drained by this goroutine.
	cancel()
	close(cancelCh) // let the pipe writer proceed (it will also unblock pipeW on ctx path)

	// Now drain whatever events arrived before cancellation and wait for both
	// channels to close. The test must complete well within the deadline.
	const deadline = 3 * time.Second
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	eventsClosed := false
	errcClosed := false
	for !eventsClosed || !errcClosed {
		select {
		case _, ok := <-events:
			if !ok {
				eventsClosed = true
			}
		case _, ok := <-errc:
			if !ok {
				errcClosed = true
			}
		case <-timer.C:
			t.Fatal("StreamChannel channels did not close within 3 s after mid-stream ctx cancel " +
				"(possible goroutine leak — mutation guard: is the ctx.Done() case inside the callback present?)")
		}
	}
	// Reaching here proves the goroutine exited promptly on cancellation.
}

// closerFunc is an io.Closer backed by a plain function, used in table-driven
// test helpers so we can compose Reader+Closer without a bespoke struct.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// TestStreamChannelHappyPathUnchanged proves that the ctx-wiring refactor does
// NOT break the normal happy-path: all chunks arrive in order and the terminal
// error is nil, identical to the pre-fix behaviour captured by
// TestStreamChannelOrdered. Also asserts that Close is called on a ReadCloser
// after a normal [DONE] termination.
//
// MUTATION GUARD: reversing the send order in StreamChannel (or swapping events
// and errc closes) breaks the ordered-token assertion.
func TestStreamChannelHappyPathUnchanged(t *testing.T) {
	t.Parallel()

	var closedFlag atomic.Bool
	rc := struct {
		io.Reader
		io.Closer
	}{
		Reader: strings.NewReader(cannedStream),
		Closer: closerFunc(func() error { closedFlag.Store(true); return nil }),
	}

	events, errc := StreamChannel(context.Background(), rc)

	var got []string
	for e := range events {
		got = append(got, e.Content)
	}
	if err := <-errc; err != nil {
		t.Fatalf("terminal error = %v, want nil", err)
	}
	want := []string{"Hel", "lo", " world"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	if !closedFlag.Load() {
		t.Fatal("StreamChannel did not call Close() on the ReadCloser on happy path")
	}
}
