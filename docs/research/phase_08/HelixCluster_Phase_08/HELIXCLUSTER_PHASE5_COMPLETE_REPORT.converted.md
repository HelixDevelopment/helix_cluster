# HelixCluster Phase 5 — Advanced & Exotic Device Ecosystem: Complete Report

**Version:** 1.0  
**Date:** 2026-05-31  
**Status:** Final Report  
**Scope:** 64 device types across 7 categories, 15 tiers

---

# Executive Summary

HelixCluster now supports virtually every Linux-capable device on Earth. Phase 5 expands the ecosystem from 12 to **64 device types** across **15 classification tiers** and **5 trust levels**, establishing a universal integration layer that enables any Linux-capable device---from a $15 FPGA to a $2.3 million Cerebras wafer-scale engine---to participate as a first-class cluster node ^1^.

## Key Metrics

| Metric | Value | Significance |
|--------|-------|-------------|
| **Total device types** | 64 (up from 12) | 5.3x hardware expansion ^2^|
| **Classification tiers** | 15 (up from 5) | Granular trust/compute classification ^1^|
| **Architecture coverage** | x86, ARM, RISC-V, FPGA, POWER, LoongArch | Eliminates architecture lock-in ^3^|
| **Minimum node price** | $15 (Colorlight FPGA) | Sub-$20 entry point for experimental clusters ^4^|
| **Best handheld compute/$** | Steam Deck at $0.17/GFLOPS | 4M+ unit installed base, native Linux ^2^|
| **Best server price/core** | Used EPYC 7551 at ~$2.10/core | 64-core builds under $1,100 ^5^|
| **Best edge/gateway node** | GL.iNet MT6000 at $159 | Docker + dual 2.5GbE + quad-core A53 ^6^|
| **Best AI inference/$** | Jetson Orin Nano Super at $3.72/TOPS | 67 TOPS at $249 via free firmware upgrade ^7^|
| **Highest-density ARM node** | Turing RK1: 32 cores, ~$800 | 4x RK3588 modules on single carrier ^7^|
| **Volunteer GPU pool potential** | 320 petaFLOPS (200K Steam Decks) | Mid-size supercomputer on donated idle cycles ^2^|
| **Cloud spot floor price** | $0.007/vCPU/hour (AWS Graviton3) | 70--90% below on-demand ^5^|
| **Max single-node AI perf** | 275 TOPS (Jetson AGX Orin) | Edge inference density rivaling cloud GPUs ^7^|

## The 64-Device Taxonomy

Phase 5 introduces a **15-tier classification system** mapping device capability, openness, and trust requirements. Tiers 1--3 (CORE_TRUSTED through EDGE_COMPUTE) cover servers, mini PCs, and desktop-class hardware for control plane and containerized workloads. Tiers 4--5 (AI_WORKER, AI_CONTROLLER) designate GPU/NPU-accelerated inference nodes. Tiers 6--8 (NETWORK_GATEWAY, STORAGE_NODE, BUDGET) cover routers, NAS devices, and lightweight nodes. Tier 9 (HANDHELD) captures volunteer-owned gaming devices. Tiers 10--12 classify emerging RISC-V and FPGA platforms. Tier 13 (CLOUD_BURST) handles ephemeral spot instances. Tiers 14--15 house exotic accelerators and excluded legacy hardware ^1^.

Five trust levels overlay these tiers. **TRUSTED** devices (x86 desktops, open RISC-V, OpenPOWER) run with full privileges. **SEMI_TRUSTED** devices (ARM SBCs, Jetson, servers) use standard Docker isolation. **EDGE** devices (routers, NAS, smart TVs) execute in sandboxed environments. **UNTRUSTED** volunteer handhelds and cloud spots require gVisor or Kata Containers. **RESEARCH** devices run only under VM-level isolation ^1^.

The master table assigns every platform a tier, trust level, compute class, and Linux readiness score. Of 64 devices, 48 (75%) achieve production-ready Linux support; 9 (14%) require experimental kernels; 7 (11%), including Xbox Series X/S and Apple Watch, are formally excluded due to absent Linux pathways or closed ecosystems ^1^.

## Chapter-by-Chapter Summary

**Chapter 1: Gaming & Handheld Computing.** The Steam Deck is the highest-impact handheld node: 1.6 TFLOPS RDNA 2 GPU, $279 refurbished, zero-friction SteamOS compatibility ^2^. The ROG Ally (8.6 TFLOPS, $999) and GPD Win 4 (11.88 TFLOPS) deliver superior per-device performance but smaller installed bases. Nintendo Switch requires early-hardware CFW; Switch 2 awaits a 6--18 month homebrew timeline. Xbox is excluded due to sandboxed dev mode with no Linux path. Power-aware scheduling enables the "Volunteer GPU Tier" model ^2^.

**Chapter 2: Advanced ARM SBCs & Developer Boards.** The Jetson Orin Nano Super at $249/67 TOPS is the premier AI inference edge node, outperforming discrete GPUs per-watt ^7^. The RK3588 ecosystem delivers the best ARM cluster hardware: NanoPi R6S ($139, dual 2.5GbE) as gateway, Turing RK1 (32 cores, ~$800) for density ^7^. Build recipes span $500 (4x ROCK 5B), $1,000 (Orin Nano Super AI build), and $2,000 (Turing RK1 density) ^7^.

**Chapter 3: RISC-V & Emerging Architectures.** Docker v29 ships for RISC-V within days of x86/ARM, with Go and Rust achieving parity---yet the Milk-V Pioneer (64 cores, $1,199) delivers roughly one-tenth the throughput of a mid-range ARM server ^3^. SiFive P550 offers best single-core RISC-V (GB6: 136); Milk-V Jupiter introduces first RVV 1.0 board. LoongArch 3A6000 (~$300) and OpenPOWER Talos II are viable complements. Verdict: RISC-V is insurance against lock-in, not a performance play ^3^.

**Chapter 4: FPGA & Programmable Logic Compute.** The DE10-Nano ($190, dual Cortex-A9 + 110K logic elements) is the best-value Linux-capable FPGA node ^4^. The Colorlight 5A-75B ($15) runs Linux via soft-core RISC-V with dual GbE, making it the cheapest cluster entry point. Three integration paths are defined: hard-processor SoCs, soft-core RISC-V (VexRiscv, Rocket Chip), and hybrid acceleration. Open-source toolchains (Yosys, nextpnr, LiteX) enable proprietary-free SoC construction ^4^.

**Chapter 5: Enterprise, Server & Cloud Nodes.** The 2025--2026 used server market is unprecedented: hyperscaler AI refreshes flood secondary markets with EPYC and Xeon hardware at fractions of original cost ^5^. EPYC 7551 (~$2.10/core) and EPYC 7742 (64-core builds under $1,100) lead on price-per-core. Ampere Altra Q80-30 ($800--1,200 used, 80 cores) and Altra Max M128-30 ($1,200--2,000, 128 cores) provide ARM density at half x86 power. The Minisforum MS-01 ($679, dual 10GbE SFP+) is the standout mini PC node. Cloud spot extends on-prem clusters at $0.007--0.012/vCPU/hour via Go preemption handlers and WireGuard mesh ^5^.

**Chapter 6: IoT, Smart Home & Edge Devices.** The GL.iNet MT6000 ($159, quad-core A53, 8 GB eMMC, Docker, dual 2.5GbE, 900 Mbps WireGuard) is the single most cost-effective edge node in Phase 5 ^6^. Synology DS923+ and QNAP TS-464 serve as Docker-capable storage nodes. LG webOS and NVIDIA Shield TV Pro are the most viable smart TV compute donors. Apple Watch, Echo, and HomePod are excluded due to closed ecosystems preventing background services ^6^.

**Chapter 7: Exotic & Future Technologies.** Groq's LPU achieves 300--500 tok/sec on Llama 2 70B (10x H100) via 150 TB/s on-chip SRAM, but NVIDIA's December 2025 IP acquisition creates vendor uncertainty ^8^. Cerebras CS-3 ($2--3M, 125 petaFLOPS FP16) targets ultra-large model inference. Quantum computing, neuromorphic chips, and photonic processors are assessed as not cluster-relevant before 2029. Bitcoin ASICs are excluded due to inability to execute general-purpose computation ^8^.

**Chapter 8: Universal Integration Layer & Taxonomy.** Automatic device discovery probes CPU, GPU, RAM, storage, and network to assign tier and trust without manual configuration ^1^. A Go-based engine compiles capability descriptors for scheduler matchmaking. Five cluster build recipes are specified: $250 edge, $500 AI starter, $1,000 home lab, $2,000 ARM density, and $5,000+ production ^1^.

**Chapter 9: Implementation Roadmap.** Phase 5 executes across four 6-week sub-phases over 24 weeks: 5a (handhelds/SBCs, weeks 1--6), 5b (RISC-V/FPGA, weeks 7--12), 5c (enterprise/IoT, weeks 13--18), and 5d (exotic technology, weeks 19--24) ^9^.

## Strategic Impact

Phase 5 transforms HelixCluster from a PC-and-edge cluster into a **universal compute fabric**. The strategic implications are threefold.

**Economic:** The volunteer compute model becomes viable at scale. A 200,000-node Steam Deck pool---achievable at 2--5% opt-in rates---delivers 320 petaFLOPS of aggregate GPU performance on donated idle cycles ^2^. Combined with used EPYC at $2.10/core and cloud spot at $0.007/vCPU/hour, production-grade clusters assemble at one-tenth to one-hundredth the cost of equivalent cloud capacity.

**Technical:** Architecture lock-in ceases to exist. RISC-V achieves Docker parity, FPGAs provide reconfigurable compute, ARM servers match x86 price-performance, and exotic accelerators handle specialized inference. Workloads migrate across architectures based on efficiency, not binary compatibility ^3^ ^4^ ^8^. The universal integration layer abstracts all differences into capability descriptors consumed by the scheduler's matchmaking protocol.

**Operational:** Edge and core unify under one control plane. A $159 GL.iNet router participates in the same WireGuard mesh and reports to the same scheduler as a $3,000 Ampere Altra server ^5^ ^6^. The 15-tier system enforces trust boundaries and isolation levels automatically based on device capability---not operator configuration. The result is a cluster extending from a $15 FPGA to a $2.3 million wafer-scale engine, managed through a single binary and a single authentication flow ^1^.

HelixCluster Phase 5 demonstrates that distributed computing need not discriminate by device pedigree. The only requirement is Linux capability---and that list now numbers sixty-four and growing.

---

## References

^2^: HelixCluster Phase 5, Section 1: Gaming & Handheld Computing Devices. Steam Deck architecture, x86 handheld comparison, Nintendo evaluation, power-aware scheduling, and Volunteer GPU Tier framework.

^7^: HelixCluster Phase 5, Section 2: Advanced ARM SBCs & Developer Boards. Jetson Orin Nano Super, RK3588 ecosystem, Turing RK1, and cluster build recipes.

^3^: HelixCluster Phase 5, Section 3: RISC-V & Emerging Architectures. RISC-V board ecosystem, Docker readiness, Milk-V Pioneer benchmarks, LoongArch, and cross-compilation pipeline.

^4^: HelixCluster Phase 5, Section 4: FPGA & Programmable Logic Compute. DE10-Nano, Colorlight, soft-core RISC-V, open-source toolchain, and FPGA cluster integration.

^5^: HelixCluster Phase 5, Section 5: Enterprise, Server & Cloud Nodes. Used EPYC/Ampere Altra markets, MS-01 mini PC, cloud spot pricing, and hybrid cloud WireGuard mesh.

^6^: HelixCluster Phase 5, Section 6: IoT, Smart Home & Edge Devices. MT6000 edge node, NAS storage nodes, smart TV compute, and wearable exclusion rationale.

^8^: HelixCluster Phase 5, Section 7: Exotic & Future Technologies. Groq LPU, Cerebras, quantum/neuromorphic timelines, and Bitcoin ASIC exclusion.

^1^: HelixCluster Phase 5 Architecture Document. 64-device taxonomy, 15-tier system, 5 trust levels, Go discovery engine, and cluster build recipes.

^9^: HelixCluster Phase 5, Section 9: Implementation Roadmap. 24-week execution plan across 4 sub-phases with week-level deliverables.


---

# 1. Gaming & Handheld Computing Devices

The gaming handheld market represents one of the most compelling untapped reservoirs of distributed compute capacity for HelixCluster. With over four million Steam Deck units shipped, 155 million Nintendo Switches sold, and the PC handheld segment forecasted to reach 4.7 million annual units by 2029, these devices collectively represent a latent compute pool that rivals many commercial cloud regions. What makes this category uniquely valuable is not merely the aggregate FLOPS, but the intersection of capable hardware, functional Linux support, and usage patterns that leave devices idle for significant portions of each day. A Steam Deck purchased for $279 (refurbished) delivers 1.6 TFLOPS of RDNA 2 GPU compute and 448 GFLOPS of Zen 2 CPU performance in a 4–15 watt envelope, while its owner sleeps, works, or engages in other activities. This chapter examines every major gaming and handheld computing platform for HelixCluster viability, establishes a clear prioritization framework, and presents the integration architecture for what we designate the "Volunteer GPU Tier."

## 1.1 Steam Deck & Steam Deck OLED

### 1.1.1 Hardware: AMD Custom APU Architecture

The Steam Deck and its OLED successor share a common architectural foundation built around AMD custom APUs. The original LCD model uses the 7nm "Aerith" silicon, while the OLED refresh moves to the more efficient 6nm "Sephiroth" die. Both implement a quad-core, eight-thread Zen 2 CPU complex running at 2.4–3.5 GHz alongside eight RDNA 2 compute units operating at up to 1.6 GHz. This configuration yields approximately 448 GFLOPS FP32 from the CPU and 1.6 TFLOPS FP32 from the GPU, placing the Steam Deck's graphics capability in the same neighborhood as a desktop Radeon RX 550 or GTX 1050 Ti. The sixteen gigabytes of LPDDR5 memory (upgraded from 5500 MT/s on the LCD to 6400 MT/s on the OLED) operates as a unified memory architecture shared between CPU and GPU, a critical advantage for machine learning inference where model size frequently exceeds discrete GPU VRAM.

The thermal design power spans 4W to 15W, adjustable through SteamOS settings or direct power play table manipulation. At 4W, the device achieves extraordinary efficiency suitable for background CPU tasks; at 15W docked mode, it sustains full GPU clocks for compute-intensive workloads. Storage options range from 64GB eMMC on the base LCD model to 1TB NVMe on the premium OLED variant, with all models supporting microSD expansion. The OLED model additionally upgrades networking from Wi-Fi 5 to tri-band Wi-Fi 6E, a meaningful improvement for distributed workloads that depend on network throughput for task distribution and result upload.

### 1.1.2 SteamOS 3.0: Native Linux as First-Class Citizen

The Steam Deck's defining characteristic for HelixCluster purposes is not its silicon but its operating system. SteamOS 3.0 and later versions are derived from Arch Linux, running a modern kernel with Mesa RADV drivers providing first-class Vulkan 1.3+ support, OpenCL 3.0 via Mesa Rusticl, and community-validated ROCm compatibility through environment variable overrides. The desktop mode, accessible by switching from the Steam UI to a full KDE Plasma environment, exposes the complete Linux userspace: systemd, pacman, Docker, Flatpak, and all standard tooling expected on a contemporary Linux workstation.

This is not a hack, a jailbreak, or a vendor-tolerated modification. It is a supported, documented, and actively maintained feature of the platform. Valve employs Linux kernel developers who upstream driver improvements, contributes to Mesa and AMD GPU driver development, and has publicly committed to the openness of the Steam Deck ecosystem. For HelixCluster, this means zero engineering effort is required to establish a functional Linux environment. The agent installs through standard package management, container images pull from standard registries, and GPU compute workloads execute through standard APIs without vendor-specific SDKs or proprietary driver stacks.

### 1.1.3 GPU Compute via ROCm, Vulkan Compute, and OpenCL

The Steam Deck's GPU compute stack operates at three levels of capability and reliability. Vulkan compute shaders, exposed through the Mesa RADV driver, represent the primary and most stable path. This is the backend used by llama.cpp's Vulkan implementation, which achieves an estimated 8–12 tokens per second on 7B parameter models at Q4_0 quantization and 60–80 tokens per second in prompt processing. Vulkan requires no driver modifications, no environment workarounds, and no vendor-specific tooling. It works out of the box on every Steam Deck ever manufactured.

OpenCL 3.0 support arrives through Mesa Rusticl, an open-source OpenCL implementation that has been steadily improving its AMD GPU coverage. While functional for many compute workloads, Rusticl remains less mature than RADV's Vulkan path and may exhibit compatibility gaps with legacy OpenCL code written against proprietary drivers.

ROCm and HIP represent the third path, offering the broadest framework compatibility at the cost of unofficial status. The Steam Deck's RDNA 2 iGPU is not on AMD's officially supported hardware list for ROCm, but community workarounds using `HSA_OVERRIDE_GFX_VERSION=10.3.0` successfully trick the ROCm runtime into treating the integrated GPU as a discrete RDNA 2 card. This enables PyTorch HIP backend execution, rocBLAS matrix operations, and other ROCm-dependent frameworks. The limitation is stability: some ROCm versions crash during iGPU memory allocation, and the workaround may break with ROCm updates. For production HelixCluster workloads, Vulkan compute remains the recommended API; ROCm is offered as an optional secondary path with documented caveats.

### 1.1.4 Market Position: Highest-Impact Handheld for HelixCluster

With over four million units in circulation and a refurbished LCD entry price of $279, the Steam Deck delivers GPU compute at approximately $0.17 per GFLOPS — the best price-to-performance ratio among all handheld devices and competitive with many dedicated single-board computers. At 4–15W, a fleet of one hundred Steam Decks consumes less aggregate power than a single EPYC server while delivering 160 TFLOPS of GPU compute and 44.8 TFLOPS of CPU compute. The 16GB unified memory enables GPU inference on larger models than discrete mobile GPUs typically permit, and the built-in battery, display, and controls mean the device functions as a fully standalone node that can be deployed anywhere without peripheral dependencies.

The OLED model, despite its superior efficiency and Wi-Fi 6E networking, became significantly less attractive for dedicated procurement following a 43–46% price increase in May 2026. At $789–949, new OLED units compete with far more powerful alternatives. However, for the volunteer compute donor model — existing owners contributing idle cycles — both LCD and OLED models are equally viable. The primary HelixCluster value proposition for Steam Deck is not hardware procurement but zero-incremental-cost compute donation from a four-million-unit installed base.

The following table summarizes the key differences between LCD and OLED variants for cluster deployment purposes:

| Specification | Steam Deck LCD (Refurbished) | Steam Deck OLED (New) |
|---|---|---|
| **APU Process** | 7nm (Aerith) | 6nm (Sephiroth) |
| **CPU FLOPS** | ~448 GFLOPS FP32 | ~448 GFLOPS FP32 |
| **GPU FLOPS** | 1.6 TFLOPS FP32 | 1.6 TFLOPS FP32 |
| **RAM / Speed** | 16 GB LPDDR5-5500 | 16 GB LPDDR5-6400 |
| **RAM Bandwidth** | ~88 GB/s | ~102 GB/s |
| **Storage** | 64GB–512GB NVMe/eMMC | 512GB–1TB NVMe |
| **Wi-Fi** | Wi-Fi 5 (802.11ac) | Wi-Fi 6E (tri-band) |
| **Battery** | 40 Wh | 50 Wh |
| **TDP Range** | 4–15W | 4–15W |
| **Price** | $279–359 | $789–949 |
| **Cluster Suitability** | Excellent value | Diminished by price hike |

Realistic node estimates based on distributed computing opt-in rates (typically 2–5% for projects like BOINC or Folding@Home) suggest HelixCluster could attract 80,000–200,000 Steam Deck nodes, 10,000–25,000 x86 handheld nodes, and at most 1,000–5,000 Nintendo Switch hobbyist nodes in the near term. These figures, while conservative, represent a substantial compute pool: 200,000 Steam Decks alone deliver 320 petaFLOPS of aggregate GPU performance, equivalent to a mid-size supercomputing installation, operating entirely on donated idle cycles from volunteer device owners.

## 1.2 x86 Handhelds: ROG Ally, Legion Go, GPD Win, Ayaneo

### 1.2.1 AMD Z1 Extreme and Ryzen Z1: RDNA 3 Performance Leadership

The x86 handheld market extends well beyond the Steam Deck, and several competitors deliver substantially higher raw compute. The ASUS ROG Ally and Ally X, the Lenovo Legion Go, the GPD Win 4 and Win Mini, and various Ayaneo models all employ AMD's Ryzen Z1 Extreme or related Zen 4-based APUs with RDNA 3 graphics. The Z1 Extreme offers eight Zen 4 cores running at up to 5.1 GHz and twelve RDNA 3 compute units at 2.7 GHz, yielding 8.6 TFLOPS FP32 — more than five times the Steam Deck's GPU throughput. The ROG Ally X further extends this platform with 24GB of LPDDR5 memory (versus 16GB on the Steam Deck) and an 80Wh battery double the capacity of the original Ally.

The GPD Win 4 (2025) pushes even further with the Ryzen AI 9 HX 370 and its Radeon 890M integrated GPU, delivering up to 11.88 TFLOPS from sixteen RDNA 3.5 compute units alongside twelve Zen 5 CPU cores and 32GB of LPDDR5x memory. This is genuine desktop-replacement compute in a handheld form factor, complete with a physical keyboard, OCuLink external GPU expansion port, and full UEFI-based Linux compatibility.

The cumulative PC handheld installed base reached approximately 7.9 million units by the end of 2025, growing at 32% year over year. While the Steam Deck constitutes roughly half of this total, the x86 handheld segment collectively represents a rapidly expanding pool of high-performance volunteer compute donors. Each ROG Ally or GPD Win owner who installs Bazzite effectively doubles or triples their per-device contribution relative to a Steam Deck donor.

### 1.2.2 Linux Compatibility: Bazzite and Native Installation

Unlike the Steam Deck, these x86 handhelds do not ship with Linux. They run Windows 11 by default, but all models offer full Linux installation capability through standard UEFI boot with Secure Boot disabled. The Bazzite distribution — a Fedora-based, SteamOS-like operating system optimized for handheld gaming — has emerged as the de facto standard for this hardware category, with community builds that in some benchmarks achieve up to 32% better gaming performance than Windows. For HelixCluster, Bazzite provides the same container runtime, systemd, and GPU compute stack as SteamOS, making agent deployment straightforward across all x86 handhelds.

Ubuntu 23.04 and later versions boot out of the box on most models, with only occasional Wi-Fi driver issues that community kernels resolve. The standard Mesa RADV stack provides identical Vulkan and OpenCL support to the Steam Deck, while ROCm compatibility uses the `HSA_OVERRIDE_GFX_VERSION=11.0.0` override for RDNA 3. From a HelixCluster agent perspective, there is no meaningful difference in software environment between a Steam Deck running SteamOS and an ROG Ally running Bazzite.

### 1.2.3 Price/Performance Comparison

The following table compares the Steam Deck, the leading x86 handhelds, and the Orange Pi 5 Max reference platform across the metrics most relevant to HelixCluster deployment:

| Specification | Steam Deck (LCD refurb) | ROG Ally X | GPD Win 4 (2025) | Orange Pi 5 Max |
|---|---|---|---|---|
| **Price** | $279 | ~$999 | ~$1,100 | $125 (16GB) |
| **CPU** | Zen 2, 4c/8t | Zen 4, 8c/16t | Zen 5, 12c/24t | ARM A76, 8c |
| **CPU FLOPS** | ~448 GFLOPS | ~2,000+ GFLOPS | ~3,000+ GFLOPS | ~500 GFLOPS |
| **GPU** | 8 CU RDNA 2 | 12 CU RDNA 3 | 16 CU RDNA 3.5 | Mali-G610 MP4 |
| **GPU FLOPS** | 1.6 TFLOPS | 8.6 TFLOPS | 11.9 TFLOPS | ~0.5 TFLOPS |
| **RAM** | 16 GB LPDDR5 | 24 GB LPDDR5 | 32 GB LPDDR5x | 16 GB LPDDR4X |
| **RAM Bandwidth** | ~88 GB/s | ~88 GB/s | ~120 GB/s | ~34 GB/s |
| **TDP Range** | 4–15W | 9–30W | 20–35W | 5–10W |
| **Linux Support** | Native (SteamOS) | Full (Bazzite/Ubuntu) | Full (Bazzite/Ubuntu) | Full (Armbian) |
| **GPU Compute APIs** | Vulkan, OpenCL, ROCm | Vulkan, OpenCL, ROCm | Vulkan, OpenCL, ROCm | OpenCL, Vulkan |
| **$/GFLOPS (GPU)** | $0.17 | $0.09 | $0.09 | $0.25 |
| **Best Use Case** | Volunteer GPU donor | High-performance donor | Premium workstation node | Headless edge/ethernet |

The ROG Ally X and GPD Win 4 deliver superior raw compute per dollar at $0.09 per GFLOPS, but their higher acquisition cost limits volunteer adoption. The Steam Deck's $279 refurbished entry point, combined with its four-million-unit installed base and zero-friction Linux environment, makes it the highest-impact target despite lower per-device performance. The Orange Pi 5 Max remains competitive for headless CPU-only or ethernet-dependent workloads where its 2.5GbE port and sub-10W power envelope offset the weaker GPU ecosystem.

## 1.3 Nintendo Consoles

### 1.3.1 Original Switch: Tegra X1 with Homebrew Linux

The Nintendo Switch, with 155 million units sold, represents an enormous theoretical compute pool. The Tegra X1 SoC inside the original model provides four ARM Cortex-A57 cores and a 256-core Maxwell GPU delivering approximately 393 GFLOPS FP32 in docked mode. However, the practical HelixCluster viability of the original Switch is severely constrained. Running Linux requires the Atmosphere custom firmware, which in turn depends on an unpatchable bootROM exploit present only in early production units or a hardware modchip for later revisions. Nintendo actively bans modified consoles from online services, and the 4GB of RAM, absence of CUDA support, and reliance on Vulkan compute shaders alone limit practical workload compatibility to trivial proof-of-concept demonstrations.

Community efforts through the switchroot project maintain Ubuntu 24.04 LTS images for vulnerable hardware, but the ecosystem remains a hobbyist endeavor. For HelixCluster, the original Switch is classified as Tier 4 experimental at best, suitable only for research into ARM64 edge workloads and not for any production deployment.

### 1.3.2 Switch 2: Ampere GPU and the Homebrew Timeline

