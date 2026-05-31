# Helix Cluster OS — Phase 8 Roadmap: Chutes AI Integration

> **Research Document** | Phase 8 Planning | 2026-05-31
>
> This document defines the bridge from Phase 7 (Industry Benchmarking & Hardening) into Phase 8, integrating HelixCluster GPU nodes with Chutes.ai — the world's most secure decentralized AI inference platform — enabling dual-revenue compute operation as Bittensor Subnet 64 miners while consuming Chutes inference for internal HelixCluster workloads.

---

## 1. Current State Summary

### Phases 0–7 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | Complete | 29 submodules, CI/CD scaffolding, 20-service docker-compose, 26 pkg stubs, go.work, buf proto pipeline |
| **1** | Core Infrastructure | Complete | Container orchestration (`pkg/infra`), VM testing framework, CLI skeleton (`helix_infra`) |
| **2** | Console Nodes & Distributed Foundations | Complete | SWIM gossip (`pkg/swim`), WireGuard mesh (`pkg/wireguard`), PS4/PS5 agents (`internal/console`) |
| **3** | Edge & Mobile Devices | Complete | etcd integration, full Omega scheduler + ClassAds, mTLS/SPIFFE, ARM64/Android/iOS agents |
| **4** | Virtual Testing Matrix | Complete | Deterministic VM snapshots, chaos injection, HelixQA challenge framework, coverage gates |
| **5** | Advanced & Exotic Devices | Complete | RISC-V, embedded systems, exotic compute substrate adapters |
| **6** | Multi-Cluster Federation | Complete | Cross-cluster gossip, federated scheduling, global resource aggregation |
| **7** | Industry Benchmarking & Hardening | Complete | GPU compute benchmarks, security hardening, production SLO validation |

### Phase 8 Research Artifacts

| Document | Location | Contents |
|----------|----------|----------|
| `plan_phase8.md` | `phase_08/HelixCluster_Phase_08/` | Chutes.ai integration objective and research approach |
| `helixcluster_phase8_sec00.md` | `phase_08/HelixCluster_Phase_08/` | Executive summary — 100B tokens/day, 42 repos, 6 Go services |
| `helixcluster_phase8_sec01.md` | `phase_08/HelixCluster_Phase_08/` | Chutes.ai platform deep-dive: SDK, miner, validator, GraVal, E2EE |
| `helixcluster_phase8_sec03.md` | `phase_08/HelixCluster_Phase_08/` | Bittensor integration: Yuma Consensus, SN64 scoring, TAO tokenomics |
| `helixcluster_phase8_sec06.md` | `phase_08/HelixCluster_Phase_08/` | Integration architecture: 6 Go services, 3 Helm charts, 4 Bash scripts |
| `helixcluster_phase8_sec07.md` | `phase_08/HelixCluster_Phase_08/` | Emerging technologies and 24-week implementation roadmap |
| `HELIXCLUSTER_PHASE8_CHUTES_INTEGRATION_ARCHITECTURE.md` | `phase_08/HelixCluster_Phase_08/` | Complete integration architecture document |
| `HELIXCLUSTER_PHASE8_COMPLETE_REPORT.md` | `phase_08/HelixCluster_Phase_08/` | Full Phase 8 deliverables report |

---

## 2. Phase 8 Scope & Goals

### Primary Objective

Transform HelixCluster GPU nodes into **dual-revenue compute providers** that simultaneously earn HLX rewards from HelixCluster proof-of-work and TAO rewards from Chutes.ai inference serving on Bittensor Subnet 64, while consuming Chutes.ai's decentralized inference network as the AI inference backend for HelixCluster-internal workloads.

### Four Pillars

