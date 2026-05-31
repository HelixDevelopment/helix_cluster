# Phase 6 — Gap Audit (Multi-Cluster Federation)

| Field | Value |
|---|---|
| Auditor | Engineering auditor (code-grounded) |
| Date | 2026-06-01 |
| Scope | `docs/research/PHASE_6_ROADMAP.md` deliverables vs. actual code in `pkg/`, `internal/`, `cmd/`, `api/`, `security/` submodule |
| **Honest completion** | **~4% of Phase 6 deliverables DONE** |

**One-line summary:** Phase 6 (federation: cells, hierarchical SWIM, CRDT/HLC, NAT traversal, SPIFFE federation, Cilium/Karmada/ArgoCD) is **essentially not started** — all 11 planned packages are absent and there are zero federation/cross-cell terms in the Go codebase; the only Phase-6-adjacent code is single-cluster scaffolding (`pkg/swim`, `pkg/wireguard`) plus a documented NAT-traversal **stub** that returns "not implemented".

---

## Method / Anti-bluff notes

- A deliverable is **DONE** only if implemented AND covered by real-behavior tests. Stub/TODO/mock-only/error-path-only ⇒ PARTIAL or MISSING.
- Searched the full Go tree for federation vocabulary (`karmada`, `propagationpolicy`, `merkle`, `hybrid logical`, `g-counter`, `lww-register`, `cluster mesh`, `applicationset`, `cross-cell`, `inter-cell`): **0 matches**.
- Searched for the 11 planned package dirs (`federation`, `crdt`, `hlc`, `nattraversal`, `cilium`, `gitops`, `internal/cell`, `internal/federation`, `internal/chaos`, `pkg/swim/hierarchical`, `pkg/spiffe/federation`): **none exist** (`pkg/testing/chaos` is unit-test chaos helpers, unrelated).
- Recent hardening (cost-aware scheduling, `pkg/fiber`, `pkg/gpuattest`, e2ee/attestation in the `security` submodule, etcd/NATS/Postgres integration tests) is real but is **single-cluster Phase 5/8 scope**, not Phase 6 federation. Credited only where the roadmap names it an "existing package to extend".

---

## Deliverable Status Table

