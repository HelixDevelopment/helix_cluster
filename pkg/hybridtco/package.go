// Package hybridtco implements a Total-Cost-of-Ownership (TCO) calculator for a
// HYBRID own+burst economic model used by Helix Cluster OS capacity planning.
//
// Unlike pkg/local (which prices a single owned GPU in isolation), this package
// models a whole fleet that runs a fixed FRACTION of its work on hardware the
// operator already owns, and BURSTS the remaining fraction to a cloud provider
// at a quoted per-hour burst price. The planner needs one blended, deterministic
// $/hr figure for the fleet so it can size budgets and compare the hybrid policy
// against a pure-owned or pure-cloud policy.
//
// # Model
//
// Each owned GPU carries two cost components per hour:
//
//	ownedAmortizedHourly = CapexPerGPU / (LifespanMonths * HoursPerMonth * Utilization)
//	ownedPowerHourly      = (PowerWatts / 1000) * PowerPriceKWh
//	ownedHourly           = ownedAmortizedHourly + ownedPowerHourly
//
// The Utilization divisor in the amortized term is the core of the model: a
// card that is busy only a fraction of the time must recover its purchase price
// over the hours it actually does chargeable work. A HIGHER Utilization spreads
// the same capex over more worked hours and therefore LOWERS the amortized $/hr
// per unit of work (and a lower Utilization raises it). Power is charged per
// worked hour at the kWh tariff and is independent of Utilization.
//
// The fleet blends owned and burst by the burst fraction:
//
//	EffectiveHourly = (1 - BurstFraction) * ownedHourly + BurstFraction * BurstPricePerHr
//
// and the monthly bill is that blended rate run across the whole fleet for a
// month of operating hours:
//
//	MonthlyTCO = EffectiveHourly * GPUCount * HoursPerMonth
//
// When the burst price exceeds the owned hourly cost (the usual case — cloud
// on-demand is dearer than amortized owned hardware), increasing BurstFraction
// shifts weight onto the pricier term and raises both EffectiveHourly and
// MonthlyTCO. The direction is therefore documented and monotonic in the sign
// of (BurstPricePerHr - ownedHourly).
//
// HONEST EXTERNAL SEAM: every input is an operator-supplied planning fact
// (asset capex, power bill tariff, expected utilization, the burst contract's
// quoted rate and the policy's burst fraction). Nothing here is fetched from a
// live provider; the calculation is pure and deterministic over those facts.
package hybridtco

import (
	"errors"
	"fmt"
)

// HoursPerMonth is the operating-hour count used to convert a per-hour rate into
// a monthly bill: 730 = 8760 hours/year / 12 months, the conventional cloud
// billing month.
const HoursPerMonth = 730.0

// Validation errors.
var (
	// ErrInvalidConfig is returned by Validate (and the Checked* helpers) when a
	// Config field is out of its permitted range.
	ErrInvalidConfig = errors.New("hybridtco: invalid config")
)

// Config captures one hybrid fleet's economic profile. All monetary values are
// US dollars.
type Config struct {
	// GPUCount is the number of GPUs in the fleet. Must be >= 1.
	GPUCount int
	// CapexPerGPU is the up-front purchase price of ONE owned GPU, in dollars.
	// Must be > 0.
	CapexPerGPU float64
	// LifespanMonths is the depreciation horizon over which capex is recovered,
	// in months. Must be > 0.
	LifespanMonths float64
	// PowerWatts is one card's electrical draw while working, in watts. Must be
	// >= 0.
	PowerWatts float64
	// PowerPriceKWh is the utility tariff in dollars per kilowatt-hour. Must be
	// >= 0.
	PowerPriceKWh float64
	// Utilization is the fraction of operating hours a card spends doing
	// chargeable work, in (0, 1]. Amortized capex is spread over only these
	// worked hours, so higher Utilization lowers the amortized $/hr.
	Utilization float64
	// BurstFraction is the fraction of fleet work served by bursting to cloud,
	// in [0, 1]. The complementary (1 - BurstFraction) runs on owned hardware.
	BurstFraction float64
	// BurstPricePerHr is the cloud provider's quoted burst rate per GPU-hour, in
	// dollars. Must be >= 0.
	BurstPricePerHr float64
}

