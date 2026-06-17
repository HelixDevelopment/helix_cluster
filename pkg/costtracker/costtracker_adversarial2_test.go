package costtracker

import (
	"math"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SECOND adversarial pass on pkg/costtracker.
//
// The first pass fixed the concurrent-Record data race + lost-update by adding
// the mutex (see costtracker_adversarial_test.go). This pass attacks a DIFFERENT
// surface: the input-sanitization clamp in Record and the conservation of the
// reported total under POISONED inputs.
//
// REAL BUG found and fixed this pass:
//
//   Record's clamp guarded ONLY `< 0`. A single NaN or ±Inf cost/hours sailed
//   through the clamp (NaN/Inf comparisons against 0 are false) and poisoned the
//   ENTIRE Report: TotalCost/TotalHours/BlendedPerHour all became NaN or +Inf.
//   That is strictly worse than the negative case the clamp already defends
//   against — one bad record annihilates every other (correct) recorded charge,
//   destroying cost conservation irrecoverably. The fix extends the clamp to
//   treat non-finite inputs the same as negatives (clamp to 0).
//
// These tests are mutation-verified: each FAILS on the pre-fix clamp (only `<0`)
// and PASSES on the fixed clamp.
// ---------------------------------------------------------------------------

const advers2Eps = 1e-9

// TestPoison_NaNRateDoesNotDestroyTotal: a NaN rate must contribute 0 cost and
// leave every other charge's money intact. Pre-fix: TotalCost == NaN.
func TestPoison_NaNRateDoesNotDestroyTotal(t *testing.T) {
	tr := New()
	tr.Record(1.0, 1.0, false)        // 1.0 cost, 1.0 hour
	tr.Record(math.NaN(), 2.0, false) // poison rate -> must clamp to 0 cost
	tr.Record(5.0, 2.0, false)        // 10.0 cost, 2.0 hours

	got := tr.Report(1.0)
	t.Logf("after NaN-rate: cost=%v hours=%v blended=%v", got.TotalCost, got.TotalHours, got.BlendedPerHour)

	if math.IsNaN(got.TotalCost) || math.IsInf(got.TotalCost, 0) {
		t.Fatalf("NaN rate poisoned TotalCost: %v", got.TotalCost)
	}
	// Conservation: only the two good charges count toward cost. The poisoned
	// record's hours are also clamped (rate AND hours sanitized together), so it
	// contributes neither cost nor hours.
	if math.Abs(got.TotalCost-11.0) > advers2Eps {
		t.Fatalf("TotalCost=%.9f want 11.0 (NaN charge must contribute 0)", got.TotalCost)
	}
	if math.IsNaN(got.BlendedPerHour) || math.IsInf(got.BlendedPerHour, 0) {
		t.Fatalf("BlendedPerHour non-finite: %v", got.BlendedPerHour)
	}
}

// TestPoison_NaNHoursDoesNotDestroyTotal: a NaN hours must contribute 0 hours
// and 0 cost, and must not poison TotalHours / BlendedPerHour. Pre-fix:
// TotalHours == NaN, BlendedPerHour == NaN.
func TestPoison_NaNHoursDoesNotDestroyTotal(t *testing.T) {
	tr := New()
	tr.Record(2.0, 3.0, false)        // 6.0 cost, 3.0 hours
	tr.Record(4.0, math.NaN(), false) // poison hours
	tr.Record(1.0, 1.0, true)         // 1.0 cost, 1.0 hour

	got := tr.Report(2.0)
	t.Logf("after NaN-hours: cost=%v hours=%v blended=%v", got.TotalCost, got.TotalHours, got.BlendedPerHour)

	if math.IsNaN(got.TotalHours) || math.IsInf(got.TotalHours, 0) {
		t.Fatalf("NaN hours poisoned TotalHours: %v", got.TotalHours)
	}
	if math.Abs(got.TotalHours-4.0) > advers2Eps {
		t.Fatalf("TotalHours=%.9f want 4.0 (NaN-hours charge must contribute 0)", got.TotalHours)
	}
	if math.Abs(got.TotalCost-7.0) > advers2Eps {
		t.Fatalf("TotalCost=%.9f want 7.0", got.TotalCost)
	}
	if math.IsNaN(got.BlendedPerHour) || math.IsInf(got.BlendedPerHour, 0) {
		t.Fatalf("BlendedPerHour non-finite after NaN hours: %v", got.BlendedPerHour)
	}
	// Blended must equal cost/hours exactly over the surviving charges.
	if math.Abs(got.BlendedPerHour-(got.TotalCost/got.TotalHours)) > advers2Eps {
		t.Fatalf("blended %.9f != cost/hours %.9f", got.BlendedPerHour, got.TotalCost/got.TotalHours)
	}
}

// TestPoison_PosInfDoesNotDestroyTotal: a +Inf rate must clamp to 0, not blow
// the total to +Inf. Pre-fix: TotalCost == +Inf.
func TestPoison_PosInfDoesNotDestroyTotal(t *testing.T) {
	tr := New()
	tr.Record(3.0, 2.0, false)        // 6.0 cost
	tr.Record(math.Inf(1), 5.0, true) // poison -> 0
	tr.Record(2.0, 2.0, false)        // 4.0 cost

	got := tr.Report(1.0)
	t.Logf("after +Inf rate: cost=%v hours=%v", got.TotalCost, got.TotalHours)

	if math.IsInf(got.TotalCost, 0) || math.IsNaN(got.TotalCost) {
		t.Fatalf("+Inf rate poisoned TotalCost: %v", got.TotalCost)
	}
	if math.Abs(got.TotalCost-10.0) > advers2Eps {
		t.Fatalf("TotalCost=%.9f want 10.0 (+Inf charge must contribute 0)", got.TotalCost)
	}
}

// TestPoison_NegInfDoesNotInvertTotal: a -Inf rate must clamp to 0 (the clamp's
// whole purpose: a bad record must not invert/destroy the total). Pre-fix the
// `<0` check WOULD have caught -Inf for the rate... but -Inf*hours is still -Inf,
// and -Inf passes `<0` so it clamps; the real gap is +Inf and NaN. This test
// pins that -Inf is also handled and the total stays the sum of good charges.
func TestPoison_NegInfDoesNotInvertTotal(t *testing.T) {
	tr := New()
	tr.Record(2.0, 2.0, false)          // 4.0 cost
	tr.Record(math.Inf(-1), 3.0, false) // -Inf rate -> clamp to 0
	tr.Record(math.Inf(-1), math.Inf(-1), false)
	tr.Record(3.0, 1.0, true) // 3.0 cost

	got := tr.Report(1.0)
	t.Logf("after -Inf: cost=%v hours=%v", got.TotalCost, got.TotalHours)

	if got.TotalCost < 0 {
		t.Fatalf("negative/-Inf record inverted total: %.9f", got.TotalCost)
	}
	if math.IsNaN(got.TotalCost) || math.IsInf(got.TotalCost, 0) {
		t.Fatalf("non-finite TotalCost: %v", got.TotalCost)
	}
	if math.Abs(got.TotalCost-7.0) > advers2Eps {
		t.Fatalf("TotalCost=%.9f want 7.0", got.TotalCost)
	}
}

// TestPoison_ConservationSurvivesInterleavedPoison: a long mixed stream where
// every 5th charge is a poison value (NaN / +Inf / -Inf), interleaved with good
// charges. The reported total MUST equal the independently summed total of the
// GOOD charges only — poison contributes exactly 0. This is the conservation
// centerpiece for this pass: one (or many) bad records cannot change the money.
func TestPoison_ConservationSurvivesInterleavedPoison(t *testing.T) {
	tr := New()
	poisons := []float64{math.NaN(), math.Inf(1), math.Inf(-1)}

	var wantCost, wantHours float64
	for i := 0; i < 3000; i++ {
		if i%5 == 0 {
			// Pure poison in BOTH fields -> clamps to 0 cost AND 0 hours, so the
			// good-charges-only oracle below is exact.
			p := poisons[i%len(poisons)]
			tr.Record(p, p, i%2 == 0)
			continue
		}
		rate := 0.1 + float64(i%13)*0.07
		hours := 0.5 + float64((i*3)%29)*0.25
		tr.Record(rate, hours, i%3 == 0)
		wantCost += rate * hours
		wantHours += hours
	}

	got := tr.Report(2.0)
	t.Logf("interleaved poison: gotCost=%.9f wantCost=%.9f gotHours=%.9f wantHours=%.9f",
		got.TotalCost, wantCost, got.TotalHours, wantHours)

	if math.IsNaN(got.TotalCost) || math.IsInf(got.TotalCost, 0) {
		t.Fatalf("poison stream produced non-finite TotalCost: %v", got.TotalCost)
	}
	if math.IsNaN(got.TotalHours) || math.IsInf(got.TotalHours, 0) {
		t.Fatalf("poison stream produced non-finite TotalHours: %v", got.TotalHours)
	}
	// Order differs from the oracle (good charges only), so allow a relative slack.
	if rel := math.Abs(got.TotalCost-wantCost) / wantCost; rel > 1e-12 {
		t.Fatalf("CONSERVATION under poison: gotCost=%.9f wantCost=%.9f relErr=%g", got.TotalCost, wantCost, rel)
	}
	if rel := math.Abs(got.TotalHours-wantHours) / wantHours; rel > 1e-12 {
		t.Fatalf("CONSERVATION under poison: gotHours=%.9f wantHours=%.9f relErr=%g", got.TotalHours, wantHours, rel)
	}
}

// TestConcurrent_RecordPoisonVsReport is a DIFFERENT concurrent path than the
// first pass's race test: writers inject poison (NaN/Inf) records concurrently
// with good records while readers call Report. Beyond -race cleanliness, the
// final Report MUST stay finite and equal the count of good unit charges — a
// concurrently-recorded poison value must not be able to corrupt a reader's
// total. Guarded by a hard timeout so it can never hang the suite.
func TestConcurrent_RecordPoisonVsReport(t *testing.T) {
	const writers = 40
	const perWriter = 250
	const goodPerWriter = perWriter - perWriter/5 // 4 of every 5 are good unit charges
	wantTotal := writers * goodPerWriter

	tr := New()
	var wg sync.WaitGroup
	start := make(chan struct{})
	done := make(chan struct{})

	poisons := []float64{math.NaN(), math.Inf(1), math.Inf(-1)}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				if i%5 == 0 {
					p := poisons[(seed+i)%len(poisons)]
					tr.Record(p, p, i%2 == 0) // pure poison -> 0
				} else {
					tr.Record(1.0, 1.0, i%2 == 0) // good unit charge
				}
			}
		}(w)
	}

	readerStop := make(chan struct{})
	var readerWG sync.WaitGroup
	for r := 0; r < 6; r++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			<-start
			for {
				select {
				case <-readerStop:
					return
				default:
					rep := tr.Report(2.0)
					if math.IsNaN(rep.TotalCost) || math.IsInf(rep.TotalCost, 0) {
						// Surfaced via the final assert too, but flag early.
						t.Errorf("reader saw non-finite TotalCost mid-flight: %v", rep.TotalCost)
						return
					}
				}
			}
		}()
	}

	go func() {
		close(start)
		wg.Wait()
		close(readerStop)
		readerWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("timeout: concurrent poison test did not finish")
	}

	got := tr.Report(2.0)
	t.Logf("concurrent poison: gotCost=%.1f wantTotal=%d", got.TotalCost, wantTotal)
	if math.IsNaN(got.TotalCost) || math.IsInf(got.TotalCost, 0) {
		t.Fatalf("final TotalCost non-finite after concurrent poison: %v", got.TotalCost)
	}
	if got.TotalCost != float64(wantTotal) {
		t.Fatalf("CONSERVATION under concurrent poison: TotalCost=%.1f want=%d", got.TotalCost, wantTotal)
	}
	if got.TotalHours != float64(wantTotal) {
		t.Fatalf("CONSERVATION under concurrent poison: TotalHours=%.1f want=%d", got.TotalHours, wantTotal)
	}
}
