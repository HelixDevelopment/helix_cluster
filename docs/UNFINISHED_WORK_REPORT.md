# Helix Cluster OS — Unfinished-Work Report & Phased Completion Plan

| Field | Value |
|---|---|
| Created | 2026-06-04 |
| Author | Autonomous completion loop (subagent-driven) |
| Status | active / living document |
| Canonical registry | `data/hxc_registry.db` (448 Completed / 240 Queued) — markdown mirrors are stale |
| PRR | `docs/PRODUCTION_READINESS_REVIEW.md` — 66/80 = 82.5% (bar ≥95%) |
| Next free ticket id | **HXC-1640** |

> This report is produced under **CLAUDE-1** (tests must prove end-user usability — no PASS-bluffs), **CLAUDE-2** (cross-platform parity — no Linux-only stubs), and **CLAUDE-3** (docs continuously in-sync via `docs_chain`). It is grounded in five parallel read-only analysis sweeps of the live tree on 2026-06-04, not on prior claims.

---

## 0. Executive Summary

The codebase is **broad and largely test-covered at the unit level (82.4% main module)** but has **one dominant structural gap and several bounded correctness gaps**:

1. **Dead code / orphaned features (STRATEGIC, owner-decision-gated):** Of 212 `pkg/` packages, **only ~30 are reachable from any binary**. **~178 packages (~51,400 non-test LOC)** have zero importers anywhere — entire implemented subsystems (multi-Raft, STONITH, marketplace/economics, federation, data-plane networking, LLM routing, scheduling helpers) are **not wired into any running application**. `cmd/helixd` is a hollow shell that only probes etcd/postgres/redis and serves `/status`. This is precisely the "implemented but unusable by end users" pattern CLAUDE-1 forbids. **Resolution requires an owner decision** (wire-in vs. prune vs. formally document-as-library) because it defines the product's architecture and is hard to reverse.

2. **Concurrency correctness (BOUNDED, autonomously fixable):** 5 Major + 5 Minor confirmed hazards (double-close panic, data race on member state, PTY use-after-close, unbounded scheduler map, unbounded async goroutines). **In progress this wave.**

3. **Three doc/build defects (HXC-1637/1638/1639):** Makefile build target, missing `helixctl` CLI, SQL schema divergence. **Fixed / in progress this wave.**

4. **Test-type depth & QA-framework coverage:** Root module has near-universal *unit* tests, but consensus subsystems lack integration/stress/chaos, crypto has no fuzz, and the **HelixQA framework's own validators/vision logic is untested** (a CLAUDE-1 risk: an untested gate cannot certify usability).

5. **Security scanning:** `govulncheck` (0 reachable vulns) + `cyclonedx-gomod` SBOM are wired. **Snyk and SonarQube are absent**; both are now scriptable via Podman Compose but require owner-supplied tokens. `gosec` (FOSS SAST) can be promoted to a token-free gate now.

6. **CI-related PRR gaps are constitutionally blocked:** items 8/9/10/16/60 require active CI, which repo governance disables (`.github/workflows/disabled/`). These are working-as-intended under the no-CI rule, not gaps to close autonomously.

---

## 1. Current-State Inventory

### 1.1 Registry (canonical: `data/hxc_registry.db`)
- **448 Completed, 240 Queued.** Queued breaks down as:
  - **Bucket A — autonomously-actionable (~78):** pure Go/Rust/Elixir/Wasm/docs, testable locally without creds/hardware/cluster.
  - **Bucket B — infra/hardware/cloud-blocked (~152):** real SBC/Android/iOS/Jetson/FPGA/TEE hardware, K8s/K3s clusters, live cloud creds (AWS/Azure/GCP/Bittensor/Chutes), multi-host nets, multi-day soak rigs.
  - **Bucket C — governance/owner-decision (~10):** empty placeholder rows (HXC-016..020), CI re-enable (HXC-1262/HXC-105), Corellium procurement (HXC-1283), PRR sign-off (HXC-1286), release authorization (HXC-1140).

### 1.2 Production Readiness Review — gaps
- **NOT-READY (constitutional / CI):** item 8 (CI build/test active), item 9 (release pipeline active).
- **PARTIAL, locally closeable:** item 7 (coverage gate not enforced — `pkg/covgate` exists, unwired), item 21 (HelixQA challenges not executed with sink-side evidence), item 33 (mTLS e2e — HXC-600), item 34 (dep-scan continuous gate / SBOM-renovate).
- **PARTIAL, CI-blocked:** items 10, 16, 60.

