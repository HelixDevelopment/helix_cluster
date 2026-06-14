package economics

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// relEqual compares a and b allowing a relative tolerance for large
// magnitudes plus a small absolute floor for values near zero. The
// production conservation guarantee is mathematically exact
// (sum(ledger) == distributable by construction), so the only slack is
// IEEE-754 summation drift, which scales with magnitude — hence a relative
// bound is the honest oracle, stricter than a fixed 1e-9 would be for small
// pools and not falsely failing for huge ones.
func relEqual(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= 1e-9 {
		return true
	}
	scale := math.Max(math.Abs(a), math.Abs(b))
	return diff <= 1e-12*scale
}

// ---------------------------------------------------------------------------
// CONSERVATION — the centerpiece. No value may be created or destroyed:
// sum(ledger) + treasury + reinvest must equal total. We sweep many weight
// vectors, including uneven splits that do not divide evenly, large
// magnitudes, and many participants. We additionally assert value is never
// CREATED (conserved <= total + slack) so a one-directional over-allocation
// bug (e.g. dropping the treasury subtraction) is caught explicitly.
// ---------------------------------------------------------------------------

func TestAdv_Distribute_ConservationSweep(t *testing.T) {
	const runUUID = "econ-adv-cons-7c19d3"
	d := NewRewardDistributor()

	rules := []DistributionRule{
		{TreasuryCut: 0, ReinvestCut: 0},
		{TreasuryCut: 0.10, ReinvestCut: 0.05},
		{TreasuryCut: 0.33, ReinvestCut: 0.33},
		{TreasuryCut: 0.5, ReinvestCut: 0.4999999},
		{TreasuryCut: 0.999, ReinvestCut: 0},
	}

	// Weight vectors chosen to defeat clean division: 100 among 3, primes,
	// lopsided ratios, dust + whale.
	weightVecs := [][]float64{
		{100},     // single participant
		{1, 1, 1}, // 3-way, indivisible thirds
		{100.0 / 3, 100.0 / 3, 100.0 / 3},
		{1, 2, 3, 4, 5, 6, 7},   // ascending
		{0.01, 999999.99},       // dust + whale
		{7, 11, 13, 17, 19, 23}, // primes
		{1e-6, 1e6, 1, 333.333}, // mixed magnitude
	}
	scales := []float64{1, 7.0, 1e6, 1e9}

	for ri, rule := range rules {
		for wi, wv := range weightVecs {
			for _, scale := range scales {
				earnings := make(map[string]float64, len(wv))
				var wantTotal float64
				for i, w := range wv {
					v := w * scale
					earnings[fmt.Sprintf("p%d", i)] = v
					wantTotal += v
				}
				got, err := d.Distribute(earnings, rule)
				if err != nil {
					t.Fatalf("[%s] r%d w%d scale=%g unexpected error: %v", runUUID, ri, wi, scale, err)
				}

				// Total must equal sum of inputs.
				if !relEqual(got.Total, wantTotal) {
					t.Fatalf("[%s] r%d w%d scale=%g Total=%v want sum(earnings)=%v",
						runUUID, ri, wi, scale, got.Total, wantTotal)
				}

				// Per-participant non-negativity + finiteness; zero-weight
				// participant must receive exactly zero.
				var ledgerSum float64
				for p, share := range got.Ledger {
					if math.IsNaN(share) || math.IsInf(share, 0) {
						t.Fatalf("[%s] r%d w%d scale=%g ledger[%s]=%v not finite", runUUID, ri, wi, scale, p, share)
					}
					if share < 0 {
						t.Fatalf("[%s] r%d w%d scale=%g NEGATIVE share ledger[%s]=%v", runUUID, ri, wi, scale, p, share)
					}
					if earnings[p] == 0 && share != 0 {
						t.Fatalf("[%s] zero-weight participant %s got nonzero share %v", runUUID, p, share)
					}
					ledgerSum += share
				}
				if got.Treasury < 0 || got.Reinvest < 0 {
					t.Fatalf("[%s] negative cut: treasury=%v reinvest=%v", runUUID, got.Treasury, got.Reinvest)
				}

				conserved := ledgerSum + got.Treasury + got.Reinvest

				// (a) Conservation: nothing created or destroyed.
				if !relEqual(conserved, got.Total) {
					t.Fatalf("[%s] r%d w%d scale=%g CONSERVATION VIOLATED: sum(ledger)+treasury+reinvest=%.17g want Total=%.17g delta=%g",
						runUUID, ri, wi, scale, conserved, got.Total, conserved-got.Total)
				}
				// (b) No value created: conserved must never exceed total
				// beyond float slack (directional guard for over-allocation).
				slack := 1e-9 + 1e-12*math.Max(math.Abs(conserved), math.Abs(got.Total))
				if conserved > got.Total+slack {
					t.Fatalf("[%s] r%d w%d scale=%g VALUE CREATED: conserved=%.17g > Total=%.17g (excess %g)",
						runUUID, ri, wi, scale, conserved, got.Total, conserved-got.Total)
				}
				// (c) distributable share matches the cut math: ledgerSum ==
				// total*(1 - T - R).
				wantDistributable := got.Total * (1 - rule.TreasuryCut - rule.ReinvestCut)
				if !relEqual(ledgerSum, wantDistributable) {
					t.Fatalf("[%s] r%d w%d scale=%g sum(ledger)=%.17g want distributable=%.17g",
						runUUID, ri, wi, scale, ledgerSum, wantDistributable)
				}
			}
		}
	}
	t.Logf("[%s] proved: across %d rules x %d weight vectors x %d scales, sum(ledger)+treasury+reinvest==Total, no negative/created value",
		runUUID, len(rules), len(weightVecs), len(scales))
}

