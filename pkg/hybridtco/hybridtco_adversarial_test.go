package hybridtco

import (
	"errors"
	"math"
	"testing"
)

// This file adversarially pins the hybrid-TCO economic model's MONOTONICITY and
// ACCOUNTING invariants with fine-grid sweeps (the centerpiece) plus exact
// component-decomposition and boundary probes. Each test is built so that a
// directional/sign mutation, an inverted ratio, a swapped numerator/denominator,
// or a dropped/double-counted cost component in package.go makes it FAIL.

const advEps = 1e-9

// adversarialBase is a clean, hand-verifiable config. Values chosen so the
// component arithmetic divides out exactly where convenient.
func adversarialBase() Config {
	return Config{
		GPUCount:        10,
		CapexPerGPU:     7300.0, // chosen so amort math is tidy with 730 hrs/mo
		LifespanMonths:  10.0,
		PowerWatts:      400.0,
		PowerPriceKWh:   0.10,
		Utilization:     0.5,
		BurstFraction:   0.25,
		BurstPricePerHr: 2.00,
	}
}

// ----------------------------------------------------------------------------
// MONOTONICITY (centerpiece): fine-grid sweeps, every adjacent pair checked.
// ----------------------------------------------------------------------------

// fineGrid returns n+1 points evenly spaced over [lo,hi] inclusive.
func fineGrid(lo, hi float64, n int) []float64 {
	out := make([]float64, 0, n+1)
	for i := 0; i <= n; i++ {
		out = append(out, lo+(hi-lo)*float64(i)/float64(n))
	}
	return out
}

// TestUtilizationFineGridStrictlyDecreasesAmortized sweeps Utilization across a
// fine grid in (0,1] and asserts OwnedAmortizedHourly STRICTLY decreases across
// EVERY adjacent pair. A coarse endpoint test would miss a mid-range dip; an
// inverted ratio (Util in the numerator) or a dropped Util divisor flips/flattens
// this and fails here.
func TestUtilizationFineGridStrictlyDecreasesAmortized(t *testing.T) {
	id := runUUID(t)
	base := adversarialBase()

	// Avoid exactly 0; sweep up to 1.0 inclusive.
	grid := fineGrid(0.02, 1.0, 200)
	prevAmort := math.Inf(1)
	prevOwned := math.Inf(1)
	prevMonthly := math.Inf(1)
	for _, u := range grid {
		cfg := base
		cfg.Utilization = u
		amort := cfg.OwnedAmortizedHourly()
		owned := cfg.OwnedHourly()
		monthly := MonthlyTCO(cfg)

		if amort >= prevAmort-advEps {
			t.Fatalf("run=%s amortized not strictly decreasing at u=%.5f: %.9f >= prev %.9f",
				id, u, amort, prevAmort)
		}
		// Owned hourly = amort + (Util-independent) power, so it too strictly falls.
		if owned >= prevOwned-advEps {
			t.Fatalf("run=%s owned hourly not strictly decreasing at u=%.5f: %.9f >= prev %.9f",
				id, u, owned, prevOwned)
		}
		// MonthlyTCO depends on Util only through the owned portion; with bf<1 it is
		// strictly decreasing in Util too.
		if monthly >= prevMonthly-advEps {
			t.Fatalf("run=%s MonthlyTCO not strictly decreasing in util at u=%.5f: %.6f >= prev %.6f",
				id, u, monthly, prevMonthly)
		}
		prevAmort, prevOwned, prevMonthly = amort, owned, monthly
	}
	t.Logf("run=%s util fine-grid (%d pts) amort/owned/monthly all strictly decreasing", id, len(grid))
}

