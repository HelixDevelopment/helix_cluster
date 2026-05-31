# Helix Cluster OS — Phase 8B Roadmap: Reverse Integration (Chutes AI)

> **Research Document** | Phase 8B Planning | 2026-05-31
>
> This document defines Phase 8B — the inverse of Phase 8 — covering how HelixCluster
> consumes Chutes AI and other decentralized GPU clouds as external compute sources
> rather than participating in those networks as miners or validators.

---

## 1. Current State Summary

### Phases 0–8 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | Complete | 29 submodules, CI/CD, 20-service docker-compose, 26 pkg stubs, go.work, buf pipeline |
| **1** | Core Infrastructure | Complete | Container orchestration (`pkg/infra`), VM testing, CLI skeleton |
| **2** | Console Nodes & Distributed Foundations | Complete | SWIM gossip, WireGuard mesh, Omega scheduler stubs, session CRDT |
| **3** | Edge & Mobile | Complete | etcd integration, full scheduler, WebSocket I/O, mTLS/SPIFFE, ARM64 agents |
| **4** | Security Hardening | Complete | SPIFFE/SPIRE, policy engine, Vault integration, audit logging |
| **5** | Observability & Telemetry | Complete | Prometheus/OTel pipeline, Jaeger tracing, Grafana dashboards |
| **6** | Advanced Scheduling & ClassAds | Complete | ClassAds expressions, Omega concurrency, pluggable score pipeline |
| **7** | GPU Compute Layer | Complete | NVML integration, GPU resource aggregation, local vLLM serving |
| **8** | Chutes AI Forward Integration | Complete | MinerController, Chutes API client, RewardDistributor, GraValVerifier, UnifiedMarketplaceManager |

