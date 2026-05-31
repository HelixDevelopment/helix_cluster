# HelixCluster Phase 5 -- Advanced & Exotic Device Ecosystem Architecture

> **Version:** 1.0
> **Date:** July 2025
> **Classification:** Architecture Specification
> **Scope:** Comprehensive integration architecture for all advanced, exotic, and emerging compute devices into the HelixCluster distributed compute platform

---

## 1. Executive Summary

HelixCluster Phase 5 extends the device ecosystem beyond Phases 1-3 (PC, Console, Mobile/Edge) to encompass **seven new dimensions** of compute: gaming handhelds, advanced ARM SBCs, RISC-V emerging architectures, FPGA accelerators, enterprise/server/cloud nodes, IoT/smart/edge devices, and exotic/future technologies. This document provides the definitive architecture for integrating **60+ device types** across **15 tiers** into a unified compute fabric.

### Key Metrics at a Glance

| Metric | Value |
|--------|-------|
| **Total Device Types** | 60+ (up from 12 in Phase 3) |
| **Tier Count** | 15 tiers (up from 5) |
| **Architecture Coverage** | x86, ARM, RISC-V, FPGA, POWER, LoongArch, z/Architecture |
| **Min Node Price** | $15 (Colorlight 5A-75B FPGA) |
| **Max Node Price** | $2,847 (Jetson Thor T5000) / $13,195 (VCK190 FPGA) |
| **Aggregate TOPS Range** | 0 (router) to 275 TOPS (Jetson AGX Orin) to 2070 TFLOPS (Thor) |
| **Power Range** | 0.5W (ULX3S FPGA) to 360W (EPYC 9654) |
| **Cloud-Connected Node Types** | 3 (AWS Graviton, Azure, GCP via WireGuard mesh) |

### Primary Integration Targets (Immediate -- 2025)

1. **Steam Deck / Steam Deck OLED** -- 500K-1M+ potential volunteer nodes; native Linux, 1.6 TFLOPS GPU, 16GB unified memory
2. **NVIDIA Jetson Orin Nano Super** -- 67 TOPS at $249; premier AI inference edge node
3. **Radxa ROCK 5B + RK3588 ecosystem** -- Best price/performance ARM cluster boards; 2.5GbE, NVMe, 6 TOPS NPU
4. **GL.iNet GL-MT6000 router** -- $159 edge compute node with Docker, dual 2.5GbE, quad-core A53
5. **Used AMD EPYC servers** -- 64 cores for under $1,000; ultimate density-per-dollar compute

### Watch List (6-18 Months)

- Nintendo Switch 2 homebrew (potential 3+ TFLOPS Ampere GPU nodes)
- RISC-V RVA23-profile chips (SiFive P870 competitive performance)
- Groq LPU / NVIDIA LPU integration (LLM inference tier)
- Cerebras CS-3 cloud/on-prem API integration

---

## 2. Complete Device Taxonomy

### 2.1 HelixCluster Tier System (Extended)

Phase 5 introduces a 15-tier classification system extending the original 5-tier model. Each tier maps to a trust level, compute class, and deployment pattern.

```
TIER 1:  CORE_TRUSTED         -- Primary control plane, databases
TIER 2:  SEMI_TRUSTED         -- Containerized workloads, CI/CD
TIER 3:  EDGE_COMPUTE         -- Field-deployed compute nodes
TIER 4:  AI_WORKER            -- ML inference, GPU/NPU accelerated
TIER 5:  AI_CONTROLLER        -- High-throughput inference head nodes
TIER 6:  NETWORK_GATEWAY      -- Routers, ingress controllers
TIER 7:  STORAGE_NODE         -- Distributed storage (Ceph/MinIO)
TIER 8:  BUDGET               -- Cost-sensitive lightweight nodes
TIER 9:  HANDHELD             -- Gaming handhelds, volunteer-owned
TIER 10: RISC_V_EXPERIMENTAL  -- Emerging architecture nodes
TIER 11: FPGA_SOFT_CORE       -- Soft-core CPU on FPGA fabric
TIER 12: FPGA_HARD_ACCEL      -- FPGA with hard ARM cores + accelerators
TIER 13: CLOUD_BURST          -- Spot/preemptible cloud instances
TIER 14: EXOTIC_ACCEL         -- Quantum, neuromorphic, photonic (research)
TIER 15: LEGACY_RETIRED       -- EOL devices, do not deploy new
```

#### Trust Levels

| Trust Level | Description | Device Examples |
|-------------|-------------|-----------------|
| **TRUSTED** | User-controlled Linux, open firmware, auditable | x86 desktop, OpenPOWER, RISC-V (open boards) |
| **SEMI_TRUSTED** | Linux with proprietary firmware/boot, well-documented | ARM SBCs, Jetson, Steam Deck, Ampere servers |
| **EDGE** | Containerized/sandboxed, limited privileges | Routers, NAS, Smart TVs, Mini PCs |
| **UNTRUSTED** | Requires sandboxing, limited workload eligibility | Volunteer handhelds, cloud spot, closed IoT |
| **RESEARCH** | Experimental, not for production workloads | Quantum simulators, neuromorphic dev kits |

#### Compute Classes

| Class | Symbol | Description |
|-------|--------|-------------|
| **General Purpose** | `cpu` | Standard CPU workloads, containers |
| **GPU Compute** | `gpu` | Vulkan/CUDA/ROCm accelerated |
| **Neural Processing** | `npu` | Dedicated AI inference hardware |
| **Tensor Accelerator** | `tpu` | Google TPU, Jetson DLA, etc. |
| **FPGA Fabric** | `fpga` | Reconfigurable logic |
| **Quantum** | `qpu` | Quantum processing (research) |

### 2.2 Device Classification Matrix

#### Master Device Table (60+ Devices)

| # | Device | Architecture | Tier | Trust | Compute Class | Cores | RAM | Price | Key Spec |
|---|--------|-------------|------|-------|--------------|-------|-----|-------|----------|
| 1 | Steam Deck LCD | x86 Zen 2 + RDNA 2 | T9 | UNTRUSTED | cpu,gpu | 4c/8t | 16GB | $279 (refurb) | 1.6 TFLOPS GPU |
| 2 | Steam Deck OLED | x86 Zen 2 + RDNA 2 | T9 | UNTRUSTED | cpu,gpu | 4c/8t | 16GB | $789 | Wi-Fi 6E, 6nm |
| 3 | ROG Ally X | x86 Zen 4 + RDNA 3 | T9 | UNTRUSTED | cpu,gpu | 8c/16t | 24GB | $999 | 8.6 TFLOPS GPU |
| 4 | Lenovo Legion Go | x86 Zen 4 + RDNA 3 | T9 | UNTRUSTED | cpu,gpu | 8c/16t | 16GB | $699 | 8.6 TFLOPS GPU |
| 5 | GPD Win 4 2025 | x86 Zen 5 + RDNA 3.5 | T9 | UNTRUSTED | cpu,gpu | 12c/24t | 32GB | $1,200 | 11.88 TFLOPS GPU |
| 6 | Nintendo Switch | ARM A57 + Maxwell | T15 | RESEARCH | cpu | 4x A57 | 4GB | EOL | Homebrew only |
| 7 | Nintendo Switch 2 | ARM A78C + Ampere | T10 | RESEARCH | cpu,gpu | 8x A78C | 12GB | $449 | Watch -- no homebrew yet |
| 8 | Xbox Series X | x86 Zen 2 + RDNA 2 | T15 | LEGACY | -- | 8x Zen 2 | 16GB | $499 | No Linux path |
| 9 | Jetson Orin Nano Super | ARM A78AE + Ampere | T4 | SEMI | cpu,npu,gpu | 6x A78AE | 8GB | $249 | **67 TOPS, top AI pick** |
| 10 | Jetson Orin NX 16GB | ARM A78AE + Ampere | T4 | SEMI | cpu,npu,gpu | 8x A78AE | 16GB | $600 | 157 TOPS |
| 11 | Jetson AGX Orin 64GB | ARM A78AE + Ampere | T5 | SEMI | cpu,npu,gpu | 12x A78AE | 64GB | $1,599 | 275 TOPS |
| 12 | Jetson Thor T5000 | Neoverse-V3 + Blackwell | T5 | SEMI | cpu,npu,gpu | 14x V3AE | 128GB | $2,847 | 2070 FP4 TFLOPS |
| 13 | Radxa ROCK 5B | ARM A76/A55 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 4-32GB | $157 | 2.5GbE, PCIe 3.0 x4 |
| 14 | NanoPi R6S | ARM A76/A55 + Mali | T6 | SEMI | cpu,npu | 8 (4+4) | 8GB | $139 | **Dual 2.5GbE** |
| 15 | NanoPi R6C | ARM A76/A55 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 4-8GB | $85 | 1x 2.5GbE + NVMe |
| 16 | Banana Pi BPI-M7 | ARM A76/A55 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 8-32GB | $165 | Dual 2.5GbE + WiFi 6 |
| 17 | Mixtile Blade 3 | ARM A76/A55 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 4-32GB | $195 | U.2 stacking, dual 2.5GbE |
| 18 | Turing RK1 (module) | ARM A76/A55 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 8-32GB | $110 | CM4-compatible, 7W |
| 19 | Turing Pi 2.5 (carrier) | N/A (holds 4x RK1) | T2 | SEMI | -- | 32 total | up to 128GB | $279 | Mini-ITX, GbE switch |
| 20 | FriendlyELEC CM3588 NAS | ARM A76/A55 + Mali | T7 | SEMI | cpu,npu | 8 (4+4) | 4-32GB | $160 | **4x M.2 NVMe slots** |
| 21 | Firefly ITX-3588J | ARM A76/A55 + Mali | T7 | SEMI | cpu,npu | 8 (4+4) | 4-32GB | $449 | 4x SATA, mini-ITX, PoE |
| 22 | Odroid M1 | ARM A55 + Mali | T7 | SEMI | cpu,npu | 4x A55 | 4-8GB | $70 | SATA + NVMe, supply til 2036 |
| 23 | Odroid M1S | ARM A55 + Mali | T8 | SEMI | cpu | 4x A55 | 4-8GB | $59 | PCIe 2.1, includes case+PSU |
| 24 | Odroid N2+ | ARM A73/A53 + Mali | T8 | SEMI | cpu | 6 (4+2) | 2-4GB | $69 | Budget, reliable |
| 25 | Khadas VIM4 | ARM A73/A53 + Mali | T2 | SEMI | cpu,npu | 8 (4+4) | 8GB | $220 | A311D2, HDMI input |
| 26 | Khadas Edge2 | ARM A76/A55 + Mali | T3 | SEMI | cpu,npu | 8 (4+4) | 8-16GB | $199 | WiFi only, 5.7mm thin |
| 27 | BeagleBone AI-64 | TI TDA4VM | T2 | SEMI | cpu,npu | 2x A72 + 6x R5F | 4GB | $185 | 8 TOPS, industrial I/O |
| 28 | Milk-V Pioneer | RISC-V C920 (64x) | T10 | TRUSTED | cpu | 64x C920 | up to 128GB | $1,199 | Build farm, 2.5GbE |
| 29 | SiFive HiFive Premier P550 | RISC-V P550 (4x) | T10 | TRUSTED | cpu | 4x P550 | 16-32GB | $399 | Best single-core RISC-V |
| 30 | Milk-V Jupiter (K1/M1) | RISC-V X60 (8x) | T10 | TRUSTED | cpu,npu | 8x X60 | 4-16GB | $150 | First RVV 1.0, 2 TOPS |
| 31 | VisionFive 2 / Mars | RISC-V U74 (4x) | T10 | TRUSTED | cpu | 4x U74 | 1-8GB | $70 | Most mature RISC-V SBC |
| 32 | Kendryte K230 | RISC-V C908 (2x) | T14 | RESEARCH | npu | 2x C908 | 0.5-2GB | $49 | 6 TOPS KPU, AI edge |
| 33 | Loongson 3A6000 | LoongArch (4x) | T10 | SEMI | cpu | 4 (SMT) | 8-16GB | $300 | Zen 1-2 class performance |
| 34 | POWER9 Blackbird | POWER9 (8x) | T10 | TRUSTED | cpu | 8 cores | up to 256GB | $1,600 | Fully open firmware |
| 35 | PYNQ-Z2 | Zynq-7000 (2x A9) | T12 | SEMI | cpu,fpga | 2x A9 | 512MB | $129 | Python FPGA, GbE |
| 36 | DE10-Nano | Cyclone V (2x A9) | T12 | SEMI | cpu,fpga | 2x A9 | 1GB | $190 | Best value Linux FPGA |
| 37 | KV260 | Zynq UltraScale+ | T12 | SEMI | cpu,fpga,npu | 4x A53 | 4GB | $249 | DPU 0.92 TOPS |
| 38 | ZUBoard 1CG | Zynq UltraScale+ | T12 | SEMI | cpu,fpga | 2x A53 | 1GB | $159 | Cheapest UltraScale+ |
| 39 | Colorlight 5A-75B | Lattice ECP5 | T11 | TRUSTED | fpga | VexRiscv soft | 2MB | $15 | Dual GbE, $15 entry! |
| 40 | ULX3S | Lattice ECP5 | T11 | TRUSTED | fpga | VexRiscv soft | 32MB | $195 | Open-source champion |
| 41 | EBAZ4205 | Zynq-7000 (2x A9) | T11 | TRUSTED | cpu,fpga | 2x A9 | 256MB | $12 | Repurposed mining board |
| 42 | AMD EPYC 7742 server | x86 Zen 2 | T1 | TRUSTED | cpu | 64c/128t | up to 4TB | $900 (used) | 128x PCIe Gen4 |
| 43 | AMD EPYC 7713 server | x86 Zen 3 | T1 | TRUSTED | cpu | 64c/128t | up to 4TB | $1,200 (used) | Best value modern server |
| 44 | Ampere Altra Q80-30 | ARM Neoverse N1 | T1 | SEMI | cpu | 80c | up to 4TB | $1,500 | 128x PCIe Gen4 |
| 45 | Ampere Altra Max M128 | ARM Neoverse N1 | T1 | SEMI | cpu | 128c | up to 4TB | $2,500 | Maximum core density |
| 46 | Minisforum MS-01 | x86 (i9-13900H) | T3 | SEMI | cpu | 14c/20t | 64GB | $679 | **Dual 10GbE SFP+** |
| 47 | ASUS NUC 14 Pro | x86 (Core Ultra 7) | T3 | SEMI | cpu,npu | 16c/22t | 96GB | $869 | vPro, AI NPU |
| 48 | Mac Studio M3 Ultra | ARM (Apple) | T2 | SEMI | cpu,gpu,npu | 32c | up to 512GB | $3,995 | 819 GB/s, 80 GPU cores |
| 49 | AWS Graviton4 (c8g) | ARM Neoverse V2 | T13 | UNTRUSTED | cpu | 96 vCPU | up to 3TB | ~$0.011/vCPU/hr | Cloud spot |
| 50 | Used A100 40GB GPU | NVIDIA Ampere | T2 | SEMI | gpu | -- | 40GB HBM | $5,000 | 312 TFLOPS FP16 |
| 51 | AMD MI210 GPU | AMD CDNA 2 | T2 | SEMI | gpu | -- | 64GB HBM | $2,500 | 181 TFLOPS FP16, ROCm |
| 52 | GL.iNet GL-MT6000 | ARM A53 | T6 | EDGE | cpu | 4x A53 @ 2.0 | 1GB | $159 | **Docker + dual 2.5GbE** |
| 53 | GL.iNet GL-MT3000 | ARM A53 | T6 | EDGE | cpu | 2x A53 @ 1.3 | 512MB | $89 | Budget router node |
| 54 | NanoPi R6S (router) | ARM A76/A55 | T6 | EDGE | cpu,npu | 8 (4+4) | 8GB | $139 | Compute-class router |
| 55 | Synology DS923+ | x86 (Ryzen R1600) | T7 | EDGE | cpu | 2C/4T | 4-32GB | $550 | Docker, 4-bay NAS |
| 56 | QNAP TS-464 | x86 (Celeron N5095) | T7 | EDGE | cpu | 4C | 4-16GB | $450 | Dual 2.5GbE, Docker |
| 57 | LG webOS TV | ARM (varies) | T3 | EDGE | cpu | 2-4x ARM | 2-4GB | N/A | Node.js background svcs |
| 58 | NVIDIA Shield TV Pro | ARM + Tegra X1+ | T3 | EDGE | cpu,gpu | 4x ARM | 3GB | $199 | Android, most open TV |
| 59 | Samsung Tizen TV | ARM (varies) | T3 | EDGE | cpu | 2-4x ARM | 1.5-3GB | N/A | Limited Node.js bg svcs |
| 60 | Siemens IoT2050 | ARM A53 | T3 | EDGE | cpu | 4x A53 | 2GB | $350 | Industrial I/O gateway |
| 61 | Groq LPU | Custom TSP | T14 | SEMI | npu | N/A | ~230MB/chip | Cloud/On-prem | 300-500 tok/s 70B |
| 62 | Cerebras CS-3 | WSE-3 (900K cores) | T14 | SEMI | npu | 900K | 44GB SRAM | $2-3M | 125 PFLOPS FP16 |
| 63 | IBM z17 mainframe | Telum II | T14 | TRUSTED | cpu,npu | 208 (max) | 64TB | Very expensive | Ultra-secure workloads |
| 64 | Intel Loihi 2 | Neuromorphic | T14 | RESEARCH | npu | 128 async | N/A | $2,500 (dev kit) | Research only |