| Pillar | Description | Outcome |
|--------|-------------|---------|
| **Miner Integration** | Deploy `chutes-miner` on HelixCluster K3s GPU nodes via `MinerController` Go controller; run Gepetto strategy with dual-resource arbitration | Nodes earn TAO (1.7–17/day per H100) alongside HLX rewards |
| **AI Inference Layer** | HelixCluster apps consume Chutes.ai OpenAI-compatible API (vLLM/SGLang) via E2EE-protected Go client; ML-KEM-768 post-quantum encryption | 100% E2EE for sensitive workloads; 85% cost savings vs. centralized clouds |
| **Multi-Marketplace Manager** | Unified adapter layer routes GPU workloads across Chutes (SN64/TAO), io.net (Solana/IO), Akash (Cosmos/AKT), and Salad (fiat) | 30%+ revenue uplift; automated marketplace arbitrage |
| **Confidential Computing** | Intel TDX + NVIDIA CC TEE stack (`sek8s`), GraVal GPU attestation, hybrid PQC TLS (X25519 + ML-KEM-768) | Hardware-rooted trust; EU AI Act compliance pipeline |

### What Chutes AI Integration Means

Chutes.ai (Rayon Labs, January 2025) is a serverless AI compute platform running on **Bittensor Subnet 64**, processing 100 billion tokens/day through 8,000+ GPU nodes worldwide. It provides:
- A GPU marketplace where miners earn TAO proportional to compute contribution (55% compute units, 25% invocations, 15% unique chute diversity, 5% bounties)
- Post-quantum E2EE inference (ML-KEM-768 + ChaCha20-Poly1305) with Intel TDX hardware attestation
- An OpenAI-compatible REST API backed by vLLM, SGLang, and TurboDiffusion serving engines (~85% cheaper than AWS H100)
- GraVal: Proof of Consecutive VRAM Work — CUDA-based GPU hardware verification preventing fake hardware claims

Phase 8 is the **forward integration**: HelixCluster joins Chutes.ai as miner, consumer, and orchestrator. Phase 8B (next) covers reverse integration (exposing HelixCluster compute to Chutes as a custom subnet).

---

## 3. Sub-Phases

| Sub-Phase | Name | Weeks | Key Tasks | Goal |
|-----------|------|-------|-----------|------|
| **8a** | Chutes Miner Integration | 1–6 | `MinerController` Go controller; GraVal bootstrap DaemonSet; custom HelixGepetto strategy; Bittensor wallet registration; PostgreSQL + Redis miner stack; node onboarding scripts | 100+ HelixCluster nodes active on SN64; dual HLX + TAO revenue |
| **8b** | AI Inference Layer | 7–12 | Chutes API Go client with streaming; E2EE proxy (ML-KEM-768); model router (latency/throughput/quality/cost); AWQ 4-bit model serving; vLLM + SGLang Helm deployment; fallback chain logic | 1B+ tokens/day through HelixCluster nodes; E2EE on all sensitive inference |
| **8c** | Multi-Marketplace Expansion | 13–18 | `MarketplaceAdapter` interface; io.net / Akash / Salad adapters; LP revenue optimizer; composite scoring (price 30%, availability 30%, latency 20%, throughput 20%); TEE 1.5x routing multiplier | 2+ marketplaces per GPU; 30% revenue uplift vs. single marketplace |
| **8d** | Security & TEE Hardening | 19–24 | Intel TDX + NVIDIA CC via `sek8s`; hybrid PQC TLS node-to-node; LUKS encrypted root; Cosign image admission; EU AI Act compliance pipeline; carbon-aware scheduler; export control tier verification | TEE-attested inference on pilot H100 fleet; auto-generated compliance docs |