### Phase 8B Research Artifacts

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE8B_REVERSE_ARCHITECTURE.md` | `docs/research/phase_08_B/HelixCluster_Phase_8B/` | Complete reverse-integration architecture (10,000+ words, 102 code blocks) |
| `HELIXCLUSTER_PHASE8B_COMPLETE_REPORT.converted.md` | `docs/research/phase_08_B/HelixCluster_Phase_8B/` | Full Phase 8B deliverables summary |
| `helixcluster_phase8b_sec00.md` | `docs/research/phase_08_B/HelixCluster_Phase_8B/` | Executive summary and paradigm shift description |
| `helixcluster_phase8b_sec02.md` | `docs/research/phase_08_B/HelixCluster_Phase_8B/` | Remote GPU node abstraction and virtual device layer |
| `helixcluster_phase8b_sec06.md` | `docs/research/phase_08_B/HelixCluster_Phase_8B/` | Complete implementation and 24-week roadmap |

### Phase 8 vs Phase 8B: The Direction Reversal

| Dimension | Phase 8 (Forward) | Phase 8B (Reverse) |
|-----------|------------------|--------------------|
| **Role in ecosystem** | HelixCluster *joins* Chutes as miner/validator | HelixCluster *consumes* Chutes as API client |
| **Token exposure** | Full (must hold/stake TAO) | None (pay in USD/crypto per use) |
| **Capital required** | $50K–500K (hardware + stake) | $0–100 (API credits) |
| **Setup time** | Weeks (sync blockchain, configure miner) | Minutes (API key + config entry) |
| **Relationship model** | HelixCluster subordinate to subnet rules | HelixCluster is the orchestrator; network is a supplier |
| **Revenue direction** | Earn TAO from inference serving | Save cost vs. hyperscalers (50–90%) |
| **Complexity** | High (Bittensor protocol, validator scoring) | Low (OpenAI-compatible REST API) |
| **Go implementations** | MinerController, RewardDistributor | GPUPoolManager, BurstController, ProviderAdapters |

> **Core principle of 8B:** _"We do not join their network. We make their network serve us."_

---

## 2. Phase 8B Scope & Goals

### Primary Objective

Transform HelixCluster into a **unified GPU compute consumer** that treats Chutes AI,
io.net, RunPod, Akash, and hyperscaler clouds as fungible, interchangeable compute
sources — all controlled by HelixCluster's own scheduler, encrypted by HelixCluster's
own E2EE stack, and routed by HelixCluster's own cost optimizer.

### Four Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Unified GPU Pool** | Four-tier hierarchy (Local → Remote Proxy → Cloud → Decentralized) behind one `GPUProvider` interface | Remote GPUs appear as local devices |
| **Post-Quantum Security** | ML-KEM-768 + ChaCha20-Poly1305 E2EE; GraVal GPU verification; Intel TDX attestation | All remote traffic quantum-safe; fake-GPU fraud eliminated |
| **Economic Arbitrage** | ComputeBroker scores providers in real time; CostAwareScheduler routes to cheapest meeting SLA | 50–90% cost reduction vs. AWS on-demand |
| **Elastic Burst** | BurstController state machine spills workloads at 90% local utilization; recovers at 63% | Zero capacity planning; infinite elastic headroom |

### Four-Tier GPU Hierarchy

| Tier | Priority | Cost | Latency | Source |
|------|----------|------|---------|--------|
| **Local** (owned) | 1 — highest | $0.31–2.78/hr effective | <50 ms | RTX 4090, A100 80GB, H100 80GB |
| **Remote GPU Proxy** | 2 | $1.03–2.69/hr | 50–200 ms | io.net Ray clusters, RunPod serverless |
| **Cloud Burst** | 3 | $2.69–12.29/hr | 20–100 ms | AWS EC2 Spot, GCP, Azure |
| **Decentralized** | 4 — elastic | $0.28–1.80/1M tokens | 100–500 ms | Chutes AI, io.net, Akash |

---

## 3. Phase 8B Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **8B-a** | Chutes Consumer + E2EE | 6 | 40 | E2EE proxy (ML-KEM-768), ChutesProvider, local vLLM stack; e2e encrypted inference through Chutes TEE at p99 <500 ms |
| **8B-b** | GPU Pool Manager + Remote Proxy | 6 | 45 | PoolManager, IONetProvider, RunPodProvider, CUDA API interceptor, virtual `/dev/nvidia*`, Docker Compose dev stack |
| **8B-c** | Multi-Platform + Burst Controller | 6 | 50 | AWSProvider (Spot), BurstController state machine, ComputeBroker, Helm chart, Prometheus/Grafana dashboards, 48-hour load test |
| **8B-d** | TEE + Production Hardening | 6 | 45 | GraVal three-phase verification, TDX attestation enforcement, key rotation via CronJob, chaos tests, 99.9% soak |

**Total: 24 weeks | ~180 tasks | ~720 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 8B, planned)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/pool` *(planned)* | `GPUProvider` interface, `VirtualGPU`, `WorkloadRequest`, `PoolManager`, `Scheduler` | `cmd/gpu-pool-manager`, `internal/gpu` |
| `pkg/pool/scheduler` *(planned)* | `PriorityScheduler`, `CostAwareScheduler`, `LatencyAwareScheduler` | `pkg/pool` |
| `pkg/provider/chutes` *(planned)* | `ChutesProvider` — OpenAI-compatible REST adapter with ML-KEM-768 E2EE | `pkg/pool`, `pkg/e2ee` |
| `pkg/provider/ionet` *(planned)* | `IONetProvider` — Ray cluster adapter for multi-GPU training bursts | `pkg/pool` |
| `pkg/provider/runpod` *(planned)* | `RunPodProvider` — serverless GPU adapter with warm-pool pre-warming | `pkg/pool` |
| `pkg/provider/aws` *(planned)* | `AWSProvider` — EC2 Spot instance adapter with 2-min interrupt handling | `pkg/pool` |
| `pkg/e2ee` *(planned)* | `E2EEProxy` (ML-KEM-768 + ChaCha20-Poly1305), `GraValVerifier`, `AttestationVerifier` | `pkg/provider/chutes`, `cmd/e2ee-proxy` |
| `pkg/burst` *(planned)* | `BurstController` state machine, `CostRouter`, hysteresis logic | `pkg/pool`, `cmd/burst-controller` |
| `pkg/proxy` *(planned)* | CUDA API interceptor (`VirtualGPU`), `CUDAMemoryManager`, virtual device creation | `pkg/pool`, `internal/gpu` |
| `pkg/local` *(planned)* | `LocalGPURegistrar` — nvidia-smi discovery, TCO-based effective cost | `pkg/pool` |

