// Command helix-health is the health monitoring daemon for Helix Cluster OS.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/health"
)

// healthServer holds the composite checker and optional gRPC health client.
type healthServer struct {
	checker *health.CompositeChecker
}

func newHealthServer() *healthServer {
	return &healthServer{
		checker: health.NewCompositeChecker(),
	}
}

func (s *healthServer) registerDefaults() {
	s.checker.AddCheck("helix-health", func() health.Status { return health.Healthy })
}

// clusterHealth aggregates all registered checks into a JSON response.
func (s *healthServer) clusterHealth(w http.ResponseWriter, r *http.Request) {
	status, checks := s.checker.Check()
	code := http.StatusOK
	if status == health.Unhealthy {
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := struct {
		Status  string               `json:"status"`
		Checks  []health.CheckResult `json:"checks,omitempty"`
		Message string               `json:"message,omitempty"`
	}{
		Status:  string(status),
		Checks:  checks,
		Message: "",
	}
	if status != health.Healthy {
		resp.Message = fmt.Sprintf("cluster is %s", status)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// serviceHealth returns health for a specific named service.
func (s *healthServer) serviceHealth(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/check/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"service name required"}`, http.StatusBadRequest)
		return
	}
	name := parts[0]

	status, checks := s.checker.Check()
	for _, c := range checks {
		if c.Name == name {
			code := http.StatusOK
			if c.Status == health.Unhealthy {
				code = http.StatusServiceUnavailable
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(struct {
				Status  string `json:"status"`
				Service string `json:"service"`
			}{
				Status:  string(c.Status),
				Service: name,
			})
			return
		}
	}

	// Service not found — still report based on overall if it's the default self-check
	if name == "helix-health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Service string `json:"service"`
		}{
			Status:  string(status),
			Service: name,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Service string `json:"service"`
	}{
		Error:   "service not registered",
		Service: name,
	})
}

func main() {
	port := os.Getenv("HELIX_HEALTH_PORT")
	if port == "" {
		port = "50055"
	}

	srv := newHealthServer()
	srv.registerDefaults()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", srv.clusterHealth)
	mux.HandleFunc("/check/", srv.serviceHealth)

	httpSrv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("helix-health listening on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// Ensure helixv1 is imported so the generated package is reachable.
var _ = helixv1.CheckRequest{}