*Prices are approximate as of mid-2025, in USD. Used/refurbished pricing shown where applicable.*

---

## 3. Gaming & Handheld Integration

### 3.1 Steam Deck / Steam Deck OLED

The Steam Deck is the single most promising non-PC compute node for HelixCluster. With 4M+ units shipped, native SteamOS (Arch Linux), and a 1.6 TFLOPS RDNA 2 GPU, it represents an untapped pool of latent compute capacity.

#### Hardware Architecture

| Component | Steam Deck LCD | Steam Deck OLED |
|-----------|---------------|-----------------|
| APU | AMD "Aerith" 7nm | AMD "Sephiroth" 6nm |
| CPU | Zen 2 4c/8t @ 3.5 GHz | Zen 2 4c/8t @ 3.5 GHz |
| GPU | 8 CU RDNA 2 @ 1.6 GHz | 8 CU RDNA 2 @ 1.6 GHz |
| GPU FLOPS | 1.6 TFLOPS FP32 | 1.6 TFLOPS FP32 |
| RAM | 16 GB LPDDR5-5500 | 16 GB LPDDR5-6400 |
| RAM Bandwidth | ~88 GB/s | ~102 GB/s |
| TDP Range | 4-15W | 4-15W |
| Network | Wi-Fi 5 | Wi-Fi 6E tri-band |

#### Integration Architecture

SteamOS 3.0+ is Arch Linux with KDE Plasma desktop. Full desktop mode provides terminal, pacman package manager, Flatpak, systemd, and Docker (via distrobox or rootful).

**Compute Backends:**

| API | Support Level | Workaround Required |
|-----|--------------|---------------------|
| Vulkan Compute 1.3+ | **Native, excellent** | None (Mesa RADV) |
| OpenCL 3.0 | Good | Mesa Rusticl |
| ROCm/HIP | Unofficial but functional | `HSA_OVERRIDE_GFX_VERSION=10.3.0` |

```dockerfile
# HelixCluster Steam Deck Agent Container
FROM archlinux:latest

RUN pacman -S --noconfirm mesa vulkan-radeon vulkan-tools opencl-rusticl-mesa

# ROCm workaround for RDNA 2 iGPU
ENV HSA_OVERRIDE_GFX_VERSION=10.3.0
RUN pacman -S --noconfirm rocm-opencl-runtime

# llama.cpp with Vulkan backend for inference
RUN pacman -S --noconfirm llama-cpp-vulkan

COPY helixcluster-agent-steamdeck /usr/local/bin/
ENTRYPOINT ["helixcluster-agent-steamdeck", "--backends=vulkan,cpu"]
```

**Key Insight:** The 16GB unified memory is shared between CPU and GPU, enabling the GPU to access the full 16GB for compute workloads. This is particularly valuable for ML inference where model size matters -- a Steam Deck can run 7B parameter models (Q4_0 quantized ~4GB) with room to spare, unlike discrete GPUs with limited VRAM.

#### Power-Aware Workload Scheduling

The Steam Deck's adjustable TDP (4-15W) and battery-powered nature require intelligent scheduling:

| Device State | CPU Quota | GPU Quota | TDP | Policy |
|--------------|-----------|-----------|-----|--------|
| Gaming (active) | 25% (1 core) | 0% | 4W | Background CPU only |
| Docked + idle | 100% | 100% | 15W | Full compute allowed |
| Battery > 50% | 50% | 50% | 10W | Reduced GPU clocks |
| Battery < 20% | 0% | 0% | 4W | **Pause all compute** |
| Charging + idle | 100% | 100% | 15W | Full compute allowed |

### 3.2 x86 Handhelds (ROG Ally, Legion Go, GPD Win)

x86 handhelds share the same integration architecture as the Steam Deck but offer significantly higher performance.

| Device | CPU | GPU | GPU FLOPS | RAM | TDP | Linux Support |
|--------|-----|-----|-----------|-----|-----|---------------|
| ROG Ally X | Zen 4 8c/16t | 12 CU RDNA 3 | 8.6 TFLOPS | 24GB | 9-30W | Excellent (Bazzite) |
| Legion Go | Zen 4 8c/16t | 12 CU RDNA 3 | 8.6 TFLOPS | 16GB | 9-30W | Full (Legion Go S ships SteamOS) |
| GPD Win 4 2025 | Zen 5 12c/24t | 16 CU RDNA 3.5 | 11.88 TFLOPS | 32GB | 20-35W | Full (OCuLink eGPU) |
| Ayaneo Next Lite | Zen 2 6c/12t | Vega-class | ~2 TFLOPS | 16GB | 15-25W | HoloISO pre-installed |

All x86 handhelds support the same compute stack: Vulkan Compute (primary), OpenCL via Mesa Rusticl, and ROCm/HIP with `HSA_OVERRIDE_GFX_VERSION=11.0.0` for RDNA 3.

**Recommendation:** Use **Vulkan Compute as the primary API** across all handheld AMD APUs. It is fully supported by Mesa RADV without workarounds, well-tested via llama.cpp, and lower overhead than OpenCL on RDNA.

### 3.3 Nintendo Integration

#### Nintendo Switch (Original) -- Tegra X1

The original Switch requires homebrew exploit (RCM for unpatched units, modchip for patched). L4T Ubuntu 24.04 is available via switchroot project. Critical limitations:

- **No CUDA** -- Maxwell GPU has only 2 SMs with cut-down shared memory
- **No OpenCL** -- Vulkan compute shaders only
- 4GB RAM (severely limiting)
- ~393 GFLOPS FP32 GPU max (docked)
- Nintendo actively bans modded consoles online

**Verdict:** Not a practical HelixCluster node. Suitable only for hobbyist experimentation at Tier 15 (RESEARCH).

#### Nintendo Switch 2 -- T239 "Drake" Platform

| Component | Specification |
|-----------|--------------|
| CPU | 8x ARM Cortex-A78C @ 1.7 GHz |
| GPU | NVIDIA Ampere, 1536 CUDA cores |
| GPU FLOPS | ~3.1 TFLOPS FP32 (docked, estimated) |
| RAM | 12 GB LPDDR5 (102 GB/s docked) |
| Storage | 256 GB + microSD Express (NVMe) |

**Current Status (June 2025):** No public jailbreak exists. The homebrew scene is in early exploration. Industry estimates suggest a modchip or softmod may surface within 6-18 months. The T239's enhanced security (lessons from Tegra X1 RCM exploit) may delay this.

**Potential Value:** If homebrew succeeds, the Switch 2 would be a genuinely capable compute node -- 3-5x the Steam Deck's GPU performance with CUDA support on mobile Ampere. **Timeline estimate: 12-24 months before viable HelixCluster integration.**

### 3.4 GPU Compute from Handhelds

#### Vulkan Compute Performance Estimates

| Device | llama.cpp 7B Q4_0 (pp512) | llama.cpp 7B Q4_0 (tg128) | Power |
|--------|---------------------------|---------------------------|-------|
| Steam Deck | ~60-80 t/s | ~8-12 t/s | 4-15W |
| ROG Ally X | ~120-160 t/s | ~20-30 t/s | 9-30W |
| GPD Win 4 (890M) | ~160-200 t/s | ~30-40 t/s | 20-35W |

*Estimates extrapolated from comparable RDNA 2/3 hardware benchmarks.*

#### Idle Compute Availability Model

Based on Steam Deck usage patterns (primarily evening/weekend gaming device):

| Time Period | Availability | Available Nodes (est.) |
|-------------|-------------|----------------------|
| Weekday 9am-5pm | **85-90% idle** | 400K-850K |
| Weekday 6pm-10pm | 40-60% idle (gaming prime time) | 150K-500K |
| Weekend daytime | 30-50% idle | 100K-400K |
| Overnight 12am-8am | **90-95% idle** | 450K-900K |

With a 2-5% opt-in rate (typical for distributed computing projects), HelixCluster could realistically attract **5,000-20,000 Steam Deck nodes immediately**, growing to **20,000-50,000** by 2026-2027 as the handheld market expands to 4.7M units annually.

---

## 4. Advanced ARM SBC Cluster Architecture

### 4.1 NVIDIA Jetson Integration

The Jetson family represents the most mature AI/ML edge platform. All Jetson devices run NVIDIA's L4T (Linux for Tegra), an Ubuntu derivative with full CUDA, TensorRT, cuDNN, and JetPack SDK support.

#### Jetson Device Matrix

