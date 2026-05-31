# Helix Cluster OS — Comprehensive Audit Report

**Audit ID:** HXC-923  
**Date:** 2026-05-31  
**Auditor:** AI Code Analysis  
**Scope:** Full codebase (pkg/, internal/, cmd/, test/, web/, deploy/)  
**Packages:** 75 Go packages, 77 pass `go test -race -count=1`  

---

## §1: Executive Summary

| Category | Count | Severity |
|----------|-------|----------|
| **Critical** | 3 | Must fix before production |
| **High** | 4 | Should fix before production |
| **Medium** | 6 | Fix before v1.0 |
| **Low** | 8 | Nice to have |
| **Info** | 5 | Documentation/tracking |

**Overall Assessment:** The codebase is structurally sound with good test coverage (77 race-clean packages). However, there are **3 critical issues** where cmd binaries have stub/separate implementations instead of using the real `internal/` services, and several architectural inconsistencies.

---

## §2: Critical Issues (Must Fix)

### CRIT-1: `cmd/helix-security` has stub token implementation (NOT using `internal/security`)

**File:** `cmd/helix-security/main.go`  
**Impact:** Production security service issues hardcoded "stub-token-*" tokens with no real authentication.

**Details:**
- `cmd/helix-security` defines its own `server` struct with stub methods
- Issues tokens like `fmt.Sprintf("stub-token-%s-%d", req.Identity, time.Now().Unix())`
- Authorize allows ANY token starting with "stub-token-"
- Does NOT import or use `internal/security` which has real `Orchestrator` + `PolicyEnforcer`
- `internal/security/server.go` has `GRPCServer` with real cert lifecycle, SPIFFE, Vault, RBAC

**Fix:** Replace stub `server` with `internal/security.GRPCServer` backed by real `Orchestrator` and `PolicyEnforcer`.

---

### CRIT-2: `cmd/helix-health` is HTTP-only, does NOT use `internal/health` gRPC server

**File:** `cmd/helix-health/main.go`  
**Impact:** Health service exposes HTTP REST API but NOT the gRPC `HealthService` defined in `api/v1/health.proto`.

**Details:**
- `cmd/helix-health` serves HTTP on `/health` and `/check/` endpoints
- Does NOT import `internal/health` which implements `helixv1.HealthServiceServer`
- `internal/health/server.go` has gRPC methods: `Check`, `Watch`, `ReportHealth`
- Gateway expects gRPC health checks; HTTP-only breaks service mesh integration

**Fix:** Add gRPC server alongside HTTP, or replace HTTP with gRPC using `internal/health.Server`.

---

### CRIT-3: `cmd/helix-build` uses wrong/broken imports

**File:** `cmd/helix-build/main.go`  
**Impact:** Build binary imports `"build"` and `helixv1 "api"` which are NOT valid module paths.

**Details:**
```go
import (
    "build"              // ← NOT github.com/HelixDevelopment/helix_cluster/pkg/build
    helixv1 "api"        // ← NOT github.com/HelixDevelopment/helix_cluster/api/v1
)
```
- These are treated as local module imports (relative to cmd/helix-build/go.mod)
- The `cmd/helix-build/go.mod` is a separate module that may have its own `build` package
- This creates a shadow/duplicate build service, NOT the real `pkg/build` + `internal/build`
- `internal/build/server.go` has real `Server` with `Orchestrator` + `WorkerPool`

**Fix:** Use proper import paths and wire to `internal/build.Server`.

---

## §3: High Issues (Should Fix)

### HIGH-1: `time.After` memory leak in `pkg/infra/orchestrator.go`

**File:** `pkg/infra/orchestrator.go:308`  
**Impact:** Each partition operation leaks a timer goroutine if context is cancelled before duration expires.

```go
go func() {
    select {
    case <-time.After(duration):  // ← leaks if ctx.Done() fires first
        o.mu.Lock()
        node.Status = "running"
        o.mu.Unlock()
    case <-ctx.Done():
    }
}()
```

**Fix:** Use `time.NewTimer` and `Stop()` it in the ctx.Done() branch.

---

### HIGH-2: `time.After` memory leak in `pkg/retry/retry.go`

**File:** `pkg/retry/retry.go:78, 103`  
**Impact:** Each retry attempt leaks a timer if context is cancelled during delay.

```go
select {
case <-ctx.Done():
    return ctx.Err()
case <-time.After(delay):  // ← leaks if ctx.Done() fires
}
```

**Fix:** Use `time.NewTimer` with deferred `Stop()`.

---

