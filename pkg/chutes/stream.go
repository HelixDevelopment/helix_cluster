package chutes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// doneSentinel is the OpenAI SSE terminator that ends a stream.
const doneSentinel = "[DONE]"

// ErrStreamClosed is returned (via the error channel) when the stream ends
// without an explicit [DONE] sentinel — useful to distinguish a clean close
// from a truncated one. A normal [DONE] stream yields a nil error.
var ErrStreamClosed = errors.New("chutes: stream closed without [DONE]")

// StreamChoiceDelta is one decoded streamed token delta, in arrival order.
type StreamChoiceDelta struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamChoice struct {
	Index        int               `json:"index"`
	Delta        StreamChoiceDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

// streamFrame is one parsed SSE data frame's JSON payload.
type streamFrame struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
}

// StreamEvent is one ordered event surfaced to the caller. Content is the token
// delta text for choice 0; FinishReason is set on the final content frame.
type StreamEvent struct {
	Content      string
	FinishReason string
	Model        string
}

// DecodeStream reads an SSE body and returns the ORDERED token deltas. It parses
// "data: {json}" frames, joins multi-line data fields with "\n" per the SSE
// spec, ignores keep-alive comment lines (those starting with ':') and blank
// separators, and STOPS at the "[DONE]" sentinel. Frame order is preserved: the
// returned slice is in exactly the order frames arrived.
//
// This is the eager form used by tests and simple callers; StreamChannel offers
// an incremental channel for live consumption.
func DecodeStream(r io.Reader) ([]StreamEvent, error) {
	var events []StreamEvent
	err := scanSSE(r, func(payload string) (bool, error) {
		if payload == doneSentinel {
			return true, nil // stop
		}
		var f streamFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			return false, fmt.Errorf("chutes: decode SSE frame: %w", err)
		}
		ev := StreamEvent{Model: f.Model}
		if len(f.Choices) > 0 {
			ev.Content = f.Choices[0].Delta.Content
			ev.FinishReason = f.Choices[0].FinishReason
		}
		events = append(events, ev)
		return false, nil
	})
	if err != nil {
		return events, err
	}
	return events, nil
}

// scanSSE drives a line scanner over an SSE stream, assembling logical events
// separated by blank lines. For each event it extracts the (possibly multi-line)
// "data:" field and invokes onData with the joined payload. onData returns
// (stop, err); a true stop halts scanning (used for [DONE]). It returns
// ErrStreamClosed if EOF is reached without a stop signal.
func scanSSE(r io.Reader, onData func(payload string) (stop bool, err error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var dataLines []string
	flush := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return onData(payload)
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			// Event boundary: dispatch the accumulated data field.
			stop, err := flush()
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
		case strings.HasPrefix(line, ":"):
			// Keep-alive / comment line: ignore.
			continue
		case strings.HasPrefix(line, "data:"):
			// Strip "data:" and a single optional leading space (SSE spec).
			v := line[len("data:"):]
			v = strings.TrimPrefix(v, " ")
			dataLines = append(dataLines, v)
		default:
			// Other SSE fields (event:, id:, retry:) are not needed here.
			continue
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("chutes: scan SSE: %w", err)
	}
	// Flush a trailing event with no terminating blank line.
	stop, err := flush()
	if err != nil {
		return err
	}
	if stop {
		return nil
	}
	return ErrStreamClosed
}

