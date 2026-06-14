package balancemonitor

// Adversarial, sink-side probes for the numeric-poison / silent-alarm bug class.
//
// Threat model (tonight's recurring numeric bug): a NaN/±Inf or overflow value
// slips a `< floor`-only guard and silently defeats the low-balance ALARM — the
// alarm that MUST fire stays silent (NaN < x is ALWAYS false), or fires when it
// must not. We assert SINK-SIDE: the Monitor's observable verdict
// (r.LowBalance + the presence/absence of a "low_balance_warning" event), never
// merely "did not panic".
//
// FINDINGS (three-way discrimination, honest):
//
//   (1) WIRE NaN/±Inf INJECTION  -> NO BUG (protective contract worth pinning).
//       The only prod path that populates BalanceUSD is the HTTP+JSON decode in
//       GetBalance (json.NewDecoder(resp.Body).Decode(&br)). encoding/json
//       REJECTS the literal tokens NaN / Infinity and REJECTS float overflow
//       (1e309+) with a decode error, which GetBalance surfaces (returns the
//       error, records NO event) BEFORE the `balanceUSD < m.floorUSD` guard is
//       ever reached. So a hostile server CANNOT push NaN/±Inf into the alarm
//       comparison. TestWireCannotInjectNaNOrInf pins this: every hostile body
//       yields (err != nil, zero events) — never a silent healthy reading.
//
//   (2) NaN FLOOR (caller-supplied)  -> LATENT RISK (note, no fix here).
//       floorUSD comes from the exported NewMonitor(_, _, floorUSD). If a caller
//       passes floorUSD = NaN, then `balanceUSD < NaN` is ALWAYS false, so the
//       low-balance alarm NEVER fires for ANY balance, however empty the
//       account. That is a real silent-alarm hazard — BUT the documented
//       contract only promises `LowBalance == (BalanceUSD < floorUSD)` and makes
//       no NaN claim, a NaN floor is a caller programming error, and there are
//       ZERO importers of this package (blast radius nil). Per the protocol this
//       is a LATENT RISK: TestNaNFloorSilencesAlarm_LatentRisk PINS the CURRENT
//       observed behavior (alarm stays silent) so a future floor-sanitizing fix
//       is a deliberate, visible contract change rather than an accident. It is
//       NOT gated behind any env var and passes on unfixed prod.
//
//   (3) CONSERVATION UNDER CONCURRENCY  -> exercised here too with -race to keep
//       the shared event-log lock honest (complements the conservation_test.go).
//
//   (4) IDENTITY / DUPLICATE-RUNID EDGE -> duplicate RunIDs are double-counted by
//       design (one event-pair per call); pinned so a dedup regression is caught.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// serveRawBody returns a server that writes the exact bytes `body` for
// GET /billing/balance, letting us inject hostile JSON the std encoder would
// never itself emit (NaN/Infinity tokens, overflow literals).
func serveRawBody(t *testing.T, body string) *httptest.Server {
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

// TestWireCannotInjectNaNOrInf is the (a) NUMERIC-POISON sink probe.
//
// For EVERY hostile wire encoding of NaN/±Inf/overflow, prod MUST refuse to
// produce a Reading: GetBalance returns an error AND records zero events. The
// failure mode we are hunting — a silent "healthy" reading (LowBalance=false)
// for a poisoned balance — would show up as err==nil with events recorded.
//
// SINK-SIDE assertion: (err != nil) && (len(Events()) == 0). If a future change
// (e.g. switching to a lenient decoder, or json.Number + ParseFloat that yields
// +Inf) lets NaN/±Inf reach the `< floor` guard, this test fails because an
// event would be recorded for a value that silently reads as not-low.
func TestWireCannotInjectNaNOrInf(t *testing.T) {
	const floorUSD = 10.00

	hostile := []struct {
		name string
		body string
	}{
		{"literal_nan_token", `{"balance": NaN}`},
		{"literal_infinity_token", `{"balance": Infinity}`},
		{"literal_neg_infinity_token", `{"balance": -Infinity}`},
		{"string_nan", `{"balance": "NaN"}`},
		{"positive_overflow", `{"balance": 1e309}`},
		{"negative_overflow", `{"balance": -1e309}`},
		{"total_available_nan", `{"total_available": NaN}`},
		{"total_available_overflow", `{"total_available": 1e400}`},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveRawBody(t, tc.body)
			defer srv.Close()

			m := newMonitorWithClock(srv.URL, "sk-hostile", floorUSD, fixedClock, srv.Client())

			r, err := m.GetBalance(context.Background(), "run-hostile")
			if err == nil {
				// The poison reached the sink. Prove it is the silent-alarm
				// failure and not some benign success.
				t.Fatalf("POISON REACHED SINK: GetBalance accepted hostile body %q -> "+
					"BalanceUSD=%v (NaN=%v Inf=%v) LowBalance=%v; expected a decode error",
					tc.body, r.BalanceUSD, math.IsNaN(r.BalanceUSD),
					math.IsInf(r.BalanceUSD, 0), r.LowBalance)
			}
			// No event may be recorded for a rejected reading.
			if evs := m.Events(); len(evs) != 0 {
				t.Fatalf("hostile body %q errored (good) but recorded %d event(s); "+
					"a rejected reading must leave the log untouched", tc.body, len(evs))
			}
		})
	}
}

