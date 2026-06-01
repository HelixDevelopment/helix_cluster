# §7.1 Anti-Bluff Remediation — package group `infra_build`

| Field | Value |
|---|---|
| Group | infra_build (`pkg/infra`, `pkg/build`, `internal/build`) |
| Risk | HIGH |
| Constitution anchors | CLAUDE-1 (§7.1 + §11.4.39), PCS-6 |
| Date | 2026-06-01 |
| Self-verify | build=ok vet=ok race=ok (all three packages) |

## 0. Summary

The bluffs called out in `AUDIT_REPORT.md §4` (`infra` map-echo orchestrator; `build`
`time.After` + magic-`"fail"` simulation) had **already been substantially remediated by
prior commits** in this tree: the fakes were renamed to `simulatedOrchestrator` /
`simulatedBuilder`, marked with an unmistakable exported `Simulated()` marker and PCS-6
WARNING docs, hidden behind injection seams (`Orchestrator` / `Builder` interfaces), and a
**real** local builder (`internal/build.ExecBuilder`) plus `//go:build integration`
skeletons for the container/real-service paths were added.

The one gap against THIS task's explicit mandate — *"the non-container path (e.g. building
a local Go module) must be genuinely real and runnable now"* and *"use the evidence helper"*
— was that **no test compiled a real Go source tree, captured the executable, RAN it, and
proved the output via the `evidence` substrate.** I closed that gap with two new
mutation-paired, evidence-backed tests that invoke the real Go toolchain through the real
service seam and execute the produced binary. No production code needed changing (the real
builder was already correct); the remediation is the missing end-user-visible proof.

## 1. Bluffs found (file:line) and current disposition

| Bluff (AUDIT_REPORT §4) | Location | Disposition |
|---|---|---|
| Orchestrator is a map-echo fake; `Boot` writes `Status:"running"`/`Healthy:true`, `Health` echoes a canned flag (never probes). | `pkg/infra/orchestrator.go:165-184` (Boot), `:241-267` (Health) | Already remediated in-tree: type renamed `simulatedOrchestrator`, `Simulated()bool=true` (`:159`), PCS-6 WARNING docs (`:104-125`, `:141-147`), `Orchestrator` injection seam (`:85-102`). Tests assert `Simulated()` honesty and only the bookkeeping. Real-service proof gated `//go:build integration` (`infra_integration_test.go`). **Verified honest; no change needed.** |
| Build `runBuild` simulates via `time.After` + magic `RepoURL=="fail"`; `ImageTag` fabricated; success asserted with no artifact. | `pkg/build/build.go:139-160` (`simulatedBuilder.Build`) | Already remediated: type renamed `simulatedBuilder`, `Simulated()bool=true` (`:136`), PCS-6 WARNING (`:118-130`), `Builder` seam + `NewServiceWithBuilder` (`:112-197`). Real builder `internal/build.ExecBuilder` runs real `os/exec`, streams real stdout/stderr, digests real artifact bytes, refuses to fabricate (`internal/build/exec_builder.go`). **Verified honest.** |
| No proof a REAL artifact is produced and is runnable NOW (task mandate: real `go build` of a local module). | (was absent) | **NEW** `internal/build/gobuild_real_test.go` — see §2. |

## 2. Files changed (file-by-file)

