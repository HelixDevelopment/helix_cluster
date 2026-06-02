// Package chutesaccount provides a client for the Chutes API,
// covering model-list and user-account/balance queries against
// an OpenAI-style HTTP API.
//
// Every outbound request carries the header:
//
//	Authorization: Bearer <apiKey>
//
// where apiKey is a cpk_-style token supplied at construction time.
// Removing the header-setting line causes TestGetAccountBalance to fail
// because the test server responds 401 to any request without the header.
package chutesaccount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ----------------------------------------------------------------------------
// Public types
// ----------------------------------------------------------------------------

// ModelEntry represents a single model returned by the /v1/models endpoint.
type ModelEntry struct {
	// ID is the model identifier (e.g. "chutes/deepseek-r1").
	ID string `json:"id"`

	// PricePerToken is the per-token price (input+output combined) in USD.
	PricePerToken float64 `json:"price_per_token"`

	// TEE indicates whether the model runs inside a Trusted-Execution
	// Environment and therefore provides attestation guarantees.
	TEE bool `json:"tee"`
}

// Account holds the authenticated user's account information.
type Account struct {
	// Balance is the current wallet balance in USD.
	Balance float64 `json:"balance"`
}

// ----------------------------------------------------------------------------
// Client
// ----------------------------------------------------------------------------

// Client is a Chutes API HTTP client. Construct one with NewClient.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Client that sends every request to baseURL and
// authenticates with apiKey (a cpk_-style Bearer token).
//
// A zero-value http.Client with a 30-second timeout is used internally.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ----------------------------------------------------------------------------
// ListModels
// ----------------------------------------------------------------------------

// modelsResponse is the OpenAI-style envelope returned by /v1/models.
type modelsResponse struct {
	Object string `json:"object"` // "list"
	Data   []struct {
		ID     string          `json:"id"`
		Object string          `json:"object"` // "model"
		Extra  json.RawMessage `json:"extra,omitempty"`

		// Chutes-specific fields embedded at the top level.
		PricePerToken float64 `json:"price_per_token"`
		TEE           bool    `json:"tee"`
	} `json:"data"`
}

// ListModels fetches the list of available models from /v1/models and
// returns one ModelEntry per model.
//
// It returns an error if the HTTP request fails, the server responds with
// a non-2xx status, or the response body cannot be decoded.
func (c *Client) ListModels(ctx context.Context) ([]ModelEntry, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("chutesaccount: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chutesaccount: do request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, err
	}

	var envelope modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("chutesaccount: decode models: %w", err)
	}

	entries := make([]ModelEntry, 0, len(envelope.Data))
	for _, d := range envelope.Data {
		entries = append(entries, ModelEntry{
			ID:            d.ID,
			PricePerToken: d.PricePerToken,
			TEE:           d.TEE,
		})
	}
	return entries, nil
}

// ----------------------------------------------------------------------------
// GetAccount
// ----------------------------------------------------------------------------

// accountResponse is the JSON returned by /users/me.
type accountResponse struct {
	Balance float64 `json:"balance"`
}

// GetAccount retrieves the authenticated user's account information from
// /users/me.
//
// It returns an error if the HTTP request fails, the server responds with
// a non-2xx status, or the response body cannot be decoded.
func (c *Client) GetAccount(ctx context.Context) (Account, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/users/me", nil)
	if err != nil {
		return Account{}, fmt.Errorf("chutesaccount: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("chutesaccount: do request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return Account{}, err
	}

	var ar accountResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return Account{}, fmt.Errorf("chutesaccount: decode account: %w", err)
	}

	return Account{Balance: ar.Balance}, nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// newRequest builds an *http.Request for the given method and path (relative
// to c.baseURL), attaches the Authorization header, and associates ctx.
//
// MUTATION GUARD: the line that sets the Authorization header is the guard
// pinned by TestGetAccountBalance — removing it causes the test server to
// return 401, which in turn causes checkStatus to return an error and the
// test to fail. (See chutesaccount_test.go: TestGetAccountBalance)
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	// MUTATION GUARD (pins TestGetAccountBalance): removing this line makes
	// the test server return HTTP 401 and the test fails.
	req.Header.Set("Authorization", "Bearer "+c.apiKey) // guard: TestGetAccountBalance
	return req, nil
}

// checkStatus returns a descriptive error when the HTTP response status is
// outside the 2xx range.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("chutesaccount: server returned %d: %s", resp.StatusCode, string(body))
}
