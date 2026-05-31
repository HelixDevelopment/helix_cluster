// Package gateway provides an HTTP reverse proxy that routes requests to
// backend HelixCluster gRPC services.
package gateway

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// gatewayHeader marks responses that traversed the gateway. It gives clients
// and tests a sink-side signal that routing actually occurred.
const gatewayHeader = "X-Helix-Gateway"

// route defines a single path prefix → backend mapping.
type route struct {
	prefix  string
	backend string
}

// routes is the static route table for HelixCluster services.
var routes = []route{
	{prefix: "/api/v1/scheduler/", backend: "http://localhost:50052"},
	{prefix: "/api/v1/session/", backend: "http://localhost:50053"},
	{prefix: "/api/v1/build/", backend: "http://localhost:50051"},
	{prefix: "/api/v1/node/", backend: "http://localhost:50054"},
}

// Gateway is an HTTP reverse proxy with path-based routing.
//
// The proxies map may be reconfigured via SetProxy after construction, so all
// access is guarded by mu to remain safe for concurrent serving.
type Gateway struct {
	mu      sync.RWMutex
	proxies map[string]*httputil.ReverseProxy
	routes  []route

	// auth, when non-nil, enables JWT/RBAC enforcement in ServeHTTP. It is set
	// only via the WithAuth option so the zero/default gateway stays open.
	auth *AuthPolicy
}

// NewGateway creates a new Gateway with the default route table.
//
// Optional Options (e.g. WithAuth) tune behavior. With no options the gateway
// keeps its historical open, unauthenticated behavior, so existing callers and
// tests are unaffected.
func NewGateway(opts ...Option) (*Gateway, error) {
	g := &Gateway{
		proxies: make(map[string]*httputil.ReverseProxy),
		routes:  routes,
	}

	for _, r := range g.routes {
		target, err := url.Parse(r.backend)
		if err != nil {
			return nil, fmt.Errorf("invalid backend URL %q: %w", r.backend, err)
		}
		g.proxies[r.prefix] = newReverseProxy(target)
	}

	for _, opt := range opts {
		opt(g)
	}

	return g, nil
}

// newReverseProxy builds a single-host reverse proxy that emits a usable,
// machine-readable JSON error when the backend is unreachable (the stdlib
// default writes a bare 502 with an empty body) and stamps the gateway marker
// header on every proxied response.
func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("gateway: backend %s unreachable for %s: %v", target, r.URL.Path, err)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(gatewayHeader, "true")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"backend unavailable","status":502}`))
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set(gatewayHeader, "true")
		return nil
	}
	return proxy
}

// ServeHTTP implements http.Handler.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
		return
	}

	for _, route := range g.routes {
		if strings.HasPrefix(r.URL.Path, route.prefix) {
			// Enforce JWT/RBAC BEFORE proxying so unauthorized requests never
			// reach the upstream. No-op when auth is disabled.
			if e := g.authorize(r); e != nil {
				writeAuthError(w, e)
				return
			}
			g.mu.RLock()
			proxy, ok := g.proxies[route.prefix]
			g.mu.RUnlock()
			if !ok {
				http.Error(w, "no proxy for route", http.StatusInternalServerError)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
	}

	http.NotFound(w, r)
}

// SetProxy overrides the reverse proxy for a given prefix (useful in tests).
// It is safe to call concurrently with ServeHTTP.
func (g *Gateway) SetProxy(prefix string, proxy *httputil.ReverseProxy) {
	g.mu.Lock()
	g.proxies[prefix] = proxy
	g.mu.Unlock()
}

// ListenAndServe starts the gateway HTTP server on the given address.
func (g *Gateway) ListenAndServe(addr string) error {
	log.Printf("gateway listening on %s", addr)
	return http.ListenAndServe(addr, g)
}