### HIGH-3: `internal/node/node.go` ignores errors on shutdown

**File:** `internal/node/node.go:160-162`  
**Impact:** Node deregistration and WireGuard stop errors are silently discarded.

```go
_ = a.registry.Deregister(context.Background(), "helix-node", a.ID)
_ = a.wg.Stop()
_ = a.protocol.Stop()
```

**Fix:** Log errors or return them to the caller.

---

### HIGH-4: Missing error wrapping (`%w`) in 15+ locations

**Files:** `pkg/jwt`, `pkg/crypto`, `pkg/validator`, `pkg/security`, `pkg/wasm`, `pkg/discovery`, `pkg/storage`, `pkg/testing/chaos`  
**Impact:** Errors cannot be unwrapped with `errors.Is()` / `errors.As()`.

**Example:**
```go
return fmt.Errorf("invalid exp claim type")  // ← should be fmt.Errorf("...: %w", err)
```

**Fix:** Add `%w` verb where the underlying error is available.

---

## §4: Medium Issues

### MED-1: 5 packages have no tests

**Packages:**
- `cmd/helix-advisory` — no tests
- `cmd/helix-node` — no tests
- `cmd/helix-scheduler` — no tests
- `cmd/helix-session` — no tests
- `test/benchmark` — has benchmarks but no unit tests (expected)

**Fix:** Add at minimum a smoke test that starts the binary and verifies it listens.

---

### MED-2: `cmd/helix-security` uses `log.Fatalf` in goroutine

**File:** `cmd/helix-security/main.go`  
**Impact:** `log.Fatalf` calls `os.Exit(1)` which bypasses deferred cleanups and graceful shutdown.

```go
go func() {
    if err := s.Serve(lis); err != nil {
        log.Fatalf("serve: %v", err)  // ← bypasses GracefulStop
    }
}()
```

**Fix:** Use `log.Printf` + channel to signal fatal errors to main goroutine.

---

### MED-3: `context.Background()` used without timeout in library code

**Files:**
- `internal/node/node.go:143` — `registry.Register(context.Background(), inst)`
- `internal/node/node.go:160` — `registry.Deregister(context.Background(), ...)`
- `pkg/discovery/package.go:227` — `backend.Delete(context.Background(), key)`

**Impact:** Operations can hang indefinitely if etcd is unavailable.

**Fix:** Use `context.WithTimeout` with reasonable defaults (5-10s).

---

### MED-4: `pkg/tracing` and `pkg/serde` use `panic()` in production code

**Files:**
- `pkg/tracing/package.go:241, 249` — `panic(fmt.Sprintf("tracing: failed to read random bytes: %v", err))`
- `pkg/serde/package.go:23` — `panic(fmt.Sprintf("marshal failed: %v", err))`

**Impact:** Library code panics instead of returning errors, crashing the entire process.

**Fix:** Return errors instead of panicking.

---

### MED-5: `pkg/log` calls `os.Exit(1)` on FatalLevel

**File:** `pkg/log/package.go:155`  
**Impact:** Logging a fatal message terminates the process unconditionally.

**Fix:** This may be intentional for Fatal level, but document it clearly.

---

### MED-6: `cmd/helixd` ignores JSON encoding errors

**File:** `cmd/helixd/main.go:49`  
```go
_ = json.NewEncoder(w).Encode(resp)  // ← error ignored
```

**Fix:** Handle or log the error.

---

## §5: Low Issues

### LOW-1: Empty directories remaining

- `cmd/helix-test/testdata/snapshots/` — empty (needed for tests)
- `docs/codegraph/` — empty
- `test/mutation/` — empty
- `test/unit/` — empty
- `web/public/` — empty

**Fix:** Add `.gitkeep` files or populate with content.

---

### LOW-2: `cmd/helix-build/go.mod` is a separate module

**Impact:** The build command has its own go.mod, creating module boundary complexity. It imports `"build"` and `"api"` as local packages, shadowing the real ones.

**Fix:** Consider merging into the main module or using proper replace directives.

---

### LOW-3: `challenges/` and `recovery/` modules have broken dependencies

**Impact:** These modules reference deleted packages (`containers`, `concurrency`). They were partially fixed but may still have issues.

**Fix:** Audit and fix or remove if not part of the core project.

---

### LOW-4: `internal/gpu/monitor.go` has mock-only GPU metrics

**File:** `internal/gpu/monitor.go:162`  
```go
_ = memUsed // memory usage tracked but does not affect status in mock
```

**Impact:** GPU health monitoring is simulated, not real.