| Device | AI Perf | GPU | CPU | RAM | Power | Price | Helix Tier |
|--------|---------|-----|-----|-----|-------|-------|------------|
| Orin Nano 4GB | 34 TOPS | 512 Ampere | 6x A78AE @ 1.7 | 4GB | 7-25W | $199 | T4 |
| **Orin Nano Super** | **67 TOPS** | 1024 Ampere | 6x A78AE @ 1.7 | 8GB | 7-25W | **$249** | **T4** |
| Orin NX 8GB | 117 TOPS | 1792 Ampere | 8x A78AE @ 2.0 | 8GB | 10-25W | $450 | T4 |
| Orin NX 16GB | 157 TOPS | 2048 Ampere | 8x A78AE @ 2.0 | 16GB | 10-25W | $600 | T5 |
| AGX Orin 32GB | 200 TOPS | 2048 Ampere | 12x A78AE @ 2.2 | 32GB | 15-60W | $999 | T5 |
| AGX Orin 64GB | 275 TOPS | 2048 Ampere | 12x A78AE @ 2.2 | 64GB | 15-60W | $1,599 | T5 |
| **Thor T5000** | **2070 FP4 TFLOPS** | 2560 Blackwell | 14x Neoverse-V3 | 128GB | 40-130W | ~$2,847 | **T5** |

**Critical Update:** The Orin Nano Super upgrade (Dec 2024) boosted existing 8GB kits from 40 to 67 TOPS via JetPack 6.2 software -- a free performance increase with no hardware changes.

#### TensorRT and AI Framework Stack

```yaml
# Jetson HelixCluster Agent Configuration
jetson_agent:
  device_class: "AI_WORKER"
  backends:
    primary: "tensorrt"
    secondary: "cuda"
    fallback: "cpu"

  tensorrt_config:
    precision: "INT8"  # or FP16, FP8 (Orin+), FP4 (Thor)
    workspace_mb: 2048
    dla_core: 0  # Deep Learning Accelerator, Orin NX/AGX only

  workload_eligibility:
    - "llm_inference_7b"
    - "llm_inference_13b"  # Orin NX 16GB+ and AGX only
    - "llm_inference_70b"  # Thor T5000 only
    - "object_detection"
    - "image_classification"
    - "embedding_generation"

  power_profile:
    mode: "MAXN"  # MAXN, 15W, 25W (device-dependent)
    thermal_limit_c: 85
```

The Jetson Orin Nano Super at **$249** is the **top recommendation for ML inference workloads** in HelixCluster. It delivers 67 TOPS INT8 with 102 GB/s memory bandwidth -- roughly equivalent to a GTX 1650 or T4 for quantized inference. It runs YOLOv5 at 30-60+ FPS and can handle 7B parameter LLMs (Llama 3.1, Mistral) at the edge.

### 4.2 RK3588 Ecosystem

The Rockchip RK3588 (4x Cortex-A76 @ 2.4 GHz + 4x Cortex-A55 @ 1.8 GHz, Mali-G610 MP4, 6 TOPS NPU) has become the dominant high-performance ARM SBC SoC. Multiple vendors offer boards with different I/O trade-offs.

#### Mainline Linux Status (as of Linux 6.12+)

| Component | Status |
|-----------|--------|
| GPU (Mali-G610 via Panfrost) | **Working** |
| 3D Acceleration | **Working** |
| USB3 / CPU freq scaling | **Working** |
| 2.5GbE (on ROCK 5B) | **Working** |
| HDMI display (6.13+) | **Working** |
| NPU acceleration | Vendor kernel only (RKNN-Toolkit2); mainline expected Q2 2025 |
| Video codecs (VP8, H.264) | Partial (VDPU121) |

**Practical implication:** For headless cluster nodes, mainline Linux 6.12+ is fully viable. For AI/ML NPU workloads, vendor kernels (Linux 5.10/5.15) with RKNN-Toolkit2 are still required.

#### RK3588 Board Comparison

| Board | RAM Options | Network | NVMe | Special | Price |
|-------|-------------|---------|------|---------|-------|
| **Radxa ROCK 5B** | 4-32GB | 1x 2.5GbE (PoE HAT) | PCIe 3.0 x4 | Best mainline support | $157 |
| **NanoPi R6S** | 8GB | **2x 2.5GbE + GbE** | No | Best networking | $139 |
| **NanoPi R6C** | 4-8GB | 1x 2.5GbE + GbE | M.2 | Balanced option | $85 |
| **Banana Pi BPI-M7** | 8-32GB | 2x 2.5GbE + WiFi 6 | M.2 | Compact 92x62mm | $165 |
| **Mixtile Blade 3** | 4-32GB | Dual 2.5GbE (LACP) | U.2 PCIe 3.0 x4 | Purpose-built cluster | $195 |
| **Turing RK1** | 8-32GB | GbE (via carrier) | M.2 (via carrier) | CM4-compatible module | $110 |
| **Firefly ITX-3588J** | 4-32GB | 2x GbE (1x PoE) | M.2 SATA | 4x SATA, mini-ITX | $449 |

### 4.3 Recommended SBC Cluster Builds

#### Build A: Budget AI Edge Cluster ($500)

| Component | Qty | Unit Price | Total |
|-----------|-----|-----------|-------|
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6C 8GB | 1 | $125 | $125 |
| 256GB NVMe SSD | 1 | $35 | $35 |
| 5V/4A PSU + case | 2 | $25 | $50 |
| 8-port GbE switch | 1 | $15 | $15 |
| **Total** | **3 nodes** | | **$474** |

*Use case:* Entry-level AI inference + general compute. Orin handles ML; R6C handles networking and containers.

#### Build B: Balanced RK3588 Cluster ($1,000)

| Component | Qty | Unit Price | Total |
|-----------|-----|-----------|-------|
| Radxa ROCK 5B 8GB | 3 | $157 | $471 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| FriendlyELEC CM3588 NAS 16GB | 1 | $160 | $160 |
| 512GB NVMe SSDs | 4 | $40 | $160 |
| 8-port 2.5GbE switch | 1 | $40 | $40 |
| PSUs + cases | 4 | $10 | $40 |
| **Total** | **5 nodes** | | **$1,010** |

*Use case:* General compute cluster with dedicated storage. 24 CPU cores + 18 TOPS NPU aggregate, 2.5GbE mesh.

#### Build C: Maximum Density Cluster ($2,000)

| Component | Qty | Unit Price | Total |
|-----------|-----|-----------|-------|
| Turing Pi 2.5 board | 1 | $279 | $279 |
| Turing RK1 16GB | 4 | $160 | $640 |
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| 512GB NVMe SSDs | 5 | $40 | $200 |
| 1TB NVMe SSD (for NAS) | 1 | $60 | $60 |
| PSU + cooling kit | 1 | $80 | $80 |
| 8-port 2.5GbE switch | 1 | $40 | $40 |
| SATA SSDs (for CM3588) | 4 | $30 | $120 |
| **Total** | **7 nodes** | | **$1,807** |

*Use case:* Maximum density in mini-ITX footprint. 32 RK3588 cores + 6 Orin cores + 73 TOPS NPU aggregate. Internal GbE switch on Turing Pi plus 2.5GbE external backhaul.

---

## 5. RISC-V Integration Architecture

### 5.1 RISC-V Device Support Matrix

RISC-V has transitioned from academic curiosity to production-viable architecture in 2024-2025, but significant performance gaps remain compared to ARM and x86.

| Board | SoC | Cores | Arch | GB6 Single | GB6 Multi | Power | Price | Status |
|-------|-----|-------|------|------------|-----------|-------|-------|--------|
| **Milk-V Pioneer** | SG2042 | 64x C920 @ 2.0 | RV64GCV | ~40* | ~2,800* | 125W | $1,199 | Build farm ready |
| **SiFive P550** | EIC7700X | 4x P550 @ 1.4 | RV64GC | 136 | 423 | 8-13W | $399 | Best single-core RISC-V |
| **Milk-V Jupiter M1** | SpacemiT M1 | 8x X60 @ 1.8 | RV64GCVB | ~120 | ~500 | 15W | ~$150 | First RVV 1.0 |
| VisionFive 2 | JH7110 | 4x U74 @ 1.5 | RV64GC | ~75 | ~200 | 5W | $70 | Most mature |
| Kendryte K230 | K230 | 2x C908 | RV64GCV | N/A | N/A | 3W | $49 | AI edge only |

*Estimated from SPEC benchmarks. Pioneer: CERN db12 score 378.3 (5.8/core) vs Ampere Altra Max 3,754 (14.66/core) -- roughly 10x slower overall.*

**Key insight:** RISC-V is viable today for edge-tier, semi-trusted compute nodes running lightweight containerized workloads, build farms, and IoT aggregation. It is **not yet suitable** for performance-critical control plane nodes.

### 5.2 Cross-Compilation Strategy

All HelixCluster agent code must cross-compile cleanly for `riscv64gc-unknown-linux-gnu`.

#### Go (Golang)

```go
// Device detection for RISC-V nodes
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "runtime"
    "strings"
)

// CapabilityReport represents what this RISC-V node can do
type CapabilityReport struct {
    Architecture    string            `json:"architecture"`
    CPUFeatures     []string          `json:"cpu_features"`
    VectorExtension string            `json:"vector_extension"` // "none", "rvv0.7", "rvv1.0"
    Cores           int               `json:"cores"`
    RAMBytes        uint64            `json:"ram_bytes"`
    ComputeClass    []string          `json:"compute_class"` // ["cpu"] for most RISC-V
    WorkloadTypes   []string          `json:"workload_types"`
    TrustLevel      string            `json:"trust_level"`
    BenchmarkScore  map[string]float64 `json:"benchmark_score"`
}

func detectRISCVCapabilities() (*CapabilityReport, error) {
    if runtime.GOARCH != "riscv64" {
        return nil, fmt.Errorf("not a RISC-V architecture: %s", runtime.GOARCH)
    }

    report := &CapabilityReport{
        Architecture: "riscv64",
        Cores:        runtime.NumCPU(),
        TrustLevel:   "trusted", // Open ISA enables full audit
    }

    // Read CPU features from /proc/cpuinfo
    cpuinfo, err := os.ReadFile("/proc/cpuinfo")
    if err != nil {
        return nil, err
    }

    for _, line := range strings.Split(string(cpuinfo), "\n") {
        if strings.HasPrefix(line, "isa") {
            isa := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
            report.CPUFeatures = strings.Split(isa, "_")

            // Detect vector extension version
            if strings.Contains(isa, "rv64gv") || strings.Contains(isa, "_v_") {
                if strings.Contains(string(cpuinfo), "spacemit") {
                    report.VectorExtension = "rvv1.0" // SpacemiT K1/M1 = RVV 1.0
                } else if strings.Contains(string(cpuinfo), "sophon") {
                    report.VectorExtension = "rvv0.7" // SG2042 = pre-ratification
                } else {
                    report.VectorExtension = "rvv1.0"
                }
            }
        }
    }

    // Determine compute class based on hardware
    report.ComputeClass = []string{"cpu"}

    // Jupiter/K230 have onboard AI
    if report.VectorExtension == "rvv1.0" {
        report.ComputeClass = append(report.ComputeClass, "rvv_accel")
    }

    // Workload eligibility based on capability
    switch {
    case report.Cores >= 64:
        report.WorkloadTypes = []string{"build_farm", "ci_cd", "lightweight_services"}
    case report.Cores >= 8:
        report.WorkloadTypes = []string{"edge_compute", "iot_aggregation", "lightweight_services"}
    default:
        report.WorkloadTypes = []string{"sensor_gateway", "protocol_bridge", "monitoring"}
    }

    return report, nil
}
```

Native `linux/riscv64` binaries are available from go.dev since Go 1.21. The `GORISCV64` environment variable enables targeting RVA20/22/23 profiles.

#### Rust

The `riscv64gc-unknown-linux-gnu` target is **Tier 2** with active RISE-funded work to reach Tier 1. Cross-compilation with `cross` works well:

```bash
# Cross-compile HelixCluster agent for RISC-V
cross build --target riscv64gc-unknown-linux-gnu --release
```

#### Zig

Excellent native cross-compilation with no additional tooling:

```bash
# Compile for RISC-V directly from any host
zig build -Dtarget=riscv64-linux-gnu -Dcpu=baseline_rv64
```

#### C/C++ (GCC/LLVM)

Mature support since ~2018. Vector extension autovectorization works for RVV 1.0 targets in both compilers.

### 5.3 Performance Expectations

| Workload | RISC-V Board | Performance | vs Raspberry Pi 5 | Notes |
|----------|-------------|-------------|-------------------|-------|
| Native compilation (Rust) | VisionFive 2 | 936s | 12.3x slower | In-order U74 at 1.5 GHz |
| Native compilation (Rust) | Milk-V Pioneer | ~80s | Comparable | 64 cores compensate |
| Native compilation (Rust) | SiFive P550 | ~280s | 3.7x slower | Best single-core RISC-V |
| LLM inference (llama.cpp) | SiFive P550 | ~0.24 tok/s | Very slow | Memory bandwidth limited |
| Container startup | Jupiter M1 | ~3s | 1.5x slower | Docker v29 works well |
| Web server (nginx) | Pioneer | 10K+ req/s | Competitive | Embarrassingly parallel |

**Bottom line:** RISC-V nodes excel at highly parallel, low-single-thread workloads (build farms, web serving, IoT aggregation). They lag significantly for single-threaded or vector-heavy tasks. Deploy in the cluster as **Tier 10 (EXPERIMENTAL)** with limited workload eligibility until performance improves in 2027+.

