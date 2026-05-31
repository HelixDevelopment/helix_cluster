package chutes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// doneSentinel is the OpenAI SSE terminator that ends a stream.
const doneSentinel = "[DONE]"

// ErrStreamClosed is returned (via the error channel) when the stream ends
// without an explicit [DONE] sentinel — useful to distinguish a clean close
// from a truncated one. A normal [DONE] stream yields a nil error.
var ErrStreamClosed = errors.New("chutes: stream closed without [DONE]")

// StreamDelta is one decoded streamed token delta, in arrival order.
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
func StreamChannel(ctx context.Context, r io.Reader) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent)
	errc := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errc)
		err := scanSSE(r, func(payload string) (bool, error) {
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
			select {
			case events <- ev:
				return false, nil
			case <-ctx.Done():
				return true, ctx.Err()
			}
		})
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
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
