# Phase 5 — Cross-Dimension Insights

> **Synthesis Date:** July 2025
> **Source Dimensions:** 7 research streams, 40+ device categories, 150+ individual devices
> **Target:** HelixCluster Phase 5 architecture and procurement strategy

---

## Insight 1: The Steam Deck Bridges Consumer Gaming and Production Cluster Compute
**Dimensions:** Dim 1 (Gaming & Handheld) + Dim 5 (Enterprise Server & Cloud)

**Insight:** The Steam Deck is the only device that simultaneously ranks as a top-tier gaming handheld (4M+ units shipped) and a viable HelixCluster compute node. Its 1.6 TFLOPS RDNA 2 GPU, 16GB unified memory, and native SteamOS (Arch Linux) place it in a unique category: a consumer entertainment device that requires zero modification to join a distributed compute cluster. At $279 refurbished, it delivers GPU compute at $0.17/GFLOPS — competitive with used EPYC systems for GPU-accelerated workloads. The 4-15W TDP envelope means a fleet of Steam Decks consumes less power than a single EPYC server while delivering aggregate GPU compute equivalent to a mid-range desktop GPU. The comparison with Dim 5's entry-level enterprise hardware is striking: a single Steam Deck OLED matches ~30% of a Jetson Orin Nano Super's GPU compute (1.6 TFLOPS vs. 2.6 TFLOPS effective) at 60% lower power and with a built-in battery/UPS.

**Implication:** HelixCluster should treat the Steam Deck ecosystem (including ROG Ally, Legion Go, and GPD Win) as a distinct "volunteer GPU tier" — consumer devices that donate idle compute without requiring hardware purchases. This tier bridges the gap between hobbyist distributed computing (BOINC, Folding@Home) and production cluster workloads.

**Action Item:** Develop a "Desktop Mode Agent" that auto-launches when Steam Deck enters desktop mode, with power-aware scheduling that pauses GPU compute when gaming resumes and leverages the 16GB unified memory for ML inference workloads (Dim 1's Vulkan compute backend + Dim 5's container orchestration).

---

## Insight 2: ARM SBC Density vs. Used Server Muscle — The Turing RK1 vs. EPYC vs. Jetson Trilemma
**Dimensions:** Dim 2 (Advanced ARM SBCs) + Dim 5 (Enterprise Server & Cloud)

**Insight:** Three radically different approaches to cluster compute have converged at similar price points (~$1,000-$2,500), creating a genuine architectural choice for HelixCluster builders. The Turing Pi 2.5 with 4x RK1 modules delivers 32 Cortex-A76/A55 cores, 24 TOPS NPU aggregate, 128GB RAM max, and 4x NVMe in a mini-ITX footprint for ~$2,100 — but requires an integrated GbE switch for inter-node communication. A used EPYC 7742 system (64 Zen 2 cores, 128GB DDR4, 128x PCIe Gen4 lanes) costs ~$900-1,100 but draws 225W and occupies a full tower. The Jetson Orin Nano Super ($249 x 4 = ~$1,000) delivers 268 TOPS aggregate AI performance at 100W total but only 24 CPU cores and 32GB RAM. The density-per-watt champion is the Turing Pi (25 GOPS/W CPU + 0.3 TOPS/W NPU at <80W), while the raw-compute-per-dollar champion is the used EPYC, and the AI-inference champion is the Jetson quartet. No single approach dominates all metrics.

**Implication:** HelixCluster Phase 5 should adopt a heterogeneous rack architecture where ARM SBC clusters (Turing Pi / Mixtile Blade 3) handle edge-tier container workloads, used EPYC systems run control-plane and database services, and Jetson Orin nodes serve AI inference — all connected via the 2.5GbE/10GbE mesh described in Dim 5's networking topology.

**Action Item:** Benchmark the three configurations head-to-head on identical workloads (K3s control plane, PostgreSQL, Redis, llama.cpp inference) and publish a "cluster recipe" guide showing build lists for each approach at $1,000, $2,500, and $5,000 price points.

