//go:build e2e

// This file is the REAL spawn-the-binary end-to-end proof for gpu-pool-manager.
// It is gated behind the `e2e` build tag because it go-builds and spawns a real
// OS process and binds a real TCP port; run it explicitly with:
//
//	go test -tags e2e -run TestE2E ./cmd/gpu-pool-manager/ -v
//
// It is NOT a mock and NOT skipped. It go-builds the binary, reserves a free
// loopback port (net.Listen :0 -> read addr -> close), launches the process with
// that port via the binary's real --addr flag, polls the real GET /providers
// endpoint until the server is ready, and then asserts genuine end-user-visible
// behavior against the actual HTTP surface the binary exposes:
//
//   - GET /providers returns the three default providers the binary registers,
//     in the documented Tier-asc/Name-asc order, each with its real health flag.
//   - POST /allocate returns the lowest-Tier healthy provider (local-a100,
//     TierLocal=1) — the real allocation contract an end user depends on.
//   - GET /allocate (wrong method) is rejected 405, and POST /providers (wrong
//     method) is rejected 405 — the binary's real method gating.
//
// The process is killed cleanly at the end and all evidence is written to
// qa-results/.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	e2eGPUReadyTimeout = 20 * time.Second
	e2eGPUPollEvery    = 50 * time.Millisecond
	e2eGPUHTTPTimeout  = 3 * time.Second
)

// e2eGPUFreePort reserves a free loopback TCP port via bind-:0/read-back/close
// and returns "127.0.0.1:<port>".
func e2eGPUFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	return fmt.Sprintf("127.0.0.1:%d", addr.Port)
}

// e2eGPUBuild go-builds the gpu-pool-manager binary to a temp path. No
// -buildvcs=false is required in this checkout; if a future environment needs
// it, the failure message here will name the exact cause.
func e2eGPUBuild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gpu-pool-manager-e2e")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build gpu-pool-manager: %v\n%s", err, out)
	}
	return bin
}

// e2eGPUWaitReady polls GET /providers until the spawned server answers 200 or
// the deadline passes.
func e2eGPUWaitReady(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: e2eGPUHTTPTimeout}
	deadline := time.Now().Add(e2eGPUReadyTimeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/providers")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(e2eGPUPollEvery)
	}
	t.Fatalf("gpu-pool-manager never became ready on %s within %s", base, e2eGPUReadyTimeout)
}

