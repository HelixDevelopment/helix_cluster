package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChutesConfig configures a ChutesProvider.
type ChutesConfig struct {
	// BaseURL is the Chutes control-API root, e.g. "https://gpu.chutes.example".
	// Required; NewChutesProvider returns ErrNoEndpoint when empty.
	BaseURL string
	// Token, when non-empty, is sent as an "Authorization: Bearer" header on
	// every request.
	Token string
	// Timeout bounds each individual HTTP request. Zero means no per-request
	// timeout beyond the caller's context.
	Timeout time.Duration
	// HTTPClient lets tests inject a client; nil uses a default.
	HTTPClient *http.Client
}

// ChutesProvider is a reference Provider that rents GPU compute from a
// Chutes-style HTTP control API. The wire protocol is, per method:
//
//	GET    {BaseURL}/v1/gpu/offers              -> {"offers":[GPUOffer...]}
//	POST   {BaseURL}/v1/gpu/leases  {offer_id}  -> Lease (201/200)
//	DELETE {BaseURL}/v1/gpu/leases/{leaseID}    -> 204/200 (404 => not found)
//
// See the package HONEST EXTERNAL SEAM note: this talks the documented wire
// protocol; tests drive it against a local httptest emulator.
type ChutesProvider struct {
	base    string
	token   string
	timeout time.Duration
	hc      *http.Client
}

// compile-time proof the reference adapter satisfies the abstraction.
var _ Provider = (*ChutesProvider)(nil)

// NewChutesProvider builds a ChutesProvider from cfg. It returns ErrNoEndpoint
// if BaseURL is empty so a misconfigured adapter never silently no-ops.
func NewChutesProvider(cfg ChutesConfig) (*ChutesProvider, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, ErrNoEndpoint
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &ChutesProvider{
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		token:   cfg.Token,
		timeout: cfg.Timeout,
		hc:      hc,
	}, nil
}

// Name returns the stable provider label.
func (p *ChutesProvider) Name() string { return "chutes" }

// offersEnvelope is the GET /v1/gpu/offers response body.
type offersEnvelope struct {
	Offers []GPUOffer `json:"offers"`
}

// provisionRequest is the POST /v1/gpu/leases request body.
type provisionRequest struct {
	OfferID string `json:"offer_id"`
}

// ListOffers issues GET {BaseURL}/v1/gpu/offers and parses the JSON envelope
// into typed GPUOffers. A non-200 status surfaces an error rather than an empty
// slice, so a failing control plane is never read as "no GPUs for rent".
func (p *ChutesProvider) ListOffers(ctx context.Context) ([]GPUOffer, error) {
	raw, status, err := p.do(ctx, http.MethodGet, "/v1/gpu/offers", nil)
	if err != nil {
		return nil, fmt.Errorf("chutes: list offers: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("chutes: list offers: unexpected status %d", status)
	}
	var env offersEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("chutes: decode offers: %w", err)
	}
	return env.Offers, nil
}

// Provision POSTs {offer_id} to {BaseURL}/v1/gpu/leases and returns the Lease
// parsed FROM THE SERVER RESPONSE BODY — the lease id and endpoint are read
// off the wire, never fabricated locally. A non-2xx status returns the zero
// Lease wrapped in ErrProvisionFailed.
func (p *ChutesProvider) Provision(ctx context.Context, offerID string) (Lease, error) {
	if strings.TrimSpace(offerID) == "" {
		return Lease{}, ErrEmptyOfferID
	}
	body, err := json.Marshal(provisionRequest{OfferID: offerID})
	if err != nil {
		return Lease{}, fmt.Errorf("chutes: marshal provision: %w", err)
	}
	raw, status, err := p.do(ctx, http.MethodPost, "/v1/gpu/leases", body)
	if err != nil {
		return Lease{}, fmt.Errorf("chutes: provision: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return Lease{}, fmt.Errorf("chutes: provision offer %q: %w: status %d", offerID, ErrProvisionFailed, status)
	}
	var lease Lease
	if err := json.Unmarshal(raw, &lease); err != nil {
		return Lease{}, fmt.Errorf("chutes: decode lease: %w", err)
	}
	if lease.ID == "" {
		// A 2xx with no lease id is a broken server contract, not a usable lease.
		return Lease{}, fmt.Errorf("chutes: provision offer %q: %w: empty lease id in response", offerID, ErrProvisionFailed)
	}
	return lease, nil
}

// Terminate issues DELETE {BaseURL}/v1/gpu/leases/{leaseID} to release the
// lease. A 404 maps to ErrLeaseNotFound; any other non-2xx is an error. A
// missing/empty leaseID is rejected before any request is sent.
func (p *ChutesProvider) Terminate(ctx context.Context, leaseID string) error {
	if strings.TrimSpace(leaseID) == "" {
		return ErrEmptyLeaseID
	}
	path := "/v1/gpu/leases/" + leaseID
	_, status, err := p.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("chutes: terminate %q: %w", leaseID, err)
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("chutes: terminate %q: %w", leaseID, ErrLeaseNotFound)
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return fmt.Errorf("chutes: terminate %q: unexpected status %d", leaseID, status)
	}
	return nil
}

// do performs one HTTP request against the configured base URL, returning the
// raw body and status code. body may be nil for GET/DELETE.
func (p *ChutesProvider) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.hc.Do(req)
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
