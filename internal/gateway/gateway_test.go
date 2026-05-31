package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), `"status":"healthy"`) {
		t.Errorf("body = %s, want healthy status", string(body))
	}
}

func TestNotFound(t *testing.T) {
	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestRoutePrefixes(t *testing.T) {
	// Start a tiny backend to verify routing.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-hit"))
	}))
	defer backend.Close()

	g, err := NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	// Override the build proxy to point at our test backend.
	target, _ := url.Parse(backend.URL)
	g.SetProxy("/api/v1/build/", httputil.NewSingleHostReverseProxy(target))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/build/status", nil)
	g.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "backend-hit" {
		t.Errorf("body = %s, want backend-hit", string(body))
	}
}