---

## Insight 3: RISC-V Is the Insurance Policy Against Architecture Lock-In
**Dimensions:** Dim 3 (RISC-V Emerging) + Dim 7 (Exotic Future Compute)

**Insight:** RISC-V has crossed the critical threshold from "academic curiosity" to "production-viable for edge workloads" in 2025. Docker v29 ships for RISC-V within 6 days of x86/ARM release, Kubernetes runs via community K3s forks, and Go/Rust/Zig all compile natively. However, the performance gap remains severe: the 64-core Milk-V Pioneer achieves only ~1/10th the throughput of an Ampere Altra Max, and even the best single-core RISC-V (SiFive P550 at $399) underperforms a Raspberry Pi 4. The strategic value of RISC-V for HelixCluster is not immediate performance but **future-proofing**: the RVA23-profile chips (SiFive P870, Tenstorrent Ascalon) sampling in 2025-2026 promise to close the gap with mid-range ARM by 2027. The market is projected to grow from $1.1B (2023) to $7B+ (2030). Meanwhile, Dim 7's exotic silicon — from Groq LPUs to Cerebras WSE-3 — represents potential integration targets that may or may not support standard ISAs. RISC-V's open ISA ensures HelixCluster can always compile and run its agent software regardless of which exotic hardware enters the market.

**Implication:** RISC-V should be treated as a "compile target insurance policy" rather than a primary compute tier. Maintaining RISC-V builds of the HelixCluster agent ensures architectural portability if supply chain disruptions affect ARM or x86 availability.

**Action Item:** Establish a CI/CD pipeline that builds HelixCluster agent binaries for riscv64gc alongside x86_64 and arm64. Deploy 2-3 Milk-V Jupiter boards as Tier-4 edge nodes to validate real-world compatibility. Monitor RVA23-profile chip announcements for 2027 procurement planning.

---

## Insight 4: FPGA + AI SBC Hybrids Create a New "Reconfigurable Accelerator" Tier
**Dimensions:** Dim 2 (Advanced ARM SBCs) + Dim 4 (FPGA Compute)

**Insight:** The convergence of hard-processor FPGAs (Zynq, Zynq UltraScale+) with AI-focused ARM SBCs (Jetson Orin, RK3588 with NPU) creates a unique hybrid tier that neither category achieves alone. The Kria KV260 ($249) pairs a quad-core Cortex-A53 with FPGA fabric and a DPU delivering 0.92 TOPS — less raw AI performance than the Jetson Orin Nano Super (67 TOPS) but with deterministic latency and 5x better energy efficiency than Jetson Nano for equivalent INT8 workloads. The DE10-Nano ($190) offers dual Cortex-A9 + substantial FPGA fabric, running standard Linux while enabling custom hardware acceleration. The emerging open-source toolchain (OpenXC7 for Zynq-7000, Yosys/nextpnr for ECP5) means these hybrids can be programmed without proprietary Vivado licenses. A single ECP5-85K FPGA can host 4-8 VexRiscv soft cores, creating "many-small-nodes" on a single chip. The most exciting intersection is partial reconfiguration: "FPGA containers" where bitstreams swap at runtime like Docker images, enabling workload-specific hardware acceleration.

**Implication:** FPGA+ARM hybrids should form a specialized tier in HelixCluster for latency-sensitive and custom-precision workloads — cryptography, signal processing, and custom ML inference where Jetson's GPU approach is overkill or too variable.

**Action Item:** Procure 2x KV260 and 2x DE10-Nano boards to build a "reconfigurable accelerator" testbed. Develop LiteX+VexRiscv soft-core configurations that run the HelixCluster agent entirely within FPGA fabric (no hard processor). Evaluate partial reconfiguration for dynamic workload switching.

---

## Insight 5: Router + NAS Convergence Creates the "Always-On Edge Backbone"
**Dimensions:** Dim 6 (IoT Smart Edge) + Dim 2 (Advanced ARM SBCs)

