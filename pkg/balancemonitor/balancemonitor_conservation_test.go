package balancemonitor

// Adversarial accounting-correctness + race tests for the Monitor event log.
//
// Domain mapping (this package is an OpenAI-style billing *balance monitor*,
// not a debit/credit ledger). The conservation invariant therefore lives in
// the event log, which is the package's shared accounting state:
//
//	Every successful GetBalance appends EXACTLY ONE "reading" event, and
//	EXACTLY ONE additional "low_balance_warning" event iff the reading was
//	strictly below the floor. Nothing else writes events.
//
// So the independent running sum (the conservation oracle) is:
//
//	len(events) == Nsuccess + Nlow
//	count(Kind=="reading")             == Nsuccess
//	count(Kind=="low_balance_warning") == Nlow
//
// where Nsuccess and Nlow are tallied independently from the call results.
//
// The shared state (m.events) is mutated under m.mu in GetBalance and read
// under m.mu in Events(). That lock is the load-bearing synchronization: it
// is the equivalent of a balance RMW lock in a ledger. A lost/duplicated
// append (a balance-accounting race) breaks conservation; the -race detector
// independently flags the unsynchronized slice append.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// serveDynamicBalance returns a server whose /billing/balance response is
// computed per request by balanceFor(callIndex). The per-request call index
// lets each goroutine receive a deterministic, distinct balance so the test
// can tally the expected low/high split independently of the Monitor.
func serveDynamicBalance(t *testing.T, balanceFor func(i int) float64) *httptest.Server {
	t.Helper()
	var counter int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing/balance" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		i := int(atomic.AddInt64(&counter, 1) - 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]float64{"balance": balanceFor(i)})
	}))
	return srv
}

// TestEventLogConservationSerial drives many randomized-but-deterministic
// readings serially and asserts the event log conserves EXACTLY:
//
//	len(events)             == N + (#low readings)
//	#reading events         == N
//	#low_balance_warning    == #readings strictly below the floor
//
// ANTI-BLUFF: the expected counts are computed from an INDEPENDENT running
// tally (wantReadings / wantWarnings) accumulated from each call's returned
// Reading, never from m.Events(). If GetBalance dropped a "reading" append,
// double-appended, or emitted a warning on the wrong side of the floor, the
// independent tally and the event log would diverge and this test fails.
//
// MUTATION that bites:
//   - Drop the `m.events = append(... "reading" ...)` line  -> #reading < N -> FAIL.
//   - Duplicate that append                                 -> #reading > N -> FAIL.
//   - Flip `low := balanceUSD < m.floorUSD` to <= or >      -> warning count
//     diverges from the independently-tallied wantWarnings  -> FAIL.
func TestEventLogConservationSerial(t *testing.T) {
	const (
		floorUSD = 10.00
		N        = 500
		apiKey   = "sk-conservation-serial"
	)

	// Deterministic pseudo-random balances straddling the floor on both sides
	// and hitting the exact boundary, with a fixed multiplier sequence.
	balanceFor := func(i int) float64 {
		// values in cents 0..1999 -> 0.00 .. 19.99, around the 10.00 floor,
		// and every 17th sample lands exactly on the floor to exercise the edge.
		if i%17 == 0 {
			return floorUSD
		}
		cents := (i*37 + 3) % 2000
		return float64(cents) / 100.0
	}

	srv := serveDynamicBalance(t, balanceFor)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, apiKey, floorUSD, fixedClock, srv.Client())

	// Independent conservation oracle.
	var wantReadings, wantWarnings int
	for i := 0; i < N; i++ {
		r, err := m.GetBalance(context.Background(), "run-serial")
		if err != nil {
			t.Fatalf("GetBalance #%d: %v", i, err)
		}
		wantReadings++
		// Independent low determination from the returned balance: strict <.
		if r.BalanceUSD < floorUSD {
			wantWarnings++
		}
		// Cross-check: the Monitor's own LowBalance must agree with the
		// independent strict-< oracle at every single reading. This bites a
		// flipped comparison even before we look at the event log.
		if got, want := r.LowBalance, r.BalanceUSD < floorUSD; got != want {
			t.Fatalf("reading #%d balance=%.2f floor=%.2f: LowBalance=%v, independent oracle=%v",
				i, r.BalanceUSD, floorUSD, got, want)
		}
	}

	events := m.Events()

	var gotReadings, gotWarnings int
	for _, e := range events {
		switch e.Kind {
		case "reading":
			gotReadings++
		case "low_balance_warning":
			gotWarnings++
		default:
			t.Fatalf("unexpected event Kind %q", e.Kind)
		}
	}

	if gotReadings != wantReadings {
		t.Errorf("CONSERVATION VIOLATION: reading events: got %d, want %d (one per successful call)",
			gotReadings, wantReadings)
	}
	if gotWarnings != wantWarnings {
		t.Errorf("CONSERVATION VIOLATION: low_balance_warning events: got %d, want %d (one per strictly-below-floor reading)",
			gotWarnings, wantWarnings)
	}
	if total, want := len(events), wantReadings+wantWarnings; total != want {
		t.Errorf("CONSERVATION VIOLATION: total events: got %d, want %d (= readings + warnings)",
			total, want)
	}
}

