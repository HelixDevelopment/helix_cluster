package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAllocate_PicksLowestHealthyTier is the LOAD-BEARING GUARD for
// tier-correct allocation. It registers a healthy TierCloud provider AND a
// healthy TierLocal provider, performs a real httptest POST /allocate, and
// asserts the JSON winner is the TierLocal provider (the lowest, most-preferred
// tier). If tier ordering is ignored (mutation: return first/any registered),
// the winner would be "cloud-x"/TierCloud and these assertions FAIL.
func TestAllocate_PicksLowestHealthyTier(t *testing.T) {
	const runUUID = "b1f4c7a2-3d9e-4a51-9c8b-0e2f6d4a7c10"

	m := NewManager()
	// Register Cloud FIRST so a naive "return first healthy" mutation would
	// wrongly win with the cloud provider.
	m.Register(Provider{Name: "cloud-x", Tier: TierCloud, Healthy: true})
	m.Register(Provider{Name: "local-y", Tier: TierLocal, Healthy: true})

	srv := httptest.NewServer(newRouter(m))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/allocate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /allocate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var got allocateResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	// Hand-derived expected winner: between {cloud-x@TierCloud(2),
	// local-y@TierLocal(1)} the lowest tier is TierLocal -> local-y.
	if got.Name != "local-y" {
		t.Fatalf("allocate winner name: got %q, want %q (lowest-tier provider)", got.Name, "local-y")
	}
	if got.Tier != TierLocal {
		t.Fatalf("allocate winner tier: got %d, want %d (TierLocal)", got.Tier, TierLocal)
	}
	if !got.Healthy {
		t.Fatalf("allocate winner healthy: got %v, want true", got.Healthy)
	}

	t.Logf("run %s: registered[cloud-x@Tier%d(healthy), local-y@Tier%d(healthy)] -> POST /allocate selected %q@Tier%d (delta: tier-ordering picked TierLocal over the first-registered TierCloud)",
		runUUID, TierCloud, TierLocal, got.Name, got.Tier)
}

// TestProviders_ListsBothWithHealth asserts GET /providers returns every
// registered provider with its correct name/tier/health, via a real HTTP
// roundtrip.
func TestProviders_ListsBothWithHealth(t *testing.T) {
	const runUUID = "7c2a91de-5b40-4f6e-8a13-1d0e9f3b2c45"

	m := NewManager()
	m.Register(Provider{Name: "local-y", Tier: TierLocal, Healthy: true})
	m.Register(Provider{Name: "cloud-x", Tier: TierCloud, Healthy: false})

	srv := httptest.NewServer(newRouter(m))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/providers")
	if err != nil {
		t.Fatalf("GET /providers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var got []Provider
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("provider count: got %d, want 2", len(got))
	}

	// Hand-derived expected (router sorts Tier asc): [local-y@1 healthy, cloud-x@2 unhealthy].
	if got[0].Name != "local-y" || got[0].Tier != TierLocal || !got[0].Healthy {
		t.Fatalf("providers[0]: got %+v, want {local-y TierLocal healthy=true}", got[0])
	}
	if got[1].Name != "cloud-x" || got[1].Tier != TierCloud || got[1].Healthy {
		t.Fatalf("providers[1]: got %+v, want {cloud-x TierCloud healthy=false}", got[1])
	}

	t.Logf("run %s: registered 2 providers -> GET /providers returned %d entries with health=[%v,%v] (delta: empty->2 providers listed with per-tier health preserved)",
		runUUID, len(got), got[0].Healthy, got[1].Healthy)
}

// TestAllocate_NoHealthy503 asserts that when no provider is healthy, POST
// /allocate returns 503 Service Unavailable.
func TestAllocate_NoHealthy503(t *testing.T) {
	const runUUID = "f93b6e08-1c27-4d5a-bb90-6a4e2c8f7d31"

	m := NewManager()
	m.Register(Provider{Name: "local-y", Tier: TierLocal, Healthy: false})
	m.Register(Provider{Name: "cloud-x", Tier: TierCloud, Healthy: false})

	srv := httptest.NewServer(newRouter(m))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/allocate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /allocate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want %d (503 when all unhealthy)", resp.StatusCode, http.StatusServiceUnavailable)
	}

	// Sink-side: a 503 must NOT carry a JSON allocation body with a provider name.
	body := make([]byte, 0)
	buf := make([]byte, 512)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	if strings.Contains(string(body), "\"name\"") {
		t.Fatalf("503 body unexpectedly contained an allocation: %q", string(body))
	}

	t.Logf("run %s: registered 2 providers all healthy=false -> POST /allocate returned %d (delta: zero-healthy pool surfaced 503 instead of a bogus allocation)",
		runUUID, resp.StatusCode)
}
