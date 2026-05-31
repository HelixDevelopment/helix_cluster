## 9. Implementation Roadmap

The preceding eight chapters mapped a landscape of sixty-four device types across seven dimensions --- from Steam Decks to quantum processors, from fifteen-dollar FPGA boards to seventy-thousand-dollar Cerebras racks. The question now is not what is possible, but what comes first, and how each layer of integration enables the next. This chapter provides a twenty-four-week implementation roadmap organized into four sequential phases. Each phase builds upon the deliverables of its predecessor, with explicit dependencies, success criteria, and risk mitigations.

The sequencing is deliberate. Phase 5a prioritizes the highest-impact, lowest-friction integrations: Steam Deck volunteer agents and the RK3588 Jetson board families that already run well-supported Linux. Phase 5b tackles architectural portability through RISC-V cross-compilation and FPGA accelerator agents, both of which depend on the container orchestration and CI pipelines established in 5a. Phase 5c introduces enterprise muscle and always-on edge infrastructure, requiring the heterogeneous scheduling proven in 5a and 5b to route workloads across trust tiers. Phase 5d reaches for exotic technologies --- Groq LPU inference, quantum research plugins --- that sit atop the full stack assembled in the preceding eighteen weeks.

**Table 1: Master Implementation Timeline**

| Phase | Weeks | Key Deliverables | Dependencies | Success Criteria |
|-------|-------|-----------------|--------------|------------------|
| 5a: Gaming & SBC | 1--6 | Steam Deck Flatpak agent; RK3588 APT packages; Jetson TensorRT backend; power-aware scheduler | Phase 4 cluster core (K3s, WireGuard mesh) | 10-node mixed cluster reporting metrics; Steam Deck agent runs without gaming interference |
| 5b: RISC-V & FPGA | 7--12 | riscv64 agent binaries; Milk-V Pioneer build farm; Zynq hard-core packages; KV260 DPU backend | 5a CI pipeline, container registry, cross-arch manifest support | 8-node heterogeneous cluster: arm64 + riscv64 + FPGA in unified mesh |
| 5c: Enterprise & IoT | 13--18 | EPYC automated provisioning; Ampere Altra packages; OpenWrt router agent; NAS storage nodes; cloud spot handler | 5a Jetson agents for AI routing; 5b tier assignment for multi-arch scheduling | 5 on-prem + 5 spot hybrid cloud; MT6000 router agent under 5% CPU overhead |
| 5d: Exotic Technology | 19--24 | GroqCloud API integration; Cerebras CS-3 backend; Qiskit quantum plugin; webOS TV agent; security hardening | 5c enterprise tier for inference backbones; 5b FPGA tier for accelerator abstraction | LLM inference via Groq under 100ms TTFT; all 60+ devices have integration paths |

### 9.1 Phase 5a: Gaming & SBC Integration (Weeks 1--6)

Phase 5a opens with the highest-priority integration in the entire Phase 5 program: the Steam Deck. Four million units shipped, native SteamOS (Arch Linux), sixteen gigabytes of unified memory, and a 1.6 TFLOPS RDNA 2 GPU make it the only consumer handheld that requires zero hardware modification to join a production cluster. Week 1 delivers a Flatpak-packaged agent that auto-launches in Desktop Mode, detects the AMD Custom APU via Vulkan compute, and reports into the mesh. Week 2 extends this to the x86 handheld family --- ROG Ally, Legion Go, GPD Win --- through Bazzite compatibility and ROCm overrides. The power-aware scheduler introduced in Week 5 monitors `/sys/class/power_supply` for battery state and `/sys/class/hwmon` for thermal zones, suspending GPU compute when gaming activity is detected. A Steam Deck donating cycles while docked and charging contributes 1.6 TFLOPS without ever impacting a gaming session.

Weeks 3--4 shift to the advanced ARM SBC tier. The RK3588 ecosystem --- ROCK 5B, NanoPi R6S, R6C, and Turing RK1 --- receives support via Armbian APT packages. Each board is probed for its 6 TOPS NPU, 2.5GbE interfaces, and NVMe storage, then classified into STANDARD or NETWORK_GATEWAY tier. Jetson integration runs in parallel: L4T packages for the Orin Nano Super bring the TensorRT backend online, registering the device as an AI_WORKER with 67 TOPS of inference capacity. Week 6 closes with a ten-node integration test mixing handheld donors and ARM SBC workers, validating that the scheduler routes AI workloads to Jetson, edge containers to RK3588 boards, and batch jobs to idle handhelds.