**Fix:** Add real GPU metric collection for Linux (nvidia-smi parsing) or document as mock-only.

---

### LOW-5: `internal/build/orchestrator.go` has simulated build failure

**File:** `internal/build/orchestrator.go:336`  
```go
j.AppendLog("Build failed: simulated failure")
```

**Impact:** Build failures are simulated, not real build execution.

**Fix:** Document as placeholder for real build executor (Docker, BuildKit, etc.).

---

### LOW-6: Web UI uses mock data only

**Impact:** The React dashboard has no real backend connection; all data is mocked in `web/src/api/client.ts`.

**Fix:** Add real API client with error handling, fallback to mock in dev mode.

---

### LOW-7: `test/chaos` tests use deterministic RNG seed

**Impact:** Chaos tests always use the same "random" sequence (seed=42), reducing chaos coverage.

**Fix:** Use time-based seed or parameterized seeds.

---

### LOW-8: Missing `go.sum` entries for some workspace modules

**Impact:** `go work sync` may not fully resolve all module dependencies.

**Fix:** Run `go work sync` and commit updated `go.work.sum`.

---

## §6: Architectural Inconsistencies

### ARCH-1: Two-tier service pattern inconsistency

Some `cmd/` binaries use `internal/` services (good):
- `cmd/helix-scheduler` → `internal/scheduler`
- `cmd/helix-session` → `internal/session`
- `cmd/helix-node` → `internal/node`
- `cmd/helix-advisory` → `internal/advisory`

Some `cmd/` binaries have their own implementation (bad):
- `cmd/helix-security` → stub (NOT `internal/security`)
- `cmd/helix-health` → HTTP-only (NOT `internal/health` gRPC)
- `cmd/helix-build` → separate module with wrong imports

**Fix:** Unify all cmd binaries to use their `internal/` counterparts.

---

### ARCH-2: Port allocation gaps

| Service | Port | Status |
|---------|------|--------|
| helix-build | :50051 | ✅ |
| helix-scheduler | :50052 | ✅ |
| helix-session | :50053 | ✅ |
| helix-node | :50054 | ✅ |
| helix-health | :50055 | ✅ |
| helix-security | :50056 | ✅ |
| helix-llm | :50057 | ✅ |
| helix-policy | :50058 | ✅ |
| helix-advisory | :50059 | ✅ |

All ports allocated. No gaps.

---

### ARCH-3: Gateway routing table incomplete

The gateway proxies to backends, but some services are not registered:
- `helix-node` (:50054) — not in gateway routes
- `helix-advisory` (:50059) — not in gateway routes

**Fix:** Add routes in `internal/gateway/gateway.go` or `cmd/helix-gateway/main.go`.

---

## §7: Positive Findings

| Finding | Detail |
|---------|--------|
| **Zero data races** | All 77 packages pass `-race` |
| **Zero circular imports** | Clean dependency graph |
| **Good test coverage** | 831 test functions across all packages |
| **No hardcoded credentials** | No passwords, API keys, or secrets in code |
| **No SQL injection** | No dynamic SQL construction |
| **No XSS in Web UI** | No `dangerouslySetInnerHTML` or `eval()` |
| **Helm validates** | `helm template` succeeds, kubeconform passes |
| **All binaries compile** | `go build ./cmd/...` succeeds with GOWORK |
| **Web UI builds** | `npm run build` succeeds (193 KB bundle) |
| **Documentation complete** | 12 markdown files, 1,123 lines |

---

## §8: Recommended Fix Priority

### Wave 1 (Immediate — before any deployment)
1. **CRIT-1:** Wire `cmd/helix-security` to `internal/security`
2. **CRIT-2:** Add gRPC to `cmd/helix-health` using `internal/health`
3. **CRIT-3:** Fix `cmd/helix-build` imports and wire to `internal/build`

### Wave 2 (Before production)
4. **HIGH-1, HIGH-2:** Fix `time.After` memory leaks
5. **HIGH-3:** Handle errors in `internal/node` shutdown
6. **HIGH-4:** Add `%w` error wrapping

### Wave 3 (Before v1.0)
7. **MED-1:** Add smoke tests to cmd binaries
8. **MED-2:** Remove `log.Fatalf` from goroutines
9. **MED-3:** Add context timeouts
10. **MED-4, MED-5:** Remove panics from library code
11. **ARCH-3:** Complete gateway routing table

### Wave 4 (Nice to have)
12. **LOW-1 to LOW-8:** Polish items

---

*End of Audit Report — HXC-923*
