package hybridtco

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"testing"
)

// runUUID returns a random hex id so each run's logs are correlatable.
func runUUID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

const eps = 1e-9

// documentedConfig is the worked 100-GPU hybrid profile referenced by clause 1.
// Every field is a planning fact; the expected TCO in TestDocumentedMonthlyTCO
// is DERIVED from these via the package formula, not hardcoded.
func documentedConfig() Config {
	return Config{
		GPUCount:        100,
		CapexPerGPU:     2500.0, // $/card
		LifespanMonths:  48.0,   // 4-year depreciation
		PowerWatts:      300.0,  // W while working
		PowerPriceKWh:   0.08,   // $/kWh utility tariff
		Utilization:     0.7,    // 70% of operating hours worked
		BurstFraction:   0.08,   // 8% of work bursts to cloud
		BurstPricePerHr: 0.90,   // $/GPU-hr cloud burst rate
	}
}

// Clause 1: for the documented 100-GPU config, MonthlyTCO equals an
// independently hand-computed figure from the SAME formula, and that figure
// lands within the $8,000–$15,000 target band.
//
// Mutation: the hand recomputation here reproduces the full formula including
// the burst term. If the package drops the burst term (e.g. returns plain owned
// hourly), package MonthlyTCO diverges from this independent expectation and the
// equality assert fails.
func TestDocumentedMonthlyTCO(t *testing.T) {
	id := runUUID(t)
	cfg := documentedConfig()

	// Independent recomputation from raw inputs (no reuse of package methods).
	amort := cfg.CapexPerGPU / (cfg.LifespanMonths * HoursPerMonth * cfg.Utilization)
	power := (cfg.PowerWatts / 1000.0) * cfg.PowerPriceKWh
	owned := amort + power
	blended := (1.0-cfg.BurstFraction)*owned + cfg.BurstFraction*cfg.BurstPricePerHr
	wantMonthly := blended * float64(cfg.GPUCount) * HoursPerMonth

	t.Logf("run=%s BEFORE amort=%.6f power=%.6f owned=%.6f blended=%.6f wantMonthly=%.4f",
		id, amort, power, owned, blended, wantMonthly)

	got := MonthlyTCO(cfg)
	t.Logf("run=%s AFTER  MonthlyTCO=%.4f", id, got)

	if math.Abs(got-wantMonthly) > 1e-6 {
		t.Fatalf("run=%s MonthlyTCO=%.6f, independently computed=%.6f (delta %.6g)",
			id, got, wantMonthly, got-wantMonthly)
	}

	// Sanity: the hand figure must be inside the documented band, otherwise the
	// config no longer demonstrates the intended hybrid economics.
	const lo, hi = 8000.0, 15000.0
	if wantMonthly < lo || wantMonthly > hi {
		t.Fatalf("run=%s computed monthly TCO %.2f outside band [%.0f,%.0f]", id, wantMonthly, lo, hi)
	}
	if got < lo || got > hi {
		t.Fatalf("run=%s MonthlyTCO %.2f outside band [%.0f,%.0f]", id, got, lo, hi)
	}

	// The burst term must actually contribute: blended must differ from a
	// burst-free owned-only blend. This pins down that the burst weighting is
	// live (a dropped burst term would make these equal).
	ownedOnlyMonthly := owned * float64(cfg.GPUCount) * HoursPerMonth
	if math.Abs(got-ownedOnlyMonthly) < 1.0 {
		t.Fatalf("run=%s burst term inert: hybrid %.4f ~= owned-only %.4f", id, got, ownedOnlyMonthly)
	}
}

// Clause 2: increasing BurstFraction changes blended cost monotonically in the
// documented direction. With the documented config BurstPricePerHr (0.90) >
// OwnedHourly, so MORE burst must RAISE EffectiveHourly and MonthlyTCO.
//
// Mutation: dropping the burst term makes EffectiveHourly constant in
// BurstFraction, so the strict increase across the sweep fails.
func TestBurstFractionMonotonic(t *testing.T) {
	id := runUUID(t)
	base := documentedConfig()

	owned := base.OwnedHourly()
	if base.BurstPricePerHr <= owned {
		t.Fatalf("run=%s precondition: burst price %.4f must exceed owned %.4f for the documented direction",
			id, base.BurstPricePerHr, owned)
	}
	t.Logf("run=%s BEFORE owned=%.6f burstPrice=%.6f (burst pricier => more burst raises TCO)",
		id, owned, base.BurstPricePerHr)

	fractions := []float64{0.0, 0.08, 0.2, 0.5, 0.9, 1.0}
	prevEff := math.Inf(-1)
	prevMonthly := math.Inf(-1)
	for _, bf := range fractions {
		cfg := base
		cfg.BurstFraction = bf
		eff := EffectiveHourly(cfg)
		monthly := MonthlyTCO(cfg)
		t.Logf("run=%s AFTER  bf=%.2f eff=%.6f monthly=%.4f", id, bf, eff, monthly)

		if eff <= prevEff+eps {
			t.Fatalf("run=%s EffectiveHourly not strictly increasing at bf=%.2f: %.6f <= prev %.6f",
				id, bf, eff, prevEff)
		}
		if monthly <= prevMonthly+eps {
			t.Fatalf("run=%s MonthlyTCO not strictly increasing at bf=%.2f: %.6f <= prev %.6f",
				id, bf, monthly, prevMonthly)
		}
		prevEff, prevMonthly = eff, monthly
	}

	// Endpoint anchors: bf=0 => owned hourly; bf=1 => burst price.
	zero := base
	zero.BurstFraction = 0
	if math.Abs(EffectiveHourly(zero)-owned) > eps {
		t.Fatalf("run=%s bf=0 eff=%.6f != owned %.6f", id, EffectiveHourly(zero), owned)
	}
	full := base
	full.BurstFraction = 1
	if math.Abs(EffectiveHourly(full)-base.BurstPricePerHr) > eps {
		t.Fatalf("run=%s bf=1 eff=%.6f != burst price %.6f", id, EffectiveHourly(full), base.BurstPricePerHr)
	}
}

