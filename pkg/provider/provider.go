// Package provider defines a GPU-rental Provider abstraction for Helix Cluster
// OS and ships a Chutes reference adapter that talks an HTTP control API over
// net/http.
//
// SCOPE: this is NOT inference. A Provider rents raw GPU compute — it lists
// purchasable GPU offers (gpu type, $/hr, region, availability), provisions a
// lease against a chosen offer, and terminates that lease. Higher layers
// (scheduler, marketplace) consume the typed offers and leases; this package
// owns only the rent/return lifecycle over the wire.
//
// HONEST EXTERNAL SEAM: the live Chutes GPU-rental service is OUT OF SCOPE for
// this package. ChutesProvider speaks a concrete JSON/HTTP wire protocol
// (documented on each method) against a configurable BaseURL + bearer Token. It
// is exercised end to end in tests against a LOCAL net/http/httptest server that
// emulates list/provision/terminate; pointing BaseURL at the real Chutes
// endpoint with a real token is the only change needed to drive production. No
// real cloud, container, or network beyond the injected endpoint is contacted.
package provider

import (
	"context"
	"errors"
)

// Sentinel errors let callers branch on the failure class with errors.Is.
var (
	// ErrNoEndpoint is returned when a provider is built without a BaseURL so a
	// misconfigured adapter never silently no-ops.
	ErrNoEndpoint = errors.New("provider: no endpoint configured")
	// ErrEmptyOfferID is returned by Provision when called with an empty offerID.
	ErrEmptyOfferID = errors.New("provider: empty offer id")
	// ErrEmptyLeaseID is returned by Terminate when called with an empty leaseID.
	ErrEmptyLeaseID = errors.New("provider: empty lease id")
	// ErrLeaseNotFound is returned when the server reports the lease does not
	// exist (HTTP 404 on terminate). It wraps so errors.Is can detect it.
	ErrLeaseNotFound = errors.New("provider: lease not found")
	// ErrProvisionFailed is returned when a provision request is refused by the
	// server (any non-2xx); the returned lease is the zero value, never a
	// fabricated one.
	ErrProvisionFailed = errors.New("provider: provision failed")
)

// GPUOffer is a single purchasable GPU-rental offer advertised by a provider.
// It is the typed result of ListOffers — every field is parsed from the
// provider response, never invented.
type GPUOffer struct {
	// ID is the provider-scoped offer identifier passed back to Provision.
	ID string `json:"id"`
	// GPUType is the accelerator model, e.g. "H100", "A100-80G".
	GPUType string `json:"gpu_type"`
	// PricePerHour is the rental cost in USD per hour.
	PricePerHour float64 `json:"price_per_hour"`
	// Region is the provider region/zone the GPU lives in, e.g. "us-east-1".
	Region string `json:"region"`
	// Available is how many units of this offer can be provisioned right now.
	Available int `json:"available"`
}

// Lease is an active GPU rental obtained from Provision. Endpoint is the
// reachable address of the rented machine (the thing a workload connects to).
type Lease struct {
	// ID is the provider lease identifier, required for Terminate.
	ID string `json:"id"`
	// OfferID is the offer this lease was provisioned from.
	OfferID string `json:"offer_id"`
	// Endpoint is the reachable address of the leased GPU machine.
	Endpoint string `json:"endpoint"`
}

// Provider is the GPU-rental abstraction. Implementations rent raw GPU compute
// from a backing marketplace/cloud.
type Provider interface {
	// Name returns a stable provider label used in logs and selection.
	Name() string
	// ListOffers returns the currently purchasable GPU offers.
	ListOffers(ctx context.Context) ([]GPUOffer, error)
	// Provision rents the GPU described by offerID and returns the active Lease.
	Provision(ctx context.Context, offerID string) (Lease, error)
	// Terminate releases the lease identified by leaseID.
	Terminate(ctx context.Context, leaseID string) error
}
