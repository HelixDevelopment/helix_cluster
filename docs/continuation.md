# Continuation Document

**Revision:** 69
**Last modified:** 2026-06-01T21:17:16Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `6f55986` | Foundation wave 16: mark 6 items Completed (HXC-1238/1239/1240/1241/1138/1199) — BUGGIFY framework / DST virtual-time compression / 1000+ node scale (253k ev/s) / FoundationDB 4-phase workload / coverage+mutation gate tool / edge sensor-fusion. Adversarial review caught+fixed 3 real CLAUDE-1 bluffs; mutation spot-check confirms invariants bite. Registry 165->171 Completed / 468 Queued. |
| `75f076f` | Foundation wave 16: 6 disjoint pure-Go DST/testing-infra streams. HXC-1238 BUGGIFY framework (pkg/testing/dst/buggify.go: Buggify type + Engine.Buggify(p) driven by seeded PRNG, ~25% FireQuarter, enabled gate, BuggifyDuration 60s->0.1s timeout compression; determinism + 100k-trial fire-rate proofs). HXC-1239 virtual-time compression (NEW pkg/testing/dstcompress: Measure() real-wall-vs-virtual ratio + BatchSchedule; proves ~2e9:1 over 24h virtual horizon, >=8:1 at 1000 nodes). HXC-1240 1000+ node scale (NEW pkg/testing/dstscale: RunScale measured throughput/mem/virtual-span; -short 1000-node correctness + integration 1000x100k @ 253k ev/s, 12.8KB/node). HXC-1241 FoundationDB 4-phase workload (NEW pkg/testing/dstworkload: SETUP/EXECUTION/CHECK/METRICS over dst+chaos; real task-assignment protocol, chaos mutates Node.Online, NoLostTasks/NoDoubleAssignment/Quorum>=3 invariants that BITE, throughput+p99). HXC-1138 coverage+mutation gate tool (NEW pkg/covgate: ParseCoverProfile + threshold verdict + mutation-pairing scanner vs run_mutations.sh convention; fixture-driven FAIL cases). HXC-1199 edge sensor-fusion (NEW pkg/edgefusion: deterministic windowed multi-stream fusion, distinct workload class, exact-output assertion). Adversarial review caught+fixed 3 real CLAUDE-1 bluffs (B always-true WallElapsed<0 + self-referential same-timestamp; C always-true ClockMonotonic removed->measured ProcessedRatio; D structurally-dead NoDoubleAssignment map-loop -> duplicates seam). ZERO new deps, zero edits to existing files (6 disjoint dirs). Gate: build/vet/vet-integration clean, full -short -race green, dstscale integration 253k ev/s, mutation spot-check confirms D+F invariants bite. |
| `888a5a3` | Foundation wave 15: mark 4 items Completed (HXC-1136/1137/1231/1260) — session-create EXIT GATE proven vs real tmux / scheduler-latency EXIT GATE <100ms / provisioner-lifecycle-FSM / KPI-quality-gate. MVP exit-gate triad complete (1135+1136+1137). Registry 161->165 Completed / 474 Queued. |
| `553a043` | Foundation wave 15: 4 streams, 4 items incl 2 MVP exit gates. HXC-1136 session-create EXIT GATE — wired pkg/session.Manager to a REAL tmux/PTY backend via BackendAdapter (fixed the nil-backend StatusRunning PASS-bluff in internal/session.NewServer; added NewServerWithTmuxBackend seam + SessionExists sink-probe); E2E proves htmux create -> helix-session -> real tmux session visible running in 388ms (<3s). HXC-1137 scheduler decision-latency EXIT GATE (<100ms across 10 jobs, histogram + placement-correctness + 120ms-slow-plugin mutation). NEW pkg/testing/instance Provisioner/Instance lifecycle FSM (provisioned->booting->ready->stopped on virtual clock, illegal-transition reject). NEW pkg/qualitygate KPI baseline validation + critical-severity CI gate (8 rules). ZERO new deps. GATE caught+fixed a real regression: 1136's real backend exposed test/chaos+test/e2e relying on the no-op bluff (1000 free sessions -> 509 leaked tmux -> PTY exhaustion) -> switched those harnesses to the in-memory NewServerWithTmuxBackend(nil) seam + added SessionExists to the test double. Full -short -race green, 1136 real-tmux E2E + 1231/1260 integration green, live mutation spot-check on qualitygate critical gate confirmed real. |
| `34efa5d` | Foundation wave 14: mark 4 items Completed (HXC-1135/1229/1247/1261) — MVP two-node-formation EXIT GATE proven vs real etcd / chaos-sequencer+Byzantine / device-profile-registry / Welch-t-test. Registry 157->161 Completed / 478 Queued. |
| `1c0d943` | Foundation wave 14: 4 disjoint streams, 4 items incl the MVP two-node-formation EXIT GATE. cmd/helix-agent --etcd-endpoints/HELIX_ETCD_ENDPOINTS wiring + HXC-1135 E2E (two REAL helix-agent processes form a cluster via real etcd, both visible <60s w/ per-run UUID + formation timestamp + evidence file); pkg/testing/chaos seeded deterministic fault sequencer (reproducible injection log) + Byzantine equivocation fault; NEW pkg/deviceprofile versioned-YAML device profile registry T1-T8 + schema validation/rejection; NEW pkg/stats Welch t-test regression detector (t-stat/Welch-Satterthwaite df/p-value). ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, 1135 two-process formation PROVEN vs REAL etcd (2.76s), deviceprofile+stats integration green, live mutation spot-check on Welch significance confirmed real. |
| `e1dab02` | Foundation wave 13: mark 6 items Completed (HXC-1189/1190/1198/1210/1213/1220) — gateway-auth-deny-reason / node-etcd-wiring (real etcd cross-visibility) / OPA-decision-log / EdgeAware-Filter+Score / offline-sync-delta-compression. Registry 151->157 Completed / 482 Queued. |
| `203c42a` | Foundation wave 13: 5 disjoint-package streams, 6 items. internal/gateway X-Helix-Deny-Reason header + scheduler:write scope enforcement (401/403 reject-before-proxy); internal/node REAL EtcdBackend wiring (Config.EtcdEndpoints -> NewEtcdBackend w/ SWIM-tied TTL lease + resource metadata, BackendType() status, in-memory fallback) — node registrations now cluster-visible (unblocks 1135 two-node formation); internal/policy real persisted OPA decision-log sink (DecisionLogEntry RunUUID/Allow/Reason/PolicyName); NEW pkg/scheduler/edgeaware.go EdgeAware Filter (tier>=6 reject session/sleeping/over-thermal/not-charging w/ reasons) + Score (per-tier+thermal-headroom bonuses); NEW pkg/offlinesync delta-compression (flate) offline job-ledger + idempotent reconcile (100% recovery). ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, integration tier 1210 vs REAL etcd (A sees B+metadata, UUID) + gateway/policy green, live mutation spot-check on edgeaware T6 guard confirmed real. |
| `23df87b` | Foundation wave 12: mark 14 items Completed (HXC-1148/1149/1150/1151/1156/1157/1188/1191/1195/1196/1211/1212/1214/1215) — 12 integration-proven vs real etcd/minio + 2 gate-verified already-implemented (STUN/OTLP). Registry 137->151 Completed / 488 Queued. |
| `7718c20` | Foundation wave 12: 5 disjoint-package streams, 12 items integration-proven vs real etcd/minio. pkg/wireguard typed ErrUnsupported (errors.Is) + key-rotation grace/overlap ActiveKeys() consumed by mesh; pkg/scheduler named ClassAdFilter/ScorePlugin (classads Requirements/Rank) + thermal-aware plugin (filter/score on temp_c threshold); internal/scheduler real PREEMPTED lifecycle event (terminal, ordered); NEW pkg/edge trust model (FULL/STANDARD/SEMI/EDGE_DONOR) + workload restriction matrix + work-unit resource-limit executor (kill-after duration) + declarative per-tier ScheduleRule engine; NEW internal/verifier SEMI output verification (sha256 redundant-recompute reject tamper) + pkg/discovery etcd TTL-lifecycle integration test + pkg/storage real-minio S3 round-trip integration test. ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, integration tier vs real etcd(:38623 ephemeral)+minio(:9000) green, mutation spot-check on verifier confirmed real. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `6f55986` |
| **Timestamp** | 2026-06-01T21:17:16Z |

