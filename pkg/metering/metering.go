// Package metering implements a deterministic, pure-Go marketplace metering
// lifecycle: a provider Registers (proving a capability via a supplied
// predicate), Claims a single Bounty, serves METERED invocations (each
// recording compute units), and is then Paid out.
//
// Payout = bountyReward + sum(meteredUnits)*ratePerUnit.
//
// The lifecycle is strictly enforced:
//   - a provider cannot claim a bounty without first registering,
//   - a provider cannot meter without first claiming a bounty,
//   - registration with an unproven capability is rejected.
//
// The package is self-contained (its own types) and does not depend on
// pkg/marketplace. It performs no I/O, no networking and reads no host state;
// all behaviour is a pure function of method inputs and accumulated state.
package metering

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by Market. Callers may match these with errors.Is.
var (
	// ErrCapabilityNotProven is returned by Register when capOK is false.
	ErrCapabilityNotProven = errors.New("metering: provider capability not proven")
	// ErrNotRegistered is returned when an operation requires a registered
	// provider but none exists.
	ErrNotRegistered = errors.New("metering: provider not registered")
	// ErrAlreadyRegistered is returned by Register for a provider that has
	// already registered.
	ErrAlreadyRegistered = errors.New("metering: provider already registered")
	// ErrNoBountyClaimed is returned when metering is attempted before a
	// bounty has been claimed.
	ErrNoBountyClaimed = errors.New("metering: no bounty claimed")
	// ErrBountyAlreadyClaimed is returned by ClaimBounty when the provider has
	// already claimed a bounty (single claim per provider).
	ErrBountyAlreadyClaimed = errors.New("metering: bounty already claimed")
	// ErrEmptyBountyID is returned by ClaimBounty when given an empty bounty id.
	ErrEmptyBountyID = errors.New("metering: empty bounty id")
	// ErrNonPositiveUnits is returned by Meter when units <= 0.
	ErrNonPositiveUnits = errors.New("metering: units must be positive")
)

// provider holds the accumulated lifecycle state for a single provider.
type provider struct {
	registered bool
	bountyID   string // non-empty once a bounty is claimed
	claimed    bool
	totalUnits int64 // exact sum of metered units
}

// Market is a deterministic in-memory marketplace metering ledger. The zero
// value is not usable; construct one with NewMarket. Market is not safe for
// concurrent use.
type Market struct {
	providers map[string]*provider
}

// NewMarket returns an empty Market ready for provider registration.
func NewMarket() *Market {
	return &Market{providers: make(map[string]*provider)}
}

// Register records providerID as an available provider. The capability is
// considered proven iff capOK is true (the predicate is evaluated by the
// caller and its result supplied here). Registering an already-registered
// provider, or registering with capOK=false, returns a typed error and does
// not mutate state.
func (m *Market) Register(providerID string, capOK bool) error {
	if providerID == "" {
		return fmt.Errorf("%w: empty provider id", ErrNotRegistered)
	}
	if _, ok := m.providers[providerID]; ok {
		return fmt.Errorf("%w: %q", ErrAlreadyRegistered, providerID)
	}
	if !capOK {
		return fmt.Errorf("%w: %q", ErrCapabilityNotProven, providerID)
	}
	m.providers[providerID] = &provider{registered: true}
	return nil
}

// ClaimBounty binds bountyID to a registered provider. A provider may claim at
// most one bounty. Claiming without registering, or claiming a second bounty,
// returns a typed error and does not mutate state.
func (m *Market) ClaimBounty(providerID, bountyID string) error {
	p, ok := m.providers[providerID]
	if !ok || !p.registered {
		return fmt.Errorf("%w: %q", ErrNotRegistered, providerID)
	}
	if bountyID == "" {
		return ErrEmptyBountyID
	}
	if p.claimed {
		return fmt.Errorf("%w: %q already holds %q", ErrBountyAlreadyClaimed, providerID, p.bountyID)
	}
	p.claimed = true
	p.bountyID = bountyID
	return nil
}

// Meter records units of compute consumed by a metered invocation. The
// provider must have registered and claimed a bounty; otherwise a typed error
// is returned and no usage accrues. units must be strictly positive.
func (m *Market) Meter(providerID string, units int) error {
	p, ok := m.providers[providerID]
	if !ok || !p.registered {
		return fmt.Errorf("%w: %q", ErrNotRegistered, providerID)
	}
	if !p.claimed {
		return fmt.Errorf("%w: %q", ErrNoBountyClaimed, providerID)
	}
	if units <= 0 {
		return fmt.Errorf("%w: %d", ErrNonPositiveUnits, units)
	}
	p.totalUnits += int64(units)
	return nil
}

// TotalUnits returns the exact summed metered units for providerID, or 0 if the
// provider is unknown.
func (m *Market) TotalUnits(providerID string) int64 {
	if p, ok := m.providers[providerID]; ok {
		return p.totalUnits
	}
	return 0
}

// Payout computes the amount owed to providerID:
//
//	bountyReward + sum(meteredUnits)*ratePerUnit
//
// If the provider is unknown, or has not claimed a bounty, the payout is 0
// (no bounty reward and no metered usage accrues). The computation is a pure
// function of the accumulated metered units and the supplied rates.
func (m *Market) Payout(providerID string, ratePerUnit, bountyReward float64) float64 {
	p, ok := m.providers[providerID]
	if !ok || !p.claimed {
		return 0
	}
	return bountyReward + float64(p.totalUnits)*ratePerUnit
}
