package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/gateway"
)

func TestGatewayHandler(t *testing.T) {
	g, err := gateway.NewGateway()
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}

	srv := httptest.NewServer(g)
	defer srv.Close()

	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