### 4.2 New internal/ Packages (Phase 8B, planned)

| Package | Purpose |
|---------|---------|
| `internal/gpu` *(planned)* | GPU tier management: local NVML probes, provider registration, health monitor wiring |
| `internal/costbroker` *(planned)* | `ComputeBroker` — real-time provider scoring (price 30%, availability 30%, latency 20%, throughput 20%) |

### 4.3 New cmd/ Binaries (Phase 8B, planned)

| Binary | Role |
|--------|------|
| `cmd/gpu-pool-manager` *(planned)* | gRPC + HTTP front-end for pool operations |
| `cmd/burst-controller` *(planned)* | Monitors utilization; drives MONITOR → SPILL → RECOVER cycle |
| `cmd/e2ee-proxy` *(planned)* | Transparent ML-KEM-768 proxy for outbound remote-GPU traffic |

### 4.4 Existing Packages Extended

| Existing Package | Extension |
|-----------------|-----------|
| `pkg/gpu` | Add `ProviderAdapter` registration hooks |
| `pkg/metrics` | Add GPU-tier utilization, cost-per-hour, provider health dashboards |
| `pkg/scheduler` | Add `GPUTier`-aware filter predicate |
| `pkg/security` | Integrate `GraValVerifier` into provider admission |
| `internal/node` | Register virtual GPU devices into cluster resource model |

---

## 5. Integration Points with Phase 8

### 5.1 Forward vs Reverse Data Flows

```
Phase 8 (Forward):   HelixCluster GPU Hardware
                        │ exposes capacity
                        ▼
                     Chutes Subnet 64 (Bittensor)
                        │ earns TAO rewards
                        ▼
                     MinerController / RewardDistributor

Phase 8B (Reverse):  Chutes AI API / io.net / RunPod / AWS
                        │ provides capacity via ProviderAdapter
                        ▼
                     GPUPoolManager (pkg/pool)
                        │ unified scheduling
                        ▼
                     HelixCluster Workloads
```

### 5.2 Shared Security Components

| Component | Phase 8 Use | Phase 8B Use |
|-----------|------------|--------------|
| `GraValVerifier` | Verify HelixCluster node claims to Chutes validators | Verify incoming remote GPU before admission to pool |
| `E2EEProxy` | Encrypt miner traffic to Chutes validators | Encrypt all outbound workload data to remote GPU tiers |
| `AttestationVerifier` | Attest own TDX instance to subnet | Verify remote provider TDX quotes before routing sensitive workloads |

### 5.3 ComputeBroker Routing Bridge

The Phase 8 `UnifiedMarketplaceManager` (multi-marketplace scoring) feeds real-time
price and availability signals directly into the Phase 8B `ComputeBroker`. When
HelixCluster is simultaneously a miner (P8) and a buyer (P8B), the `ComputeBroker`
applies a 10% discount to providers where idle capacity already exists, preventing
double-spend on compute HelixCluster already owns.

---

## 6. Priority Ordering

### P0 — Critical Path (Reverse Integration Cannot Function Without These)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/pool` — `GPUProvider` interface + `PoolManager` | 2 weeks | All providers plug into this; nothing else works without it |
| 2 | `pkg/provider/chutes` — `ChutesProvider` | 2 weeks | Primary burst target; first proven remote path |
| 3 | `pkg/e2ee` — `E2EEProxy` + ML-KEM-768 | 2 weeks | All remote traffic must be quantum-safe by design |
| 4 | `pkg/local` — `LocalGPURegistrar` | 1 week | Pool must know local tier before remote spillover triggers |
| 5 | `pkg/burst` — `BurstController` state machine | 2 weeks | Automatic spillover is the core value proposition |

