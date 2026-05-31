package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/health"
)

func TestClusterHealth(t *testing.T) {
	srv := newHealthServer()
	srv.checker.AddCheck("db", func() health.Status { return health.Healthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.clusterHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Status  string               `json:"status"`
		Checks  []health.CheckResult `json:"checks"`
		Message string               `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("expected healthy, got %s", resp.Status)
	}
	if len(resp.Checks) == 0 {
		t.Error("expected at least one check")
	}
}

func TestClusterHealthUnhealthy(t *testing.T) {
	srv := newHealthServer()
	srv.checker.AddCheck("db", func() health.Status { return health.Unhealthy })

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.clusterHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}

	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Status != "unhealthy" {
		t.Errorf("expected unhealthy, got %s", resp.Status)
	}
}

func TestServiceHealth(t *testing.T) {
	srv := newHealthServer()
	srv.checker.AddCheck("scheduler", func() health.Status { return health.Healthy })

	req := httptest.NewRequest(http.MethodGet, "/check/scheduler", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Service != "scheduler" {
		t.Errorf("expected scheduler, got %s", resp.Service)
	}
}

func TestServiceHealthNotFound(t *testing.T) {
	srv := newHealthServer()

	req := httptest.NewRequest(http.MethodGet, "/check/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestServiceHealthMissingName(t *testing.T) {
	srv := newHealthServer()

	req := httptest.NewRequest(http.MethodGet, "/check/", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestServiceHealthUnhealthy(t *testing.T) {
	srv := newHealthServer()
	srv.checker.AddCheck("cache", func() health.Status { return health.Unhealthy })

	req := httptest.NewRequest(http.MethodGet, "/check/cache", nil)
	rec := httptest.NewRecorder()
	srv.serviceHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestNewHealthServer(t *testing.T) {
	srv := newHealthServer()
	if srv.checker == nil {
		t.Fatal("expected checker to be initialized")
	}
}

func TestRegisterDefaults(t *testing.T) {
	srv := newHealthServer()
	srv.registerDefaults()
	status, _ := srv.checker.Check()
	if status != health.Healthy {
		t.Errorf("expected healthy after defaults, got %s", status)
	}
}
