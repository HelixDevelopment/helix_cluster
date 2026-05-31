// Package local implements a registry of locally-owned GPUs with a
// Total-Cost-of-Ownership (TCO) model for Helix Cluster OS.
//
// A datacentre may own GPUs outright instead of (or alongside) renting cloud
// capacity. To decide whether to run a job locally or burst to a cloud spot
// offer, the scheduler needs an apples-to-apples effective $/hr for the local
// hardware. A bare "we already paid for it, so it's free" view is wrong: owned
// hardware still costs the amortised purchase price spread over its useful life
// AND the electricity it burns while running.
//
// The effective amortised $/hr TCO of a local GPU is therefore the sum of two
// terms:
//
//	amortised capital $/hr = price / (lifespanHours * utilization)
//	power           $/hr   = (watts / 1000) * pricePerKWh
//	effective       $/hr   = amortised capital $/hr + power $/hr
//
// The utilization divisor in the capital term is deliberate: a card that is
// only busy 70% of the time must recover its purchase price over the hours it
// actually does work, so a LOWER utilization RAISES the capital $/hr (and
// conversely higher utilization LOWERS it). This effective figure is directly
// comparable against a cloud offer's quoted $/hr.
//
// HONEST EXTERNAL SEAM: this package owns no live signal. Purchase price,
// lifespan, power draw, the local utility's $/kWh tariff and expected
// utilization are operator-supplied facts about owned hardware (asset
// inventory, the power bill), not values fetched from any provider API. There
// is therefore no provider/cloud round-trip to fake here; the "cloud" side
// enters only as a plain cloudHourly float in CheaperThanCloud, which callers
// obtain from the (separate, out-of-scope) cloud-offer subsystem. The model is
// a pure, deterministic cost calculation over registered facts.
package local

import (
	"errors"
	"fmt"
	"sync"
)

// HoursPerYear is the hour count used to convert a lifespan expressed in years
// into lifespan hours. 365 days * 24 hours = 8760; this is the conventional
// (non-leap) datacentre planning year.
const HoursPerYear = 365 * 24 // 8760

// Common registration / lookup errors.
var (
	// ErrUnknownGPU is returned by EffectiveHourlyCost / CheaperThanCloud when
	// the requested id was never registered.
	ErrUnknownGPU = errors.New("local: unknown gpu id")
	// ErrDuplicateID is returned by Register when the id is already taken.
	ErrDuplicateID = errors.New("local: duplicate gpu id")
	// ErrInvalidGPU is returned by Register when a field is out of range (e.g.
	// non-positive price, lifespan, or utilization > 1).
	ErrInvalidGPU = errors.New("local: invalid gpu specification")
)

// GPU describes one locally-owned GPU asset and the operator facts needed to
// compute its amortised TCO. All monetary values are US dollars.
type GPU struct {
	// ID uniquely identifies this GPU within a registrar.
	ID string
	// Model is a free-form hardware label (e.g. "NVIDIA H100 80GB"). It does
	// not affect the cost computation; it is carried for reporting/audit.
	Model string
	// Count is the number of physical cards this entry represents. The TCO is
	// reported per-card (see EffectiveHourlyCost); Count is retained for
	// fleet-level aggregation by callers and must be >= 1.
	Count int
	// PurchasePrice is the up-front capital cost of ONE card, in dollars. Must
	// be > 0.
	PurchasePrice float64
	// LifespanYears is the expected useful life of the card, in years. Must be
	// > 0. Combined with HoursPerYear it yields the lifespan-hours denominator.
	LifespanYears float64
	// PowerDrawWatts is the card's electrical draw while working, in watts.
	// Must be >= 0 (a passive accelerator could in principle be 0).
	PowerDrawWatts float64
	// PowerPriceKWh is the local utility tariff in dollars per kilowatt-hour.
	// Must be >= 0.
	PowerPriceKWh float64
	// Utilization is the expected fraction of wall-clock time the card spends
	// doing chargeable work, in (0, 1]. The capital cost is amortised over
	// only these busy hours, so lower utilization raises the capital $/hr.
	Utilization float64
}