// TestE2EGPUPoolManagerSpawnAllocate spawns the REAL binary and proves the
// allocation + listing HTTP surface works for an end user over real TCP.
func TestE2EGPUPoolManagerSpawnAllocate(t *testing.T) {
	evDir := filepath.Join("..", "..", "qa-results", "live-20260622", "cmd_e2e4", "gpu-pool-manager")
	if err := os.MkdirAll(evDir, 0o750); err != nil {
		t.Fatalf("mkdir evidence dir: %v", err)
	}
	var ev strings.Builder
	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		ev.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + "  " + line + "\n")
	}
	defer func() { _ = os.WriteFile(filepath.Join(evDir, "e2e.log"), []byte(ev.String()), 0o640) }()

	bin := e2eGPUBuild(t)
	logf("BUILT binary: %s", bin)

	addr := e2eGPUFreePort(t)
	base := "http://" + addr
	logf("RESERVED free port, launching with --addr %s", addr)

	cmd := exec.Command(bin, "--addr", addr)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn gpu-pool-manager: %v", err)
	}
	pid := cmd.Process.Pid
	logf("SPAWNED process pid=%d addr=%s", pid, addr)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	e2eGPUWaitReady(t, base)
	logf("READY: GET /providers answered 200 (pid=%d)", pid)

	client := &http.Client{Timeout: e2eGPUHTTPTimeout}

	// ---- GET /providers: real list, in documented order, with real health. ----
	resp, err := client.Get(base + "/providers")
	if err != nil {
		t.Fatalf("GET /providers: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET /providers Content-Type=%q, want application/json", ct)
	}
	var providers []Provider
	if err := json.Unmarshal(body, &providers); err != nil {
		t.Fatalf("decode /providers (%s): %v", body, err)
	}
	logf("GET /providers -> %s", strings.TrimSpace(string(body)))
	if len(providers) != 3 {
		t.Fatalf("expected 3 default providers, got %d: %s", len(providers), body)
	}
	// Documented stable order: Tier asc, then Name asc.
	wantNames := []string{"local-a100", "cloud-h100", "decentralized-pool"}
	wantTiers := []Tier{TierLocal, TierCloud, TierDecentralized}
	for i := range providers {
		if providers[i].Name != wantNames[i] {
			t.Fatalf("provider[%d].Name=%q, want %q (order: %s)", i, providers[i].Name, wantNames[i], body)
		}
		if providers[i].Tier != wantTiers[i] {
			t.Fatalf("provider[%d].Tier=%d, want %d", i, providers[i].Tier, wantTiers[i])
		}
		if !providers[i].Healthy {
			t.Fatalf("provider[%d]=%q expected Healthy=true", i, providers[i].Name)
		}
	}
	logf("VERIFIED /providers: 3 providers in Tier-asc/Name-asc order, all healthy")

	// ---- POST /allocate: lowest-Tier healthy provider (local-a100). ----
	resp, err = client.Post(base+"/allocate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /allocate: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /allocate status=%d body=%s", resp.StatusCode, body)
	}
	var alloc allocateResponse
	if err := json.Unmarshal(body, &alloc); err != nil {
		t.Fatalf("decode /allocate (%s): %v", body, err)
	}
	logf("POST /allocate -> %s", strings.TrimSpace(string(body)))
	if alloc.Name != "local-a100" {
		t.Fatalf("allocate picked %q, want lowest-Tier healthy %q", alloc.Name, "local-a100")
	}
	if alloc.Tier != TierLocal {
		t.Fatalf("allocate Tier=%d, want TierLocal=%d", alloc.Tier, TierLocal)
	}
	if !alloc.Healthy {
		t.Fatalf("allocate returned an unhealthy provider: %+v", alloc)
	}
	logf("VERIFIED /allocate: end user gets lowest-Tier healthy provider local-a100 (Tier=%d)", alloc.Tier)

	// ---- Method gating: wrong methods are rejected 405 by the real router. ----
	wreq, _ := http.NewRequest(http.MethodGet, base+"/allocate", nil)
	wresp, err := client.Do(wreq)
	if err != nil {
		t.Fatalf("GET /allocate: %v", err)
	}
	_ = wresp.Body.Close()
	if wresp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /allocate status=%d, want 405", wresp.StatusCode)
	}
	preq, _ := http.NewRequest(http.MethodPost, base+"/providers", nil)
	presp, err := client.Do(preq)
	if err != nil {
		t.Fatalf("POST /providers: %v", err)
	}
	_ = presp.Body.Close()
	if presp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /providers status=%d, want 405", presp.StatusCode)
	}
	logf("VERIFIED method gating: GET /allocate -> 405, POST /providers -> 405")

	// Clean kill.
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	logf("KILLED process pid=%d cleanly. PASS. Evidence in %s", pid, evDir)

	readme := fmt.Sprintf(`# gpu-pool-manager spawn-the-binary E2E evidence

Run: %s (UTC)
Binary: %s
Listen addr (reserved free loopback port): %s

## Proven (real spawned process, real TCP)
- GET /providers returns the 3 registered providers in Tier-asc/Name-asc order, all healthy.
- POST /allocate returns the lowest-Tier healthy provider local-a100 (TierLocal=1).
- Method gating: GET /allocate -> 405, POST /providers -> 405.

See e2e.log for the timestamped trace.
`, time.Now().UTC().Format(time.RFC3339), bin, addr)
	_ = os.WriteFile(filepath.Join(evDir, "README.md"), []byte(readme), 0o640)
}