The Nintendo Switch 2, launched in 2025 with the custom NVIDIA T239 "Drake" SoC, dramatically improves the hardware proposition: eight Cortex-A78C cores, 1536 Ampere CUDA cores, 12GB of LPDDR5 memory with 102 GB/s bandwidth, and a docked GPU performance estimate of approximately 3.1 TFLOPS FP32. This is ten times the original Switch's compute and roughly double the Steam Deck's GPU throughput. The architectural leap from Maxwell to Ampere, combined with substantially more memory and the potential for CUDA support through NVIDIA's Linux for Tegra (L4T) distribution, makes the Switch 2 a theoretically high-value HelixCluster node.

The critical unknown is the homebrew timeline. As of mid-2025, no public jailbreak or kernel exploit exists. Nintendo learned from the Tegra X1's unpatchable RCM vulnerability and has likely implemented significantly enhanced boot chain security on the T239. Historical patterns suggest a 6–18 month window before a softmod or modchip solution emerges, but this is an estimate, not a guarantee. The microSD Express slot exposes a true PCIe Gen3 x1 NVMe link, which modders have already leveraged for storage expansion, but the system firmware remains uncompromised.

HelixCluster should monitor the Switch 2 homebrew scene on a quarterly basis. If and when Linux becomes bootable, the Switch 2 could deliver 3+ TFLOPS of Ampere GPU compute with CUDA compatibility in a portable, low-power device — a genuinely valuable addition to the cluster's ARM64 compute tier. Until then, it remains on the watch list with zero engineering investment.

## 1.4 Xbox and Other Gaming Platforms

### 1.4.1 Xbox Series X/S: Excluded Due to Sandboxed Dev Mode

The Xbox Series X presents a frustrating paradox for HelixCluster. Its hardware is genuinely impressive: an eight-core Zen 2 CPU at 3.8 GHz, 52 RDNA 2 compute units delivering 12.15 TFLOPS FP32, and 16GB of GDDR6 with up to 560 GB/s memory bandwidth. A jailbroken Series X running Linux would be an outstanding compute node, competitive with mid-range gaming PCs. However, no such jailbreak exists, and Microsoft's security has held across every Xbox generation.

The sanctioned Developer Mode offers only a sandboxed UWP environment with crippling restrictions: applications are limited to 1GB of RAM (5GB for games), 2–4 shared CPU cores, up to 45% GPU access, DirectX 11 only for applications, and no arbitrary code execution outside the UWP container. A $19 Partner Center account enables Dev Mode, but Microsoft can revoke access at any time. There is no path to native Linux, no container runtime, and no GPU compute API access suitable for distributed workloads.

For these reasons, all Xbox platforms — Series X, Series S, One X, and original One — are formally excluded from HelixCluster consideration. No engineering resources should be allocated to Xbox support. If a future jailbreak materializes, this assessment should be revisited immediately, but such an event is not predicted within the current planning horizon.

### 1.4.2 GPU Compute API Support Matrix

The diversity of GPU architectures across gaming devices creates a complex API compatibility landscape. The following table summarizes compute API availability across the platforms evaluated in this chapter:

| API / Platform | Steam Deck (RDNA 2) | ROG Ally (RDNA 3) | Switch (Maxwell) | Switch 2 (Ampere) | Xbox Series X (RDNA 2) |
|---|---|---|---|---|---|
| **Vulkan Compute** | Native (RADV) | Native (RADV) | Limited | Future potential | UWP-restricted |
| **OpenCL 3.0** | Mesa Rusticl | Mesa Rusticl | Not available | Future (L4T) | Not available |
| **ROCm/HIP** | Override required | Override required | N/A | N/A | Not available |
| **CUDA** | Not supported | Not supported | Not available | Potential (L4T) | Not available |
| **Native Linux** | Yes (SteamOS) | Yes (install) | Homebrew only | Pending jailbreak | No |
| **Workaround-free?** | Yes | Yes | No | No | N/A |
| **HelixCluster Tier** | Tier 2 (Compute) | Tier 2 (Compute) | Tier 4 (Exp.) | Tier 5 (Watch) | Unsupported |

The clear pattern emerging from this matrix is that Linux support and open driver stacks determine HelixCluster viability more strongly than raw FLOPS. The Steam Deck and ROG Ally achieve full compute API coverage through Mesa's open-source drivers, requiring at most an environment variable override for ROCm. The Switch platforms depend on homebrew maturity. The Xbox, despite possessing the most powerful GPU of the group, contributes zero useful compute due to its closed ecosystem.

## 1.5 Handheld Integration Architecture

### 1.5.1 Power-Aware Scheduling: Gaming-Aware Compute Management

The defining operational challenge for handheld compute nodes is that their primary purpose — gaming — must never be degraded by background cluster workloads. A Steam Deck owner contributing idle GPU cycles to HelixCluster must experience zero impact on frame rates, input latency, or battery life during gaming sessions. This requires a power-aware scheduling system that continuously monitors device state and adapts compute allocation in real time.

The scheduler operates on five distinct device state profiles. When the handheld is actively gaming, all GPU compute suspends immediately and CPU compute is restricted to low-priority background threads using no more than 25% of available cores. When docked with external power, the device may sustain full 15W TDP operation, dedicating both CPU and GPU to cluster workloads. On battery above 50% charge, a balanced profile permits 50% CPU and 50% GPU allocation at 10W TDP. Below 50% but above 20% charge, compute throttles to 25% of each processor. Below 20% battery, all cluster activity pauses to preserve remaining power for the device's primary function.

State detection relies on multiple signals: process name monitoring for known game executables, GPU utilization thresholds above 60% sustained for three seconds, Steam Deck-specific D-Bus signals from the Steam client, and external power presence detection. The transition from gaming to idle triggers within 500 milliseconds, releasing compute resources back to the cluster when the session ends. This responsiveness is essential for volunteer retention — a donor who experiences gaming interference will uninstall the agent permanently.

Beyond reactive state detection, the scheduler also implements predictive idle window estimation. By analyzing historical usage patterns, the agent learns a donor's typical gaming schedule and pre-stages workloads during anticipated idle periods. A donor who consistently plays for two hours each evening can have compute tasks queued and ready to execute the moment the game process terminates, maximizing utilization of each idle window without any perceptible startup delay.

### 1.5.2 Container Strategy: Distrobox and Toolbx for Isolated Agent Environments

HelixCluster agent deployment on handhelds uses a containerized approach that isolates cluster workloads from the host gaming environment while maintaining full GPU compute access. The recommended implementation uses Distrobox or Toolbx to create a mutable container layer atop the host's immutable or semi-immutable system (SteamOS, Bazzite), though standard Docker or Podman execution is equally viable on devices with standard filesystem layouts.

The container image is built from an Arch Linux base to match SteamOS and must include Mesa RADV drivers, Vulkan loader, and optional OpenCL and ROCm runtime components. The agent container requires privileged access to `/dev/dri` for GPU rendering, `/dev/kfd` for ROCm where applicable, and the host's network namespace for cluster communication. GPU memory is shared through the container boundary without overhead since AMD APUs use unified memory architecture.

```dockerfile
# HelixCluster Steam Deck Agent Container
FROM archlinux:latest

LABEL maintainer="helixcluster@example.org"
LABEL description="HelixCluster agent for Steam Deck and x86 handhelds"

# Core system dependencies
RUN pacman -Syu --noconfirm && \
    pacman -S --noconfirm \
        mesa vulkan-radeon vulkan-icd-loader \
        vulkan-tools clinfo \
        rocm-opencl-runtime rocm-hip-sdk \
        python python-pip docker \
        systemd dbus \
        wget curl git htop \
        && pacman -Scc --noconfirm

# ROCm workaround for RDNA 2 / RDNA 3 APU detection
ENV HSA_OVERRIDE_GFX_VERSION=10.3.0
ENV GGML_VULKAN=1

# HelixCluster agent binary and configuration
COPY helixcluster-agent /usr/local/bin/
COPY agent-config.yaml /etc/helixcluster/

# llama.cpp Vulkan backend for inference workloads
COPY --from=ghcr.io/ggml-org/llama.cpp:vulkan-latest \
    /app/llama-server /usr/local/bin/
COPY --from=ghcr.io/ggml-org/llama.cpp:vulkan-latest \
    /app/llama-cli /usr/local/bin/

# Health check endpoint
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD helixcluster-agent health || exit 1

EXPOSE 9090/tcp

ENTRYPOINT ["/usr/local/bin/helixcluster-agent"]
CMD ["--config", "/etc/helixcluster/agent-config.yaml"]
```

The corresponding systemd unit file for host-level orchestration manages container lifecycle, automatic restart on failure, and graceful shutdown on gaming activity detection:

```ini
# /etc/systemd/system/helixcluster-agent.service
[Unit]
Description=HelixCluster Compute Agent for Handheld
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
Restart=always
RestartSec=10

# Environment and GPU passthrough
Environment="HSA_OVERRIDE_GFX_VERSION=10.3.0"
Environment="GGML_VULKAN=1"
Environment="HELIX_TIER=edge"

ExecStartPre=-/usr/bin/docker rm -f helixcluster-agent
ExecStart=/usr/bin/docker run \
    --name helixcluster-agent \
    --device /dev/dri:/dev/dri \
    --device /dev/kfd:/dev/kfd \
    --group-add video \
    --group-add render \
    --network host \
    --pid host \
    -v /etc/helixcluster:/etc/helixcluster:ro \
    -v /var/lib/helixcluster:/var/lib/helixcluster \
    -e HSA_OVERRIDE_GFX_VERSION=10.3.0 \
    -e GGML_VULKAN=1 \
    -e HELIX_DEVICE_PROFILE=steamdeck \
    -e HELIX_POWER_AWARE=1 \
    ghcr.io/helixcluster/agent:handheld-latest

ExecStop=-/usr/bin/docker stop -t 30 helixcluster-agent
ExecStopPost=-/usr/bin/docker rm -f helixcluster-agent

[Install]
WantedBy=multi-user.target
```

The agent runtime integrates with the host's power management through D-Bus interfaces exposed by SteamOS and Bazzite. When the power daemon signals a transition from AC to battery power, or when Steam reports a game launch event, the agent receives a SIGUSR1 signal triggering a 500-millisecond checkpoint of in-memory state followed by voluntary container pause. On SIGUSR2 (game exit, AC reconnect, or explicit user resume), the container unpauses and resumes workload execution from the checkpointed state. This mechanism ensures that no task progress is lost during gaming interruptions and that compute donation remains entirely transparent to the device's owner.

For x86 handhelds running Bazzite rather than SteamOS, the same container and systemd configuration applies with only the `HELIX_DEVICE_PROFILE` environment variable changed to `bazzite-handheld`. The Bazzite distribution exposes identical D-Bus power signals and GPU device paths, making the agent fully portable across the x86 handheld ecosystem. For the Switch and other ARM-based handhelds where homebrew Linux becomes available, a separate ARM64 container build uses the same architecture but with Mali or Ampere GPU drivers substituted for AMD's Mesa stack.

The volunteer donor model, the tiered trust architecture, and the power-aware scheduling system together establish handheld gaming devices as a legitimate and productive tier of HelixCluster compute. The Steam Deck leads this tier by every practical metric: native Linux support, price per FLOPS, installed base size, and ecosystem openness. The x86 handhelds extend it with higher per-device performance for users willing to install Linux or Bazzite. The Nintendo platforms represent future potential contingent on homebrew maturity. And the Xbox, for all its impressive silicon, remains permanently excluded until its ecosystem opens in ways that current evidence does not suggest will occur. The volunteer GPU tier, anchored by the Steam Deck and extended through the growing x86 handheld ecosystem, represents a genuinely new paradigm for distributed computing: consumer entertainment hardware that contributes production-grade FLOPS during its many hours of daily idleness, at zero incremental hardware cost to the cluster operator.


---

# 2. Advanced ARM SBCs & Developer Boards

Dedicated ARM single-board computers (SBCs) and developer boards bring purpose-built I/O, industrial networking, and integrated AI accelerators to the HelixCluster ecosystem. This chapter examines the three dominant board families available in 2025: NVIDIA's Jetson AI/edge platform, the Rockchip RK3588 ecosystem that has become the de facto standard for high-performance ARM SBCs, and specialized alternatives from Amlogic, Texas Instruments, and Hardkernel. The chapter concludes with three cluster build recipes optimized for budget, AI inference, and compute density.

Modern ARM boards offer 8-core CPUs at 2.4 GHz, NPUs delivering 6–67 TOPS of INT8 inference, 2.5GbE networking, and full-speed NVMe storage — specifications that rival entry-level x86 servers at a fraction of the power. For HelixCluster deployments, these boards serve as the workhorse compute tier: reliable, low-power nodes for containerized services, edge inference, and distributed storage.

---

## 2.1 NVIDIA Jetson Family

NVIDIA's Jetson family is the most mature AI/edge computing platform in the ARM ecosystem. Every Jetson module is built around NVIDIA GPU architecture, with CUDA cores, Tensor Cores, and deep-learning accelerators (NVDLA) forming a unified inference pipeline. This section traces the family's performance spectrum, examines the transformative Orin Nano Super update, and details the software architecture behind Jetson's edge AI dominance.

### 2.1.1 Jetson Nano to AGX Orin: The 0.5 to 275 TOPS Spectrum

The Jetson family spans a 550× range in AI compute. The Jetson Nano (2019, 128-core Maxwell, 4× Cortex-A57) delivered 0.5 TFLOPS FP16 — now discontinued (December 2023) and unsuitable for modern transformers. The TX2 NX (256-core Pascal, 1.3 TFLOPS) and Xavier NX (48 Volta Tensor Cores + dual NVDLA, 21 TOPS) bridged the gap, with the Xavier NX remaining viable for mid-tier vision workloads on JetPack 5.x.

The Orin generation redefined edge AI. The Orin NX (8GB/16GB) scales from 117 to 157 TOPS with up to 2048 CUDA cores, while the AGX Orin reaches 275 TOPS with 12× Cortex-A78AE cores and 204.8 GB/s memory bandwidth — approaching T4-level inference in a credit-card-sized module. The upcoming Jetson Thor T5000 (2025) introduces Blackwell architecture with 2070 FP4 TFLOPS, 14× Neoverse-V3AE cores, 128GB LPDDR5X, and 4× 25GbE — a 7.5× AI improvement over AGX Orin that also positions it as a cluster head node for edge LLM inference.

**Table 1: NVIDIA Jetson Family Comparison — From Nano to Thor**

| Module | GPU Architecture | AI Perf (INT8) | CUDA Cores | CPU Cores | RAM | Max Power | Price |
|---|---|---|---|---|---|---|---|
| Jetson Nano 4GB | Maxwell (128-core) | 0.5 TFLOPS FP16 | 128 | 4× A57 | 4 GB | 10 W | $99–160 (EOL) |
| Jetson TX2 NX | Pascal (256-core) | 1.3 TFLOPS | 256 | 4× A57 + 2× Denver2 | 8 GB | 15 W | ~$200–250 |
| Jetson Xavier NX | Volta + 48 Tensor Cores | 21 TOPS | 384 | 6× Carmel | 8–16 GB | 20 W | ~$300–575 |
| Jetson Orin Nano 4GB | Ampere (512-core) | 34 TOPS | 512 | 6× A78AE | 4 GB | 25 W | ~$199 |
| **Jetson Orin Nano Super** | **Ampere (1024-core)** | **67 TOPS** | **1024** | **6× A78AE** | **8 GB** | **25 W** | **$249** |
| Jetson Orin NX 16GB | Ampere (2048-core) | 157 TOPS | 2048 | 8× A78AE | 16 GB | 25 W | ~$600 |
| Jetson AGX Orin 64GB | Ampere (2048-core) | 275 TOPS | 2048 | 12× A78AE | 64 GB | 60 W | ~$1,599 |
| Jetson Thor T5000 | Blackwell (2560-core) | 2070 FP4 TFLOPS | 2560 | 14× Neoverse-V3 | 128 GB | 130 W | ~$2,847 |

### 2.1.2 Jetson Orin Nano Super: 67 TOPS at $249

In December 2024, NVIDIA released JetPack 6.2 — a free software upgrade that boosted existing Orin Nano 8GB kits from 40 TOPS to 67 TOPS INT8, doubling memory bandwidth from 68 GB/s to 102 GB/s through optimized power profiles. The rebranded "Orin Nano Super Developer Kit" at $249 is now the highest AI inference-per-dollar SBC available.

At 67 TOPS, the Orin Nano Super rivals entry-level desktop GPUs for quantized inference — running YOLOv5 at 60+ FPS and serving 7B parameter LLMs via TensorRT-LLM with 4-bit quantization. The 1024 Ampere CUDA cores, 32 Tensor Cores, and 102 GB/s bandwidth create an inference pipeline that outperforms many $500+ discrete GPUs on per-watt metrics.

For HelixCluster, the Orin Nano Super is the default AI inference worker. At $3.72 per TOPS (versus $23.80 for AGX Orin), it makes multi-node inference clusters economically viable. Four units provide 268 TOPS aggregate at under $1,000 and 100W total draw.

### 2.1.3 TensorRT, CUDA, and AI/ML Integration Architecture

The Jetson software architecture centers on a vertically integrated stack that general-purpose ARM SBCs cannot replicate. Understanding this architecture is essential for designing HelixCluster AI workloads.

```
┌─────────────────────────────────────────────────────────────┐
│                    APPLICATION LAYER                        │
│  PyTorch / TensorFlow / ONNX Runtime / vLLM / MLC-LLM      │
├─────────────────────────────────────────────────────────────┤
│                  TENSORRT OPTIMIZER                         │
│  Graph optimization │ Layer fusion │ Kernel auto-tuning    │
│  FP32 → FP16 → INT8 quantization │ Dynamic batching        │
├─────────────────────────────────────────────────────────────┤
│              CUDA RUNTIME + cuDNN + cuBLAS                  │
│  GPU kernel dispatch │ Memory management │ Stream queues   │
├─────────────────────────────────────────────────────────────┤
│              HARDWARE ACCELERATION LAYER                    │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────┐  │
│  │ CUDA Cores   │  │ Tensor Cores │  │ NVDLA Engines   │  │
│  │ (FP32/FP16)  │  │ (INT8/FP16)  │  │ (INT8/INT16)    │  │
│  └──────────────┘  └──────────────┘  └─────────────────┘  │
├─────────────────────────────────────────────────────────────┤
│                  L4T LINUX KERNEL                           │
│  GPU driver │ NVDLA driver │ VIC (video) │ ISP (camera)   │
├─────────────────────────────────────────────────────────────┤
│              JETSON HARDWARE (Orin Nano Super)              │
│  1024 Ampere CUDA cores │ 32 Tensor Cores │ 6× A78AE CPU  │
└─────────────────────────────────────────────────────────────┘
```

**CUDA** provides the foundation, dispatching thousands of concurrent threads across the GPU's Streaming Multiprocessor (SM) units. The Orin Nano Super organizes 1024 CUDA cores into 8 SMs, each with 128 CUDA cores, 4 Tensor Cores, and shared memory caches.

**TensorRT** optimizes inference by applying layer fusion (combining consecutive operations), precision calibration (FP32 → INT8), and kernel auto-tuning. A ResNet-50 model running 45 FPS in raw PyTorch achieves 180+ FPS after TensorRT optimization.

**TensorRT-LLM** extends this to large language models with optimized attention kernels (FlashAttention, PagedAttention) and speculative decoding. An INT4-quantized Llama 3.1 7B runs at 15–25 tokens/second — sufficient for interactive chat and agent workflows.

**NVDLA** engines provide deterministic, power-efficient inference on Orin NX and AGX Orin. The Orin Nano Super lacks NVDLA but compensates with its dense CUDA + Tensor Core pipeline, delivering superior flexibility for modern architectures.

### 2.1.4 JetPack SDK and Container Runtime for Edge AI Workloads

JetPack is NVIDIA's unified SDK, bundling the L4T kernel, CUDA toolkit, cuDNN, TensorRT, and multimedia libraries. JetPack 6.0 (Ubuntu 22.04 base, mid-2025) includes the NVIDIA Container Toolkit, which is critical for HelixCluster deployments.

The NVIDIA Container Runtime injects CUDA drivers and GPU devices into containers at launch, enabling standard Docker workloads with full GPU access:

```bash
# JetPack container runtime workflow for HelixCluster AI nodes
docker run --runtime nvidia --rm \
  --device /dev/nvhost-gpu --device /dev/nvhost-ctrl \
  -v $(pwd)/models:/models:ro \
  nvcr.io/nvidia/tensorrt:24.07-py3 \
  trtexec --onnx=/models/resnet50.onnx --int8 --saveEngine=/models/resnet50.trt
```

JetPack's **NGC registry** provides pre-built, Jetson-validated containers for PyTorch, TensorFlow, TensorRT, and Triton Inference Server — eliminating the hours of source compilation required on general-purpose ARM boards.

The trade-off is vendor lock-in. Jetson requires NVIDIA's L4T kernel; mainline Linux cannot drive the GPU, NVDLA, or multimedia engines. NVIDIA commits to 10+ years of JetPack updates, but open-source-focused deployments must weigh this dependency against the performance advantages.

---

## 2.2 Rockchip RK3588 Ecosystem

If NVIDIA Jetson dominates AI inference, the Rockchip RK3588 has become the uncontested champion of general-purpose ARM SBCs. This 8nm SoC combines 4× Cortex-A76 performance cores at 2.4 GHz with 4× Cortex-A55 efficiency cores at 1.8 GHz, a Mali-G610 MP4 GPU, and a 6 TOPS NPU — all at price points ranging from $75 to $449 depending on the board's I/O configuration. Over a dozen manufacturers now produce RK3588-based boards, each targeting different deployment scenarios.

### 2.2.1 Nine Boards Compared: ROCK 5B, NanoPi R6S, BPI-M7, Firefly ITX-3588J, Turing RK1

The RK3588 ecosystem offers precise workload matching across form factors. The **Radxa ROCK 5B** ($157, 8GB) is the default general-purpose node — PCIe 3.0 x4 NVMe, 2.5GbE, up to 32GB RAM, and the best mainline Linux support of any RK3588 board. The **NanoPi R6S** ($139) sacrifices M.2 NVMe for a second 2.5GbE port, creating a triple-Ethernet gateway ideal for load balancer and ingress controller roles (validated at 2.35 Gbps bidirectional per port). The **Banana Pi BPI-M7** ($165) packs dual 2.5GbE plus WiFi 6/BT 5.2 into a 92×62mm footprint — one of few RK3588 boards with onboard wireless.

For storage-focused deployments, the **Firefly ITX-3588J** ($449) offers full Mini-ITX layout with four SATA3 ports, PCIe 3.0 x4 expansion, and industrial temperature range (-20°C to 60°C). The **FriendlyELEC CM3588 NAS Kit** ($130–180) pairs an RK3588 module with four M.2 2280 NVMe slots, purpose-built for Ceph or MinIO nodes. The **Mixtile Blade 3** ($160–259) targets clustering with its Pico-ITX form factor, dual 2.5GbE with LACP, and U.2 edge connector enabling 4-board stacks at 20 Gbps inter-board bandwidth.

The **Turing RK1** ($110–210 per module) stands apart as a 260-pin SO-DIMM compute module, pin-compatible with Raspberry Pi CM4 and Jetson carriers. This allows it to slot into the Turing Pi 2.5 cluster board, existing CM4 carriers, and Jetson developer kit bases. The **NanoPi R6C** ($85–125) offers a balanced middle ground: one 2.5GbE port plus M.2 NVMe. The **Khadas Edge2** ($199–299) prioritizes ultra-thin compactness (5.7mm) but omits Ethernet entirely — WiFi 6 only, requiring a USB adapter for wired connectivity.

### 2.2.2 NanoPi R6S: Dual 2.5GbE Cluster Gateway at $139

The NanoPi R6S is the ideal edge gateway for HelixCluster. Its RK3588S SoC delivers identical CPU and NPU performance to the full RK3588 but with fewer PCIe lanes — a worthwhile trade for the triple Ethernet configuration. Dual Realtek RTL8125BG controllers achieve 2.35 Gbps bidirectional throughput, while the third GbE port provides out-of-band management or dedicated WAN.

At 4.6W idle and 11.4W maximum, the R6S achieves approximately 7 GFLOPS/W — among the most efficiency-dense nodes evaluated. For HelixCluster, it serves as the WireGuard mesh gateway, ingress controller, and edge load balancer, with sufficient CPU headroom for TLS termination and lightweight containers alongside networking duties.

### 2.2.3 Turing RK1: 32 Cores in Mini-ITX at ~$800

The Turing RK1 is the highest density-per-watt ARM cluster configuration. Each module packs a full RK3588 SoC, 8–32GB LPDDR4x/LPDDR5, and 16GB eMMC into a 69.6×45mm SO-DIMM package. Four modules slot into the Turing Pi 2.5 carrier board ($279 mini-ITX with integrated GbE L2 switch), creating a 32-core cluster smaller than a standard ITX motherboard.

A fully populated Turing Pi 2.5 delivers: **32 CPU cores**, **24 TOPS NPU** aggregate, **up to 128GB RAM**, **4× M.2 NVMe**, integrated GbE L2 switch plus 2× external GbE uplinks, and **under 80W** total power draw.

The SO-DIMM form factor's CM4 compatibility is transformative — existing CM4 carriers and Jetson bases accept RK1 modules without modification. Pricing scales with RAM: $676 (4× 8GB), $976 (4× 16GB), or $1,176 (4× 32GB). With carrier board, SSDs, PSU, and cooling, a complete 8GB build totals approximately $1,700; the 32GB build reaches $2,100.

### 2.2.4 Mainline Linux Status: GPU in 6.10, NPU Support Q2 2025

Mainline Linux support for the RK3588 has matured rapidly — critical for production deployments requiring security updates without vendor kernel dependencies.

As of Linux 6.12 (late 2024), mainline supports: GPU (Mali-G610 via Panfrost with 3D acceleration), all CPU cores with frequency scaling, USB3, 2.5GbE/GbE networking, NVMe/SATA/eMMC storage, and VP8/H.264 video decode. Linux 6.13 added HDMI display output.

The remaining gap is **NPU acceleration**: the 6 TOPS NPU requires vendor RKNN-Toolkit2 on Linux 5.10/5.15, though Collabora targets Q2 2025 for an upstream driver. For headless cluster nodes — container orchestration, databases, general compute — mainline 6.12+ is fully viable. AI inference nodes should use vendor kernels until the NPU driver lands.

---

## 2.3 Other Notable ARM Boards

Beyond the Jetson and RK3588 ecosystems, several ARM boards fill specialized niches in the HelixCluster topology. The Khadas VIM4 (Amlogic A311D2), Hardkernel Odroid family, and BeagleBone AI-64 each offer unique capabilities that merit consideration for specific deployment scenarios.

### 2.3.1 Khadas VIM4, Odroid N2+/M1, BeagleBone AI-64