---

## 6. FPGA Accelerator Architecture

### 6.1 FPGA as Compute Node (Soft-Core)

#### RISC-V Soft-Core Options on FPGA

| Core | LUTs (Artix-7) | Fmax | Coremark/MHz | Linux? | Notes |
|------|---------------|------|--------------|--------|-------|
| VexRiscv (linux) | 2,883 | 180 MHz | 2.27 | **Yes** | Best balance, MMU |
| VexRiscv (max perf) | 1,935 | 200 MHz | 2.57 | No | Dynamic branch prediction |
| NaxRiscv (OoO) | ~13,300 | 155 MHz | 5.02 | Yes | Superscalar |
| CVA6 (Ariane) | ~5,000+ | ~100 MHz | ~3.0+ | Yes | 64-bit application class |
| Rocket Chip | ~5,000+ | ~50-100 MHz | ~2.0+ | Yes | Berkeley reference |

**Multi-core opportunity:** A single ECP5-85K (84K LUTs) can host 4-8 VexRiscv Linux cores at ~2,900 LUTs each. Antmicro demonstrated a **quad-core SMP VexRiscv** on Arty A7 at 100 MHz, booting Linux in ~4 seconds using only 70% of the device.

#### LiteX SoC Builder

LiteX enables constructing complete Linux-capable SoCs from Python:

```python
# LiteX+VexRiscv SoC for HelixCluster FPGA node (conceptual)
from litex_boards.platforms import ulx3s
from litex.soc.integration.builder import Builder
from litex.soc.cores.cpu.vexriscv import VexRiscv

# Build a 4-core VexRiscv SoC with Ethernet
# - CPU: VexRiscv (Linux variant, 4x cores)
# - Memory: 32MB SDRAM (ULX3S) or DDR3
# - Network: LiteEth (1x GbE MAC)
# - Storage: LiteSDCard + SPI Flash
# Result: 4 Linux-capable cores at ~100-180 MHz, GbE networking
```

### 6.2 FPGA as Accelerator (Hard Logic)

#### Hard-Processor FPGAs (Immediate Path)

These devices run standard Linux on hard ARM cores with FPGA fabric for acceleration:

| Board | ARM Cores | FPGA | RAM | GbE | Price | Tier |
|-------|-----------|------|-----|-----|-------|------|
| **DE10-Nano** | 2x A9 @ 800 MHz | 110K LE | 1GB | 1x | $190 | T12 |
| **PYNQ-Z2** | 2x A9 @ 667 MHz | 85K | 512MB | 1x | $129 | T12 |
| **ZUBoard 1CG** | 2x A53 + 2x R5F | 81K | 1GB | 1x | $159 | T12 |
| **KV260** | 4x A53 + 2x R5F | 256K | 4GB | 1x | $249 | T12 |

The **DE10-Nano** is the best value for a Linux-capable FPGA node. The **KV260** is the premier AI-accelerated FPGA with DPU achieving 0.92 TOPS (B3136) at ~7.9W.

#### Open-Source Toolchain Status

| FPGA Family | Synthesis | P&R | Bitstream | Status |
|-------------|-----------|-----|-----------|--------|
| Lattice iCE40 | Yosys | nextpnr | IceStorm | **Mature** |
| Lattice ECP5 | Yosys | nextpnr | prjtrellis | **Mature** |
| Xilinx 7-Series | Yosys | nextpnr-xilinx | prjxray | **Advancing (OpenXC7)** |
| AMD UltraScale+ | -- | -- | -- | Requires Vivado |

The **OpenXC7 breakthrough** (FOSDEM 2025) achieved a fully open-source toolchain for Zynq-7000, enabling BOOT.BIN generation in 5 minutes with no proprietary tools.

### 6.3 Partial Reconfiguration for "FPGA Containers"

Dynamic Partial Reconfiguration (DPR) allows swapping FPGA fabric portions at runtime without disrupting operations. This enables a concept analogous to software containers:

| Concept | Software Container | FPGA Partial Reconfiguration |
|---------|-------------------|------------------------------|
| Image | Docker image (.tar) | Bitstream file (.bit/.bin) |
| Runtime | Container engine | ICAP/PCAP reconfiguration port |
| Isolation | Namespaces, cgroups | Physical spatial separation |
| Orchestration | Kubernetes | Custom scheduler |
| Swap time | Seconds | Milliseconds to seconds |

**Current Status:** DPR is supported on Xilinx 7-Series, UltraScale, and UltraScale+ through Vivado. However, it remains complex: each reconfigurable module must be pre-compiled for specific partition boundaries, and timing closure across boundaries is challenging.

**HelixCluster Recommendation:** For near-term deployment, use **full bitstream swapping** (rebooting the FPGA with a new configuration) rather than DPR. DPR should be evaluated in Phase 5b for dynamic workload switching on Zynq UltraScale+ platforms.


---

## 7. Enterprise & Cloud Node Architecture

### 7.1 Used Server Integration

The used/liquidation server market in 2025 offers extraordinary value for HelixCluster deployment. With 64-core AMD EPYC processors available for under $200 and complete server builds under $1,000, used x86 servers provide unmatched compute density per dollar.

#### AMD EPYC Used Market (2025 Pricing)

| Model | Cores | Arch | Used Price | $/Core | Platform Cost (total) | PassMark |
|-------|-------|------|------------|--------|----------------------|----------|
| EPYC 7551 | 32c/64t | Zen 1 | $65-75 | $2.10 | ~$350 | ~18,000 |
| EPYC 7402 | 24c/48t | Zen 2 | ~$175 | $7.30 | ~$400 | ~35,000 |
| **EPYC 7702** | **64c/128t** | **Zen 2** | **~$500** | **$7.80** | **~$900** | **~71,000** |
| **EPYC 7742** | **64c/128t** | **Zen 2** | **~$500-750** | **$9.80** | **~$1,100** | **~75,000** |
| **EPYC 7713** | **64c/128t** | **Zen 3** | **~$800** | **$14.00** | **~$1,200** | **~81,500** |
| EPYC 7H12 | 64c/128t | Zen 2 | ~$600 | $9.40 | ~$1,000 | ~78,000 |
| EPYC 9654 | 96c/192t | Zen 4 | ~$1,500 | $15.60 | ~$2,200 | ~125,000 |
| EPYC 9754 | 128c/256t | Zen 4 | ~$4,200 | $32.80 | ~$5,000 | ~155,000 |

*Platform cost includes CPU, motherboard (Supermicro H11SSL-i or equivalent), 64GB DDR4 RDIMM ECC, 1TB NVMe, PSU, case.*

**The EPYC 7702/7742 at ~$900-1,100 complete represents the best price/core for modern performance.** With 128x PCIe Gen4 lanes per socket, these systems offer unmatched I/O expansion for GPU hosting, storage, or high-speed networking.

#### ARM Servers

| Model | Cores | Used Price | $/Core | Memory | Notes |
|-------|-------|------------|--------|--------|-------|
| Ampere Altra Q80-30 | 80 | ~$1,500 | $18.75 | 8-ch DDR4, 4TB max | 128x PCIe Gen4 |
| Ampere Altra Max M128 | 128 | ~$2,500 | $19.53 | 8-ch DDR4, 4TB max | Maximum core density |
| AmpereOne A192-32X | 192 | $5,555 | $28.93 | 12-ch DDR5 | Custom cores, 5nm |

**Linux Compatibility:** Excellent for all Ampere platforms. Full upstream Linux since 5.10+. Certified for Ubuntu, RHEL, SUSE, Debian, FreeBSD. Supports LinuxBoot for open-source firmware.

#### Procurement Strategy

```yaml
# enterprise_nodes.yaml -- HelixCluster enterprise node configuration
enterprise_tier:
  name: "CORE_TRUSTED"
  trust_level: "trusted"

  node_types:
    primary_compute:
      target: "Used EPYC 7702/7742 or 7713"
      min_cores: 64
      min_ram_gb: 128
      storage: "1TB NVMe + 4TB SATA backup"
      network: "Dual 1GbE minimum, 10GbE preferred"
      max_power_w: 250
      cost_target: "Under $1,500 per node"
      workload_types:
        - "kubernetes_control_plane"
        - "databases"
        - "ci_cd_runners"
        - "container_hosts"

    high_density:
      target: "Ampere Altra Q80-30 or M128"
      min_cores: 80
      min_ram_gb: 256
      network: "Dual 10GbE"
      max_power_w: 200
      cost_target: "Under $3,000 per node"
      workload_types:
        - "arm_native_containers"
        - "high_density_web_services"
        - "java_microservices"

    gpu_compute:
      target: "EPYC 7702 + A100 40GB or MI210"
      min_cores: 64
      min_ram_gb: 256
      gpu: "NVIDIA A100 40GB or AMD MI210"
      network: "Dual 10GbE or InfiniBand"
      max_power_w: 450
      cost_target: "Under $6,000 per node"
      workload_types:
        - "llm_inference_70b"
        - "model_training"
        - "gpu_batch_jobs"

  security:
    boot: "UEFI Secure Boot + TPM 2.0"
    firmware: "Coreboot or LinuxBoot where possible"
    attestation: "TPM-based node attestation on join"
    isolation: "KVM for tenant separation, containers for internal"
```

### 7.2 Mini PC Fleet

Mini PCs offer compact, efficient cluster nodes ideal for edge deployments where rack space is unavailable.

| Model | CPU | Cores | Max RAM | Networking | Price | Score |
|-------|-----|-------|---------|------------|-------|-------|
| **Minisforum MS-01** | i9-13900H | 14c/20t | 64GB DDR5 | **2x 10GbE SFP+ + 2x 2.5GbE** | ~$679 | **9.5/10** |
| ASUS NUC 14 Pro | Core Ultra 7 165H | 16c/22t | 96GB | 2x 2.5GbE + TB4 | ~$869 | 7/10 |
| Beelink SER9 Pro | Ryzen AI 9 HX 370 | 12c/24t | 64GB LPDDR5X | 1x 2.5GbE | ~$999 | 7.5/10 |
| Mac Mini M4 Pro | M4 Pro (14c) | 14c | 64GB | 1x 10GbE + TB4 | $1,399 | 7/10 |

The **Minisforum MS-01** is the clear winner: dual 10GbE SFP+ provides 20Gbps aggregate network throughput, three M.2 slots support up to 6TB NVMe, and the PCIe x16 (x8 electrical) slot accommodates a half-height GPU like the RTX A2000.

### 7.3 Cloud Spot Instance Workers

Cloud spot instances deliver compute at 60-90% below on-demand pricing. HelixCluster integrates cloud nodes via WireGuard mesh VPN.

#### Spot Pricing (AWS Graviton, typical 2025)

| Instance | vCPUs | RAM | On-Demand | Spot (typical) | $/vCPU/hr |
|----------|-------|-----|-----------|----------------|-----------|
| t4g.nano | 2 | 0.5GB | $0.0042/hr | ~$0.001/hr | $0.0005 |
| c7g.2xlarge | 8 | 16GB | $0.29/hr | ~$0.058/hr | $0.0073 |
| c7g.16xlarge | 64 | 128GB | $2.32/hr | ~$0.46/hr | $0.0072 |
| r7g.2xlarge | 8 | 64GB | $0.456/hr | ~$0.091/hr | $0.011 |

#### Preemption Handling Architecture

```go
// Cloud spot instance lifecycle manager for HelixCluster
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "time"
)

// SpotInstanceManager handles cloud spot node lifecycle
type SpotInstanceManager struct {
    Provider    string // "aws", "azure", "gcp"
    InstanceID  string
    Region      string

    // HelixCluster integration
    ClusterAddr string
    AgentToken  string

    // Checkpoint configuration
    CheckpointInterval time.Duration
    CheckpointBucket   string // S3/GCS/Azure Blob
}

// InterruptionNotice represents a cloud preemption warning
type InterruptionNotice struct {
    Action     string    `json:"action"`     // "stop", "terminate", "hibernate"
    Time       time.Time `json:"time"`       // When the action will occur
    InstanceID string    `json:"instanceId"`
}

// AWS IMDS interruption check (2-minute warning)
func (sim *SpotInstanceManager) CheckAWSInterruption() (*InterruptionNotice, error) {
    // IMDS endpoint for spot interruption notices
    req, _ := http.NewRequest("GET", 
        "http://169.254.169.254/latest/meta-data/spot/instance-action", nil)
    req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")

    // Get IMDSv2 token
    tokenReq, _ := http.NewRequest("PUT",
        "http://169.254.169.254/latest/api/token", nil)
    tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
    tokenResp, err := http.DefaultClient.Do(tokenReq)
    if err != nil {
        return nil, err
    }
    token, _ := io.ReadAll(tokenResp.Body)
    tokenResp.Body.Close()

    req.Header.Set("X-aws-ec2-metadata-token", string(token))
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode == 404 {
        return nil, nil // No interruption notice
    }

    body, _ := io.ReadAll(resp.Body)
    var notice InterruptionNotice
    if err := json.Unmarshal(body, &notice); err != nil {
        return nil, err
    }

    return &notice, nil
}

// HandlePreemption gracefully handles spot interruption
func (sim *SpotInstanceManager) HandlePreemption(ctx context.Context) error {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            var notice *InterruptionNotice
            var err error

            switch sim.Provider {
            case "aws":
                notice, err = sim.CheckAWSInterruption()
            case "azure":
                notice, err = sim.CheckAzureInterruption()
            case "gcp":
                notice, err = sim.CheckGCPInterruption() // 30-second warning
            }

            if err != nil {
                fmt.Printf("Error checking interruption: %v\n", err)
                continue
            }

            if notice != nil {
                fmt.Printf("PREEMPTION WARNING: %s in %s\n", 
                    notice.Action, time.Until(notice.Time))

                // 1. Stop accepting new workloads
                sim.drainNode()

                // 2. Checkpoint running workloads
                sim.checkpointWorkloads()

                // 3. Notify cluster control plane
                sim.notifyClusterDrain()

                // 4. Upload checkpoint state to object storage
                sim.uploadCheckpoints()

                return fmt.Errorf("instance preempted: %s", notice.Action)
            }
        }
    }
}

func (sim *SpotInstanceManager) drainNode() {
    // Signal HelixCluster agent to stop accepting new work
    http.Post(fmt.Sprintf("http://localhost:9090/drain"), 
        "application/json", nil)
}

func (sim *SpotInstanceManager) checkpointWorkloads() {
    // Trigger checkpoint for all running workloads
    // Implementation depends on workload type
}

func (sim *SpotInstanceManager) notifyClusterDrain() {
    // Notify cluster control plane of imminent departure
    http.Post(fmt.Sprintf("%s/nodes/%s/drain", sim.ClusterAddr, sim.InstanceID),
        "application/json", 
        strings.NewReader(`{"reason":"spot_preemption","ttl":"2m"}`))
}

func (sim *SpotInstanceManager) uploadCheckpoints() {
    // Upload checkpoint state to S3/GCS/Azure Blob
    // for recovery on replacement instance
}
```