### P1 — Essential for Phase 8B MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 6 | `pkg/provider/ionet` — Ray cluster adapter | 2 weeks | Multi-GPU training bursts; second cheapest tier |
| 7 | `pkg/provider/runpod` — serverless adapter + warm pool | 1 week | <15 s cold-start for inference overflow |
| 8 | `pkg/proxy` — CUDA interceptor + virtual `/dev/nvidia*` | 3 weeks | Raw CUDA workloads on remote GPUs (beyond inference APIs) |
| 9 | Helm chart + Prometheus/Grafana dashboards | 1 week | Operator visibility into tier utilization and cost |
| 10 | `pkg/e2ee` — `GraValVerifier` provider admission gate | 1 week | Eliminates fake-GPU fraud before admission |

### P2 — Important but Deferrable

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 11 | `pkg/provider/aws` — EC2 Spot + interrupt handling | 2 weeks | Compliance / geographic requirements; expensive; Chutes covers most burst |
| 12 | Intel TDX attestation enforcement for sensitive labels | 2 weeks | Needed for confidential workloads; harmless to defer |
| 13 | Key rotation via CronJob + HSM integration | 1 week | Operational hygiene; manual rotation acceptable initially |
| 14 | Chaos engineering suite + 7-day soak | 1 week | 99.9% SLA target; can run after functional hardening |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Chutes API rate limits under burst load | Medium | High | Implement fallback chain: Chutes → io.net → RunPod → AWS; respect HTTP-429 with exponential backoff |
| gRPC proxy overhead (100–500 µs/kernel) makes HPC workloads unusable | High | Medium | Workload classifier routes HPC to local GPUs or CoreWeave InfiniBand; proxy only for inference/training/rendering |
| Remote GPU cold-start violates SLA | Medium | High | Pre-warmed pools (RunPod warm-pool pattern); FlashBoot provisioning; fall back to next tier immediately |
| AWS Spot interruption mid-workload | Medium | Medium | 2-minute preemption warning → CRIU checkpoint to S3; BurstController triggers failover to Chutes/RunPod |
| ML-KEM-768 implementation vulnerability | Low | Critical | Use audited Cloudflare `circl` library only; schedule external security audit at Phase 8B-d |
| Provider price spike negates cost savings | Low | Medium | ComputeBroker re-scores providers every 60 seconds; hard MaxCostPerHour cap per WorkloadRequest |
| Fake-GPU fraud from decentralized providers | Medium | High | GraVal three-phase verification gates admission; label `graval.verified: "true"` required before scheduling |
| CUDA interceptor breaking CUDA version updates | Medium | Medium | Pin CUDA runtime versions in provider manifests; integration test suite covers major CUDA API surface |

---

## 8. Success Criteria / Phase 8B Exit Gates

### Functional KPIs

| KPI | Target | Measurement |
|-----|--------|-------------|
| E2EE handshake latency (ML-KEM-768) | <1 ms | Unit benchmark (Cloudflare `circl`) |
| Inference p99 via Chutes (remote) | <500 ms | Integration test: 1,000 requests |
| Burst trigger time (local >90% → remote alloc) | <5 seconds | Automated load test with synthetic saturation |
| Provider failover time (unhealthy → next tier) | <10 seconds | Chaos test: kill active adapter |
| Cost reduction vs. AWS on-demand (H100-equiv) | 50–90% | Monthly cost report from `CostTracker` |
| GPU pool utilization (local tier) | >75% average | Prometheus `gpu_utilization` metric |
| GraVal verification pass rate | 100% of admitted providers | Admission controller logs |
| E2EE encryption overhead | <3% throughput penalty | Benchmark: with vs. without proxy |
| 7-day soak test request success rate | >99.9% | Continuous integration load test |
| Monthly TCO (100 GPU-equivalent load) | $8,000–15,000 | CostTracker monthly report |

### CLAUDE-1 End-User Usability Gates (per §CLAUDE-1 + §7.1)

The following gates are mandatory before Phase 8B is declared complete.
Tests passing on non-functional features are treated as PASS-bluffs (§7.1 violation).

