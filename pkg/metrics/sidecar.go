// Package metrics — sidecar wires an HTTP /metrics exposition endpoint that
// gRPC-only binaries (which have no HTTP server of their own) can opt into by
// setting the HELIX_METRICS_ADDR environment variable.
//
// Design: opt-in, zero-config by default.  An empty addr (or unset env var)
// means "do not start a sidecar" — the function returns (nil, nil) and the
// caller needs no special handling.  When addr is non-empty the sidecar binds,
// starts serving in a goroutine, and returns the *http.Server so the caller can
// call Shutdown on process exit.
package metrics

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"
)

// StartSidecarOnListener starts an HTTP server on the already-open listener
// lis, serving GET /metrics from reg.  It returns immediately; the server runs
// in a background goroutine.  Callers must call srv.Shutdown to stop it.
//
// This variant is the primitive used by tests: tests call net.Listen themselves
// to learn the ephemeral address before starting the server.
func StartSidecarOnListener(lis net.Listener, reg *Registry) *http.Server {
	mux := http.NewServeMux()
	Mount(mux, reg)
	// Timeouts mirror helixd (cmd/helixd/main.go) and add ReadHeaderTimeout to
	// harden against slow-header / Slowloris attacks (gosec G112).
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go srv.Serve(lis) //nolint:errcheck // Serve returns ErrServerClosed on Shutdown; callers check via Shutdown.
	return srv
}

// StartSidecar starts an HTTP sidecar that serves GET /metrics from reg on
// addr.  If addr is the empty string the call is a no-op and (nil, nil) is
// returned (opt-in: unconfigured deployments are unaffected).
//
// On success the *http.Server is returned; callers should call srv.Shutdown
// (with a suitable context) during graceful shutdown.  Any error opening the
// listener is returned immediately.
func StartSidecar(addr string, reg *Registry) (*http.Server, error) {
	if addr == "" {
		return nil, nil
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return StartSidecarOnListener(lis, reg), nil
}

// StartSidecarFromEnv reads the sidecar bind address from the
// HELIX_METRICS_ADDR environment variable and delegates to StartSidecar.
//
// When the variable is unset (or empty) the call is a no-op and (nil, nil) is
// returned, so existing deployments that do not set the variable are
// unaffected and no port conflict is possible.
func StartSidecarFromEnv(reg *Registry) (*http.Server, error) {
	return StartSidecar(os.Getenv("HELIX_METRICS_ADDR"), reg)
}

// ShutdownSidecar stops srv gracefully using the background context.  It is a
// convenience wrapper callers can use in a defer.  If srv is nil it is a no-op.
func ShutdownSidecar(srv *http.Server) {
	if srv == nil {
		return
	}
	srv.Shutdown(context.Background()) //nolint:errcheck
}
