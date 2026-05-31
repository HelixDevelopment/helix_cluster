# Helix Cluster OS — Phase 5 Roadmap: Advanced & Exotic Device Ecosystem

> **Research Document** | Phase 5 Planning | 2026-05-31
>
> This document defines Phase 5, which extends HelixCluster from a PC/console/edge/mobile cluster into a **universal compute fabric** spanning 64 device types across 15 tiers and 7 device categories — from a $15 FPGA to a $2.3 million wafer-scale engine.

---

## 1. Current State Summary

### Phases 0–4 Completed

| Phase | Name | Status | Key Deliverables |
|-------|------|--------|------------------|
| **0** | Foundation | Complete | 29 submodules, CI/CD, 20-service docker-compose, 26 pkg stubs, buf proto pipeline |
| **1** | Core Infrastructure | Complete | Container orchestration (`pkg/infra`), VM testing, CLI skeleton (`helix_infra`) |
| **2** | Console Nodes & Distributed Foundations | Complete | PS4/PS5 agents, SWIM gossip (`pkg/swim`), WireGuard mesh (`pkg/wireguard`), Omega scheduler stub |
| **3** | Edge & Mobile Devices | Complete | Orange Pi/RPi/SBC agents, Android agent, iOS research, etcd persistence, SPIFFE mTLS |
| **4** | Virtual & Testing Matrix | Complete | K3s integration, VM-based heterogeneous test matrix, WireGuard mesh hardening, CI/CD expansion |

### Phase 5 Research Artifacts

| Document | Location | Contents |
|----------|----------|----------|
| `HELIXCLUSTER_PHASE5_COMPLETE_REPORT.md` | `docs/research/phase_05/HelixCluster_Phase_05/` | 64-device full report, 7 chapters + roadmap |
| `HELIXCLUSTER_PHASE5_ADVANCED_DEVICES_ARCHITECTURE.md` | `docs/research/phase_05/HelixCluster_Phase_05/` | 15-tier taxonomy, trust model, discovery engine |
| `plan_phase5.md` | `docs/research/phase_05/HelixCluster_Phase_05/` | 7-dimension research stream plan |
| `helixcluster_phase5_sec01–09.md` | `docs/research/phase_05/HelixCluster_Phase_05/` | Per-chapter deep dives (handhelds → exotic → roadmap) |

---

## 2. Phase 5 Scope & Goals

### Primary Objective

Integrate all remaining Linux-capable compute categories not covered by Phases 1–4. Deliver a **universal integration layer** with automatic device discovery, tier/trust assignment, and workload routing across 64 device types and 8 CPU architectures.

### Seven Device Category Pillars

| Pillar | Device Examples | Tier(s) | Key Value Proposition |
|--------|----------------|---------|----------------------|
| **1. Gaming & Handheld Compute** | Steam Deck (1.6 TFLOPS), ROG Ally (8.6 TFLOPS), GPD Win 4 (11.88 TFLOPS), Nintendo Switch CFW | T9 HANDHELD | 4M+ Steam Deck units; Volunteer GPU Tier at ~$0.17/GFLOPS; zero-modification SteamOS |
| **2. Advanced ARM SBCs & Developer Boards** | Jetson Orin Nano Super (67 TOPS), RK3588 boards (ROCK 5B, NanoPi R6S), Turing RK1, BeagleBone AI-64 | T2–T5 | Best AI inference/$: $3.72/TOPS; RK3588 dual 2.5GbE gateway; up to 275 TOPS per node |
| **3. RISC-V & Emerging Architectures** | Milk-V Pioneer (64-core SG2042), Milk-V Jupiter (RVV 1.0), VisionFive 2, SiFive HiFive, LoongArch 3A6000, OpenPOWER Talos II | T10 RISC_V_EXPERIMENTAL | Architecture lock-in insurance; Go/Docker parity on riscv64 achieved |
| **4. FPGA & Programmable Logic** | DE10-Nano ($190, 110K LE), Colorlight 5A-75B ($15 soft-core Linux), Zynq UltraScale+ MPSoC, KV260 DPU | T11–T12 | $15 cluster entry point; reconfigurable accelerators; open toolchain (Yosys, LiteX) |
| **5. Enterprise, Server & Cloud Nodes** | Used AMD EPYC 7742 ($2.10/core), Ampere Altra Q80-30 (80-core ARM), AWS Graviton4 spot ($0.007/vCPU/hr), Minisforum MS-01 | T1–T3, T13 | Density: 64-core EPYC under $1,100; cloud spot burst with WireGuard mesh |
| **6. IoT, Smart Home & Edge** | GL.iNet MT6000 router ($159, Docker, dual 2.5GbE), Synology DS923+ NAS, QNAP TS-464, LG webOS Smart TV | T6–T8 | $159 full edge node; always-on storage backbone; TV CPU donors during idle |
| **7. Exotic & Future Technologies** | Groq LPU (300–500 tok/s, <100ms TTFT), Cerebras CS-3 (125 PF FP16), IBM Quantum (Qiskit async), neuromorphic (research) | T14 EXOTIC_ACCEL | Sub-100ms LLM inference; ultra-large model backends; quantum circuit research plugin |