## §3: Active Work

| HXC | Title | Status |
|-----|-------|--------|
| HXC-922 | Node service, advisory locks, chaos tests, docs | ✅ Done |
| HXC-921 | Web UI, K8s/Helm, integration, E2E, benchmarks | ✅ Done |
| HXC-920 | Health, security, build, wireguard services | ✅ Done |
| HXC-919 | htmux, messaging, GPU, WASM, storage, build manifest | ✅ Done |
| HXC-918 | Wire scheduler/session gRPC to real backends, etcd discovery | ✅ Done |
| HXC-916 | Stub expansion (config, retry, ratelimit, validator, build cache) | ✅ Done |
| HXC-914 | LLM, policy, setup, distributed lock | ✅ Done |
| HXC-913 | Health, security, ClassAds parser | ✅ Done |
| HXC-912 | Gateway, helixd, helix-agent | ✅ Done |
| HXC-911 | Scheduler, Session gRPC services | ✅ Done |
| HXC-910 | Console, security, testing infra | ✅ Done |

## §4: Next Planned Work

All MVP, Phase 2, Phase 3, and Phase 4 are **COMPLETE**. Potential future enhancements:

1. **Performance optimization** — Profile and optimize hot paths
2. **Multi-region support** — Cross-region cluster federation
3. **GPU scheduling v2** — Multi-GPU, fractional GPU allocation
4. **Web UI real-time** — WebSocket integration for live updates
5. **Operator pattern** — Kubernetes operator for cluster management

## §5: Known Issues / Blockers

- None

## §6: Quick Commands

```bash
# Run all tests (requires GOWORK due to workspace modules)
GOWORK=$(pwd)/go.work go test ./... -race -count=1

# Build all binaries
GOWORK=$(pwd)/go.work go build ./cmd/...

# Build web UI
cd web && npm run build

# Render Helm chart
helm template helix-cluster deploy/helm/

# Run benchmarks
go test -bench=. ./test/benchmark

# Run chaos tests
go test -race -count=1 ./test/chaos
```
