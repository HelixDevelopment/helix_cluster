package scan

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a clock function that always returns t0.
func fixedClock(t0 time.Time) func() time.Time {
	return func() time.Time { return t0 }
}

// ---------------------------------------------------------------------------
// TestRoutesToLeastLoaded
//
// Pins the MUTATION GUARD in scan.go: the `healthy[i].Load < healthy[j].Load`
// comparison inside Route. Inverting it to `>` selects the MOST-loaded node
// and makes this test fail because the assertion `chosenID == "node-low"` would
// receive "node-high" instead.
// ---------------------------------------------------------------------------
func TestRoutesToLeastLoaded(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reg := New("helix-scan-endpoint", fixedClock(t0))

	reg.AddBackend(Backend{ID: "node-high", Load: 100, Healthy: true})
	reg.AddBackend(Backend{ID: "node-low", Load: 10, Healthy: true})
	reg.AddBackend(Backend{ID: "node-mid", Load: 50, Healthy: true})

	runID := "run-least-loaded-001"
	chosenID, err := reg.Route(runID)
	if err != nil {
		t.Fatalf("Route returned unexpected error: %v", err)
	}

	// SINK-SIDE assertion: the returned node must be the least-loaded one.
	if chosenID != "node-low" {
		t.Errorf("expected node-low (Load=10) but got %q", chosenID)
	}

	// Assert the runID was recorded in the history.
	hist := reg.History()
	if len(hist) != 1 {
		t.Fatalf("expected 1 history record, got %d", len(hist))
	}
	rec := hist[0]
	if rec.RunID != runID {
		t.Errorf("history RunID: want %q, got %q", runID, rec.RunID)
	}
	if rec.NodeID != "node-low" {
		t.Errorf("history NodeID: want %q, got %q", "node-low", rec.NodeID)
	}
	if !rec.Timestamp.Equal(t0) {
		t.Errorf("history Timestamp: want %v, got %v", t0, rec.Timestamp)
	}
}

// ---------------------------------------------------------------------------
// TestEndpointStableAcrossMembership
//
// Verifies that EndpointName() returns the same value before and after
// AddBackend / RemoveBackend, and that routing rebalances to the new
// least-loaded node after the previous least-loaded node is removed.
// Also asserts the runID in history for each routing call.
// ---------------------------------------------------------------------------
func TestEndpointStableAcrossMembership(t *testing.T) {
	t0 := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	reg := New("helix-scan-stable", fixedClock(t0))

	initialName := reg.EndpointName()
	if initialName != "helix-scan-stable" {
		t.Fatalf("initial EndpointName: want %q, got %q", "helix-scan-stable", initialName)
	}

	// Add three nodes.
	reg.AddBackend(Backend{ID: "alpha", Load: 5, Healthy: true})
	reg.AddBackend(Backend{ID: "beta", Load: 20, Healthy: true})
	reg.AddBackend(Backend{ID: "gamma", Load: 50, Healthy: true})

	// Endpoint name must be unchanged after Add.
	if got := reg.EndpointName(); got != initialName {
		t.Errorf("EndpointName after AddBackend: want %q, got %q", initialName, got)
	}

	// First route: expect alpha (Load=5).
	runID1 := "run-stable-001"
	chosen1, err := reg.Route(runID1)
	if err != nil {
		t.Fatalf("first Route error: %v", err)
	}
	if chosen1 != "alpha" {
		t.Errorf("first route: want %q, got %q", "alpha", chosen1)
	}

	// Assert runID1 is recorded.
	hist := reg.History()
	if len(hist) < 1 || hist[0].RunID != runID1 {
		t.Errorf("RunID not recorded for first route: %+v", hist)
	}

	// Remove alpha (the current least-loaded).
	reg.RemoveBackend("alpha")

	// Endpoint name must be unchanged after Remove.
	if got := reg.EndpointName(); got != initialName {
		t.Errorf("EndpointName after RemoveBackend: want %q, got %q", initialName, got)
	}

	// Second route: rebalanced — expect beta (Load=20).
	runID2 := "run-stable-002"
	chosen2, err := reg.Route(runID2)
	if err != nil {
		t.Fatalf("second Route error: %v", err)
	}
	if chosen2 != "beta" {
		t.Errorf("second route after remove: want %q, got %q", "beta", chosen2)
	}

	// Assert runID2 is recorded.
	hist2 := reg.History()
	found := false
	for _, r := range hist2 {
		if r.RunID == runID2 {
			found = true
			if r.NodeID != "beta" {
				t.Errorf("history NodeID for %q: want beta, got %q", runID2, r.NodeID)
			}
		}
	}
	if !found {
		t.Errorf("RunID %q not found in history", runID2)
	}

	// Add a new cheaper node and confirm routing moves again.
	reg.AddBackend(Backend{ID: "delta", Load: 1, Healthy: true})
	runID3 := "run-stable-003"
	chosen3, err := reg.Route(runID3)
	if err != nil {
		t.Fatalf("third Route error: %v", err)
	}
	if chosen3 != "delta" {
		t.Errorf("third route after add delta: want delta, got %q", chosen3)
	}

	// Endpoint name still unchanged.
	if got := reg.EndpointName(); got != initialName {
		t.Errorf("EndpointName after second AddBackend: want %q, got %q", initialName, got)
	}
}