| Deliverable (roadmap) | Status | Evidence file:line | Notes |
|---|---|---|---|
| `pkg/federation` — cell registry, lifecycle state machine, federated API proxy | MISSING | no dir under `pkg/` | No package; no `Cell`/`join`/`evacuate` types anywhere. |
| `pkg/swim/hierarchical` — two-tier LAN+WAN delegate gossip | MISSING | `pkg/swim/` (no `hierarchical/`) | `pkg/swim` is single-tier only; no delegate/gateway/WAN code. |
| `pkg/crdt` — G-Counter, LWW-Register, OR-Set + Merkle delta | MISSING | no dir under `pkg/` | Zero CRDT/Merkle code in tree. |
| `pkg/hlc` — Hybrid Logical Clock | MISSING | no dir under `pkg/` | `pkg/swim/clock.go` is a plain monotonic helper, not an HLC. |
| `pkg/nattraversal` — STUN/TURN/ICE + UDP hole punch + QUIC | MISSING (stub adjacent) | `pkg/wireguard/nat_traversal.go:16-41` | `DiscoverExternalAddress` hits ipify and the http client returns "not available in this build"; `SetupPortMapping`/`RemovePortMapping` return `"not implemented"`. Tests assert the **error path only** (`pkg/wireguard/package_test.go:288-308`). No STUN/TURN/ICE/QUIC. |
| `pkg/cilium` — Cluster Mesh client | MISSING | no dir under `pkg/` | No Cilium/eBPF integration. |
| `pkg/spiffe/federation` — trust-bundle exchange across cells | MISSING | `pkg/security/spiffe.go` (no federation refs) | `pkg/security/spiffe.go` exists for single-domain SPIFFE; no trust-bundle/federation code. |
| `pkg/gitops` — ArgoCD ApplicationSet federation | MISSING | no dir under `pkg/` | No ArgoCD/ApplicationSet code. |
| `internal/cell` — gateway mgmt, inter-cell WG wiring, SWIM delegate config | MISSING | no dir under `internal/` | Absent. |
| `internal/federation` — Karmada/OCM, PropagationPolicy, kubectl aggregation | MISSING | no dir under `internal/` | Absent. |
| `internal/chaos` — 12 chaos experiments (CE-01..CE-12) | MISSING | no dir under `internal/` | Absent (`pkg/testing/chaos` is unit-test fault helpers). |
| `cmd/helix-federation` binary | MISSING | `cmd/` (not present) | No federation CLI; no `helixctl federation` / `cell join`. |
| Ext: `pkg/swim` Phi-accrual + gateway-relay suspicion | PARTIAL | `pkg/swim/failure_detector.go:8-33` | FailureDetector uses a **fixed `timeout`** + `time.Timer` suspicion, NOT Phi-accrual; no gateway-relay path. Base SWIM (gossip/prober/suspicion) is real and tested for single-cluster. |
| Ext: `pkg/wireguard` multi-cell peers + zero-downtime key rotation | PARTIAL | `pkg/wireguard/keyrotation.go:97-141` | `RotateKeysTracked` rotates and records the *previous* key for audit and is tested (`keyrotation_config_test.go:61-308`), but there is **no zero-downtime overlap-window** ("add new before remove old", 200 ms window) and no multi-cell/inter-cell route advertising. Single-cluster mesh only. |
| Ext: `pkg/security` SPIFFE/SPIRE federation + OPA cross-cluster | PARTIAL | `pkg/security/spiffe.go`, `internal/policy/` | Single-domain SPIFFE + single-cluster policy exist; cross-cluster trust bundles / OPA admission absent. |
| Ext: `pkg/scheduler` inter-cell Karmada propagation + spot scoring | PARTIAL | `pkg/scheduler/cost_gpu.go:1-30` | Real **single-cluster** cost-aware GPU placement plugin (tested), but no inter-cell Karmada propagation; "cost reduction" here ≠ cross-cell bursting. |
| Ext: `internal/node` cell agent / federation identity attestation | MISSING | `internal/node/node.go` | Node agent has no cell/federation/tier-selection logic. |
| Exit gate: real multi-cell integration tests (CLAUDE-1) | MISSING | n/a | No multi-cell testbed; the only WG nat test asserts the stub error path. |
| Exit gate: e2e operator flows (`cell join`, `--all-cells`, GitOps) | MISSING | n/a | No CLI surface exists to exercise. |

---

## TOP IMPLEMENTABLE GAPS (Go, no new infra)

Prioritized for maximum federation value while remaining pure-Go and locally testable. Each is mutation-pairable per §1.1.

### 1. `pkg/hlc` — Hybrid Logical Clock (P0, foundational)
**Target dir:** `pkg/hlc/`
**Spec:** Implement a Hybrid Logical Clock: a `Clock` holding `{wallMillis, logical}` with `Now()` (advances against `time.Now`), `Update(remote Timestamp)` (max of local/remote wall, bumps logical on ties, clamps drift to a configurable bound e.g. 10 ms→error), and a total-order `Timestamp.Compare`. Pure-Go, deterministic with an injectable wall-clock func. This is the timestamp substrate every CRDT type needs and unblocks `pkg/crdt`.
**Acceptance test (mutation-pairable):** Feed an out-of-order remote timestamp newer than local wall; assert returned timestamp is strictly greater than both inputs and that two events at identical wall time get distinct logical counters preserving causal order. Mutant: change `Update` to ignore the remote logical (use only wall) → causal-order assertion must fail.

### 2. `pkg/crdt` — G-Counter, LWW-Register, OR-Set (P0)
**Target dir:** `pkg/crdt/`
**Spec:** Implement three state-based CRDTs over `pkg/hlc` timestamps with `Merge(other)` that is commutative, associative, idempotent. G-Counter (per-replica counter map, value=sum); LWW-Register (value + HLC, last-writer-wins on Compare); OR-Set (add/remove with unique tags). No network — just the convergence algebra.
**Acceptance test:** Property test merging the same set of ops in randomized orders across 3 replicas; assert all replicas converge to byte-identical state and that `Merge` is idempotent (merging twice = once). Mutant: make OR-Set `Remove` delete by element instead of by tag → concurrent add/remove convergence test must fail.

