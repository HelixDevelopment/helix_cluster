// Package gateway – REST API surface (HXC-1118).
//
// This file adds two REST endpoints to the Gateway mux:
//
//	POST /v1/sessions  – create a session; returns 201 {id, status:"CREATING"}
//	GET  /v1/pool/utilization – current cluster utilization; returns 200 {cpu_pct, mem_pct, gpu_pct}
//
// And one documentation endpoint:
//
//	GET /openapi.json – serves the embedded OpenAPI 3.0 specification.
//
// All dependencies on real external services are injected via interfaces so
// the unit tier can run deterministically without a live backend.
package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	_ "embed"
)

// ── OpenAPI spec ────────────────────────────────────────────────────────────

//go:embed openapi.yaml
var openapiYAML []byte // raw YAML, kept for inspection / forward-compat.

// openapiJSON is the canonical machine-readable form served at /openapi.json.
// We maintain it as a separate static JSON constant because the unit test
// must parse it to verify the OpenAPI 3.0 version field and required paths —
// generating JSON from YAML at runtime would require an external dep.
const openapiJSON = `{
  "openapi": "3.0.3",
  "info": {
    "title": "HelixCluster Gateway API",
    "version": "0.1.0",
    "description": "REST surface exposed by the HelixCluster API gateway (HXC-1118)."
  },
  "paths": {
    "/v1/sessions": {
      "post": {
        "summary": "Create a session",
        "operationId": "createSession",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/SessionSpec" }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Session accepted; status is CREATING.",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/SessionResponse" }
              }
            }
          },
          "400": { "description": "Malformed request body." }
        }
      }
    },
    "/v1/pool/utilization": {
      "get": {
        "summary": "Current pool utilization",
        "operationId": "getPoolUtilization",
        "responses": {
          "200": {
            "description": "Numeric utilization percentages.",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/PoolUtilization" }
              }
            }
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "SessionSpec": {
        "type": "object",
        "properties": {
          "resource": { "type": "object", "description": "Opaque resource specification." }
        }
      },
      "SessionResponse": {
        "type": "object",
        "required": ["id", "status"],
        "properties": {
          "id":     { "type": "string" },
          "status": { "type": "string", "enum": ["CREATING"] },
          "spec":   { "type": "object" }
        }
      },
      "PoolUtilization": {
        "type": "object",
        "required": ["cpu_pct", "mem_pct", "gpu_pct"],
        "properties": {
          "cpu_pct": { "type": "number", "format": "double" },
          "mem_pct": { "type": "number", "format": "double" },
          "gpu_pct": { "type": "number", "format": "double" }
        }
      }
    }
  }
}`

// ── Domain types ─────────────────────────────────────────────────────────────

// SessionSpec is the request body for POST /v1/sessions.
// The Resource field is an opaque JSON object forwarded verbatim to the backend.
type SessionSpec struct {
	Resource map[string]interface{} `json:"resource,omitempty"`
}

// SessionResponse is the 201 body returned after session acceptance.
type SessionResponse struct {
	ID     string                 `json:"id"`
	Status string                 `json:"status"`
	Spec   map[string]interface{} `json:"spec,omitempty"`
}

// PoolUtilization carries the current cluster resource utilization percentages.
// All three fields are present on every response; a value of 0.0 means "not
// measured / unavailable" rather than absent.
type PoolUtilization struct {
	CPUPct float64 `json:"cpu_pct"`
	MemPct float64 `json:"mem_pct"`
	GPUPct float64 `json:"gpu_pct"`
}

// ── Backend seams (dependency injection) ────────────────────────────────────

// SessionBackend is the contract the session handler uses to create sessions.
// In the unit tier the gateway is wired with an in-process fake. The
// integration tier (and production) wires a real RPC/HTTP client.
type SessionBackend interface {
	// CreateSession allocates a new session with the given spec and returns its
	// opaque ID.  It MUST return a non-empty string on success.
	CreateSession(spec SessionSpec) (id string, err error)
}

// PoolBackend is the contract the utilization handler uses to fetch current
// pool metrics. In unit tests the gateway is wired with a deterministic fake.
type PoolBackend interface {
	// Utilization returns the current cluster resource utilization percentages.
	Utilization() (PoolUtilization, error)
}

// ── Default (no-op) backends ─────────────────────────────────────────────────
// These are used when callers build a Gateway without injecting a real backend.
// They return safe zero values so the gateway stays operational even when the
// upstream services are absent.