### 15-Tier Classification System (New in Phase 5)

```
T1  CORE_TRUSTED       — Control plane, databases (x86/OpenPOWER, open firmware)
T2  SEMI_TRUSTED       — Containerized workloads (ARM SBCs, Jetson, EPYC servers)
T3  EDGE_COMPUTE       — Field compute nodes (Khadas Edge2, mini PCs)
T4  AI_WORKER          — ML inference, GPU/NPU accelerated (Jetson Orin family)
T5  AI_CONTROLLER      — High-throughput inference head (Jetson AGX Orin, Thor)
T6  NETWORK_GATEWAY    — Routers, ingress controllers (GL.iNet MT6000, NanoPi R6S)
T7  STORAGE_NODE       — Distributed storage (Synology, FriendlyELEC CM3588 NAS)
T8  BUDGET             — Cost-sensitive lightweight nodes (Odroid M1S, N2+)
T9  HANDHELD           — Volunteer gaming handhelds (Steam Deck, ROG Ally)
T10 RISC_V_EXPERIMENTAL— Emerging arch nodes (Milk-V, VisionFive 2)
T11 FPGA_SOFT_CORE     — Soft-core CPU on FPGA (Colorlight + VexRiscv)
T12 FPGA_HARD_ACCEL    — FPGA with hard ARM + accelerators (DE10-Nano, KV260)
T13 CLOUD_BURST        — Spot/preemptible cloud instances (AWS Graviton, Azure)
T14 EXOTIC_ACCEL       — Quantum, neuromorphic, photonic (research-only)
T15 LEGACY_RETIRED     — EOL/no Linux path (Xbox Series X, Apple Watch)
```

---

## 3. Sub-Phases

| Sub-Phase | Name | Weeks | Tasks | Goal |
|-----------|------|-------|-------|------|
| **5a** | Gaming & SBC Integration | 1–6 | 28 | Steam Deck Flatpak agent; RK3588 APT packages; Jetson TensorRT backend; power-aware scheduler; 10-node mixed cluster |
| **5b** | RISC-V & FPGA | 7–12 | 24 | riscv64 native agent binaries; Milk-V Pioneer build farm; Zynq hard-core packages; KV260 DPU; 8-node arm64+riscv64+FPGA cluster |
| **5c** | Enterprise & IoT | 13–18 | 24 | EPYC auto-provisioning; Ampere Altra packages; cloud spot WireGuard + preemption handler; MT6000 OpenWrt agent; NAS storage nodes; hybrid 5+5 cluster |
| **5d** | Exotic Technology | 19–24 | 24 | Groq LPU API (<100ms TTFT); Cerebras CS-3 backend; Qiskit quantum plugin; webOS smart TV agent; gVisor/Kata full security hardening; 60+ device GA |

**Total: 24 weeks | 100 tasks | ~400 person-hours**

---

## 4. Package Breakdown