### 9.2 Phase 5b: RISC-V & FPGA (Weeks 7--12)

Phase 5b depends on the CI/CD pipeline and multi-arch container registry operational since Phase 5a Week 4. The central challenge is architectural portability: the HelixCluster agent must compile natively on riscv64gc without emulation. Week 7 establishes the cross-compilation pipeline --- `GOARCH=riscv64 GOOS=linux` builds in CI, producing native binaries within minutes of every commit. Week 8 deploys the first RISC-V production node: a Milk-V Pioneer with 64 SG2042 cores, configured as a build-farm worker. Week 9 adds the VisionFive 2 and Milk-V Jupiter, probing for RVV 1.0 vector extension support.

The FPGA track begins in Week 10 with Zynq hard-processor support. The DE10-Nano and PYNQ-Z2 run PetaLinux-based agent packages on their ARM Cortex-A9 cores, reporting FPGA fabric to the control plane as a schedulable resource. Week 11 integrates the KV260's DPU through the Vitis AI backend, enabling YOLO offload at 0.92 TOPS --- a fraction of the Jetson's throughput but with deterministic latency and five times better energy efficiency. Week 12 closes with an eight-node test spanning three architectures: arm64 (ROCK 5B), riscv64 (Milk-V Pioneer), and FPGA_HARD_ACCEL (KV260), all in a single WireGuard mesh.

### 9.3 Phase 5c: Enterprise & IoT (Weeks 13--18)

Phase 5c introduces the density that transforms a hobby cluster into a production platform. Week 13 tackles the most cost-effective core source in the taxonomy: used AMD EPYC servers. An automated provisioning script detects CPU model, memory channels, and NVMe topology, installs the agent, and registers the node as CORE_TRUSTED within thirty minutes. The script includes Coreboot detection to upgrade trust tier when open firmware is present. Week 14 extends this to Ampere Altra, validating 80-core ARM64 containers.

The cloud-hybrid track opens in Week 15. Terraform modules auto-provision AWS Graviton4 spot instances that WireGuard into the on-prem mesh; a preemption handler catches the two-minute AWS warning, drains the node, and checkpoints stateful workloads. Weeks 16--17 address the always-on edge backbone. The GL.iNet GL-MT6000 router runs the agent as a Docker container alongside OpenWrt, consuming under five percent CPU while contributing its quad-core A53 and dual 2.5GbE. Synology DS923+ and QNAP TS-464 NAS units deploy via Container Manager and Container Station, registering storage capacity and running cache services. Week 18 validates the full hybrid: five on-prem nodes plus five cloud spot workers, with graceful failover under simulated preemption.

### 9.4 Phase 5d: Exotic Technology (Weeks 19--24)

Phase 5d reaches beyond conventional silicon. Week 19 integrates GroqCloud as an inference backend: LLM workloads hit the Groq API with sub-100ms time-to-first-token on Llama 3.1 70B, a latency envelope impossible with edge hardware alone. Week 20 adds Cerebras CS-3 cloud API for large-model inference beyond the LPU's SRAM budget. Week 21 implements a quantum research plugin using Qiskit Runtime, allowing nodes to submit circuit jobs to IBM Quantum asynchronously --- firmly experimental, but architecturally integrated.

Week 22 pilots the most unconventional donor: an LG webOS smart TV running a JavaScript agent as a background service, communicating via WebSocket to a nearby edge gateway. The TV's video decode hardware leaves CPU cores idle during streaming, creating a genuinely free compute source. Week 23 hardens the entire stack: gVisor sandboxes for UNTRUSTED tier nodes, TPM attestation for CORE_TRUSTED provisioning, and full tier-aware isolation. Week 24 marks general availability: documentation complete, all sixty-plus device types having defined integration paths, benchmark suite published, and the security model enforced across every trust tier.

**Table 2: Weekly Milestone Map**

