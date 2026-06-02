// Package chutes is an inference-provider client over an OpenAI-compatible
// /v1/chat/completions endpoint (default base https://llm.chutes.ai/v1).
//
// SCOPE: this is INFERENCE, distinct from the sibling package
// github.com/HelixDevelopment/helix_cluster/pkg/provider, which rents raw GPU
// compute. A Client here sends a chat-completion request to a Chutes-hosted (or
// any OpenAI-compatible) LLM endpoint, authenticating with a "cpk_" bearer API
// key, and returns the parsed completion.
//
// HONEST EXTERNAL SEAM: the live Chutes inference service is out of scope for
// this package. Client speaks the concrete OpenAI JSON/HTTP wire protocol
// against a configurable base URL + bearer key. It is exercised end to end in
// tests against a LOCAL net/http/httptest server that emulates
// /v1/chat/completions (including HTTP-429 backpressure); pointing BaseURL at
// the real endpoint with a real "cpk_" key is the only change needed to drive
// production. No real cloud or network beyond the injected endpoint is
// contacted.
//
// RETRY SEMANTICS: HTTP 429 (Too Many Requests) is treated as transient and is
// retried with bounded backoff. Any other non-2xx status is surfaced
// immediately as a typed *APIError — never a fabricated completion.
package chutes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Chutes OpenAI-compatible API root used when Config.BaseURL
// is empty.
const DefaultBaseURL = "https://llm.chutes.ai/v1"

// defaultMaxRetries bounds how many ADDITIONAL attempts are made after the first
// when the server replies 429. Total attempts == defaultMaxRetries + 1.
const defaultMaxRetries = 3

// defaultBaseBackoff is the first inter-retry delay; it grows linearly per retry.
// Kept small so production latency stays bounded; tests inject a tiny value.
const defaultBaseBackoff = 200 * time.Millisecond

// ErrEmptyAPIKey is returned by New when constructed without an API key, so a
// misconfigured client never silently issues unauthenticated requests.
var ErrEmptyAPIKey = errors.New("chutes: empty api key")

// APIError is the typed error surfaced for a non-retryable, non-2xx HTTP
// response. It carries the status code and (truncated) response body so callers
// can branch on the failure class with errors.As — it is NEVER a fabricated
// completion.
type APIError struct {
	// StatusCode is the HTTP status returned by the endpoint.
	StatusCode int
	// Body is the raw (possibly truncated) response body for diagnostics.
	Body string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("chutes: api error: status %d: %s", e.StatusCode, e.Body)
}

// Message is a single OpenAI-compatible chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest is the POST /v1/chat/completions request body
// (OpenAI-compatible shape).
type ChatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Choice is one completion choice in a ChatCompletionResponse.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage reports token accounting for a completion (OpenAI-compatible shape).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionResponse is the /v1/chat/completions response body
// (OpenAI-compatible shape).
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Config configures a Client.
type Config struct {
	// BaseURL is the OpenAI-compatible API root. Empty means DefaultBaseURL.
	BaseURL string
	// APIKey is sent as "Authorization: Bearer <APIKey>". Required; a "cpk_"
	// prefixed key is expected by Chutes but not enforced here.
	APIKey string
	// HTTPClient lets callers/tests inject a client; nil uses a default.
	HTTPClient *http.Client
	// MaxRetries bounds additional attempts after a 429. Zero means
	// defaultMaxRetries. Negative disables retries.
	MaxRetries int
	// BaseBackoff is the first inter-retry delay (grows linearly). Zero means
	// defaultBaseBackoff; tests inject a tiny value for determinism.
	BaseBackoff time.Duration
	// sleep is the injectable delay function used between retries. nil uses
	// time.Sleep. Unexported so it is test-only.
	sleep func(time.Duration)
}

// Client posts chat completions to an OpenAI-compatible endpoint.
type Client struct {
	base        string
	apiKey      string
	hc          *http.Client
	maxRetries  int
	baseBackoff time.Duration
	sleep       func(time.Duration)
}

// New builds a Client from cfg. It returns ErrEmptyAPIKey when no API key is
// supplied so the client never silently issues unauthenticated requests.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrEmptyAPIKey
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	maxRetries := cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultMaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	backoff := cfg.BaseBackoff
	if backoff == 0 {
		backoff = defaultBaseBackoff
	}
	sleep := cfg.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Client{
		base:        strings.TrimRight(base, "/"),
		apiKey:      cfg.APIKey,
		hc:          hc,
		maxRetries:  maxRetries,
		baseBackoff: backoff,
		sleep:       sleep,
	}, nil
}

// CreateChatCompletion POSTs req to {BaseURL}/chat/completions with the bearer
// API key and returns the parsed completion read off the wire.
//
// On HTTP 429 it retries with bounded linear backoff up to MaxRetries
// additional attempts. The completion is decoded FROM THE SERVER RESPONSE — it
// is never fabricated. A persistent 429 (retries exhausted) or any other
// non-2xx status returns a typed *APIError.
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("chutes: marshal request: %w", err)
	}

	// Total attempts = 1 initial + maxRetries.
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Linear backoff: attempt 1 -> 1*base, attempt 2 -> 2*base, ...
			c.sleep(time.Duration(attempt) * c.baseBackoff)
		}

		raw, status, err := c.do(ctx, body)
		if err != nil {
			// Transport-level error (e.g. context cancelled): not retryable here.
			return nil, fmt.Errorf("chutes: chat completion: %w", err)
		}

		switch {
		case status >= 200 && status < 300:
			var resp ChatCompletionResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return nil, fmt.Errorf("chutes: decode response: %w", err)
			}
			return &resp, nil
		case status == http.StatusTooManyRequests:
			// Transient: remember the error and loop to retry (if budget remains).
			lastErr = &APIError{StatusCode: status, Body: truncate(raw)}
			continue
		default:
			// Non-retryable non-2xx: surface a typed error, never a completion.
			return nil, &APIError{StatusCode: status, Body: truncate(raw)}
		}
	}

	// Retries exhausted while still seeing 429.
	if lastErr == nil {
		// Defensive: should not happen, but never return (nil, nil).
		lastErr = &APIError{StatusCode: http.StatusTooManyRequests, Body: "retries exhausted"}
	}
	return nil, fmt.Errorf("chutes: chat completion: retries exhausted: %w", lastErr)
}

// do performs one HTTP POST of body to the chat-completions path, returning the
// raw response body and status code.
func (c *Client) do(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// truncate bounds an error body so a huge upstream payload cannot bloat error
// messages.
func truncate(b []byte) string {
	const max = 512
	s := string(b)
	if len(s) > max {
		return s[:max] + "...(truncated)"
	}
	return s
}
