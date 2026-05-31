// Package marketplace implements a GPU/inference marketplace adapter layer and
// a composite scorer for Helix Cluster OS.
//
// The cluster can rent GPU/inference capacity from external marketplaces (e.g.
// Chutes and other confidential-compute providers). Each marketplace exposes a
// set of Offers — a price, an availability percentage, a latency, a throughput,
// and whether the capacity runs inside a Trusted Execution Environment (TEE /
// confidential compute). The scheduler needs to pick the BEST offer for a job
// according to a tunable, weighted preference (cheap vs fast vs reliable) and,
// when a job carries confidential data, to prefer TEE-backed offers.
//
// This package provides three things:
//
//   - Adapter: the seam over a marketplace. Name() identifies it; ListOffers
//     returns its current Offers. ChutesAdapter is a reference Adapter that
//     reads from an injectable OfferSource (a fake in tests; a real Chutes API
//     client in production — both satisfy the same OfferSource interface).
//
//   - CompositeScorer: ranks Offers by a weighted, per-dimension-normalised
//     score with a TEE preference multiplier applied when a job requires TEE.
//     Rank returns ScoredOffers sorted best-first.
//
//   - Encryptor / Attestor: stdlib reference seams for the confidential path.
//     The reference implementations actually round-trip (AES-GCM) and actually
//     verify (HMAC) so tests prove real crypto behaviour. The production
//     digital.vasic.security submodule (PQ E2EE / proof-of-GPU attestation)
//     plugs in behind these exact interfaces without changing callers.
//
//   - TokenStream: a real SSE (text/event-stream) decoder for the streamed
//     token path marketplaces like Chutes use. It yields generated tokens in the
//     exact order the upstream emitted them and stops cleanly at the [DONE]
//     sentinel (see stream.go).
//
//   - GPUReservationTable: per-class GPU capacity accounting that REALLY denies
//     cross-class over-use — a reservation can only draw from its own class's
//     pool and never exceeds that class's total, even under concurrency (see
//     reservation.go).
//
// HONEST EXTERNAL SEAM: no network/provider call lives in this package. Live
// marketplace I/O is modelled behind OfferSource; live confidential crypto is
// modelled behind Encryptor/Attestor. Tests use software references and a
// local httptest server only — never a real provider, never a container.
package marketplace

import (
	"context"
	"errors"
	"fmt"
)

// Offer is a single rentable unit of GPU/inference capacity advertised by a
// marketplace. All four scored dimensions are present; Confidential flags
// whether the capacity executes inside a TEE.
type Offer struct {
	// ID uniquely identifies the offer within its marketplace.
	ID string
	// Provider names the marketplace/adapter the offer came from.
	Provider string
	// PricePerHour is the rental cost in USD per hour. Lower is better.
	PricePerHour float64
	// AvailabilityPct is the advertised availability, 0..100. Higher is better.
	AvailabilityPct float64
	// LatencyMS is the request latency in milliseconds. Lower is better.
	LatencyMS float64
	// ThroughputTokS is generation throughput in tokens/second. Higher is better.
	ThroughputTokS float64
	// Confidential is true when the capacity runs inside a TEE / confidential
	// VM. Jobs that require TEE prefer (and may mandate) such offers.
	Confidential bool
}

// Adapter is the seam over one GPU/inference marketplace.
type Adapter interface {
	// Name identifies the marketplace (e.g. "chutes"). Must be non-empty.
	Name() string
	// ListOffers returns the marketplace's currently advertised offers.
	ListOffers(ctx context.Context) ([]Offer, error)
}

// OfferSource is the injectable backend behind an Adapter. The fake source in
// tests and a real Chutes HTTP client both implement it; the Adapter never
// knows which it is talking to.
type OfferSource interface {
	// Fetch returns the raw offers from the marketplace backend.
	Fetch(ctx context.Context) ([]Offer, error)
}

// ChutesAdapter is the reference Adapter. It stamps each returned offer's
// Provider with its name and delegates the actual fetch to an OfferSource.
//
// In production the OfferSource is a real Chutes API client (HTTP/JSON against
// the Chutes marketplace). In tests it is a fake or a local httptest-backed
// client. The adapter logic is identical either way.
type ChutesAdapter struct {
	// AdapterName is the Name() reported by this adapter. Defaults to "chutes".
	AdapterName string
	// Source supplies the raw offers. Required.
	Source OfferSource
}

// ErrNoSource is returned by ListOffers when no OfferSource was configured.
var ErrNoSource = errors.New("marketplace: adapter has no offer source")

// Name implements Adapter.
func (c *ChutesAdapter) Name() string {
	if c.AdapterName == "" {
		return "chutes"
	}
	return c.AdapterName
}

// ListOffers implements Adapter. It fetches from the source and stamps the
// Provider field of every offer with this adapter's Name so downstream ranking
// and audit can attribute each offer to its marketplace. The source's error is
// wrapped, not swallowed.
func (c *ChutesAdapter) ListOffers(ctx context.Context) ([]Offer, error) {
	if c.Source == nil {
		return nil, ErrNoSource
	}
	raw, err := c.Source.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: list offers: %w", c.Name(), err)
	}
	out := make([]Offer, len(raw))
	for i, o := range raw {
		o.Provider = c.Name()
		out[i] = o
	}
	return out, nil
}