| Week | Phase | Milestone | Deliverable | Acceptance |
|------|-------|-----------|-------------|------------|
| 1 | 5a | Steam Deck prototype | Flatpak agent, Vulkan compute | Runs on SteamOS, detects 1.6 TFLOPS |
| 2 | 5a | x86 handheld support | Bazzite/ROCm override | Agent runs on ROG Ally, Legion Go |
| 3 | 5a | RK3588 base support | Armbian packages for ROCK 5B, R6S | APT install, NPU detected |
| 4 | 5a | Jetson Orin integration | L4T packages, TensorRT backend | 67 TOPS reported, AI_WORKER tier |
| 5 | 5a | Power-aware scheduler | Battery/thermal workload control | >80% battery drain reduction |
| 6 | 5a | Phase integration test | 10-node mixed cluster | All nodes report, accept workloads |
| 7 | 5b | RISC-V agent build | riscv64 Go binaries, CI pipeline | Native binary, no emulation |
| 8 | 5b | Milk-V Pioneer | Debian packages, build farm | 64-core CI, benchmarks logged |
| 9 | 5b | VisionFive 2 / Jupiter | Armbian, RVV 1.0 detection | Edge workloads on RISC-V |
| 10 | 5b | FPGA Zynq support | PetaLinux for DE10-Nano, PYNQ-Z2 | Agent on ARM cores, fabric reported |
| 11 | 5b | FPGA DPU integration | KV260 Vitis AI backend | YOLO offload at 0.92 TOPS |
| 12 | 5b | Phase integration test | 8-node arm64+riscv64+FPGA cluster | Full heterogeneity demonstrated |
| 13 | 5c | Used EPYC onboarding | Auto-provisioning script | Joins as CORE_TRUSTED in <30 min |
| 14 | 5c | Ampere Altra | ARM64 server packages | 80-core container density test |
| 15 | 5c | Cloud spot | WireGuard mesh, preemption handler | Graceful join/leave under spot kill |
| 16 | 5c | Router gateway | OpenWrt opkg for MT6000 | <5% CPU overhead, routing intact |
| 17 | 5c | NAS storage | Container Manager / Container Station | Storage capacity reported |
| 18 | 5c | Phase integration test | Hybrid 5 on-prem + 5 spot | Checkpointing under preemption |
| 19 | 5d | Groq LPU API | LLM routing to GroqCloud | <100ms TTFT on 70B model |
| 20 | 5d | Cerebras API | CS-3 cloud backend | Large-model inference verified |
| 21 | 5d | Quantum plugin | Qiskit Runtime integration | Async circuit submission |
| 22 | 5d | Smart TV | webOS JS agent prototype | Background service, WebSocket comm |
| 23 | 5d | Security hardening | gVisor/Kata, full attestation | All tiers sandboxed appropriately |
| 24 | 5d | Phase 5 GA | Docs, 60+ devices, benchmarks | Complete integration paths defined |

#### 9.4.1 Risk Mitigation

Three risks threaten the overall timeline. First, the NVIDIA acquisition of Groq IP (December 2025) creates vendor volatility for the LPU inference tier. Mitigation: architect the inference backend behind a provider-agnostic interface so that Groq, Cerebras, SambaNova SN40L, or local llama.cpp on Jetson can serve the same workload type with only configuration changes. Second, RISC-V performance remains an order of magnitude behind ARM and x86; a Pioneer build farm could become a bottleneck. Mitigation: cap RISC-V assignments to build jobs and lightweight edge tasks, never routing latency-sensitive inference to RISC-V nodes, and monitor RVA23-profile chip announcements for 2027 procurement. Third, Steam Deck agent adoption depends on volunteer opt-in. Mitigation: make the agent unobtrusive --- background CPU-only by default, GPU only when docked and charging, with one-click pause --- and publish benchmarks showing that a docked Steam Deck contributes meaningful compute without detectable performance impact.

#### 9.4.2 Beyond Phase 5: Phase 6 Possibilities

Phase 5 ends with a sixty-device taxonomy and a working heterogeneous cluster spanning eight architectures. Phase 6 would expand in three directions. The first is scale: the volunteer GPU tier of Steam Decks, ROG Ally units, and eventually Nintendo Switch 2 homebrew devices could grow the cluster by an order of magnitude, requiring a gossip-protocol overlay for million-node membership. The second is intelligence: integrating the Groq/Cerebras backbone with a cluster-wide retrieval-augmented generation layer, turning HelixCluster into a distributed AI brain where edge nodes cache and preprocess while exotic accelerators handle heavy inference. The third is autonomy: self-healing node procurement --- spot-instance auto-scaling that selects instance types based on real-time price-performance data, and automated procurement that triggers used EPYC purchases when price-per-core thresholds are hit. The architecture built across these twenty-four weeks accommodates all three directions without structural redesign --- the tier system, the trust model, and the WireGuard mesh scale naturally from ten nodes to ten thousand.
