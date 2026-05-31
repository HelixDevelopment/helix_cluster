# Phase 7 — Code-Grounded Gap Audit

> **Auditor:** engineering auditor (anti-bluff, CLAUDE-1) | **Date:** 2026-06-01
> **Honest completion estimate: ~18% of Phase 7 named deliverables.**

**One-line summary:** None of the 15 planned `pkg/*` Phase 7 packages or the 2 planned `internal/*` packages exist by name; the only real Phase 7 progress is *partial* hardening folded into pre-existing packages (gang scheduling/preemption, session CRDT convergence + migration, SWIM suspicion, 2-of-3 health tiers, real etcd integration), and the rest of the 23-gap matrix (Multi-Raft, MVCC, backfill, hash-slot, voting, STONITH, DST/BUGGIFY, Porcupine, device-plugin/GRES, admission control, repair, BOINC trust) is MISSING.

---

## Method & Caveats

- Verified against actual source under `pkg/`, `internal/`, `cmd/`, `api/` — not against the prose report.
- Roadmap §4.1/§4.2 names 15 `pkg/*` + 2 `internal/*` *planned* packages. A directory-name check shows **0 of 17 exist by their planned name** (`pkg/{multiraft,mvcc,crdt,backfill,deviceplugin,hashslot,stonith,constraint,voting,scan,dst,porcupine,gangscheduler,admissioncontrol,repair}`, `internal/{advisory(trust),chaos}`).
- **Name collision warning:** `internal/advisory/` **exists but is NOT the planned BOINC trust-scoring package**. It is an advisory *distributed-lock* gRPC service (`internal/advisory/server.go:1` "Package advisory implements the Advisory Lock gRPC service"). Do not mark G-09 done from its presence.
- DONE requires real implementation **and** real-behavior tests (CLAUDE-1). Stub/mock-only/untested ⇒ PARTIAL/MISSING.

---

## Deliverable Status Table

| Deliverable (Gap / Pkg) | Status | Evidence (file:line) | Notes |
|---|---|---|---|
| Gang scheduling, all-or-nothing (G-10) `pkg/gangscheduler` | **PARTIAL** | `pkg/scheduler/gang_preempt.go:66` `ScheduleGang`; tests `pkg/scheduler/gang_preempt_test.go:10-340` | Real atomic placement + preemption with optimistic-version tests. NOT in planned `pkg/gangscheduler`; **no topology/NUMA/NVLink scoring** (the G-18 half). |
| Topology-aware NUMA/NVLink placement (G-18) | **MISSING** | no match for `numa\|nvlink\|topology` in scheduler/gpu | Not implemented. |
| Cost-aware GPU placement (Phase 8C, not P7) | DONE (out-of-scope) | `pkg/scheduler/cost_gpu.go:1`; `cost_gpu_test.go` | Real plugin+tests but labeled Phase 8C; not a Phase 7 gap. |
| CRDT delta-state sync (G-01 partial, P2) `pkg/crdt` | **PARTIAL** | `pkg/session/crdt.go:9`; convergence laws `pkg/session/convergence_test.go:102-209` | LWW only, session-scoped. No standalone `pkg/crdt` (G-counter/PN-counter/OR-set/LWW-map), no 5s delta cross-cell sync, not wired to `pkg/pubsub`/federation. |
| Session migration / ASM (G-03 part) `pkg/session` | **PARTIAL** | `pkg/session/migration.go:10` `MigrationPlanner`; lifecycle tests `pkg/session/lifecycle_test.go:93-152` | State-machine + CRIU/DMTCP/CRDT/container strategies modeled; lifecycle transitions tested. No hash-slot router, no MOVED/ASK, no Atomic Slot Migration. |
| Hash-slot router CRC16 + MOVED/ASK (G-03) `pkg/hashslot` | **MISSING** | no `crc16\|16384\|MOVED\|ASK\|hash slot` in `pkg/session`,`internal/gateway` | Absent. |
| SWIM PFAIL→FAIL two-phase consensus (G-23) | **PARTIAL** | `pkg/swim/suspicion.go:44` SuspicionManager (Suspect→indirect probe→Dead, incarnation refutation) | Single-node suspicion+refutation is real and tested (`suspicion_test.go`). **Missing majority/quorum confirmation** the roadmap requires for PFAIL→FAIL (`grep majority\|quorum` → none). |
| Three-tier health probes (G-04, P1) `pkg/health` | **PARTIAL** | `pkg/health/rollup.go:22-23` `Liveness`,`Readiness`; tests `rollup_test.go:94` | Only **2 of 3** tiers. **No `Startup` kind**, no GPU grace period. |
| MVCC revision store + B-tree + watch (G-08, P0) `pkg/mvcc` | **MISSING** | `pkg/etcd/package.go:118` only references etcd's own `mvccpb` enum | No own MVCC store, no time-travel, no bbolt B-tree backend. |
| Multi-Raft per-shard consensus (G-01, P0) `pkg/multiraft` | **MISSING** | no `multiraft\|multi-raft` in repo `*.go` | Absent; etcd single-write path unchanged. |
| SLURM backfill scheduler (G-02, P0) `pkg/backfill` | **MISSING** | no scheduler `backfill` symbol (only Herald docops backfill, unrelated) | Absent; no resource-availability timeline. |
| DST framework + 1k sim/commit (G-14/15, P0) `pkg/dst` | **MISSING** | no `dst\|turmoil\|buggify` in repo | Absent; no deterministic sim, no BUGGIFY macros. CI gate unenforceable. |
| Porcupine linearizability in CI (G-16, P1) `pkg/porcupine` | **MISSING** | no `porcupine` in repo | Absent. |
| Voting quorum, largest-subcluster-wins (G-19, P0) `pkg/voting` | **MISSING** | no `voting\|largest subcluster` in repo | Absent; split-brain unresolved. |
| STONITH fencing agents (G-19, P1) `pkg/stonith` | **MISSING** | no `stonith\|fenc` in repo | Absent. |
| Device-plugin / GRES descriptors (G-11/13/17, P1) `pkg/deviceplugin` | **MISSING** | `internal/gpu/manager.go` has mock detect only (`gpu_test.go:TestDetectGPUsMock`); no `gres\|fingerprint` | GPU manager allocates/releases (real tests) but is not an extensible GRES fingerprinting framework. |
| BOINC trust model (G-09, P1) `internal/advisory` | **MISSING** | `internal/advisory/server.go:1` is a **lock** service, not trust | Planned package not implemented; name collision. |
| Failover admission control (G-22, P1) `pkg/admissioncontrol` | **MISSING** | no `admission` in `internal/gateway`/scheduler | Absent. |
| Cassandra 3-layer repair (G-01 part, P1) `pkg/repair` | **MISSING** | no `merkle\|anti-entropy\|hinted handoff` in repo | Absent. |
| Pacemaker 4-type constraint engine (G-20, P2) `pkg/constraint` | **MISSING** | no `pkg/constraint`; no colocation/ordering/stickiness engine | Absent. |
| SCAN stable VIP/DNS (G-21, P2) `pkg/scan` | **MISSING** | no `pkg/scan` | Absent. |
| APF FlowSchema→PriorityLevel→Queue (G-07, P2) `pkg/security` | **MISSING** | `pkg/security/` = spiffe/tls/vault only | Not implemented; existing `pkg/ratelimit` is generic, not APF. |
| Informer cache `helixcache.Watcher` + rate-limited queue (G-05/06, P2) `internal/node` | **MISSING** | `internal/node/` = node/server only; no `helixcache`/informer | Absent. |
| Nightly chaos pipeline (G-15 part) `internal/chaos` | **MISSING** | `internal/chaos` absent | Absent. |
| TLA+ formal specs (P2) | **MISSING** | no `.tla` specs found | Absent. |
| etcd real integration (substrate, supports G-08) | **DONE** | `pkg/etcd/etcd_integration_test.go:26` real broker via `brokertest.StartEtcd`; sink-side tests `:148,:177` | Genuine real-service tests (KV/watch/lease/lock). Supports but does not constitute MVCC/Multi-Raft. |

