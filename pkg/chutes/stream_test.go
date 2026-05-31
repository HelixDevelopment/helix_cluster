package chutes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