#### WireGuard Mesh Integration

Cloud nodes connect to on-prem clusters via WireGuard VPN:

```
Architecture:
  On-Prem Cluster        WireGuard Tunnel        Cloud Spot Instances
  [HelixCluster]  <====  (UDP/51820)  ====>  [AWS/GCP/Azure VMs]
  [10.0.1.0/24]         Encrypted mesh          [10.0.2.0/24]
```

WireGuard provides kernel-level implementation with modern cryptography (Curve25519), ~10 line configurations, and native roaming support.

#### TCO Breakeven Analysis

| Scenario | Cloud (Spot) | Owned Hardware | Breakeven |
|----------|-------------|----------------|-----------|
| 64 vCPU, continuous | ~$110-150/mo | ~$80-120/mo (EPYC, amortized) | ~18-24 months |
| 64 vCPU, bursty (200h/mo) | ~$35-50/mo | ~$80-120/mo | Cloud always wins |
| GPU (A100), continuous | ~$600-800/mo | ~$500-700/mo | ~12-18 months |
| GPU (A100), bursty | ~$150-300/mo | ~$500-700/mo | Cloud always wins |

**Rule of thumb:** For steady-state 24/7 workloads, owned hardware breaks even at 18-30 months. For bursty, variable, or experimental workloads, cloud spot is 3-5x more cost-effective. A hybrid approach (on-prem base + cloud burst) optimizes for both.

### 7.4 GPU Compute Nodes

#### GPU Comparison for ML Workloads

| GPU | VRAM | FP16 TFLOPS | Used Price | TFLOPS/$ | Best For |
|-----|------|-------------|------------|----------|----------|
| RTX 4090 | 24GB GDDR6X | 330 | ~$1,200 | 0.275 | Budget inference |
| RTX 5090 | 32GB GDDR7 | 450 | ~$1,800 | 0.250 | Budget inference (newer) |
| A100 40GB | 40GB HBM2e | 312 | ~$5,000 | 0.062 | Production inference |
| A100 80GB | 80GB HBM2e | 312 | ~$8,000 | 0.039 | Large model inference |
| MI210 | 64GB HBM2e | 181 | ~$2,500 | 0.072 | ROCm workloads |
| MI300X | 192GB HBM3 | 163 | ~$12,000 | 0.014 | Maximum VRAM |
| H100 SXM5 | 80GB HBM3 | 989 | ~$8,000 (used) | 0.124 | Training |

**HelixCluster GPU Recommendation:** For CUDA-dependent workloads, a used A100 40GB (~$5,000) offers the best balance of memory, performance, and ecosystem maturity. For ROCm-friendly workloads, the MI210 (~$2,500) offers exceptional value. For budget inference with consumer hardware tolerance, RTX 4090 (~$1,200) is unmatched.

---

## 8. IoT & Smart Device Integration

### 8.1 Router as Cluster Gateway

Routers are among the most promising edge compute devices. They are always-on, connected, and OpenWrt-capable models provide a full Linux environment with package management and Docker support.

#### The GL.iNet GL-MT6000 (Flint 2) -- Best Value Edge Node

| Specification | Value |
|--------------|-------|
| CPU | Quad-core ARM Cortex-A53 @ 2.0 GHz (12nm) |
| RAM | 1GB DDR4 3200MHz |
| Storage | **8GB eMMC 5.1** (remarkable for a router) |
| Network | 2x 2.5GbE (WAN/LAN) + 4x GbE LAN |
| Wi-Fi | Wi-Fi 6 AX6000 4x4:4 dual-band |
| VPN | 900Mbps WireGuard, 190Mbps OpenVPN |
| Power | <20W typical (48W max adapter) |
| Price | **~$159** |

**Docker Support:** The MT6000 runs Docker containers on OpenWrt 24.x:

```bash
# Install Docker on GL-MT6000
opkg update
opkg install dockerd luci-app-dockerman

# Run HelixCluster agent container
docker run -d --name helix-edge   --restart unless-stopped   --network host   -e NODE_TIER=NETWORK_GATEWAY   -e TRUST_LEVEL=edge   helixcluster/agent-edge:latest
```

Users have reported running Nginx Proxy Manager, AdGuard Home, and other containers alongside routing duties. The 8GB eMMC is the critical differentiator -- most routers have 16-256MB flash, making container deployment impossible.

#### Router Comparison for HelixCluster

| Router | CPU | RAM | Storage | 2.5GbE | Docker | Power | Price | Tier |
|--------|-----|-----|---------|--------|--------|-------|-------|------|
| GL.iNet MT6000 | 4x A53 @ 2.0 | 1GB | **8GB eMMC** | 2x | **Yes** | <20W | $159 | T6 |
| GL.iNet MT3000 | 2x A53 @ 1.3 | 512MB | 256MB NAND | 1x | No | ~7W | $89 | T6 |
| ASUS TUF-AX6000 | 4x A53 @ 2.0 | 512MB | 256MB | 2x | No | ~15W | $220 | T6 |
| **NanoPi R6S** | **4xA76+4xA55** | **8GB** | **32GB eMMC** | **2x** | **Yes** | ~5-11W | **$129** | **T6** |
| TP-Link Archer C7 | MIPS 74Kc | 128MB | 16MB | No | No | ~10W | $80 | T8 |

The **NanoPi R6S** deserves special mention: at $129, it provides 8 real CPU cores (A76+A55), 8GB RAM, a 6 TOPS NPU, 32GB eMMC, and dual 2.5GbE -- specifications that far exceed any consumer router. It is a compute-class SBC in router form factor.

### 8.2 NAS as Persistent Storage Node

NAS devices are ideal HelixCluster nodes: always-on, abundant storage, and Docker support.

#### Synology DS923+

| Specification | Value |
|--------------|-------|
| CPU | AMD Ryzen R1600 (dual-core, 4-thread) @ 3.1 GHz |
| RAM | 4GB DDR4 ECC (expandable to **32GB**) |
| Drive Bays | 4x 3.5"/2.5" SATA + 2x M.2 NVMe |
| Network | 2x 1GbE (10GbE expansion available) |
| Docker | Yes (via Container Manager) |
| Power | ~12W hibernation, ~32W access |
| Price | ~$550 + drives |

#### QNAP TS-464

| Specification | Value |
|--------------|-------|
| CPU | Intel Celeron N5095 (quad-core) @ 2.9 GHz burst |
| RAM | 4-8GB DDR4 (upgradable to 16GB) |
| Drive Bays | 4x SATA + 2x M.2 NVMe |
| Network | **2x 2.5GbE** (no add-on needed) |
| PCIe | 1x Gen3 x2 (for 10GbE or Edge TPU) |
| Docker/LXD/Kata | Yes (Container Station) |
| Power | ~22W typical |
| Price | ~$450 + drives |

**Both Synology and QNAP provide full Docker support.** A HelixCluster agent running in a container on a NAS is a Tier 7 (STORAGE_NODE) compute node with built-in persistent storage.

### 8.3 Smart TV as Idle Compute Donor

Modern smart TVs contain multi-core ARM processors, gigabytes of RAM, and dedicated video hardware. When idle, significant CPU cycles are available.

| Platform | CPU | RAM | Background Services | Native Code | Openness | Tier |
|----------|-----|-----|---------------------|-------------|----------|------|
| **LG webOS** | 2-4x ARM | 2-4GB | **JS services on Node.js** | No | **High** | T3 |
| Samsung Tizen | 2-4x ARM | 1.5-3GB | Node.js (Tizen SDK) | No | Medium | T3 |
| Android TV (Shield) | Tegra X1+ 4-core | 3GB | **Full Android services** | Yes (NDK) | High | T3 |
| Chromecast Google TV | 4x A55 @ 1.9 | 2GB | Android Services | Yes (NDK) | Medium | T3 |

**LG webOS is the most promising platform.** Background JS services run persistently on Node.js, the webOS OSE is open source, and CLI tools (`ares-*`) enable full development workflow. A JavaScript-based HelixCluster agent could run as a persistent service, communicating over WebSocket.

**Realistic compute contribution:** Low-intensity tasks only -- data relay, simple aggregation, heartbeat services, cluster coordination. The VPU handles 4K streaming with minimal CPU usage, leaving main cores available during viewing.

### 8.4 Wearable/Smart Speaker Limitations

These devices are **not viable** for HelixCluster compute donation:

| Device | Why Excluded | Exception |
|--------|-------------|-----------|
| Apple Watch | Closed ecosystem, 1-2W thermal limit, 300mAh battery, no background daemons | None |
| Wear OS | Limited background services, 1-2W thermal, battery optimization kills tasks | None |
| Amazon Echo | Completely closed; Skills run in AWS cloud, not on-device | None |
| Google Nest | Fuchsia OS, no developer access for background compute | None |
| Apple HomePod | Fully closed audioOS ecosystem | None |

The only theoretical use case for wearables would be fitness data processing as an "edge sensor," not a compute donor. Smart speakers are useful only as cluster voice interfaces (via cloud Skills that call cluster APIs), not as compute nodes.

---

## 9. Exotic & Future Technology Integration

### 9.1 Groq LPU for LLM Inference

Groq's LPU (Language Processing Unit) architecture delivers the fastest LLM inference in the industry through a single-core, tiled dataflow processor with deterministic execution.

| Metric | Groq LPU | NVIDIA H100 | Advantage |
|--------|----------|-------------|-----------|
| On-chip memory | ~230 MB SRAM | 80GB HBM3 | LPU: 650x bandwidth |
| Memory bandwidth | 150 TB/s/chip | 3.35 TB/s | LPU: **45x** |
| Llama 2 70B throughput | 300-500 tok/s | 30-40 tok/s | LPU: **10-12x** |
| Energy per token | 1-3 joules | 10-30 joules | LPU: **5-10x** |
| Deterministic latency | Yes (<100ms TTFT) | No (200-1000ms) | LPU: consistent |

**Critical Update (December 2025):** NVIDIA signed a ~$20 billion non-exclusive licensing agreement with Groq, acquiring LPU technology IP and hiring CEO Jonathan Ross and ~90% of senior engineers. Groq continues as a nominally independent company.

**Integration Path:** Groq offers GroqCloud (API access, from $0.05/M input tokens) and GroqRack (on-premises). For HelixCluster, LPUs serve as a **dedicated LLM inference tier** -- they cannot train models or handle multimodal workloads, but excel at low-latency, single-stream LLM inference.

```yaml
# Groq LPU inference tier configuration
lpu_inference_tier:
  device_class: "EXOTIC_ACCEL"
  trust_level: "semi_trusted"

  integration_mode: "api"  # or "on_prem" for GroqRack

  groqcloud:
    api_endpoint: "https://api.groq.com/openai/v1"
    models:
      - "llama-3.1-70b"      # 300-500 tok/s
      - "llama-3.1-8b"       # 750+ tok/s
      - "mixtral-8x7b"       # MoE support
    pricing:
      input: "$0.05/M tokens"
      output: "$0.59/M tokens"
      batch_api: "50% discount"

  workload_eligibility:
    - "llm_inference_low_latency"  # Primary use case
    - "chat_completions"
    - "text_generation"

  non_eligible:
    - "model_training"           # LPUs cannot train
    - "multimodal_inference"      # Text only
    - "batch_embedding"          # Use GPU tier instead
```

### 9.2 Quantum as Future Accelerator