The **Khadas VIM4** ($220, Amlogic A311D2, 4× A73 + 4× A53, 3.2 TOPS NPU) offers unique HDMI input for video capture workloads. However, vendor kernel 5.4 is required (mainline support lags RK3588 significantly), and the single GbE port plus lack of native NVMe diminish its cluster appeal at a price above the ROCK 5B.

The **Odroid N2+** ($69–95, Amlogic S922X) is a proven platform with idle draw of just 1.6W, ideal for DNS, DHCP, and monitoring services. Hardkernel guarantees supply until 2036. Limitations are severe for modern workloads: 4GB RAM max, no NPU, no NVMe. The **Odroid M1** ($70–90, RK3568B2, 0.8 TOPS) adds M.2 NVMe (PCIe 3.0 x2) and a SATA port — a unique combination for low-cost distributed storage nodes. It shares the 2036 supply guarantee.

The **BeagleBone AI-64** ($185–230, TI TDA4VM) is unique: 2× Cortex-A72, 6× Cortex-R5F real-time cores, a C7x DSP, and 8 TOPS Deep Learning Accelerator. The R5F cores enable deterministic control alongside AI inference — no Jetson or RK3588 board offers this. However, 4GB RAM, a dual-core CPU, and TI's smaller software community limit it to specialized industrial edge roles.

### 2.3.2 Power Consumption, Thermal, and Networking Comparison

Board selection for HelixCluster must account for power budget and thermal constraints, particularly in dense deployments where multiple boards share an enclosure. The following table summarizes these critical operational parameters.

**Table 2: Power, Thermal, and Networking Comparison**

| Board | SoC | Idle Power | Max Power | Thermal Design | Primary Network | Secondary Network | NPU TOPS |
|---|---|---|---|---|---|---|---|
| Jetson Orin Nano Super | Orin | 7 W | 25 W | Active heatsink required | 1× GbE | — | 67 |
| Jetson AGX Orin 64GB | Orin | 15 W | 60 W | Active cooling mandatory | 1× GbE | — | 275 |
| Radxa ROCK 5B 8GB | RK3588 | 2.8 W | 10 W | Heatsink recommended | 1× 2.5GbE | M.2 E-Key WiFi | 6 |
| NanoPi R6S | RK3588S | 4.6 W | 11.4 W | Metal enclosure dissipates | 2× 2.5GbE | 1× GbE | 6 |
| NanoPi R6C | RK3588S | 3.2 W | 9 W | Heatsink recommended | 1× 2.5GbE | 1× GbE | 6 |
| Banana Pi BPI-M7 | RK3588 | 2.5 W | 9 W | Compact heatsink | 2× 2.5GbE | WiFi 6 / BT 5.2 | 6 |
| Turing RK1 (per module) | RK3588 | 1.8 W | 7 W | Carrier board cooling | 1× GbE (via carrier) | — | 6 |
| Firefly ITX-3588J | RK3588 | 1.35 W | 20 W | Mini-ITX case airflow | 2× GbE | WiFi 6 | 6 |
| Mixtile Blade 3 | RK3588 | 2.2 W | 8 W | Heatsink + case fan | 2× 2.5GbE | U.2 stacking | 6 |
| Khadas VIM4 | A311D2 | 3 W | 12 W | Active cooling kit avail. | 1× GbE | WiFi 6 | 3.2 |
| Odroid N2+ 4GB | S922X | 1.6 W | 6.2 W | Passive heatsink suffic. | 1× GbE | — | — |
| Odroid M1 8GB | RK3568B2 | 1.5 W | 5 W | Passive heatsink | 1× GbE | SATA port | 0.8 |
| BeagleBone AI-64 | TDA4VM | 4 W | 15 W | Heatsink recommended | 1× GbE | M.2 E-Key | 8 |

Key patterns emerge: RK3588 boards achieve remarkable efficiency — the Firefly ITX-3588J idles at 1.35W, and the Turing RK1 at 1.8W, critical for always-on nodes. The Jetson Orin Nano Super's 25W maximum is higher than RK3588 alternatives, but its NPU delivers 2.7 TOPS/W — far exceeding the RK3588's 0.6 TOPS/W. The NanoPi R6S's dual 2.5GbE at 11.4W maximum represents the best network-bandwidth-per-watt ratio available.

---

## 2.4 Recommended SBC Cluster Configurations

The following three build recipes translate the board analysis into actionable procurement lists. Each recipe targets a specific budget and workload profile, with component pricing and expected aggregate performance.

### 2.4.1 Budget Build ($500): 4× ROCK 5B 8GB + Switch

This configuration uses ROCK 5B boards as homogeneous worker nodes. Four units at $157 each provide 32 CPU cores, 24 TOPS aggregate NPU, four PCIe 3.0 x4 NVMe slots, and four 2.5GbE ports — compute density rivaling entry-level x86 servers at one-quarter the power draw.

The homogeneous design simplifies administration: identical Armbian images across all nodes. A 2.5GbE switch ($85) provides sufficient bandwidth for container orchestration and storage replication. Expected performance: ~200 GFLOPS CPU, 24 TOPS INT8 NPU, 32GB RAM, and 4× NVMe SSDs at 3,500 MB/s — enough for a 4-node K3s cluster running PostgreSQL, Redis, web services, and light inference.

### 2.4.2 AI-Focused Build ($1,000): Jetson Orin Nano Super + 4× ROCK 5B

This heterogeneous design pairs AI specialization with general compute. The Jetson Orin Nano Super serves as AI inference controller and cluster head, running TensorRT-optimized models and LLM serving. Four ROCK 5B workers handle containerized services, storage, and CPU workloads.

The Jetson's 67 TOPS and 102 GB/s bandwidth create a dedicated inference tier the RK3588 cannot match — the ROCK 5B's 6 TOPS NPU supports basic object detection but lacks the software maturity for transformer models. The 2.5GbE mesh connects Jetson to workers with bandwidth for model distribution and result streaming.

This five-node topology delivers 268 TOPS aggregate AI (67 + 24×4), 40 CPU cores, 40GB RAM, and five NVMe slots. It serves a quantized 7B LLM on the Jetson while running web services and databases on ROCK 5B workers — a complete edge AI platform for under $1,000.

### 2.4.3 Density Build ($2,000): 2× Turing Pi 2.5 + 8× Turing RK1

Two Turing Pi 2.5 carrier boards — each hosting four RK1 modules — deliver 64 CPU cores, 48 TOPS NPU, up to 256GB RAM, and 8× M.2 NVMe slots in a footprint smaller than a single micro-ATX case. The boards connect via external GbE uplinks to a 2.5GbE switch, with each board's internal L2 switch handling intra-board communication. The RK1's 7W TDP enables passive cooling through the carrier heatsink — no fans required at moderate temperatures.

**Table 3: SBC Cluster Build Recipes — Three Budget Tiers**

| Component | Budget ($500) | AI-Focused ($1,000) | Density ($2,000) |
|---|---|---|---|
| **Head/AI Node** | 1× ROCK 5B (shared) | 1× Jetson Orin Nano Super | 2× Turing Pi 2.5 carriers |
| **Worker Nodes** | 4× ROCK 5B 8GB @ $157 | 4× ROCK 5B 8GB @ $157 | 8× Turing RK1 8GB @ $169 |
| **RAM per Node** | 8 GB | 8 GB (ROCK) / 8 GB (Jetson) | 8–32 GB (module choice) |
| **Network Switch** | 5-port 2.5GbE unmanaged ~$85 | 5-port 2.5GbE managed ~$120 | 8-port 2.5GbE managed ~$150 |
| **NVMe Storage** | Optional: reuse existing | 4× 500GB NVMe ~$200 | 8× 500GB NVMe ~$400 |
| **Power Supply** | 5× USB-C PD 30W ~$50 | 5× USB-C PD + 65W ~$75 | 2× 150W ATX ~$80 |
| **Cables/Accessories** | Ethernet, heatsinks ~$25 | Ethernet, heatsinks ~$30 | Rackmount kit ~$50 |
| **Total CPU Cores** | 32 (8× A76 + 8× A55 × 4) | 40 (32 + 6 Jetson) | 64 (8× per module) |
| **Total NPU TOPS** | 24 (6 × 4) | 91 (67 + 6 × 4) | 48 (6 × 8) |
| **Total RAM** | 32 GB | 40 GB | 64–256 GB |
| **Aggregate NVMe** | 4× PCIe 3.0 x4 | 4× PCIe 3.0 x4 + Jetson ext. | 8× M.2 (carrier) |
| **Max Power** | ~45 W | ~70 W | ~160 W |
| **Estimated Cost** | **~$513** | **~$1,034** | **~$1,992** |
| **Best For** | K3s, web services, light inference | LLM serving, vision AI, mixed workloads | Max density, CI/CD, render farm |

### 2.4.4 Selecting the Right Configuration

The **budget build** suits first deployments and stateless services without AI inference. Homogeneous ROCK 5B nodes minimize operational complexity — one Armbian image, one update pipeline.

The **AI-focused build** addresses the most common production requirement: edge AI alongside traditional services. The Jetson's TensorRT stack handles model serving while ROCK 5B workers provide general compute, mirroring cloud Kubernetes patterns where GPU nodes run inference and CPU nodes handle everything else.

The **density build** maximizes cores and RAM in minimal rack space. Two Turing Pi 2.5 boards fit in 2U with room for switch and PSU, delivering 64 cores in a footprint requiring four mini-ITX cases otherwise. The trade-off is GbE inter-node bandwidth (vs. 2.5GbE on ROCK 5B builds) and less mature NPU software versus Jetson's TensorRT.

**Table 4: Master SBC Comparison — 18 Boards for HelixCluster**

| Board | SoC | CPU Cores | RAM (Max) | AI Perf. | Network | NVMe | Price | Best Cluster Role |
|---|---|---|---|---|---|---|---|---|
| **Jetson Orin Nano Super** | Orin | 6× A78AE | 8 GB | 67 TOPS | 1× GbE | External M.2 | $249 | AI inference controller |
| **Jetson AGX Orin 64GB** | Orin | 12× A78AE | 64 GB | 275 TOPS | 1× GbE | M.2 | $1,599 | High-throughput AI head node |
| **Jetson Thor T5000** | Blackwell | 14× Neoverse-V3 | 128 GB | 1000 FP8 T | 4× 25GbE | M.2 Gen5 | ~$2,847 | LLM inference / cluster controller |
| **Radxa ROCK 5B** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 1× 2.5GbE | PCIe 3.0 x4 | $157 | General compute worker |
| **NanoPi R6S** | RK3588S | 4×A76+4×A55 | 8 GB | 6 TOPS | 2× 2.5GbE + GbE | No | $139 | Edge gateway / load balancer |
| **NanoPi R6C** | RK3588S | 4×A76+4×A55 | 8 GB | 6 TOPS | 1× 2.5GbE + GbE | M.2 | $85 | Balanced compute + storage |
| **Banana Pi BPI-M7** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 2× 2.5GbE + WiFi 6 | M.2 | $165 | Compact wireless node |
| **Mixtile Blade 3** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 2× 2.5GbE | U.2 PCIe 3.0 x4 | $160 | Cluster stacking / density |
| **Turing RK1** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 1× GbE (carrier) | M.2 (carrier) | $110 | Compute module / density build |
| **Firefly ITX-3588J** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 2× GbE | M.2 SATA | $449 | NAS / storage node |
| **FriendlyELEC CM3588** | RK3588 | 4×A76+4×A55 | 32 GB | 6 TOPS | 1× 2.5GbE | **4× M.2 NVMe** | $130+ | Distributed storage (Ceph/MinIO) |
| **Khadas VIM4** | A311D2 | 4×A73+4×A53 | 8 GB | 3.2 TOPS | 1× GbE + WiFi 6 | M.2 (breakout) | $220 | Video capture / gateway |
| **Khadas Edge2** | RK3588S | 4×A76+4×A55 | 16 GB | 6 TOPS | WiFi 6 only | No | $199 | Ultra-compact wireless node |
| **Odroid M1 8GB** | RK3568B2 | 4× A55 | 8 GB | 0.8 TOPS | 1× GbE | M.2 PCIe 3.0 x2 | $90 | Low-power storage node |
| **Odroid M1S 8GB** | RK3566 | 4× A55 | 8 GB | — | 1× GbE | M.2 PCIe 2.1 | $59 | Entry-level container host |
| **Odroid N2+ 4GB** | S922X | 4×A73+2×A53 | 4 GB | — | 1× GbE | No | $69 | Infrastructure services (DNS/DHCP) |
| **BeagleBone AI-64** | TDA4VM | 2× A72 | 4 GB | 8 TOPS | 1× GbE | No | $185 | Industrial edge / real-time control |
| **ROCKPro64** | RK3399 | 2×A72+4×A53 | 4 GB | — | 1× GbE | PCIe 4x | $80 | Legacy node (use if owned) |

The RK3588 family dominates general-purpose and density roles, NVIDIA Jetson controls AI inference, and specialized boards fill storage, networking, and industrial niches. Selection should begin with workload: AI inference demands Jetson, general compute favors RK3588, storage requires SATA/multi-NVMe, and networking benefits from dual-Ethernet. The $150–$350 price band delivers optimal value — boards above this threshold justify their premium only for specific I/O or AI needs, while sub-$100 boards sacrifice too much capability for meaningful cluster contribution.


---

# 3. RISC-V & Emerging Architectures

RISC-V has crossed the threshold from academic curiosity to production-viable platform for edge-tier workloads. Docker v29 ships for RISC-V within six days of x86/ARM release, Go and Rust compile natively, and community Kubernetes forks run on commercially available boards. Yet the performance gap remains severe: the most powerful RISC-V board today delivers roughly one-tenth the throughput of a mid-range ARM server, and the best single-core RISC-V CPU matches only a Raspberry Pi 3 B+. For HelixCluster, RISC-V is not a performance play---it is an insurance policy against architecture lock-in.

This chapter maps the RISC-V board landscape, evaluates software ecosystem readiness, surveys LoongArch and OpenPOWER as complementary architectures, and defines a concrete integration strategy with cross-compilation pipelines, capability detection, and tier assignments.

---

## 3.1 RISC-V Board Ecosystem

### 3.1.1 Current Board Landscape and Linux Support

The RISC-V single-board computer market fragmented rapidly in 2023-2025, producing devices from $60 embedded modules to $1,999 server-class bundles. Table 3.1 compares the six boards most relevant to HelixCluster.

**Table 3.1 --- RISC-V Board Comparison for HelixCluster Deployment**

| Board | SoC / Cores | ISA | RAM | Network | Storage | Price | Linux Support |
|-------|-------------|-----|-----|---------|---------|-------|---------------|
| Milk-V Pioneer | SG2042 / 64x C920 @ 2.0 GHz | RV64GC + RVV 0.7.1 | Up to 128 GB DDR4-3200 ECC | 2x 2.5GbE | 2x M.2, 5x SATA | $1,199 (board) | Debian, Fedora, Ubuntu; vendor kernel patches |
| SiFive HiFive Premier P550 | ESWIN EIC7700X / 4x P550 @ 1.4 GHz | RV64GC | 16-32 GB LPDDR5-6400 | 2x GbE | 128 GB eMMC, SATA | $399 (16 GB) | Debian, Fedora; ~100 patches over mainline |
| Milk-V Jupiter (M1) | SpacemiT M1 / 8x X60 @ 1.8 GHz | RV64GCVB (RVA22, RVV 1.0) | 4-16 GB LPDDR4X | 2x GbE (PoE) | M.2, microSD | ~$150 (est.) | Bianbu, Armbian, Debian; RVV 1.0 toolchain |
| VisionFive 2 / Milk-V Mars | StarFive JH7110 / 4x U74 @ 1.5 GHz | RV64GC | 1-8 GB LPDDR4 | 1x GbE | M.2, microSD | $60-100 | Debian, Fedora, Armbian, OpenSUSE |
| Kendryte K230 | Dual C908 @ 1.6+0.8 GHz | RV64GC + RVV 1.0 | 512 MB-2 GB LPDDR3 | USB-Eth | microSD, SPI | $49-88 | Buildroot, custom Linux |
| Pine64 Star64 | JH7110 / 4x U74 @ 1.5 GHz | RV64GC | 2-8 GB LPDDR4 | 1x GbE | M.2, microSD | $70-90 | Armbian, NixOS, NuttX |

The JH7110-based trio---VisionFive 2, Milk-V Mars, and Pine64 Star64---has the most mature software ecosystem with broad distribution support. However, the U74 cores use an in-order pipeline at 1.5 GHz on a 28 nm node, yielding performance far below modern alternatives. Rust compilation benchmarks place the Milk-V Mars at 936 seconds versus the Raspberry Pi 5's 76 seconds---a 12.2x gap. These boards suit IoT protocol bridging and sensor aggregation, but not general-purpose compute.

The SiFive HiFive Premier P550 is the highest-performance single-core RISC-V board commercially available. Its four P550 out-of-order cores at 1.4 GHz achieve a Geekbench 6 single-core score of 136---comparable to a Raspberry Pi 3 B+ and half the Pi 4's 295. Memory bandwidth is severely constrained: LPDDR5-6400 theoretically capable of 40+ GB/s delivers only ~10 GB/s in practice, and PCIe Gen3 x4 achieves ~800 MB/s rather than the expected 2+ GB/s, indicating SoC-level bandwidth limitations. At $399, the P550 costs more than an RK3588-based ARM SBC that delivers 3-5x the compute performance.

The Milk-V Jupiter, built around the SpacemiT M1 SoC, is the first board implementing the RVA22 profile with RVV 1.0---the first ratified RISC-V vector extension. RVV 1.0 enables compiler autovectorization in GCC and LLVM, yielding 2-13x gains on vectorizable kernels. However, software compatibility remains problematic; XDA's review characterized general app performance as "flimsy," and the PCIe 2.1 x2 implementation bottlenecks expansion cards.

### 3.1.2 Milk-V Pioneer: 64-Core SG2042 at $1,199

The Milk-V Pioneer is the only RISC-V board in the server-class category. Its 64 T-Head XuanTie C920 cores, 128 GB ECC RAM capacity, dual 2.5GbE networking, and multiple M.2/SATA slots give it the I/O and memory footprint of a real server node. Crowd Supply bundles the board at $1,199 (CPU included) or $1,999 with 128 GB RAM, a 1 TB SSD, and a 10GbE add-in card.

The C920 cores implement RVV 0.7.1---the pre-ratification vector draft---which lacks production compiler support. This means the 64-core array cannot leverage vector acceleration for ML or crypto workloads. Additionally, the cores lack the out-of-order execution depth of modern server CPUs; SPEC CPU2017 single-core results confirm weak single-threaded performance.

The Pioneer's genuine strength is parallelism. With 64 threads and 128 GB RAM, it can run 64 concurrent compilation jobs, making it a capable native RISC-V build farm. For embarrassingly parallel workloads---CI/CD pipelines, software packaging---the core count compensates. But for latency-sensitive services or single-threaded control plane software, the Pioneer underperforms relative to its price class.

### 3.1.3 Performance Benchmarks vs. ARM and x86

CERN's High Energy Physics benchmarking framework provides the most authoritative cross-architecture comparison. The db12 throughput score places the SG2042 at 378.3 total (5.8 per core), versus the Ampere Altra Max at 3,754 (14.66 per core)---roughly 10x slower in aggregate and 2.5x slower per core. Power efficiency (HS23 per watt) is surprisingly competitive at ~3.0 versus the Altra Max's 4.17, and at under 2 W per core at maximum load, the architecture scales power linearly with thread count.

**Table 3.2 --- Cross-Architecture Performance Benchmarks (Price-Equivalent Comparison)**

| System | Cores | Arch | GB6 Single | GB6 Multi | db12 Score | Power | Price | Perf/$ Index |
|--------|-------|------|------------|-----------|------------|-------|-------|-------------|
| Raspberry Pi 5 | 4 | ARM A76 | 784 | 1,566 | --- | ~8 W | $60 | 26.1 |
| Orange Pi 5 Max | 8 | ARM A55/A76 | ~850 | ~3,200 | --- | ~15 W | $120 | 26.7 |
| SiFive P550 | 4 | RISC-V P550 | 136 | 423 | --- | 8-13 W | $399 | 1.1 |
| Milk-V Jupiter M1 | 8 | RISC-V X60 | ~120 | ~500 | --- | ~15 W | ~$150 | 3.3 |
| **Milk-V Pioneer** | **64** | **RISC-V C920** | **~40*** | **~2,800*** | **378 (5.8/core)** | **125 W** | **$1,199** | **2.3** |
| VisionFive 2 | 4 | RISC-V U74 | ~75 | ~200 | --- | ~5 W | $70 | 2.9 |
| Ampere Altra Max | 128 | ARM N2 | ~350 | ~15,000 | 3,754 (14.7/core) | 250 W | $4,000+ | 3.8 |
| Loongson 3A6000 | 4 | LoongArch | ~400* | ~1,600* | --- | ~50 W | ~$300 | 5.3 |

\* Estimated from SPEC and workload benchmarks.

The performance-per-dollar index reveals the challenge facing RISC-V: at equivalent price points, ARM SBCs deliver 10-25x better multi-threaded performance per dollar. The Pioneer's 64-core design partially compensates, but at $1,199 it competes against used x86 servers and new ARM boards with substantially more performance.

The single-core deficit is the binding constraint. HelixCluster's control plane components---API gateways, etcd, schedulers---are often single-threaded. A RISC-V node running these services would create a latency bottleneck that cascades through the cluster. RISC-V boards should be restricted to Tier 3-4 edge and build-farm roles until single-core performance improves by at least 3-4x.

---

## 3.2 Software Ecosystem Maturity

### 3.2.1 Docker: Production-Ready on RISC-V

Docker v29.0.0 shipped for RISC-V64 in November 2025 just six days after the x86/ARM release, with full feature parity: containerd v2.1.5 as the default image store, nftables-based networking, API v1.44, and rootless container support. Automated build infrastructure using native BananaPi F3 hardware compiles Debian, RPM, and Gentoo packages.

For HelixCluster, the standard container deployment model works on RISC-V with no runtime modifications. Installation on Debian and Ubuntu RISC-V ports is straightforward:

```bash
# Docker installation on riscv64 (Debian/Ubuntu)
sudo apt-get update
sudo apt-get install -y docker.io docker-compose-plugin

# Verify architecture support
docker run --rm riscv64/debian:unstable uname -m
# Expected output: riscv64
```

The caveat is image availability. While Docker's official `riscv64/debian`, `riscv64/alpine`, and `riscv64/ubuntu` base images are maintained, many third-party images lack RISC-V builds. HelixCluster's CI pipeline must produce multi-arch manifests including `linux/riscv64`. The `docker buildx` system with QEMU can cross-build RISC-V images on x86 hosts, though native builds on the Pioneer are preferred.

### 3.2.2 Cross-Compilation Toolchain Status

Go, Rust, Zig, and C/C++ all support RISC-V as a compilation target, though maturity varies. Table 3.3 summarizes the current state.

**Table 3.3 --- Language Cross-Compilation Status for RISC-V (2025)**

| Language | Target Triple | Tier | Native Compilation | Cross-Compile from x86 | Notes |
|----------|---------------|------|-------------------|------------------------|-------|
| Go | `linux/riscv64` | Tier 1 (since 1.21) | ✅ Yes | ✅ `GOARCH=riscv64` | `GORISCV64` env var selects RVA20/22/23 profile |
| Rust | `riscv64gc-unknown-linux-gnu` | Tier 2 (Tier 1 effort) | ✅ Yes | ✅ `cross` or `rustc` | RISE-funded Tier-1 migration in progress |
| Zig | `riscv64-linux-musl` | Tier 1 | ✅ Yes | ✅ Built-in cross-compilation | Fine-grained ISA extension control |
| C/C++ (GCC) | `riscv64-linux-gnu` | Production | ✅ Yes | ✅ `gcc-riscv64` package | Mature since ~2018; RVV 1.0 autovectorization |
| C/C++ (LLVM) | `riscv64` | Production | ✅ Yes | ✅ Clang/LLVM built-in | LTO enabled in Linux 6.9+ |
| Java | `riscv64` | Functional | ✅ OpenJDK port | ⚠️ Limited tooling | Functional but unoptimized vs x86/ARM |

**Go** offers the cleanest RISC-V support. Native `linux/riscv64` binaries have been available since Go 1.21, and `GORISCV64` enables targeting specific RVA profiles:

```bash
# Cross-compile Go application for RISC-V from x86/ARM build host
GOOS=linux GOARCH=riscv64 GORISCV64=rva22u64 go build -o helix-agent-riscv64 ./cmd/agent

# GORISCV64 accepts: rva20u64, rva22u64, rva23u64
# rva22u64 enables compressed instructions and Zba/Zbb bit manipulation
# rva23u64 adds vector operations where hardware supports RVV
```

The RISE Project continues to optimize Go's RISC-V backend with vectorized `memmove`, SHA-256, SHA-512, and MD5 routines. For a cluster agent written in Go, RISC-V requires no source-level changes---only CI pipeline additions.

**Rust** targets `riscv64gc-unknown-linux-gnu` at Tier 2 with a RISE-funded effort to reach Tier 1. Cross-compilation works via the `cross` tool:

```bash
# Cross-compile Rust binary for RISC-V
cross build --target riscv64gc-unknown-linux-gnu --release

# Or with native rustc
rustup target add riscv64gc-unknown-linux-gnu
CARGO_TARGET_RISCV64GC_UNKNOWN_LINUX_GNU_LINKER=riscv64-linux-gnu-gcc \
  cargo build --target riscv64gc-unknown-linux-gnu --release
```

**Zig** provides the most ergonomic cross-compilation experience. Its built-in cross-compilation requires no additional toolchain installation:

```bash
# Cross-compile Zig to RISC-V with musl libc
zig build -Dtarget=riscv64-linux-musl -Dcpu=baseline_rv64+rva22u64

# Or compile a single file
zig cc -target riscv64-linux-musl -mcpu=baseline_rv64+rva22u64 \
  -o helix-agent-riscv64 main.c
```

Zig's `-mcpu` flag enables fine-grained ISA extension control, allowing builds optimized for specific RISC-V boards---RVV 1.0 instructions on the Jupiter while maintaining compatibility with the Pioneer's RVV 0.7.1.

**C/C++** via GCC and LLVM is fully mature, with RVV 1.0 intrinsics (`<riscv_vector.h>`) and autovectorization. Translation tools like `neon2rvv` enable porting ARM Neon code to RISC-V vector instructions.

### 3.2.3 Kubernetes: Community K3s Forks Work, Official Support Pending