**Scoreboard:** DONE 1 (etcd substrate) · PARTIAL 5 (gang/preempt, session CRDT, session migration, SWIM suspicion, 2-tier health) · MISSING 17+ of the named 23-gap matrix. The 7 P0 blockers (Multi-Raft, backfill, DST, BUGGIFY, voting, MVCC, hash-slot) are **all MISSING**.

---

## TOP IMPLEMENTABLE GAPS (pure-Go, no new infra)

Prioritized; each is self-contained, mutation-pairable, and needs no external hardware/cluster.

### 1. `pkg/hashslot` — CRC16 16,384-slot router (G-03, P0)
**Target dir:** `pkg/hashslot/`. Implement Redis-compatible `CRC16(key)%16384` slot mapping, a `SlotMap` (slot→nodeID) with `Owner(key)`, `Reshard(slots, dst)`, and `MOVED`/`ASK` redirection structs for in-flight migration (Atomic Slot Migration: source serves `ASK` while a slot is `MIGRATING`, dest accepts on `ASKING`). Pure computation + in-memory map; wire later into `internal/gateway`. **Acceptance test (mutation-pairable):** assert known Redis CRC16 vectors (e.g. `"123456789"→0x31C3`, `{user1000}` hashtag extraction routes to same slot as `user1000`); mutation = drop the `{}`-hashtag carve-out or change modulus → hashtag co-location and vector tests fail.

### 2. `pkg/voting` — largest-subcluster-wins quorum (G-19, P0)
**Target dir:** `pkg/voting/`. In-memory vote store with per-voter TTL (3s) and a `Resolve(view []Membership) Decision` that grants survival to the partition holding a strict majority of registered votes (tie → deterministic lowest-ID loses, all-fence). Pure logic over an injected clock. **Acceptance test:** 5-node set partitioned 3/2 → 3-side `Survive`, 2-side `Fence`; even 2/2 split → both fence. Mutation = flip `>` to `>=` in the majority check → the 2/2 split test (which must NOT grant survival) fails.

