// Package marketplaceadapter implements HXC-1454: a MarketplaceAdapter
// interface plus a Chutes adapter and a Name()-based dispatch Registry.
//
// It is fully self-contained (stdlib only) and does NOT import the shipped
// pkg/marketplace. The Chutes adapter speaks real HTTP and is exercised in
// tests against an httptest fixture server (no real network).
package marketplaceadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// Offer is a current pricing quote returned by a marketplace.
type Offer struct {
	Marketplace string  `json:"marketplace"`
	PriceUSD    float64 `json:"price_usd"`
	GPUModel    string  `json:"gpu_model"`
}

// WorkSpec describes a unit of work to submit to a marketplace.
type WorkSpec struct {
	ID       string `json:"id"`
	GPUModel string `json:"gpu_model"`
}

// WorkResult is the outcome of submitting a WorkSpec to a marketplace.
type WorkResult struct {
	Marketplace string `json:"marketplace"`
	JobID       string `json:"job_id"`
	Accepted    bool   `json:"accepted"`
}

// MarketplaceAdapter is the common interface every marketplace backend
// implements. Name() is the routing key used by Registry.Dispatch.
type MarketplaceAdapter interface {
	Name() string
	GetCurrentPricing(ctx context.Context, gpuModel string) (Offer, error)
	SubmitWork(ctx context.Context, spec WorkSpec) (WorkResult, error)
}

// ErrUnknownMarketplace is returned by Dispatch when no registered adapter's
// Name() matches the requested marketplace.
var ErrUnknownMarketplace = errors.New("marketplaceadapter: unknown marketplace")

// Registry holds adapters keyed by their Name() and routes work to them.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]MarketplaceAdapter
}

// NewRegistry returns an empty Registry ready for Register.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]MarketplaceAdapter)}
}

// Register adds an adapter under its Name(). A later Register with the same
// Name() replaces the earlier one.
func (r *Registry) Register(a MarketplaceAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adapters == nil {
		r.adapters = make(map[string]MarketplaceAdapter)
	}
	r.adapters[a.Name()] = a
}

// Dispatch routes a SubmitWork call to the adapter whose Name() == marketplace.
// It returns ErrUnknownMarketplace if no such adapter is registered.
func (r *Registry) Dispatch(ctx context.Context, marketplace string, spec WorkSpec) (WorkResult, error) {
	r.mu.RLock()
	a, ok := r.adapters[marketplace]
	r.mu.RUnlock()
	if !ok {
		return WorkResult{}, fmt.Errorf("%w: %q", ErrUnknownMarketplace, marketplace)
	}
	return a.SubmitWork(ctx, spec)
}

// ChutesAdapter is a MarketplaceAdapter backed by the Chutes HTTP API.
type ChutesAdapter struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewChutesAdapter constructs a ChutesAdapter pointed at baseURL with a default
// HTTP client.
func NewChutesAdapter(baseURL string) *ChutesAdapter {
	return &ChutesAdapter{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{},
	}
}

// Name returns the routing key for this adapter.
func (c *ChutesAdapter) Name() string { return "chutes" }

func (c *ChutesAdapter) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// GetCurrentPricing GETs the Chutes pricing endpoint for gpuModel and decodes
// the returned Offer, stamping Marketplace="chutes".
func (c *ChutesAdapter) GetCurrentPricing(ctx context.Context, gpuModel string) (Offer, error) {
	url := c.BaseURL + "/v1/pricing?gpu=" + gpuModel
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Offer{}, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return Offer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Offer{}, fmt.Errorf("marketplaceadapter: chutes pricing status %d", resp.StatusCode)
	}
	var o Offer
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return Offer{}, err
	}
	o.Marketplace = "chutes"
	return o, nil
}

// SubmitWork POSTs spec to the Chutes submit endpoint. On a 2xx response it
// returns WorkResult{Marketplace:"chutes", JobID:..., Accepted:true} using the
// JobID echoed by the server.
func (c *ChutesAdapter) SubmitWork(ctx context.Context, spec WorkSpec) (WorkResult, error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return WorkResult{}, err
	}
	url := c.BaseURL + "/v1/work"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return WorkResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return WorkResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkResult{}, fmt.Errorf("marketplaceadapter: chutes submit status %d", resp.StatusCode)
	}
	var decoded struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return WorkResult{}, err
	}
	return WorkResult{
		Marketplace: "chutes",
		JobID:       decoded.JobID,
		Accepted:    true,
	}, nil
}
