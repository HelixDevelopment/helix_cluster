# Continuation Document

**Revision:** 76
**Last modified:** 2026-06-01T22:12:56Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `e93fad6` | Foundation wave 20: 3 disjoint streams (all approved; A minor silent-error-discard fixed by orchestrator). /metrics sidecar completing HXC-1110 'every binary' intent (pkg/metrics/sidecar.go StartSidecar/StartSidecarFromEnv opt-in HTTP listener HELIX_METRICS_ADDR-gated; in-process scrape asserts exact 'helix_sidecar_hits_total 3'; wired into the 6 gRPC-only mains advisory/build/node/scheduler/security/session; orchestrator added log-on-bind-error to all 6 to avoid silent failure). HXC-1248 YAML chaos Scenario Engine (NEW pkg/testing/scenario: Scenario{phases,faults,MaxFaults,MaxAffected,AbortOn} parsed via yaml.v3; Engine.Run->ExecutionLog with per-phase + global blast-radius clamp + abort-on-condition early-stop; deterministic, no wall-clock; reuses chaos catalog read-only). HXC-1244 simulated node fault injectors (NEW pkg/testing/chaos/node_faults.go: NodeRestart w/ recovery window, DiskSpaceExhaustion write-block, ProcessTerminate — each a real chaos.Fault with state-flipping Apply/Restore; distinct from existing NodeCrash/SlowDisk/OOMKill). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; mutation spot-checks B blast-radius + C node-restart bite; A sidecar exact-value scrape verified. |
| `b7f988f` | Foundation wave 19: mark 3 items Completed (HXC-1142/1096/1125) — batch/interactive mode switching+preemption / GPU /proc parser (fixture-tested) / Setup Wizard orchestrator (review caught+fixed a critical not-wired-into-main CLAUDE-1 bluff; node-config now real, verified sink-side). Registry 177->180 Completed / 459 Queued. |
| `9c58146` | Foundation wave 19: 3 disjoint streams (A/B approved no-fix; C had a CRITICAL CLAUDE-1 bluff CAUGHT+FIXED by review). HXC-1142 batch/interactive mode switching + preemption (pkg/agentprovision/modeswitch.go: ModeManager with REAL fixed-capacity resource pool — Interactive Submit preempts the most-recent Batch occupant when full, marks Preempted=true in an eviction ledger, frees+reuses the slot; SwitchMode mutates live record changing the preempt outcome; mutation: Preempted=true->false fails the bite). HXC-1096 GPU /proc parser (internal/gpu/nvidia_parser.go: PURE ParseNvidiaInformation/ParseNvidiaProcTree fixture-tested on M3 — exact Model/UUID/MemoryMB, MiB+MB units, no-Model->error, malformed-mem->0, contiguous Index + Model-less skip; nvidia_reader_linux.go //go:build linux real /proc reader; mutation: corrupt Model prefix fails parse). HXC-1125 Setup Wizard orchestrator (cmd/helix-setup: Orchestrator detect->GenerateConfig->mesh-join(MeshJoiner seam)->Lifecycle SM, real per-OS memory via mem_darwin/linux/other.go, scripts/helix-setup.sh entrypoint with driver-install as printed host-step). REVIEW CAUGHT: orchestrator was implemented+tested but NEVER wired into main() (main wrote a hardcoded static config -> 21 passing tests on UNREACHABLE code = CLAUDE-1 PASS-bluff). FIXED: wired NewOrchestrator(...).Run() into run(); binary now writes node-config.yaml with REAL hardware-detected tier (T2 on M3, cpu_cores 11), flag node_id/cluster_name, wg_endpoint — VERIFIED sink-side by me. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, mutation spot-checks A+B bite, C verified end-to-end via real binary run. |
| `9a42d7c` | Foundation wave 18: mark 3 items Completed (HXC-1098/1110/1141) — GPU sharing modes (honest ErrUnsupported hardware seam) / Prometheus /metrics wired into 5 HTTP services / interactive-agent admission+context registry. Mutation spot-checks confirm bites. Registry 174->177 Completed / 462 Queued. |
| `1a67f0a` | Foundation wave 18: 3 disjoint streams (all approved no-fix; mutation spot-checks confirm bites). HXC-1098 GPU sharing modes (internal/gpu/backend_sharing.go: SharingMode Exclusive/MPS/TimeSlice/MIG + REAL DeviceSharingState.Admit decisions — exclusive rejects 2nd job, time-slice rejects over-quantum, MIG rejects over-partition-limit, Release frees; ConfigureSharing hardware control returns typed ErrUnsupported on M3 via real AppleBackend.EnableMPS — NEVER fake hardware success; 25 tests). HXC-1110 Prometheus /metrics wiring (pkg/metrics/mount.go Mount+NewServiceRegistry reusing existing Registry/PrometheusHandler; in-process httptest scrape asserts exact 'helix_test_jobs_total 2' rejecting 0/1; WIRED into 5 HTTP-serving mains gateway/policy/helixd/health/llm; gRPC-only bins correctly skipped). HXC-1141 interactive-agent provisioning (NEW pkg/agentprovision: AgentAdmission per-node cap+queue + per-user rate-limit reusing pkg/ratelimit.PerKeyLimiter; ContextRegistry copy-isolated shared-context map; workload type defined locally — ZERO scheduler edits; 9 deterministic bite tests). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green under load, dataplane+security ok; mutation spot-checks: A ConfigureSharing fake-success->ErrUnsupported test FAILS, A cap-unlimited->queue test FAILS (C), confirming decisions bite. |
| `a0d623e` | Foundation wave 17: mark 3 items Completed (HXC-1097/1111/1112) — GPUBackend interface+registry+AutoDetect (honest ErrUnsupported for compute) / gRPC W3C trace-ID propagation (3-hop proven) / standard grpc.health.v1 wired into 6 services. All 3 mutation spot-checks confirm assertions bite. Registry 171->174 Completed / 465 Queued. |
| `05f408d` | Foundation wave 17: 3 disjoint streams (all approved no-fix; mutation spot-check confirms all 3 bite). HXC-1097 unified GPUBackend interface + BackendRegistry.AutoDetect (REAL probes nvidia-smi/rocm-smi/sycl-ls + Apple/Darwin backend with real system_profiler/sysctl device detection + metrics) — compute ops (Execute/ExecuteDistributed/AllocateMemory/MPS) return typed ErrUnsupported, NEVER fake success (CLAUDE-2 honest seam; mutation: fake-success->ErrUnsupported test FAILS). HXC-1111 gRPC W3C trace-ID propagation (pkg/tracing/grpc.go Unary client+server interceptors reusing existing Inject/Extract/ParseTraceParent; proven across a REAL 3-hop in-process gateway->scheduler->session chain — one trace ID survives, distinct per-hop parent; mutation: drop-inject->3-hop equality FAILS). HXC-1112 standard grpc.health.v1 (pkg/health/grpc.go RegisterGRPC+SetServing bridging health.Checker->SERVING/NOT_SERVING; in-process grpc_health_v1 client Check proves SERVING+NOT_SERVING; wired into 6 gRPC service mains advisory/build/node/scheduler/security/session; htmux/gateway/policy correctly skipped — no grpc.Server). ZERO new deps (used grpc's health/grpc_health_v1/metadata/bufconn already vendored). Gate: build/vet/vet-integration clean, full -short -race green, dataplane+security ok, 3 mutation spot-checks confirm assertions bite. |
| `6f55986` | Foundation wave 16: mark 6 items Completed (HXC-1238/1239/1240/1241/1138/1199) — BUGGIFY framework / DST virtual-time compression / 1000+ node scale (253k ev/s) / FoundationDB 4-phase workload / coverage+mutation gate tool / edge sensor-fusion. Adversarial review caught+fixed 3 real CLAUDE-1 bluffs; mutation spot-check confirms invariants bite. Registry 165->171 Completed / 468 Queued. |
| `75f076f` | Foundation wave 16: 6 disjoint pure-Go DST/testing-infra streams. HXC-1238 BUGGIFY framework (pkg/testing/dst/buggify.go: Buggify type + Engine.Buggify(p) driven by seeded PRNG, ~25% FireQuarter, enabled gate, BuggifyDuration 60s->0.1s timeout compression; determinism + 100k-trial fire-rate proofs). HXC-1239 virtual-time compression (NEW pkg/testing/dstcompress: Measure() real-wall-vs-virtual ratio + BatchSchedule; proves ~2e9:1 over 24h virtual horizon, >=8:1 at 1000 nodes). HXC-1240 1000+ node scale (NEW pkg/testing/dstscale: RunScale measured throughput/mem/virtual-span; -short 1000-node correctness + integration 1000x100k @ 253k ev/s, 12.8KB/node). HXC-1241 FoundationDB 4-phase workload (NEW pkg/testing/dstworkload: SETUP/EXECUTION/CHECK/METRICS over dst+chaos; real task-assignment protocol, chaos mutates Node.Online, NoLostTasks/NoDoubleAssignment/Quorum>=3 invariants that BITE, throughput+p99). HXC-1138 coverage+mutation gate tool (NEW pkg/covgate: ParseCoverProfile + threshold verdict + mutation-pairing scanner vs run_mutations.sh convention; fixture-driven FAIL cases). HXC-1199 edge sensor-fusion (NEW pkg/edgefusion: deterministic windowed multi-stream fusion, distinct workload class, exact-output assertion). Adversarial review caught+fixed 3 real CLAUDE-1 bluffs (B always-true WallElapsed<0 + self-referential same-timestamp; C always-true ClockMonotonic removed->measured ProcessedRatio; D structurally-dead NoDoubleAssignment map-loop -> duplicates seam). ZERO new deps, zero edits to existing files (6 disjoint dirs). Gate: build/vet/vet-integration clean, full -short -race green, dstscale integration 253k ev/s, mutation spot-check confirms D+F invariants bite. |
| `888a5a3` | Foundation wave 15: mark 4 items Completed (HXC-1136/1137/1231/1260) — session-create EXIT GATE proven vs real tmux / scheduler-latency EXIT GATE <100ms / provisioner-lifecycle-FSM / KPI-quality-gate. MVP exit-gate triad complete (1135+1136+1137). Registry 161->165 Completed / 474 Queued. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `e93fad6` |
| **Timestamp** | 2026-06-01T22:12:56Z |

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