// TestBurstFractionFineGridDirectional sweeps BurstFraction across a fine grid in
// [0,1] under three regimes and asserts the documented direction holds on EVERY
// adjacent pair:
//   - burst price > owned  => strictly increasing
//   - burst price < owned  => strictly decreasing
//   - burst price == owned => flat (constant)
//
// A sign flip in the blend (e.g. BurstFraction*owned + (1-bf)*burst) breaks at
// least one regime; a dropped burst term flattens all three (the >/< regimes
// fail).
func TestBurstFractionFineGridDirectional(t *testing.T) {
	id := runUUID(t)
	base := adversarialBase()
	owned := base.OwnedHourly()
	grid := fineGrid(0.0, 1.0, 200)

	type regime struct {
		name      string
		burst     float64
		direction int // +1 increasing, -1 decreasing, 0 flat
	}
	regimes := []regime{
		{"burst-dearer", owned + 1.0, +1},
		{"burst-cheaper", owned / 2.0, -1},
		{"burst-equal", owned, 0},
	}

	for _, r := range regimes {
		cfgBase := base
		cfgBase.BurstPricePerHr = r.burst
		prevEff := math.NaN()
		prevMonthly := math.NaN()
		for i, bf := range grid {
			cfg := cfgBase
			cfg.BurstFraction = bf
			eff := EffectiveHourly(cfg)
			monthly := MonthlyTCO(cfg)
			if i > 0 {
				deff := eff - prevEff
				dmon := monthly - prevMonthly
				switch r.direction {
				case +1:
					if deff <= advEps || dmon <= advEps {
						t.Fatalf("run=%s [%s] not increasing at bf=%.5f: deff=%.3g dmon=%.3g",
							id, r.name, bf, deff, dmon)
					}
				case -1:
					if deff >= -advEps || dmon >= -advEps {
						t.Fatalf("run=%s [%s] not decreasing at bf=%.5f: deff=%.3g dmon=%.3g",
							id, r.name, bf, deff, dmon)
					}
				case 0:
					if math.Abs(deff) > advEps || math.Abs(dmon) > advEps {
						t.Fatalf("run=%s [%s] not flat at bf=%.5f: deff=%.3g dmon=%.3g",
							id, r.name, bf, deff, dmon)
					}
				}
			}
			prevEff, prevMonthly = eff, monthly
		}
		t.Logf("run=%s [%s] fine-grid direction=%+d held across %d pts (owned=%.6f burst=%.6f)",
			id, r.name, r.direction, len(grid), owned, r.burst)
	}
}

// ----------------------------------------------------------------------------
// ACCOUNTING: total == sum of parts, no component dropped or double-counted.
// ----------------------------------------------------------------------------

// TestAccountingDecomposition pins, with hand-computed round numbers, that:
//   - OwnedHourly == OwnedAmortizedHourly + OwnedPowerHourly  (no drop/double)
//   - EffectiveHourly == (1-bf)*owned + bf*burst              (exact blend weights)
//   - MonthlyTCO == EffectiveHourly * count * HoursPerMonth   (no extra factor)
//
// The hand figures are computed from raw inputs, independent of package methods.
func TestAccountingDecomposition(t *testing.T) {
	id := runUUID(t)
	cfg := adversarialBase()

	// Hand-computed from raw inputs.
	// amort = 7300 / (10 * 730 * 0.5) = 7300 / 3650 = 2.0 exactly.
	wantAmort := cfg.CapexPerGPU / (cfg.LifespanMonths * HoursPerMonth * cfg.Utilization)
	// power = (400/1000)*0.10 = 0.04 exactly.
	wantPower := (cfg.PowerWatts / 1000.0) * cfg.PowerPriceKWh
	wantOwned := wantAmort + wantPower // 2.04
	// blend with bf=0.25, burst=2.00: 0.75*2.04 + 0.25*2.00 = 1.53 + 0.50 = 2.03.
	wantEff := (1.0-cfg.BurstFraction)*wantOwned + cfg.BurstFraction*cfg.BurstPricePerHr
	wantMonthly := wantEff * float64(cfg.GPUCount) * HoursPerMonth

	// Spot-check the load-bearing round numbers so a silent formula change is caught
	// even if the package's own helpers were mutated in lockstep.
	if math.Abs(wantAmort-2.0) > 1e-9 || math.Abs(wantPower-0.04) > 1e-9 ||
		math.Abs(wantOwned-2.04) > 1e-9 || math.Abs(wantEff-2.03) > 1e-9 {
		t.Fatalf("run=%s sanity arithmetic drifted: amort=%.9f power=%.9f owned=%.9f eff=%.9f",
			id, wantAmort, wantPower, wantOwned, wantEff)
	}

	gotAmort := cfg.OwnedAmortizedHourly()
	gotPower := cfg.OwnedPowerHourly()
	gotOwned := cfg.OwnedHourly()
	gotEff := EffectiveHourly(cfg)
	gotMonthly := MonthlyTCO(cfg)

	t.Logf("run=%s amort=%.9f power=%.9f owned=%.9f eff=%.9f monthly=%.6f",
		id, gotAmort, gotPower, gotOwned, gotEff, gotMonthly)

	// OwnedHourly is EXACTLY the sum of its two parts (catches drop/double-count).
	if math.Abs(gotOwned-(gotAmort+gotPower)) > 1e-12 {
		t.Fatalf("run=%s OwnedHourly %.12f != amort+power %.12f", id, gotOwned, gotAmort+gotPower)
	}
	if math.Abs(gotAmort-wantAmort) > 1e-9 {
		t.Fatalf("run=%s amort %.9f != hand %.9f", id, gotAmort, wantAmort)
	}
	if math.Abs(gotPower-wantPower) > 1e-12 {
		t.Fatalf("run=%s power %.12f != hand %.12f", id, gotPower, wantPower)
	}
	if math.Abs(gotOwned-wantOwned) > 1e-9 {
		t.Fatalf("run=%s owned %.9f != hand %.9f", id, gotOwned, wantOwned)
	}
	if math.Abs(gotEff-wantEff) > 1e-9 {
		t.Fatalf("run=%s eff %.9f != hand %.9f", id, gotEff, wantEff)
	}
	if math.Abs(gotMonthly-wantMonthly) > 1e-6 {
		t.Fatalf("run=%s monthly %.6f != hand %.6f", id, gotMonthly, wantMonthly)
	}
}