**Total: 24 weeks | ~180 tasks | ~720 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 8, planned)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/chutes` (planned) | `MinerController`, `GraValVerifier`, `E2EEProxy`, Chutes API `Client`, `ChutesMinerConfig` | `internal/gpu`, `internal/llm`, K3s client |
| `pkg/marketplace` (planned) | `UnifiedManager`, `MarketplaceAdapter` interface, Chutes/io.net/Akash/Salad adapters, `RevenueOptimizer` | `pkg/chutes`, `pkg/scheduler`, `internal/gpu` |
| `pkg/e2ee` (planned) | ML-KEM-768 + ChaCha20-Poly1305 proxy; HKDF-SHA256 key derivation; gzip compression pipeline | `pkg/chutes`, `internal/gateway` |
| `pkg/bittensor` (planned) | Bittensor wallet (coldkey/hotkey), TAO balance, subnet registration, Yuma Consensus weight queries | `pkg/chutes`, `pkg/marketplace` |

### 4.2 New internal/ Packages (Phase 8, planned)

| Package | Purpose |
|---------|---------|
| `internal/llm` (planned) | LLM inference routing: model selector, streaming SSE handler, E2EE-transparent client wrapper |
| `internal/gpu` (existing, extended) | GraVal attestation hooks; MIG profile management; dual-workload capacity split (Helix PoW vs. Chutes inference) |

### 4.3 Helm Charts & Automation (planned)

| Artifact | Purpose |
|----------|---------|
| `helm/helixcluster-chutes/` (planned) | Unified miner + inference Helm chart; `values.yaml` for validators, GraVal, Gepetto, inference engines, TEE, monitoring |
| `helm/helixcluster-chutes/values-models.yaml` (planned) | 8 pre-configured model deployments: Llama-3.2-1B, DeepSeek-V3, Llama-3.1-405B, FLUX.1-schnell/dev, BGE-large, Whisper-large-v3, Qwen2.5-7B |
| `scripts/chutes-node-prep.sh` (planned) | Bare-metal GPU node preparation (drivers, K3s, NVIDIA operator) |
| `scripts/chutes-miner-deploy.sh` (planned) | Automated miner stack deployment and validator registration |
| `scripts/chutes-health-monitor.sh` (planned) | Ongoing miner health checks and GraVal re-attestation |
| `scripts/chutes-verify.sh` (planned) | GPU identity verification and SN64 connectivity test |

---

## 5. Integration Points with Phase 7

### 5.1 GPU Benchmarking → Miner Profiling

```
Phase 7 GPU benchmarks ──► pkg/chutes MinerController
internal/gpu benchmark data  │  GPU performance coefficients
                              │  feed into Gepetto cost-weighted
                              └── chute selection strategy
```

### 5.2 Security Hardening → TEE Stack

```
Phase 7 mTLS/SPIFFE (internal/security) ──► pkg/e2ee ML-KEM-768 overlay
Phase 7 WireGuard mesh (pkg/wireguard)       E2EE proxy wraps existing
                                             authenticated connections
```

### 5.3 Existing Infrastructure Reuse

| Phase 7 Artifact | Phase 8 Role |
|------------------|-------------|
| K3s Kubernetes (GPU nodes) | Chutes miner stack runs as K3s workloads alongside Helix PoW |
| `pkg/scheduler` Omega model | HelixGepetto consults Helix load to reserve GPU capacity |
| `internal/gateway` API layer | Routes internal AI requests to Chutes API client |
| `pkg/metrics` + Prometheus | Monitors TAO earnings, GraVal attestation status, token throughput |
| `pkg/health` | Extended with miner-api and GraVal DaemonSet health checks |

---

## 6. Priority Ordering

### P0 — Critical Path (Revenue Generation Gate)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/chutes` MinerController + GraValVerifier | 2 weeks | Miner cannot register or earn without GPU attestation |
| 2 | Bittensor wallet + subnet registration | 1 week | Required before first TAO emission |
| 3 | Custom HelixGepetto strategy | 1 week | Prevents GPU starvation of Helix PoW tasks |
| 4 | `helm/helixcluster-chutes` base chart | 2 weeks | Declarative deployment of entire miner stack |
| 5 | Node prep + deploy automation scripts | 1 week | Scales onboarding beyond manual setup |

### P1 — Essential for Phase 8 MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 6 | Chutes API Go client + streaming | 1 week | HelixCluster apps need inference backend |
| 7 | `pkg/e2ee` ML-KEM-768 proxy | 2 weeks | Required for sensitive workload routing to TEE nodes |
| 8 | `values-models.yaml` model configurations | 1 week | Defines which models HelixCluster nodes serve |
| 9 | `pkg/marketplace` UnifiedManager + Chutes adapter | 2 weeks | Revenue optimization across marketplaces |

