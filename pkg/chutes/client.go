// Package chutes provides a clean-room, stdlib-only client for a Chutes-style
// decentralized-inference network. It exposes an OpenAI-compatible
// chat/completions HTTP client with a PROVIDER FALLBACK CHAIN, a Server-Sent-
// Events streaming decoder, an encryption envelope seam with a working AES-256-
// GCM reference implementation, and a GPU-attestation seam with a working HMAC
// reference.
//
// SCOPE / HONESTY: this package talks the OpenAI /v1/chat/completions wire
// protocol over net/http; it does NOT embed a model runtime. The crypto and
// attestation seams ship REAL, round-tripping stdlib reference implementations
// (AES-256-GCM, HMAC-SHA256). The PQ ML-KEM end-to-end encryption in
// digital.vasic.security/pkg/e2ee and the proof-of-GPU attestation in
// digital.vasic.security/pkg/gpuattest satisfy the Encryptor and Attestor seams
// defined here without changing any caller.
package chutes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Sentinel errors let callers branch on the failure class with errors.Is.
var (
	// ErrNoEndpoints is returned when a client is built with no endpoints.
	ErrNoEndpoints = errors.New("chutes: no endpoints configured")
	// ErrAllEndpointsFailed is returned when every endpoint in the fallback
	// chain failed; it wraps the last underlying cause.
	ErrAllEndpointsFailed = errors.New("chutes: all endpoints failed")
	// ErrEmptyMessages is returned for a request with no messages.
	ErrEmptyMessages = errors.New("chutes: request has no messages")
	// ErrNoModel is returned for a request with no model.
	ErrNoModel = errors.New("chutes: request has no model")
)

// Message is a single OpenAI-style chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is an OpenAI-compatible chat/completions request body.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Choice is one completion choice in a non-streamed response.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage reports token accounting from the provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is a parsed OpenAI-compatible chat/completions response. The
// Endpoint field is filled in by the client with the endpoint URL that actually
// served the request — it is the load-bearing field for fallback assertions.
type ChatResponse struct {
	ID       string   `json:"id"`
	Model    string   `json:"model"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
	Endpoint string   `json:"-"`
}

// Content returns the assistant content of the first choice, or "".
func (r ChatResponse) Content() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// Endpoint is one provider endpoint in the fallback chain.
type Endpoint struct {
	// Name is a stable label used in error messages and fallback assertions.
	Name string
	// BaseURL is the provider root, e.g. "https://chutes.example". The client
	// appends "/v1/chat/completions".
	BaseURL string
	// APIKey, when non-empty, is sent as an "Authorization: Bearer" header.
	APIKey string
}

// Config configures a Client.
type Config struct {
	// Endpoints is the fallback chain in preference order: index 0 is preferred,
	// later entries are tried on error/5xx/timeout.
	Endpoints []Endpoint
	// Timeout bounds each individual endpoint attempt. Zero means no per-attempt
	// timeout beyond the caller's context.
	Timeout time.Duration
	// HTTPClient lets tests inject a client; nil uses a default.
	HTTPClient *http.Client
}

// Client is an OpenAI-compatible chat client with a provider fallback chain.
type Client struct {
	endpoints []Endpoint
	timeout   time.Duration
	hc        *http.Client
}

// NewClient builds a Client from cfg. It returns ErrNoEndpoints if the chain is
// empty so a misconfigured client never silently no-ops.
func NewClient(cfg Config) (*Client, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, ErrNoEndpoints
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	eps := make([]Endpoint, len(cfg.Endpoints))
	copy(eps, cfg.Endpoints)
	return &Client{endpoints: eps, timeout: cfg.Timeout, hc: hc}, nil
}

// Endpoints returns the configured fallback chain in preference order (a copy).
func (c *Client) Endpoints() []Endpoint {
	out := make([]Endpoint, len(c.endpoints))
	copy(out, c.endpoints)
	return out
}

// retryableStatus reports whether an HTTP status should trigger fallback to the
// next endpoint. 5xx and 429 are transient/server-side; 4xx (except 429) are
// caller errors and are surfaced without burning the rest of the chain.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// ChatCompletion runs req against the fallback chain. It tries the preferred
// endpoint first; on transport error, timeout, or a retryable status (5xx/429)
// it falls back to the next configured endpoint. The returned ChatResponse has
// Endpoint set to the Name of the endpoint that served it.
//
// A non-retryable 4xx (e.g. 400/401) is returned immediately, wrapped, without
// trying the rest of the chain — falling back on a bad API key would be wrong.
func (c *Client) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if len(req.Messages) == 0 {
		return ChatResponse{}, ErrEmptyMessages
	}
	if req.Model == "" {
		return ChatResponse{}, ErrNoModel
	}
	req.Stream = false

	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("chutes: marshal request: %w", err)
	}

	var lastErr error
	for _, ep := range c.endpoints {
		resp, status, err := c.attempt(ctx, ep, body)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", ep.Name, err)
			continue
		}
		if retryableStatus(status) {
			lastErr = fmt.Errorf("%s: server status %d", ep.Name, status)
			continue
		}
		if status != http.StatusOK {
			// Non-retryable client error: do not fall back, surface it.
			return ChatResponse{}, fmt.Errorf("chutes: %s: non-retryable status %d", ep.Name, status)
		}
		resp.Endpoint = ep.Name
		return resp, nil
	}
	return ChatResponse{}, fmt.Errorf("%w: %v", ErrAllEndpointsFailed, lastErr)
}

// attempt performs a single endpoint request. It returns the parsed response
// (only meaningful when status==200) and the HTTP status code.
func (c *Client) attempt(ctx context.Context, ep Endpoint, body []byte) (ChatResponse, int, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	url := ep.BaseURL + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if ep.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}

	httpResp, err := c.hc.Do(httpReq)
	if err != nil {
		return ChatResponse{}, 0, err
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return ChatResponse{}, httpResp.StatusCode, err
	}
	if httpResp.StatusCode != http.StatusOK {
		// Body of an error response is not parsed as a ChatResponse.
		return ChatResponse{}, httpResp.StatusCode, nil
	}
	var parsed ChatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ChatResponse{}, httpResp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	return parsed, httpResp.StatusCode, nil
}
