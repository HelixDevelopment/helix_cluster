package costtracker

import (
	"math"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial / mutation-verified contract tests for the cost accumulator.
//
// Cost model under audit (costtracker.go):
//   - Tracker holds a []allocation slice; Record() APPENDS (negative -> 0).
//   - Report() RANGES over the slice, summing costPerHour*hours and hours.
//   - There is NO synchronization on t.allocs.
//
// Centerpieces:
//   (A) CONSERVATION  — Report().TotalCost == exact sum of per-charge costs,
//       and TotalHours == exact sum of hours; no charge dropped/double-counted;
//       per-bucket (spot vs on-demand) costs partition the grand total exactly.
//   (B) CONCURRENCY   — Record hammered from many goroutines, with concurrent
//       Report readers, under `go test -race`. An unguarded slice append/read
//       races here AND can drop charges (lost append), breaking conservation.
//
// FINDING: the production Tracker has NO mutex. Record (append) racing with
// Record/Report is a genuine data race and a lost-update (non-conservation)
// bug. TestConcurrent_RecordVsReport_RaceFree is expected to FAIL under
// `go test -race` against the current production code (race report) and/or
// the conservation assert (lost appends). See the report for the minimal fix.
// ---------------------------------------------------------------------------

const adversEps = 1e-9

// TestConservation_GrandTotalEqualsSumOfCharges records many heterogeneous
// charges and asserts the reported totals equal an independently accumulated
// sum — to the last representable bit when summed in the same order, and within
// a tight tolerance regardless. Pins bug class (1): money created/lost.
func TestConservation_GrandTotalEqualsSumOfCharges(t *testing.T) {
	tr := New()

	// Deterministic but irregular mix: small + large, spot + on-demand.
	type charge struct {
		rate, hours float64
		spot        bool
	}
	charges := make([]charge, 0, 1000)
	for i := 0; i < 1000; i++ {
		rate := 0.01 + float64(i%37)*0.013    // 0.01 .. ~0.48
		hours := 0.25 + float64((i*7)%50)*1.5 // small fractional + large
		charges = append(charges, charge{rate: rate, hours: hours, spot: i%3 == 0})
	}

	// Oracle: accumulate in the SAME iteration order the production loop uses
	// (it ranges allocs in insertion order), so the float sum is bit-identical.
	var wantCost, wantHours float64
	var wantSpotCost, wantOnDemandCost float64
	for _, c := range charges {
		tr.Record(c.rate, c.hours, c.spot)
		cost := c.rate * c.hours
		wantCost += cost
		wantHours += c.hours
		if c.spot {
			wantSpotCost += cost
		} else {
			wantOnDemandCost += cost
		}
	}

	got := tr.Report(2.50)

	// Bit-identical: same operands, same order.
	if got.TotalCost != wantCost {
		t.Fatalf("CONSERVATION violated: TotalCost=%.17g want=%.17g (delta=%g)",
			got.TotalCost, wantCost, got.TotalCost-wantCost)
	}
	if got.TotalHours != wantHours {
		t.Fatalf("CONSERVATION violated: TotalHours=%.17g want=%.17g (delta=%g)",
			got.TotalHours, wantHours, got.TotalHours-wantHours)
	}
	// Per-bucket partition must sum back to the grand total (no charge attributed
	// to the wrong bucket or counted twice). Allow tiny float reassociation slack.
	if math.Abs((wantSpotCost+wantOnDemandCost)-got.TotalCost) > adversEps {
		t.Fatalf("ATTRIBUTION/partition violated: spot+onDemand=%.9f != total=%.9f",
			wantSpotCost+wantOnDemandCost, got.TotalCost)
	}
}

// TestConservation_NoDropNoDoubleCount records N identical unit charges and
// asserts the total is exactly N (no append dropped, none double-counted). This
// is the cleanest detector of a dropped/duplicated charge mutation.
func TestConservation_NoDropNoDoubleCount(t *testing.T) {
	const n = 10000
	tr := New()
	for i := 0; i < n; i++ {
		tr.Record(1.0, 1.0, i%2 == 0) // each contributes exactly 1.0 cost and 1.0 hour
	}
	got := tr.Report(1.0)

	if got.TotalCost != float64(n) {
		t.Fatalf("expected TotalCost=%d (one per charge), got %.6f — charge dropped or double-counted",
			n, got.TotalCost)
	}
	if got.TotalHours != float64(n) {
		t.Fatalf("expected TotalHours=%d, got %.6f", n, got.TotalHours)
	}
	if got.BlendedPerHour != 1.0 {
		t.Fatalf("blended should be exactly 1.0, got %.9f", got.BlendedPerHour)
	}
}

// TestPrecision_LongMixedSequence sums a long run of large and tiny charges and
// checks the running total stays accurate vs a high-fidelity (Kahan) oracle.
// Pins bug class (4): precision drift on a running sum.
func TestPrecision_LongMixedSequence(t *testing.T) {
	tr := New()

	var kahanSum, comp float64 // Kahan compensated summation oracle
	add := func(x float64) {
		y := x - comp
		tmp := kahanSum + y
		comp = (tmp - kahanSum) - y
		kahanSum = tmp
	}

	const n = 50000
	for i := 0; i < n; i++ {
		var rate, hours float64
		if i%1000 == 0 {
			rate, hours = 100.0, 100.0 // occasional large charge: 10000
		} else {
			rate, hours = 0.001, 0.5 // many tiny charges: 0.0005
		}
		tr.Record(rate, hours, false)
		add(rate * hours)
	}

	got := tr.Report(1.0)
	// Naive float64 summation vs Kahan: drift must be a tiny relative amount.
	relErr := math.Abs(got.TotalCost-kahanSum) / kahanSum
	if relErr > 1e-12 {
		t.Fatalf("PRECISION drift too large: got=%.9f kahan=%.9f relErr=%g",
			got.TotalCost, kahanSum, relErr)
	}
}

// TestNegativeClamped pins the DOCUMENTED contract: negative inputs are clamped
// to zero (not rejected, not propagated as negative). A mutation that drops the
// clamp (lets negatives through) would invert the total and fail here.
func TestNegativeClamped(t *testing.T) {
	tr := New()
	tr.Record(-5.0, 10.0, false) // rate clamped -> contributes 0 cost, 10 hours
	tr.Record(2.0, -3.0, false)  // hours clamped -> contributes 0 cost, 0 hours
	tr.Record(2.0, 4.0, false)   // normal: 8 cost, 4 hours

	got := tr.Report(1.0)
	if got.TotalCost != 8.0 {
		t.Fatalf("clamp contract: TotalCost=%.6f want 8.0 (negatives must clamp to 0)", got.TotalCost)
	}
	if got.TotalHours != 14.0 {
		t.Fatalf("clamp contract: TotalHours=%.6f want 14.0", got.TotalHours)
	}
	if got.TotalCost < 0 {
		t.Fatalf("negative total leaked through clamp: %.6f", got.TotalCost)
	}
}

// TestConcurrent_RecordVsReport_RaceFree is the CONCURRENCY CENTERPIECE.
//
// Many goroutines call Record while other goroutines call Report concurrently;
// after the writers finish, a final Report must equal the exact sum of every
// charge issued (each charge contributes exactly 1.0 cost and 1.0 hour, so the
// grand total must be writers*perWriter).
//
// Under `go test -race` an unguarded append/range trips the race detector.
// Even without -race, concurrent appends lose updates (two goroutines read the
// same len, write the same index, one append is dropped), so the final total
// comes out LESS than the issued total — a non-conservation bug.
func TestConcurrent_RecordVsReport_RaceFree(t *testing.T) {
	const writers = 50
	const perWriter = 200
	const wantTotal = writers * perWriter

	tr := New()

	var wg sync.WaitGroup
	start := make(chan struct{})

	// Writers: each issues `perWriter` unit charges.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				tr.Record(1.0, 1.0, i%2 == 0)
			}
		}()
	}

	// Concurrent readers: hammer Report while writers run. These reads race
	// with the appends in the unsynchronized implementation.
	readerStop := make(chan struct{})
	var readerWG sync.WaitGroup
	for r := 0; r < 8; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			<-start
			for {
				select {
				case <-readerStop:
					return
				default:
					_ = tr.Report(2.0) // ranges t.allocs concurrently with append
				}
			}
		}()
	}

	close(start)
	wg.Wait()
	close(readerStop)
	readerWG.Wait()

	got := tr.Report(2.0)
	if got.TotalCost != float64(wantTotal) {
		t.Fatalf("CONCURRENCY non-conservation: TotalCost=%.1f want=%d (lost %d appends)",
			got.TotalCost, wantTotal, wantTotal-int(got.TotalCost))
	}
	if got.TotalHours != float64(wantTotal) {
		t.Fatalf("CONCURRENCY non-conservation: TotalHours=%.1f want=%d",
			got.TotalHours, wantTotal)
	}
}
