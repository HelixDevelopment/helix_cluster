# Executive Summary

HelixCluster now supports virtually every Linux-capable device on Earth. Phase 5 expands the ecosystem from 12 to **64 device types** across **15 classification tiers** and **5 trust levels**, establishing a universal integration layer that enables any Linux-capable device---from a $15 FPGA to a $2.3 million Cerebras wafer-scale engine---to participate as a first-class cluster node [^8^].

## Key Metrics

| Metric | Value | Significance |
|--------|-------|-------------|
| **Total device types** | 64 (up from 12) | 5.3x hardware expansion [^1^] |
| **Classification tiers** | 15 (up from 5) | Granular trust/compute classification [^8^] |
| **Architecture coverage** | x86, ARM, RISC-V, FPGA, POWER, LoongArch | Eliminates architecture lock-in [^3^] |
| **Minimum node price** | $15 (Colorlight FPGA) | Sub-$20 entry point for experimental clusters [^4^] |
| **Best handheld compute/$** | Steam Deck at $0.17/GFLOPS | 4M+ unit installed base, native Linux [^1^] |
| **Best server price/core** | Used EPYC 7551 at ~$2.10/core | 64-core builds under $1,100 [^5^] |
| **Best edge/gateway node** | GL.iNet MT6000 at $159 | Docker + dual 2.5GbE + quad-core A53 [^6^] |
| **Best AI inference/$** | Jetson Orin Nano Super at $3.72/TOPS | 67 TOPS at $249 via free firmware upgrade [^2^] |
| **Highest-density ARM node** | Turing RK1: 32 cores, ~$800 | 4x RK3588 modules on single carrier [^2^] |
| **Volunteer GPU pool potential** | 320 petaFLOPS (200K Steam Decks) | Mid-size supercomputer on donated idle cycles [^1^] |
| **Cloud spot floor price** | $0.007/vCPU/hour (AWS Graviton3) | 70--90% below on-demand [^5^] |
| **Max single-node AI perf** | 275 TOPS (Jetson AGX Orin) | Edge inference density rivaling cloud GPUs [^2^] |

## The 64-Device Taxonomy

Phase 5 introduces a **15-tier classification system** mapping device capability, openness, and trust requirements. Tiers 1--3 (CORE_TRUSTED through EDGE_COMPUTE) cover servers, mini PCs, and desktop-class hardware for control plane and containerized workloads. Tiers 4--5 (AI_WORKER, AI_CONTROLLER) designate GPU/NPU-accelerated inference nodes. Tiers 6--8 (NETWORK_GATEWAY, STORAGE_NODE, BUDGET) cover routers, NAS devices, and lightweight nodes. Tier 9 (HANDHELD) captures volunteer-owned gaming devices. Tiers 10--12 classify emerging RISC-V and FPGA platforms. Tier 13 (CLOUD_BURST) handles ephemeral spot instances. Tiers 14--15 house exotic accelerators and excluded legacy hardware [^8^].

Five trust levels overlay these tiers. **TRUSTED** devices (x86 desktops, open RISC-V, OpenPOWER) run with full privileges. **SEMI_TRUSTED** devices (ARM SBCs, Jetson, servers) use standard Docker isolation. **EDGE** devices (routers, NAS, smart TVs) execute in sandboxed environments. **UNTRUSTED** volunteer handhelds and cloud spots require gVisor or Kata Containers. **RESEARCH** devices run only under VM-level isolation [^8^].

The master table assigns every platform a tier, trust level, compute class, and Linux readiness score. Of 64 devices, 48 (75%) achieve production-ready Linux support; 9 (14%) require experimental kernels; 7 (11%), including Xbox Series X/S and Apple Watch, are formally excluded due to absent Linux pathways or closed ecosystems [^8^].

## Chapter-by-Chapter Summary