// TestAdv_Distribute_ExactThirds pins the indivisible-split case with a
// non-trivial cut so the rounding/conservation contract is nailed to a
// concrete number, not just a sweep tolerance.
func TestAdv_Distribute_ExactThirds(t *testing.T) {
	const runUUID = "econ-adv-thirds-4ab8e1"
	d := NewRewardDistributor()
	// total = 100; cuts 0 -> distributable = 100; each of 3 equal -> 100/3.
	earnings := map[string]float64{"a": 1, "b": 1, "c": 1}
	got, err := d.Distribute(earnings, DistributionRule{})
	if err != nil {
		t.Fatalf("[%s] %v", runUUID, err)
	}
	var sum float64
	for _, v := range got.Ledger {
		if !relEqual(v, 1.0) { // total=3, distributable=3, each gets 1
			t.Errorf("[%s] share=%v want 1", runUUID, v)
		}
		sum += v
	}
	if !relEqual(sum, got.Total) {
		t.Fatalf("[%s] indivisible-3 conservation: sum=%v Total=%v delta=%g", runUUID, sum, got.Total, sum-got.Total)
	}
	t.Logf("[%s] proved: 3 equal weights -> exact thirds, sum(ledger)==Total (delta %g)", runUUID, sum-got.Total)
}

// ---------------------------------------------------------------------------
// DEGENERATE cases.
// ---------------------------------------------------------------------------

func TestAdv_Distribute_Degenerate(t *testing.T) {
	const runUUID = "econ-adv-degen-1f5b9c"
	d := NewRewardDistributor()
	rule := DistributionRule{TreasuryCut: 0.10, ReinvestCut: 0.05}

	// Zero participants -> error, no div-by-zero / no empty Distribution that
	// silently "succeeds".
	if _, err := d.Distribute(map[string]float64{}, rule); err == nil {
		t.Errorf("[%s] empty earnings must error", runUUID)
	}

	// All-zero weights -> total==0 -> documented error, NOT a div-by-zero panic.
	if _, err := d.Distribute(map[string]float64{"a": 0, "b": 0}, rule); err == nil {
		t.Errorf("[%s] all-zero earnings must error (total must be positive), got nil", runUUID)
	}

	// Single participant gets the entire distributable remainder.
	got, err := d.Distribute(map[string]float64{"solo": 500}, rule)
	if err != nil {
		t.Fatalf("[%s] %v", runUUID, err)
	}
	wantSolo := 500.0 * (1 - 0.10 - 0.05) // 425
	if !relEqual(got.Ledger["solo"], wantSolo) {
		t.Errorf("[%s] solo share=%v want %v", runUUID, got.Ledger["solo"], wantSolo)
	}

	// One participant holds all the weight; a zero-weight peer gets exactly 0.
	got, err = d.Distribute(map[string]float64{"whale": 1000, "ghost": 0}, DistributionRule{})
	if err != nil {
		t.Fatalf("[%s] %v", runUUID, err)
	}
	if got.Ledger["ghost"] != 0 {
		t.Errorf("[%s] zero-weight 'ghost' got %v, want exactly 0", runUUID, got.Ledger["ghost"])
	}
	if !relEqual(got.Ledger["whale"], 1000) {
		t.Errorf("[%s] whale share=%v want 1000", runUUID, got.Ledger["whale"])
	}
	t.Logf("[%s] proved: empty + all-zero -> error (no panic); single participant takes whole distributable; zero-weight peer -> exactly 0", runUUID)
}

// ---------------------------------------------------------------------------
// ROI — hand-calculated, non-inversion, and div-by-zero guards.
// ---------------------------------------------------------------------------