### 4.1 New pkg/ Packages (Phase 5, planned)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `pkg/device` | Universal device capability descriptor: tier, trust, compute class, arch probing | Scheduler matchmaking, `internal/node` |
| `pkg/handheld` | Steam Deck / x86 handheld agent: Vulkan compute detection, power-aware scheduling, battery/thermal monitoring | `pkg/device`, `pkg/scheduler` |
| `pkg/fpga` | FPGA resource abstraction: hard-processor SoC, soft-core RISC-V, DPU accelerator backends | `pkg/device`, `internal/node` |
| `pkg/riscv` | RISC-V agent build pipeline: riscv64 cross-compilation helpers, RVV 1.0 detection, board probing | `pkg/device`, CI pipeline |
| `pkg/cloudspot` | Cloud spot instance handler: WireGuard join/leave, preemption signal handler, workload checkpoint trigger | `pkg/wireguard`, `pkg/scheduler` |
| `pkg/inference` | Provider-agnostic LLM inference backend: Groq LPU, Cerebras CS-3, Jetson TensorRT, local llama.cpp | `internal/llm`, `pkg/scheduler` |
| `pkg/quantum` | Qiskit Runtime quantum circuit plugin: async job submission, result polling, research-tier isolation | `pkg/device` (T14 only) |

All Phase 5 `pkg/` entries are **(planned)** — no existing implementations found in `pkg/`.

### 4.2 New internal/ Packages (Phase 5, planned)

| Package | Purpose | Integration Point |
|---------|---------|-------------------|
| `internal/handheld` | Steam Deck / ROG Ally / GPD Win device adapters; Flatpak agent packaging | `pkg/handheld` |
| `internal/sbc` | Advanced ARM SBC adapters: RK3588 NPU, Jetson TensorRT, Turing RK1 module | `pkg/device`, `internal/node` |
| `internal/fpga` | FPGA hard-core + soft-core adapters; DPU fabric resource reporting | `pkg/fpga` |
| `internal/enterprise` | EPYC/Ampere Altra auto-provisioning; MS-01 mini PC; Coreboot trust upgrade | `pkg/device`, `internal/node` |
| `internal/iot` | OpenWrt router agent; NAS Container Manager; webOS JS agent bridge | `pkg/device`, `internal/node` |
| `internal/exotic` | Groq/Cerebras API clients; quantum plugin; neuromorphic stub | `pkg/inference`, `pkg/quantum` |

### 4.3 Existing pkg/ Packages Extended

| Existing Package | Phase 5 Extension |
|-----------------|-------------------|
| `pkg/scheduler` | Tier-aware matchmaking using capability descriptors; power-aware policy for T9 handhelds |
| `pkg/wireguard` | Cloud spot dynamic mesh join/leave; preemption-safe tunnel teardown |
| `pkg/resources` | Multi-arch resource probing: NPU TOPS, FPGA logic elements, GPU TFLOPS, quantum QPU |
| `pkg/discovery` | 15-tier device registry; trust-level-aware service routing |

---

## 5. Integration Points with Phase 4

### 5.1 K3s + WireGuard → Universal Mesh

```
Phase 4: K3s control plane + hardened WireGuard mesh
           │
Phase 5a:  Steam Deck Flatpak agent joins same WireGuard mesh
           RK3588 / Jetson register as K3s worker nodes
           │
Phase 5b:  riscv64 binaries compile natively; FPGA ARM cores join mesh
           Multi-arch manifest container registry (arm64 + riscv64 + x86)
           │
Phase 5c:  Cloud spot instances WireGuard into on-prem cluster
           OpenWrt router becomes ingress gateway node
           │
Phase 5d:  Groq/Cerebras API backends route from same scheduler interface
           gVisor/Kata sandboxing applied per tier automatically
```

### 5.2 Tier System Overlay on Phase 4 Nodes

| Phase 4 Node Type | Phase 5 Tier Assignment |
|-------------------|------------------------|
| x86 desktop/laptop | T1 CORE_TRUSTED |
| PS4/PS5 console | T2 SEMI_TRUSTED |
| Orange Pi / RPi SBC | T3 EDGE_COMPUTE |
| Android/iOS device | T8 BUDGET or T15 LEGACY |

### 5.3 Proto APIs Extended

| Proto | Phase 5 Addition |
|-------|-----------------|
| `node.proto` | `tier`, `trust_level`, `compute_class`, `arch` fields |
| `scheduler.proto` | Tier-aware constraints; `power_budget` field for handhelds |
| `resources.proto` | `npu_tops`, `fpga_logic_elements`, `tflops_gpu` fields |

---

## 6. Priority Ordering