### 1.3 Test coverage (verified by reproduction)
- main 82.4% / 255 pkgs; security 87.8% / 13 pkgs; api/v1 14.7% (**by-design** — 100% generated protobuf stubs, acceptable).
- **No forbidden blanket non-Linux skips** found (CLAUDE-2 clean). Skips are legitimate `-short`/env-gated/tool-absent.
- **Blind spots not in the existing report:** `helixqa` (5 untested incl. validators + vision/ORB), `Herald` first-party (17 untested incl. `internal/http/routes.go`), `containers` (9, mostly thin mains).

---

## 2. Defects Surfaced & Status (this wave)

| ID | Defect | Status |
|---|---|---|
| HXC-1637 | `make build` compiled non-existent `./cmd/helix-cluster` | **FIXED** — builds all 22 real `./cmd/*` into `bin/` (verified `go build -o bin/ ./cmd/...`); zig/cmake guarded by `command -v`. |
| HXC-1638 | `helixctl` documented but absent | **FIXED** — real cobra gRPC client `cmd/helixctl` (`build submit/status/cancel/logs`) with in-process BuildService round-trip tests; docs corrected; `list` omitted (no backing RPC — no bluff). Pending coordinator review+commit. |
| HXC-1639 | `0001_primary_schema.sql` diverges from migrate chain `001-015` | **IN PROGRESS** — reconcile to single source of truth (canonical = migrate chain) + drift-guard test so it can never silently diverge again. |

---

## 3. Confirmed Concurrency Hazards & Status (this wave)

| Ref | Location | Class | Severity/Confidence | Status |
|---|---|---|---|---|
| F1 | `pkg/swim/protocol.go` Stop/Leave | double-close panic | Major/High | **IN PROGRESS** (swim stream) |
| F2 | `pkg/swim/protocol.go` HealthyMembers/Members | data race on `Member.State` | Major/High | **IN PROGRESS** (swim stream) |
| F3 | `pkg/swim/protocol.go` probeRandomMember | untracked goroutine | Minor/High | **IN PROGRESS** (swim stream) |
| F4 | `pkg/swim/failure_detector.go` Confirm | lock-order fragility | Major/Med | **IN PROGRESS** (swim stream) |
| F5 | `pkg/session/backends/native.go` | PTY double-close / use-after-close | Major/High | **IN PROGRESS** (session stream) |
| F6 | `pkg/scheduler/scheduler.go` placements | unbounded map (no completion path) | Major/High | **IN PROGRESS** (scheduler stream) |
| F7 | `EventBus/pkg/bus/bus.go` PublishAsync | unbounded goroutines | Major/High | **IN PROGRESS** (eventbus stream) |
| F8 | `EventBus/pkg/nats/nats.go` Subscribe | unbuffered delivery stall | Minor/Med | **IN PROGRESS** (eventbus stream) |
| F9 | `pkg/tieredcache/tieredcache.go` Hot tier | no size cap (maintenance-only evict) | Minor/High | QUEUED (next wave) |
| F10 | `EventBus/pkg/bus/bus.go` trySend | per-event timer alloc on hot path | Minor/Med | **IN PROGRESS** (eventbus stream) |

Each fix is delivered via TDD with `go test -race`, scoped to one package, and gated before commit. F9 is folded into the next responsiveness wave alongside the lazy-init / semaphore programme (§4 Phase 4).

---

## 4. Phased Completion Plan

Each phase is a set of **gated waves**. Every wave obeys: per-package mutation gate, `go build ./... && go vet ./... && go test -race` for touched modules, no PASS-bluffs, docs_chain sync in the same wave (CLAUDE-3), commit to `main` only when green. Parallel streams always own **disjoint** packages.

### Phase 0 — Stabilize the foundation (THIS WAVE, in flight)
- **0.1** HXC-1637 Makefile build target ✅
- **0.2** HXC-1638 real `helixctl` CLI ✅ (review+commit pending)
- **0.3** HXC-1639 SQL single-source + drift-guard test ⏳
- **0.4** Concurrency Major/High fixes F1–F7 ⏳
- **Exit gate:** whole-tree `go build ./... && go test -race ./...` green; all six streams reviewed (spec + quality); committed; CHANGELOG + docs_chain synced.

