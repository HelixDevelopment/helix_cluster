package balancemonitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixedNow is the deterministic clock injected into every test Monitor so that
// Reading.At is predictable and can be asserted on.
var fixedNow = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return fixedNow }

// serveBalance spins up an httptest.Server that responds to GET /billing/balance
// with the given USD amount encoded under "balance".  It also captures the
// Authorization header so tests can assert the API key was forwarded.
func serveBalance(t *testing.T, usd float64) (srv *httptest.Server, gotAuthHeader *string) {
	t.Helper()
	hdr := ""
	gotAuthHeader = &hdr
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing/balance" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		hdr = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]float64{"balance": usd})
	}))
	return srv, gotAuthHeader
}

// TestBalanceParsedAndStamped verifies that a balance above the floor is parsed
// correctly, that LowBalance is false, and that the RunID is embedded in both
// the returned Reading and the event log entry.
//
// Mutation guard: the low := balanceUSD < m.floorUSD line in balancemonitor.go.
// This test fails if that guard is inverted (< → >) because LowBalance would
// wrongly be true for a balance above the floor.
func TestBalanceParsedAndStamped(t *testing.T) {
	const (
		wantBalance = 99.50
		floorUSD    = 10.00
		runID       = "run-abc-001"
		apiKey      = "sk-test-key-1"
	)

	srv, gotAuth := serveBalance(t, wantBalance)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, apiKey, floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetBalance returned unexpected error: %v", err)
	}

	// --- sink-side assertions on the returned Reading ---

	if r.BalanceUSD != wantBalance {
		t.Errorf("BalanceUSD: got %v, want %v", r.BalanceUSD, wantBalance)
	}
	if r.LowBalance {
		t.Errorf("LowBalance: got true, want false (balance %.2f is above floor %.2f)", r.BalanceUSD, floorUSD)
	}
	if r.RunID != runID {
		t.Errorf("RunID: got %q, want %q", r.RunID, runID)
	}
	if !r.At.Equal(fixedNow) {
		t.Errorf("At: got %v, want %v", r.At, fixedNow)
	}

	// --- sink-side assertions on the event log ---

	events := m.Events()
	if len(events) != 1 {
		t.Fatalf("event log: got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Kind != "reading" {
		t.Errorf("event Kind: got %q, want %q", ev.Kind, "reading")
	}
	if ev.RunID != runID {
		t.Errorf("event RunID: got %q, want %q", ev.RunID, runID)
	}
	if ev.Balance != wantBalance {
		t.Errorf("event Balance: got %v, want %v", ev.Balance, wantBalance)
	}
	if !ev.At.Equal(fixedNow) {
		t.Errorf("event At: got %v, want %v", ev.At, fixedNow)
	}

	// --- the API key must have been forwarded ---
	wantAuth := "Bearer " + apiKey
	if *gotAuth != wantAuth {
		t.Errorf("Authorization header: got %q, want %q", *gotAuth, wantAuth)
	}
}

