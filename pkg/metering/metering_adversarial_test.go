package metering

import (
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// This file is an adversarial, mutation-verified audit of pkg/metering's
// accounting ledger, probing the SAME bug class that struck sibling
// pkg/costtracker: an unsynchronized running accumulator that under concurrent
// Record both data-races AND silently drops charges (lost-update ->
// non-conservation). It complements (does not duplicate) metering_test.go and
// conservation_test.go, which already pin sequential conservation, per-key
// isolation, payout attribution, lifecycle, and serialized-concurrent safety.
//
// MODEL / SYNCHRONIZATION (audited at metering.go):
//   - Accumulator: per-provider int64 running sum `p.totalUnits` (metering.go:50),
//     keyed in a plain map[string]*provider (metering.go:57). No mutex, no atomic.
//   - Meter does `p.totalUnits += int64(units)` (metering.go:117): a read-modify-
//     write with NO synchronization. units is `int`, guarded > 0 (NaN impossible;
//     negative/zero rejected with ErrNonPositiveUnits).
//   - Market is EXPLICITLY documented "not safe for concurrent use"
//     (metering.go:54-55).
//
// FINDING (honest): this is the costtracker defect class — but here it is a
// DISCLOSED hazard, not a hidden bug. costtracker's accumulator lost updates
// while being used as if safe; metering.go contracts single-goroutine use up
// front. So the correct audit posture is:
//   (a) prove the SEQUENTIAL contract is exactly conservative (no drop/double/
//       overflow/precision-drift) — the property the package actually promises;
//   (b) prove the documented hazard is REAL and reproducible (the unsynchronized
//       RMW at metering.go:117 races and under-bills under concurrent Meter),
//       pinning the "not safe for concurrent use" doc to observable reality so it
//       can never silently become a false claim of safety.
// No production code is modified; any decision to add a sync.Mutex (which would
// promote Market to concurrency-safe) is left to the coordinator.

// kahanOracle sums float64s with compensated (Kahan) summation as an independent
// high-accuracy reference for Payout's float arithmetic.
func kahanOracle(vals []float64) float64 {
	var sum, c float64
	for _, v := range vals {
		y := v - c
		t := sum + y
		c = (t - sum) - y
		sum = t
	}
	return sum
}

// TestAdversarial_PrecisionLongMixedMagnitude feeds a long stream of mixed-
// magnitude unit counts (interleaving tiny 1s with large near-int32 values) to
// one provider and asserts the int64 accumulator reproduces the exact arithmetic
// sum with ZERO drift, and that Payout matches a Kahan-compensated float oracle.
//
// ANTI-BLUFF: the expected total is an independent int64 sum of the identical
// stream; the expected payout is recomputed via Kahan summation of per-event
// (units*rate) contributions. A lost/doubled increment, or float drift in the
// running sum, diverges from at least one oracle.
//
// MUTATION: metering.go Meter `+= int64(units)` -> `+= int64(units) - 1` makes
// got short by the event count; `+= 2*int64(units)` doubles it -> got != want.
func TestAdversarial_PrecisionLongMixedMagnitude(t *testing.T) {
	id := runUUID("adversarial-precision-long-mixed-magnitude")
	t.Logf("run-uuid=%s START precision", id)

	const (
		pid          = "prov-precision"
		ratePerUnit  = 0.1 // non-representable in binary float -> stresses precision
		bountyReward = 3.0
		events       = 20000
	)

	m := NewMarket()
	if err := m.Register(pid, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty(pid, "b-precision"); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}

	var wantUnits int64
	contribs := make([]float64, 0, events+1)
	for i := 0; i < events; i++ {
		// Interleave magnitudes: every 7th event is large, else small.
		u := 1 + (i % 5)
		if i%7 == 0 {
			u = 1_000_003 + (i % 11) // large, prime-ish, varied
		}
		if err := m.Meter(pid, u); err != nil {
			t.Fatalf("Meter[%d](%d): %v", i, u, err)
		}
		wantUnits += int64(u)
		contribs = append(contribs, float64(u)*ratePerUnit)
	}

	gotUnits := m.TotalUnits(pid)
	if gotUnits != wantUnits {
		t.Fatalf("PRECISION/CONSERVATION broken: got %d, want %d (delta=%d)",
			gotUnits, wantUnits, gotUnits-wantUnits)
	}

	// Payout float check vs Kahan oracle (oracle includes the flat bounty term).
	contribs = append(contribs, bountyReward)
	wantPayout := kahanOracle(contribs)
	gotPayout := m.Payout(pid, ratePerUnit, bountyReward)
	// Payout computes bountyReward + float64(totalUnits)*rate in ONE multiply, so
	// it is at least as accurate as the per-event oracle; allow <=1 ULP-scale slack.
	diff := math.Abs(gotPayout - wantPayout)
	tol := math.Abs(wantPayout) * 1e-12
	t.Logf("run-uuid=%s events=%d units got=%d want=%d payout got=%.6f kahan=%.6f diff=%.3e tol=%.3e",
		id, events, gotUnits, wantUnits, gotPayout, wantPayout, diff, tol)
	if diff > tol {
		t.Fatalf("payout precision drift: got %.10f, kahan-oracle %.10f, diff %.3e > tol %.3e",
			gotPayout, wantPayout, diff, tol)
	}
	t.Logf("run-uuid=%s END precision OK", id)
}

// TestAdversarial_Int64NoOverflowOnLargeRunningSum proves the running sum keeps
// exact int64 accounting across a total that far exceeds int32 range (so a
// 32-bit accumulator or premature narrowing would wrap/saturate and lose value).
//
// ANTI-BLUFF: want is an independent int64 sum > math.MaxInt32; got must equal
// it bit-for-bit. A narrowing bug (int32 sum, or int return) overflows and the
// equality fails.
func TestAdversarial_Int64NoOverflowOnLargeRunningSum(t *testing.T) {
	id := runUUID("adversarial-int64-no-overflow")
	t.Logf("run-uuid=%s START overflow-guard", id)

	const (
		pid   = "prov-bignum"
		chunk = 2_000_000 // each Meter call
		calls = 3000      // 3000 * 2e6 = 6e9 > MaxInt32 (~2.147e9)
	)
	m := NewMarket()
	if err := m.Register(pid, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty(pid, "b-bignum"); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}

	var want int64
	for i := 0; i < calls; i++ {
		if err := m.Meter(pid, chunk); err != nil {
			t.Fatalf("Meter[%d]: %v", i, err)
		}
		want += int64(chunk)
	}
	got := m.TotalUnits(pid)
	t.Logf("run-uuid=%s got=%d want=%d maxint32=%d (got exceeds int32: %v)",
		id, got, want, int64(math.MaxInt32), want > int64(math.MaxInt32))
	if want <= int64(math.MaxInt32) {
		t.Fatalf("test misconfigured: want %d must exceed MaxInt32 to detect narrowing", want)
	}
	if got != want {
		t.Fatalf("OVERFLOW/NARROWING: got %d, want %d (delta=%d) — running sum lost magnitude",
			got, want, got-want)
	}
	t.Logf("run-uuid=%s END overflow-guard OK", id)
}

// TestAdversarial_MissingAndUnclaimedQueriesAreZero pins the edge contract for
// queries against absent or not-yet-billable providers: an empty/missing key
// reports TotalUnits 0 and Payout 0; a registered-but-unclaimed provider also
// pays 0 (no bounty term, no units) — so usage can never be conjured for a
// provider that never legitimately accrued it.
//
// MUTATION: making TotalUnits return a nonzero default, or Payout pay the bounty
// to an unclaimed provider (drop the `!p.claimed` guard at metering.go:139),
// breaks these zero assertions.
func TestAdversarial_MissingAndUnclaimedQueriesAreZero(t *testing.T) {
	id := runUUID("adversarial-missing-unclaimed-zero")
	t.Logf("run-uuid=%s START zero-queries", id)

	const (
		ratePerUnit  = 2.0
		bountyReward = 100.0
	)
	m := NewMarket()

	// Totally unknown provider.
	if u := m.TotalUnits("ghost"); u != 0 {
		t.Fatalf("TotalUnits(missing): got %d, want 0", u)
	}
	if p := m.Payout("ghost", ratePerUnit, bountyReward); p != 0 {
		t.Fatalf("Payout(missing): got %v, want 0 (no provider, no bounty)", p)
	}

	// Registered but never claimed: no claim => no bounty reward, no units.
	const reg = "prov-registered-only"
	if err := m.Register(reg, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if u := m.TotalUnits(reg); u != 0 {
		t.Fatalf("TotalUnits(registered, unclaimed): got %d, want 0", u)
	}
	if p := m.Payout(reg, ratePerUnit, bountyReward); p != 0 {
		t.Fatalf("Payout(registered, unclaimed): got %v, want 0 (bounty must NOT pay without a claim)", p)
	}
	t.Logf("run-uuid=%s END zero-queries OK", id)
}

// TestAdversarial_NaNRateDoesNotCorruptUnits is a defensive contract probe: even
// if a caller passes a NaN rate to Payout (a float-domain poison), the integer
// unit ledger is untouched and still reports the exact conserved total. This
// proves usage accounting (the revenue-critical integer invariant) is isolated
// from float poisoning in the read-only Payout projection.
func TestAdversarial_NaNRateDoesNotCorruptUnits(t *testing.T) {
	id := runUUID("adversarial-nan-rate-isolation")
	t.Logf("run-uuid=%s START nan-rate", id)

	const pid = "prov-nan"
	m := NewMarket()
	if err := m.Register(pid, true); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.ClaimBounty(pid, "b-nan"); err != nil {
		t.Fatalf("ClaimBounty: %v", err)
	}
	units := []int{4, 6, 5}
	var want int64
	for _, u := range units {
		if err := m.Meter(pid, u); err != nil {
			t.Fatalf("Meter(%d): %v", u, err)
		}
		want += int64(u)
	}

	// Poison the float projection.
	pay := m.Payout(pid, math.NaN(), 1.0)
	if !math.IsNaN(pay) {
		// bountyReward + units*NaN = NaN; this is the expected float-domain result.
		t.Fatalf("Payout with NaN rate: got %v, want NaN (float poison propagates)", pay)
	}
	// The integer ledger MUST be intact and exactly conserved despite the poison.
	if got := m.TotalUnits(pid); got != want {
		t.Fatalf("unit ledger corrupted by NaN-rate Payout: got %d, want %d", got, want)
	}
	// A subsequent clean Payout recovers an exact finite value.
	clean := m.Payout(pid, 0.5, 2.0)
	wantClean := 2.0 + float64(want)*0.5
	t.Logf("run-uuid=%s units=%d nan-payout=%v clean-payout=%v want=%v", id, want, pay, clean, wantClean)
	if clean != wantClean {
		t.Fatalf("clean Payout after NaN call: got %v, want %v", clean, wantClean)
	}
	t.Logf("run-uuid=%s END nan-rate OK", id)
}

// TestAdversarial_DocumentedRaceIsRealUnderRace is the centerpiece tying this
// audit to the costtracker incident. The unsynchronized RMW at metering.go:117
// is the IDENTICAL hazard. Because Market is documented "not safe for concurrent
// use", we do NOT race the in-process detector against the package (that would
// mark THIS suite failed for exercising disallowed usage). Instead we re-exec a
// child `go test` that runs the unsynchronized-concurrency demonstration helper
// (below) UNDER -race, and assert the child reports a DATA RACE on Meter.
//
// This pins the doc claim to observable reality: if someone later adds a
// sync.Mutex (promoting Market to safe), the child stops racing and this test
// fails LOUDLY, forcing the "not safe for concurrent use" doc to be corrected in
// the same change — no silent drift between doc and code in EITHER direction.
//
// ANTI-BLUFF: the helper does the exact lost-update pattern (many goroutines,
// same provider, no caller lock). Under -race the runtime is guaranteed to flag
// the concurrent read/write of p.totalUnits at metering.go:117. A green child is
// only possible if the production accumulator became synchronized.
func TestAdversarial_DocumentedRaceIsRealUnderRace(t *testing.T) {
	id := runUUID("adversarial-documented-race-is-real")

	if os.Getenv("METERING_RACE_CHILD") == "1" {
		// Child role: perform the documented-unsafe concurrent Meter so the
		// parent's `go test -race` invocation can observe the race. This runs as
		// a normal test function selected by name; it intentionally omits any
		// caller-side serialization.
		runUnsynchronizedConcurrentMeter()
		return
	}

	t.Logf("run-uuid=%s START documented-race (parent re-exec under -race)", id)

	// Re-exec ourselves: `go test -race -run TestAdversarial_DocumentedRaceIsRealUnderRace`
	// with the child env set. The child triggers the race; -race makes the child
	// process exit non-zero and print "DATA RACE" + the offending file:line.
	cmd := exec.Command("go", "test", "-race", "-count=1",
		"-run", "^TestAdversarial_DocumentedRaceIsRealUnderRace$", ".")
	cmd.Env = append(os.Environ(), "METERING_RACE_CHILD=1")
	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("run-uuid=%s child exit-err=%v", id, err)

	racedMeter := strings.Contains(output, "DATA RACE") &&
		strings.Contains(output, "metering.go:117")
	if !racedMeter {
		t.Fatalf("DOC-DRIFT: expected child to report a DATA RACE at metering.go:117 for "+
			"unsynchronized concurrent Meter (the documented 'not safe for concurrent use' "+
			"hazard). It did NOT. Either Market became concurrency-safe (update the doc + this "+
			"test) or the demonstration regressed.\nchild err=%v\n--- child output (head) ---\n%s",
			err, headLines(output, 40))
	}
	if err == nil {
		t.Fatalf("child `go test -race` exited 0 despite a race — race build not effective")
	}
	t.Logf("run-uuid=%s END documented-race OK (race observed at metering.go:117, child failed as required)", id)
}

// runUnsynchronizedConcurrentMeter performs the costtracker-class lost-update
// pattern: many goroutines call Meter on ONE provider with NO caller lock. It is
// the body executed by the re-exec'd child above, under -race.
func runUnsynchronizedConcurrentMeter() {
	m := NewMarket()
	const pid = "p"
	_ = m.Register(pid, true)
	_ = m.ClaimBounty(pid, "b")
	const goroutines, perWorker, units = 16, 1500, 1
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				_ = m.Meter(pid, units) // UNSYNCHRONIZED — the documented hazard
			}
		}()
	}
	wg.Wait()
	_ = m.TotalUnits(pid)
}

// headLines returns at most n lines of s for compact failure diagnostics.
func headLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