// TestEventLogConservationConcurrent is the -race teeth. Many goroutines call
// GetBalance concurrently against a real httptest server. The event log must
// STILL conserve exactly:
//
//	#reading events      == number of successful calls
//	#low_balance_warning == number of strictly-below-floor readings
//	len(events)          == sum of the two
//
// ANTI-BLUFF — two independent bites:
//
//  1. Conservation bite: each goroutine reports back its own (success, low)
//     tally via atomic counters that NEVER touch m.events. The event-log
//     counts are compared against those atomics. An unsynchronized append in
//     GetBalance loses concurrent writes (two goroutines read the same slice
//     header, one append clobbers the other) -> #reading < successes ->
//     conservation FAILS deterministically under load.
//
//  2. Race bite: `go test -race` instruments every memory access. With the
//     m.mu lock removed from GetBalance/Events(), the concurrent
//     append-to-shared-slice and the snapshot read form a data race the
//     detector reports, failing the test even on runs where the counts happen
//     to line up.
//
// MUTATION that bites: delete `m.mu.Lock()/Unlock()` around the append in
// GetBalance (and/or in Events()) -> -race reports a data race here AND the
// conservation counts diverge under -count load. Re-adding the lock is what
// makes both bites green, proving the lock is load-bearing.
func TestEventLogConservationConcurrent(t *testing.T) {
	const (
		floorUSD       = 10.00
		goroutines     = 64
		perGoroutine   = 40
		apiKey         = "sk-conservation-concurrent"
		totalSuccesses = goroutines * perGoroutine
	)

	// Half the served balances are below the floor, half above, deterministic
	// by request index, so a meaningful number of warnings are produced.
	balanceFor := func(i int) float64 {
		if i%2 == 0 {
			return 3.00 // below floor -> low
		}
		return 42.00 // above floor -> not low
	}

	srv := serveDynamicBalance(t, balanceFor)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, apiKey, floorUSD, fixedClock, srv.Client())

	var (
		gotSuccess int64 // independent: # successful GetBalance calls
		gotLow     int64 // independent: # readings strictly below floor
		wg         sync.WaitGroup
		startGate  = make(chan struct{})
	)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			<-startGate // maximize contention: release all goroutines at once
			for j := 0; j < perGoroutine; j++ {
				r, err := m.GetBalance(context.Background(), "run-concurrent")
				if err != nil {
					// Record nothing on error; a 5xx/transport hiccup must not
					// count as a success in the oracle. (None expected here.)
					t.Errorf("concurrent GetBalance error: %v", err)
					return
				}
				atomic.AddInt64(&gotSuccess, 1)
				if r.BalanceUSD < floorUSD {
					atomic.AddInt64(&gotLow, 1)
				}
			}
		}()
	}
	close(startGate)
	wg.Wait()

	wantSuccess := atomic.LoadInt64(&gotSuccess)
	wantLow := atomic.LoadInt64(&gotLow)

	if wantSuccess != totalSuccesses {
		t.Fatalf("oracle sanity: successes=%d, want %d (no call should have failed)",
			wantSuccess, totalSuccesses)
	}

	events := m.Events()

	var gotReadings, gotWarnings int64
	for _, e := range events {
		switch e.Kind {
		case "reading":
			gotReadings++
		case "low_balance_warning":
			gotWarnings++
		default:
			t.Fatalf("unexpected event Kind %q", e.Kind)
		}
	}

	// Conservation: the event log must EXACTLY match the independent atomics.
	if gotReadings != wantSuccess {
		t.Errorf("CONSERVATION VIOLATION (concurrent): reading events: got %d, want %d "+
			"(lost/duplicated append under concurrency -> accounting race)",
			gotReadings, wantSuccess)
	}
	if gotWarnings != wantLow {
		t.Errorf("CONSERVATION VIOLATION (concurrent): low_balance_warning events: got %d, want %d",
			gotWarnings, wantLow)
	}
	if total, want := int64(len(events)), wantSuccess+wantLow; total != want {
		t.Errorf("CONSERVATION VIOLATION (concurrent): total events: got %d, want %d (= readings + warnings)",
			total, want)
	}
}