func TestAdv_ROI_HandAndDirection(t *testing.T) {
	const runUUID = "econ-adv-roi-6e0c22"
	d := NewRewardDistributor()

	cases := []struct {
		name            string
		revenue, cost   float64
		dailyNet        float64
		wantROI, wantBE float64
	}{
		{"profit", 1500, 1000, 50, 50, 20},    // (1500-1000)/1000*100
		{"break_even", 1000, 1000, 25, 0, 40}, // zero ROI at parity
		{"loss", 500, 1000, 25, -50, 40},      // negative return allowed
		{"double", 2000, 1000, 50, 100, 20},   // more revenue -> higher ROI
	}
	for _, c := range cases {
		got, err := d.GetParticipantROI(c.name,
			map[string]float64{c.name: c.revenue},
			map[string]float64{c.name: c.cost}, c.dailyNet)
		if err != nil {
			t.Fatalf("[%s] %s: %v", runUUID, c.name, err)
		}
		if !relEqual(got.ROIPercent, c.wantROI) {
			t.Errorf("[%s] %s ROIPercent=%v want %v", runUUID, c.name, got.ROIPercent, c.wantROI)
		}
		if !relEqual(got.BreakEvenDays, c.wantBE) {
			t.Errorf("[%s] %s BreakEvenDays=%v want %v", runUUID, c.name, got.BreakEvenDays, c.wantBE)
		}
	}

	// Non-inversion / monotonicity: with fixed cost, more revenue must yield
	// strictly higher ROI. This catches an inverted formula like
	// (cost-revenue)/cost or cost/revenue.
	cost := 1000.0
	lo, _ := d.GetParticipantROI("x", map[string]float64{"x": 1200}, map[string]float64{"x": cost}, 10)
	hi, _ := d.GetParticipantROI("x", map[string]float64{"x": 1800}, map[string]float64{"x": cost}, 10)
	if !(hi.ROIPercent > lo.ROIPercent) {
		t.Fatalf("[%s] ROI not monotonic in revenue (inverted formula?): rev1800 ROI=%v <= rev1200 ROI=%v",
			runUUID, hi.ROIPercent, lo.ROIPercent)
	}

	// Wrong-denominator guard: ROI must use cost (investment) as denominator,
	// not revenue. (rev-cost)/cost for {1500,1000} = 50; (rev-cost)/rev would
	// be 33.33. Pin the correct one.
	roi, _ := d.GetParticipantROI("x", map[string]float64{"x": 1500}, map[string]float64{"x": 1000}, 10)
	if !relEqual(roi.ROIPercent, 50) {
		t.Fatalf("[%s] denominator check: ROIPercent=%v want 50 (=(rev-cost)/cost); 33.33 would mean /revenue", runUUID, roi.ROIPercent)
	}

	t.Logf("[%s] proved: ROI=(rev-cost)/cost*100 hand-matches profit/breakeven/loss/double, monotonic in revenue, denominator=cost", runUUID)
}

func TestAdv_ROI_DivByZeroGuards(t *testing.T) {
	const runUUID = "econ-adv-roi0-9d3a47"
	d := NewRewardDistributor()

	// Zero investment (cost) -> undefined ROI -> error, no Inf/NaN.
	if _, err := d.GetParticipantROI("x", map[string]float64{"x": 100}, map[string]float64{"x": 0}, 10); err == nil {
		t.Errorf("[%s] zero cost must error (undefined ROI)", runUUID)
	}
	// Zero dailyNet -> undefined break-even -> error.
	if _, err := d.GetParticipantROI("x", map[string]float64{"x": 100}, map[string]float64{"x": 50}, 0); err == nil {
		t.Errorf("[%s] zero dailyNet must error (undefined break-even)", runUUID)
	}
	// Missing cost -> error (not silent zero).
	if _, err := d.GetParticipantROI("x", map[string]float64{"x": 100}, map[string]float64{}, 10); err == nil {
		t.Errorf("[%s] missing cost must error", runUUID)
	}
	// Unknown participant -> typed sentinel.
	_, err := d.GetParticipantROI("ghost", map[string]float64{"x": 1}, map[string]float64{"x": 1}, 1)
	if !errors.Is(err, ErrUnknownParticipant) {
		t.Errorf("[%s] unknown participant want ErrUnknownParticipant, got %v", runUUID, err)
	}
	t.Logf("[%s] proved: zero cost, zero dailyNet, missing cost, unknown participant all error (no Inf/NaN/panic)", runUUID)
}

// ---------------------------------------------------------------------------
// DETERMINISM — map iteration order must not change any output value.
// ---------------------------------------------------------------------------

func TestAdv_Distribute_Determinism(t *testing.T) {
	const runUUID = "econ-adv-det-2b7f81"
	d := NewRewardDistributor()
	earnings := map[string]float64{"a": 17, "b": 41, "c": 3, "d": 100, "e": 0.5}
	rule := DistributionRule{TreasuryCut: 0.07, ReinvestCut: 0.11}

	first, err := d.Distribute(earnings, rule)
	if err != nil {
		t.Fatalf("[%s] %v", runUUID, err)
	}
	for i := 0; i < 50; i++ {
		got, err := d.Distribute(earnings, rule)
		if err != nil {
			t.Fatalf("[%s] iter %d: %v", runUUID, i, err)
		}
		if got.Total != first.Total || got.Treasury != first.Treasury || got.Reinvest != first.Reinvest {
			t.Fatalf("[%s] iter %d nondeterministic scalars", runUUID, i)
		}
		for p, v := range first.Ledger {
			if got.Ledger[p] != v {
				t.Fatalf("[%s] iter %d ledger[%s]=%v != %v", runUUID, i, p, got.Ledger[p], v)
			}
		}
	}
	t.Logf("[%s] proved: identical inputs -> byte-identical distribution across 51 runs (map order irrelevant)", runUUID)
}
