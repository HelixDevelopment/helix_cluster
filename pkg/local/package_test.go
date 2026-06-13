package local

import (
	"errors"
	"math"
	"testing"
)

// round4 rounds to 4 decimal places so float assertions can pin a concrete
// hand-computed $/hr without depending on the last ULP.
func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// canonicalGPU is the worked example from the spec:
//
//	$30000 / (5yr * 8760h * 0.7 util) + 700W * $0.12/kWh.
func canonicalGPU() GPU {
	return GPU{
		ID:             "h100-0",
		Model:          "NVIDIA H100 80GB",
		Count:          1,
		PurchasePrice:  30000,
		LifespanYears:  5,
		PowerDrawWatts: 700,
		PowerPriceKWh:  0.12,
		Utilization:    0.7,
	}
}

// TestEffectiveHourlyCost_HandComputed pins the full TCO to a hand-computed
// value: capital 30000/(5*8760*0.7)=0.9784735... plus power 0.7*0.12=0.084,
// summing to 1.0624735..., which rounds to 1.0625 $/hr.
//
// Mutation that this kills: drop the power term in GPU.EffectiveHourly (return
// g.AmortizedCapitalHourly() alone, omitting + g.PowerHourly()). That yields
// 0.9785 rounded, which is NOT 1.0625, so this assertion FAILS. The capital-
// only and full values are deliberately far apart (0.9785 vs 1.0625) so the
// rounding cannot mask the dropped power term.
func TestEffectiveHourlyCost_HandComputed(t *testing.T) {
	r := NewLocalGPURegistrar()
	if err := r.Register(canonicalGPU()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.EffectiveHourlyCost("h100-0")
	if err != nil {
		t.Fatalf("EffectiveHourlyCost: %v", err)
	}

	const want = 1.0625
	if round4(got) != want {
		t.Fatalf("effective $/hr = %v (rounded %v); want %v", got, round4(got), want)
	}

	// Guard rail proving the power term is actually present: the capital-only
	// figure (what the mutation would return) is a DIFFERENT, lower number.
	if round4(canonicalGPU().AmortizedCapitalHourly()) != 0.9785 {
		t.Fatalf("capital-only $/hr = %v; want 0.9785 (sanity)", round4(canonicalGPU().AmortizedCapitalHourly()))
	}
	if round4(got) == round4(canonicalGPU().AmortizedCapitalHourly()) {
		t.Fatalf("effective equals capital-only %v: power term missing", round4(got))
	}
}

// TestPowerComponent_HandComputed pins the power term alone: 700W is 0.7kW, at
// $0.12/kWh that is exactly $0.084/hr.
//
// Mutation that this kills: forget the /1000 watts→kW conversion in
// GPU.PowerHourly (return g.PowerDrawWatts * g.PowerPriceKWh). That yields 84.0,
// not 0.084, failing the assertion.
func TestPowerComponent_HandComputed(t *testing.T) {
	g := canonicalGPU()
	if round4(g.PowerHourly()) != 0.084 {
		t.Fatalf("power $/hr = %v; want 0.084", round4(g.PowerHourly()))
	}
}

// TestHigherUtilization_LowersAmortizedHourly checks the capital term is
// monotonically DECREASING in utilization: a busier card recovers its price
// over more hours, so its amortised $/hr is lower. Pinned: at 0.7 util the
// capital is 0.9785, at 0.9 util it is 0.7610.
//
// Mutation that this kills: divide by utilization with the wrong polarity, e.g.
// multiply instead of divide in AmortizedCapitalHourly
// (price/lifespanHours * utilization). Then higher utilization would RAISE the
// figure and the lo>hi inequality (plus the pinned 0.7610 value) FAILS.
func TestHigherUtilization_LowersAmortizedHourly(t *testing.T) {
	lo := canonicalGPU() // 0.7 util
	hi := canonicalGPU()
	hi.Utilization = 0.9

	cLo := lo.AmortizedCapitalHourly()
	cHi := hi.AmortizedCapitalHourly()

	if !(cHi < cLo) {
		t.Fatalf("higher utilization did not lower capital $/hr: 0.9util=%v not < 0.7util=%v", cHi, cLo)
	}
	if round4(cLo) != 0.9785 {
		t.Fatalf("capital@0.7util = %v; want 0.9785", round4(cLo))
	}
	if round4(cHi) != 0.7610 {
		t.Fatalf("capital@0.9util = %v; want 0.7610", round4(cHi))
	}
}

// TestHigherPowerPrice_RaisesTCO checks the effective TCO is monotonically
// INCREASING in the local power tariff, with both endpoints pinned: at
// $0.12/kWh effective is 1.0625, at $0.20/kWh it rises to 1.1185.
//
// Mutation that this kills: ignore PowerPriceKWh in PowerHourly (e.g. return a
// constant or use a hardcoded rate). Then raising the tariff would not change
// the TCO and the strict expensive>cheap inequality (plus the pinned 1.1185)
// FAILS.
func TestHigherPowerPrice_RaisesTCO(t *testing.T) {
	cheap := canonicalGPU() // $0.12/kWh
	pricey := canonicalGPU()
	pricey.PowerPriceKWh = 0.20

	if !(pricey.EffectiveHourly() > cheap.EffectiveHourly()) {
		t.Fatalf("higher power price did not raise TCO: 0.20=%v not > 0.12=%v",
			pricey.EffectiveHourly(), cheap.EffectiveHourly())
	}
	if round4(cheap.EffectiveHourly()) != 1.0625 {
		t.Fatalf("TCO@0.12 = %v; want 1.0625", round4(cheap.EffectiveHourly()))
	}
	if round4(pricey.EffectiveHourly()) != 1.1185 {
		t.Fatalf("TCO@0.20 = %v; want 1.1185", round4(pricey.EffectiveHourly()))
	}
}

// TestCheaperThanCloud_Boundary exercises the strict-less-than comparison on
// both sides and exactly at the boundary. The local TCO is 1.0624735... $/hr.
//
// Mutation that this kills: use <= instead of < in CheaperThanCloud. Then the
// equal-rate case (cloudHourly == local TCO) would wrongly report true and the
// "boundary -> false" assertion FAILS. Also kills swapping the operands
// (local > cloud), which flips both the above and below cases.
func TestCheaperThanCloud_Boundary(t *testing.T) {
	r := NewLocalGPURegistrar()
	if err := r.Register(canonicalGPU()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	tco, err := r.EffectiveHourlyCost("h100-0")
	if err != nil {
		t.Fatalf("EffectiveHourlyCost: %v", err)
	}

	cases := []struct {
		name        string
		cloudHourly float64
		want        bool
	}{
		{"cloud dearer -> local cheaper", tco + 0.5, true},
		{"cloud cheaper -> local not cheaper", tco - 0.5, false},
		{"exactly equal -> not cheaper (strict)", tco, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.CheaperThanCloud("h100-0", tc.cloudHourly)
			if err != nil {
				t.Fatalf("CheaperThanCloud: %v", err)
			}
			if got != tc.want {
				t.Fatalf("CheaperThanCloud(cloud=%v) = %v; want %v (local TCO=%v)",
					tc.cloudHourly, got, tc.want, tco)
			}
		})
	}
}

// TestRegister_RejectsInvalid proves validation actually gates bad specs rather
// than silently storing a card that would divide-by-zero or mis-amortise.
//
// Mutation that this kills: make GPU.validate always return nil. Then each
// invalid spec below would Register without error and the wantErr assertion
// FAILS (and a zero utilization/lifespan would later divide by zero).
func TestRegister_RejectsInvalid(t *testing.T) {
	base := canonicalGPU()

	mut := func(f func(*GPU)) GPU { g := base; f(&g); return g }

	cases := []struct {
		name string
		gpu  GPU
	}{
		{"empty id", mut(func(g *GPU) { g.ID = "" })},
		{"zero count", mut(func(g *GPU) { g.Count = 0 })},
		{"zero price", mut(func(g *GPU) { g.PurchasePrice = 0 })},
		{"negative lifespan", mut(func(g *GPU) { g.LifespanYears = -1 })},
		{"negative watts", mut(func(g *GPU) { g.PowerDrawWatts = -5 })},
		{"negative tariff", mut(func(g *GPU) { g.PowerPriceKWh = -0.01 })},
		{"zero utilization", mut(func(g *GPU) { g.Utilization = 0 })},
		{"utilization above one", mut(func(g *GPU) { g.Utilization = 1.5 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewLocalGPURegistrar()
			if err := r.Register(tc.gpu); !errors.Is(err, ErrInvalidGPU) {
				t.Fatalf("Register(%s) err = %v; want ErrInvalidGPU", tc.name, err)
			}
		})
	}
}

// TestRegister_DuplicateRejected proves a second Register of the same id is
// refused, so a re-registration cannot silently overwrite the asset record.
//
// Mutation that this kills: remove the exists check in Register (always store).
// Then the second Register returns nil and the ErrDuplicateID assertion FAILS.
func TestRegister_DuplicateRejected(t *testing.T) {
	r := NewLocalGPURegistrar()
	if err := r.Register(canonicalGPU()); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	dup := canonicalGPU()
	dup.PurchasePrice = 9999 // different facts, same id
	if err := r.Register(dup); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate Register err = %v; want ErrDuplicateID", err)
	}

	// The original record must survive: its TCO must be unchanged (1.0625), not
	// the duplicate's altered price.
	got, err := r.EffectiveHourlyCost("h100-0")
	if err != nil {
		t.Fatalf("EffectiveHourlyCost: %v", err)
	}
	if round4(got) != 1.0625 {
		t.Fatalf("after rejected dup, TCO = %v; want original 1.0625", round4(got))
	}
}

// TestUnknownGPU_Errors proves queries on an unregistered id fail loudly rather
// than returning a misleading zero cost.
//
// Mutation that this kills: have EffectiveHourlyCost return (0, nil) for a
// missing id. Then errors.Is(err, ErrUnknownGPU) is false and this FAILS — and
// CheaperThanCloud would treat the phantom 0 as cheaper than any cloud rate.
func TestUnknownGPU_Errors(t *testing.T) {
	r := NewLocalGPURegistrar()

	if _, err := r.EffectiveHourlyCost("nope"); !errors.Is(err, ErrUnknownGPU) {
		t.Fatalf("EffectiveHourlyCost(nope) err = %v; want ErrUnknownGPU", err)
	}
	if _, err := r.CheaperThanCloud("nope", 5.0); !errors.Is(err, ErrUnknownGPU) {
		t.Fatalf("CheaperThanCloud(nope) err = %v; want ErrUnknownGPU", err)
	}
}
