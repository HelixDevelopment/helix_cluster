package balancemonitor

// Adversarial SECOND-PASS probes (sink-side). The first pass pinned the `<`
// guard, NaN/Inf wire-rejection, the NaN-floor latent risk, event-log
// conservation, the -race lock, Events() copy isolation, and duplicate-RunID
// double-counting. This pass closes the remaining UNPINNED sink contracts in
// the dual-field billing decoder — the spot most likely to harbor a silent
// "wrong verdict" regression because it is where the verdict's INPUT is chosen.
//
// Mapping the parent bug taxonomy onto this package (an OpenAI-style billing
// *balance monitor*, not a load-imbalance ratio monitor):
//
//   (A) ALIASING of returned state .... Events() copy already pinned; Event is a
//       flat value struct with no pointer/slice/map fields, so no deeper alias
//       channel exists. Re-asserted here that two snapshots are independent AND
//       that mutating a returned Reading cannot reach back into the log.
//   (B) NaN/Inf/overflow poison ....... wire-rejection pinned in pass 1; this
//       file pins the COMPLEMENT: the dual-field SELECTION that feeds the only
//       value the verdict ever sees.
//   (C) comparator total-order ........ the verdict is a single `<`; pinned.
//   (D) wrong balanced/imbalanced verdict (SINK) ... the genuinely-uncovered
//       gap: the `total_available` fallback field driving a LOW verdict AND its
//       warning event. Pass 1 only exercised total_available ABOVE the floor
//       (TestTotalAvailableField), so a mutation that mis-wired the fallback
//       (e.g. `case br.TotalAvailable != nil: balanceUSD = 0`) would emit a
//       silent "healthy" reading for an empty account and NO existing test
//       would catch it. These tests bite that.
//
// VERDICT for the package: NO new always-on bug found. All probes pass on
// BYTE-IDENTICAL production code; they are contract pins, not reproducers.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveRawBody2 writes exact bytes for GET /billing/balance so we can pin the
// decoder's field-selection contract with literal JSON shapes.
func serveRawBody2(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing/balance" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func warningCount(evs []Event) int {
	n := 0
	for _, e := range evs {
		if e.Kind == "low_balance_warning" {
			n++
		}
	}
	return n
}

// TestTotalAvailableDrivesLowVerdict pins finding (D): the `total_available`
// FALLBACK field must drive the low-balance verdict AND emit a warning exactly
// like the primary `balance` field. Pass 1 only checked total_available ABOVE
// the floor, leaving the alarm-on-fallback path unproven.
//
// SINK-SIDE: r.LowBalance==true AND exactly one low_balance_warning event for a
// total_available value strictly below the floor. A mutation that drops or
// mis-wires the `case br.TotalAvailable != nil` branch (e.g. zeroes the value,
// or skips the warning) produces a silent healthy reading -> this FAILS.
func TestTotalAvailableDrivesLowVerdict(t *testing.T) {
	const floorUSD = 10.00

	srv := serveRawBody2(t, `{"total_available": 2.50}`) // strictly below floor
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-ta-low", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-ta-low")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if r.BalanceUSD != 2.50 {
		t.Errorf("BalanceUSD: got %v, want 2.50 (total_available must populate the verdict input)", r.BalanceUSD)
	}
	if !r.LowBalance {
		t.Errorf("LowBalance: got false, want true (total_available %.2f < floor %.2f)", r.BalanceUSD, floorUSD)
	}
	evs := m.Events()
	if got := warningCount(evs); got != 1 {
		t.Errorf("low_balance_warning via total_available: got %d, want 1", got)
	}
	if len(evs) != 2 {
		t.Errorf("event log: got %d events, want 2 (reading + warning)", len(evs))
	}
}

