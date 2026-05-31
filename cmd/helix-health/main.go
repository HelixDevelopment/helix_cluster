// Command helix-health is the health monitoring daemon for Helix Cluster OS.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	ihealth "github.com/HelixDevelopment/helix_cluster/internal/health"
	pkghealth "github.com/HelixDevelopment/helix_cluster/pkg/health"
	"google.golang.org/grpc"
)

// httpServer holds the composite checker for legacy HTTP endpoints.
type httpServer struct {
	checker *pkghealth.CompositeChecker
}

func newHTTPServer() *httpServer {
	return &httpServer{
		checker: pkghealth.NewCompositeChecker(),
	}
}

func (s *httpServer) registerDefaults() {
	s.checker.AddCheck("helix-health", func() pkghealth.Status { return pkghealth.Healthy })
}

// clusterHealth aggregates all registered checks into a JSON response.
func (s *httpServer) clusterHealth(w http.ResponseWriter, r *http.Request) {
	status, checks := s.checker.Check()
	code := http.StatusOK
	if status == pkghealth.Unhealthy {
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := struct {
		Status  string                `json:"status"`
		Checks  []pkghealth.CheckResult `json:"checks,omitempty"`
		Message string                `json:"message,omitempty"`
	}{
		Status:  string(status),
		Checks:  checks,
		Message: "",
	}
	if status != pkghealth.Healthy {
		resp.Message = fmt.Sprintf("cluster is %s", status)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// serviceHealth returns health for a specific named service.
func (s *httpServer) serviceHealth(w http.ResponseWriter, r *http.Request) {
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
			if c.Status == pkghealth.Unhealthy {
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

	// ---- gRPC health service (internal/health) ----
	aggregator := ihealth.NewAggregator()
	grpcSrv := ihealth.NewServer(aggregator)

	gs := grpc.NewServer()
	helixv1.RegisterHealthServiceServer(gs, grpcSrv)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}

	grpcErr := make(chan error, 1)
	go func() {
		log.Printf("helix-health gRPC listening on :%s", port)
		if err := gs.Serve(lis); err != nil {
			grpcErr <- err
		}
	}()

	// ---- HTTP health endpoints (legacy, dual-stack) ----
	httpSrv := newHTTPServer()
	httpSrv.registerDefaults()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", httpSrv.clusterHealth)
	mux.HandleFunc("/check/", httpSrv.serviceHealth)

	httpPort := os.Getenv("HELIX_HEALTH_HTTP_PORT")
	if httpPort == "" {
		// If no separate HTTP port is set, reuse the gRPC port with a
		// second listener.  In production these are often the same port
		// with TLS muxing, but here we bind a separate TCP port.
		httpPort = port
	}

	httpServerInst := &http.Server{
		Addr:         ":" + httpPort,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	httpErr := make(chan error, 1)
	go func() {
		log.Printf("helix-health HTTP listening on :%s", httpPort)
		if err := httpServerInst.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			httpErr <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case err := <-grpcErr:
		log.Printf("grpc serve error: %v", err)
	case err := <-httpErr:
		log.Printf("http listen error: %v", err)
	}

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	gs.GracefulStop()
	if err := httpServerInst.Shutdown(ctx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
}