// TestEffectiveHourlyConvexWeights pins that the blend uses complementary weights
// summing to 1: across a fine bf grid, EffectiveHourly stays within [min,max] of
// {owned,burst} and equals the exact convex combination. A swapped-weight bug
// (bf on owned, 1-bf on burst) is caught by comparing against the convex value at
// every grid point.
func TestEffectiveHourlyConvexWeights(t *testing.T) {
	id := runUUID(t)
	base := adversarialBase()
	owned := base.OwnedHourly()
	burst := base.BurstPricePerHr // 2.00; owned=2.04, so burst < owned in this base.
	lo := math.Min(owned, burst)
	hi := math.Max(owned, burst)

	for _, bf := range fineGrid(0.0, 1.0, 100) {
		cfg := base
		cfg.BurstFraction = bf
		eff := EffectiveHourly(cfg)
		// Convex combination with the CORRECT weighting.
		want := (1.0-bf)*owned + bf*burst
		if math.Abs(eff-want) > advEps {
			t.Fatalf("run=%s bf=%.4f eff=%.9f != convex %.9f", id, bf, eff, want)
		}
		// Must lie within the hull of the two endpoints.
		if eff < lo-advEps || eff > hi+advEps {
			t.Fatalf("run=%s bf=%.4f eff=%.9f outside [%.9f,%.9f]", id, bf, eff, lo, hi)
		}
	}
	t.Logf("run=%s convex blend verified across grid (owned=%.6f burst=%.6f)", id, owned, burst)
}

// TestMonthlyTCOLinearInGPUCount pins that MonthlyTCO scales exactly linearly
// with GPUCount (the per-GPU rate is multiplied by count, not e.g. squared or
// off-by-one). Doubling the count must exactly double the bill.
func TestMonthlyTCOLinearInGPUCount(t *testing.T) {
	id := runUUID(t)
	base := adversarialBase()
	c1 := base
	c1.GPUCount = 1
	per := MonthlyTCO(c1)
	for _, n := range []int{1, 2, 5, 10, 37, 100, 1000} {
		cfg := base
		cfg.GPUCount = n
		got := MonthlyTCO(cfg)
		want := per * float64(n)
		if math.Abs(got-want) > 1e-6*math.Max(1, want) {
			t.Fatalf("run=%s n=%d monthly=%.6f != per*n %.6f", id, n, got, want)
		}
	}
	t.Logf("run=%s monthly linear in GPUCount (per-gpu=%.6f)", id, per)
}

// ----------------------------------------------------------------------------
// BOUNDARY probes.
// ----------------------------------------------------------------------------