**Insight:** The GL.iNet GL-MT6000 ($159, quad-core A53 @ 2.0 GHz, 1GB RAM, 8GB eMMC, dual 2.5GbE, Docker support) and the NanoPi R6S ($129, RK3588S, 8GB RAM, 6 TOPS NPU, dual 2.5GbE) represent a new category: **network infrastructure that doubles as compute nodes**. These devices are always-on by definition (routers cannot sleep), consume <20W, and provide the cluster's networking backbone while simultaneously contributing CPU and NPU cycles. When combined with NAS devices (Synology DS923+, QNAP TS-464) that also run Docker and provide distributed storage, the edge layer becomes self-sufficient. The MT6000's 8GB eMMC is remarkable for a router — most OpenWrt devices have 16-256MB flash — enabling full container ecosystems. The NanoPi R6S's RK3588S SoC (4x A76 + 4x A55) provides compute density comparable to a Raspberry Pi 5 but with dual 2.5GbE and 6 TOPS NPU. At 4.6W idle / 11.4W max, the R6S achieves ~7 GFLOPS/W — among the best efficiency ratios across all 40+ device types evaluated.

**Implication:** The edge backbone of HelixCluster should be built from router+compute hybrids rather than separate networking and compute hardware. This reduces cost, power, and physical footprint while ensuring 24/7 availability.

**Action Item:** Standardize on GL-MT6000 for edge gateway/router+compute roles and NanoPi R6S for edge AI+compute roles. Deploy them as a unified edge tier with WireGuard mesh backhaul to core nodes. Develop a "router-first" deployment script that installs the HelixCluster agent via Docker on OpenWrt without disrupting routing functions.

---

## Insight 6: The Universal Price/Performance Sweet Spot Is $150-$350 Per Node
**Dimensions:** Dim 1 (Gaming) + Dim 2 (ARM SBCs) + Dim 5 (Enterprise) + Dim 6 (IoT Edge)

**Insight:** Across all seven dimensions, a remarkable convergence emerges at the $150-$350 price point:

| Device | Price | Compute | Power | Best Feature |
|--------|-------|---------|-------|-------------|
| Steam Deck LCD (refurb) | $279 | 1.6 TFLOPS GPU + 448 GFLOPS CPU | 4-15W | Native Linux, volunteer-owned |
| Jetson Orin Nano Super | $249 | 67 TOPS AI, 6x A78AE CPU | 7-25W | Best AI performance per dollar |
| Radxa ROCK 5B 8GB | $157 | 8 cores, 6 TOPS NPU, 2.5GbE | 5-10W | Best general-purpose ARM SBC |
| GL.iNet GL-MT6000 | $159 | 4x A53 @ 2.0 GHz, Docker | <20W | Router + compute dual role |
| NanoPi R6S | $129 | 8 cores, 6 TOPS NPU, dual 2.5GbE | 4.6-11W | Best edge AI+network combo |
| EPYC 7551 used system | ~$350 | 32 Zen cores, 128x PCIe Gen4 | 180W | Best x86 core density per dollar |
| DE10-Nano | $190 | 2x A9 + FPGA fabric, GbE | 3-10W | Best FPGA+ARM hybrid |

At this price point, HelixCluster can deploy a **heterogeneous 7-node starter cluster** covering all workload types (GPU, AI, general compute, edge, networking, FPGA) for approximately $1,500 — less than the cost of a single Mac Studio M3 Ultra. The price/performance curve steepens dramatically above $500 (diminishing returns) and flattens below $100 (insufficient capability).

**Implication:** HelixCluster's "recommended starter kit" should be built around this $150-$350 sweet spot, mixing 2-3 device types rather than homogenous nodes. This maximizes workload flexibility while minimizing capital expenditure.

**Action Item:** Design and publish the "HelixCluster Phase 5 Starter Kit" — a $1,500 build list with explicit device choices, expected performance benchmarks, and deployment scripts. Target: 8+ CPU cores, 8+ GB RAM, GPU or NPU acceleration, 2.5GbE networking, and FPGA programmability across the ensemble.

---