| Gate | Requirement | Evidence Required |
|------|-------------|-------------------|
| **Real integration (no mock-only)** | Every `ProviderAdapter` must be tested against the real provider endpoint in CI (Chutes sandbox, io.net staging, RunPod test account) | CI log showing live HTTP 200 from real API |
| **End-to-end user test** | A user submits a workload via `helix submit`; local GPUs are artificially saturated; workload automatically routes to Chutes; response is returned to the user with correct output | Recorded demo / screen capture |
| **HelixQA Challenge: BurstChallenge** | Automated HelixQA Challenge saturates local GPU tier, injects provider failure, verifies automatic failover to next tier within SLA; asserts user receives correct inference result throughout | HelixQA Challenge PASS log |
| **Sink-side evidence** | Prometheus dashboard showing GPU tier utilization spike, provider switch event, and cost-per-hour change during burst | Screenshot of Grafana dashboard captured during load test |
| **E2EE usability** | User sending a sensitive workload (labeled `tee: "true"`) receives correct output; traffic capture confirms all payloads are encrypted (no plaintext in network dump) | Wireshark/tcpdump capture + decryption confirmation |

---

## 9. Bridge to Future Work / GA

### Post Phase 8B: Toward GA

| Milestone | Deliverable |
|-----------|-------------|
| **Phase 9: Bidirectional Arbitrage** | Combine Phase 8 (miner revenue) with Phase 8B (buyer savings) into a unified arbitrage controller that dynamically allocates GPUs between earning and spending based on TAO/token price signals |
| **Phase 10: Cluster Federation** | Expose HelixCluster's unified GPU pool as a first-class provider to other HelixCluster deployments; peer clusters consume each other's excess capacity via the same `GPUProvider` interface |
| **Phase 11: Autonomous Cost Optimizer** | ML-based cost prediction model trained on historical provider price data; pre-buy burst capacity before demand spikes; sell idle capacity into decentralized marketplaces automatically |
| **GA Readiness** | External security audit of E2EE implementation; SOC 2 Type II for confidential compute path; 99.95% SLA over 90-day production window; documented runbooks for all BurstController failure modes |

### Architecture Extension Points

The `GPUProvider` interface is the single integration seam. Adding a new provider (e.g.,
CoreWeave, Vast.ai, Spheron) requires only one new `pkg/provider/<name>` implementation
and a Helm values entry — no changes to the Pool Manager, Burst Controller, or scheduler.
The reverse-integration architecture is explicitly designed to expand to N providers
without protocol lock-in.

---

## 10. References

1. `docs/research/phase_08_B/HelixCluster_Phase_8B/HELIXCLUSTER_PHASE8B_REVERSE_ARCHITECTURE.md` — Core architecture (10,000+ words, 102 code blocks)
2. `docs/research/phase_08_B/HelixCluster_Phase_8B/HELIXCLUSTER_PHASE8B_COMPLETE_REPORT.converted.md` — Phase 8B deliverables summary
3. `docs/research/phase_08_B/HelixCluster_Phase_8B/helixcluster_phase8b_sec00.md` — Executive summary and paradigm shift
4. `docs/research/phase_08_B/HelixCluster_Phase_8B/helixcluster_phase8b_sec02.md` — Remote GPU node abstraction and workload suitability matrix
5. `docs/research/phase_08_B/HelixCluster_Phase_8B/helixcluster_phase8b_sec06.md` — Complete implementation and 24-week roadmap
6. `docs/research/phase_08_B/HelixCluster_Phase_8B/helixcluster_phase8_sec00.md` — Phase 8 forward direction (miner/consumer/orchestrator vision)
7. `docs/research/PHASE_2_ROADMAP.md` — Reference roadmap format
8. `pkg/pool/`, `pkg/provider/`, `pkg/e2ee/`, `pkg/burst/`, `pkg/proxy/`, `pkg/local/` — Phase 8B target packages (planned)
9. `internal/gpu/`, `internal/costbroker/` — Phase 8B internal packages (planned)
10. `cmd/gpu-pool-manager/`, `cmd/burst-controller/`, `cmd/e2ee-proxy/` — Phase 8B binaries (planned)