// Validate reports whether the Config fields are in range for a meaningful TCO.
func (c Config) Validate() error {
	switch {
	case c.GPUCount < 1:
		return fmt.Errorf("%w: gpu count %d < 1", ErrInvalidConfig, c.GPUCount)
	case c.CapexPerGPU <= 0:
		return fmt.Errorf("%w: capex %.2f <= 0", ErrInvalidConfig, c.CapexPerGPU)
	case c.LifespanMonths <= 0:
		return fmt.Errorf("%w: lifespan months %.4f <= 0", ErrInvalidConfig, c.LifespanMonths)
	case c.PowerWatts < 0:
		return fmt.Errorf("%w: power %.2fW < 0", ErrInvalidConfig, c.PowerWatts)
	case c.PowerPriceKWh < 0:
		return fmt.Errorf("%w: power price %.4f/kWh < 0", ErrInvalidConfig, c.PowerPriceKWh)
	case c.Utilization <= 0 || c.Utilization > 1:
		return fmt.Errorf("%w: utilization %.4f not in (0,1]", ErrInvalidConfig, c.Utilization)
	case c.BurstFraction < 0 || c.BurstFraction > 1:
		return fmt.Errorf("%w: burst fraction %.4f not in [0,1]", ErrInvalidConfig, c.BurstFraction)
	case c.BurstPricePerHr < 0:
		return fmt.Errorf("%w: burst price %.4f < 0", ErrInvalidConfig, c.BurstPricePerHr)
	default:
		return nil
	}
}

// OwnedAmortizedHourly returns the capital-recovery component of one owned card,
// in $/hr: CapexPerGPU / (LifespanMonths * HoursPerMonth * Utilization).
//
// Higher Utilization enlarges the denominator and so LOWERS this figure: the
// same capex is recovered over more worked hours. Callers must have validated
// the Config (Validate rules out the zero denominator).
func (c Config) OwnedAmortizedHourly() float64 {
	lifespanHours := c.LifespanMonths * HoursPerMonth
	return c.CapexPerGPU / (lifespanHours * c.Utilization)
}

// OwnedPowerHourly returns the electricity component of one owned card, in $/hr:
// (PowerWatts / 1000) * PowerPriceKWh. Watts/1000 is kilowatts; over one worked
// hour that is exactly the kWh consumed, priced at the tariff.
func (c Config) OwnedPowerHourly() float64 {
	return (c.PowerWatts / 1000.0) * c.PowerPriceKWh
}

// OwnedHourly returns the full per-card owned TCO in $/hr: amortized capital
// plus power.
func (c Config) OwnedHourly() float64 {
	return c.OwnedAmortizedHourly() + c.OwnedPowerHourly()
}

// EffectiveHourly returns the blended per-GPU $/hr of the hybrid policy:
//
//	(1 - BurstFraction) * OwnedHourly + BurstFraction * BurstPricePerHr
//
// It is a pure function of the Config. When BurstPricePerHr > OwnedHourly,
// raising BurstFraction raises this value (and vice-versa); at BurstFraction 0
// it equals OwnedHourly and at 1 it equals BurstPricePerHr.
func EffectiveHourly(cfg Config) float64 {
	owned := cfg.OwnedHourly()
	return (1.0-cfg.BurstFraction)*owned + cfg.BurstFraction*cfg.BurstPricePerHr
}

// MonthlyTCO returns the fleet's monthly bill in dollars:
//
//	EffectiveHourly(cfg) * GPUCount * HoursPerMonth
//
// i.e. the blended per-GPU rate run across every GPU for one month of operating
// hours.
func MonthlyTCO(cfg Config) float64 {
	return EffectiveHourly(cfg) * float64(cfg.GPUCount) * HoursPerMonth
}

// CheckedMonthlyTCO is MonthlyTCO with an up-front Validate, returning
// ErrInvalidConfig (wrapped) instead of producing a meaningless number from a
// malformed Config.
func CheckedMonthlyTCO(cfg Config) (float64, error) {
	if err := cfg.Validate(); err != nil {
		return 0, err
	}
	return MonthlyTCO(cfg), nil
}