Quantum computers cannot directly join a standard compute cluster. They require hybrid classical-quantum programming via cloud APIs.

| Vendor | System | Qubits | Gate Fidelity | Access Model | Cost |
|--------|--------|--------|---------------|--------------|------|
| IBM | Heron r1 | 133 | 99.9% single | Cloud (Qiskit Runtime) | ~$1.60/sec |
| IBM | Heron r2 | 156 | 99.9% single | Cloud | ~$1.60/sec |
| Google | Sycamore | 70 | ~99.5% | Cloud (Cirq) | GCP pricing |
| IonQ | Tempo (AQ 64) | 64 | 99.99% | Cloud + On-prem | Enterprise |
| Quantinuum | H2 | 56 | 99.8% | Cloud | Enterprise |

**HelixCluster Integration Pattern (2029+):**

```go
// Quantum accelerator node (future)
type QuantumAccelerator struct {
    Provider    string // "ibm", "google", "ionq"
    Backend     string // "heron", "sycamore", "tempo"
    Qubits      int
    MaxGateDepth int

    // Classical orchestration
    ClassicalCores int    // Companion CPU cores
    RAMGB          int
}

// Submit quantum circuit as specialized workload
func (qa *QuantumAccelerator) SubmitCircuit(circuit QuantumCircuit) (JobID, error) {
    // 1. Validate circuit against hardware constraints
    // 2. transpile to native gate set
    // 3. submit via provider SDK (Qiskit, Cirq, etc.)
    // 4. return job ID for async result collection
}
```

**Timeline:** IBM targets quantum advantage by 2026 and fault-tolerant systems by 2029 (Starling: 200 logical qubits). Practical HelixCluster integration as a specialized accelerator tier is projected for **2029-2031**.

### 9.3 Neuromorphic / Photonic Watch List

| Technology | Readiness | HelixCluster Fit | Probability by 2027 |
|------------|-----------|------------------|---------------------|
| **Intel Loihi 2** | Research dev kit | Neuromorphic research only | 3% |
| **IBM NorthPole** | Limited production | Edge inference (224MB max model) | 8% |
| **Lightmatter Passage L20** | Sampling late 2026 | Photonic interconnect (not compute) | 5% |
| **Ayar Labs Optical I/O** | Sampling 2025 | Chip-to-chip optical links | 5% |
| **Cerebras CS-3** | **Production** | Large model inference (125 PFLOPS) | **40%** |
| **SambaNova SN40L** | **Production** | CoE inference/training | **45%** |
| **Etched AI Sohu** | Pre-production | Transformer-only ASIC | 10% |
| **IBM z17** | Production | Ultra-secure workloads | 15% |

**Cerebras CS-3** is the most HelixCluster-relevant exotic system already in production. The WSE-3 delivers 125 PFLOPS FP16 with 44GB on-chip SRAM at 21 PB/s bandwidth -- 7,000x an H100's memory bandwidth. Available on-prem ($2-3M) or via cloud API ($0.10/M tokens for Llama 3.1 8B). For HelixCluster, CS-3 integration would be via cloud API as a specialized inference backend.

---

## 10. Universal Integration Layer

### 10.1 Device Discovery Protocol

HelixCluster uses a multi-layered discovery protocol to detect and classify new device types automatically.

```go
// Device discovery and classification engine
package discovery

import (
    "context"
    "encoding/json"
    "fmt"
    "net"
    "os"
    "runtime"
    "strings"
    "time"
)

// DeviceProfile represents a discovered node's complete capability set
type DeviceProfile struct {
    // Identity
    DeviceID      string    `json:"device_id"`
    Hostname      string    `json:"hostname"`
    FirstSeen     time.Time `json:"first_seen"`
    LastSeen      time.Time `json:"last_seen"`

    // Hardware
    Architecture  string    `json:"architecture"`   // "x86_64", "arm64", "riscv64", "ppc64le"
    CPUModel      string    `json:"cpu_model"`
    CPUCores      int       `json:"cpu_cores"`
    CPUMhz        float64   `json:"cpu_mhz"`
    RAMBytes      uint64    `json:"ram_bytes"`

    // Compute capabilities
    ComputeClasses []string `json:"compute_classes"` // "cpu", "gpu", "npu", "fpga", "qpu"

    // GPU details (if applicable)
    GPUs          []GPUInfo `json:"gpus,omitempty"`

    // NPU/AI Accelerator details
    NPUs          []NPUInfo `json:"npus,omitempty"`

    // FPGA details (if applicable)
    FPGA          *FPGAInfo `json:"fpga,omitempty"`

    // Software environment
    OS            string    `json:"os"`
    OSVersion     string    `json:"os_version"`
    KernelVersion string    `json:"kernel_version"`
    ContainerRuntime string `json:"container_runtime"` // "docker", "podman", "none"

    // Network
    NetworkInterfaces []NetInterface `json:"network_interfaces"`

    // HelixCluster classification
    AssignedTier  string    `json:"assigned_tier"`
    TrustLevel    string    `json:"trust_level"`
    WorkloadTypes []string  `json:"workload_types"`

    // Benchmark scores
    Benchmarks    map[string]float64 `json:"benchmarks"`
}

type GPUInfo struct {
    Vendor        string  `json:"vendor"`        // "amd", "nvidia", "intel", "arm"
    Model         string  `json:"model"`
    VRAMBytes     uint64  `json:"vram_bytes"`
    DriverVersion string  `json:"driver_version"`
    ComputeAPIs   []string `json:"compute_apis"` // "vulkan", "cuda", "rocm", "opencl"
    TFLOPSFP32    float64 `json:"tflops_fp32,omitempty"`
}

type NPUInfo struct {
    Vendor      string  `json:"vendor"`       // "nvidia", "rockchip", "qualcomm"
    Model       string  `json:"model"`
    TOPsINT8    float64 `json:"tops_int8"`
    TOPsFP16    float64 `json:"tops_fp16,omitempty"`
    SDKVersion  string  `json:"sdk_version"`
}

type FPGAInfo struct {
    Vendor      string  `json:"vendor"`       // "xilinx", "intel", "lattice"
    Family      string  `json:"family"`       // "zynq7000", "ultrascale+", "ecp5"
    LogicCells  int     `json:"logic_cells"`
    HasHardCPU  bool    `json:"has_hard_cpu"` // ARM hard cores?
    SoftCores   int     `json:"soft_cores"`   // VexRiscv etc.
    BitstreamHash string `json:"bitstream_hash"` // For verification
}

type NetInterface struct {
    Name      string   `json:"name"`
    MAC       string   `json:"mac"`
    IPs       []string `json:"ips"`
    SpeedMbps int      `json:"speed_mbps"`
    IsUp      bool     `json:"is_up"`
}

// DiscoveryEngine performs comprehensive device detection
type DiscoveryEngine struct {
    tierClassifier *TierClassifier
    benchmarker    *Benchmarker
}

// Discover runs the full discovery pipeline
func (de *DiscoveryEngine) Discover(ctx context.Context) (*DeviceProfile, error) {
    profile := &DeviceProfile{
        DeviceID:      generateDeviceID(),
        FirstSeen:     time.Now(),
        LastSeen:      time.Now(),
        Benchmarks:    make(map[string]float64),
        ComputeClasses: []string{"cpu"}, // CPU is always available
    }

    // Step 1: Basic system info
    profile.Hostname, _ = os.Hostname()
    profile.Architecture = runtime.GOARCH
    profile.OS = runtime.GOOS

    // Step 2: CPU detection
    if err := de.detectCPU(profile); err != nil {
        return nil, fmt.Errorf("cpu detection failed: %w", err)
    }

    // Step 3: Memory detection
    if err := de.detectMemory(profile); err != nil {
        return nil, fmt.Errorf("memory detection failed: %w", err)
    }

    // Step 4: GPU detection (architecture-specific)
    if err := de.detectGPU(profile); err != nil {
        // GPU detection failure is non-fatal
        fmt.Printf("GPU detection skipped: %v\n", err)
    }

    // Step 5: NPU/Accelerator detection
    if err := de.detectNPU(profile); err != nil {
        // NPU detection failure is non-fatal
    }

    // Step 6: FPGA detection
    if err := de.detectFPGA(profile); err != nil {
        // FPGA detection failure is non-fatal
    }

    // Step 7: Network detection
    if err := de.detectNetwork(profile); err != nil {
        return nil, fmt.Errorf("network detection failed: %w", err)
    }

    // Step 8: Container runtime detection
    de.detectContainerRuntime(profile)

    // Step 9: Run micro-benchmarks
    if err := de.benchmarker.RunBenchmarks(ctx, profile); err != nil {
        fmt.Printf("Benchmarks failed: %v\n", err)
    }

    // Step 10: Classify into tier
    de.tierClassifier.Classify(profile)

    return profile, nil
}

// detectGPU uses multiple strategies for cross-platform GPU detection
func (de *DiscoveryEngine) detectGPU(profile *DeviceProfile) error {
    // Strategy 1: Vulkan (works everywhere with Mesa)
    if gpus, err := detectVulkanGPUs(); err == nil {
        profile.GPUs = append(profile.GPUs, gpus...)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "gpu")
    }

    // Strategy 2: NVIDIA-specific (CUDA)
    if gpus, err := detectNVIDIAGPUs(); err == nil {
        profile.GPUs = append(profile.GPUs, gpus...)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "gpu")
    }

    // Strategy 3: ROCm (AMD)
    if gpus, err := detectROCmGPUs(); err == nil {
        profile.GPUs = append(profile.GPUs, gpus...)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "gpu")
    }

    // Strategy 4: Android/iGPU (Mali, Adreno)
    if gpus, err := detectMobileGPUs(); err == nil {
        profile.GPUs = append(profile.GPUs, gpus...)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "gpu")
    }

    return nil
}

// detectNPU looks for neural processing units
func (de *DiscoveryEngine) detectNPU(profile *DeviceProfile) error {
    // Strategy 1: Rockchip NPU (RK3588, RK3568)
    if npu, err := detectRockchipNPU(); err == nil {
        profile.NPUs = append(profile.NPUs, *npu)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "npu")
    }

    // Strategy 2: NVIDIA DLA (Jetson)
    if npu, err := detectNVIDIADLA(); err == nil {
        profile.NPUs = append(profile.NPUs, *npu)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "npu")
    }

    // Strategy 3: Qualcomm Hexagon (Snapdragon)
    if npu, err := detectQualcommNPU(); err == nil {
        profile.NPUs = append(profile.NPUs, *npu)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "npu")
    }

    // Strategy 4: Apple Neural Engine
    if npu, err := detectAppleNE(); err == nil {
        profile.NPUs = append(profile.NPUs, *npu)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "npu")
    }

    // Strategy 5: Jetson Orin DLA
    if npu, err := detectJetsonDLA(); err == nil {
        profile.NPUs = append(profile.NPUs, *npu)
        profile.ComputeClasses = appendUniq(profile.ComputeClasses, "npu")
    }

    return nil
}

func appendUniq(slice []string, item string) []string {
    for _, s := range slice {
        if s == item {
            return slice
        }
    }
    return append(slice, item)
}
```

### 10.2 Capability Negotiation

After discovery, devices negotiate their workload eligibility with the cluster control plane.

```yaml
# capability_manifest.yaml -- Generated by discovery engine
device_id: "hc-deadbeef-1234-5678"
generated_at: "2025-07-01T12:00:00Z"

hardware:
  architecture: "arm64"
  cpu_cores: 8
  cpu_model: "Rockchip RK3588"
  ram_bytes: 8589934592  # 8GB

compute_classes:
  - class: "cpu"
    score: 2850.5        # Normalized benchmark score
    eligible_workloads:
      - "containers"
      - "web_services"
      - "lightweight_compute"

  - class: "npu"
    vendor: "rockchip"
    model: "RK3588_NPU"
    tops_int8: 6.0
    sdk_version: "RKNN-Toolkit2 v2.0"
    precision_support: ["INT4", "INT8", "INT16", "FP16"]
    eligible_workloads:
      - "object_detection"
      - "image_classification"
      - "face_detection"
    non_eligible_workloads:
      - "llm_inference_70b"  # Insufficient memory and TOPS

network:
  primary_interface:
    name: "eth0"
    speed_mbps: 2500
    ip: "10.0.1.100"

storage:
  total_bytes: 274877906944  # 256GB NVMe
  type: "nvme"

assigned_tier: "STANDARD"
trust_level: "SEMI_TRUSTED"

workload_constraints:
  max_concurrent_containers: 8
  max_memory_per_workload: 4294967296  # 4GB
  thermal_limit_c: 80
  power_profile: "performance"  # or "balanced", "powersave"

scheduling_hints:
  preferred_workload_types: ["edge_inference", "web_proxy"]
  avoid_workload_types: ["gpu_training", "heavy_compilation"]
  available_hours: "18:00-08:00"  # Overnight compute donation
```

### 10.3 Security Model per Device Class