The upstream K3s project has not officially prioritized RISC-V and maintains no build infrastructure. Community forks have filled the gap: CARV-ICS-FORTH provides K3s v1.27.3+k3s1 for RISC-V64, while Cloud-V publishes setup scripts tested on VisionFive 2 and Milk-V Pioneer hardware.

The primary limitation is K3s's SQLite-embedded database, which requires CGO and complicates RISC-V cross-compilation. External etcd is recommended:

```bash
# Control plane setup (Cloud-V validated)
wget https://raw.githubusercontent.com/alitariq4589/kubernetes-riscv/main/scripts/control-plane-setup-riscv64.sh
chmod +x control-plane-setup-riscv64.sh && ./control-plane-setup-riscv64.sh
```

For HelixCluster, the pragmatic approach is to run the K3s control plane on x86 or ARM Tier 1-2 nodes and register RISC-V workers via `kubeadm` join. This avoids placing etcd or API-server latency requirements on RISC-V hardware.

---

## 3.3 LoongArch, POWER9, and Other Architectures

### 3.3.1 Loongson 3A6000: Performance Between Zen 1 and Zen 2

Loongson's LoongArch ISA represents China's push for technological sovereignty and produces the most competitive non-x86/non-ARM desktop CPU available. The 3A6000 is a quad-core, 2.5 GHz processor with SMT, built on a 6-wide out-of-order core with a 1024-entry indirect branch predictor and 4-pipe FPU with 256-bit LASX SIMD. Chips and Cheese concluded its per-core performance sits between AMD Zen 1 and Zen 2---impressive for an independently developed architecture.

For HelixCluster, the 3A6000 is significant for two reasons. First, Debian officially promoted `loong64` to a supported architecture in December 2025 for Debian 14 "Forky," with approximately 30,000 packages available. Go, Rust, GCC, LLVM, and OpenJDK all support LoongArch upstream. Second, at approximately $300 for the CPU, the price/performance ratio approaches viability for edge compute.

The limitations are availability and trust. Hardware is difficult to source outside China, and the closed ISA limits external audit. The SMT implementation provides only ~20% throughput gain versus 40%+ on Zen. For HelixCluster, the 3A6000 rates as a Tier 3 edge node for non-sensitive workloads, constrained by supply chain and geopolitical risk.

### 3.3.2 OpenPOWER Talos II: Fully Open Firmware, Unique Security Value

Raptor Computing Systems' OpenPOWER platforms offer the only fully open-source firmware server-grade ecosystem, with source code available down to the BMC. The Talos II (dual-socket EATX, up to 44 cores and 2 TB RAM) and the smaller Blackbird (single-socket micro-ATX, up to 8 cores and 256 GB RAM) run Ubuntu, Fedora, and OpenBSD with complete driver support.

The security value proposition is unmatched: every firmware byte is auditable and user-modifiable. For HelixCluster nodes running cryptographic key management or consensus algorithms, this auditability places OpenPOWER in a unique trust tier. However, the price is steep: the Blackbird board costs approximately $2,500, and an 8-core bundle approaches $1,600. POWER10 enterprise servers start at $43,000.

Raw performance per dollar is poor compared to used x86 or new ARM. The POWER9 Sforza cores are competitive with Zen 1 generation x86 but clock lower and consume more power. For HelixCluster, OpenPOWER should be reserved for Tier 4 specialized security nodes handling trust-critical functions, not general compute.

### 3.3.3 MIPS: Effectively Retired for General Compute

MIPS is functionally discontinued as a general-purpose compute architecture. Wave Computing, MIPS's owner, pivoted to RISC-V in 2021. Remaining MIPS relevance is limited to OpenWrt routers (MediaTek MT7621 and similar SoCs) and educational use in computer architecture courses. Loongson's pre-LoongArch chips used MIPS64, but the company has fully transitioned to its custom ISA.

No viable MIPS hardware exists for cluster deployment. OpenWrt MIPS routers may serve as network infrastructure---the GL.iNet MT6000's MT7621A predecessor was MIPS-based---but cannot function as compute nodes. HelixCluster requires no MIPS support in its build pipeline.

---

## 3.4 RISC-V Integration Architecture

### 3.4.1 Cross-Compilation Pipeline for the HelixCluster Agent

The HelixCluster agent---written in Go with Rust components for cryptographic operations---must compile for `linux/riscv64` as a standard release target. The recommended CI/CD pipeline produces statically linked binaries to avoid libc version mismatches across RISC-V distributions:

```bash
#!/bin/bash
# helixcluster-release-riscv64.sh
# Multi-component build for RISC-V64 target

set -euo pipefail
VERSION=${1:-$(git describe --tags --always)}
OUTDIR="dist/${VERSION}/linux-riscv64"
mkdir -p "${OUTDIR}"

# --- Go agent component ---
GOOS=linux GOARCH=riscv64 GORISCV64=rva22u64 CGO_ENABLED=0 \
  go build -ldflags "-s -w -X main.version=${VERSION}" \
  -o "${OUTDIR}/helix-agent-riscv64" ./cmd/agent

# --- Rust crypto component ---
cross build --target riscv64gc-unknown-linux-gnu --release \
  --manifest-path crypto/Cargo.toml

# --- Zig helper for RVV detection ---
zig build -Dtarget=riscv64-linux-musl \
  -Dcpu=baseline_rv64+rv64i+m+a+f+d+c+v \
  -Drelease-safe --prefix "${OUTDIR}/" helpers/rvv-detect/

# --- Verify architecture ---
file "${OUTDIR}/helix-agent-riscv64"
```

The pipeline uses `GORISCV64=rva22u64` to target the RVA22 profile, ensuring compatibility with the Jupiter's SpacemiT M1 while remaining backward-compatible with the Pioneer's C920 cores (which implement RVA20 plus vendor extensions). The `rvv-detect` Zig utility probes for vector extension support at runtime, enabling conditional dispatch of vector-optimized paths.

### 3.4.2 Capability Detection and Tier Assignment

When a RISC-V node joins HelixCluster, the agent runs a capability detection sequence that maps hardware features to workload tiers. The detection probes CPU cores, RAM, vector extensions, network throughput, and storage IOPS, then assigns the node to a tier that determines which workloads it may accept.

The tier assignment is expressed as a YAML configuration consumed by the scheduler:

```yaml
# riscv-tier-assignments.yaml
# HelixCluster tier mapping for RISC-V and emerging architectures
# Consumed by the node admission controller on agent registration

tiers:
  tier3_edge:
    description: "Edge/build nodes with adequate parallelism for batch workloads"
    match_any:
      - board: "milk-v-pioneer"
        min_cores: 32
        min_ram_gb: 32
        features: ["rv64gc", "multi-sata", "2.5gbe"]
        max_single_thread_latency_ms: 500
        workloads: ["ci-build", "package-assembly", "log-aggregation", "relay"]
      - board: "loongson-3a6000"
        min_cores: 4
        min_ram_gb: 8
        features: ["loongarch64", "lasx"]
        workloads: ["web-services", "file-serving", "edge-api"]

  tier4_experimental:
    description: "Developer/test nodes with limited production workload eligibility"
    match_any:
      - board: "sifive-p550"
        min_cores: 4
        min_ram_gb: 16
        features: ["rv64gc", "pcie-gen3"]
        max_single_thread_latency_ms: 2000
        workloads: ["dev-services", "risc-v-testing", "documentation-build"]
      - board: "milk-v-jupiter"
        min_cores: 8
        min_ram_gb: 4
        features: ["rv64gc", "rvv1.0", "poe"]
        workloads: ["ai-inference-edge", "sensor-aggregation", "protocol-bridge"]
      - board: "visionfive2"
        min_cores: 4
        min_ram_gb: 2
        features: ["rv64gc", "gbe"]
        max_node_concurrency: 2
        workloads: ["iot-bridge", "health-check-probe"]
      - board: "raptor-blackbird"
        min_cores: 4
        min_ram_gb: 16
        features: ["power9", "open-firmware"]
        trust_level: "trusted"
        workloads: ["key-management", "consensus-participant", "audit-logger"]

capability_probes:
  vector_extensions:
    command: "/usr/lib/helix/rvv-detect"
    outputs:
      rvv_1.0: { enable_workloads: ["vector-ml", "crypto-accelerated"] }
      rvv_0.7.1: { note: "pre-ratification; disable vector paths" }
      none: { fallback: "scalar-only" }

  memory_bandwidth:
    command: "dd if=/dev/zero bs=1M count=512 | mbw 256"
    thresholds_mbps:
      - { min: 5000,  label: "high" }
      - { min: 1000,  label: "medium" }
      - { min: 0,     label: "low", throttle_concurrency: true }

  network_throughput:
    command: "iperf3 -c gateway.helix.local -t 10"
    thresholds_gbps:
      - { min: 2.0, label: "2.5gbe", tier_bonus: "tier3" }
      - { min: 0.8, label: "gbe", tier_bonus: "tier4" }

scheduling_constraints:
  riscv_nodes:
    max_control_plane_components: 0
    etcd_eligible: false
    gpu_workloads: false
    require_anti_affinity_with: ["tier1", "tier2-critical"]
    notes: "RISC-V workers must not host control plane or latency-critical services"
```

This configuration encodes several architectural decisions. The Pioneer is the only RISC-V board rated for Tier 3 edge workloads, restricted to embarrassingly parallel tasks like CI builds. The Jupiter's RVV 1.0 support unlocks vector-ML workloads for edge AI inference. The VisionFive 2 and Mars are capped at two concurrent workloads and restricted to IoT bridging. The OpenPOWER Blackbird is the only emerging-architecture node trusted for consensus and key management, reflecting its fully auditable firmware.

The `scheduling_constraints` block prevents RISC-V nodes from hosting Kubernetes control plane components or etcd, avoiding latency-critical infrastructure on hardware whose single-threaded performance is an order of magnitude below Tier 1-2 standards.

### 3.4.3 Future-Proofing: RISC-V Vector Extensions and the RVA23 Roadmap

The RISC-V landscape will shift significantly in 2026-2027 with the arrival of RVA23-profile processors. The SiFive P870---sampling in 2025 with commercial boards expected in 2026---claims >2 SpecInt20017 per GHz and scales to 256 cores, with full RVV 1.0 vector support at 2x128-bit VLEN. Tenstorrent's Ascalon processor also targets RVA23 with competitive per-thread performance. Both are manufactured on 5-7 nm nodes and should narrow the 2.5x per-core gap with ARM Neoverse.

Industry projections estimate the RISC-V market growing from $1.1 billion (2023) to $7+ billion (2030). For HelixCluster, this means RISC-V should transition from Tier 4 experimental to Tier 2-3 production-viable between 2027 and 2028.

The recommended future-proofing strategy has three components. First, maintain the `linux/riscv64` CI build target now so the cluster agent deploys without porting work when RVA23 hardware arrives. Second, use the `GORISCV64=rva22u64` baseline today but prepare an `rva23u64` build path that enables vector crypto and enhanced atomic instructions. Third, deploy 2-3 Milk-V Jupiter boards as active Tier 4 nodes to validate real-world container behavior on RVV-capable hardware before performance-class chips arrive.

HelixCluster's emerging architecture support is not about immediate performance. It is about ensuring that when RISC-V closes the gap with ARM---as ARM closed the gap with x86 a decade ago---the software and operational playbooks are already in place.


---

## 4. FPGA & Programmable Logic Compute

Field-Programmable Gate Arrays (FPGAs) occupy a distinctive position in the HelixCluster compute hierarchy. Unlike fixed-architecture CPUs, GPUs, or NPUs, FPGAs offer **reconfigurable hardware** that can be tailored to specific workloads at the gate level. A single FPGA can transform from a network packet processor into a cryptographic accelerator, a signal-processing pipeline, or a multi-core RISC-V cluster node -- all by loading a different bitstream. This chapter evaluates the practical platforms available in 2025, from $15 Lattice boards to $250 AI-accelerated SoCs, and defines how each fits into the HelixCluster tier model.

The central question for cluster integration is whether an FPGA board can run Linux and execute a HelixCluster agent. The answer is yes -- through three distinct paths: hard-processor SoCs (ARM cores integrated with FPGA fabric), soft-core RISC-V implementations synthesized directly into programmable logic, and hybrid configurations where a nearby CPU host orchestrates an FPGA accelerator. Each path carries different implications for performance, tooling complexity, and trust classification.

### 4.1 FPGA Hardware Platforms

#### 4.1.1 Xilinx/AMD Zynq Series, Intel Cyclone V SoC, Lattice ECP5

The FPGA landscape for cluster computing divides into three families. **Xilinx/AMD Zynq** combines ARM hard processors with FPGA fabric on a single die. The Zynq-7000 series pairs dual-core Cortex-A9 (up to 667 MHz) with Artix-7 programmable logic; UltraScale+ MPSoC upgrades to quad-core A53 plus dual R5F real-time cores with significantly more logic. The Zynq ecosystem's maturity makes it the most predictable integration path -- boards like the PYNQ-Z2 ($129-199), ZUBoard 1CG ($159), and KV260 ($249) run standard Linux distributions with no FPGA development required for basic cluster participation.

**Intel/Altera Cyclone V SoC** similarly fuses dual Cortex-A9 cores with Altera FPGA fabric. The DE10-Nano, powered by this architecture, provides 110K logic elements and is one of the most widely used boards in academic FPGA cluster research. Intel's Quartus Prime toolchain remains proprietary but offers a free Lite edition sufficient for most cluster-node designs.

**Lattice ECP5** represents the open-source frontier. Unlike Xilinx and Intel devices, the ECP5 is fully supported by the open-source toolchain chain Yosys -> nextpnr -> prjtrellis -- no proprietary software required. The ECP5-25K and ECP5-85K power boards from $15 to $250. While it cannot match Zynq's hard-processor convenience, its complete toolchain openness provides unprecedented hardware auditability: every gate in the design is inspectable and verifiable.

#### 4.1.2 DE10-Nano ($190): Best Price/Performance for Linux-Capable FPGA

The Terasic DE10-Nano, at $190 academic ($225 retail), is the most balanced entry point for a Linux-capable FPGA cluster node. Its specifications -- dual Cortex-A9 at 800 MHz, 1 GB DDR3, GbE, HDMI, and 110K logic elements -- provide sufficient resources to run standard ARM Linux while retaining substantial fabric for workload-specific acceleration.

The DE10-Nano's value strengthens when compared within the Zynq ecosystem. The PYNQ-Z2 uses the same Zynq-7020 SoC (2x A9, 85K logic cells) but offers only 512 MB RAM versus the DE10-Nano's 1 GB. For cluster nodes where RAM determines dataset and model size limits, the additional memory justifies the modest premium. The GbE connects directly to the hard processor system, providing standard Linux networking with no FPGA fabric consumption. This means a DE10-Nano can join HelixCluster as a conventional ARM node immediately, with FPGA acceleration added incrementally as custom bitstreams are developed.

#### 4.1.3 Colorlight 5A-75B ($15): Cheapest FPGA Running Linux

At $15-25 from Chinese vendors, the Colorlight 5A-75B is the cheapest entry point into FPGA computing. Originally an LED display controller, it carries a Lattice ECP5-25K FPGA, dual GbE PHYs, and 2 MB SDRAM. For the cost of a microcontroller, you receive a programmable logic platform capable of hosting a soft-core RISC-V processor running Linux.

The 2 MB SDRAM is the primary constraint -- general-purpose Linux requires more memory, so practical deployment typically involves external memory expansion or running a minimal Zephyr RTOS agent instead of full Linux. The ULX3S ($155-250) addresses this with 32 MB SDRAM, WiFi via ESP32, and full USB support, making it the more practical open-source option for soft-core cluster nodes.

The Colorlight's dual GbE is architecturally significant: both PHYs connect directly to FPGA fabric, enabling custom network switching, line-rate packet processing, and distributed computing topologies implemented entirely in hardware. For edge-gateway applications, the Colorlight offers unmatched price-performance.

### 4.2 FPGA as Compute Accelerator

#### 4.2.1 Soft-Core CPUs: PicoRV32, VexRiscv, Rocket Chip

When an FPGA lacks a hard processor, programmable logic can host a synthesized CPU. Three RISC-V soft-cores dominate the open-source landscape in 2025.

**PicoRV32**, in a single Verilog file, occupies ~1,000 LUTs at ~170 MHz. Its lack of MMU precludes Linux support, making it ideal for control-plane logic within larger designs but unsuitable as a cluster compute node.

**VexRiscv** offers a dramatically more capable option. The Linux-capable configuration uses ~2,883 LUTs, achieves 180 MHz on Artix-7 and ECP5 fabrics, and delivers 2.27 Coremark/MHz. Antmicro demonstrated a **quad-core SMP VexRiscv** on a Digilent Arty A7, consuming 70% of device resources at 100 MHz and booting Linux in ~4 seconds. An ECP5-85K could theoretically host 20+ VexRiscv cores, though practical limits suggest 4-8 is realistic.

**Rocket Chip**, Berkeley's 64-bit reference implementation, requires 5,000+ LUTs and achieves only 50-100 MHz on affordable FPGAs. It suits larger Artix-7 100T or Zynq UltraScale+ fabric better than cost-optimized nodes.

#### 4.2.2 Hard-Processor Approach: Zynq ARM Cores + FPGA Fabric

For immediate HelixCluster integration, hard-processor FPGAs offer the lowest-friction path. Zynq or Cyclone V SoC boards run standard ARM Linux, support standard networking, and execute HelixCluster agent binaries without modification. The FPGA fabric operates as a co-processor: ARM cores handle orchestration, networking, and general-purpose tasks, while the fabric accelerates specific workloads. Communication between the ARM Processing System (PS) and FPGA Programmable Logic (PL) occurs through AXI interfaces, with memory-mapped regions allowing the Linux kernel to transfer data to and from fabric accelerators as if they were peripheral devices.

This division maps cleanly onto HelixCluster's workload model. When a workload requiring FPGA acceleration arrives, the agent loads the appropriate bitstream (if not already resident) and streams data through the fabric via DMA or memory-mapped AXI transfers. The KV260 extends this pattern with its DPU IP -- ARM cores run the cluster agent while the DPU executes quantized neural network inference at 0.92 TOPS INT8 peak. The PYNQ framework further simplifies this model by exposing FPGA overlays as Python libraries, though at the cost of a heavier software stack.

#### 4.2.3 AI Inference on FPGA: KV260 Benchmarks vs Jetson Orin

The KV260's DPU B3136 achieves 0.92 TOPS peak INT8 at ~7.9W total board power, translating to ~17 FPS for YOLOX and ~140 FPS for ResNet-50 (with the larger B4096 DPU at 300 MHz). Direct TOPS comparisons favor the Jetson Orin Nano Super at 40-67 TOPS, but FPGAs excel in custom quantization flexibility, deterministic latency, and power efficiency at low batch sizes. Research shows the KV260 consumes approximately five times less energy than Jetson Nano for equivalent INT8 inference. For latency-sensitive or custom-precision workloads, FPGA nodes justify their place alongside GPU-accelerated alternatives in a heterogeneous cluster.

| Board | Device | Hard CPU | Logic | RAM | GbE | Price | Best Use Case |
|-------|--------|----------|-------|-----|-----|-------|---------------|
| **Colorlight 5A-75B** | ECP5-25K | None (soft-core) | 25K LUTs | 2 MB | 2x (fabric) | $15-25 | Edge gateway, packet processing |
| **ULX3S** | ECP5-12K/85K | None (soft-core) | 12-84K LUTs | 32 MB | WiFi (ESP32) | $155-250 | Open-source RISC-V node, audited compute |
| **PYNQ-Z2** | XC7Z020 | 2x A9 @ 667 MHz | 85K cells | 512 MB | 1x (HPS) | $129-199 | Budget Zynq worker, Python prototyping |
| **DE10-Nano** | 5CSEBA6 | 2x A9 @ 800 MHz | 110K LEs | 1 GB | 1x (HPS) | $190 | Best value Linux-capable FPGA worker |
| **ZUBoard 1CG** | ZU1CG | 2x A53 + 2x R5F | 81K cells | 1 GB | 1x | $159 | Entry UltraScale+, modern cores |
| **KV260** | XCK26 | 4x A53 + 2x R5F | 256K cells | 4 GB | 1x | $249 | AI inference with DPU acceleration |
| **ZCU102** | ZU9EG | 4x A53 + 2x R5F | 600K cells | 4.5 GB | 4x | $2,995 | HPC research, 10GbE+ backbone |

### 4.3 FPGA Cluster Integration

#### 4.3.1 Open-Source Toolchain: Yosys, nextpnr, LiteX for Custom SoC Building

The open-source FPGA toolchain has matured to where complete SoCs can be built without proprietary software. **Yosys** performs RTL synthesis from Verilog 2005. **nextpnr** handles timing-driven placement and routing. Device-specific tools -- IceStorm (iCE40), prjtrellis (ECP5), prjxray (Xilinx 7-Series) -- generate final bitstreams. For Lattice iCE40 and ECP5, this toolchain is production-ready. For Xilinx 7-Series, the **OpenXC7** project demonstrated at FOSDEM 2025 a complete BOOT.BIN for Zynq-7000 built in five minutes without Vivado.

**LiteX** sits above this stack as a Python-based SoC builder, assembling CPU cores, memory controllers, Ethernet MACs, and storage interfaces into a cohesive design. The following configuration builds a HelixCluster-compatible node with VexRiscv soft-core, DDR memory, and GbE:

```python
# LiteX SoC configuration for HelixCluster FPGA node
# Target: ULX3S (ECP5-85K) or Colorlight i5 (ECP5U-25K + DDR3)

from litex_boards.platforms import ulx3s
from litex.soc.cores.cpu.vexriscv import VexRiscv
from litex.soc.integration.soc_core import SoCCore
from litex.soc.integration.builder import Builder
from litex.soc.cores.ethernet import LiteEthPHY
from litedram.modules import IS42S16160
from litedram.phy import GENSDRPHY

class HelixFPGANode(SoCCore):
    def __init__(self, platform, sys_clk_freq=50e6, **kwargs):
        SoCCore.__init__(self, platform, sys_clk_freq,
            cpu_type="vexriscv",
            cpu_variant="linux",      # MMU, caches, 4KB I$/D$
            with_uart=True,
            with_timer=True,
            **kwargs)

        # DDR/SDRAM memory controller
        self.submodules.ddrphy = GENSDRPHY(
            platform.request("sdram"), sys_clk_freq)
        self.add_sdram("sdram",
            phy=self.ddrphy,
            module=IS42S16160(sys_clk_freq),
            origin=self.mem_map["main_ram"],
            l2_cache_size=8192)

        # Gigabit Ethernet MAC via LiteEth
        self.submodules.ethphy = LiteEthPHY(
            clock_pads=platform.request("eth_clocks"),
            pads=platform.request("eth"),
            clk_freq=sys_clk_freq)
        self.add_ethernet(phy=self.ethphy, dynamic_ip=True)

        # SPI Flash for bitstream + kernel storage
        self.add_spi_flash(module=W25Q128JV, mode="4x")

# Build for ULX3S ECP5-85K with fully open-source toolchain
platform = ulx3s.Platform(toolchain="trellis")
soc = HelixFPGANode(platform, sys_clk_freq=50e6,
    integrated_rom_size=0x10000,      # 64KB BIOS
    integrated_main_ram_size=0x10000000)  # 256MB mapped
builder = Builder(soc,
    output_dir="build/helix_fpga_node",
    compile_software=True,
    compile_gateware=True)
builder.build()
```

This produces a fully open-source SoC: VexRiscv CPU with MMU, DDR controller with L2 cache, GbE MAC with DHCP, and SPI flash storage -- all synthesized through Yosys and nextpnr-trellis with zero proprietary tools.

| FPGA Family | Synthesis | Place & Route | Bitstream | Open-Source Status |
|-------------|-----------|---------------|-----------|-------------------|
| Lattice iCE40 | Yosys | nextpnr | IceStorm | Mature, complete |
| Lattice ECP5 | Yosys | nextpnr | prjtrellis | Mature, complete |
| Lattice Nexus | Yosys | nextpnr | Project Oxide | Good progress |
| Gowin GW1N | Yosys | nextpnr | Project Apicula | Active development |
| Xilinx 7-Series | Yosys | nextpnr-xilinx | prjxray | Rapidly advancing (OpenXC7) |
| AMD UltraScale+ / Versal | Vivado / Vitis | Vivado / Vitis | Vivado / Vitis | Proprietary only |

#### 4.3.2 Partial Reconfiguration as "FPGA Containers" -- Concept and Limitations

Dynamic Partial Reconfiguration (DPR) allows swapping portions of FPGA fabric at runtime without disrupting static regions. The container analogy is compelling -- a bitstream serves as the image, the ICAP/PCAP port as the runtime, physical spatial separation as isolation. A cluster scheduler could deploy accelerator bitstreams on demand, exchanging a cryptographic engine for a neural network DPU based on workload.

In practice, DPR is constrained: each reconfigurable module must be pre-compiled for specific partition boundaries established during initial floorplanning, and timing closure across boundaries remains challenging. DPR has been demonstrated for CNN accelerators and just-in-time overlays, but these remain research demonstrations rather than production infrastructure.

For HelixCluster, the practical near-term approach is **full bitstream swapping** -- storing multiple configurations in SPI flash and loading the appropriate image at boot or via warm-reload. This sacrifices sub-second switching latency but eliminates floorplanning complexity. As OpenXC7 matures and DPR tooling improves, partial reconfiguration can be revisited as a mid-term enhancement.

#### 4.3.3 Networking: Ethernet MAC in FPGA, 10GbE+ Capability

Hard-processor FPGAs provide native GbE through the ARM subsystem without consuming programmable logic. Soft-core nodes require **LiteEth**, the open-source Ethernet MAC in the LiteX ecosystem supporting MII, RMII, and RGMII PHYs. On the Colorlight 5A-75B, LiteEth drives dual GbE ports directly from ECP5 I/O pins, enabling standard TCP/IP cluster communication alongside custom line-rate packet processing.

For higher bandwidth, Xilinx UltraScale+ devices with GTH transceivers support 10 GbE SFP+ directly from the FPGA fabric without external NICs. The ZCU102 (4x GbE plus PCIe Gen3 x4) and Trenz TE0808 (16x GTH transceivers) provide backplane-class connectivity for high-bandwidth cluster backbones. At this performance tier, FPGAs can implement custom switch fabrics, RDMA-like memory access across nodes, and congestion-aware routing algorithms in hardware -- capabilities that would require expensive SmartNICs or dedicated switch hardware on conventional architectures. This positions high-end FPGAs as network infrastructure nodes within HelixCluster deployments, not merely compute endpoints.

### 4.4 FPGA Integration Architecture

#### 4.4.1 Tier Assignment: STANDARD for Zynq, EDGE for Soft-Core Only