// validate checks the GPU fields are in range for a meaningful TCO.
func (g GPU) validate() error {
	switch {
	case g.ID == "":
		return fmt.Errorf("%w: empty id", ErrInvalidGPU)
	case g.Count < 1:
		return fmt.Errorf("%w: count %d < 1", ErrInvalidGPU, g.Count)
	case g.PurchasePrice <= 0:
		return fmt.Errorf("%w: purchase price %.2f <= 0", ErrInvalidGPU, g.PurchasePrice)
	case g.LifespanYears <= 0:
		return fmt.Errorf("%w: lifespan years %.4f <= 0", ErrInvalidGPU, g.LifespanYears)
	case g.PowerDrawWatts < 0:
		return fmt.Errorf("%w: power draw %.2fW < 0", ErrInvalidGPU, g.PowerDrawWatts)
	case g.PowerPriceKWh < 0:
		return fmt.Errorf("%w: power price %.4f/kWh < 0", ErrInvalidGPU, g.PowerPriceKWh)
	case g.Utilization <= 0 || g.Utilization > 1:
		return fmt.Errorf("%w: utilization %.4f not in (0,1]", ErrInvalidGPU, g.Utilization)
	default:
		return nil
	}
}

// AmortizedCapitalHourly returns the capital-recovery component of the TCO for
// one card, in $/hr: price / (lifespanHours * utilization).
//
// It is a pure function of the GPU fields; the caller must have validated them
// (Register does). With a non-positive lifespan*utilization it would divide by
// zero, which validate() rules out.
func (g GPU) AmortizedCapitalHourly() float64 {
	lifespanHours := g.LifespanYears * HoursPerYear
	return g.PurchasePrice / (lifespanHours * g.Utilization)
}

// PowerHourly returns the electricity component of the TCO for one card, in
// $/hr: (watts / 1000) * pricePerKWh. Watts/1000 converts to kilowatts; over
// one hour that is exactly the kWh consumed, priced at the tariff.
func (g GPU) PowerHourly() float64 {
	return (g.PowerDrawWatts / 1000.0) * g.PowerPriceKWh
}

// EffectiveHourly returns the full per-card effective TCO in $/hr: the sum of
// the amortised capital and power components.
func (g GPU) EffectiveHourly() float64 {
	return g.AmortizedCapitalHourly() + g.PowerHourly()
}

// LocalGPURegistrar holds registered local GPUs and answers TCO queries. It is
// safe for concurrent use.
type LocalGPURegistrar struct {
	mu   sync.RWMutex
	gpus map[string]GPU
}

// NewLocalGPURegistrar returns an empty registrar ready for Register calls.
func NewLocalGPURegistrar() *LocalGPURegistrar {
	return &LocalGPURegistrar{gpus: make(map[string]GPU)}
}

// Register adds a locally-owned GPU. It returns ErrInvalidGPU if any field is
// out of range, or ErrDuplicateID if the id is already registered. On success
// the GPU is stored and its TCO becomes queryable.
func (r *LocalGPURegistrar) Register(gpu GPU) error {
	if err := gpu.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.gpus[gpu.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateID, gpu.ID)
	}
	r.gpus[gpu.ID] = gpu
	return nil
}

// lookup returns a copy of the registered GPU and whether it exists.
func (r *LocalGPURegistrar) lookup(id string) (GPU, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.gpus[id]
	return g, ok
}

// EffectiveHourlyCost returns the per-card effective amortised $/hr TCO of the
// registered GPU id (amortised capital + power). It returns ErrUnknownGPU if
// the id was never registered.
func (r *LocalGPURegistrar) EffectiveHourlyCost(id string) (float64, error) {
	g, ok := r.lookup(id)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownGPU, id)
	}
	return g.EffectiveHourly(), nil
}

// CheaperThanCloud reports whether running this local GPU is strictly cheaper
// than a cloud offer quoting cloudHourly $/hr — i.e. whether the local
// effective TCO is < cloudHourly. It returns ErrUnknownGPU if the id was never
// registered.
//
// The comparison is strict: a local TCO exactly equal to the cloud rate is NOT
// "cheaper" and yields false. This is the boundary the scheduler relies on to
// avoid churning to the cloud for a tie.
func (r *LocalGPURegistrar) CheaperThanCloud(id string, cloudHourly float64) (bool, error) {
	local, err := r.EffectiveHourlyCost(id)
	if err != nil {
		return false, err
	}
	return local < cloudHourly, nil
}