// TestNaNFloorSilencesAlarm_LatentRisk PINS finding (2): a NaN floor silences
// the low-balance alarm for EVERY balance, including an empty/overdraft account.
//
// This is a CONTRACT-PINNING test of the CURRENT, documented-as-`<` behavior,
// NOT a fix and NOT a failing reproducer. It is the honest record that the
// package does not sanitize a NaN floor. If a future change adds floor
// validation (e.g. rejecting NaN, or treating any balance below a NaN floor as
// low), THIS TEST is the tripwire that forces that change to be deliberate.
//
// SINK-SIDE: with floor=NaN we assert LowBalance==false and zero warning events
// even for a deeply negative balance — the silent-alarm reality, made visible.
func TestNaNFloorSilencesAlarm_LatentRisk(t *testing.T) {
	floorUSD := math.NaN()

	// A balance that would be "low" under ANY finite positive floor.
	const deeplyNegative = -1000.00

	srv, _ := serveBalance(t, deeplyNegative)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-nan-floor", floorUSD, fixedClock, srv.Client())

	r, err := m.GetBalance(context.Background(), "run-nan-floor")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}

	// Document the hazard at the sink: NaN floor => comparison is always false.
	if r.LowBalance {
		t.Fatalf("UNEXPECTED: NaN floor produced LowBalance=true; the latent-risk "+
			"contract (balance < NaN is always false) has changed. balance=%.2f",
			r.BalanceUSD)
	}

	var warnings int
	for _, e := range m.Events() {
		if e.Kind == "low_balance_warning" {
			warnings++
		}
	}
	if warnings != 0 {
		t.Fatalf("UNEXPECTED: NaN floor emitted %d low_balance_warning event(s); "+
			"the documented strict-< behavior with a NaN floor must stay silent", warnings)
	}

	// Belt-and-suspenders: confirm the float identity we are relying on so the
	// test self-documents WHY the alarm is silent (not an accidental pass).
	if deeplyNegative < floorUSD {
		t.Fatal("invariant broken: (x < NaN) returned true in Go")
	}
}

// TestNaNFloorMatchesProdGuardExactly cross-checks that the silent-alarm verdict
// above is produced by the SAME `balanceUSD < m.floorUSD` guard prod uses, by
// asserting r.LowBalance equals the independently computed `bal < NaN` for a
// spread of balances. This makes the latent-risk pin bite the actual guard line
// rather than some incidental code path.
func TestNaNFloorMatchesProdGuardExactly(t *testing.T) {
	floorUSD := math.NaN()
	for _, bal := range []float64{-1e6, -0.01, 0, 0.01, 1e6} {
		srv, _ := serveBalance(t, bal)
		m := newMonitorWithClock(srv.URL, "sk-nan-spread", floorUSD, fixedClock, srv.Client())
		r, err := m.GetBalance(context.Background(), "run-nan-spread")
		srv.Close()
		if err != nil {
			t.Fatalf("GetBalance(balance=%v): %v", bal, err)
		}
		want := bal < floorUSD // always false for NaN floor
		if r.LowBalance != want {
			t.Errorf("balance=%v floor=NaN: LowBalance=%v, want %v (must mirror `balance < floor`)",
				bal, r.LowBalance, want)
		}
	}
}