### 3. `pkg/federation` — cell registry + lifecycle state machine (P0)
**Target dir:** `pkg/federation/`
**Spec:** Pure-Go cell model: `Cell{ID, Gateways, State}` and an explicit FSM with transitions `Joining→Syncing→Active→Evacuating→Detached` (plus `Failed`). Provide a thread-safe `Registry` (add/remove/list cells, snapshot) and a `Transition(cell, event)` that rejects illegal transitions. No real networking — the lifecycle/validation logic is the deliverable; wire to transport later.
**Acceptance test:** Drive a cell through the legal happy path and assert each state; then attempt an illegal jump (`Detached→Active`) and assert a typed error + unchanged state. Mutant: remove the illegal-transition guard → the rejection test must fail.

### 4. Phi-accrual failure detector in `pkg/swim` (P1)
**Target dir:** `pkg/swim/` (new `phi.go`, used by `failure_detector.go`)
**Spec:** Add a Phi-accrual detector: a sliding window of inter-arrival heartbeat intervals producing a `phi(now)` suspicion value from the sample mean/variance; expose `SuspicionLevel(memberID)` and a threshold-based `IsSuspect`. Replace/augment the current fixed-timeout `FailureDetector` so suspicion adapts to observed latency. Pure-Go, injectable clock.
**Acceptance test:** Feed a steady 1 s heartbeat stream then stop; assert phi stays low while heartbeats arrive and crosses threshold within a bounded time after they stop; assert a jittery-but-alive stream does NOT cross threshold. Mutant: drop the variance term (use mean only) → the jitter-tolerance assertion must fail.

### 5. `pkg/crdt` Merkle anti-entropy delta sync (P1, depends on #2)
**Target dir:** `pkg/crdt/merkle/`
**Spec:** Build a Merkle tree over a keyspace of CRDT registers; `Diff(remoteRoot)` returns only the divergent key set so reconciliation ships deltas, not full state (the roadmap's <5 KB/s gossip budget). Pure-Go over an in-memory map; transport-agnostic.
**Acceptance test:** Two trees differing in 1 of N keys; assert `Diff` returns exactly that key and that syncing it makes roots equal. Mutant: hash only leaf values, not subtree children → divergence in a deep key must still be detected, so a "single changed deep key" test must fail under the mutant.

### 6. `pkg/federation` federated API aggregation model (P1, depends on #3)
**Target dir:** `pkg/federation/` (new `aggregate.go`)
**Spec:** Pure-Go fan-out/merge: given a set of `Active` cells from the Registry and a per-cell query func (interface, mocked in unit tests), aggregate results with partial-failure semantics (return successes + a per-cell error map; never fail-closed on one cell). This is the engine behind `helixctl get ... --all-cells`.
**Acceptance test:** Aggregate over 3 fake cells where one returns an error; assert 2 results returned + 1 recorded error and no panic. Mutant: make aggregation abort on first error → partial-success assertion must fail.

---

## DEFERRED (need external infra / hardware / non-Go)

| Item | Reason deferred |
|---|---|
| `pkg/nattraversal` real STUN/TURN/ICE + UDP hole punch + QUIC | Requires real NAT devices / STUN-TURN servers and a network test matrix (4 NAT types) to validate honestly; not unit-testable to a real-behavior standard locally. |
| `pkg/cilium` Cluster Mesh data plane | Requires Cilium + eBPF + a Kubernetes multi-cluster; out of pure-Go scope. |
| `internal/federation` Karmada/OCM PropagationPolicy | Requires running Karmada/OCM control planes and real K8s clusters. |
| `pkg/gitops` ArgoCD ApplicationSet federation | Requires a live ArgoCD instance + Git backend to prove end-user flow. |
| `pkg/spiffe/federation` trust-bundle exchange | Needs ≥2 SPIRE servers (nested trust domains) for real cross-cell SVID/bundle validation. |
| `internal/chaos` CE-01..CE-12 + Chaos Mesh | Requires Chaos Mesh + a multi-node testbed and partition injection; the 12 KPIs are infra-bound. |
| WireGuard zero-downtime key rotation overlap window | Kernel WireGuard interface + multi-host mesh needed to prove the 200 ms overlap doesn't flap (skipped on darwin today). |
| Velero DR / Prometheus federation / split-brain alerts | External backup + monitoring stacks; KPI measurement (RPO, alert latency) requires the running federation. |
