package marketplace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Chutes (like most inference marketplaces) streams generated tokens back over
// Server-Sent Events (SSE): a text/event-stream where each event is a block of
// lines, the payload carried on `data:` lines, events separated by a blank line,
// and the stream terminated by a sentinel `data: [DONE]`. This file implements a
// real SSE decoder for that wire format so the cluster can surface streamed
// tokens to a job IN ORDER. It is stdlib-only and is exercised in tests against a
// local httptest server that speaks the exact SSE framing a real provider uses.

// ErrStreamClosed is returned by TokenStream.Next when the stream has ended
// (the [DONE] sentinel was seen or the body reached EOF). It is the streaming
// analogue of io.EOF and callers should treat it as a clean stop.
var ErrStreamClosed = errors.New("marketplace: token stream closed")

// sseDoneSentinel is the canonical end-of-stream marker emitted on a data line.
const sseDoneSentinel = "[DONE]"

// sseChunk is the JSON object carried on each SSE `data:` line for token
// streaming. Only the fields this package needs are decoded; unknown fields are
// ignored so the decoder is forward-compatible with richer provider payloads.
type sseChunk struct {
	// Token is the incremental text produced by this event.
	Token string `json:"token"`
}

// TokenStream yields generated tokens from an SSE event-stream IN THE ORDER the
// upstream emitted them. It is single-consumer and NOT safe for concurrent use:
// Next must be called from one goroutine at a time. Close releases the
// underlying body.
type TokenStream struct {
	rc      io.ReadCloser
	scanner *bufio.Scanner
	done    bool
}

// NewTokenStream wraps an SSE event-stream body in a TokenStream. The caller owns
// closing it via TokenStream.Close (which closes the underlying body).
func NewTokenStream(body io.ReadCloser) *TokenStream {
	return &TokenStream{rc: body, scanner: bufio.NewScanner(body)}
}

// Next returns the next streamed token. It returns ErrStreamClosed (and an empty
// token) once the [DONE] sentinel or end-of-body is reached. Blank lines and
// non-data SSE fields (event:, id:, retry:, comments) are skipped. A malformed
// data payload returns a decode error without advancing past it.
//
// Ordering guarantee: tokens are returned in exactly the sequence their `data:`
// lines appear in the stream — this method never reorders, dedups, or batches.
func (s *TokenStream) Next() (string, error) {
	if s.done {
		return "", ErrStreamClosed
	}
	for s.scanner.Scan() {
		line := s.scanner.Text()
		// SSE: blank line is an event boundary; nothing to emit on its own.
		if line == "" {
			continue
		}
		// SSE comments begin with ':'. Ignore them.
		if strings.HasPrefix(line, ":") {
			continue
		}
		// We only care about data: lines for token payloads.
		const dataPrefix = "data:"
		if !strings.HasPrefix(line, dataPrefix) {
			continue
		}
		// Per spec a single optional leading space after the colon is stripped.
		payload := strings.TrimPrefix(line[len(dataPrefix):], " ")
		if payload == sseDoneSentinel {
			s.done = true
			return "", ErrStreamClosed
		}
		var chunk sseChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return "", err
		}
		return chunk.Token, nil
	}
	if err := s.scanner.Err(); err != nil {
		s.done = true
		return "", err
	}
	// Clean EOF without an explicit [DONE].
	s.done = true
	return "", ErrStreamClosed
}

// Close releases the underlying stream body.
func (s *TokenStream) Close() error {
	s.done = true
	return s.rc.Close()
}

// Collect drains the stream and returns every token in order. It stops cleanly at
// ErrStreamClosed and surfaces any other error (e.g. a malformed data payload).
// It is a convenience for non-incremental callers; incremental callers use Next.
func Collect(s *TokenStream) ([]string, error) {
	var out []string
	for {
		tok, err := s.Next()
		if errors.Is(err, ErrStreamClosed) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, tok)
	}
}

// OpenTokenStream issues an HTTP GET for an SSE token stream and returns a
// TokenStream over the response body. A non-200 status is surfaced as an error
// (the body is closed) — the streaming analogue of httpOfferSource's 503 guard.
// In production the URL is the live Chutes streaming endpoint; in tests it is a
// local httptest server speaking the same event-stream framing.
func OpenTokenStream(ctx context.Context, client *http.Client, url string) (*TokenStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		status := resp.Status
		_ = resp.Body.Close()
		return nil, errors.New("marketplace: stream upstream status " + status)
	}
	return NewTokenStream(resp.Body), nil
}