### P2 — Important but Deferrable

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 10 | io.net / Akash / Salad marketplace adapters | 2 weeks | Multi-marketplace diversification (Phase 8c) |
| 11 | Intel TDX + NVIDIA CC (`sek8s`) deployment | 2 weeks | TEE hardening, EU AI Act compliance (Phase 8d) |
| 12 | Hybrid PQC TLS (X25519 + ML-KEM-768) node-to-node | 1 week | Quantum-resistant transport (Phase 8d) |
| 13 | Carbon-aware scheduler + EU AI Act compliance pipeline | 2 weeks | Regulatory readiness, Phase 8d |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| GraVal CUDA library binary dependency | High | High | Pin `libgraval-miner.so` version; test on target GPU models before fleet rollout |
| Bittensor TAO price volatility | High | Medium | Revenue projections use conservative $250/TAO baseline; hedge via multi-marketplace (io.net, Akash) |
| GraVal VRAM threshold failure (95%) | Medium | High | Reserve 5% VRAM headroom in deployment configs; test each GPU SKU before registration |
| Gepetto dual-resource conflict | Medium | High | HelixGepetto hard-caps Chutes at 0% when Helix load > 80%; tested via load injection |
| Chutes API rate limiting / outages | Medium | Medium | Fallback model chain (DeepSeek-V3 → Qwen2.5-72B → Llama-3.1-70B) in API client |
| Bittensor subnet registration cost (~600 TAO) | Low (Phase 8) | High | Deferred to Phase 8B (Subnet Creator level); Phase 8 targets Miner + API Consumer only |
| Intel TDX hardware availability | Medium | Medium | TEE hardening in Phase 8d; non-TEE serving remains functional throughout Phases 8a–8c |
| MIG profile reconfiguration downtime | Medium | Low | Pre-configure MIG profiles at node provisioning; use 7g.80gb initially for flexibility |
| K3s bare-metal GPU requirement | Low | High | Document: VMs explicitly unsupported by GraVal; bare-metal or certified cloud GPU instances only |
| Export control compliance (Tier 3 nodes) | Low | High | Country-code KYC at node onboarding; hardware attestation filters embedded in MinerController |

---

## 8. Success Criteria / Phase 8 Exit Gates

| KPI | Target | Measurement |
|-----|--------|-------------|
| HelixCluster nodes active on SN64 | 100+ by week 6; 500+ by month 6 | Bittensor metagraph query |
| Daily tokens processed (via Helix nodes) | 1B+ within 3 months | Chutes validator scoring API |
| Per-H100 GPU monthly revenue | $2,000–$8,000 (TAO + HLX combined) | RewardDistributor Go service |
| GPU utilization improvement | +30–50% vs. Phase 7 baseline | `pkg/metrics` Prometheus dashboard |
| E2EE-protected inferences | 100% for workloads flagged `TEERequired=true` | `pkg/e2ee` proxy audit log |
| GraVal attestation pass rate | >99% on supported GPU SKUs | `GraValVerifier.BatchVerify` results |
| Chutes API client latency (P99) | <500ms first-token for streaming | Integration test suite |
| Test coverage (`pkg/chutes`, `pkg/marketplace`, `pkg/e2ee`) | >60% line coverage | Codecov CI gate |
| Build success rate | >99% | CI pass rate |
| **CLAUDE-1 End-User Usability Gate** | All features provably functional for end users | HelixQA Challenge suite |

### CLAUDE-1 Usability Gate (mandatory, per §CLAUDE-1)

Per the HelixConstitution end-user usability mandate, Phase 8 is **not complete** until:

1. **Real integration — no mock-only:** `pkg/chutes` API client MUST be tested against actual `https://llm.chutes.ai/v1` endpoints (or a Chutes testnet) with a live `cpk_` API key. Miner registration MUST be verified against Bittensor mainnet or testnet subtensor; not simulated.
2. **Sink-side evidence required:** TAO earnings must be confirmed on-chain (Bittensor explorer or `btcli` query); token throughput must be visible in Chutes validator scoring dashboard; GPU attestation status must appear in Prometheus/Grafana. Screenshots or log captures required before declaring completion.
3. **End-to-end test:** A full request flow — HelixCluster workload → Chutes API client → E2EE proxy → Chutes network → GPU miner → response — must execute successfully and be captured in an integration test.
4. **HelixQA Challenges:** Dedicated Challenge scenarios for: (a) miner deployment and TAO earnings; (b) E2EE inference roundtrip; (c) multi-marketplace routing decision; (d) GraVal attestation pass/fail. All Challenges must PASS on real or testnet infrastructure.
5. **No PASS-bluff:** A unit test that mocks the Chutes API and passes is not sufficient evidence that the feature works. Mock-only tests are permitted in `pkg/chutes/` unit suites but cannot substitute for real integration validation.

---

## 9. Bridge to Phase 8B

### Phase 8B: Reverse Integration — HelixCluster as Chutes Subnet Creator

Phase 8B inverts the integration direction: rather than HelixCluster joining Chutes as a miner, HelixCluster **creates and operates its own Bittensor subnet** (Level 4 integration), exposing HelixCluster's heterogeneous compute pool (consoles, edge nodes, GPU servers, mobile agents) to the broader Bittensor ecosystem as a novel AI compute commodity.

| Sub-Phase | Deliverable |
|-----------|-------------|
| 8B.1 | Custom subnet registration (~600 TAO); incentive mechanism design for heterogeneous HelixCluster compute |
| 8B.2 | Subnet validator implementation: scoring heterogeneous node contributions (console GPU, edge ARM, server H100) |
| 8B.3 | HelixCluster-native chute templates: expose Helix workloads as Chutes-compatible serverless AI endpoints via `@chute.cord()` pattern |
| 8B.4 | Child hotkey delegation for multi-subnet validator operations; Taoflow emission optimization |
| 8B.5 | Production subnet launch, miner recruitment, and emission share targeting |

Phase 8 provides the **miner tooling, economic model understanding, and Bittensor integration primitives** that Phase 8B extends into subnet ownership and custom incentive mechanism design.

---

## 10. References

1. `docs/research/phase_08/HelixCluster_Phase_08/plan_phase8.md` — Phase 8 research objective
2. `docs/research/phase_08/HelixCluster_Phase_08/helixcluster_phase8_sec00.md` — Executive summary
3. `docs/research/phase_08/HelixCluster_Phase_08/helixcluster_phase8_sec01.md` — Chutes.ai platform deep-dive
4. `docs/research/phase_08/HelixCluster_Phase_08/helixcluster_phase8_sec03.md` — Bittensor integration and TAO economics
5. `docs/research/phase_08/HelixCluster_Phase_08/helixcluster_phase8_sec06.md` — Integration architecture and Go implementation
6. `docs/research/phase_08/HelixCluster_Phase_08/helixcluster_phase8_sec07.md` — Emerging technologies and 24-week roadmap
7. `docs/research/phase_08/HelixCluster_Phase_08/HELIXCLUSTER_PHASE8_CHUTES_INTEGRATION_ARCHITECTURE.md` — Full architecture document
8. `docs/research/phase_08/HelixCluster_Phase_08/HELIXCLUSTER_PHASE8_COMPLETE_REPORT.md` — Complete Phase 8 report
9. `pkg/chutes/` — MinerController, GraValVerifier, E2EEProxy, API Client (planned)
10. `pkg/marketplace/` — UnifiedManager, MarketplaceAdapter (planned)
11. `pkg/e2ee/` — ML-KEM-768 + ChaCha20-Poly1305 proxy (planned)
12. `pkg/bittensor/` — Wallet, subnet registration, Yuma Consensus queries (planned)
13. `helm/helixcluster-chutes/` — Unified miner + inference Helm chart (planned)
14. Chutes.ai GitHub: `chutesai/chutes`, `chutesai/chutes-miner`, `chutesai/graval`, `chutesai/e2ee-proxy`, `chutesai/sek8s`
15. Bittensor Subnet 64: `5Dt7HZ7Zpw4DppPxFM7Ke3Cm7sDAWhsZXmM5ZAmE7dSVJbcQ` (recommended validator hotkey)