// TestBoundaryAnchors checks the exact endpoint identities and degenerate inputs.
func TestBoundaryAnchors(t *testing.T) {
	id := runUUID(t)
	base := adversarialBase()
	owned := base.OwnedHourly()

	// bf = 0 => EffectiveHourly == OwnedHourly (burst term must vanish).
	z := base
	z.BurstFraction = 0
	if math.Abs(EffectiveHourly(z)-owned) > advEps {
		t.Fatalf("run=%s bf=0 eff=%.9f != owned %.9f", id, EffectiveHourly(z), owned)
	}
	// bf = 1 => EffectiveHourly == BurstPricePerHr (owned term must vanish).
	f := base
	f.BurstFraction = 1
	if math.Abs(EffectiveHourly(f)-base.BurstPricePerHr) > advEps {
		t.Fatalf("run=%s bf=1 eff=%.9f != burst %.9f", id, EffectiveHourly(f), base.BurstPricePerHr)
	}

	// Utilization = 1 => amortized over ALL operating hours = capex/(lifespan*hpm).
	u1 := base
	u1.Utilization = 1.0
	wantU1 := u1.CapexPerGPU / (u1.LifespanMonths * HoursPerMonth)
	if math.Abs(u1.OwnedAmortizedHourly()-wantU1) > 1e-9 {
		t.Fatalf("run=%s util=1 amort=%.9f != %.9f", id, u1.OwnedAmortizedHourly(), wantU1)
	}

	// Zero power => power component is exactly 0, owned == amort.
	p0 := base
	p0.PowerWatts = 0
	if p0.OwnedPowerHourly() != 0 {
		t.Fatalf("run=%s zero watts power=%.12f != 0", id, p0.OwnedPowerHourly())
	}
	if math.Abs(p0.OwnedHourly()-p0.OwnedAmortizedHourly()) > 1e-12 {
		t.Fatalf("run=%s zero power: owned %.12f != amort %.12f", id, p0.OwnedHourly(), p0.OwnedAmortizedHourly())
	}

	// Zero burst price with bf=1 => EffectiveHourly == 0 and MonthlyTCO == 0.
	zb := base
	zb.BurstPricePerHr = 0
	zb.BurstFraction = 1
	if EffectiveHourly(zb) != 0 {
		t.Fatalf("run=%s bf=1 burst=0 eff=%.12f != 0", id, EffectiveHourly(zb))
	}
	if MonthlyTCO(zb) != 0 {
		t.Fatalf("run=%s bf=1 burst=0 monthly=%.12f != 0", id, MonthlyTCO(zb))
	}
	t.Logf("run=%s boundary anchors hold", id)
}

// TestValidateFailClosedAndConsistent confirms Validate fails closed on each
// out-of-range field and accepts the in-range boundary values (util=1, bf in
// {0,1}, zero power/tariff/burst). CheckedMonthlyTCO must mirror Validate exactly.
func TestValidateFailClosedAndConsistent(t *testing.T) {
	id := runUUID(t)

	// Accepted boundary cases.
	accept := []struct {
		name string
		mut  func(c *Config)
	}{
		{"util=1", func(c *Config) { c.Utilization = 1.0 }},
		{"bf=0", func(c *Config) { c.BurstFraction = 0 }},
		{"bf=1", func(c *Config) { c.BurstFraction = 1 }},
		{"zero power", func(c *Config) { c.PowerWatts = 0 }},
		{"zero tariff", func(c *Config) { c.PowerPriceKWh = 0 }},
		{"zero burst price", func(c *Config) { c.BurstPricePerHr = 0 }},
		{"one gpu", func(c *Config) { c.GPUCount = 1 }},
	}
	for _, tc := range accept {
		c := adversarialBase()
		tc.mut(&c)
		if err := c.Validate(); err != nil {
			t.Fatalf("run=%s accept %q rejected: %v", id, tc.name, err)
		}
		if _, err := CheckedMonthlyTCO(c); err != nil {
			t.Fatalf("run=%s accept %q CheckedMonthlyTCO err: %v", id, tc.name, err)
		}
	}

	// Rejected cases, including the just-outside boundary values.
	reject := []struct {
		name string
		mut  func(c *Config)
	}{
		{"util just over 1", func(c *Config) { c.Utilization = 1.0 + 1e-9 }},
		{"util 0", func(c *Config) { c.Utilization = 0 }},
		{"util neg", func(c *Config) { c.Utilization = -0.1 }},
		{"bf just over 1", func(c *Config) { c.BurstFraction = 1.0 + 1e-9 }},
		{"bf neg", func(c *Config) { c.BurstFraction = -1e-9 }},
		{"zero gpu", func(c *Config) { c.GPUCount = 0 }},
		{"neg gpu", func(c *Config) { c.GPUCount = -5 }},
		{"capex 0", func(c *Config) { c.CapexPerGPU = 0 }},
		{"lifespan 0", func(c *Config) { c.LifespanMonths = 0 }},
		{"neg power", func(c *Config) { c.PowerWatts = -1 }},
		{"neg tariff", func(c *Config) { c.PowerPriceKWh = -1e-9 }},
		{"neg burst", func(c *Config) { c.BurstPricePerHr = -1e-9 }},
	}
	for _, tc := range reject {
		c := adversarialBase()
		tc.mut(&c)
		err := c.Validate()
		if err == nil {
			t.Fatalf("run=%s reject %q: Validate accepted invalid config", id, tc.name)
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("run=%s reject %q: err %v not ErrInvalidConfig", id, tc.name, err)
		}
		if _, cerr := CheckedMonthlyTCO(c); cerr == nil {
			t.Fatalf("run=%s reject %q: CheckedMonthlyTCO accepted invalid config", id, tc.name)
		}
	}
	t.Logf("run=%s validate fail-closed: %d accepted, %d rejected", id, len(accept), len(reject))
}