### Phase 1 — Dead-code resolution (OWNER-GATED — the strategic centerpiece)
> **DECISION REQUIRED.** ~178 orphaned packages cannot be resolved autonomously. Three options, not mutually exclusive per-subsystem:
> - **(A) Wire-in:** compose the implemented subsystems into `helixd` (and the service binaries) so they run end-to-end. Highest fidelity to "nothing unfinished," but the largest effort — effectively building the real control plane. Each subsystem becomes a sub-wave: design integration seam → wire → integration/e2e test proving end-user-visible operation (CLAUDE-1 sink-side evidence) → docs.
> - **(B) Prune:** delete subsystems that are genuinely superseded/duplicated (e.g. duplicate util packages, abandoned experiments). Reversible via git history.
> - **(C) Document-as-library:** if a package is a deliberately-published reusable library (not meant to run inside a binary yet), record that intent in its doc + a registry note so it is not miscounted as dead.
>
> **Recommended sequencing once decided:** triage the 178 into A/B/C; then for bucket A, prioritize the control-plane spine — `multiraft`+`raftprofile` → `leader`/`voting`/`splitbrain`/`stonith` (HA) → scheduler helpers (`backfill`, `priorityqueue`, `nodeselector`, `preempt`, `admissioncontrol`) into `internal/scheduler` → `crdt`/`mvcc`/`hlc`/`antientropy` (replicated state) → marketplace/economics → federation/data-plane. Each wired subsystem ships with integration + e2e proof.
- **1.x (autonomous, no decision needed):** `pkg/session/migration.go` registers CRIU/DMTCP/container strategies that all return "not implemented" — a PASS-bluff inside a *wired* package. Either implement (Linux: CRIU is Bucket-B hardware) or **stop registering** the non-functional strategies (autonomous now). **Action: de-register stubs this phase; track real CRIU/DMTCP as Bucket-B.**
- **1.y:** confirm lint/gate packages (`archlint`, `etcdlint`, `covgate`, `qualitygate`, `openapivalidate`) are invoked from `make`/scripts (not Go imports) before classifying — wire any that should gate the build.

### Phase 2 — Test coverage to practical maximum + all test types
- **2.1 QA-framework self-coverage (CLAUDE-1 priority):** tests for `helixqa/pkg/validators` (image/text/video/manager/types) and `helixqa/pkg/vision/{core,detection}` (ORB detector). An untested gate cannot certify usability.
- **2.2 Untested first-party packages:** `Herald/*/internal/http` routes, `containers/pkg/lazyservice`, `helixqa/cmd/helixqa-concrete-runner`, the `challenges/runner` scaffolds across modules.
- **2.3 Missing test TYPES per subsystem (matrix-driven):**
  - **fuzz** for `pkg/crypto` and the e2ee/security envelope (crypto without fuzzing is a gap).
  - **integration + stress + chaos** for consensus: `pkg/multiraft`, `pkg/kraft`, `pkg/raftprofile`, `pkg/swim` (gossip convergence under churn/partition; Jepsen-style linearizability — HXC-1276).
  - **stress/chaos** for `pkg/resources` and `pkg/scheduler` (omega-model under load).
  - per-subsystem **benchmarks** for scheduler/consensus (220 benches exist but not co-located).
- **2.4 Coverage gate:** wire `pkg/covgate` 80% threshold into a `make cover-gate` target (local, no-CI) so regressions are caught (PRR item 7).
- **Exit gate:** every load-bearing package has ≥1 test of each applicable type; `make cover-gate` green; coverage report regenerated.

### Phase 3 — Challenges (HelixQA) bound to real features
- Author/execute Challenges for each newly-wired feature (Phase 1) and each major subsystem, capturing **sink-side evidence** (logs/metrics/screenshots) per CLAUDE-1 §6 (PRR item 21). Includes the queued challenge authoring items (HXC-1485/1486/1487/1488/1552, miner/E2EE/routing/GraVal/burst).
- HXC-1259: auto-generate challenges from test outcomes.
- **Exit gate:** no Challenge PASS on a non-functional feature; evidence stored under `qa-results/`.

### Phase 4 — Responsiveness: lazy-init, semaphores, non-blocking
- **4.1** Remaining concurrency minors (F8/F9/F10) + a tree-wide sweep for: eager init that should be `sync.Once` lazy; unbounded goroutine spawns → bounded worker pools / `golang.org/x/sync/semaphore`; blocking calls on hot paths → non-blocking `select`/buffered channels; unbounded caches → capped LRU.
- **4.2** Introduce **monitoring/metrics-driven optimization tests**: micro-benchmarks + a metrics-collection harness (HXC-1308 normalized scores) that asserts latency/throughput budgets, so optimizations are evidence-based, not guessed.
- **4.3** Stress & integration tests proving the system is "responsive like the flash and not possible to overload": saturation tests with backpressure assertions (burst controller HXC-1504/1552), p99 latency gates where a local oracle exists.
- **Exit gate:** documented latency/throughput budgets with passing guard tests; no unbounded goroutine/cache/queue remains on a hot path.