HelixCluster classifies FPGA nodes based on processor architecture, toolchain trustworthiness, and operational predictability. Hard-processor FPGAs running standard ARM Linux receive **STANDARD** tier designation, qualifying for general-purpose workloads with the same trust assumptions as other ARM nodes. Soft-core-only FPGAs, where the CPU is synthesized into programmable logic, receive **EDGE** tier status -- suitable for semi-trusted gateway roles where hardware auditability compensates for reduced performance.

| Tier | Classification | Board Examples | Integration Path | Workload Types |
|------|---------------|----------------|------------------|----------------|
| **STANDARD** | Trusted compute node | DE10-Nano, PYNQ-Z2, KV260, ZUBoard 1CG | Hard ARM cores run standard Linux + HelixCluster agent natively | General compute, AI inference, video analytics, crypto acceleration |
| **EDGE** | Semi-trusted / audited | ULX3S, Colorlight 5A-75B, Colorlight i5 | Soft-core RISC-V (VexRiscv) via LiteX; custom Linux build; RISC-V agent binary | Packet processing, signal acquisition, protocol conversion, lightweight gateway |
| **ACCELERATOR** | Specialized offload | KV260 DPU mode, custom Zynq bitstreams | ARM agent + FPGA fabric co-processor via mmap/DMA | DPU inference, custom CNN engines, real-time signal processing |
| **EXPERIMENTAL** | Research / development | ZCU102, TE0808, Versal VEK280 | Vendor tools required; custom integration | Custom accelerator IP, 10GbE+ networking, partial reconfiguration trials |

The STANDARD tier reflects unmodified Linux distributions with standard package managers, GbE networking, and conventional ARM toolchains -- operational friction near zero. The EDGE tier for soft-core RISC-V acknowledges both strengths and constraints: the fully open-source flow provides hardware-level auditability impossible with proprietary tools, but ~180 MHz clock speeds, limited RAM (32-64 MB typical), and the RISC-V software ecosystem cap practical throughput. These nodes excel at security-sensitive gateway roles, cryptographic operations, and environments where supply-chain verification outweighs raw performance.

#### 4.4.2 Workload Types Suited for FPGA

FPGAs contribute capabilities to a heterogeneous cluster that fixed architectures cannot replicate.

**Cryptography** benefits from custom bit-width arithmetic and parallel-pipelined hashing. SHA-256, AES, and elliptic-curve operations can be unrolled across fabric to achieve throughputs impossible on general-purpose CPUs, with keys physically isolated within programmable logic.

**Signal Processing** maps naturally to FPGA architecture. FIR filters, FFT, and digital down-conversion execute with deterministic, nanosecond-predictable latency. A HelixCluster node with FPGA and RF frontend can function as a distributed spectrum sensor or signal-analysis endpoint.

**Custom Protocol Implementation** is perhaps the most distinctive FPGA capability. Where standard CPUs execute protocol stacks in software, FPGAs implement entire protocols -- custom framing, error correction, encryption, routing -- in hardware at line rate. A Colorlight 5A-75B with dual GbE can serve as a protocol-translation gateway between a HelixCluster mesh and legacy industrial networks, satellite downlinks, or proprietary sensor buses.

**AI Inference at Custom Precision** represents the emerging frontier. While Jetson Orin Nano dominates general-purpose TOPS-per-watt, FPGAs excel at non-standard quantization -- ternary weights, binary neural networks, and custom fixed-point formats that reduce model size beyond GPU INT8 capabilities. The KV260's DPU supports INT8 and INT16; more experimental Zynq implementations have demonstrated sub-INT8 inference with accuracy trade-offs viable for specific edge-analytics scenarios.

**Multi-Core Cluster-on-Chip** offers a preview of future integration. A single ECP5-85K hosting quad-core SMP VexRiscv becomes four small compute nodes on one chip, each running independent Linux or Zephyr RTOS. While individual core performance is modest (~2.57 Coremark/MHz at 180 MHz), the aggregate throughput of four parallel cores with custom interconnect and shared accelerators creates a unique "many-small-nodes" architecture. The onboard FPGA fabric can implement custom cache-coherence protocols, message-passing networks, or even lightweight DMA engines between cores -- capabilities impossible to add to fixed silicon. For HelixCluster, this suggests a future where individual FPGA boards function as micro-clusters, running multiple lightweight agents and routing workloads between soft cores and hardware accelerators under a single physical node's coordination.

The practical Phase 1 deployment recommendation is straightforward: procure two to three DE10-Nano boards as baseline STANDARD-tier FPGA workers, supplemented by one KV260 for AI acceleration evaluation. A single ULX3S or Colorlight i5 running LiteX+VexRiscv serves as the EDGE-tier prototype, validating the fully open-source integration path. This modest investment -- under $800 total -- establishes FPGA capabilities within the cluster while the toolchain, especially OpenXC7 for Zynq-7000, continues maturing toward full open-source parity with proprietary alternatives.


---

## 5. Enterprise, Server & Cloud Nodes

The highest-performance tier of HelixCluster nodes comes from enterprise, server, and cloud hardware. Where a Steam Deck contributes 4–15 watts of mobile compute and an ARM SBC adds 5–25 watts, a single used EPYC server can deliver 64–128 cores at 150–360 watts — the compute equivalent of twenty to forty edge nodes in one chassis. This chapter maps the full landscape, from $65 used EPYC processors pulled from decommissioned hyperscale racks to $0.001-per-vCPU-per-hour cloud spot instances that materialize on demand and vanish two minutes later.

The used server market in 2025–2026 is historically unprecedented. Hyperscalers refreshing for AI workloads have flooded secondary markets with DDR4-based AMD EPYC and Intel Xeon systems at fractions of original cost. A 64-core EPYC 7742 that listed for $6,950 in 2019 now trades for under $750. A complete 64-core server with 128 GB RAM and NVMe storage can be assembled for less than a high-end laptop. Simultaneously, ARM servers — led by Ampere's Altra family — have matured into production-ready platforms with full upstream Linux support, and cloud spot pricing has fallen to levels where ephemeral compute is cheaper than the electricity to run owned hardware for bursty workloads.

The strategic question is not whether to use enterprise-grade nodes, but which tier to deploy and when cloud burst capacity complements on-prem hardware. This chapter answers that with hardware recommendations, total cost of ownership models, production-ready spot-preemption code, and WireGuard mesh configurations for hybrid cloud–on-prem clustering.

---

### 5.1 ARM Servers

ARM servers have transitioned from cloud-only abstractions to physically obtainable hardware. For builders willing to navigate a smaller ecosystem, ARM offers exceptional core density per watt and per dollar.

#### 5.1.1 Ampere Altra / Altra Max: 80–128 Cores

The Ampere Altra family, built on Arm Neoverse N1 cores at TSMC 7nm, was the first production ARM server platform to achieve widespread adoption. The Altra Q80-30 provides 80 cores at 150W TDP, with 8-channel DDR4-3200 memory (up to 4 TB) and 128 lanes of PCIe Gen4. It matches a dual-socket Intel Xeon Silver in memory bandwidth while consuming half the power. The Altra Max M128-30 pushes this to 128 cores at 183W TDP — a core count requiring a $15,000 AMD EPYC 9965 or dual Xeon Platinum to match in x86.

Retail pricing for the Q80-30 sits around $1,689 new, but used and liquidation pricing trends toward $800–1,200. The M128-30 bundle retails near $2,500 but has appeared in secondary markets at $1,200–2,000. Available systems include the Mt. Snow single-socket 2U (up to 4 GPUs, 24 NVMe bays), the Mt. Jade dual-socket platform (256 cores total, Arm SystemReady LS certified), and rackmount options from Gigabyte, Supermicro, and ASRock Rack.

Linux compatibility is excellent — full upstream kernel support since 5.10+, certified for Ubuntu, RHEL, SUSE, and Debian. LinuxBoot is supported for open-source firmware. Virtualization via KVM and Xen works without patches. The caveats: ARM-specific binaries are required (though most modern software publishes aarch64 builds), some proprietary enterprise tools lack ARM ports, and single-threaded performance is roughly Intel Skylake-grade rather than modern Zen 4. For containerized workloads, CI/CD runners, and distributed storage, these limitations rarely matter.

#### 5.1.2 AWS Graviton 3/4: Cloud Reference

AWS Graviton provides the benchmark for evaluating ARM without hardware commitment. Graviton3 (Neoverse V1, 64 cores, 307 GB/s bandwidth) powers the c7g/m7g/r7g families. Graviton4 (Neoverse V2, 96 cores, 537 GB/s bandwidth, DDR5-5600) delivers ~30% better price-performance. Graviton4's improvements are substantial — 50% more cores, 75% more memory bandwidth, and real-world gains of 40% in database workloads. One curious finding: Graviton3's 256-bit SVE SIMD registers make it paradoxically faster than Graviton4 (128-bit SVE) for certain vector search workloads.

#### 5.1.3 Performance vs. x86 Benchmarks

At the socket level, an Altra Max M128-30 delivers roughly 75,000–85,000 PassMark points — comparable to a 64-core EPYC 7742 (~81,500 points). Per-core, each Neoverse N1 core achieves ~600–700 PassMark points versus 1,200–1,400 for Zen 4 or Sapphire Rapids. The ARM advantage is density and efficiency: 128 cores at 183W enables hosting 200–400 lightweight containers simultaneously, making the Altra Max exceptional for microservice aggregation and edge-to-core relay nodes.

---

### 5.2 Used x86 Servers

The used x86 server market is the single best source of raw compute capacity for HelixCluster. With careful procurement, builders achieve core counts and memory bandwidths that dwarf consumer hardware at comparable or lower prices.

#### 5.2.1 AMD EPYC 7002/7003 Series

AMD EPYC dominates used server value. The 7002 "Rome" series (Zen 2, SP3 socket) offers the best price per core. The EPYC 7551 — 32 cores, 180W — trades for $65–75 used, yielding ~$2.10 per core. The 64-core EPYC 7742 costs $500–750 used and provides 128x PCIe Gen4 lanes unmatched by any consumer platform. A complete 7742 build (CPU, Supermicro H11SSL-i motherboard, 128 GB DDR4, 1 TB NVMe, PSU, case) totals $900–1,100.

The 7003 "Milan" series (Zen 3) adds 15–20% IPC improvement and higher boost clocks. The 64-core EPYC 7713 at $800–1,000 used offers the best balance of modern performance and core density, compatible with most Rome motherboards after a BIOS update. Platform costs remain reasonable: Supermicro H11SSL-i boards trade at $200–400, and DDR4 RDIMM ECC 32 GB modules cost $30–40 each.

For builders seeking the bleeding edge, used EPYC 9004 "Genoa" (Zen 4, SP5 socket) processors have begun appearing at compelling prices. The 96-core EPYC 9654 trades at $1,500–2,000 used — roughly $18 per core for a 12-channel DDR5 platform with 128x PCIe Gen5 lanes.

#### 5.2.2 Intel Xeon E5 v3/v4 and Scalable

Intel's massive installed base creates a liquid used market. Legacy Xeon E5 v3 (Haswell) and v4 (Broadwell) processors are extraordinarily cheap: the E5-2697 v4 (18 cores) costs $50–100, and the E5-2699 v4 (22 cores) costs $100–150. Dual-socket platforms yield 36–44 total cores for under $200 in CPU costs, with Supermicro X10DRI motherboards at $150–250. DDR4 RDIMMs for these platforms are plentiful and cheap, making full builds accessible even with minimal budget. These systems are power-hungry (250–400W platform) but for always-on infrastructure services — monitoring, DNS, DHCP, internal relays — the cost-effectiveness is unmatched.

Newer Intel Xeon Scalable (Sapphire Rapids, Emerald Rapids) offer AMX instructions accelerating INT8 inference, making them competitive for AI serving when obtained at used discount. However, for general containerized compute, used EPYC generally offers superior core density and PCIe lane availability at equivalent price points.

#### 5.2.3 Threadripper PRO: Workstation Alternative

AMD Threadripper PRO bridges desktop and server. The 5995WX (64 Zen 3 cores, 8-channel DDR4, 128x PCIe Gen4) trades at $2,500–3,500 used and pairs with WRX80 motherboards around $1,000. The 7975WX (32 Zen 4 cores, 8-channel DDR5) at $1,500–2,000 used offers higher single-threaded performance. For HelixCluster, Threadripper PRO nodes excel as development workstations doubling as cluster members — CI/CD build servers, local AI inference with GPU, and video transcoding. Rackmount chassis conversions are available for 24/7 deployment, though workstation cooling must be upgraded for sustained loads.

**Table 1: Server Price-per-Core Comparison**

| Hardware Option | Cores | System Price* | $/Core | TDP | Best For |
|----------------|-------|--------------|--------|-----|----------|
| Used EPYC 7551 | 32 | ~$350 | $10.94 | 180W | Entry-level compute node |
| Used EPYC 7742 | 64 | ~$900 | $14.06 | 225W | High-density container host |
| Used EPYC 7713 | 64 | ~$1,200 | $18.75 | 225W | Modern high-performance node |
| Ampere Altra Q80-30 | 80 | ~$1,500 | $18.75 | 150W | ARM-native container density |
| Ampere Altra Max M128 | 128 | ~$2,500 | $19.53 | 183W | Maximum core density per watt |
| EPYC 9654 (Genoa, used) | 96 | ~$2,000 | $20.83 | 360W | Modern x86, DDR5, PCIe Gen5 |
| Threadripper PRO 5995WX | 64 | ~$3,500 | $54.69 | 280W | Workstation + occasional server |
| Mac Mini M4 Pro | 14 | $1,399 | $99.93 | 35W | Silent dev node, AI inference |

*System price includes CPU, motherboard, 64 GB RAM, 1 TB NVMe, PSU, and case. Cloud spot excluded (zero CAPEX, ongoing OPEX).

---

### 5.3 Mini PCs & Compact Workstations

Not every node needs a rack. Mini PCs offer a compelling middle ground — compact enough for desk-side deployment, yet powerful enough to serve as mesh relays, build agents, and lightweight compute nodes. The critical differentiator is networking: most mini PCs ship with single Gigabit or 2.5GbE, insufficient for high-throughput mesh backhaul. One device breaks this pattern.

#### 5.3.1 Minisforum MS-01: Best Mini PC Cluster Node

The Minisforum MS-01, built around Intel's i9-13900H (14 cores, 20 threads, up to 5.4 GHz), is the standout compact workstation for HelixCluster. Its defining feature — dual 10GbE SFP+ ports via Intel X710 — provides 20 Gbps aggregate throughput, enabling direct high-speed node-to-node links via SFP+ DAC cables without a switch. Three M.2 slots support up to 6 TB NVMe. A PCIe x16 slot (x8 electrical) accommodates a half-height GPU such as the NVIDIA RTX A2000. Intel vPro enables remote management for headless deployments.

At $679 barebones ($550–700 used/promotional), the MS-01 occupies a unique position: 10GbE networking in a 1.8-liter chassis at one-third the cost of building a comparable small-form-factor server. For field offices, retail locations, and home labs serving as mesh relays, it provides core-node-grade networking in a backpack-sized package.

#### 5.3.2 Mini PC Comparison Table

**Table 2: Mini PC Comparison for HelixCluster**

| Metric | Minisforum MS-01 | ASUS NUC 14 Pro | Beelink SER9 Pro | Apple Mac Mini M4 Pro |
|--------|-----------------|-----------------|-----------------|----------------------|
| CPU | i9-13900H | Core Ultra 7 165H | Ryzen AI 9 HX 370 | M4 Pro (10P+4E) |
| Cores/Threads | 14/20 | 16/22 | 12/24 | 14/14 |
| TDP | 45W | 28W | 54W | 35W |
| Max RAM | 64 GB DDR5-5200 | 96 GB DDR5 | 64 GB LPDDR5X | 64 GB |
| Networking | 2x 10GbE SFP+, 2x 2.5GbE | 2x 2.5GbE, 2x TB4 | 1x 2.5GbE | 1x 10GbE, 2x TB5 |
| NVMe Slots | 3x M.2 | 3x (varied) | 1x M.2 | None (soldered) |
| PCIe Slot | x16 (x8 elec.) | None | None | None |
| Remote Mgmt | Intel vPro | Intel vPro | None | None |
| Price (new) | $679 | $869 | $999 | $1,399–2,199 |
| Used Range | $550–700 | $600–800 | $450–550 | $1,100–1,500 |
| **HelixCluster Score** | **9.5/10** | **7.0/10** | **7.5/10** | **7.0/10** |

The MS-01 wins decisively on networking and expandability. The NUC 14 Pro offers higher RAM capacity (96 GB) but is limited to 2.5GbE. The SER9 Pro provides the best integrated GPU (Radeon 890M) for light AI inference but lags on networking. The Mac Mini M4 Pro offers exceptional memory bandwidth (273 GB/s) and silent operation but is constrained by soldered storage, no expansion, and macOS licensing restrictions on datacenter deployment.

---

### 5.4 Cloud Spot Instance Integration

Cloud spot instances represent the most elastic tier of HelixCluster compute — materializing when needed, running at 70–90% below standard rates, and evaporating when capacity is reclaimed. Properly managed, they extend on-prem clusters with burst capacity. Improperly managed, they create chaos of interrupted workloads and lost state.

#### 5.4.1 AWS, Azure, and GCP Spot Pricing

Spot instances exploit excess cloud capacity. Real-world blended savings for Kubernetes workloads average 59–77%.

**Table 3: Cloud Spot Instance Pricing Comparison**

| Provider | Instance Type | vCPUs | RAM | On-Demand/hr | Spot/hr | $/vCPU/hr |
|----------|-------------|-------|-----|-------------|---------|-----------|
| AWS | c7g.large (Graviton3) | 2 | 4 GB | $0.0725 | ~$0.014 | $0.0070 |
| AWS | c7g.2xlarge (Graviton3) | 8 | 16 GB | $0.29 | ~$0.058 | $0.0073 |
| AWS | c7g.16xlarge (Graviton3) | 64 | 128 GB | $2.32 | ~$0.46 | $0.0072 |
| AWS | c8g.xlarge (Graviton4) | 4 | 8 GB | $0.182 | ~$0.036 | $0.0091 |
| AWS | m8g.metal-24xl (Graviton4) | 96 | 384 GB | $5.32 | ~$1.06 | $0.0110 |
| Azure | D8pds v6 (AMD EPYC) | 8 | 32 GB | $0.384 | ~$0.077 | $0.0096 |
| Azure | E96ads v6 (AMD EPYC) | 96 | 672 GB | $6.14 | ~$1.23 | $0.0128 |
| GCP | c3d-highcpu-16 (AMD EPYC) | 16 | 32 GB | $0.76 | ~$0.15 | $0.0094 |
| GCP | t2d-standard-48 (AMD EPYC) | 48 | 192 GB | $2.03 | ~$0.41 | $0.0085 |

Graviton (ARM) instances are 15–25% cheaper than comparable x86 spots with equal or better container performance. At $0.007–0.012 per vCPU per hour, a 64-vCPU spot node costs ~$110–150/month continuously — comparable to the electricity cost of running a 225W on-prem server in many regions.

#### 5.4.2 Preemption Handling: Go Checkpoint Handler

Spot instances can be reclaimed with minimal warning — 2 minutes on AWS, 30 seconds on Azure and GCP. HelixCluster must handle this through checkpointing, draining, and mixed replica strategies.

The following Go preemption handler implements the AWS Instance Metadata Service interruption watcher, checkpoint trigger, and graceful node drain. It compiles to a static binary suitable for any Linux spot instance.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "os/signal"
    "syscall"
    "time"
)

const (
    awsIMDSInterruptURL = "http://169.254.169.254/latest/meta-data/spot/instance-action"
    azureMetadataURL    = "http://169.254.169.254/metadata/scheduledevents?api-version=2020-07-01"
    gcpMaintenanceURL   = "http://metadata.google.internal/computeMetadata/v1/instance/maintenance-event"
    pollInterval        = 5 * time.Second
    drainTimeout        = 90 * time.Second
    checkpointDir       = "/var/lib/helixcluster/checkpoints"
)

type SpotAction struct {
    Action string `json:"action"`
    Time   string `json:"time"`
}

type PreemptionHandler struct {
    provider     string
    nodeID       string
    checkpointFn func(string) error
    drainFn      func(context.Context) error
}

func NewHandler(provider, nodeID string) *PreemptionHandler {
    return &PreemptionHandler{
        provider:     provider,
        nodeID:       nodeID,
        checkpointFn: defaultCheckpoint,
        drainFn:      defaultDrain,
    }
}

func (h *PreemptionHandler) Run(ctx context.Context) error {
    log.Printf("[helix-spot] Starting watcher on %s node %s", h.provider, h.nodeID)
    ticker := time.NewTicker(pollInterval)
    defer ticker.Stop()

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case sig := <-sigCh:
            log.Printf("[helix-spot] Signal %v, initiating shutdown", sig)
            return h.handlePreemption(ctx, "signal")
        case <-ticker.C:
            if detected, reason := h.checkPreemption(); detected {
                log.Printf("[helix-spot] Preemption detected: %s", reason)
                return h.handlePreemption(ctx, reason)
            }
        }
    }
}

func (h *PreemptionHandler) checkPreemption() (bool, string) {
    switch h.provider {
    case "aws":
        return h.checkAWS()
    case "azure":
        return h.checkAzure()
    case "gcp":
        return h.checkGCP()
    default:
        return false, ""
    }
}