// Clause 3: higher Utilization lowers the owned amortized component per unit of
// work — the capex is spread over more worked hours.
//
// Mutation: if the amortized term ignores Utilization (drops the divisor), the
// component becomes constant across utilizations and this strict-decrease check
// fails.
func TestHigherUtilizationLowersAmortized(t *testing.T) {
	id := runUUID(t)
	base := documentedConfig()

	utils := []float64{0.3, 0.5, 0.7, 0.9, 1.0}
	prev := math.Inf(1)
	var firstAmort, lastAmort float64
	for i, u := range utils {
		cfg := base
		cfg.Utilization = u
		amort := cfg.OwnedAmortizedHourly()

		// Independent expectation derived from the formula for this utilization.
		want := cfg.CapexPerGPU / (cfg.LifespanMonths * HoursPerMonth * u)
		if math.Abs(amort-want) > 1e-9 {
			t.Fatalf("run=%s u=%.2f amort=%.9f != derived %.9f", id, u, amort, want)
		}
		t.Logf("run=%s u=%.2f amort=%.9f", id, u, amort)

		if amort >= prev-eps {
			t.Fatalf("run=%s amortized not strictly decreasing as util rises: u=%.2f amort=%.9f >= prev %.9f",
				id, u, amort, prev)
		}
		prev = amort
		if i == 0 {
			firstAmort = amort
		}
		lastAmort = amort
	}
	t.Logf("run=%s BEFORE(lowU)=%.9f AFTER(highU)=%.9f (must fall)", id, firstAmort, lastAmort)
	if lastAmort >= firstAmort {
		t.Fatalf("run=%s amortized at highest util %.9f not below lowest util %.9f", id, lastAmort, firstAmort)
	}

	// Exact inverse-proportionality anchor: doubling util halves the amortized
	// component. amort(0.5)/amort(1.0) must be 2.
	half := base
	half.Utilization = 0.5
	one := base
	one.Utilization = 1.0
	ratio := half.OwnedAmortizedHourly() / one.OwnedAmortizedHourly()
	if math.Abs(ratio-2.0) > 1e-9 {
		t.Fatalf("run=%s amort(0.5)/amort(1.0)=%.9f, want 2.0", id, ratio)
	}
}

// Validate guards the input ranges; exercised so malformed configs are rejected
// rather than silently producing nonsense.
func TestValidate(t *testing.T) {
	id := runUUID(t)
	good := documentedConfig()
	if err := good.Validate(); err != nil {
		t.Fatalf("run=%s documented config rejected: %v", id, err)
	}
	if _, err := CheckedMonthlyTCO(good); err != nil {
		t.Fatalf("run=%s CheckedMonthlyTCO good config: %v", id, err)
	}

	bad := []struct {
		name  string
		mutfn func(c *Config)
	}{
		{"zero gpus", func(c *Config) { c.GPUCount = 0 }},
		{"neg capex", func(c *Config) { c.CapexPerGPU = -1 }},
		{"zero lifespan", func(c *Config) { c.LifespanMonths = 0 }},
		{"neg power", func(c *Config) { c.PowerWatts = -1 }},
		{"neg tariff", func(c *Config) { c.PowerPriceKWh = -0.01 }},
		{"util 0", func(c *Config) { c.Utilization = 0 }},
		{"util >1", func(c *Config) { c.Utilization = 1.5 }},
		{"burst frac <0", func(c *Config) { c.BurstFraction = -0.1 }},
		{"burst frac >1", func(c *Config) { c.BurstFraction = 1.1 }},
		{"neg burst price", func(c *Config) { c.BurstPricePerHr = -1 }},
	}
	for _, tc := range bad {
		c := documentedConfig()
		tc.mutfn(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("run=%s %q: expected ErrInvalidConfig, got nil", id, tc.name)
		}
		if _, err := CheckedMonthlyTCO(c); err == nil {
			t.Fatalf("run=%s %q: CheckedMonthlyTCO accepted invalid config", id, tc.name)
		}
	}
	t.Logf("run=%s validate rejected %d malformed configs", id, len(bad))
}
