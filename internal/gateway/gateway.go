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
)

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
type Gateway struct {
	proxies map[string]*httputil.ReverseProxy
	routes  []route
}

// NewGateway creates a new Gateway with the default route table.
func NewGateway() (*Gateway, error) {
	g := &Gateway{
		proxies: make(map[string]*httputil.ReverseProxy),
		routes:  routes,
	}

	for _, r := range g.routes {
		target, err := url.Parse(r.backend)
		if err != nil {
			return nil, fmt.Errorf("invalid backend URL %q: %w", r.backend, err)
		}
		g.proxies[r.prefix] = httputil.NewSingleHostReverseProxy(target)
	}

	return g, nil
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
			proxy, ok := g.proxies[route.prefix]
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
func (g *Gateway) SetProxy(prefix string, proxy *httputil.ReverseProxy) {
	g.proxies[prefix] = proxy
}

// ListenAndServe starts the gateway HTTP server on the given address.
func (g *Gateway) ListenAndServe(addr string) error {
	log.Printf("gateway listening on %s", addr)
	return http.ListenAndServe(addr, g)
}