// ---------------------------------------------------------------------------
// TestNoHealthyBackendError
//
// Verifies that Route returns ErrNoHealthyBackend when no healthy node exists,
// both when the registry is empty and when all nodes are unhealthy.
// ---------------------------------------------------------------------------
func TestNoHealthyBackendError(t *testing.T) {
	reg := New("helix-scan-empty", fixedClock(time.Now()))

	_, err := reg.Route("run-empty-001")
	if !errors.Is(err, ErrNoHealthyBackend) {
		t.Errorf("empty registry: want ErrNoHealthyBackend, got %v", err)
	}

	reg.AddBackend(Backend{ID: "sick", Load: 0, Healthy: false})
	_, err = reg.Route("run-empty-002")
	if !errors.Is(err, ErrNoHealthyBackend) {
		t.Errorf("all unhealthy: want ErrNoHealthyBackend, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestTieBreaksByNodeID
//
// When two nodes have identical Load, the one with the lexicographically
// smaller ID must be selected.
// ---------------------------------------------------------------------------
func TestTieBreaksByNodeID(t *testing.T) {
	reg := New("helix-scan-tie", fixedClock(time.Now()))

	reg.AddBackend(Backend{ID: "zzz", Load: 10, Healthy: true})
	reg.AddBackend(Backend{ID: "aaa", Load: 10, Healthy: true})
	reg.AddBackend(Backend{ID: "mmm", Load: 10, Healthy: true})

	runID := "run-tie-001"
	chosen, err := reg.Route(runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "aaa" {
		t.Errorf("tie break: want %q, got %q", "aaa", chosen)
	}

	hist := reg.History()
	if len(hist) != 1 || hist[0].RunID != runID {
		t.Errorf("RunID not properly recorded: %+v", hist)
	}
}

// ---------------------------------------------------------------------------
// TestUnhealthyNodesSkipped
//
// Unhealthy nodes must never be returned by Route even if they have lower Load.
// ---------------------------------------------------------------------------
func TestUnhealthyNodesSkipped(t *testing.T) {
	reg := New("helix-scan-health", fixedClock(time.Now()))

	reg.AddBackend(Backend{ID: "sick-zero", Load: 0, Healthy: false})
	reg.AddBackend(Backend{ID: "healthy-ten", Load: 10, Healthy: true})

	chosen, err := reg.Route("run-health-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "healthy-ten" {
		t.Errorf("should skip unhealthy node: want healthy-ten, got %q", chosen)
	}
}

// ---------------------------------------------------------------------------
// TestSetBackendLoadAndHealth
//
// Exercises the convenience mutators and verifies routing reacts.
// ---------------------------------------------------------------------------
func TestSetBackendLoadAndHealth(t *testing.T) {
	reg := New("helix-scan-mutate", fixedClock(time.Now()))
	reg.AddBackend(Backend{ID: "n1", Load: 5, Healthy: true})
	reg.AddBackend(Backend{ID: "n2", Load: 20, Healthy: true})

	// Lower n2's load below n1 → n2 must be selected.
	if err := reg.SetBackendLoad("n2", 1); err != nil {
		t.Fatalf("SetBackendLoad: %v", err)
	}
	chosen, err := reg.Route("run-mutate-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "n2" {
		t.Errorf("after load mutation: want n2, got %q", chosen)
	}

	// Mark n2 unhealthy → n1 must be selected.
	if err := reg.SetBackendHealth("n2", false); err != nil {
		t.Fatalf("SetBackendHealth: %v", err)
	}
	chosen2, err := reg.Route("run-mutate-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen2 != "n1" {
		t.Errorf("after health mutation: want n1, got %q", chosen2)
	}
}

// ---------------------------------------------------------------------------
// TestHistoryOrdering
//
// History must preserve insertion order across multiple Route calls, and each
// record must carry the correct RunID and NodeID.
// ---------------------------------------------------------------------------
func TestHistoryOrdering(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	clock := func() time.Time {
		ts := t0.Add(time.Duration(tick) * time.Second)
		tick++
		return ts
	}

	reg := New("helix-scan-history", clock)
	reg.AddBackend(Backend{ID: "h1", Load: 1, Healthy: true})
	reg.AddBackend(Backend{ID: "h2", Load: 2, Healthy: true})

	runIDs := []string{"r1", "r2", "r3"}
	for _, id := range runIDs {
		if _, err := reg.Route(id); err != nil {
			t.Fatalf("Route(%q): %v", id, err)
		}
	}

	hist := reg.History()
	if len(hist) != 3 {
		t.Fatalf("expected 3 history records, got %d", len(hist))
	}
	for i, id := range runIDs {
		if hist[i].RunID != id {
			t.Errorf("history[%d].RunID: want %q, got %q", i, id, hist[i].RunID)
		}
		if hist[i].NodeID != "h1" {
			t.Errorf("history[%d].NodeID: want h1, got %q", i, hist[i].NodeID)
		}
		// Timestamps must be strictly increasing (injected clock ticks).
		if i > 0 && !hist[i].Timestamp.After(hist[i-1].Timestamp) {
			t.Errorf("history timestamps not increasing: [%d]=%v [%d]=%v",
				i-1, hist[i-1].Timestamp, i, hist[i].Timestamp)
		}
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentRouteSafety
//
// Hammers Route from many goroutines to surface data races (run with -race).
// Every returned node must be healthy.
// ---------------------------------------------------------------------------
func TestConcurrentRouteSafety(t *testing.T) {
	reg := New("helix-scan-concurrent", fixedClock(time.Now()))
	for i := 0; i < 10; i++ {
		reg.AddBackend(Backend{ID: fmt.Sprintf("c%d", i), Load: i, Healthy: i%3 != 0})
	}
	// Ensure at least one healthy node exists.
	reg.AddBackend(Backend{ID: "always-healthy", Load: 0, Healthy: true})

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			runID := fmt.Sprintf("concurrent-run-%d", g)
			nodeID, err := reg.Route(runID)
			if err != nil {
				// acceptable only if all are unhealthy (not the case here)
				t.Errorf("goroutine %d: unexpected error: %v", g, err)
				return
			}
			if nodeID == "" {
				t.Errorf("goroutine %d: empty nodeID returned", g)
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestHTTPEndpointExposesSCANState
//
// Uses net/http/httptest to exercise a real in-process HTTP round-trip that
// exposes Registry state via JSON. Asserts on the sink-side HTTP response body.
// ---------------------------------------------------------------------------
func TestHTTPEndpointExposesSCANState(t *testing.T) {
	reg := New("helix-scan-http", fixedClock(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)))
	reg.AddBackend(Backend{ID: "web1", Load: 30, Healthy: true})
	reg.AddBackend(Backend{ID: "web2", Load: 5, Healthy: true})

	// Pre-route so history is non-empty.
	runID := "run-http-001"
	_, err := reg.Route(runID)
	if err != nil {
		t.Fatalf("pre-route: %v", err)
	}

	type scanStatus struct {
		EndpointName string          `json:"endpoint_name"`
		History      []RoutingRecord `json:"history"`
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := scanStatus{
			EndpointName: reg.EndpointName(),
			History:      reg.History(),
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(status); err != nil {
			http.Error(w, "encode error", http.StatusInternalServerError)
		}
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/scan/status")
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP status: want 200, got %d", resp.StatusCode)
	}

	var status scanStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}

	// Sink-side assertions on the decoded response body.
	if status.EndpointName != "helix-scan-http" {
		t.Errorf("endpoint_name: want helix-scan-http, got %q", status.EndpointName)
	}
	if len(status.History) != 1 {
		t.Fatalf("history length: want 1, got %d", len(status.History))
	}
	if status.History[0].RunID != runID {
		t.Errorf("history[0].RunID: want %q, got %q", runID, status.History[0].RunID)
	}
	if status.History[0].NodeID != "web2" {
		t.Errorf("history[0].NodeID: want web2 (Load=5), got %q", status.History[0].NodeID)
	}
}
