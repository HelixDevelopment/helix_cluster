package metrics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStartSidecarOnListener_ScrapesRealCounter proves that the sidecar actually
// serves the Prometheus-format output and that the counter value is exactly
// what was incremented — not zero, not any other value.  A stub that returns
// 200/empty, or a body that does not contain the correct value, will fail.
func TestStartSidecarOnListener_ScrapesRealCounter(t *testing.T) {
	t.Parallel()

	reg := NewServiceRegistry("sidecartest")
	c := reg.RegisterCounter("helix_sidecar_hits_total", "test counter")
	c.Inc()
	c.Inc()
	c.Inc()
	// Value must be exactly 3 — any off-by-one or stub would fail the assertion below.

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv := StartSidecarOnListener(lis, reg)
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	addr := lis.Addr().String()

	// Retry briefly in case the goroutine hasn't started serving yet.
	var body string
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(fmt.Sprintf("http://%s/metrics", addr))
		if err == nil {
			raw, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				t.Fatalf("read body: %v", readErr)
			}
			body = string(raw)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sidecar never became ready: last error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The Prometheus text line for a counter value of exactly 3 must appear.
	// The assertion is on the concrete value "helix_sidecar_hits_total 3" so a
	// response of 0, 1, 2, or an empty body would all fail — this is not a
	// no-panic check.
	wantLine := "helix_sidecar_hits_total 3"
	if !strings.Contains(body, wantLine) {
		t.Fatalf("expected body to contain %q; got:\n%s", wantLine, body)
	}
}

// TestStartSidecar_EmptyAddr verifies the opt-in no-op: an empty addr must
// return (nil, nil) without opening any listener.
func TestStartSidecar_EmptyAddr(t *testing.T) {
	t.Parallel()

	reg := NewServiceRegistry("sidecartest_noop")
	srv, err := StartSidecar("", reg)
	if err != nil {
		t.Fatalf("StartSidecar(\"\", reg) returned error %v; want nil", err)
	}
	if srv != nil {
		srv.Shutdown(context.Background()) //nolint:errcheck
		t.Fatalf("StartSidecar(\"\", reg) returned non-nil *http.Server; want nil")
	}
}

// TestStartSidecarFromEnv_Unset verifies that when HELIX_METRICS_ADDR is not
// set in the environment, StartSidecarFromEnv returns (nil, nil) — the
// opt-in no-op behaviour that keeps existing deployments unchanged.
//
// Note: we cannot unset env vars in a parallel test portably, so this test
// ensures the behaviour by calling with a clean env read path (the unit
// delegates to StartSidecar("", reg) which we test separately above).
func TestStartSidecarFromEnv_Unset(t *testing.T) {
	// Do not set HELIX_METRICS_ADDR — it must be absent or empty in the test env.
	t.Setenv("HELIX_METRICS_ADDR", "")

	reg := NewServiceRegistry("sidecartest_env")
	srv, err := StartSidecarFromEnv(reg)
	if err != nil {
		t.Fatalf("StartSidecarFromEnv (unset) returned error %v; want nil", err)
	}
	if srv != nil {
		srv.Shutdown(context.Background()) //nolint:errcheck
		t.Fatalf("StartSidecarFromEnv (unset) returned non-nil *http.Server; want nil")
	}
}

// TestShutdownSidecar_Nil verifies that ShutdownSidecar(nil) is a safe no-op
// (does not panic, does not block).
func TestShutdownSidecar_Nil(t *testing.T) {
	t.Parallel()
	ShutdownSidecar(nil) // must not panic
}