## Insight 7: Cloud Spot + On-Prem Heterogeneous = "Elastic Cluster"
**Dimensions:** Dim 5 (Enterprise Server & Cloud) + Dim 6 (IoT Smart Edge)

**Insight:** The integration of cloud spot instances with on-prem heterogeneous hardware creates a genuinely elastic cluster architecture. AWS Graviton4 spot instances at $0.007-0.012/vCPU/hour can be 3-5x cheaper than owned hardware for bursty workloads. WireGuard mesh tunnels connect cloud VMs to on-prem edge nodes (GL-MT6000 routers, NAS devices) with minimal overhead. The key insight from preemption economics: for steady-state 24/7 workloads, owned hardware breaks even at 18-30 months; for bursty or experimental workloads, cloud spot always wins. HelixCluster can use on-prem edge nodes (Dim 6's MT6000/R6S routers, NAS devices) as the persistent "base layer" that maintains cluster state and handles always-on services, while cloud spot instances provide elastic burst capacity. The 2-minute AWS preemption warning is sufficient for checkpoint/resume if the cluster agent implements graceful shutdown hooks. Mixed replica strategies (N baseline on-prem + M opportunistic spot) optimize cost without sacrificing availability.

**Implication:** HelixCluster should be designed as a "hybrid-first" platform where on-prem edge nodes form the persistent backbone and cloud spot instances join as ephemeral compute donors. This is fundamentally different from "cloud-first" or "on-prem-only" architectures.

**Action Item:** Implement spot instance awareness in the HelixCluster agent: (1) drain and checkpoint on preemption warning, (2) prefer spot for stateless/batch workloads, (3) maintain on-prem replicas for stateful services. Develop Terraform/Pulumi modules for auto-provisioning Graviton4 spot nodes that WireGuard into the on-prem mesh.

---

## Insight 8: Groq LPU as the LLM Inference Backbone — If NVIDIA Doesn't Kill It
**Dimensions:** Dim 7 (Exotic Future Compute) + Dim 1 (Gaming & Handheld)

**Insight:** The Groq LPU represents the most HelixCluster-relevant exotic technology (55% probability by 2027), with deterministic sub-100ms TTFT latency and 300-500 tokens/sec on Llama 2 70B — 10x faster than H100. At 1-3 joules per token (vs. 10-30 for H100), it is the efficiency champion for LLM inference. The NVIDIA $20B licensing acquisition (December 2025) creates both opportunity and risk: LPU technology may be integrated into NVIDIA's product stack (broadening availability) or buried (eliminating competition). The Dim 1 connection is critical: Steam Deck and other handhelds running llama.cpp via Vulkan achieve only ~8-12 tokens/sec for 7B models. For HelixCluster to serve as a "distributed LLM brain," it needs an inference backbone that complements the edge devices. Groq LPU (via GroqCloud API or GroqRack on-prem) could serve as the "fast path" for LLM queries while edge devices handle local caching, prompt preprocessing, and small-model inference.

**Implication:** The LLM inference tier of HelixCluster should be designed as a two-tier system: Groq LPU (or equivalent low-latency accelerator) for the fast path, and Steam Deck / Jetson edge nodes for local caching and small-model inference.

**Action Item:** Integrate GroqCloud API as a HelixCluster inference backend. Benchmark latency and throughput for typical agent workloads (prompt classification, RAG query generation, response synthesis). Develop a fallback strategy if NVIDIA discontinues standalone LPU products. Monitor SambaNova SN40L (45% probability) and Cerebras CS-3 (40%) as alternatives.

---

## Insight 9: The "Compute Donor" Model — Devices That Contribute While Idle
**Dimensions:** Dim 1 (Gaming & Handheld) + Dim 6 (IoT Smart Edge)

**Insight:** A new compute paradigm emerges from the intersection of gaming handhelds and always-on edge devices: the "compute donor" model where devices contribute cycles during idle time without sacrificing their primary function. The Steam Deck is the archetype — a gaming device used intermittently that can donate 1.6 TFLOPS GPU and 448 GFLOPS CPU during off-hours. Smart TVs (LG webOS, Samsung Tizen) have dedicated video decode hardware leaving CPU cores idle during streaming. NAS devices (Synology, QNAP) run 24/7 with substantial headroom above their storage workload. Even the GL-MT6000 router's hardware offloading engines leave its quad-core A53 available for user processes. The aggregate potential is enormous: 4M+ Steam Decks, 155M+ Nintendo Switches (if homebrewed), millions of NAS units, and countless smart TVs. At a conservative 2-5% opt-in rate (typical for BOINC), this represents 100K-400K gaming handhelds and millions of edge devices.

**Implication:** HelixCluster should embrace the "compute donor" model as a core design principle, not an afterthought. The agent must be unobtrusive, power-aware, and function-respecting — pausing compute when the device is actively used and resuming during idle periods.

**Action Item:** Develop a "Donor Agent" with three operating modes: (1) **Background** — CPU-only, low priority, never interfere with primary use; (2) **Docked** — full GPU/CPU compute when connected to AC power and external display; (3) **Scheduled** — compute only during configured hours (e.g., 11 PM - 7 AM). Implement idle detection across all supported platforms (system load, user activity, battery state).

---

## Insight 10: Security Tier Mapping — From Fully Trusted to Exotic
**Dimensions:** Dim 1-7 (All Dimensions)

**Insight:** Every device across all seven dimensions can be classified into four security tiers based on hardware auditability, firmware openness, and supply chain trust:

| Tier | Trust Level | Representative Devices | Rationale |
|------|-------------|----------------------|-----------|
| **T1: Fully Trusted** | Auditable firmware, open ISA | RISC-V (Milk-V Pioneer, Jupiter), OpenPOWER (Raptor Blackbird), FPGA with open toolchains (ULX3S, Colorlight) | Every gate and firmware byte is auditable. No proprietary blobs required. |
| **T2: Semi-Trusted** | Standard Linux, user-controlled | Steam Deck, ROG Ally, RK3588 SBCs, EPYC servers, Ampere Altra, Mini PCs | User controls the OS but proprietary firmware (UEFI, ME, PSP) exists. Well-understood attack surface. |
| **T3: Restricted** | Sandboxed or limited access | Nintendo Switch (homebrew), Xbox Dev Mode, Smart TVs (webOS/Tizen), Wear OS | Custom code runs in sandboxed environments with limited hardware access. Suitable for non-sensitive workloads only. |
| **T4: Closed/Opaque** | No user code execution | Nintendo Switch 2 (no homebrew), Xbox (retail), Apple Watch, Echo, HomePod | Custom code is impossible or requires exploits. These devices cannot join HelixCluster as compute donors. |
| **T5: Exotic/Specialized** | Vendor-dependent trust model | Groq LPU, Cerebras CS-3, IBM z17, TPU, Trainium | Trust depends on vendor security practices. Mainframes offer extreme RAS but opaque internals. Cloud exotic hardware requires tenant isolation trust. |

The security tier determines which workloads a device can execute: T1 for cryptographic key management and consensus algorithms, T2 for general compute and containerized workloads, T3 for data aggregation and relay, T4 for none, and T5 for vendor-approved workloads only.

**Implication:** HelixCluster's workload scheduler must be security-tier-aware, routing sensitive workloads (key management, confidential inference) to T1 nodes and relegating sandboxed/TV-class nodes to non-sensitive batch processing.

**Action Item:** Implement security tier attestation in the HelixCluster agent: verify boot chain integrity, report firmware openness level, and request workload assignments matching the device's trust tier. Maintain a "tier map" that restricts consensus-critical workloads to T1 nodes only.

---

## Insight 11: Linux Support Is the Ultimate Gatekeeper
**Dimensions:** Dim 1-7 (All Dimensions)

**Insight:** Across all 150+ devices evaluated, the presence of functional Linux support is the single strongest predictor of HelixCluster viability — stronger than raw performance, price, or power efficiency. Devices with native Linux (Steam Deck, ROCK 5B, Jetson Orin, EPYC servers, RISC-V boards) achieve Tier 1-2 integration with minimal effort. Devices requiring community Linux (Nintendo Switch via L4T, FPGA soft-cores) achieve Tier 3-4 with significant porting work. Devices with no Linux path (Xbox retail, Apple Watch, Echo, HomePod) are permanently excluded regardless of hardware capability. The pattern is consistent across all dimensions: the Xbox Series X has 12 TFLOPS RDNA 2 GPU and 16GB GDDR6 but is useless for HelixCluster because no Linux jailbreak exists; the $15 Colorlight 5A-75B FPGA board runs a RISC-V soft-core Linux and is immediately viable. Even within the same device category, Linux support stratifies viability: LG webOS TVs (Linux-based, Node.js services) are Tier 2, while Samsung Tizen TVs (also Linux-based but more restrictive API) are Tier 2.5, and Android TVs with full ADB access are Tier 1.5.

**Implication:** HelixCluster's device qualification process should prioritize Linux compatibility above all other metrics. A "Linux Support Matrix" should be maintained as a living document, tracking kernel version, GPU driver status, container runtime support, and mainline vs. vendor kernel requirements for every candidate device.

**Action Item:** Create a automated "Linux Capability Scanner" that runs on candidate devices and reports: kernel version, container runtime (Docker/Podman), GPU compute API availability (Vulkan/CUDA/OpenCL/ROCm), NPU driver status, networking throughput, and systemd availability. Use this scanner as the first step in device onboarding, before any workload-specific benchmarks.

---

## Insight 12: The Complete Device Taxonomy — Every Device Mapped to a HelixCluster Tier
**Dimensions:** All 7 Dimensions

**Insight:** The seven research streams collectively define a comprehensive five-tier taxonomy that maps every evaluated device category to a HelixCluster role. This taxonomy is the architectural foundation for Phase 5:

### Tier 0: AI/Inference Controllers (Dim 2, 5, 7)
Highest-performance nodes for AI inference and model serving.
- **Devices:** Jetson AGX Orin (275 TOPS), Jetson Thor T5000 (1000 FP8 TOPS), Groq LPU, Cerebras CS-3, SambaNova SN40L, used A100/MI210 GPU servers, Mac Studio M3 Ultra
- **Role:** LLM inference backbone, vision AI pipelines, training coordination
- **Count:** 1-5% of cluster nodes, ~60% of AI inference capacity

### Tier 1: Core Compute (Dim 2, 5)
Primary workhorses for containers, databases, and general compute.
- **Devices:** Used EPYC 7742/7713/9654 servers, Ampere Altra Q80-128, Threadripper PRO, Radxa ROCK 5B, Turing RK1 clusters, Jetson Orin Nano Super
- **Role:** K3s worker nodes, PostgreSQL, Redis, web services, CI/CD runners
- **Count:** 20-30% of cluster nodes, ~70% of general compute capacity

### Tier 2: Edge Compute + Network (Dim 2, 6)
Always-on edge nodes that double as network infrastructure.
- **Devices:** GL.iNet GL-MT6000, NanoPi R6S, Synology DS923+, QNAP TS-464, NVIDIA Shield TV Pro, Jetson Xavier NX
- **Role:** WireGuard mesh gateways, local caching, sensor aggregation, distributed storage
- **Count:** 30-40% of cluster nodes, always-on backbone

### Tier 3: Volunteer / Compute Donors (Dim 1, 6)
Consumer devices that donate idle cycles.
- **Devices:** Steam Deck, ROG Ally, Legion Go, GPD Win, LG webOS TV, Samsung Tizen TV, Khadas Edge2
- **Role:** Batch processing, background tasks, idle-time inference, data relay
- **Count:** 20-50% of cluster nodes (highly variable), ~20% of burst capacity

### Tier 4: Experimental / Specialized (Dim 3, 4, 7)
Non-standard architectures for specific workloads or research.
- **Devices:** Milk-V Pioneer (RISC-V build farm), SiFive P550 (dev/test), DE10-Nano/KV260 (FPGA acceleration), Raptor Blackbird (security node), IBM z17 (ultra-secure workloads), Jetson TX2 NX (legacy AI)
- **Role:** Build farms, security-critical operations, custom hardware acceleration, research
- **Count:** 5-10% of cluster nodes, specialized workloads only

### Excluded: No Viable Integration Path
- **Devices:** Xbox (all generations, no Linux), Nintendo Switch 2 (no homebrew), Apple Watch, Echo/HomePod/Nest (closed), Bitcoin ASICs (SHA-256 only), Wear OS devices (too constrained), most automotive systems (closed)

**Aggregate Metrics (Estimated 50-Node Cluster):**
| Metric | Value |
|--------|-------|
| Total CPU cores | 800-1,200 |
| Total AI TOPS | 400-600 |
| Total RAM | 1.5-3 TB |
| Total storage | 50-100 TB |
| Aggregate power | 1,500-3,000W |
| Total CAPEX (sweet spot builds) | $8,000-$15,000 |
| Always-on nodes | 15-20 (Tiers 0-2) |
| Volunteer/donor nodes | 25-35 (Tier 3) |

**Implication:** This taxonomy enables HelixCluster to scale from a $1,500 starter kit (7 nodes across 3 tiers) to a 50-node production cluster spanning all five tiers, with clear upgrade paths and workload routing rules.

**Action Item:** Implement the five-tier taxonomy in the HelixCluster scheduler: assign nodes to tiers at registration time based on automated capability scanning, route workloads to appropriate tiers, and maintain tier-aware replication (e.g., 3 replicas on Tier 1-2 for durability, 1 replica on Tier 3 for availability). Publish the taxonomy as the "HelixCluster Phase 5 Device Reference" and update it quarterly as new devices are evaluated.

---

## Appendix: Cross-Dimension Capability Matrix

| Capability | Dim 1 Gaming | Dim 2 ARM SBCs | Dim 3 RISC-V | Dim 4 FPGA | Dim 5 Enterprise | Dim 6 IoT Edge | Dim 7 Exotic |
|---|---|---|---|---|---|---|---|
| **Linux Support** | Excellent (x86) | Excellent (ARM) | Good (emerging) | Good (hard proc.) | Excellent | Mixed | N/A (cloud/vendor) |
| **GPU Compute** | 1.6-12 TFLOPS | 0.5 TOPS (Mali) | None | DPU 0.92 TOPS | 100-450 TFLOPS | None | TPU/Groq/ASIC |
| **AI/ML** | Vulkan inference | 6-67 TOPS NPU | Limited | DPU/custom | CUDA/ROCm | 0-6 TOPS | 100-21,000 TOPS |
| **Memory** | 16-32 GB | 4-32 GB | 4-128 GB | 0.5-4 GB | 64-512 GB | 0.5-8 GB | 44 GB-1.5 TB |
| **Networking** | Wi-Fi 5/6E | 1-2x 2.5GbE | 1-2x GbE/2.5GbE | 1x GbE | 10-100 GbE | 1-2x 2.5GbE | Cloud/network |
| **Power** | 4-30W | 5-25W | 5-125W | 1-20W | 150-360W | 1-20W | 7-23,000W |
| **Price/Node** | $279-999 | $49-449 | $60-1,999 | $15-249 | $350-4,000 | $50-550 | Cloud-$3M |
| **Best For** | Volunteer GPU | General compute | Build farms | Custom accel. | Core workloads | Always-on edge | AI inference |
| **Helix Tier** | Tier 3 (Donor) | Tier 1 (Core) | Tier 4 (Exp.) | Tier 4 (Special) | Tier 0-1 (Core) | Tier 2 (Edge) | Tier 0 (AI) |

---

*Cross-dimension synthesis compiled from 7 research reports, 40+ device categories, and 150+ individual devices evaluated across dimensions 1-7 of the HelixCluster Phase 5 research program.*