```yaml
# security_model.yaml -- Per-device-class security configuration
security_policies:
  # Tier 1: Core trusted nodes
  core_trusted:
    trust_level: "trusted"
    requirements:
      - "UEFI Secure Boot or LinuxBoot"
      - "TPM 2.0 attestation"
      - "Encrypted storage (LUKS)"
      - "Signed container images only"
    workload_access: "unrestricted"
    data_access: "sensitive_data_allowed"
    network_policy: "full_mesh"
    sandbox: "none_required"
    applicable_devices:
      - "Used EPYC servers (Coreboot/LinuxBoot)"
      - "OpenPOWER Blackbird"
      - "RISC-V Pioneer (open firmware)"

  # Tier 2-5: Semi-trusted nodes
  semi_trusted:
    trust_level: "semi_trusted"
    requirements:
      - "Signed boot chain (vendor UEFI)"
      - "Container runtime security (seccomp, AppArmor)"
      - "Network policy enforcement"
    workload_access: "containerized_only"
    data_access: "encrypted_at_rest"
    network_policy: "restricted_egress"
    sandbox: "docker_container"
    applicable_devices:
      - "Jetson Orin family"
      - "RK3588 SBCs"
      - "Ampere Altra servers"
      - "Steam Deck (user-controlled)"

  # Tier 6-8: Edge nodes
  edge_untrusted:
    trust_level: "untrusted"
    requirements:
      - "gVisor or Kata Containers"
      - "No access to sensitive data"
      - "Read-only workload volumes"
      - "Network isolation (no direct node-to-node)"
    workload_access: "sandboxed_only"
    data_access: "public_data_only"
    network_policy: "proxy_only"
    sandbox: "gvisor_kata"
    applicable_devices:
      - "OpenWrt routers"
      - "Smart TVs"
      - "Volunteer handhelds"
      - "Cloud spot instances"

  # Tier 14: Research/exotic
  research:
    trust_level: "research"
    requirements:
      - "Complete network isolation"
      - "Manual workload approval"
      - "No persistent cluster state access"
    workload_access: "research_workloads_only"
    data_access: "synthetic_data_only"
    network_policy: "airgapped"
    sandbox: "vm_isolation"
    applicable_devices:
      - "Quantum simulators"
      - "Neuromorphic dev kits"
      - "Experimental RISC-V boards"
```

### 10.4 Agent Deployment Strategy

The deployment strategy varies by device capability:

| Device Type | Deployment Method | Binary Target | Sandbox |
|-------------|------------------|---------------|---------|
| x86 server/desktop | systemd service | `linux/amd64` | None (trusted) |
| ARM SBC (RK3588) | systemd service | `linux/arm64` | None (semi-trusted) |
| Jetson (L4T) | systemd + Docker | `linux/arm64` | Docker container |
| Steam Deck | Flatpak + systemd | `linux/amd64` | Flatpak sandbox |
| OpenWrt router | Docker container | `linux/arm64` | Docker (rootless) |
| NAS (Synology/QNAP) | Docker container | `linux/amd64` | Docker container |
| Smart TV (webOS) | JS service (Node.js) | N/A (JS) | webOS security |
| RISC-V SBC | systemd service | `linux/riscv64` | None (trusted) |
| FPGA (Zynq) | systemd service | `linux/arm` | None (semi-trusted) |
| FPGA (soft-core) | init.d service | `linux/riscv64` | Limited (RAM-constrained) |
| Cloud spot | Kubernetes DaemonSet | multi-arch | Kubernetes pod |

```dockerfile
# Universal HelixCluster agent (multi-arch)
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}     GOARM=${TARGETVARIANT#v}     go build -ldflags="-w -s" -o /helixcluster-agent ./cmd/agent

# Runtime stage (distroless for security)
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /helixcluster-agent /usr/local/bin/
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/helixcluster-agent"]
```

Build for all architectures:

```bash
docker buildx build   --platform linux/amd64,linux/arm64,linux/riscv64   -t helixcluster/agent:latest   --push .
```

---

## 11. Complete Device Comparison

### 11.1 Price/Performance Rankings

#### $ Per CPU Core (General Compute)

| Rank | Device | Cores | Price | $/Core | Notes |
|------|--------|-------|-------|--------|-------|
| 1 | EPYC 7551 (used) | 32 | $75 | $2.34 | Zen 1, older but cheap |
| 2 | Odroid M1S | 4 | $59 | $14.75 | Includes case+PSU |
| 3 | VisionFive 2 | 4 | $70 | $17.50 | RISC-V entry |
| 4 | Odroid M1 | 4 | $70 | $17.50 | SATA+NVMe |
| 5 | EPYC 7702 (used) | 64 | $500 | $7.81 | **Best modern value** |
| 6 | Turing RK1 8GB | 8 | $110 | $13.75 | CM4 module |
| 7 | NanoPi R6S | 8 | $139 | $17.38 | 2.5GbE + NPU |
| 8 | Radxa ROCK 5B 8GB | 8 | $157 | $19.63 | 2.5GbE + NVMe |
| 9 | Ampere Altra Q80 | 80 | $1,500 | $18.75 | ARM server density |
| 10 | Steam Deck (refurb) | 4 | $279 | $69.75 | Includes GPU, display |

#### $ Per TFLOPS (GPU Compute)

| Rank | Device | TFLOPS | Price | $/TFLOPS | Notes |
|------|--------|--------|-------|----------|-------|
| 1 | ROG Ally X | 8.6 | $999 | $116 | Handheld, 24GB RAM |
| 2 | Steam Deck (refurb) | 1.6 | $279 | $174 | Best GPU value entry |
| 3 | Jetson Orin Nano S. | 0.067* | $249 | $3,716 | *TOPS, not TFLOPS; AI-optimized |
| 4 | RTX 4090 (used) | 330 | $1,200 | $3.64 | Best desktop GPU value |
| 5 | A100 40GB (used) | 312 | $5,000 | $16.03 | Production reliability |
| 6 | MI210 (used) | 181 | $2,500 | $13.81 | ROCm ecosystem |

#### $ Per TOPS (NPU/AI Compute)

| Rank | Device | TOPS | Price | $/TOPS | Notes |
|------|--------|------|-------|--------|-------|
| 1 | **Jetson Orin Nano Super** | 67 | $249 | **$3.72** | **Best AI value** |
| 2 | NanoPi R6S | 6 | $139 | $23.17 | + 8 CPU cores |
| 3 | BeagleBone AI-64 | 8 | $185 | $23.13 | Industrial I/O |
| 4 | Jetson AGX Orin 64GB | 275 | $1,599 | $5.81 | Maximum edge AI |
| 5 | Kendryte K230 | 6 | $49 | $8.17 | AI-only, limited |

### 11.2 Power Efficiency Rankings

#### Performance Per Watt

| Device | CPU Perf (GB5) | GPU TFLOPS | NPU TOPS | Power | Perf/Watt Score | Tier |
|--------|----------------|------------|----------|-------|----------------|------|
| Jetson Orin Nano S. | ~4,500 | -- | 67 | 15W | **4.47** | AI champion |
| NanoPi R6S | ~8,500 | 0.5* | 6 | 7W | **2.07** | Edge champion |
| Steam Deck OLED | ~3,800 | 1.6 | -- | 15W | **0.36** | Handheld king |
| EPYC 7713 | ~81,500 | -- | -- | 225W | 0.36 | Server density |
| Ampere Altra M128 | ~75,000 | -- | -- | 183W | **0.41** | ARM server king |
| GL.iNet MT6000 | ~800 | -- | -- | 12W | 0.07 | Router efficiency |
| Mac Studio M3 Ultra | ~32,000 | ~10** | -- | 215W | 0.20 | Apple silicon |

*Mali-G610 MP4 estimated. **Apple GPU TFLOPS estimated.*

### 11.3 Recommended Cluster Configurations

#### Build 1: Ultra-Budget Edge ($250)

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Odroid M1S 8GB | 2 | $59 | $118 |
| GL.iNet MT3000 | 1 | $89 | $89 |
| 128GB microSD cards | 3 | $10 | $30 |
| USB Ethernet adapters | 2 | $5 | $10 |
| **Total** | **3 nodes** | | **$247** |

*Use case:* Entry-level learning cluster. 8 A55 cores, 16GB total RAM, 1.5 GbE aggregate. Runs lightweight containers, DNS, monitoring, IoT aggregation.

#### Build 2: AI Edge Starter ($500)

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6C 8GB | 1 | $125 | $125 |
| Radxa ROCK 5C 4GB | 1 | $75 | $75 |
| 256GB NVMe SSDs | 2 | $20 | $40 |
| 5-port GbE switch | 1 | $10 | $10 |
| **Total** | **3 nodes** | | **$499** |

*Use case:* Entry AI inference cluster. 67 TOPS + 6 TOPS NPU, 20 CPU cores, 20GB RAM. Runs YOLO + LLM 7B inference simultaneously.

#### Build 3: Balanced Home Lab ($1,000)

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| EPYC 7702 + H11SSL-i + 128GB | 1 | $900 | $900 |
| GL.iNet MT6000 | 1 | $159 | $159 |
| Used 1TB NVMe | 1 | $40 | $40 |
| **Total** | **2 nodes** | | **$1,099** |

*Use case:* High-density compute + edge gateway. 64 Zen 2 cores + 128GB ECC RAM + 128x PCIe Gen4 on server. MT6000 handles routing, VPN, and lightweight edge services.

#### Build 4: Advanced ARM Cluster ($2,000)

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| Turing Pi 2.5 + 4x RK1 16GB | 1 | $919 | $919 |
| Jetson Orin Nano Super | 1 | $249 | $249 |
| NanoPi R6S 8GB | 1 | $139 | $139 |
| FriendlyELEC CM3588 NAS 16GB | 1 | $160 | $160 |
| 512GB NVMe SSDs | 6 | $40 | $240 |
| 8-port 2.5GbE switch | 1 | $45 | $45 |
| PSU + cooling | 1 | $100 | $100 |
| SATA SSDs (CM3588) | 4 | $25 | $100 |
| **Total** | **7 nodes** | | **$1,952** |

*Use case:* Full ARM cluster with dedicated roles. 32 RK3588 cores + 6 Orin cores + 30 TOPS NPU aggregate. Mini-ITX density from Turing Pi. NAS provides distributed storage.

#### Build 5: Production Hybrid ($5,000)

| Component | Qty | Unit | Total |
|-----------|-----|------|-------|
| EPYC 7713 server (128GB) | 1 | $1,200 | $1,200 |
| Jetson AGX Orin 64GB | 1 | $1,599 | $1,599 |
| Minisforum MS-01 (i9, 64GB) | 1 | $679 | $679 |
| NVIDIA A100 40GB (used) | 1 | $5,000 | $5,000 |
| 10GbE switch (8-port) | 1 | $200 | $200 |
| **Total** | **4 nodes** | | **$8,678** |

*Use case:* Production-grade heterogeneous cluster. EPYC handles containers/control plane. AGX Orin handles edge AI (275 TOPS). MS-01 is the 10GbE gateway. A100 handles GPU training/inference at scale.

---

## 12. Implementation Roadmap

### 12.1 Phase 5a: Gaming & SBC (Weeks 1-6)

| Week | Milestone | Deliverables | Acceptance Criteria |
|------|-----------|--------------|---------------------|
| W1 | Steam Deck agent prototype | Flatpak package, Vulkan compute backend | Agent runs on SteamOS, detects GPU, reports 1.6 TFLOPS |
| W2 | x86 handheld support | Bazzite compatibility, ROCm override | Agent runs on ROG Ally, Legion Go, GPD Win |
| W3 | RK3588 base support | Armbian packages for ROCK 5B, R6S, R6C | Agent installs via apt, detects 6 TOPS NPU |
| W4 | Jetson Orin integration | L4T packages, TensorRT backend | Agent runs on Orin Nano Super, reports 67 TOPS |
| W5 | Power-aware scheduler | Battery/thermal-aware workload distribution | Handheld nodes throttle compute when unplugged |
| W6 | Phase 5a integration test | 10-node mixed cluster (handheld + SBC) | All nodes report metrics, accept eligible workloads |

**Phase 5a Success Criteria:**
- Steam Deck agent runs in Desktop Mode without interfering with gaming
- RK3588 boards classified as STANDARD tier with NPU workload eligibility
- Jetson Orin Nano Super classified as AI_WORKER with TensorRT backend
- Power-aware scheduler reduces handheld battery drain by >80% during gaming

### 12.2 Phase 5b: RISC-V & FPGA (Weeks 7-12)

| Week | Milestone | Deliverables | Acceptance Criteria |
|------|-----------|--------------|---------------------|
| W7 | RISC-V agent build | Native riscv64 binaries (Go), cross-compile CI | `go build` produces working riscv64 binary |
| W8 | Milk-V Pioneer support | Debian packages, build farm workload type | 64-core Pioneer runs CI builds, reports benchmarks |
| W9 | VisionFive 2 / Jupiter | Armbian packages, RVV 1.0 detection | Jupiter agent detects RVV 1.0, runs edge workloads |
| W10 | FPGA Zynq support | PetaLinux packages for DE10-Nano, PYNQ-Z2 | Agent runs on hard ARM cores, reports FPGA fabric |
| W11 | FPGA DPU integration | KV260 Vitis AI backend, DPU workload type | Agent offloads YOLO inference to DPU, achieves 0.92 TOPS |
| W12 | Phase 5b integration test | 8-node mixed cluster (RISC-V + FPGA + SBC) | Full heterogeneity: arm64 + riscv64 + fpga in one cluster |