func (h *PreemptionHandler) checkAWS() (bool, string) {
    resp, err := http.Get(awsIMDSInterruptURL)
    if err != nil || resp.StatusCode == http.StatusNotFound {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    var action SpotAction
    if json.Unmarshal(body, &action) == nil && action.Action != "" {
        return true, fmt.Sprintf("AWS %s at %s", action.Action, action.Time)
    }
    return false, ""
}

func (h *PreemptionHandler) checkAzure() (bool, string) {
    req, _ := http.NewRequest("GET", azureMetadataURL, nil)
    req.Header.Set("Metadata", "true")
    resp, err := http.DefaultClient.Do(req)
    if err != nil || resp.StatusCode != http.StatusOK {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if len(body) > 50 {
        return true, "Azure scheduled event"
    }
    return false, ""
}

func (h *PreemptionHandler) checkGCP() (bool, string) {
    req, _ := http.NewRequest("GET", gcpMaintenanceURL, nil)
    req.Header.Set("Metadata-Flavor", "Google")
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return false, ""
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    if string(body) != "NONE" {
        return true, fmt.Sprintf("GCP maintenance: %s", string(body))
    }
    return false, ""
}

func (h *PreemptionHandler) handlePreemption(ctx context.Context, reason string) error {
    log.Printf("[helix-spot] === PREEMPTION: %s ===", reason)

    checkpointCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
    defer cancel()
    if err := h.checkpointFn(h.nodeID); err != nil {
        log.Printf("[helix-spot] Checkpoint error (continuing): %v", err)
    } else {
        log.Printf("[helix-spot] Checkpoint completed for node %s", h.nodeID)
    }

    drainCtx, cancel2 := context.WithTimeout(checkpointCtx, drainTimeout)
    defer cancel2()
    if err := h.drainFn(drainCtx); err != nil {
        log.Printf("[helix-spot] Drain error: %v", err)
    }

    log.Printf("[helix-spot] Node %s drained. Exiting for reclamation.", h.nodeID)
    return nil
}

func defaultCheckpoint(nodeID string) error {
    ts := time.Now().UTC().Format(time.RFC3339)
    checkpointFile := fmt.Sprintf("%s/%s-%s.json", checkpointDir, nodeID, ts)
    state := map[string]interface{}{
        "node_id":   nodeID,
        "timestamp": ts,
        "status":    "checkpointed",
        "workloads": []string{},
    }
    data, _ := json.MarshalIndent(state, "", "  ")
    return os.WriteFile(checkpointFile, data, 0644)
}

func defaultDrain(ctx context.Context) error {
    cmd := exec.CommandContext(ctx, "kubectl", "drain", "$(hostname)",
        "--ignore-daemonsets", "--delete-emptydir-data",
        "--grace-period=30", "--timeout=90s", "--force")
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func main() {
    provider := os.Getenv("CLOUD_PROVIDER")
    if provider == "" {
        provider = "aws"
    }
    nodeID := os.Getenv("NODE_ID")
    if nodeID == "" {
        hostname, _ := os.Hostname()
        nodeID = hostname
    }
    os.MkdirAll(checkpointDir, 0755)
    handler := NewHandler(provider, nodeID)
    if err := handler.Run(context.Background()); err != nil {
        log.Fatalf("[helix-spot] Handler exited: %v", err)
    }
}
```

**Production strategies:**

1. **Mixed replicas:** Maintain N baseline replicas on on-demand or on-prem; add M opportunistic spot replicas. If spot is reclaimed, service degrades gracefully rather than failing.
2. **Instance diversification:** Spread spot requests across families and availability zones to reduce correlated preemption risk.
3. **Incremental checkpointing:** Save progress every 60 seconds to object storage so interrupted jobs resume from near their termination point.
4. **Fallback:** Shift to on-demand when spot exceeds ~60% of on-demand price.

#### 5.4.3 WireGuard Mesh: Connecting Cloud to On-Prem

WireGuard provides a lightweight, kernel-implemented VPN between cloud spot instances and on-prem nodes. Modern cryptography (Curve25519, ChaCha20), minimal configuration (~10 lines versus hundreds for OpenVPN), and native roaming support make it ideal for ephemeral cloud nodes that change IPs during reconnection. Benchmarks consistently show WireGuard achieving 900 Mbps–1 Gbps throughput per core with minimal CPU overhead, sufficient to saturate a 10GbE link on a modern processor.

```
  On-Prem Cluster        WireGuard Tunnel        Cloud Spot Instances
  [HelixCluster]  <====  (UDP/51820)  ====>  [AWS/GCP/Azure VMs]
  [10.0.1.0/24]         Encrypted mesh          [10.0.2.0/24]
```

WireGuard deploys as a DaemonSet with `hostNetwork: true` in Kubernetes, establishing node-level mesh connectivity. Each spot instance receives a WireGuard configuration at boot via cloud-init, joining the cluster overlay within seconds of launch. PersistentKeepalive packets ensure NAT traversal survives behind cloud provider firewalls and home-router NAT.

**Cloud spot node WireGuard configuration:**

```ini
[Interface]
PrivateKey = <spot-node-private-key>
Address = 10.0.2.100/24
ListenPort = 51820

[Peer]
PublicKey = <gateway-public-key>
AllowedIPs = 10.0.1.0/24, 10.0.3.0/24
Endpoint = gateway.helixcluster.local:51820
PersistentKeepalive = 25
```

#### 5.4.4 TCO Breakeven Analysis

**Table 4: TCO Breakeven — Cloud Spot vs. Owned Hardware**

| Scenario | Cloud Spot Cost | Owned Hardware Cost | Breakeven | Recommendation |
|----------|----------------|--------------------|-----------|----------------|
| 64 vCPU continuous (730 h/mo) | $110–150/mo | $80–120/mo (EPYC 7742) | 18–24 months | Own if >20 h/day |
| 64 vCPU bursty (200 h/mo) | $35–50/mo | $80–120/mo | Cloud always wins | Spot only |
| 64 vCPU experimental (40 h/mo) | $7–10/mo | $80–120/mo | Cloud always wins | Spot only |
| GPU A100 continuous | $600–800/mo | $500–700/mo | 12–18 months | Own if sustained |
| GPU A100 bursty (100 h/mo) | $150–300/mo | $500–700/mo | Cloud always wins | Spot only |
| 256 vCPU burst peak | $440–600/mo | $320–480/mo (4x EPYC) | ~24 months | Hybrid: base + burst |

**Calculation methodology:** Owned hardware TCO includes CAPEX amortized over 36 months at 8% cost of capital, electricity at $0.12/kWh, maintenance reserve at 10% of CAPEX annually, and networking costs if applicable.

For the 64-vCPU continuous scenario (EPYC 7742 build, $1,000 system cost):
- Monthly amortization: $27.78
- Electricity (225W × 730h = 164.25 kWh): $19.71
- Maintenance reserve: $8.33
- **Total monthly TCO: $55.82** base, **$75–85** with cooling and rack space

The cloud spot equivalent costs $168/month continuous — roughly 2–3× owned cost. This inverts for bursty workloads: at 200 hours/month, cloud spot costs $46 while owned hardware still incurs full amortization plus idle electricity at $65–75 effective. Below ~300 hours per month of actual utilization, cloud spot is cheaper than owned hardware.

**Rule of thumb:** For steady-state 24/7 workloads, owned hardware breaks even at 18–30 months versus cloud spot. For bursty or experimental workloads, cloud spot is 3–5× more cost-effective. The optimal architecture is hybrid: on-prem servers form the persistent core; cloud spot extends elastically via WireGuard mesh.

---

### 5.5 GPU Compute Nodes

AI inference and training demand GPU acceleration. The HelixCluster GPU tier spans consumer cards for budget inference, datacenter GPUs for production, and cloud instances for burst training.

#### 5.5.1 NVIDIA: RTX 4090/5090, A100, H100 — CUDA Ecosystem

NVIDIA's CUDA ecosystem remains dominant for GPU-accelerated computing. The used GPU market in 2025 reflects a post-training-boom correction, with prices 60–70% below peak.

The **RTX 4090** (24 GB GDDR6X, 330 TFLOPS FP16) at $1,200–1,600 used delivers unmatched price-performance for inference but lacks ECC memory and NVLink. The **RTX 5090** (32 GB GDDR7, 450 TFLOPS FP16) at $1,800–2,500 pushes VRAM for larger quantized models. The **A100** is the rational production choice: used 40GB units at $4,800–6,000; refurbished 80GB at $4,800–8,500 — down from $18,000+ at peak. With 312 TFLOPS FP16, NVLink for multi-GPU scaling, and ECC memory, it is the minimum viable GPU for serving 70B+ parameter models. The **H100 SXM5** (80 GB HBM3, 989 TFLOPS FP16) at $6,000–15,000 used justifies its cost only for training or high-throughput inference where 3.35× A100 throughput directly translates to serving capacity. The **L40S** (48 GB GDDR6, 366 TFLOPS) at $3,000–5,000 used fills the gap between consumer and HBM-class cards.

#### 5.5.2 AMD Instinct MI300X — ROCm Ecosystem

AMD Instinct offers a viable alternative for ROCm-tolerant workloads. The **MI300X** (192 GB HBM3, 1.3+ PFLOPS FP16) at $11,000–15,000 used delivers 2.4× A100's VRAM, compelling for large-model inference. The **MI210** (64 GB HBM2e, 181 TFLOPS FP16) at $2,000–3,000 used is the value play — competitive with A100 at roughly half the price. ROCm 7.0 (2026) supports MI300X and MI210 with full PyTorch integration via HIP, though ecosystem breadth remains behind CUDA. For HelixCluster builders already running AMD EPYC hosts, pairing with MI-series GPUs creates a unified vendor stack with simpler driver maintenance.

#### 5.5.3 Cloud GPU Instances as Burst Compute

For intermittent training, cloud GPU instances provide elasticity without capital commitment. AWS g5 spots (NVIDIA T4) at $0.30–0.50/hr and p4d spots (8× A100) at $9–12/hr enable distributed training experiments requiring $50,000+ in owned hardware. The TCO analysis from Section 5.4.4 applies directly: cloud GPU is cost-effective under ~200–300 hours/month; continuous serving favors owned hardware breaking even at 12–18 months.

| GPU | VRAM | FP16 TFLOPS | Used Price | Best For |
|-----|------|-------------|-----------|----------|
| RTX 4090 | 24 GB | 330 | $1,200–1,600 | Budget inference, dev/test |
| RTX 5090 | 32 GB | 450 | $1,800–2,500 | Larger model inference |
| A100 40GB | 40 GB | 312 | $4,800–6,000 | Production inference |
| A100 80GB | 80 GB | 312 | $6,000–8,500 | Large model serving |
| H100 SXM5 | 80 GB | 989 | $6,000–15,000 | Training, high-throughput |
| L40S | 48 GB | 366 | $3,000–5,000 | Mixed inference + graphics |
| MI210 | 64 GB | 181 | $2,000–3,000 | ROCm workloads, value GPU |
| MI300X | 192 GB | 1,300+ | $11,000–15,000 | Maximum VRAM inference |

---

### Summary

Enterprise and server-grade hardware transforms HelixCluster from a collection of edge devices into a production compute platform. Used EPYC delivers 32–128 core nodes for $350–2,500 that anchor the control plane. Ampere Altra provides an alternative path to extreme core density at lower power. The Minisforum MS-01 with dual 10GbE extends high-performance networking to compact form factors. Cloud spot instances — integrated via WireGuard mesh and protected by the preemption handler — provide elastic burst at a fraction of on-demand pricing. GPU nodes from used A100s to ROCm-based AMD Instinct cards accelerate AI inference and training.

The optimal deployment is hybrid: on-prem EPYC and ARM servers form the persistent core, mini PCs provide distributed relay and caching, and cloud spot instances extend the cluster elastically. For steady-state 24/7 workloads, owned hardware breaks even at 18–30 months. For everything else, the cloud is cheaper — and with proper preemption handling, it is also reliable.


---

## 6. IoT, Smart Home & Edge Devices

The compute fabric of a modern home extends far beyond laptops and servers. Wi-Fi routers handle packet routing with quad-core ARM CPUs; network-attached storage devices run Docker containers on x86-class processors; smart TVs stream 4K video while their multi-core SoCs sit largely idle; and wearables process neural workloads on-wrist. This chapter maps the hidden compute topology of the connected home, identifying which devices can serve as genuine HelixCluster nodes and which remain inaccessible behind locked-down platforms.

A clear pattern emerges from the analysis: **openness beats raw performance at the edge**. The most valuable edge nodes are not the most powerful devices but the ones that expose a Linux environment, package management, and persistent background services to the operator. A $159 OpenWrt router with Docker contributes more to the cluster than a $400 smart speaker with no developer access. This principle guides every recommendation in the sections that follow.

---

### 6.1 Routers as Cluster Gateways

Routers are the unsung heroes of distributed edge computing. They are already always-on, already networked, and---in the OpenWrt ecosystem---already running full Linux. The critical insight is that modern routers separate the packet forwarding path from the general-purpose CPU. Hardware offloading engines handle NAT, VLAN tagging, and Wi-Fi frame aggregation, leaving the ARM CPU cores available for user-space processes. A quad-core Cortex-A53 router forwarding gigabit traffic may use less than 15% of its CPU cycles for routing, with the remainder available for cluster duties.

#### 6.1.1 GL.iNet MT6000: The Best Edge Node

The GL.iNet GL-MT6000 (Flint 2) is the single most cost-effective edge compute node identified across all Phase 5 research. At approximately $159, it delivers a MediaTek MT7986AV (Filogic 830) SoC with a quad-core ARM Cortex-A53 @ 2.0 GHz, 1 GB of DDR4-3200 RAM, 8 GB of eMMC 5.1 storage, and---critically---dual 2.5 GbE ports. No other device under $200 combines this level of compute, container support, and high-speed networking.

The 8 GB eMMC is the decisive differentiator. Most OpenWrt routers ship with 16--256 MB of flash storage, barely enough for the operating system and a few packages. The MT6000's 8 GB eMMC enables Docker deployment with room for multiple container images, a lightweight database, or cached cluster state. Installing Docker on OpenWrt 24.x is straightforward: `opkg install dockerd luci-app-dockerman` enables the full container toolchain, and community reports confirm stable operation of Nginx Proxy Manager, AdGuard Home, Pi-hole, and other multi-container workloads ^10^.

Network architecture is equally compelling. The dual 2.5 GbE ports function as WAN and LAN backhaul, while four additional Gigabit LAN ports provide downstream connectivity. WireGuard VPN throughput reaches 900 Mbps---sufficient for encrypted mesh backhaul between cluster segments---and OpenVPN achieves 190 Mbps for legacy compatibility ^11^. With typical power consumption under 20 W (from a 48 W peak adapter), the MT6000 delivers roughly 8--12 GFLOPS of FP32 compute per watt at a cost-per-GFLOPS-year that rivals dedicated SBCs.

#### 6.1.2 GL.iNet MT3000: Lightweight Relay

For smaller deployments or budget-constrained environments, the GL.iNet GL-MT3000 (Beryl AX) at approximately $89 provides a capable lightweight relay node. The MediaTek MT7981 (Filogic 820) offers a dual-core Cortex-A53 @ 1.3 GHz, 512 MB DDR3L, 256 MB NAND flash, one 2.5 GbE WAN port, two Gigabit LAN ports, and Wi-Fi 6 (AX) 2x2 connectivity ^12^.

The MT3000's 256 MB of flash precludes Docker, but its 512 MB RAM is sufficient for a native opkg-installed HelixCluster agent written in Go or Rust. The USB-C power input (5 V / 3 A) means the device can run from a power bank during outages, and its compact form factor suits travel or temporary deployments. In a HelixCluster topology, the MT3000 serves as a mesh relay, heartbeat coordinator, or lightweight data aggregator rather than a full compute worker. The price-to-reliability ratio is exceptional: at $89, it costs less than most unmanaged switches while providing a full Linux environment.

#### 6.1.3 Network Appliance Architecture: Router as Mesh Gateway

The architectural pattern for router-integrated HelixCluster nodes treats the device as a dual-function appliance: Layer 3 gateway for the local network and Layer 7 cluster participant for the global fabric. The OpenWrt host maintains these responsibilities in separate execution contexts.

**OpenWrt Agent Architecture:**

```
+------------------+        +------------------+
|  Hardware        |        |  Linux Kernel    |
|  MT7986AV SoC    |------->|  OpenWrt 24.x    |
|  4x A53 @ 2GHz   |        |  1GB DDR4        |
|  8GB eMMC        |        |  2.5GbE x2       |
+------------------+        +------------------+
                                     |
        +----------------------------+-------------------------+
        |                            |                         |
   [Netfilter/     [Wi-Fi     [Docker Engine           [opkg]
    HW Offload]    mac80211]   (containerd)]         [packages]
        |                            |                         |
   Routing Plane           +--------|---------+       [helix-agent]
   (iptables/nftables)     |        |         |         (native)
                           |   +----+----+    |
                           |   |  Agent  |    |
                           |   | Container    |
                           |   +----+----+    |
                           |        |         |
                     [AdGuard] [Prometheus] [Mesh]
                           |                 Proxy    |
                           +----------------+---------+
                                            |
                                    WireGuard Mesh Tunnel
                                    (900 Mbps encrypted)
                                            |
                                +-----------+-----------+
                                |                       |
                          [Regional Hub]          [Peer MT6000]
                          (NanoPi R6S/x86)        (Remote Site)
```

The routing plane operates in kernel space with hardware acceleration, isolated from the user-space cluster agent. The HelixCluster agent runs either as a Docker container (recommended for MT6000-class storage) or as a native opkg package (required for flash-constrained devices like the MT3000). The agent communicates with regional cluster hubs via a WireGuard mesh tunnel that encrypts all cluster traffic without interfering with local LAN forwarding.

For flash-constrained routers, a split architecture is viable: the router runs a minimal 5 MB native agent that handles heartbeat, task dispatch, and local sensor aggregation, while heavier workloads (container execution, log analysis, ML inference) are forwarded to a regional hub with more RAM and storage. This "thin agent, thick hub" model maximizes the number of edge participants without overwhelming limited router resources.

**Table 6.1: Edge Router Comparison**

| Router | CPU | RAM | Storage | 2.5GbE | Docker | Power | Price | Cluster Tier |
|--------|-----|-----|---------|--------|--------|-------|-------|-------------|
| GL.iNet MT3000 | 2x A53 @ 1.3 GHz | 512 MB | 256 MB NAND | 1x | No | ~5--7 W | $89 | T2 (relay) |
| **GL.iNet MT6000** | **4x A53 @ 2.0 GHz** | **1 GB** | **8 GB eMMC** | **2x** | **Yes** | **<20 W** | **$159** | **T1 (edge)** |
| ASUS TUF-AX6000 | 4x A53 @ 2.0 GHz | 512 MB | 256 MB | 2x | No | ~15 W | $220 | T2 (relay) |
| NanoPi R6S | 4x A76 + 4x A55 | 8 GB | 32--64 GB eMMC | 2x | Yes | 4.6--11.4 W | $129 | T1 (compute) |
| TP-Link Archer C7 | QCA9558 MIPS | 128 MB | 16 MB | No | No | ~10 W | $80 | T3 (incompatible) |

The NanoPi R6S deserves special mention as a router-form-factor compute node. It is not a traditional router but rather an RK3588S SBC packaged in a router enclosure with dual 2.5 GbE ports. Its 8 GB of LPDDR4X, 6 TOPS NPU, and 4.6 W idle power consumption make it the most powerful device in this category, though it requires more technical setup than the turnkey MT6000 ^13^.

---

### 6.2 NAS as Persistent Storage Nodes

Network-attached storage devices are ideal HelixCluster candidates: they are always-on, engineered for reliability, and increasingly powerful. A modern 4-bay NAS with an AMD Ryzen or Intel Celeron CPU, 32 GB of expandable RAM, and native Docker support is not merely a file server---it is a full member of the compute fabric.

#### 6.2.1 Synology DS923+: Full Cluster Member

The Synology DS923+ occupies the premium tier of home NAS devices. Its AMD Ryzen R1600 is a dual-core, four-thread processor with a 3.1 GHz boost clock and ECC RAM support---a rarity at this price point. The base configuration includes 4 GB of DDR4 ECC, expandable to 32 GB, and two M.2 NVMe slots that can serve as cache accelerators or independent storage pools ^14^.

Docker support is provided through Synology's Container Manager (formerly Docker), which offers a web-based UI for pulling images from Docker Hub, managing volumes, and configuring network bridges. The DS923+ can run multiple containerized HelixCluster agents simultaneously: a storage-backed worker for data-intensive tasks, a Prometheus node for metrics collection, and a MinIO instance for S3-compatible object storage within the cluster.

The optional E10G22-T1-Mini add-on card upgrades the dual 1 GbE ports to a single 10 GbE connection, transforming the DS923+ from a network client into a backbone-class storage server. For HelixCluster deployments handling large dataset shuffling---machine learning training data, video transcode queues, or scientific dataset mirrors---this 10x bandwidth increase is transformative. Power consumption ranges from 12 W in hibernation to 32 W under full access, making the DS923+ efficient for its compute class.

#### 6.2.2 QNAP TS-464 and TrueNAS Options

The QNAP TS-464 provides a compelling alternative at approximately $450. Its Intel Celeron N5095 quad-core processor bursts to 2.9 GHz and includes hardware-accelerated AES encryption. The TS-464 ships with dual 2.5 GbE ports (no add-on card required), 4--8 GB of DDR4 upgradable to 16 GB, and a PCIe Gen3 x2 slot for 10 GbE or Edge TPU expansion cards ^15^.

QNAP's Container Station supports Docker, LXD, and Kata Containers---the broadest container runtime support among consumer NAS devices. This flexibility allows operators to isolate cluster workloads at different security boundaries: Docker for trusted HelixCluster agents, LXD for semi-trusted community workloads, and Kata for untrusted third-party code requiring VM-level isolation.

TrueNAS SCALE (based on Debian Linux with Kubernetes via Helm) represents the open-source path. While TrueNAS hardware requires self-assembly or vendor integration (iXsystems Mini series), it offers native Docker/Kubernetes support and ZFS-based storage with unparalleled data integrity guarantees. For operators already running TrueNAS, adding a HelixCluster agent container is a single `docker run` command.

#### 6.2.3 Storage + Compute Dual-Role Architecture

The defining architectural pattern for NAS-integrated cluster nodes is the **storage-compute dual role**. The NAS continues to serve files via SMB/NFS/AFP to household clients while simultaneously donating idle CPU cycles, RAM, and disk I/O to the cluster. This is not a secondary function---it is a primary design principle enabled by modern NAS hardware and software.

**Table 6.2: NAS Cluster Node Comparison**

| NAS | CPU | RAM (max) | Networking | Docker | Storage Bays | Power | Price | Cluster Tier |
|-----|-----|-----------|------------|--------|-------------|-------|-------|-------------|
| Synology DS923+ | AMD R1600 (2C/4T) | 4--32 GB ECC | 2x 1 GbE + 10 GbE option | Yes (Container Manager) | 4 | 12--32 W | ~$550 | T1 (storage) |
| Synology DS224+ | Intel J4125 (4C) | 2--6 GB | 2x 1 GbE | Yes (Container Manager) | 2 | ~18 W | ~$300 | T1 (light) |
| QNAP TS-464 | Intel N5095 (4C) | 4--16 GB | 2x 2.5 GbE | Yes (Docker/LXD/Kata) | 4 | ~22 W | ~$450 | T1 (storage) |

#### NAS Docker Configuration

The recommended deployment for a Synology or QNAP NAS uses a `docker-compose.yml` file that defines the HelixCluster agent, persistent storage volumes, and resource constraints:

```yaml
# helix-agent-nas.yml
# Deploy: docker-compose -f helix-agent-nas.yml up -d
version: "3.8"

services:
  helix-agent:
    image: helixcluster/agent:latest
    container_name: helix-nas-agent
    restart: unless-stopped
    # Resource limits prevent the agent from impacting NAS file services
    deploy:
      resources:
        limits:
          cpus: "2.0"
          memory: 2G
        reservations:
          cpus: "0.5"
          memory: 256M
    environment:
      - HELIX_NODE_ROLE=storage-worker
      - HELIX_STORAGE_PATH=/data
      - HELIX_MESH_ENDPOINT=wss://hub.helix.local:8443
      - HELIX_WIREGUARD_PUBKEY=${WG_PUBKEY}
      - HELIX_MAX_TASKS=4
    volumes:
      # Persistent state for agent identity and cached data
      - /volume1/helix/config:/etc/helix:rw
      # Shared NAS folder for cluster-accessible storage
      - /volume1/helix/data:/data:rw
      # Read-only mount for NAS metrics export
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
    ports:
      - "9100:9100"   # Node exporter metrics
      - "7946:7946"   # Helix mesh gossip
      - "7946:7946/udp"
    # Run with elevated caps for network mesh and wireguard
    cap_add:
      - NET_ADMIN
      - NET_RAW
    sysctls:
      - net.ipv4.ip_forward=1
      - net.ipv4.conf.all.src_valid_mark=1

  # Optional: S3-compatible gateway for cluster object storage
  minio:
    image: minio/minio:latest
    container_name: helix-minio
    restart: unless-stopped
    command: server /data --console-address ":9001"
    environment:
      - MINIO_ROOT_USER=helix-cluster
      - MINIO_ROOT_PASSWORD=${MINIO_PASSWORD}
    volumes:
      - /volume1/helix/s3:/data:rw
    ports:
      - "9000:9000"
      - "9001:9001"
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 1G
```

This configuration pins the agent to 2 CPU cores and 2 GB of RAM, ensuring that DSM/QTS file services retain headroom for household clients. The `restart: unless-stopped` policy ensures the agent resumes after NAS reboots or DSM updates. Environment variables configure the node as a `storage-worker` role, which advertises high-capacity disk access to the cluster scheduler and receives data-intensive workloads (log aggregation, dataset preprocessing, model checkpoint storage) in preference to compute-only tasks.

Key operational considerations for NAS cluster nodes:

- **Update reboots**: DSM and QTS updates require system reboots, causing temporary node unavailability. Configure the agent with a graceful shutdown timeout and mark the node as `drainable` in the cluster scheduler.
- **Resource contention**: Plex or Jellyfin transcoding competes directly with cluster agents for CPU. Use cgroup CPU limits or Synology's built-in resource monitor to enforce hard caps.
- **Disk spin-up latency**: HDD-based NAS devices experience I/O stalls when sleeping disks spin up. Use NVMe cache pools for agent state and temporary workspace, reserving HDD arrays for bulk storage.

---

### 6.3 Smart TVs as Idle Compute Donors

The modern smart television is a surprisingly capable computer. A typical 2024-model 4K TV contains a quad-core ARM SoC, 2--4 GB of RAM, gigabit networking (via USB adapter or built-in), and dedicated video decode hardware that handles 4K HDR streaming with minimal CPU involvement. When the TV is on but the viewer is passively watching, or when the TV is in standby with the SoC still powered, significant CPU cycles sit unused. The challenge is not hardware capability but software access.

#### 6.3.1 LG webOS: The Most Open Platform

LG webOS is the most favorable smart TV platform for HelixCluster integration. Its background service model allows JavaScript services to run persistently on Node.js, with full access to Node.js core modules and support for third-party JavaScript packages (without C/C++ add-ons) ^16^. The webOS OSE (Open Source Edition) takes this further by providing a fully open-source build target, and the `ares-generate -t js_service` CLI tool creates production-ready service templates ^17^.

A HelixCluster agent for webOS can be implemented as a JS Service: a lightweight Node.js process that starts at boot, maintains a WebSocket connection to the cluster mesh, and accepts task dispatches over the webOS bus. Realistic workloads include data relay (forwarding IoT sensor readings to regional hubs), heartbeat services (acting as a local coordination beacon for nearby edge nodes), and simple aggregation (computing rolling averages of temperature, power, or network latency data). The agent runs at low priority, yielding CPU instantly when the foreground application (Netflix, YouTube, the OS UI) demands resources.

The limitation is clear: webOS services run JavaScript only. No native code, no Docker, no GPU compute access. The platform is suitable for lightweight coordination and relay tasks, not for numerical workloads or ML inference.

#### 6.3.2 Samsung Tizen: Computational Capability During Decode

Samsung Tizen TVs offer background service applications written in Node.js, with `onStart()`, `onRequest()`, and `onExit()` callbacks managed by the Tizen SDK ^18^. Developer mode is enabled by entering the sequence "12345" on the Apps panel, after which apps can be sideloaded via the Tizen Studio IDE.

Tizen's background services are more restrictive than webOS. The security sandbox limits file system access, network destinations may be whitelisted by the platform, and the developer mode certificate expires periodically, requiring reactivation ^19^. A HelixCluster agent on Tizen would use the `MessagePort` API to communicate between background service and foreground companion app, with tasks limited to HTTP-based data relay and lightweight processing.

The key insight for both webOS and Tizen is that 4K video decode happens on dedicated VPU (Video Processing Unit) silicon. The ARM CPU cores handle UI rendering, network I/O, and application logic---but during passive viewing, even these duties are minimal. A background service consuming 5--10% of CPU during a movie causes no perceptible performance degradation.

#### 6.3.3 NVIDIA Shield TV Pro: A Switch-Class Compute Node

The NVIDIA Shield TV Pro is categorically different from other smart TV devices. Its Tegra X1+ SoC (T210 B01) is the same architecture that powers the Nintendo Switch, featuring a quad-core 2.0 GHz ARM CPU, 256-core Maxwell GPU, and 3 GB of DDR3 RAM ^20^. This is not a TV platform with a weak SoC; it is a low-power SBC disguised as a streaming device.

Full Android TV means full Android capabilities: native code via the NDK, background services with `JobScheduler` or foreground services, network sockets, and---uniquely---CUDA-accessible GPU compute. The Maxwell GPU, while aging, delivers approximately 100--150 GFLOPS FP32, suitable for lightweight ML inference or parallel data processing. At 5--10 W typical power draw, the Shield TV Pro outperforms many dedicated SBCs on a per-watt basis.

For HelixCluster, the Shield TV Pro is a Tier 1.5 node: not quite a full compute worker (3 GB RAM limits model sizes), but far more capable than any other TV-class device. It runs a containerized or native agent alongside its streaming duties, contributing GPU-accelerated tasks that would overwhelm CPU-only edge nodes.

**Table 6.3: Smart TV Compute Capability Comparison**

| Device | CPU | RAM | Background Services | Native Code | GPU Access | Openness | Cluster Tier |
|--------|-----|-----|---------------------|-------------|------------|----------|-------------|
| Samsung Tizen TV | Quad ARM A-series | 1.5--3 GB | Node.js (Tizen SDK) | No | No | Medium | T2 (relay) |
| LG webOS TV | Dual/Quad ARM | 1.5--4 GB | Node.js (JS Services) | No | No | High | T2 (relay) |
| Chromecast Google TV | 4x A55 @ 1.9 GHz | 2 GB | Android Services | Yes (NDK) | Mali-G31 | Medium | T2 (light) |
| **NVIDIA Shield TV Pro** | **Tegra X1+ 4-core** | **3 GB** | **Full Android** | **Yes (NDK)** | **256-core Maxwell** | **High** | **T1.5 (GPU)** |

Operational cautions for TV compute nodes are significant. Background services may be terminated during OS updates or memory pressure events. Network connectivity is unreliable: many TVs disconnect from Wi-Fi in deep standby. And there is no persistence guarantee---local databases or cached state may be wiped by platform maintenance. TV nodes should be treated as **ephemeral, best-effort participants**: valuable for augmenting cluster capacity during evening viewing hours, but never depended upon for critical-path workloads.

---

### 6.4 Wearables & Smart Speakers

Not every connected device can join the cluster. Some of the most computationally interesting hardware---Apple Watches with Neural Engines, HomePods with room-filling acoustic processing, Echo devices with always-on voice recognition---are inaccessible by design. This section explains the exclusions and identifies the specific platform restrictions that prevent integration.

#### 6.4.1 Exclusion Rationale: Closed Ecosystems, No Background Freedom

**Apple Watch (S9/S10 SiP)**. The Apple S9 SiP contains a capable dual-core CPU, 1 GB of RAM, 64 GB of storage, and a 4-core Neural Engine delivering approximately 5 TOPS---in theory, sufficient for on-device ML inference and lightweight cluster tasks. In practice, watchOS imposes draconian restrictions on background execution. Background tasks are limited to specific categories (background refresh, URL session, processing) and subject to strict time and battery constraints. The 300 mAh battery and 1--2 W thermal envelope make sustained compute donation physically impossible. The platform is entirely closed: no sideloading, no daemon installation, no WebSocket server that could maintain a cluster connection. The Apple Watch is excluded from all cluster tiers.

**Amazon Echo / Echo Dot**. The Echo Dot's MediaTek MT8516 (quad-core Cortex-A35 @ 1.3 GHz, 512 MB RAM, 4--8 GB eMMC) is modest but functional hardware. The fatal restriction is software: Alexa Skills execute exclusively as AWS Lambda functions in the cloud, not as on-device processes ^21^. There is no developer mode that enables persistent background code, no local package manager, and no path to running a HelixCluster agent. Amazon's AZ2 Neural Edge processor (in newer devices) is similarly locked. Echo devices are excluded.

**Apple HomePod / HomePod mini**. The HomePod mini uses the Apple S7 SiP with 1.5 GB of RAM and 32 GB of storage ^22^. The original HomePod used the Apple A8 with 1 GB of RAM. Both run audioOS, a variant of tvOS, with zero developer access, zero sideloading, and zero background service capability. The hardware is not the limitation; the software barrier is absolute.

**Google Nest Hub / Nest Mini**. The second-generation Nest Hub runs Fuchsia OS on an Amlogic S905D3---the same capable chip as the Chromecast with Google TV---with 2 GB of RAM. Despite Fuchsia's open-source pedigree, custom code execution is not available to end users. The device is a closed platform, and its compute potential remains untapped.

**Wear OS (Qualcomm Snapdragon W5+)**. The W5+ Gen 1 is the most advanced wearable SoC available: quad-core Cortex-A53 @ 1.7 GHz, Adreno 702 GPU, 1.5--2 GB LPDDR4, and a 4 nm process node ^23^. Wear OS permits background services with fewer restrictions than watchOS, but battery optimization policies aggressively suspend them. The 1--2 W sustained thermal envelope and intermittent connectivity (Bluetooth tethering to a phone) make the platform unsuitable for any meaningful compute donation. Wear OS is excluded.

#### 6.4.2 Future Possibilities If Platforms Open

The exclusions are not permanent. Several developments could unlock wearable and smart speaker compute for future HelixCluster versions:

- **Regulatory pressure**: The EU's Digital Markets Act and similar legislation may force Apple and Amazon to permit sideloading and alternative app stores on their devices, potentially opening watchOS and audioOS to developer tools.
- **Open-source firmware**: Community projects to reverse-engineer HomePod or Echo firmware could yield a Linux boot path, as happened with early smart TV models and game consoles.
- **New hardware categories**: AR glasses and AI pins are emerging as new wearable form factors, some of which (Rabbit R1, Humane Pin) run Android or Linux variants with more open software stacks.
- **Cloud-offload hybrids**: Even without on-device execution, wearables could function as cluster *sensors* rather than compute donors, streaming health, environmental, and location data to nearby edge nodes for processing. This "sensor edge" model does not require background compute freedom---only outbound network access.

For the present, however, these devices represent billions of dollars of silicon that cannot contribute to distributed compute. The cluster designer should ignore them entirely and focus engineering effort on the open platforms documented in Sections 6.1--6.3: routers with OpenWrt, NAS devices with Docker, and---for the adventurous---smart TVs running webOS or Android TV.

The strategic lesson of this chapter is that **the best cluster node is the one you can actually program**. A $50 Chromecast with developer mode enabled contributes more than a $400 HomePod with no access. A $159 OpenWrt router with Docker outperforms a $550 Apple Watch with a 5 TOPS Neural Engine. Openness is not merely a philosophical preference in distributed systems engineering; it is a hard technical prerequisite that determines whether a device exists in the cluster topology at all.


---

## 7. Exotic & Future Technologies

The compute landscape extends far beyond x86 servers and NVIDIA GPUs. A new wave of specialized silicon---AI-specific architectures, wafer-scale engines, neuromorphic chips, and even quantum processors---promises to reshape how clusters handle inference, training, and novel workloads. This chapter evaluates these exotic technologies through a pragmatic lens: not whether they are impressive engineering achievements, but whether they merit a place in a HelixCluster deployment before 2027.

The verdict is sharply bifurcated. A handful of production-ready AI accelerators---Groq's LPU, Cerebras's CS-3, and SambaNova's SN40L---offer genuine, measurable advantages for large language model inference and deserve serious consideration as dedicated cluster tiers. The rest, from quantum computers to Bitcoin ASICs, are either research curiosities or physically incapable of general-purpose computation. This chapter separates signal from noise.

### 7.1 Groq LPU for LLM Inference

#### 7.1.1 Architecture and Performance

Groq's Language Processing Unit (LPU) represents the most radical departure from GPU architecture in production AI silicon. Where GPUs are massively parallel, throughput-optimized processors with hierarchical memory (HBM, L2, registers), the LPU is a **single-core, tiled dataflow processor** with deterministic execution and no off-chip memory. The entire model weights and activations stream through an on-chip SRAM fabric at 150 TB/s---more than 40x the memory bandwidth of an NVIDIA H100.

This architectural gambit pays off spectacularly for single-stream LLM inference. A single LPU chip achieves **300-500 tokens per second on Llama 2 70B**, compared to 30-40 tokens/sec for an H100. Latency is deterministic and sub-100ms for time-to-first-token, versus 200-1000ms of highly variable latency on GPU systems. Energy efficiency is similarly lopsided: 1-3 joules per token versus 10-30 joules on H100-class hardware.

| Metric | Groq LPU | NVIDIA H100 |
|--------|----------|-------------|
| Memory type | On-chip SRAM (~230 MB) | HBM3 (80 GB) |
| Memory bandwidth | 150 TB/s per chip | 3.35 TB/s |
| Llama 2 70B throughput | 300-500 tokens/sec | 30-40 tokens/sec |
| Energy per token | 1-3 joules | 10-30 joules |
| Time-to-first-token | <100 ms (deterministic) | 200-1000 ms (variable) |
| Training support | No | Yes |
| Peak FP16 compute | N/A (dataflow architecture) | 989 TFLOPS |

**Table 1: Groq LPU vs. NVIDIA H100 for LLM inference.** The LPU's advantage is workload-specific: it dominates single-stream, low-latency LLM serving but cannot train models or handle multimodal workloads.

The trade-off is severe specialization. The LPU's ~230 MB of on-chip SRAM limits it to models that fit entirely in the chip fabric; larger models must be sharded across multiple LPUs in a rack-scale system. More critically, LPUs **cannot train models**. They are inference-only accelerators. They also struggle with non-transformer architectures---diffusion models, state-space models like Mamba, and mixture-of-experts (MoE) routing with large expert pools exceed the SRAM budget or break the static dataflow compilation model.

**Critical business update (December 2025):** NVIDIA signed a non-exclusive licensing agreement valued at approximately **$20 billion** with Groq, acquiring the LPU technology IP and hiring CEO Jonathan Ross along with roughly 90% of senior engineering staff. Groq continues as a nominally independent company under new CEO Simon Edwards, but the long-term trajectory points toward LPU technology being absorbed into NVIDIA's product stack rather than remaining a standalone platform. This introduces material uncertainty for any cluster architect betting on Groq-branded hardware availability through 2027.

#### 7.1.2 Cloud API Integration and On-Prem Deployment

Groq offers two consumption models. **GroqCloud** provides API access to LPU-backed inference at $0.05 per million input tokens for Llama 3.1 8B and $0.59 per million for Llama 3.1 70B, with a Batch API offering 50% discounts for asynchronous workloads. For HelixCluster integration, GroqCloud functions as an external API endpoint---cluster workloads submit inference requests via REST and receive responses, with no local hardware to manage.

**GroqRack** is the on-premises deployment option, delivering a full LPU rack for data residency and low-latency local serving. This is the HelixCluster-relevant deployment mode: a GroqRack installed in a cluster data center becomes a dedicated inference tier, with jobs routed to it based on workload type (LLM inference) and latency requirements.

The following YAML configuration defines a Groq LPU inference tier within the HelixCluster node taxonomy:

```yaml
# HelixCluster Node Tier: Groq LPU Inference Accelerator
# Version: 2025.1
# Status: Production-ready, vendor volatility high post-NVIDIA acquisition

tier_id: T2-GROQ-LPU
tier_name: "LLM Inference Accelerator (Groq LPU)"
tier_class: compute

hardware:
  accelerator: groq_lpu
  memory_per_chip_mb: 230
  memory_bandwidth_tbps: 150
  form_factor: [groqrack_1u, groqrack_4u, groqrack_9u]
  power_per_rack_kw: 3.5

performance_profile:
  optimal_workloads:
    - "Single-stream LLM inference (transformer decoder)"
    - "Low-latency chatbot serving (<100ms TTFT)"
    - "Small-to-medium model inference (<=70B parameters, unsharded)"
  unsupported_workloads:
    - "Model training (any architecture)"
    - "Diffusion and image generation models"
    - "State-space models (Mamba, RWKV)"
    - "MoE with >8 active experts per token"
    - "Multimodal fusion (vision+language encoders)"
  throughput:
    llama_3_1_70b: "300-500 tok/sec (single stream)"
    llama_3_1_8b: "750-1500 tok/sec (single stream)"

integration:
  access_methods:
    cloud_api:
      endpoint: "https://api.groq.com/openai/v1/chat/completions"
      auth: "bearer_token"
      routing: "external_http_tier"
    on_prem:
      protocol: "gRPC over RDMA"
      local_endpoint: "groqrack.internal:8080"
      routing: "dedicated_inference_tier"
  scheduler_labels:
    - "inference=llm"
    - "latency=critical"
    - "accelerator=groq-lpu"
    - "training=false"
  job_router:
    match_criteria: "workload.type == 'llm_inference' AND model.params <= 70e9"
    fallback_tier: "T2-GPU-H100"

reliability:
  mean_time_between_failure_hours: 8760
  deterministic_latency: true
  redundancy: "chip-level sparing within rack"

probability_helixcluster_relevance_2027: 0.55
risk_factors:
  - "NVIDIA acquisition may absorb LPU into proprietary stack"
  - "Groq-branded hardware availability uncertain post-2026"
  - "Single-source vendor with no second-source equivalent"
  - "Architecture too specialized for general cluster workloads"
```

**Probability of HelixCluster relevance by 2027: 55%**---conditioned on continued availability of GroqRack or NVIDIA-branded LPU-derived systems for on-prem deployment. If NVIDIA discontinues standalone LPU products, this probability drops to near zero.

### 7.2 Cerebras, SambaNova & Other AI Silicon

#### 7.2.1 Cerebras CS-3 (WSE-3): Wafer-Scale for Large Model Inference

The Cerebras Wafer Scale Engine 3 (WSE-3) is the largest computer chip ever manufactured---a 215mm x 215mm silicon wafer containing **4 trillion transistors** and 900,000 AI-optimized cores. The CS-3 system, built around a single WSE-3, delivers 125 petaFLOPS of peak FP16 compute and **44 GB of on-chip SRAM** with 21 PB/s of memory bandwidth. That bandwidth figure is roughly 7,000x that of a single H100.

The CS-3 eliminates the "memory wall" that constrains GPU inference. Because the entire model can reside in on-chip SRAM, there is no HBM bottleneck, no off-chip memory traffic, and no KV-cache paging to slower tiers. Cerebras claims a single CS-3 achieves approximately **3.5x the FP8 throughput and 7x the FP16 throughput** of an H100 rack consuming equivalent space and power. The system occupies 15U of rack space, draws approximately 23 kW, and is priced at an estimated **$2-3 million** per system.

Cerebras operates dedicated data centers with over 300 CS-3 systems in Oklahoma City and additional sites across North America and Europe. Cloud pricing is competitive at $0.10 per million tokens for Llama 3.1 8B and $0.60 per million for Llama 3.1 70B.

For HelixCluster, the CS-3 is a viable on-prem acquisition for inference-heavy deployments with sufficient capital budget. The primary limitations are cost---$2-3 million per system places it firmly in enterprise and institutional territory---and specialization for AI inference workloads. Unlike Groq LPUs, CS-3 systems can handle larger models (up to 24 trillion parameters on a single wafer) and support both inference and select training configurations.

**Probability of HelixCluster relevance by 2027: 40%.**

#### 7.2.2 SambaNova SN40L for Composition-of-Experts Workloads

SambaNova's DataScale platform, built around the SN40L Reconfigurable Dataflow Unit (RDU), offers a three-tier memory architecture designed specifically for Composition-of-Experts (CoE) and large language model inference:

- **On-chip SRAM:** 520 MB at 25.6 TB/s
- **HBM:** 64 GB at 1,600 GB/s
- **DDR DRAM:** Up to 1.5 TB at >1 TB/s

Each SN40L-16 system (16 RDUs) delivers **10.2 petaFLOPS at BF16 precision**, with 1 TB of aggregate HBM and 12 TB of DDR. The dataflow architecture fuses entire model layers into single kernel calls, achieving a **3.7x speedup over DGX H100 for CoE inference** and a 19x reduction in machine footprint. Power consumption ranges from 7-18 kW depending on workload.

SambaNova provides the SambaFlow SDK, which compiles PyTorch models to dataflow graphs, and supports Red Hat Enterprise Linux for on-prem deployment. This makes the SN40L a strong candidate for HelixCluster inference-tier deployment, particularly for organizations running CoE models or seeking a non-NVIDIA on-prem AI accelerator with full software stack support.

**Probability of HelixCluster relevance by 2027: 45%.**

**Graphcore IPU --- Discontinued.** The Graphcore Intelligence Processing Unit (IPU) was acquired by SoftBank in July 2024 for approximately $500-600 million. The Bow Pod product line was effectively discontinued post-acquisition, and Graphcore's technology is being repositioned for future SoftBank/Arm AI initiatives rather than direct market competition. Hardware is no longer commercially available. **HelixCluster relevance: <1%. Do not pursue.**

**Etched AI Sohu --- Unproven Transformer Bet.** Etched AI's Sohu chip is a transformer-only ASIC that hard-codes attention mechanisms directly into silicon. An 8-chip server claims 500,000 tokens per second on Llama 70B---roughly 20x an 8xH100 system. However, Sohu **cannot run MoE models, diffusion models, state-space models, or any non-transformer architecture**. As of early 2026, the chip is not publicly available. The transformer-only bet may lose to MoE architectures as the field evolves. **Probability of HelixCluster relevance by 2027: 10%.**

### 7.3 Quantum, Neuromorphic & Photonic

#### 7.3.1 Quantum Computing Timeline: Not Ready Before 2029

Quantum computing generates enormous research excitement and near-zero production utility for cluster workloads. IBM's Heron processor (133 qubits), Google's updated Sycamore (70 qubits), and Quantinuum's H2 (56 trapped-ion qubits) represent the current state of the art. These systems require cryogenic infrastructure operating at 15 millikelvin, consume megawatts of supporting power, and can execute circuits with gate depths of only a few thousand operations before decoherence overwhelms results.

Vendor roadmaps converge on a critical inflection point: **fault-tolerant quantum computing with logical qubits will not arrive before 2029**. IBM targets its Starling system (200 logical qubits, 100 million gates) for 2029; Google projects "commercially useful" quantum computing by the same year; Quantinuum's Apollo system aims for universal fault-tolerant computing by 2030.

Quantum computers cannot directly join a standard compute cluster. They function as co-processors accessed via cloud APIs---IBM's Qiskit Runtime, for example---with classical orchestration submitting circuits and receiving probabilistic results. The latency (milliseconds to seconds per circuit), error rates, and limited circuit depth make this viable only for specific optimization, quantum chemistry, and machine learning research workloads. A HelixCluster node could theoretically submit Qiskit jobs to IBM Quantum, but this is a niche research integration, not a production compute tier.

**Probability of HelixCluster relevance by 2027: 5%**---only as a specialized "quantum accelerator node" abstraction for optimization research. Practical integration waits for 2029+.

#### 7.3.2 Intel Loihi 2, IBM NorthPole: Research-Only Neuromorphic

Neuromorphic computing---chips that mimic biological neural architectures with spiking, event-driven computation---remains firmly in the research domain.

**Intel Loihi 2** features 128 fully asynchronous neuron cores supporting up to 1 million neurons and 120 million synapses on a single chip. Power consumption is remarkably low---30-80 mW per core in static operation---because computation is event-driven: power is consumed only when neurons "fire." The Lava SDK provides Python APIs, and the Kapoho Point USB development board ($2,500) enables edge experimentation. However, Loihi 2 has no general-purpose compute capabilities. It is a research tool for adaptive robotics, sensory processing, and brain-computer interface prototyping.

**IBM NorthPole** takes a different approach: 256 digital, programmable cores with 224 MB of on-chip SRAM and no off-chip memory whatsoever. Achieving 12x the energy efficiency of comparable digital accelerators, NorthPole is purpose-built for neural inference at the edge. The constraint is severe---the entire model must fit in 224 MB---and the chip supports only low-precision integer operations (8-bit, 4-bit, 2-bit).

Neither chip can run a standard Linux distribution, containerized workloads, or conventional AI frameworks like PyTorch. They require entirely separate software stacks and programming paradigms. For HelixCluster, they are experimental curiosities at best.

**Probability of HelixCluster relevance by 2027: 3% (Loihi 2), 8% (NorthPole)**---only if edge inference tiers expand to include dedicated neuromorphic nodes, which is unlikely before 2030.

#### 7.3.3 Photonic Computing: 3-5 Years for Interconnect, 5-10 for Compute

Photonic computing uses light rather than electrons to transport and process information, promising radical improvements in bandwidth and energy efficiency. The field splits into two distinct domains: **photonic interconnects** (near-term, shipping soon) and **photonic processors** (long-term, research stage).

Lightmatter's Passage M1000 provides 114.6 Tbps of bidirectional bandwidth across 1,024 optical SerDes lanes, packaged as a 3D photonic interposer. The newer Passage L20, announced in March 2026, delivers 6.4 Tbps optical bandwidth at only 30W TDP and begins sampling in late 2026. These are interconnect products---they replace electrical I/O between compute chips, not the compute chips themselves.

Ayar Labs unveiled the first UCIe-compatible optical chiplet in March 2025, achieving 8 Tbps chip-to-chip optical communication using standard packaging. This technology enables the disaggregation of memory and compute at rack scale, potentially transforming cluster topologies by 2028-2030.

True photonic processors---chips that perform computation using light---remain experimental. Lightmatter demonstrated a photonic processor capable of executing ResNet and BERT at 65.5 trillion 16-bit operations per second using ~78W electrical plus 1.6W optical power, but this is a research demonstration, not a commercial product.

For HelixCluster, photonic technology is relevant only as a future interconnect layer. It does not provide drop-in compute nodes. Practical deployment for data center scale is estimated at **2028-2030 for interconnect, 2030-2035 for compute**.

**Probability of HelixCluster relevance by 2027: 5%** (interconnect evaluation only).

**Bitcoin ASICs --- Absolutely Not.** The Bitmain Antminer S19 and S21 series implement SHA-256 double-hashing in hardware at the transistor level. These chips cannot be reprogrammed, reflashed, or repurposed for any other workload. Mining companies such as Hut 8, Iris Energy, and Core Scientific that have transitioned facilities to AI hosting explicitly acknowledge that SHA-256 ASICs must be physically removed and replaced with GPUs. The only theoretical repurposing---using voltage-stressed ASICs as physical reservoir computing substrates---remains experimental and unvalidated. Mining *facilities* (power, cooling, space) can be converted. Mining ASICs are e-waste for compute purposes. **HelixCluster relevance: 0%.**

### 7.4 Technology Readiness Assessment

The following table consolidates the readiness, timeline, and integration approach for all exotic technologies evaluated in this chapter. Ratings reflect the probability that each technology will be a productive HelixCluster node or tier by 2027.

| Device / Technology | Status | HelixCluster Integration Timeline | Probability by 2027 | Integration Approach | Risk Factors |
|---------------------|--------|-----------------------------------|---------------------|----------------------|--------------|
| **Groq LPU** (GroqRack / GroqCloud) | Production | Immediate (cloud) / 2026 (on-prem) | 55% | Dedicated LLM inference tier; API or rack mount | NVIDIA acquisition may absorb IP; single-source vendor; inference-only |
| **SambaNova SN40L** | Production | Immediate | 45% | On-prem CoE/inference tier via SambaFlow SDK | Smaller ecosystem than NVIDIA; dataflow compilation required |
| **Cerebras CS-3** (WSE-3) | Production | Immediate (capital permitting) | 40% | On-prem large-model inference; cloud fallback | $2-3M system cost; 23 kW per system; specialized cooling |
| **AWS Trainium3 / Inferentia2** | Cloud only | Immediate (cloud-hybrid) | 25% | AWS-connected cluster nodes for training | Vendor-locked to AWS; no on-prem purchase option |
| **Google TPU v6e/v7** | Cloud only | Immediate (cloud-hybrid) | 20% | GCP-connected nodes via JAX/TensorFlow | Vendor-locked to GCP; no on-prem availability |
| **IBM z17 mainframe** | Production | Immediate (if owned) | 15% | Ultra-secure workload tier via Linux LPAR | $10-100x per-FLOPS cost vs. x86/GPU; big-endian porting |
| **Etched AI Sohu** | Pre-production | 2027 (if available) | 10% | Transformer-only inference node | Unproven silicon; cannot run MoE/diffusion/SSM; may not ship |
| **IBM NorthPole** | Limited samples | 2028+ | 8% | Edge inference only (224 MB model max) | No Linux; research-only toolchain; integer-only precision |
| **Photonic interconnect** | Sampling 2026 | 2028-2030 | 5% | Rack-scale optical I/O between compute nodes | Not a compute node; replaces electrical interconnect only |
| **Quantum (IBM/Google/IonQ)** | Cloud API access | 2029+ | 5% | "Quantum accelerator node" via Qiskit/circuit APIs | Cryogenic infrastructure; cloud-only; error rates; niche workloads |
| **Edge NPUs** (Qualcomm/Apple) | Production | Immediate (edge clusters) | 5% | Edge micro-clusters for lightweight inference | Locked behind vendor SDKs; no low-level API; not datacenter scale |
| **Intel Loihi 2** | Research dev kit | 2030+ | 3% | Neuromorphic research node (not production) | No general-purpose compute; separate software stack entirely |
| **Graphcore IPU** | Discontinued | N/A | <1% | **Do not integrate** | Acquired by SoftBank July 2024; hardware no longer available |
| **Bitcoin ASICs** (S19/S21) | E-waste for compute | Never | 0% | **Do not integrate** | SHA-256 hardwired at transistor level; physically incapable |
| **Neuralink BCI** | Clinical trials | N/A | <1% | **Do not integrate** | Medical device; no compute relevance; decades from integration |

**Table 2: Technology Readiness Assessment for HelixCluster exotic and future compute nodes.** Probability ratings integrate technical maturity, commercial availability, software ecosystem readiness, and architectural fit. Ratings below 10% indicate technologies that should receive zero engineering investment.

#### 7.4.1 Strategic Assessment

The exotic technology landscape offers three actionable opportunities and a long list of distractions.

**Actionable now:** Groq LPUs (cloud API immediately, on-prem pending vendor stability), SambaNova SN40L (on-prem with strong CoE support), and Cerebras CS-3 (on-prem for capital-rich deployments with large-model inference needs). These three systems represent genuine alternatives to GPU inference with measurable performance advantages.

**Monitor and prepare:** Photonic interconnects from Lightmatter and Ayar Labs will reshape cluster networking by 2028-2030. IBM's z17 mainframe fills a niche for ultra-high-security, regulated workloads where price-performance is secondary to RAS guarantees. AWS Trainium3 and Google TPU v7 are viable cloud-hybrid training tiers for organizations already committed to those ecosystems.

**Ignore:** Bitcoin ASICs are physically incapable of general computation. Graphcore is defunct. Neuromorphic chips are research tools with no production software stack. Quantum computing is a 2029+ technology for all practical purposes. Neuralink is medical technology with no compute relevance.

The honest assessment for cluster architects: exotic technologies are exciting but fragile. The Groq LPU's 10x inference advantage is real, but the $20 billion NVIDIA acquisition introduces existential business risk. Cerebras's wafer-scale engineering is remarkable, but $2-3 million per system excludes most deployments. SambaNova offers the most balanced profile---production hardware, on-prem availability, competitive performance---but lacks the ecosystem depth of NVIDIA. Bet on these technologies cautiously, if at all, and only after primary tiers of conventional GPU and CPU nodes are firmly established.


---

## 8. Universal Integration Layer & Complete Taxonomy

This chapter unifies all device categories into a single architecture: the automatic discovery engine, the complete 64-device taxonomy, the five-tier security model, and five validated cluster build recipes.

---

### 8.1 Device Discovery Protocol

Every node joining HelixCluster runs a discovery engine that probes hardware, assigns a tier, and generates a capability manifest for the control plane.

#### 8.1.1 Automatic Device Detection

| Probe | Source | Extracted Data |
|-------|--------|----------------|
| CPU | `/proc/cpuinfo` | ISA, cores, frequency, flags |
| GPU | Vulkan/CUDA/ROCm sysfs | Vendor, VRAM, compute APIs |
| RAM | `/proc/meminfo` | Physical bytes |
| Storage | `statfs` | Capacity, filesystem type |
| Network | `netlink` | Speeds, IPs, mesh eligibility |

These probes require no root privileges and complete in under five seconds.

#### 8.1.2 Go Device Detection Engine

```go
package discovery

import (
    "os"
    "runtime"
    "strconv"
    "strings"
    "syscall"
    "time"
)

type DeviceProfile struct {
    DeviceID         string    `json:"device_id"`
    Hostname         string    `json:"hostname"`
    Architecture     string    `json:"architecture"`
    CPUModel         string    `json:"cpu_model"`
    CPUCores         int       `json:"cpu_cores"`
    CPUFeatures      []string  `json:"cpu_features"`
    RAMBytes         uint64    `json:"ram_bytes"`
    StorageBytes     uint64    `json:"storage_bytes"`
    ComputeClasses   []string  `json:"compute_classes"`
    GPUs             []GPUInfo `json:"gpus,omitempty"`
    NPUs             []NPUInfo `json:"npus,omitempty"`
    OS               string    `json:"os"`
    ContainerRuntime string    `json:"container_runtime"`
    AssignedTier     string    `json:"assigned_tier"`
    TrustLevel       string    `json:"trust_level"`
}

type GPUInfo struct {
    Vendor      string   `json:"vendor"`
    Model       string   `json:"model"`
    ComputeAPIs []string `json:"compute_apis"`
}

type NPUInfo struct {
    Vendor   string  `json:"vendor"`
    Model    string  `json:"model"`
    TOPsINT8 float64 `json:"tops_int8"`
}

type DiscoveryEngine struct{}

func (de *DiscoveryEngine) Discover() (*DeviceProfile, error) {
    p := &DeviceProfile{DeviceID: generateDeviceID(), ComputeClasses: []string{"cpu"}}
    p.Hostname, _ = os.Hostname()
    p.Architecture = runtime.GOARCH
    p.OS = runtime.GOOS
    de.detectCPU(p)
    de.detectMemory(p)
    de.detectGPU(p)
    de.detectNPU(p)
    de.detectStorage(p)
    de.detectContainerRuntime(p)
    classifyTier(p)
    return p, nil
}

func (de *DiscoveryEngine) detectCPU(p *DeviceProfile) {
    data, _ := os.ReadFile("/proc/cpuinfo")
    lines := strings.Split(string(data), "\n")
    cores := 0
    for _, line := range lines {
        if strings.HasPrefix(line, "processor\t:") { cores++ }
        if strings.HasPrefix(line, "model name\t:") && p.CPUModel == "" {
            p.CPUModel = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
        }
        if strings.HasPrefix(line, "flags\t:") {
            p.CPUFeatures = strings.Fields(strings.SplitN(line, ":", 2)[1])
        }
        if strings.HasPrefix(line, "isa\t:") {
            p.CPUFeatures = strings.Split(strings.TrimSpace(
                strings.SplitN(line, ":", 2)[1]), "_")
        }
    }
    if cores == 0 { cores = runtime.NumCPU() }
    p.CPUCores = cores
}

func (de *DiscoveryEngine) detectMemory(p *DeviceProfile) {
    data, _ := os.ReadFile("/proc/meminfo")
    for _, line := range strings.Split(string(data), "\n") {
        if strings.HasPrefix(line, "MemTotal:") {
            fields := strings.Fields(line)
            if len(fields) >= 2 {
                kb, _ := strconv.ParseUint(fields[1], 10, 64)
                p.RAMBytes = kb * 1024
            }
        }
    }
}

func (de *DiscoveryEngine) detectGPU(p *DeviceProfile) {
    if _, err := os.Stat("/usr/bin/vulkaninfo"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "detected", ComputeAPIs: []string{"vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if out, err := os.ReadFile("/proc/driver/nvidia/gpus/0/information"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "nvidia", Model: string(out),
            ComputeAPIs: []string{"cuda", "vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if _, err := os.Stat("/opt/rocm/bin/rocm-smi"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "amd", ComputeAPIs: []string{"rocm", "vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
    if _, err := os.Stat("/sys/class/misc/mali0"); err == nil {
        p.GPUs = append(p.GPUs, GPUInfo{Vendor: "arm", Model: "Mali-G610",
            ComputeAPIs: []string{"vulkan"}})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "gpu")
    }
}

func (de *DiscoveryEngine) detectNPU(p *DeviceProfile) {
    if _, err := os.Stat("/dev/rknpu"); err == nil {
        p.NPUs = append(p.NPUs, NPUInfo{Vendor: "rockchip", Model: "RK3588_NPU", TOPsINT8: 6.0})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "npu")
    }
    if _, err := os.Stat("/sys/class/misc/nvdec"); err == nil {
        tops := 67.0
        if p.RAMBytes >= 32<<30 { tops = 275.0 } else if p.RAMBytes >= 16<<30 { tops = 157.0 }
        p.NPUs = append(p.NPUs, NPUInfo{Vendor: "nvidia", Model: "DLA+Ampere", TOPsINT8: tops})
        p.ComputeClasses = appendUniq(p.ComputeClasses, "npu")
    }
}

func (de *DiscoveryEngine) detectStorage(p *DeviceProfile) {
    var st syscall.Statfs_t
    if syscall.Statfs("/var/lib/helixcluster", &st) == nil {
        p.StorageBytes = st.Blocks * uint64(st.Bsize)
    } else if syscall.Statfs("/", &st) == nil {
        p.StorageBytes = st.Blocks * uint64(st.Bsize)
    }
}

func (de *DiscoveryEngine) detectContainerRuntime(p *DeviceProfile) {
    if _, err := os.Stat("/usr/bin/docker"); err == nil {
        p.ContainerRuntime = "docker"
    } else if _, err := os.Stat("/usr/bin/podman"); err == nil {
        p.ContainerRuntime = "podman"
    } else {
        p.ContainerRuntime = "none"
    }
}

func classifyTier(p *DeviceProfile) {
    if len(p.NPUs) > 0 && p.NPUs[0].TOPsINT8 >= 100 {
        p.AssignedTier = "AI_CONTROLLER"; p.TrustLevel = "SEMI_TRUSTED"; return
    }
    if len(p.NPUs) > 0 && p.NPUs[0].TOPsINT8 >= 20 {
        p.AssignedTier = "AI_WORKER"; p.TrustLevel = "SEMI_TRUSTED"; return
    }
    if p.CPUCores >= 16 && p.RAMBytes >= 64<<30 {
        p.AssignedTier = "CORE_TRUSTED"; p.TrustLevel = "TRUSTED"; return
    }
    if p.CPUCores <= 4 && p.RAMBytes <= 2<<30 {
        p.AssignedTier = "NETWORK_GATEWAY"; p.TrustLevel = "EDGE"; return
    }
    if len(p.GPUs) > 0 && p.RAMBytes >= 16<<30 {
        p.AssignedTier = "HANDHELD"; p.TrustLevel = "UNTRUSTED"; return
    }
    if p.Architecture == "riscv64" {
        p.AssignedTier = "RISC_V_EXPERIMENTAL"; p.TrustLevel = "TRUSTED"; return
    }
    p.AssignedTier = "SEMI_TRUSTED"; p.TrustLevel = "SEMI_TRUSTED"
}

func appendUniq(s []string, item string) []string {
    for _, x := range s { if x == item { return s } }
    return append(s, item)
}
func generateDeviceID() string {
    return "hc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}
```

Build for all target architectures:

```bash
GOOS=linux GOARCH=amd64   go build -o helixcluster-agent-amd64
GOOS=linux GOARCH=arm64   go build -o helixcluster-agent-arm64
GOOS=linux GOARCH=riscv64 go build -o helixcluster-agent-riscv64
```

---

### 8.2 Complete Device Taxonomy (64 Devices)

Master reference. Tier assignment follows the discovery engine decision tree (Section 8.1.2).

| # | Device | Category | CPU | RAM | GPU / AI | Network | Price | Tier | Linux |
|---|--------|----------|-----|-----|----------|---------|-------|------|-------|
| 1 | Steam Deck LCD | Handheld | Zen 2 4c/8t | 16GB | RDNA 2 1.6 TFLOPS | Wi-Fi 5 | $279 (refurb) | T9-HANDHELD | Native |
| 2 | Steam Deck OLED | Handheld | Zen 2 4c/8t | 16GB | RDNA 2 1.6 TFLOPS | Wi-Fi 6E | $789 | T9-HANDHELD | Native |
| 3 | ROG Ally X | Handheld | Zen 4 8c/16t | 24GB | RDNA 3 8.6 TFLOPS | Wi-Fi 6E | $999 | T9-HANDHELD | Native |
| 4 | Lenovo Legion Go | Handheld | Zen 4 8c/16t | 16GB | RDNA 3 8.6 TFLOPS | Wi-Fi 6E | $699 | T9-HANDHELD | Native |
| 5 | GPD Win 4 2025 | Handheld | Zen 5 12c/24t | 32GB | RDNA 3.5 11.9 TFLOPS | Wi-Fi 7 | $1,200 | T9-HANDHELD | Native |
| 6 | Nintendo Switch | Handheld | ARM A57 4x | 4GB | Maxwell 393 GFLOPS | Wi-Fi 5 | EOL | T15-LEGACY | L4T only |
| 7 | Nintendo Switch 2 | Handheld | ARM A78C 8x | 12GB | Ampere ~3.1 TFLOPS | Wi-Fi 6 | $449 | T10-EXP | Not yet |
| 8 | Xbox Series X | Console | Zen 2 8c | 16GB | RDNA 2 12 TFLOPS | Wi-Fi 5 | $499 | T15-LEGACY | **None** |
| 9 | Jetson Orin Nano Super | AI Edge | 6x A78AE | 8GB | Ampere 67 TOPS | GbE | $249 | T4-AI_WORKER | Native |
| 10 | Jetson Orin NX 16GB | AI Edge | 8x A78AE | 16GB | Ampere 157 TOPS | GbE | $600 | T4-AI_WORKER | Native |
| 11 | Jetson AGX Orin 64GB | AI Ctrl | 12x A78AE | 64GB | Ampere 275 TOPS | GbE | $1,599 | T5-AI_CTL | Native |
| 12 | Jetson Thor T5000 | AI Ctrl | 14x Neoverse-V3 | 128GB | Blackwell 2070 TFLOPS | GbE | $2,847 | T5-AI_CTL | Native |
| 13 | Radxa ROCK 5B | ARM SBC | 4xA76+4xA55 | 4-32GB | Mali-G610, 6 TOPS | 2.5GbE | $157 | T2-SEMI | Mainline 6.12+ |
| 14 | NanoPi R6S | Gateway | 4xA76+4xA55 | 8GB | Mali-G610, 6 TOPS | 2x 2.5GbE | $139 | T6-NET_GW | Armbian |
| 15 | NanoPi R6C | ARM SBC | 4xA76+4xA55 | 4-8GB | Mali-G610, 6 TOPS | 2.5GbE+GbE | $85 | T2-SEMI | Armbian |
| 16 | Banana Pi BPI-M7 | ARM SBC | 4xA76+4xA55 | 8-32GB | Mali-G610, 6 TOPS | 2x 2.5GbE | $165 | T2-SEMI | Armbian |
| 17 | Mixtile Blade 3 | ARM SBC | 4xA76+4xA55 | 4-32GB | Mali-G610, 6 TOPS | Dual 2.5GbE | $195 | T2-SEMI | Armbian |
| 18 | Turing RK1 (module) | ARM SBC | 4xA76+4xA55 | 8-32GB | Mali-G610, 6 TOPS | GbE | $110 | T2-SEMI | Armbian |
| 19 | Turing Pi 2.5 | Backplane | N/A | N/A | N/A | GbE switch | $279 | T2-SEMI | N/A |
| 20 | CM3588 NAS | Storage | 4xA76+4xA55 | 4-32GB | 6 TOPS | 2.5GbE | $160 | T7-STORAGE | Armbian |
| 21 | Firefly ITX-3588J | Mini-ITX | 4xA76+4xA55 | 4-32GB | 6 TOPS | 2x GbE | $449 | T7-STORAGE | Armbian |
| 22 | Odroid M1 | Storage | 4x A55 | 4-8GB | Mali-G52 | GbE | $70 | T7-STORAGE | Native |
| 23 | Odroid M1S | Budget | 4x A55 | 4-8GB | Mali-G52 | GbE | $59 | T8-BUDGET | Native |
| 24 | Odroid N2+ | Budget | 4xA73+2xA53 | 2-4GB | Mali-G52 | GbE | $69 | T8-BUDGET | Native |
| 25 | Khadas VIM4 | ARM SBC | 4xA73+4xA53 | 8GB | Mali-G52, 2 TOPS | GbE+WiFi6 | $220 | T2-SEMI | Armbian |
| 26 | Khadas Edge2 | Edge | 4xA76+4xA55 | 8-16GB | Mali-G610, 6 TOPS | WiFi 6 | $199 | T3-EDGE | Armbian |
| 27 | BeagleBone AI-64 | Industrial | 2xA72+6xR5F | 4GB | 8 TOPS TDA4VM | GbE | $185 | T2-SEMI | TI SDK |
| 28 | Milk-V Pioneer | RISC-V | 64x C920 | up to 128GB | None | 2.5GbE | $1,199 | T10-EXP | Native |
| 29 | SiFive HiFive Premier | RISC-V | 4x P550 | 16-32GB | None | GbE | $399 | T10-EXP | Native |
| 30 | Milk-V Jupiter | RISC-V | 8x X60 | 4-16GB | 2 TOPS NPU | GbE | $150 | T10-EXP | Native |
| 31 | VisionFive 2 | RISC-V | 4x U74 | 1-8GB | IMG BXE-4-32 | GbE | $70 | T10-EXP | Native |
| 32 | Kendryte K230 | RISC-V AI | 2x C908 | 0.5-2GB | 6 TOPS KPU | 100MbE | $49 | T14-EXOTIC | SDK |
| 33 | Loongson 3A6000 | LoongArch | 4 (SMT) | 8-16GB | Integrated | GbE | $300 | T10-EXP | Loongnix |
| 34 | POWER9 Blackbird | POWER | 8 cores | up to 256GB | None | GbE | $1,600 | T1-CORE | Native |
| 35 | PYNQ-Z2 | FPGA+ARM | 2x A9 | 512MB | FPGA fabric | GbE | $129 | T12-FPGA_H | PYNQ |
| 36 | DE10-Nano | FPGA+ARM | 2x A9 | 1GB | 110K LE | GbE | $190 | T12-FPGA_H | Debian |
| 37 | KV260 | FPGA+ARM | 4x A53 | 4GB | DPU 0.92 TOPS | GbE | $249 | T12-FPGA_H | Kria Ubuntu |
| 38 | ZUBoard 1CG | FPGA+ARM | 2x A53 | 1GB | 81K LE | GbE | $159 | T12-FPGA_H | Petalinux |
| 39 | Colorlight 5A-75B | FPGA | VexRiscv soft | 2MB | 25K LUT ECP5 | Dual GbE | $15 | T11-FPGA_S | LiteX |
| 40 | ULX3S | FPGA | VexRiscv soft | 32MB | 84K LUT ECP5 | WiFi | $195 | T11-FPGA_S | LiteX |
| 41 | EBAZ4205 | FPGA+ARM | 2x A9 | 256MB | 28K LUT Artix-7 | GbE | $12 | T11-FPGA_S | OpenXC7 |
| 42 | EPYC 7742 server | x86 Srv | 64c/128t Z2 | up to 4TB | PCIe Gen4 | Dual GbE | $900 used | T1-CORE | Native |
| 43 | EPYC 7713 server | x86 Srv | 64c/128t Z3 | up to 4TB | PCIe Gen4 | Dual GbE | $1,200 used | T1-CORE | Native |
| 44 | Ampere Altra Q80-30 | ARM Srv | 80x N1 | up to 4TB | PCIe Gen4 | Dual 25GbE | $1,500 | T1-CORE | Native |
| 45 | Ampere Altra Max M128 | ARM Srv | 128x N1 | up to 4TB | PCIe Gen4 | Dual 25GbE | $2,500 | T1-CORE | Native |
| 46 | Minisforum MS-01 | Mini PC | i9-13900H 14c | 64GB | UHD | 2x 10GbE SFP+ | $679 | T3-EDGE | Native |
| 47 | ASUS NUC 14 Pro | Mini PC | Core Ultra 7 | 96GB | Arc NPU | 2x 2.5GbE | $869 | T3-EDGE | Native |
| 48 | Mac Studio M3 Ultra | Workstation | 32c M3 Ultra | up to 512GB | 80-core GPU | 10GbE+TB4 | $3,995 | T2-SEMI | macOS |
| 49 | AWS Graviton4 (c8g) | Cloud | 96 vCPU V2 | up to 3TB | None | 100 GbE | $0.011/vCPU/hr | T13-CLOUD | N/A |
| 50 | Used A100 40GB | GPU | N/A | 40GB HBM | 312 TFLOPS | PCIe | $5,000 used | T2-SEMI | CUDA |
| 51 | AMD MI210 GPU | GPU | N/A | 64GB HBM | 181 TFLOPS | PCIe | $2,500 used | T2-SEMI | ROCm |
| 52 | GL.iNet GL-MT6000 | Router | 4x A53 @ 2.0 | 1GB | None | 2x 2.5GbE+4xGbE | $159 | T6-NET_GW | OpenWrt |
| 53 | GL.iNet GL-MT3000 | Router | 2x A53 @ 1.3 | 512MB | None | 1x 2.5GbE | $89 | T6-NET_GW | OpenWrt |
| 54 | NanoPi R6S (router) | Gateway | 4xA76+4xA55 | 8GB | 6 TOPS | 2x 2.5GbE | $139 | T6-NET_GW | Armbian |
| 55 | Synology DS923+ | NAS | Ryzen R1600 | 4-32GB | None | 2x GbE | $550 | T7-STORAGE | DSM Docker |
| 56 | QNAP TS-464 | NAS | Celeron N5095 | 4-16GB | UHD | 2x 2.5GbE | $450 | T7-STORAGE | QTS Docker |
| 57 | LG webOS TV | Smart TV | 2-4x ARM | 2-4GB | None | Wi-Fi 5 | N/A | T3-EDGE | webOS |
| 58 | NVIDIA Shield TV Pro | Smart TV | Tegra X1+ | 3GB | Maxwell | GbE+WiFi5 | $199 | T3-EDGE | Android TV |
| 59 | Samsung Tizen TV | Smart TV | 2-4x ARM | 1.5-3GB | None | Wi-Fi 5 | N/A | T3-EDGE | Tizen |
| 60 | Siemens IoT2050 | Industrial | 4x A53 | 2GB | None | GbE+serial | $350 | T3-EDGE | Industrial |
| 61 | Groq LPU | AI Accel | TSP | ~230MB SRAM | 300-500 tok/s | N/A | Cloud | T14-EXOTIC | API |
| 62 | Cerebras CS-3 | AI Accel | WSE-3 900Kc | 44GB SRAM | 125 PFLOPS | N/A | $2-3M | T14-EXOTIC | API |
| 63 | IBM z17 | Mainframe | Telum II | 64TB | AI accel | Dedicated | Enterprise | T14-EXOTIC | z/OS+Linux |
| 64 | Intel Loihi 2 | Neuro | 128 async | N/A | Research | N/A | $2,500 kit | T14-EXOTIC | Lava SDK |

---

### 8.3 Security Model

Five trust levels govern the 15 tiers. Sandboxing escalates as trust decreases.

#### 8.3.1 Five Trust Tiers

| Trust | Tiers | Sandbox | Verification | Examples |
|-------|-------|---------|-------------|----------|
| **FULL** | T1, T10, T11 | None (native) | Open firmware / user boot | EPYC (Coreboot), RISC-V, FPGA |
| **STANDARD** | T2, T4, T5, T12 | Docker | Signed boot, seccomp, AppArmor | ROCK 5B, Jetson, Ampere |
| **SEMI** | T3, T7, T8 | gVisor/Kata | Runtime security, net policy | Mini PCs, NAS, TVs |
| **EDGE** | T6, T9, T13 | Kata/VM | Full sandbox, read-only, proxy | Routers, handhelds, spot |
| **EXOTIC** | T14 | API proxy | Vendor-dependent, isolated | Groq, Cerebras, quantum |

Trust flows downward only via administrative attestation.

#### 8.3.2 YAML Tier Definitions (All 15 Tiers)

```yaml
apiVersion: helixcluster.io/v1
kind: TierDefinitions
metadata:
  version: "5.0"
  date: "2025-07-01"

spec:
  tiers:
    - id: T1
      name: CORE_TRUSTED
      trust: FULL
      sandbox: none
      isolation: native
      access: unrestricted
      min: {cpu_cores: 16, ram_gb: 64, storage_gb: 500, network_mbps: 1000}

    - id: T2
      name: SEMI_TRUSTED
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: containerized
      min: {cpu_cores: 4, ram_gb: 4, storage_gb: 32, network_mbps: 1000}
      compute_classes: [cpu, npu]

    - id: T3
      name: EDGE_COMPUTE
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: sandboxed
      min: {cpu_cores: 2, ram_gb: 2, storage_gb: 16}

    - id: T4
      name: AI_WORKER
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: ai_workloads
      min: {cpu_cores: 4, ram_gb: 4, npu_tops: 20, storage_gb: 64}
      compute_classes: [cpu, npu, gpu]

    - id: T5
      name: AI_CONTROLLER
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: ai_controller
      min: {cpu_cores: 8, ram_gb: 32, npu_tops: 100, storage_gb: 256}
      compute_classes: [cpu, npu, gpu]

    - id: T6
      name: NETWORK_GATEWAY
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: gateway_only
      min: {cpu_cores: 2, ram_gb: 1, storage_gb: 8, network_ports_gbe: 2}

    - id: T7
      name: STORAGE_NODE
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: storage
      min: {cpu_cores: 2, ram_gb: 4, storage_bays: 2, network_mbps: 1000}

    - id: T8
      name: BUDGET
      trust: SEMI
      sandbox: gvisor
      isolation: sandboxed_container
      access: lightweight_only
      min: {cpu_cores: 2, ram_gb: 2, storage_gb: 8}

    - id: T9
      name: HANDHELD
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: opportunistic
      min: {cpu_cores: 4, ram_gb: 16, gpu_tflops: 1.0}
      scheduling: {power_aware: true, battery_threshold_pct: 20}

    - id: T10
      name: RISC_V_EXPERIMENTAL
      trust: FULL
      sandbox: none
      isolation: native
      access: experimental
      min: {cpu_cores: 4, ram_gb: 4}
      constraints: {max_workload_duration: 1h, no_sensitive_data: true}

    - id: T11
      name: FPGA_SOFT_CORE
      trust: FULL
      sandbox: none
      isolation: native
      access: fpga_only
      min: {fpga_lut_k: 25, ram_mb: 32}
      constraints: {bitstream_verification: required}

    - id: T12
      name: FPGA_HARD_ACCEL
      trust: STANDARD
      sandbox: docker
      isolation: container
      access: fpga_accelerated
      min: {cpu_cores: 2, ram_gb: 1, fpga_lut_k: 80}
      compute_classes: [cpu, fpga]

    - id: T13
      name: CLOUD_BURST
      trust: EDGE
      sandbox: kata
      isolation: vm
      access: ephemeral
      min: {cpu_cores: 2, ram_gb: 4}
      constraints: {preemptible: true, checkpoint_required: true, max_runtime: 4h}

    - id: T14
      name: EXOTIC_ACCEL
      trust: EXOTIC
      sandbox: api_proxy
      isolation: network_segment
      access: specialized
      constraints: {manual_approval: true, api_key_required: true}

    - id: T15
      name: LEGACY_RETIRED
      trust: none
      sandbox: isolated
      access: none
      status: deprecated
```

**Invariants:** (1) Sensitive data only on FULL or STANDARD. (2) Control plane quorum on FULL only. (3) Handhelds and spot instances: stateless batch with checkpoint/resume. (4) EXOTIC nodes receive API calls only. (5) Attestation failures trigger automatic downgrade.

---

### 8.4 Recommended Cluster Builds

Five validated configurations with exact pricing.

#### 8.4.1 Build 1: $250 Budget Edge

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Odroid M1S 8GB | 2 | $59 | $118 |
| GL.iNet GL-MT3000 | 1 | $89 | $89 |
| 128 GB microSD | 3 | $10 | $30 |
| USB Ethernet adapters | 2 | $5 | $10 |
| **Total** | **3 nodes** | | **$247** |

M1S units run Pi-hole, Prometheus, MQTT. MT3000 is WireGuard gateway. ~15W.

#### 8.4.2 Build 2: $500 AI Starter

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Jetson Orin Nano Super 8GB | 1 | $249 | $249 |
| NanoPi R6C 8GB | 1 | $125 | $125 |
| Radxa ROCK 5C 4GB | 1 | $75 | $75 |
| 256 GB NVMe SSD | 2 | $20 | $40 |
| 5-port GbE switch | 1 | $10 | $10 |
| **Total** | **3 nodes** | | **$499** |

Orin Nano Super: TensorRT inference (67 TOPS). R6C: RKNN edge AI + routing. ROCK 5C: general compute. 73 TOPS aggregate AI. ~35W.

#### 8.4.3 Build 3: $1,000 Home Lab

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Radxa ROCK 5B 8GB | 4 | $157 | $628 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| 256 GB NVMe SSD | 4 | $20 | $80 |
| 1 TB SATA SSD | 1 | $45 | $45 |
| 8-port 2.5GbE switch | 1 | $40 | $40 |
| PSUs + cases | 4 | $12 | $48 |
| **Total** | **5 nodes** | | **$980** |

Four ROCK 5B: 32 cores, 32 GB RAM, 24 TOPS NPU. R6S: WireGuard gateway. Runs K3s, PostgreSQL, Redis, object detection. ~55W.

#### 8.4.4 Build 4: $2,000 ARM Density

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Turing Pi 2.5 carrier | 1 | $279 | $279 |
| Turing RK1 16GB | 4 | $160 | $640 |
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| 512 GB NVMe SSD | 5 | $40 | $200 |
| 1 TB NVMe SSD | 1 | $60 | $60 |
| PSU + cooling kit | 1 | $100 | $100 |
| 8-port 2.5GbE switch | 1 | $45 | $45 |
| SATA SSDs | 2 | $25 | $50 |
| **Total** | **7 nodes** | | **$1,762** |

Turing Pi hosts four RK1 modules. Jetson Orin Nano Super for AI. R6S as 2.5GbE gateway. 32 RK3588 cores + 6 Orin cores, 73 TOPS. ~85W.

#### 8.4.5 Build 5: $5,000+ Production

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| EPYC 7713 server (128 GB ECC) | 1 | $1,200 | $1,200 |
| Ampere Altra Q80-30 (256 GB) | 1 | $1,500 | $1,500 |
| Jetson AGX Orin 64GB | 1 | $1,599 | $1,599 |
| Minisforum MS-01 (64 GB) | 1 | $679 | $679 |
| 8-port 10GbE SFP+ switch | 1 | $350 | $350 |
| 10GbE DAC cables 1m | 4 | $15 | $60 |
| 1 TB NVMe enterprise SSD | 4 | $80 | $320 |
| 4 TB SATA SSD | 4 | $180 | $720 |
| Rackmount chassis + PDU | 1 | $200 | $200 |
| **Total** | **4 nodes** | | **$6,628** |

EPYC 7713: K3s control plane (64c/128t). Altra Q80-30: ARM containers (80c, 256 GB). AGX Orin 64GB: AI controller (275 TOPS). MS-01: 10GbE gateway. 144 x86 cores + 80 ARM cores + 275 TOPS. ~600W.


---

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


---