**Chapter 1: Gaming & Handheld Computing.** The Steam Deck is the highest-impact handheld node: 1.6 TFLOPS RDNA 2 GPU, $279 refurbished, zero-friction SteamOS compatibility [^1^]. The ROG Ally (8.6 TFLOPS, $999) and GPD Win 4 (11.88 TFLOPS) deliver superior per-device performance but smaller installed bases. Nintendo Switch requires early-hardware CFW; Switch 2 awaits a 6--18 month homebrew timeline. Xbox is excluded due to sandboxed dev mode with no Linux path. Power-aware scheduling enables the "Volunteer GPU Tier" model [^1^].

**Chapter 2: Advanced ARM SBCs & Developer Boards.** The Jetson Orin Nano Super at $249/67 TOPS is the premier AI inference edge node, outperforming discrete GPUs per-watt [^2^]. The RK3588 ecosystem delivers the best ARM cluster hardware: NanoPi R6S ($139, dual 2.5GbE) as gateway, Turing RK1 (32 cores, ~$800) for density [^2^]. Build recipes span $500 (4x ROCK 5B), $1,000 (Orin Nano Super AI build), and $2,000 (Turing RK1 density) [^2^].

**Chapter 3: RISC-V & Emerging Architectures.** Docker v29 ships for RISC-V within days of x86/ARM, with Go and Rust achieving parity---yet the Milk-V Pioneer (64 cores, $1,199) delivers roughly one-tenth the throughput of a mid-range ARM server [^3^]. SiFive P550 offers best single-core RISC-V (GB6: 136); Milk-V Jupiter introduces first RVV 1.0 board. LoongArch 3A6000 (~$300) and OpenPOWER Talos II are viable complements. Verdict: RISC-V is insurance against lock-in, not a performance play [^3^].

**Chapter 4: FPGA & Programmable Logic Compute.** The DE10-Nano ($190, dual Cortex-A9 + 110K logic elements) is the best-value Linux-capable FPGA node [^4^]. The Colorlight 5A-75B ($15) runs Linux via soft-core RISC-V with dual GbE, making it the cheapest cluster entry point. Three integration paths are defined: hard-processor SoCs, soft-core RISC-V (VexRiscv, Rocket Chip), and hybrid acceleration. Open-source toolchains (Yosys, nextpnr, LiteX) enable proprietary-free SoC construction [^4^].

**Chapter 5: Enterprise, Server & Cloud Nodes.** The 2025--2026 used server market is unprecedented: hyperscaler AI refreshes flood secondary markets with EPYC and Xeon hardware at fractions of original cost [^5^]. EPYC 7551 (~$2.10/core) and EPYC 7742 (64-core builds under $1,100) lead on price-per-core. Ampere Altra Q80-30 ($800--1,200 used, 80 cores) and Altra Max M128-30 ($1,200--2,000, 128 cores) provide ARM density at half x86 power. The Minisforum MS-01 ($679, dual 10GbE SFP+) is the standout mini PC node. Cloud spot extends on-prem clusters at $0.007--0.012/vCPU/hour via Go preemption handlers and WireGuard mesh [^5^].

**Chapter 6: IoT, Smart Home & Edge Devices.** The GL.iNet MT6000 ($159, quad-core A53, 8 GB eMMC, Docker, dual 2.5GbE, 900 Mbps WireGuard) is the single most cost-effective edge node in Phase 5 [^6^]. Synology DS923+ and QNAP TS-464 serve as Docker-capable storage nodes. LG webOS and NVIDIA Shield TV Pro are the most viable smart TV compute donors. Apple Watch, Echo, and HomePod are excluded due to closed ecosystems preventing background services [^6^].

**Chapter 7: Exotic & Future Technologies.** Groq's LPU achieves 300--500 tok/sec on Llama 2 70B (10x H100) via 150 TB/s on-chip SRAM, but NVIDIA's December 2025 IP acquisition creates vendor uncertainty [^7^]. Cerebras CS-3 ($2--3M, 125 petaFLOPS FP16) targets ultra-large model inference. Quantum computing, neuromorphic chips, and photonic processors are assessed as not cluster-relevant before 2029. Bitcoin ASICs are excluded due to inability to execute general-purpose computation [^7^].