// TestBalanceFieldTakesPrecedence pins the decoder's precedence contract: when
// BOTH `balance` and `total_available` are present, `balance` WINS (the switch
// tests br.Balance first). This is a real, observable contract — a verdict can
// flip if precedence is reordered. Here `balance` is LOW while total_available
// is HIGH: the verdict MUST follow `balance` (low). Reordering the switch cases
// would make this read healthy -> FAIL.
func TestBalanceFieldTakesPrecedence(t *testing.T) {
	const floorUSD = 10.00

	// balance below floor, total_available far above it.
	srv := serveRawBody2(t, `{"balance": 3.00, "total_available": 999.00}`)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-prec", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-prec")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if r.BalanceUSD != 3.00 {
		t.Errorf("precedence: got BalanceUSD=%v, want 3.00 (`balance` must win over `total_available`)", r.BalanceUSD)
	}
	if !r.LowBalance {
		t.Errorf("precedence: LowBalance=false; `balance`=3.00 wins and is below floor %.2f so it must be low", floorUSD)
	}
	if got := warningCount(m.Events()); got != 1 {
		t.Errorf("precedence: low_balance_warning=%d, want 1", got)
	}
}

// TestNullBalanceFallsThroughToTotalAvailable pins that a JSON `null` for
// `balance` leaves its *float64 nil and the decoder FALLS THROUGH to
// total_available (rather than erroring or treating null as 0.0 -> a spurious
// low/healthy verdict). Here total_available is HIGH, so the correct sink-side
// result is a healthy (not-low) reading with no warning.
func TestNullBalanceFallsThroughToTotalAvailable(t *testing.T) {
	const floorUSD = 10.00

	srv := serveRawBody2(t, `{"balance": null, "total_available": 42.00}`)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-null", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-null")
	if err != nil {
		t.Fatalf("GetBalance: %v (null balance must fall through to total_available, not error)", err)
	}
	if r.BalanceUSD != 42.00 {
		t.Errorf("null fallthrough: got BalanceUSD=%v, want 42.00", r.BalanceUSD)
	}
	if r.LowBalance {
		t.Errorf("null fallthrough: LowBalance=true; 42.00 is above floor %.2f so must be healthy", floorUSD)
	}
	if got := warningCount(m.Events()); got != 0 {
		t.Errorf("null fallthrough: low_balance_warning=%d, want 0", got)
	}
}

// TestBothFieldsNullErrorsNoBalance pins that when BOTH fields are JSON `null`
// (both *float64 nil), the decoder reports ErrNoBalance and records NOTHING —
// it must NOT silently fabricate a 0.0 balance that would read as low against a
// positive floor (a fabricated alarm) or be stamped into the log.
func TestBothFieldsNullErrorsNoBalance(t *testing.T) {
	srv := serveRawBody2(t, `{"balance": null, "total_available": null}`)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-both-null", 10.0, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-both-null")
	if err != ErrNoBalance {
		t.Fatalf("both-null: got (balance=%v err=%v), want ErrNoBalance", r.BalanceUSD, err)
	}
	if evs := m.Events(); len(evs) != 0 {
		t.Errorf("both-null: recorded %d event(s); a missing-balance error must leave the log untouched", len(evs))
	}
}

// TestReturnedReadingCannotMutateLog re-arms the (A) ALIASING bite from a
// different angle than TestEventsSnapshotIsACopy: mutate the RETURNED Reading
// and confirm the in-log Event is unaffected (Reading and Event are distinct
// value structs; no shared backing). Also confirms two Events() snapshots are
// mutually independent.
func TestReturnedReadingCannotMutateLog(t *testing.T) {
	const floorUSD = 10.00
	srv, _ := serveBalance(t, 3.00) // low -> reading + warning recorded
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-alias2", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-alias2")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}

	// Tamper with the returned Reading.
	r.BalanceUSD = -777.0
	r.LowBalance = false
	r.RunID = "tampered"

	// The log must still reflect the true recorded values.
	evs := m.Events()
	if len(evs) == 0 {
		t.Fatal("expected recorded events")
	}
	for _, e := range evs {
		if e.RunID != "run-alias2" {
			t.Errorf("log RunID corrupted by Reading mutation: got %q", e.RunID)
		}
		if e.Balance != 3.00 {
			t.Errorf("log Balance corrupted by Reading mutation: got %v, want 3.00", e.Balance)
		}
	}

	// Two snapshots must be mutually independent.
	a := m.Events()
	b := m.Events()
	a[0].Kind = "tampered-snapshot"
	if b[0].Kind == "tampered-snapshot" {
		t.Error("two Events() snapshots share backing storage")
	}
}