// TestDuplicateRunIDDoubleCounted pins the (d) IDENTITY edge: the package keys
// nothing on RunID — every successful call appends its own event pair, so N
// calls with the SAME RunID produce N readings (and N warnings if low). A future
// "dedupe by RunID" change would silently drop accounting events; this catches
// it. SINK-SIDE: exact event counts in the shared log.
func TestDuplicateRunIDDoubleCounted(t *testing.T) {
	const (
		floorUSD = 10.00
		balance  = 3.00 // below floor -> each call also emits a warning
		dupRunID = "run-IDENTICAL"
		calls    = 5
	)

	srv, _ := serveBalance(t, balance)
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, "sk-dup", floorUSD, fixedClock, srv.Client())

	for i := 0; i < calls; i++ {
		r, err := m.GetBalance(context.Background(), dupRunID)
		if err != nil {
			t.Fatalf("GetBalance #%d: %v", i, err)
		}
		if !r.LowBalance {
			t.Fatalf("call #%d: balance %.2f below floor %.2f but LowBalance=false", i, balance, floorUSD)
		}
	}

	var readings, warnings int
	for _, e := range m.Events() {
		switch e.Kind {
		case "reading":
			readings++
		case "low_balance_warning":
			warnings++
		default:
			t.Fatalf("unexpected event kind %q", e.Kind)
		}
		if e.RunID != dupRunID {
			t.Errorf("event RunID: got %q, want %q", e.RunID, dupRunID)
		}
	}
	if readings != calls {
		t.Errorf("duplicate RunID: reading events=%d, want %d (no dedup expected)", readings, calls)
	}
	if warnings != calls {
		t.Errorf("duplicate RunID: warning events=%d, want %d (no dedup expected)", warnings, calls)
	}
}

// TestHostileMixWithRaceConcurrent runs valid and decode-error responses through
// many goroutines under -race. It re-arms the (c) shared-state bite: the event
// log must conserve EXACTLY one reading per SUCCESS (errors record nothing), and
// the m.mu lock must hold under contention. An unsynchronized append loses
// writes; -race reports the data race directly.
//
// The independent oracle (atomic gotSuccess / gotLow) NEVER reads m.events.
func TestHostileMixWithRaceConcurrent(t *testing.T) {
	const (
		floorUSD     = 10.00
		goroutines   = 48
		perGoroutine = 25
		apiKey       = "sk-hostile-race"
	)

	// Server alternates: even index -> valid low balance; odd index -> a body
	// that fails JSON decode (must NOT record an event and MUST surface error).
	var counter int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing/balance" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		i := atomic.AddInt64(&counter, 1) - 1
		w.Header().Set("Content-Type", "application/json")
		if i%2 == 0 {
			_ = json.NewEncoder(w).Encode(map[string]float64{"balance": 3.00}) // low
		} else {
			_, _ = w.Write([]byte(`{"balance": NaN}`)) // hostile -> decode error
		}
	}))
	defer srv.Close()

	m := newMonitorWithClock(srv.URL, apiKey, floorUSD, fixedClock, srv.Client())

	var (
		gotSuccess int64
		gotLow     int64
		gotErr     int64
		wg         sync.WaitGroup
		startGate  = make(chan struct{})
	)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			<-startGate
			for j := 0; j < perGoroutine; j++ {
				r, err := m.GetBalance(context.Background(), "run-hostile-race")
				if err != nil {
					atomic.AddInt64(&gotErr, 1)
					continue
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
	gotErrN := atomic.LoadInt64(&gotErr)

	if wantSuccess+gotErrN != goroutines*perGoroutine {
		t.Fatalf("oracle sanity: success=%d + err=%d != total %d",
			wantSuccess, gotErrN, goroutines*perGoroutine)
	}
	if gotErrN == 0 {
		t.Fatalf("expected some decode-error responses (hostile half); got 0")
	}

	var readings, warnings int64
	for _, e := range m.Events() {
		switch e.Kind {
		case "reading":
			readings++
		case "low_balance_warning":
			warnings++
		default:
			t.Fatalf("unexpected event kind %q", e.Kind)
		}
	}

	// Conservation: one reading per SUCCESS (errored hostile calls record none).
	if readings != wantSuccess {
		t.Errorf("CONSERVATION VIOLATION: reading events=%d, want %d (= successes; "+
			"hostile decode errors must record nothing)", readings, wantSuccess)
	}
	if warnings != wantLow {
		t.Errorf("CONSERVATION VIOLATION: warning events=%d, want %d", warnings, wantLow)
	}
	if total, want := int64(len(m.Events())), wantSuccess+wantLow; total != want {
		t.Errorf("CONSERVATION VIOLATION: total events=%d, want %d", total, want)
	}

	// Self-document the assertion is meaningful (some events actually flowed).
	if readings == 0 {
		t.Fatal("no successful readings recorded; test exercised nothing")
	}
}