### 3. `pkg/backfill` — SLURM-style backfill over an availability timeline (G-02, P0)
**Target dir:** `pkg/backfill/`. Build a resource-availability timeline from running jobs' (end-time, freed-resources); EASY-backfill lower-priority jobs into gaps **iff** they do not delay the highest-priority reserved job. Pure scheduling math against existing `pkg/scheduler` `Job`/`Node` types. **Acceptance test:** craft a queue where a small short job fits a gap before the head reservation (must backfill) vs. a long job that would delay it (must NOT). Mutation = remove the "does not delay reservation" guard → the long-job-must-not-backfill test fails.

### 4. `pkg/health` Startup tier + GPU grace (G-04, P1)
**Target dir:** `pkg/health/` (extend `rollup.go`). Add `Startup CheckKind` + `CheckStartup(ctx)` plus a per-dependency `GracePeriod` so startup failures are tolerated until elapsed. **Acceptance test:** a dep failing within grace reports `Healthy` under `CheckStartup` but the *same* dep fails `CheckReadiness`; after grace elapses (injected clock) startup turns `Unhealthy`. Mutation = ignore `GracePeriod` → pre-grace startup test fails.

### 5. SWIM majority PFAIL→FAIL confirmation (G-23, P1)
**Target dir:** `pkg/swim/` (extend suspicion). Add `FailureVotes` so a member only transitions Suspect→Dead after ≥⌈N/2⌉ distinct gossiped suspicions (PFAIL→FAIL), not on a single node's timeout. **Acceptance test:** with N=5, 2 suspicions keep state Suspect; the 3rd promotes to Dead and fires `OnDead` once. Mutation = lower threshold to 1 → the "2 suspicions must stay Suspect" assertion fails.

### 6. `pkg/crdt` — standalone delta-state CRDT set (G-01 partial / G-18 Phase-8 dep, P2)
**Target dir:** `pkg/crdt/`. Implement G-counter, PN-counter, and OR-set with `Merge` and delta extraction (`Delta(sinceVersion)`). Generalizes the LWW logic already proven in `pkg/session/convergence_test.go`. **Acceptance test:** property tests for commutativity/associativity/idempotency of `Merge` (mirror `convergence_test.go:102-209`); OR-set add-then-remove across reordered deltas converges. Mutation = make OR-set removal drop the unique-tag set → concurrent add/remove convergence test fails.

### 7. `internal/advisory`-trust → new `internal/trust` BOINC redundant-execution scorer (G-09, P1)
**Target dir:** `internal/trust/` (avoid the lock-service name collision). Track per-node trust from result agreement across redundant executions: quorum match raises trust, disagreement (outlier) lowers it; expose `Trusted(nodeID) bool` gated on a threshold. Pure scoring logic. **Acceptance test:** 3 nodes return result R, 1 returns R'; the outlier's trust drops below threshold and is quarantined while the 3 stay trusted. Mutation = score the outlier as if it agreed → quarantine assertion fails.

### 8. `pkg/admissioncontrol` — failover capacity reservation (G-22, P1)
**Target dir:** `pkg/admissioncontrol/`. Before accepting a workload, verify the cluster retains enough slack to absorb the loss of the K largest nodes (vSphere "N+K"); reject admission that would breach the reserve. Pure arithmetic over `pkg/scheduler` `Node` capacities. **Acceptance test:** admit while N+1 slack holds; reject the request that would consume the reserved failover headroom. Mutation = drop the reserved-headroom subtraction → the over-admit-rejection test fails.

---

## DEFERRED (need external infra / hardware / non-Go) — 6 clusters

- **DST framework + BUGGIFY + 1,000 sim/commit (G-14/15, P0):** DEFERRED — requires a deterministic-simulation harness and CI-gate wiring (Turmoil-class); large cross-cutting effort, not a single Go unit. A *toy* deterministic scheduler-sim is implementable but the roadmap's CI gate is infra.
- **Porcupine linearizability in CI (G-16, P1):** DEFERRED — depends on the `porcupine` checker + CI pipeline gating; history-recorder shim is Go-implementable but the gate is infra.
- **Nightly chaos pipeline `internal/chaos` (G-15):** DEFERRED — needs GitHub Actions + Chaos Mesh / live cluster (pod kill, partition, disk stall, clock skew).
- **STONITH fencing agents `pkg/stonith` (G-19, P1):** DEFERRED — IPMI / AWS EC2 / Azure ARM / SBD shared-disk drivers require real hardware/cloud endpoints. (A pluggable *interface* + a no-op/mock agent is Go-only, but real fencing is infra.)
- **Multi-Raft + MVCC bbolt backend (G-01/G-08, P0):** PARTIALLY DEFERRED — a Go implementation is possible but is a major storage-engine subsystem (raft groups, persistent watch streams, B-tree) far beyond a mutation-pairable unit; recommend staging behind the smaller wins above.
- **TLA+ specs + GPU NUMA/NVLink topology (G-18/P2):** DEFERRED — TLA+ is non-Go (TLC model checker); NVLink/NUMA scoring needs real topology data from GPU hardware.