### P0 — Critical Path (Universal Integration Layer)

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 1 | `pkg/device` capability descriptor + auto-probe engine | 2 weeks | Foundation for all tier/trust assignment; prerequisite for all other packages |
| 2 | Steam Deck Flatpak agent (5a Week 1) | 1 week | Highest-impact handheld; 4M units; zero-mod Linux; Volunteer GPU Tier anchor |
| 3 | RK3588 / Jetson TensorRT integration (5a Weeks 3–4) | 2 weeks | Premier AI edge nodes; T4 AI_WORKER tier foundation |
| 4 | Power-aware scheduler extension (5a Week 5) | 1 week | Required to prevent gaming interference on volunteer handhelds |
| 5 | riscv64 cross-compilation CI pipeline (5b Week 7) | 1 week | Architecture portability; all RISC-V nodes blocked on this |

### P1 — Essential for Phase 5 MVP

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 6 | FPGA hard-core adapter: DE10-Nano + Zynq (5b Weeks 10–11) | 2 weeks | T12 FPGA_HARD_ACCEL tier; DPU accelerator backend |
| 7 | EPYC auto-provisioning script (5c Week 13) | 1 week | T1 production cluster density; <30 min onboarding |
| 8 | `pkg/cloudspot` preemption handler (5c Week 15) | 1 week | Hybrid cloud bursting; spot drain + checkpoint |
| 9 | GL.iNet OpenWrt router agent (5c Week 16) | 1 week | T6 NETWORK_GATEWAY always-on backbone |
| 10 | `pkg/inference` Groq + Jetson backends (5d Week 19) | 2 weeks | Provider-agnostic LLM inference tier |

### P2 — Important but Deferrable

| # | Task | Effort | Reason |
|---|------|--------|--------|
| 11 | FPGA soft-core: Colorlight + VexRiscv (5b, T11) | 1 week | $15 entry point; experimental; low production urgency |
| 12 | `pkg/quantum` Qiskit plugin (5d Week 21) | 1 week | Research-only T14; no production workloads before 2029 |
| 13 | webOS smart TV agent (5d Week 22) | 1 week | Novelty value; JS agent over WebSocket; narrow use case |
| 14 | Cerebras CS-3 cloud API (5d Week 20) | 1 week | $2–3M hardware; access via cloud API; deferred to demand |
| 15 | Nintendo Switch 2 homebrew research | 1 week | 6–18 month homebrew timeline; Watch List item |

---

## 7. Risk Analysis

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| NVIDIA acquisition of Groq IP creates vendor lock-in | High | Medium | Provider-agnostic `pkg/inference` interface; Cerebras, SambaNova, local Jetson as fallbacks |
| Steam Deck agent adoption requires volunteer opt-in | High | Medium | Background CPU-only default; GPU only when docked+charging; one-click pause; published benchmarks |
| RISC-V performance 10x below ARM (Milk-V Pioneer) | High | Low | Cap RISC-V to build jobs and lightweight edge; never route latency-sensitive inference; monitor RVA23-profile for 2027 |
| Cloud spot preemption corrupts stateful workloads | Medium | High | 2-minute AWS warning caught by preemption handler; drain + CRIU checkpoint before termination |
| FPGA open-source toolchain (Yosys/LiteX) maturity gaps | Medium | Medium | Target hard-processor SoC first (DE10-Nano); soft-core Colorlight in P2 only |
| Nintendo Switch CFW/Switch 2 homebrew timeline slips | Medium | Low | Both on Watch List; no P0/P1 dependency; Steam Deck covers handheld tier |
| gVisor/Kata overhead on T9 UNTRUSTED handhelds | Low | Medium | Benchmark overhead per workload class; CPU-only tasks bypass sandbox in controlled profiles |
| Legal constraints on jailbroken console re-use (Xbox) | Low | High | Xbox formally excluded (T15); only open or owner-consented devices targeted |

---

## 8. Success Criteria (Phase 5 Exit Gates)