// TestLowBalanceWarns verifies that a balance strictly below the floor sets
// LowBalance=true and causes an additional "low_balance_warning" event to
// appear in the event log alongside the "reading" event.
//
// MUTATION-KILL TARGET: the guard `low := balanceUSD < m.floorUSD` in
// balancemonitor.go.  Inverting < to > or changing it to <= (when balance
// equals floor) makes this test fail because:
//   - LowBalance will be false when it must be true, OR
//   - the low_balance_warning event will not be emitted.
func TestLowBalanceWarns(t *testing.T) {
	const (
		wantBalance = 4.75  // strictly below floor
		floorUSD    = 10.00 // floor
		runID       = "run-xyz-002"
		apiKey      = "sk-test-key-2"
	)

	srv, _ := serveBalance(t, wantBalance)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, apiKey, floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetBalance returned unexpected error: %v", err)
	}

	// --- Reading must reflect the low-balance condition ---

	if r.BalanceUSD != wantBalance {
		t.Errorf("BalanceUSD: got %v, want %v", r.BalanceUSD, wantBalance)
	}
	// CRITICAL ASSERTION: this is what the mutation guard protects.
	// Inverting the < in balancemonitor.go makes LowBalance false here.
	if !r.LowBalance {
		t.Errorf("LowBalance: got false, want true (balance %.2f is below floor %.2f)", r.BalanceUSD, floorUSD)
	}
	if r.RunID != runID {
		t.Errorf("RunID: got %q, want %q", r.RunID, runID)
	}

	// --- event log must contain both a reading and a warning ---

	events := m.Events()
	if len(events) != 2 {
		t.Fatalf("event log: got %d events, want 2 (reading + low_balance_warning)", len(events))
	}

	readingEvent := events[0]
	if readingEvent.Kind != "reading" {
		t.Errorf("events[0].Kind: got %q, want %q", readingEvent.Kind, "reading")
	}
	if readingEvent.RunID != runID {
		t.Errorf("events[0].RunID: got %q, want %q", readingEvent.RunID, runID)
	}
	if readingEvent.Balance != wantBalance {
		t.Errorf("events[0].Balance: got %v, want %v", readingEvent.Balance, wantBalance)
	}

	warnEvent := events[1]
	if warnEvent.Kind != "low_balance_warning" {
		t.Errorf("events[1].Kind: got %q, want %q", warnEvent.Kind, "low_balance_warning")
	}
	if warnEvent.RunID != runID {
		t.Errorf("events[1].RunID: got %q, want %q", warnEvent.RunID, runID)
	}
	if warnEvent.Balance != wantBalance {
		t.Errorf("events[1].Balance: got %v, want %v", warnEvent.Balance, wantBalance)
	}
}

// TestExactFloorIsNotLow confirms the strict-less-than semantics: a balance
// exactly equal to the floor must NOT trigger LowBalance.
func TestExactFloorIsNotLow(t *testing.T) {
	const (
		floorUSD = 10.00
		runID    = "run-boundary-003"
	)

	srv, _ := serveBalance(t, floorUSD) // balance == floor exactly
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-floor", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if r.LowBalance {
		t.Errorf("LowBalance: got true for balance==floor (%.2f), want false (strict <)", r.BalanceUSD)
	}

	events := m.Events()
	if len(events) != 1 {
		t.Errorf("event log: got %d events, want 1 (no low_balance_warning at exact floor)", len(events))
	}
}

// TestTotalAvailableField checks the alternative JSON field name accepted by
// the parser so that APIs that expose "total_available" instead of "balance"
// are handled correctly.
func TestTotalAvailableField(t *testing.T) {
	const (
		wantBalance = 55.00
		floorUSD    = 10.00
		runID       = "run-alt-004"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]float64{"total_available": wantBalance})
	}))
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-alt", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if r.BalanceUSD != wantBalance {
		t.Errorf("BalanceUSD: got %v, want %v", r.BalanceUSD, wantBalance)
	}
	if r.RunID != runID {
		t.Errorf("RunID: got %q, want %q", r.RunID, runID)
	}
}

// TestMissingBalanceFieldErrors verifies that ErrNoBalance is returned when
// neither "balance" nor "total_available" is present in the response.
func TestMissingBalanceFieldErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-no-bal", 5.0, fixedClock, srv.Client())

	_, err := m.GetBalance(context.Background(), "run-err-005")
	if err == nil {
		t.Fatal("expected ErrNoBalance, got nil")
	}
	if err != ErrNoBalance {
		t.Errorf("error: got %v, want ErrNoBalance", err)
	}
}

// TestHTTPErrorPropagates checks that a non-2xx status from the server is
// returned as an error, not silently swallowed.
func TestHTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-bad", 5.0, fixedClock, srv.Client())

	_, err := m.GetBalance(context.Background(), "run-http-006")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

// TestEventsSnapshotIsACopy confirms that Events() returns an independent copy
// so mutations by the caller do not corrupt the internal log.
func TestEventsSnapshotIsACopy(t *testing.T) {
	const wantBalance = 20.0
	srv, _ := serveBalance(t, wantBalance)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-copy", 10.0, fixedClock, srv.Client())
	_, _ = m.GetBalance(context.Background(), "run-copy-007")

	snap := m.Events()
	snap[0].Kind = "tampered"

	fresh := m.Events()
	if fresh[0].Kind == "tampered" {
		t.Error("Events() returned a reference to the internal slice; mutations leaked back")
	}
}
