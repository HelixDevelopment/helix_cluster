# Continuation Document

**Revision:** 72
**Last modified:** 2026-06-01T21:35:04Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `1a67f0a` | Foundation wave 18: 3 disjoint streams (all approved no-fix; mutation spot-checks confirm bites). HXC-1098 GPU sharing modes (internal/gpu/backend_sharing.go: SharingMode Exclusive/MPS/TimeSlice/MIG + REAL DeviceSharingState.Admit decisions — exclusive rejects 2nd job, time-slice rejects over-quantum, MIG rejects over-partition-limit, Release frees; ConfigureSharing hardware control returns typed ErrUnsupported on M3 via real AppleBackend.EnableMPS — NEVER fake hardware success; 25 tests). HXC-1110 Prometheus /metrics wiring (pkg/metrics/mount.go Mount+NewServiceRegistry reusing existing Registry/PrometheusHandler; in-process httptest scrape asserts exact 'helix_test_jobs_total 2' rejecting 0/1; WIRED into 5 HTTP-serving mains gateway/policy/helixd/health/llm; gRPC-only bins correctly skipped). HXC-1141 interactive-agent provisioning (NEW pkg/agentprovision: AgentAdmission per-node cap+queue + per-user rate-limit reusing pkg/ratelimit.PerKeyLimiter; ContextRegistry copy-isolated shared-context map; workload type defined locally — ZERO scheduler edits; 9 deterministic bite tests). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green under load, dataplane+security ok; mutation spot-checks: A ConfigureSharing fake-success->ErrUnsupported test FAILS, A cap-unlimited->queue test FAILS (C), confirming decisions bite. |
| `a0d623e` | Foundation wave 17: mark 3 items Completed (HXC-1097/1111/1112) — GPUBackend interface+registry+AutoDetect (honest ErrUnsupported for compute) / gRPC W3C trace-ID propagation (3-hop proven) / standard grpc.health.v1 wired into 6 services. All 3 mutation spot-checks confirm assertions bite. Registry 171->174 Completed / 465 Queued. |
| `05f408d` | Foundation wave 17: 3 disjoint streams (all approved no-fix; mutation spot-check confirms all 3 bite). HXC-1097 unified GPUBackend interface + BackendRegistry.AutoDetect (REAL probes nvidia-smi/rocm-smi/sycl-ls + Apple/Darwin backend with real system_profiler/sysctl device detection + metrics) — compute ops (Execute/ExecuteDistributed/AllocateMemory/MPS) return typed ErrUnsupported, NEVER fake success (CLAUDE-2 honest seam; mutation: fake-success->ErrUnsupported test FAILS). HXC-1111 gRPC W3C trace-ID propagation (pkg/tracing/grpc.go Unary client+server interceptors reusing existing Inject/Extract/ParseTraceParent; proven across a REAL 3-hop in-process gateway->scheduler->session chain — one trace ID survives, distinct per-hop parent; mutation: drop-inject->3-hop equality FAILS). HXC-1112 standard grpc.health.v1 (pkg/health/grpc.go RegisterGRPC+SetServing bridging health.Checker->SERVING/NOT_SERVING; in-process grpc_health_v1 client Check proves SERVING+NOT_SERVING; wired into 6 gRPC service mains advisory/build/node/scheduler/security/session; htmux/gateway/policy correctly skipped — no grpc.Server). ZERO new deps (used grpc's health/grpc_health_v1/metadata/bufconn already vendored). Gate: build/vet/vet-integration clean, full -short -race green, dataplane+security ok, 3 mutation spot-checks confirm assertions bite. |
| `6f55986` | Foundation wave 16: mark 6 items Completed (HXC-1238/1239/1240/1241/1138/1199) — BUGGIFY framework / DST virtual-time compression / 1000+ node scale (253k ev/s) / FoundationDB 4-phase workload / coverage+mutation gate tool / edge sensor-fusion. Adversarial review caught+fixed 3 real CLAUDE-1 bluffs; mutation spot-check confirms invariants bite. Registry 165->171 Completed / 468 Queued. |
| `75f076f` | Foundation wave 16: 6 disjoint pure-Go DST/testing-infra streams. HXC-1238 BUGGIFY framework (pkg/testing/dst/buggify.go: Buggify type + Engine.Buggify(p) driven by seeded PRNG, ~25% FireQuarter, enabled gate, BuggifyDuration 60s->0.1s timeout compression; determinism + 100k-trial fire-rate proofs). HXC-1239 virtual-time compression (NEW pkg/testing/dstcompress: Measure() real-wall-vs-virtual ratio + BatchSchedule; proves ~2e9:1 over 24h virtual horizon, >=8:1 at 1000 nodes). HXC-1240 1000+ node scale (NEW pkg/testing/dstscale: RunScale measured throughput/mem/virtual-span; -short 1000-node correctness + integration 1000x100k @ 253k ev/s, 12.8KB/node). HXC-1241 FoundationDB 4-phase workload (NEW pkg/testing/dstworkload: SETUP/EXECUTION/CHECK/METRICS over dst+chaos; real task-assignment protocol, chaos mutates Node.Online, NoLostTasks/NoDoubleAssignment/Quorum>=3 invariants that BITE, throughput+p99). HXC-1138 coverage+mutation gate tool (NEW pkg/covgate: ParseCoverProfile + threshold verdict + mutation-pairing scanner vs run_mutations.sh convention; fixture-driven FAIL cases). HXC-1199 edge sensor-fusion (NEW pkg/edgefusion: deterministic windowed multi-stream fusion, distinct workload class, exact-output assertion). Adversarial review caught+fixed 3 real CLAUDE-1 bluffs (B always-true WallElapsed<0 + self-referential same-timestamp; C always-true ClockMonotonic removed->measured ProcessedRatio; D structurally-dead NoDoubleAssignment map-loop -> duplicates seam). ZERO new deps, zero edits to existing files (6 disjoint dirs). Gate: build/vet/vet-integration clean, full -short -race green, dstscale integration 253k ev/s, mutation spot-check confirms D+F invariants bite. |
| `888a5a3` | Foundation wave 15: mark 4 items Completed (HXC-1136/1137/1231/1260) — session-create EXIT GATE proven vs real tmux / scheduler-latency EXIT GATE <100ms / provisioner-lifecycle-FSM / KPI-quality-gate. MVP exit-gate triad complete (1135+1136+1137). Registry 161->165 Completed / 474 Queued. |
| `553a043` | Foundation wave 15: 4 streams, 4 items incl 2 MVP exit gates. HXC-1136 session-create EXIT GATE — wired pkg/session.Manager to a REAL tmux/PTY backend via BackendAdapter (fixed the nil-backend StatusRunning PASS-bluff in internal/session.NewServer; added NewServerWithTmuxBackend seam + SessionExists sink-probe); E2E proves htmux create -> helix-session -> real tmux session visible running in 388ms (<3s). HXC-1137 scheduler decision-latency EXIT GATE (<100ms across 10 jobs, histogram + placement-correctness + 120ms-slow-plugin mutation). NEW pkg/testing/instance Provisioner/Instance lifecycle FSM (provisioned->booting->ready->stopped on virtual clock, illegal-transition reject). NEW pkg/qualitygate KPI baseline validation + critical-severity CI gate (8 rules). ZERO new deps. GATE caught+fixed a real regression: 1136's real backend exposed test/chaos+test/e2e relying on the no-op bluff (1000 free sessions -> 509 leaked tmux -> PTY exhaustion) -> switched those harnesses to the in-memory NewServerWithTmuxBackend(nil) seam + added SessionExists to the test double. Full -short -race green, 1136 real-tmux E2E + 1231/1260 integration green, live mutation spot-check on qualitygate critical gate confirmed real. |
| `34efa5d` | Foundation wave 14: mark 4 items Completed (HXC-1135/1229/1247/1261) — MVP two-node-formation EXIT GATE proven vs real etcd / chaos-sequencer+Byzantine / device-profile-registry / Welch-t-test. Registry 157->161 Completed / 478 Queued. |
| `1c0d943` | Foundation wave 14: 4 disjoint streams, 4 items incl the MVP two-node-formation EXIT GATE. cmd/helix-agent --etcd-endpoints/HELIX_ETCD_ENDPOINTS wiring + HXC-1135 E2E (two REAL helix-agent processes form a cluster via real etcd, both visible <60s w/ per-run UUID + formation timestamp + evidence file); pkg/testing/chaos seeded deterministic fault sequencer (reproducible injection log) + Byzantine equivocation fault; NEW pkg/deviceprofile versioned-YAML device profile registry T1-T8 + schema validation/rejection; NEW pkg/stats Welch t-test regression detector (t-stat/Welch-Satterthwaite df/p-value). ZERO new deps. GATE: build/vet/vet-integration clean, full -short -race green, 1135 two-process formation PROVEN vs REAL etcd (2.76s), deviceprofile+stats integration green, live mutation spot-check on Welch significance confirmed real. |
| `e1dab02` | Foundation wave 13: mark 6 items Completed (HXC-1189/1190/1198/1210/1213/1220) — gateway-auth-deny-reason / node-etcd-wiring (real etcd cross-visibility) / OPA-decision-log / EdgeAware-Filter+Score / offline-sync-delta-compression. Registry 151->157 Completed / 482 Queued. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `1a67f0a` |
| **Timestamp** | 2026-06-01T21:35:04Z |

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