| KPI | Target | Measurement |
|-----|--------|-------------|
| Device type coverage | 60+ devices with defined integration paths | Integration test matrix |
| Tier/trust assignment accuracy | Auto-probe assigns correct tier for 95%+ of tested devices | Unit + integration tests against device fixtures |
| Steam Deck agent gaming interference | Zero FPS impact during gaming; >80% battery preserved | Real-device benchmark (not mock) |
| Jetson TensorRT throughput | ≥60 TOPS reported by agent (Orin Nano Super) | Real L4T node; sink-side metrics |
| RISC-V agent build | Native riscv64 binary compiles in CI without emulation | CI pipeline artifact |
| FPGA DPU workload | YOLO inference offloaded at ≥0.9 TOPS on KV260 | Real board test; Vitis AI backend |
| EPYC auto-provisioning | Bare node joins cluster as CORE_TRUSTED in <30 min | Automated provisioning script on real hardware |
| Cloud spot hybrid | 5 on-prem + 5 spot nodes; graceful failover under simulated preemption | Integration test with real AWS/GCP spot |
| Groq LPU inference | <100ms TTFT on Llama 3.1 70B | Real GroqCloud API call; latency measured at client |
| Mixed-arch cluster stability | arm64 + riscv64 + FPGA in single WireGuard mesh for 24h | Integration test (8-node heterogeneous) |
| Test coverage (new pkg/) | >60% line coverage on all Phase 5 packages | Codecov |
| **CLAUDE-1 End-User Usability Gate** | Every device category has: (a) unit tests with mutation, (b) integration tests against real hardware or hardware-equivalent fixture — no mock-only validation for claimed real-world operation, (c) end-to-end test exercising the feature as an operator would, (d) HelixQA Challenge passing on non-broken implementation, (e) sink-side evidence (log, metrics screenshot, or benchmark output) proving end-user-visible operation | HelixQA Challenge suite + manual sink-side evidence capture |

---

## 9. Bridge to Phase 6

### Phase 6: Multi-Cluster Federation

Phase 5 ends with a 64-device, 8-architecture, single-cluster fabric managed under one control plane. Phase 6 extends this into **multi-cluster federation** — multiple independent HelixCluster instances interconnecting into a federated compute mesh.

| Phase 6 Sub-Phase | Builds On Phase 5 Deliverable |
|-------------------|-------------------------------|
| 6.1 Federation Gossip Overlay | Phase 5 WireGuard mesh + `pkg/swim`; million-node gossip for volunteer GPU tier |
| 6.2 Cross-Cluster Scheduler | Phase 5 tier-aware matchmaking extended to route across cluster boundaries |
| 6.3 Distributed AI Brain Layer | Phase 5 `pkg/inference` (Groq/Cerebras/Jetson) as inference backbone; RAG cache on edge nodes |
| 6.4 Autonomous Procurement | Phase 5 `pkg/cloudspot` extended to auto-scale spot instances by real-time price-performance |
| 6.5 Identity Federation | Phase 5 trust model (TRUSTED→RESEARCH) federated across cluster boundaries via SPIFFE/SPIRE |

**Handoff condition:** Phase 6 begins only after Phase 5 exit gates are met — specifically, the CLAUDE-1 usability gate must confirm that every tier's representative device works end-to-end for an operator, with sink-side evidence, before federation complexity is layered on top.

---

## 10. References

1. `docs/research/phase_05/HelixCluster_Phase_05/HELIXCLUSTER_PHASE5_COMPLETE_REPORT.md` — Full Phase 5 report (64 devices, 7 chapters)
2. `docs/research/phase_05/HelixCluster_Phase_05/HELIXCLUSTER_PHASE5_ADVANCED_DEVICES_ARCHITECTURE.md` — 15-tier architecture specification
3. `docs/research/phase_05/HelixCluster_Phase_05/plan_phase5.md` — 7-dimension research stream plan
4. `docs/research/phase_05/HelixCluster_Phase_05/helixcluster_phase5_sec01–09.md` — Per-chapter section deep dives
5. `docs/research/PHASE_2_ROADMAP.md` — Reference template for roadmap format
6. `pkg/swim/`, `pkg/wireguard/`, `pkg/discovery/`, `pkg/scheduler/`, `pkg/resources/` — Phase 2–4 foundational packages (real, in `pkg/`)
7. `internal/gpu/`, `internal/node/`, `internal/llm/` — Phase 3–4 internal packages (real, in `internal/`)
8. `api/v1/*.proto` — API definitions extended in Phase 5
