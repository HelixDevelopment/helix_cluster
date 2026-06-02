// Command gpu-pool-manager is a self-contained HTTP front-end for a GPU
// provider pool (HXC-1530). It exposes tier-ordered allocation over HTTP.
//
// This program is FULLY SELF-CONTAINED: it defines its own minimal provider
// abstraction and imports no internal Helix packages.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sort"
	"sync"
)

// Tier is an ordered preference rank for a GPU provider. LOWER values are MORE
// preferred (TierLocal is the most preferred tier).
type Tier int

const (
	// TierLocal is on-cluster local GPU capacity — the most preferred tier.
	TierLocal Tier = iota + 1
	// TierCloud is hyperscaler cloud GPU capacity — second preference.
	TierCloud
	// TierDecentralized is decentralized/marketplace GPU capacity — least preferred.
	TierDecentralized
)

// Provider is a registered GPU capacity source.
type Provider struct {
	Name    string `json:"name"`
	Tier    Tier   `json:"tier"`
	Healthy bool   `json:"healthy"`
}

// Manager holds the registered providers and is safe for concurrent use.
type Manager struct {
	mu        sync.RWMutex
	providers []Provider
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{}
}

// Register adds a provider to the pool.
func (m *Manager) Register(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers = append(m.providers, p)
}

// Providers returns a snapshot copy of all registered providers.
func (m *Manager) Providers() []Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Provider, len(m.providers))
	copy(out, m.providers)
	return out
}

// Allocate returns the HEALTHY provider with the most-preferred (lowest) Tier.
// Ties on Tier are broken deterministically by Name (ascending). It returns
// ok=false when no healthy provider exists.
func (m *Manager) Allocate() (Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var best Provider
	found := false
	for _, p := range m.providers {
		if !p.Healthy {
			continue
		}
		if !found {
			best, found = p, true
			continue
		}
		// Lower Tier wins; on equal Tier, lexically-smaller Name wins.
		if p.Tier < best.Tier || (p.Tier == best.Tier && p.Name < best.Name) {
			best = p
		}
	}
	return best, found
}

// allocateResponse is the JSON body returned by a successful POST /allocate.
type allocateResponse struct {
	Name    string `json:"name"`
	Tier    Tier   `json:"tier"`
	Healthy bool   `json:"healthy"`
}

// newRouter wires the HTTP surface around a Manager. All logic lives here (and
// in Manager) so the front-end is fully httptest-able.
func newRouter(m *Manager) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/allocate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p, ok := m.Allocate()
		if !ok {
			http.Error(w, "no healthy provider available", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(allocateResponse{
			Name:    p.Name,
			Tier:    p.Tier,
			Healthy: p.Healthy,
		})
	})

	mux.HandleFunc("/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Stable ordering for deterministic output: Tier asc, then Name asc.
		ps := m.Providers()
		sort.SliceStable(ps, func(i, j int) bool {
			if ps[i].Tier != ps[j].Tier {
				return ps[i].Tier < ps[j].Tier
			}
			return ps[i].Name < ps[j].Name
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ps)
	})

	return mux
}

func main() {
	addr := flag.String("addr", ":8080", "listen address for the GPU pool manager HTTP front-end")
	flag.Parse()

	m := NewManager()
	// A couple of default providers across tiers.
	m.Register(Provider{Name: "local-a100", Tier: TierLocal, Healthy: true})
	m.Register(Provider{Name: "cloud-h100", Tier: TierCloud, Healthy: true})
	m.Register(Provider{Name: "decentralized-pool", Tier: TierDecentralized, Healthy: true})

	log.Printf("gpu-pool-manager listening on %s", *addr)
	if err := http.ListenAndServe(*addr, newRouter(m)); err != nil {
		log.Fatalf("gpu-pool-manager: server error: %v", err)
	}
}