// StreamChannel decodes an SSE body incrementally, sending each ordered
// StreamEvent on the returned channel and closing it when [DONE] is reached or
// the context is cancelled. The error channel carries at most one terminal
// error (nil on clean [DONE] close, ErrStreamClosed on truncation). The caller
// must drain events; both channels are closed by the goroutine.
//
// Cancellation contract: when ctx is cancelled, StreamChannel stops reading r
// and closes both channels promptly — even if r's Read is currently blocked
// (e.g. a slow or stalled network body). This is achieved via two mechanisms:
//
//  1. An io.Pipe forwards r → scanner; closing pipeW causes bufio.Scanner to
//     see EOF regardless of the pipe's read side.
//  2. If r is an io.Closer (e.g. http.Response.Body), the watcher goroutine
//     calls r.Close() on ctx cancellation, which unblocks any in-progress
//     r.Read() call so the forwarding goroutine exits promptly.
//
// Ownership: if r implements io.Closer, StreamChannel calls r.Close() exactly
// once — either on a clean [DONE] completion or on ctx cancellation. This
// mirrors the defer Body.Close() pattern used by attempt() and StreamCompletion.
func StreamChannel(ctx context.Context, r io.Reader) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent)
	errc := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errc)

		// closeOnce ensures r.Close() is called exactly once even though it may
		// be triggered by either the watcher (ctx cancel) or the deferred cleanup.
		var closeOnce sync.Once
		closeReader := func() {
			if rc, ok := r.(io.Closer); ok {
				closeOnce.Do(func() { rc.Close() })
			}
		}
		// Always close r when the goroutine exits (idempotent via closeOnce).
		defer closeReader()

		// Pipe r through an io.Pipe so that closing the write end (pipeW)
		// immediately signals EOF to bufio.Scanner reading from pipeR — even if r
		// itself is still blocking. Without this indirection, ctx cancellation
		// would not interrupt a blocked bufio.Scanner.Scan() call because the
		// scanner's next Scan() is stuck inside r.Read().
		pipeR, pipeW := io.Pipe()

		// scanDone is closed by the main goroutine immediately after scanSSE
		// returns, signalling the watcher goroutine that scanning has finished.
		// The watcher then closes both r (via closeReader) and pipeW, which
		// unblocks the copy goroutine from wherever it is currently blocked:
		//
		//   • If the copy goroutine is blocked in r.Read() (common when r has
		//     trailing data after [DONE]), closeReader() unblocks it.
		//   • If the copy goroutine is blocked in pipeW.Write() (waiting for the
		//     scanner to drain pipeR), pipeW.CloseWithError unblocks it.
		//
		// This is the deterministic teardown path regardless of whether the stop
		// was a clean [DONE], a decode error, or a ctx cancellation.
		scanDone := make(chan struct{})

		// copyDone signals that the forwarding goroutine has exited.
		copyDone := make(chan struct{})

		// Forward r → pipeW. When copy returns (r exhausted, r errors, or r was
		// closed by the watcher) we close pipeW so the scanner sees EOF.
		go func() {
			defer close(copyDone)
			_, copyErr := io.Copy(pipeW, r)
			// nil copyErr → pipeW.Close() signals clean EOF to the scanner.
			// non-nil copyErr → pipeW.CloseWithError propagates the error.
			pipeW.CloseWithError(copyErr)
		}()

		// Watcher: unblocks the copy goroutine whenever scanning stops — whether
		// due to ctx cancellation or a normal/early return from scanSSE ([DONE],
		// decode error). Two shutdown sources are multiplexed here:
		//
		//  1. ctx.Done() — caller cancelled; use ctx.Err() as the pipe error so
		//     the copy goroutine propagates a meaningful error upward.
		//  2. scanDone — scanSSE returned (clean [DONE] or decode error); use nil
		//     so the copy goroutine's CloseWithError preserves the real error.
		//
		// In both cases we call closeReader() first to unblock any in-progress
		// r.Read(), then CloseWithError on pipeW to unblock any pending Write.
		go func() {
			select {
			case <-ctx.Done():
				closeReader()
				pipeW.CloseWithError(ctx.Err())
			case <-scanDone:
				closeReader()
				pipeW.CloseWithError(nil)
			case <-copyDone:
				// Copy goroutine already closed pipeW; nothing to do.
			}
		}()

		err := scanSSE(pipeR, func(payload string) (bool, error) {
			if payload == doneSentinel {
				return true, nil
			}
			var f streamFrame
			if uerr := json.Unmarshal([]byte(payload), &f); uerr != nil {
				return false, fmt.Errorf("chutes: decode SSE frame: %w", uerr)
			}
			ev := StreamEvent{Model: f.Model}
			if len(f.Choices) > 0 {
				ev.Content = f.Choices[0].Delta.Content
				ev.FinishReason = f.Choices[0].FinishReason
			}
			// Both the event send and a concurrent ctx cancellation are handled
			// here. If ctx fires while the caller is slow to drain events, we
			// stop immediately rather than blocking until the caller consumes.
			select {
			case events <- ev:
				return false, nil
			case <-ctx.Done():
				return true, ctx.Err()
			}
		})

		// Signal the watcher that scanSSE has returned. The watcher will close r
		// (unblocking any pending r.Read) and pipeW (unblocking any pending
		// pipeW.Write), after which the copy goroutine exits and copyDone closes.
		close(scanDone)

		// Wait for the copy goroutine to finish so we don't leak it. By this
		// point the watcher has (or shortly will) close pipeW and r, so the copy
		// goroutine will exit promptly regardless of what state it is in.
		<-copyDone

		errc <- err
	}()
	return events, errc
}

// StreamCompletion POSTs a streaming chat request to a single endpoint and
// returns the decoded ordered events. It is a convenience wrapper that sets
// Stream=true, sends the request, and decodes the SSE body. Fallback is not
// applied here: streaming a partial response and then switching providers would
// produce an inconsistent token stream, so callers pick the endpoint explicitly.
func (c *Client) StreamCompletion(ctx context.Context, ep Endpoint, req ChatRequest) ([]StreamEvent, error) {
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}
	if req.Model == "" {
		return nil, ErrNoModel
	}
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("chutes: marshal stream request: %w", err)
	}

	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	url := ep.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if ep.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	httpResp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chutes: %s: stream status %d", ep.Name, httpResp.StatusCode)
	}
	return DecodeStream(httpResp.Body)
}