type defaultSessionBackend struct {
	mu      sync.Mutex
	counter int
}

func (b *defaultSessionBackend) CreateSession(spec SessionSpec) (string, error) {
	b.mu.Lock()
	b.counter++
	id := fmt.Sprintf("session-%04d", b.counter)
	b.mu.Unlock()
	return id, nil
}

type defaultPoolBackend struct{}

func (defaultPoolBackend) Utilization() (PoolUtilization, error) {
	return PoolUtilization{CPUPct: 0, MemPct: 0, GPUPct: 0}, nil
}

// ── REST API mux ─────────────────────────────────────────────────────────────

// RESTAPI is the REST surface attached to the Gateway.
// It holds its own http.ServeMux so routes can be unit-tested independently of
// the full Gateway reverse-proxy logic, and it also registers itself on a
// provided *http.ServeMux (or the gateway's handler) via Register.
type RESTAPI struct {
	sessions SessionBackend
	pool     PoolBackend
	mux      *http.ServeMux
}

// newRESTAPI constructs a RESTAPI wired to the given backends, or the default
// no-op backends if nil is passed.
func newRESTAPI(sessions SessionBackend, pool PoolBackend) *RESTAPI {
	if sessions == nil {
		sessions = &defaultSessionBackend{}
	}
	if pool == nil {
		pool = defaultPoolBackend{}
	}
	a := &RESTAPI{sessions: sessions, pool: pool, mux: http.NewServeMux()}
	a.registerRoutes()
	return a
}

// registerRoutes wires all REST paths onto a.mux.
func (a *RESTAPI) registerRoutes() {
	a.mux.HandleFunc("/v1/sessions", a.handleSessions)
	a.mux.HandleFunc("/v1/pool/utilization", a.handlePoolUtilization)
	a.mux.HandleFunc("/openapi.json", a.handleOpenAPIJSON)
}

// ServeHTTP lets RESTAPI act as an http.Handler so it can be embedded directly
// inside the Gateway ServeHTTP dispatch loop.
func (a *RESTAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

// handleSessions handles POST /v1/sessions.
func (a *RESTAPI) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var spec SessionSpec
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&spec); err != nil && err.Error() != "EOF" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid JSON: %s"}`, err.Error())
			return
		}
	}

	id, err := a.sessions.CreateSession(spec)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"error":"backend error: %s"}`, err.Error())
		return
	}

	resp := SessionResponse{
		ID:     id,
		Status: "CREATING",
	}
	if spec.Resource != nil {
		resp.Spec = spec.Resource
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated) // 201
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Too late to change status; log only.
		_ = err
	}
}

// handlePoolUtilization handles GET /v1/pool/utilization.
func (a *RESTAPI) handlePoolUtilization(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	util, err := a.pool.Utilization()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"error":"backend error: %s"}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(util); err != nil {
		_ = err
	}
}

// handleOpenAPIJSON serves the embedded OpenAPI 3.0 JSON specification.
func (a *RESTAPI) handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(openapiJSON))
}

// ── Gateway integration ──────────────────────────────────────────────────────

// RESTOption is a functional option used by WithREST.
type RESTOption func(*RESTAPI)

// WithSessionBackend injects a custom SessionBackend (used in tests and prod).
func WithSessionBackend(b SessionBackend) RESTOption {
	return func(a *RESTAPI) { a.sessions = b }
}

// WithPoolBackend injects a custom PoolBackend (used in tests and prod).
func WithPoolBackend(b PoolBackend) RESTOption {
	return func(a *RESTAPI) { a.pool = b }
}

// WithREST is a Gateway Option that attaches the REST API surface to the
// Gateway. Routes /v1/sessions, /v1/pool/utilization, and /openapi.json are
// claimed by the RESTAPI; all other traffic continues to the proxy/auth logic.
func WithREST(opts ...RESTOption) Option {
	return func(g *Gateway) {
		api := newRESTAPI(nil, nil)
		for _, o := range opts {
			o(api)
		}
		// Re-register after any option mutation.
		api.mux = http.NewServeMux()
		api.registerRoutes()
		g.restAPI = api
	}
}

// restAPIPathPrefix returns true when the request path belongs to the REST API
// surface and should NOT be dispatched to the reverse-proxy logic.
func restAPIPath(path string) bool {
	return path == "/v1/sessions" ||
		strings.HasPrefix(path, "/v1/pool/") ||
		path == "/openapi.json"
}