// TestLowBalanceBoundaryExact pins the off-by-one boundary of the alert at the
// exact floor and one cent on each side, under the documented strict-< rule.
//
// ANTI-BLUFF: asserts all three of {below, exact, above} in one test so a
// comparison mutation cannot satisfy the boundary by coincidence:
//   - `<`  (correct): below=low, exact=NOT low, above=NOT low.
//   - `<=` (mutation): exact would wrongly be low                -> FAIL on exact.
//   - `>`  (mutation): above would wrongly be low / below not    -> FAIL.
//   - `<` with floor off by a cent: below or exact flips         -> FAIL.
func TestLowBalanceBoundaryExact(t *testing.T) {
	const floorUSD = 10.00

	cases := []struct {
		name    string
		balance float64
		wantLow bool
	}{
		{"one_cent_below_floor", 9.99, true},
		{"exact_floor", 10.00, false}, // strict < => equal is NOT low
		{"one_cent_above_floor", 10.01, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serveBalance(t, tc.balance)
			defer srv.Close()

			m := newMonitorWithClock(srv.URL, "sk-boundary", floorUSD, fixedClock, srv.Client())

			r, err := m.GetBalance(context.Background(), "run-boundary")
			if err != nil {
				t.Fatalf("GetBalance: %v", err)
			}
			if r.LowBalance != tc.wantLow {
				t.Errorf("balance=%.2f floor=%.2f: LowBalance=%v, want %v (strict-< boundary)",
					tc.balance, floorUSD, r.LowBalance, tc.wantLow)
			}

			// Event-log side must agree: a warning event appears iff low.
			events := m.Events()
			var warnings int
			for _, e := range events {
				if e.Kind == "low_balance_warning" {
					warnings++
				}
			}
			wantWarnings := 0
			if tc.wantLow {
				wantWarnings = 1
			}
			if warnings != wantWarnings {
				t.Errorf("balance=%.2f: low_balance_warning events=%d, want %d",
					tc.balance, warnings, wantWarnings)
			}
		})
	}
}

// TestZeroAndNegativeBalanceAreLow covers the edge cases at and below zero.
// The documented rule is purely `balance < floor`; a zero or negative
// (overdraft) balance is far below any positive floor and MUST be low. The
// package does not reject negative balances (it has no overdraft guard), so
// the honest assertion is: negative parses fine and is flagged low.
func TestZeroAndNegativeBalanceAreLow(t *testing.T) {
	const floorUSD = 10.00

	for _, bal := range []float64{0.0, -5.25} {
		srv, _ := serveBalance(t, bal)

		m := newMonitorWithClock(srv.URL, "sk-zero", floorUSD, fixedClock, srv.Client())
		r, err := m.GetBalance(context.Background(), "run-zero")
		srv.Close()
		if err != nil {
			t.Fatalf("GetBalance(balance=%.2f): %v", bal, err)
		}
		if r.BalanceUSD != bal {
			t.Errorf("BalanceUSD: got %v, want %v", r.BalanceUSD, bal)
		}
		if !r.LowBalance {
			t.Errorf("balance=%.2f floor=%.2f: LowBalance=false, want true", bal, floorUSD)
		}
	}
}