**Chapter 8: Universal Integration Layer & Taxonomy.** Automatic device discovery probes CPU, GPU, RAM, storage, and network to assign tier and trust without manual configuration [^8^]. A Go-based engine compiles capability descriptors for scheduler matchmaking. Five cluster build recipes are specified: $250 edge, $500 AI starter, $1,000 home lab, $2,000 ARM density, and $5,000+ production [^8^].

**Chapter 9: Implementation Roadmap.** Phase 5 executes across four 6-week sub-phases over 24 weeks: 5a (handhelds/SBCs, weeks 1--6), 5b (RISC-V/FPGA, weeks 7--12), 5c (enterprise/IoT, weeks 13--18), and 5d (exotic technology, weeks 19--24) [^9^].

## Strategic Impact

Phase 5 transforms HelixCluster from a PC-and-edge cluster into a **universal compute fabric**. The strategic implications are threefold.

**Economic:** The volunteer compute model becomes viable at scale. A 200,000-node Steam Deck pool---achievable at 2--5% opt-in rates---delivers 320 petaFLOPS of aggregate GPU performance on donated idle cycles [^1^]. Combined with used EPYC at $2.10/core and cloud spot at $0.007/vCPU/hour, production-grade clusters assemble at one-tenth to one-hundredth the cost of equivalent cloud capacity.

**Technical:** Architecture lock-in ceases to exist. RISC-V achieves Docker parity, FPGAs provide reconfigurable compute, ARM servers match x86 price-performance, and exotic accelerators handle specialized inference. Workloads migrate across architectures based on efficiency, not binary compatibility [^3^] [^4^] [^7^]. The universal integration layer abstracts all differences into capability descriptors consumed by the scheduler's matchmaking protocol.

**Operational:** Edge and core unify under one control plane. A $159 GL.iNet router participates in the same WireGuard mesh and reports to the same scheduler as a $3,000 Ampere Altra server [^5^] [^6^]. The 15-tier system enforces trust boundaries and isolation levels automatically based on device capability---not operator configuration. The result is a cluster extending from a $15 FPGA to a $2.3 million wafer-scale engine, managed through a single binary and a single authentication flow [^8^].

HelixCluster Phase 5 demonstrates that distributed computing need not discriminate by device pedigree. The only requirement is Linux capability---and that list now numbers sixty-four and growing.

---

## References

[^1^]: HelixCluster Phase 5, Section 1: Gaming & Handheld Computing Devices. Steam Deck architecture, x86 handheld comparison, Nintendo evaluation, power-aware scheduling, and Volunteer GPU Tier framework.

[^2^]: HelixCluster Phase 5, Section 2: Advanced ARM SBCs & Developer Boards. Jetson Orin Nano Super, RK3588 ecosystem, Turing RK1, and cluster build recipes.

[^3^]: HelixCluster Phase 5, Section 3: RISC-V & Emerging Architectures. RISC-V board ecosystem, Docker readiness, Milk-V Pioneer benchmarks, LoongArch, and cross-compilation pipeline.

[^4^]: HelixCluster Phase 5, Section 4: FPGA & Programmable Logic Compute. DE10-Nano, Colorlight, soft-core RISC-V, open-source toolchain, and FPGA cluster integration.

[^5^]: HelixCluster Phase 5, Section 5: Enterprise, Server & Cloud Nodes. Used EPYC/Ampere Altra markets, MS-01 mini PC, cloud spot pricing, and hybrid cloud WireGuard mesh.

[^6^]: HelixCluster Phase 5, Section 6: IoT, Smart Home & Edge Devices. MT6000 edge node, NAS storage nodes, smart TV compute, and wearable exclusion rationale.

[^7^]: HelixCluster Phase 5, Section 7: Exotic & Future Technologies. Groq LPU, Cerebras, quantum/neuromorphic timelines, and Bitcoin ASIC exclusion.

[^8^]: HelixCluster Phase 5 Architecture Document. 64-device taxonomy, 15-tier system, 5 trust levels, Go discovery engine, and cluster build recipes.

[^9^]: HelixCluster Phase 5, Section 9: Implementation Roadmap. 24-week execution plan across 4 sub-phases with week-level deliverables.