### Phase 5 — Security scanning (deep) + remediation
- **5.1 Token-free now:** promote `gosec` + `govulncheck` to first-class `make security-scan` (no-CI, no token). Triage & fix findings.
- **5.2 SonarQube (Podman Compose):** add `deploy/compose/security_sonarqube.yml` + `make sonar-up/sonar-scan/sonar-down` (non-interactive, token via `SONAR_TOKEN` env). **Owner input: mint `SONAR_TOKEN` once.**
- **5.3 Snyk (Podman):** add `make snyk-test/snyk-code`. **Owner input: supply `SNYK_TOKEN`.**
- **5.4** Analyze all findings → resolve everything; record evidence under `qa-results/`.
- **Exit gate:** clean (or triaged-with-justification) gosec + govulncheck; Sonar/Snyk runs captured once tokens provided.

### Phase 6 — Autonomously-actionable registry backlog (Bucket A, ~78 items)
- Work the ~78 Bucket-A items in dependency order (protos/schemas → algorithms → binaries → docs). Examples: proto stubs (HXC-1117), Avro routing (HXC-1119), DST engine (HXC-1234/1419), MVCC store (HXC-1390), hash-slot router (HXC-1394), backfill scheduler (HXC-1392), chutes client (HXC-1426), e2ee envelope reconcile (HXC-1431/1433), burst controller (HXC-1504), provider adapters (HXC-1510), coverage uplift (HXC-1490).
- Each closes its registry row only with real tests + (where applicable) a Challenge.

### Phase 7 — Documentation, manuals, courses, website, diagrams, SQL (continuous, CLAUDE-3)
- Every code change above triggers same-wave updates to: `README.md`, `docs/**`, user guides/manuals, `DATABASE_SCHEMA.md` + SQL, `ARCHITECTURE_DIAGRAMS.md` (Mermaid), and **all exports** (md→html/pdf/docx) via `docs_chain sync` + `docs_chain verify`.
- **Video courses:** extend/refresh the course materials (scripts + storyboards under `docs/` / `web/`) to cover new features and the corrected CLI/build flow. (Course *recording* may need owner involvement; scripts/storyboards are authored autonomously.)
- **Website (`web/`):** update content to match shipped reality (features, CLI, architecture, install).
- Register every new material in `.docs_chain/contexts/*.yaml` so it is enforced out of the box.
- **Exit gate:** `make docs-verify` / `docs_chain verify` in-sync; no stale doc.

### Phase 8 — Infra/hardware/cloud backlog (Bucket B) — owner-provisioned
- K8s/K3s deploy, SBC/Android/iOS/Jetson/FPGA/TEE, live-cloud, Bittensor testnet, multi-day soak. **Blocked on owner-provided hardware/creds/clusters.** Tracked, not autonomously closeable.

### Phase 9 — Governance gates (Bucket C) — owner decisions
- CI re-enable (collides with no-CI constitution — needs explicit owner ruling), define empty placeholder rows HXC-016..020, Corellium procurement, PRR ≥95% sign-off, v1.0.0-dev-mvp release authorization.

---

## 5. Decision Gates Needing Owner Input

| # | Decision | Why it can't be autonomous | Recommended default |
|---|---|---|---|
| D1 | **Dead-code: wire-in vs prune vs document** (178 pkgs) | Architecture-defining, hard to reverse, ~51k LOC | Triage A/B/C; wire the control-plane spine first |
| D2 | **CI re-enable** (PRR items 8/9/10/16/60) | Conflicts with no-CI constitutional rule (HXC-105/1262) | Keep no-CI; enforce gates via local `make` targets |
| D3 | **`SONAR_TOKEN` / `SNYK_TOKEN`** | Secrets only the owner holds | Run token-free `gosec`+`govulncheck` now; Sonar/Snyk when tokens provided |
| D4 | **Video course recording** | May need owner voice/screen capture | Author scripts/storyboards autonomously; flag recording |
| D5 | **v1.0.0-dev-mvp release** (HXC-1140) | Release authorization | Hold for sign-off after Phase 0–2 |

---

## 6. How the autonomous loop runs this

- **Parallelism:** 3–6 implementer streams per wave, each owning **disjoint** packages (no textual conflicts); whole-tree build+race gate before any commit.
- **Quality:** two-stage review (spec compliance, then code quality) on each stream's output before commit; no commit on a failing gate; adversarial review for PASS-bluffs.
- **Safety:** no `sudo`/interactive/root-prompting processes; no destructive ops; changes must not break existing green functionality.
- **Continuity:** registry (`data/hxc_registry.db`) + this report + `.remember/` carry state across waves; this document is updated every wave.
