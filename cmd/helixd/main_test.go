package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		resp := status{
			Version:   version,
			Status:    "healthy",
			Services:  checkServices(),
			Timestamp: time.Now().Unix(),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body status
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Version != version {
		t.Errorf("version = %s, want %s", body.Version, version)
	}
	if body.Status != "healthy" {
		t.Errorf("status = %s, want healthy", body.Status)
	}
	if len(body.Services) != 3 {
		t.Errorf("services len = %d, want 3", len(body.Services))
	}
}

func TestPingTCPUnreachable(t *testing.T) {
	result := pingTCP("127.0.0.1:1", 100*time.Millisecond)
	if result != "unreachable" {
		t.Errorf("ping = %s, want unreachable", result)
	}
}