**Phase 5b Success Criteria:**
- RISC-V devices classified as EXPERIMENTAL tier with limited workload eligibility
- FPGA nodes with hard ARM cores classified as FPGA_HARD_ACCEL tier
- KV260 DPU inference integrated into AI workload routing
- Full bitstream management system for FPGA reconfiguration

### 12.3 Phase 5c: Enterprise & IoT (Weeks 13-18)

| Week | Milestone | Deliverables | Acceptance Criteria |
|------|-----------|--------------|---------------------|
| W13 | Used EPYC onboarding | Automated provisioning script, Coreboot detection | EPYC 7702 server joins cluster as CORE_TRUSTED in <30 min |
| W14 | Ampere Altra support | ARM64 server packages, 80-core benchmark | Altra Q80-30 reports 80 cores, runs 500+ containers |
| W15 | Cloud spot integration | WireGuard mesh, spot preemption handler | AWS c7g spot nodes join/leave cluster gracefully |
| W16 | Router gateway nodes | OpenWrt opkg, Docker-based agent for MT6000 | MT6000 runs agent alongside routing, <5% CPU overhead |
| W17 | NAS storage nodes | Synology Container Manager + QNAP Container Station | DS923+ and TS-464 agents report storage capacity |
| W18 | Phase 5c integration test | Hybrid cloud-on-prem: 5 on-prem + 5 spot nodes | Spot preemption handled gracefully, workloads checkpointed |

**Phase 5c Success Criteria:**
- Used EPYC servers classified as CORE_TRUSTED with unrestricted workload access
- Ampere Altra classified as SEMI_TRUSTED with ARM-native container routing
- GL-MT6000 classified as NETWORK_GATEWAY with Docker-based deployment
- Cloud spot instances classified as CLOUD_BURST with checkpoint/resume

### 12.4 Phase 5d: Exotic Technology Integration (Weeks 19-24)

| Week | Milestone | Deliverables | Acceptance Criteria |
|------|-----------|--------------|---------------------|
| W19 | Groq LPU API integration | LLM inference routing to GroqCloud | Cluster routes LLM workloads to Groq API, <100ms TTFT |
| W20 | Cerebras API integration | CS-3 cloud API backend for large models | 70B parameter inference via Cerebras Cloud |
| W21 | Quantum research plugin | Qiskit Runtime integration, circuit submission | Cluster node submits Qiskit jobs, receives results async |
| W22 | Smart TV experimental | webOS JS service agent prototype | LG TV runs JS-based agent, communicates via WebSocket |
| W23 | Security hardening | gVisor/Kata for edge nodes, full attestation | All UNTRUSTED nodes run in gVisor, TPM attestation verified |
| W24 | Phase 5 GA | Complete documentation, 60+ device support, benchmarks | All 60+ devices from taxonomy have integration paths |

**Phase 5d Success Criteria:**
- Groq LPU integrated as EXOTIC_ACCEL inference tier
- Cerebras CS-3 available as large-model inference backend
- Quantum computers accessible as research-only accelerator nodes
- Full security model enforced: trusted/semi-trusted/edge/research tiers with appropriate sandboxing

### Final Architecture Overview

```
HelixCluster Phase 5 Complete Topology:

[Cloud Tier]
  ├── AWS Graviton4 spot (c8g) -- CLOUD_BURST
  ├── GroqCloud API -- EXOTIC_ACCEL (inference)
  ├── Cerebras Cloud API -- EXOTIC_ACCEL (large models)
  └── IBM Quantum (Qiskit) -- EXOTIC_ACCEL (research)

[Core Tier - TRUSTED]
  ├── EPYC 7713 server (64c) -- CORE_TRUSTED
  ├── OpenPOWER Blackbird -- CORE_TRUSTED
  └── Milk-V Pioneer (64c RISC-V) -- EXPERIMENTAL

[AI Tier - SEMI_TRUSTED]
  ├── Jetson AGX Orin 64GB (275 TOPS) -- AI_CONTROLLER
  ├── Jetson Orin Nano Super (67 TOPS) -- AI_WORKER
  ├── KV260 FPGA (DPU 0.92 TOPS) -- FPGA_HARD_ACCEL
  └── RK3588 boards (6 TOPS each) -- STANDARD

[Edge Tier - EDGE/UNTRUSTED]
  ├── GL.iNet MT6000 router -- NETWORK_GATEWAY
  ├── NanoPi R6S router (6 TOPS) -- NETWORK_GATEWAY
  ├── Synology DS923+ NAS -- STORAGE_NODE
  ├── QNAP TS-464 NAS -- STORAGE_NODE
  ├── Minisforum MS-01 -- EDGE_COMPUTE
  ├── Steam Deck (volunteer) -- HANDHELD
  ├── ROG Ally X (volunteer) -- HANDHELD
  └── LG webOS TV (experimental) -- EDGE_COMPUTE

[Exotic Tier - RESEARCH]
  ├── IBM Quantum -- EXOTIC_ACCEL (quantum)
  ├── Intel Loihi 2 -- EXOTIC_ACCEL (neuromorphic)
  └── LiteX+VexRiscv FPGA -- FPGA_SOFT_CORE
```

---

## Appendix A: YAML Tier Definitions (Complete)

```yaml
# helixcluster_tiers.yaml -- Complete tier definition for Phase 5
apiVersion: helixcluster.io/v1
kind: TierDefinitions
metadata:
  version: "5.0"
  date: "2025-07-01"

spec:
  tiers:
    - id: T1
      name: CORE_TRUSTED
      trust_level: trusted
      sandbox: none
      workload_access: unrestricted
      min_requirements:
        cpu_cores: 16
        ram_gb: 64
        storage_gb: 500
        network_mbps: 1000
        uptime_pct: 99.9
      compute_classes: [cpu]
      typical_devices:
        - "AMD EPYC 7702/7713/9654"
        - "Ampere Altra Q80-30"
        - "OpenPOWER Blackbird"
        - "Threadripper PRO 5995WX"

    - id: T2
      name: SEMI_TRUSTED
      trust_level: semi_trusted
      sandbox: docker
      workload_access: containerized
      min_requirements:
        cpu_cores: 4
        ram_gb: 4
        storage_gb: 32
        network_mbps: 1000
      compute_classes: [cpu, npu]
      typical_devices:
        - "Radxa ROCK 5B"
        - "Banana Pi BPI-M7"
        - "Khadas VIM4"
        - "Mixtile Blade 3"
        - "Mac Studio M3 Ultra"

    - id: T3
      name: EDGE_COMPUTE
      trust_level: edge
      sandbox: gvisor
      workload_access: sandboxed
      min_requirements:
        cpu_cores: 2
        ram_gb: 2
        storage_gb: 16
        network_mbps: 100
      compute_classes: [cpu]
      typical_devices:
        - "Minisforum MS-01"
        - "ASUS NUC 14 Pro"
        - "Khadas Edge2"
        - "LG webOS TV"
        - "NVIDIA Shield TV Pro"

    - id: T4
      name: AI_WORKER
      trust_level: semi_trusted
      sandbox: docker
      workload_access: ai_workloads
      min_requirements:
        cpu_cores: 4
        ram_gb: 4
        npu_tops: 20
        storage_gb: 64
      compute_classes: [cpu, npu, gpu]
      workload_types:
        - object_detection
        - image_classification
        - llm_inference_7b
        - embedding_generation
      typical_devices:
        - "Jetson Orin Nano Super"
        - "Jetson Orin NX 8GB"
        - "Jetson Orin NX 16GB"

    - id: T5
      name: AI_CONTROLLER
      trust_level: semi_trusted
      sandbox: docker
      workload_access: ai_controller
      min_requirements:
        cpu_cores: 8
        ram_gb: 32
        npu_tops: 100
        storage_gb: 256
      compute_classes: [cpu, npu, gpu]
      workload_types:
        - llm_inference_70b
        - model_serving
        - multi_model_pipeline
      typical_devices:
        - "Jetson AGX Orin 32GB/64GB"
        - "Jetson Thor T5000"

    - id: T6
      name: NETWORK_GATEWAY
      trust_level: edge
      sandbox: docker_rootless
      workload_access: gateway_only
      min_requirements:
        cpu_cores: 2
        ram_gb: 1
        storage_gb: 8
        network_ports_gbe: 2
      compute_classes: [cpu]
      typical_devices:
        - "GL.iNet GL-MT6000"
        - "GL.iNet GL-MT3000"
        - "NanoPi R6S"
        - "ASUS TUF-AX6000"

    - id: T7
      name: STORAGE_NODE
      trust_level: edge
      sandbox: docker
      workload_access: storage
      min_requirements:
        cpu_cores: 2
        ram_gb: 4
        storage_bays: 2
        network_mbps: 1000
      compute_classes: [cpu]
      typical_devices:
        - "Synology DS923+"
        - "QNAP TS-464"
        - "FriendlyELEC CM3588 NAS"
        - "Odroid M1"

    - id: T8
      name: BUDGET
      trust_level: semi_trusted
      sandbox: none
      workload_access: lightweight_only
      min_requirements:
        cpu_cores: 2
        ram_gb: 2
        storage_gb: 8
      compute_classes: [cpu]
      typical_devices:
        - "Odroid M1S"
        - "Odroid N2+"
        - "Pine64 Quartz64"

    - id: T9
      name: HANDHELD
      trust_level: untrusted
      sandbox: flatpak
      workload_access: opportunistic
      min_requirements:
        cpu_cores: 4
        ram_gb: 16
        gpu_tflops: 1.0
      compute_classes: [cpu, gpu]
      scheduling:
        power_aware: true
        battery_threshold_pct: 20
        thermal_limit_c: 85
      typical_devices:
        - "Steam Deck LCD/OLED"
        - "ROG Ally / Ally X"
        - "Lenovo Legion Go"
        - "GPD Win 4"

    - id: T10
      name: RISC_V_EXPERIMENTAL
      trust_level: trusted
      sandbox: none
      workload_access: experimental
      min_requirements:
        cpu_cores: 4
        ram_gb: 4
      compute_classes: [cpu]
      constraints:
        max_workload_duration: 1h
        no_sensitive_data: true
        no_persistent_state: true
      typical_devices:
        - "Milk-V Pioneer"
        - "SiFive P550"
        - "Milk-V Jupiter"
        - "VisionFive 2"
        - "Loongson 3A6000"

    - id: T11
      name: FPGA_SOFT_CORE
      trust_level: trusted
      sandbox: none
      workload_access: fpga_only
      min_requirements:
        fpga_lut_k: 25
        ram_mb: 32
      compute_classes: [fpga]
      constraints:
        bitstream_verification: required
        max_runtime_per_bitstream: 24h
      typical_devices:
        - "Colorlight 5A-75B"
        - "ULX3S"
        - "Alchitry Au"

    - id: T12
      name: FPGA_HARD_ACCEL
      trust_level: semi_trusted
      sandbox: docker
      workload_access: fpga_accelerated
      min_requirements:
        cpu_cores: 2
        ram_gb: 1
        fpga_lut_k: 80
      compute_classes: [cpu, fpga]
      typical_devices:
        - "DE10-Nano"
        - "PYNQ-Z2"
        - "ZUBoard 1CG"
        - "KV260"

    - id: T13
      name: CLOUD_BURST
      trust_level: untrusted
      sandbox: kubernetes_pod
      workload_access: ephemeral
      min_requirements:
        cpu_cores: 2
        ram_gb: 4
      compute_classes: [cpu]
      constraints:
        preemptible: true
        checkpoint_required: true
        max_runtime: 4h
      typical_providers:
        - "AWS Graviton4 spot"
        - "Azure spot"
        - "GCP spot"

    - id: T14
      name: EXOTIC_ACCEL
      trust_level: semi_trusted
      sandbox: api_proxy
      workload_access: specialized
      compute_classes: [npu, qpu]
      constraints:
        manual_approval: true
        api_key_required: true
        workload_specific: true
      typical_devices:
        - "Groq LPU (cloud/on-prem)"
        - "Cerebras CS-3 (cloud API)"
        - "IBM Quantum (cloud API)"
        - "SambaNova SN40L"
        - "Intel Loihi 2"

    - id: T15
      name: LEGACY_RETIRED
      trust_level: untrusted
      sandbox: isolated
      workload_access: none
      compute_classes: []
      status: deprecated
      notes: "Do not deploy new nodes. Existing nodes in this tier should be decommissioned."
      typical_devices:
        - "Jetson Nano 4GB"
        - "Nintendo Switch (original)"
        - "Xbox (all generations)"
        - "ROCKPro64"
        - "Intel Xeon E5 v3/v4"

---

*End of HelixCluster Phase 5 Advanced & Exotic Device Ecosystem Architecture v1.0*

**Document Statistics:**
- Total device types covered: 64
- Total tiers defined: 15
- Cluster build recipes: 5 (at $250, $500, $1,000, $2,000, $5,000+)
- Implementation phases: 4 (24 weeks total)
- Architecture coverage: x86, ARM64, RISC-V, FPGA, POWER, LoongArch, z/Architecture
- Code examples: Go (3), YAML (4), Dockerfile (2)