- **`internal/build/gobuild_real_test.go`** — NEW (only file added; no production code modified).
  - `TestExecBuilder_RealGoBuild_ProducesRunnableBinary`: generates a real Go module
    (`go.mod` + `main.go`) whose program prints a unique per-run token injected via
    `-ldflags -X main.Token=...`; drives it through the real `pkg/build.Service` worker-pool
    seam wired to the real `ExecBuilder`; the build command invokes the **real Go toolchain**
    (`go build`, hermetic: `GOPROXY=off CGO_ENABLED=0 GOFLAGS=-mod=mod`) writing the
    executable to `$HELIX_OUTPUT`. Asserts: `StateSucceeded`; a **non-empty (>1KiB) compiled
    binary** exists in the artifact store; the image tag is content-addressed
    (`sha256:<digest>`) over the **real compiled bytes**; then writes the stored binary back
    to disk, `chmod 0755`, **EXECUTES it**, and asserts it prints `helix-fixture-output:<token>`.
  - `TestExecBuilder_RealGoBuild_CompileErrorFails`: compiles a deliberately broken source
    (undefined symbol) and asserts the job reaches `StateFailed` from a **real non-zero
    compiler exit** (not the old `"fail"` sentinel), no `ImageTag`, and the **real compiler
    diagnostic** (`thisSymbolDoesNotExist`) is captured in the job logs.
  - Both use the `evidence` substrate: `evidence.NewT(...)`, `e.Token()` minted into the
    artifact, `e.MustDelta(...)` (0 → real binary bytes; running → failed), `e.MustPositive(...)`
    (executed program's stdout contains the run token), and `e.Manifest()`.

## 3. Behaviors now PROVEN (and how)

| Behavior | Proof | Evidence |
|---|---|---|
| A real artifact is produced by a real builder, runnable NOW with no container runtime. | `go build` of a generated module → executable captured → re-executed → prints unique run token. | `MustPositive` on the executed binary's stdout; `MustDelta` 0→len(binary)>1KiB. |
| Image tag is content-addressed to REAL output (not fabricated). | `cache.Digest(binBytes)` recomputed equals `art.Digest` and is embedded in `ImageTag`. | assert + store round-trip (`store.Get(digest)` returns the exact compiled bytes). |
| Genuine build-failure path (no magic string). | broken source → real `go build` non-zero exit → `StateFailed`, no tag, real diagnostic in logs. | `MustDelta` running→failed; log substring assert. |
| Orchestrator/builder are honestly marked non-production until real impls wired. | `Simulated()==true` guards (pre-existing tests). | `TestOrchestrator_IsSimulated`, `TestExecBuilder_NotSimulated`. |

**Mutation verification (false-positive immunity, §1.1):** I temporarily mutated
`ExecBuilder.Build` to fabricate a success (constant tag + `recordArtifact`, skipping the
real command). **Both new tests FAILED** (`digest not found: deadbeef`; `expected "failed"
actual "succeeded"`), then I restored `exec_builder.go` byte-identical (`diff -q` IDENTICAL).
A fabricated success cannot pass because (a) no file is written so the binary-bytes delta
fails, and (b) no fabrication can make an absent/garbage binary print a token minted at the
test's runtime.

## 4. Exact unit / -race results (this group only)

```
pkg/infra      : build ok, vet ok, go test -race -count=1 -> ok  (1.5s)
pkg/build      : build ok, vet ok, go test -race -count=1 -> ok  (pkg/build 2.1s, pkg/build/cache 1.7s)
internal/build : build ok, vet ok, go test -race -count=1 -> ok  (8.3s)  [includes the 2 new real-go-build tests]
```

New tests pass under `-race`; the real-build test SKIPS gracefully (never FAILS) if no `go`
toolchain is on PATH.

## 5. Integration tests for the orchestrator to run (command + service needed)

These are `//go:build integration`; the orchestrator runs them with real services/runtimes.
I did NOT start any container/database.

| Test | Command | Service / runtime needed |
|---|---|---|
| `pkg/infra/infra_integration_test.go::TestRealOrchestrator_HealthProbesRealService` | `go test -tags integration -run TestRealOrchestrator_HealthProbesRealService ./pkg/infra/...` | A CLI container runtime (podman/docker/nerdctl) so `brokertest.StartNATS` boots a real NATS broker; the test does a real TCP greeting probe. **SKIPS** (not fails) if no runtime. Still gated by a `t.Skip` until a real `Orchestrator` impl is wired (see PENDING_FORENSICS). |
| `pkg/build/build_integration_test.go::TestRealBuild_ImagePullable` | `go test -tags integration -run TestRealBuild_ImagePullable ./pkg/build/...` | A real container image builder + registry (docker buildx/buildkit). Currently `t.Skip` skeleton until a real container `Builder` is wired (see PENDING_FORENSICS). |

The non-container real path (`internal/build.ExecBuilder` + the new `go build` tests) runs in
the **default** (non-tagged) suite and is genuinely real now.

## 6. PENDING_FORENSICS

1. **`pkg/build` cannot import the `evidence` helper.** `pkg/build` is its OWN Go module
   (`pkg/build/go.mod`, `module .../pkg/build`), while `pkg/testing/evidence` lives in the
   root module `github.com/HelixDevelopment/helix_cluster`. Importing it into `pkg/build`
   tests would require adding a `require` + `replace` to `pkg/build/go.mod` — **forbidden by
   the task (never edit go.mod/go.sum/go.work).** Mitigation: the evidence-backed real-build
   proof was placed in `internal/build` (root module, same module as `evidence`, and where the
   real builder lives), which is the correct home for it. **Reason this is not a fix:** moving
   `evidence` or re-rooting `pkg/build` is a cross-module change outside this task's edit scope.

2. **Real CONTAINER image builder (docker buildx/buildkit) not wired.** A real Builder that
   produces OCI layers and a *pullable registry image* (the §3 task-4/§4 "pullable image"
   sink-side proof) requires a container runtime + registry the agent must not start, and the
   driver code would touch `cmd/` / a new package outside this group. Left as the honest
   `//go:build integration` `t.Skip` skeleton in `pkg/build/build_integration_test.go`.
   `ExecBuilder`'s docstring explicitly scopes itself as a local-exec (non-container) builder.

3. **Real production `Orchestrator` (boots real containers/VMs and probes them) not wired.**
   `NewOrchestrator` still returns `simulatedOrchestrator` (honestly `Simulated()==true`).
   A real impl needs the `containers` submodule runtime + live services and would add a new
   type wiring `brokertest`/`runtime` — cross-package + requires running infra the agent must
   not start. Left as the honest `//go:build integration` `t.Skip` skeleton in
   `pkg/infra/infra_integration_test.go` (whose real-probe half DOES run when a runtime exists).

## 7. Honesty statement

No production code was modified in this remediation; the real builder was already correct.
The only change is one added test file providing the missing end-user-visible, sink-side,
evidence-backed proof that a real artifact is built and runs. No test was written for a
non-functional feature; mutation testing confirms a fabricated success FAILS the new tests.
