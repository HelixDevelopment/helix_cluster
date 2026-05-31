# Phase 5, Dimension 4: FPGA & Programmable Logic Compute for HelixCluster

**Research Date:** July 2025
**Analyst:** Technology Research Team
**Scope:** Field-Programmable Gate Array (FPGA) boards and programmable logic devices as compute nodes or accelerators within the HelixCluster distributed computing framework.

---

## Executive Summary

FPGAs represent a unique and increasingly viable tier in the HelixCluster compute hierarchy. Unlike fixed-architecture CPUs, GPUs, or NPUs, FPGAs offer **reconfigurable hardware** that can be tailored to specific workloads at the gate level. This report evaluates FPGA platforms ranging from $15 Lattice ECP5 boards to $13,000 AMD Versal AI Core dev kits, assesses their viability as cluster nodes, and explores the emerging ecosystem of open-source tooling that makes FPGA development increasingly accessible.

**Key Findings:**
- **Most cost-effective Linux-capable FPGA:** The DE10-Nano ($190-225) with its dual Cortex-A9 hard processor offers the best price-to-capability ratio for running Linux and joining a cluster. The ZUBoard 1CG ($159) is the cheapest Zynq UltraScale+ entry point.
- **Cheapest FPGA that can run Linux (soft-core):** The Colorlight 5A-75B (~$15-25) with a Lattice ECP5 can run a VexRiscv RISC-V soft-core with Linux, though with only 2MB SDRAM it requires external memory expansion for practical use. The ULX3S ($155-250) with 32MB SDRAM is a more practical open-source option.
- **HelixCluster agent viability:** Yes -- FPGAs with hard ARM cores (Zynq, Cyclone V SoC) can run standard Linux and join clusters. Soft-core RISC-V implementations (VexRiscv via LiteX) can also boot Linux, though at ~200 MHz with ~2.57 Coremark/MHz, performance is modest compared to hard processors.
- **FPGA vs Jetson for AI inference:** The Kria KV260 ($249) achieves ~0.92 peak TOPS (B3136 DPU) at ~7.9W, competitive with Jetson Nano in power efficiency but with significantly lower raw TOPS than Jetson Orin Nano (40-67 TOPS). FPGAs excel in latency-sensitive and custom-quantized inference scenarios.
- **Open-source toolchain status (2025):** Mature for Lattice iCE40 and ECP5; rapidly advancing for Xilinx 7-Series via OpenXC7/F4PGA. The OSS CAD Suite provides a one-stop distribution. AMD UltraScale+ and Versal still require proprietary Vivado.
- **Partial reconfiguration:** Commercially available on Xilinx/AMD devices but complex to implement. The concept of "FPGA containers" via bitstream swapping is viable but immature for general cluster deployment.
- **Multiple soft-core opportunity:** A single mid-range FPGA (e.g., ECP5-85K) can host 4-8 VexRiscv cores (~2K LUTs each), enabling many small compute nodes on one chip.

---

## 1. FPGA Hardware Platforms Overview

### 1.1 Xilinx/AMD Zynq-7000 Series (ARM + FPGA SoC)

The Zynq-7000 combines dual-core ARM Cortex-A9 processors (Processing System, PS) with Artix-7 FPGA fabric (Programmable Logic, PL) on a single chip. This makes it a natural cluster node candidate since the hard ARM cores run standard Linux distributions.

| Board | Device | ARM Cores | Logic Cells | RAM | Ethernet | Price |
|-------|--------|-----------|-------------|-----|----------|-------|
| **PYNQ-Z2** | XC7Z020 | 2x A9 @ 667 MHz | 85K | 512 MB DDR3 | 1x GbE | ~$129-199 [^2390^][^2403^] |
| ZedBoard | XC7Z020 | 2x A9 | 85K | 512 MB DDR3 | 1x GbE | ~$589 [^2390^] |
| MicroZed | XC7Z010 | 2x A9 | 28K | 1 GB DDR3 | Via carrier | ~$178 [^2390^] |
| Cora Z7-10 | XC7Z010 | 2x A9 | 28K | 512 MB | 1x GbE | ~$149 [^2390^] |
| Arty Z7-20 | XC7Z020 | 2x A9 | 85K | 512 MB | 1x GbE | ~$299 [^2390^] |
| **EBAZ4205** | XC7Z010 | 2x A9 | 28K | 256 MB | 1x GbE | ~$10-15 [^2407^] |

**HelixCluster Assessment:** The PYNQ-Z2 is the standout value board for educational and prototyping cluster use. The EBAZ4205 (a repurposed mining board) offers incredible value at ~$10 but requires community support for bring-up. All Zynq-7000 boards can run PetaLinux or Ubuntu ARM and join a cluster via Ethernet immediately. The OpenXC7 project now enables a fully open-source toolchain for Zynq-7000 PL development without Vivado [^2404^][^2406^].

### 1.2 Xilinx/AMD Zynq UltraScale+ MPSoC

The next-generation MPSoC upgrades to quad-core Cortex-A53, dual-core Cortex-R5F real-time processors, and significantly more FPGA fabric.

| Board | Device | A53 Cores | Logic Cells | RAM | Features | Price |
|-------|--------|-----------|-------------|-----|----------|-------|
| **ZUBoard 1CG** | ZU1CG | 2x A53 + 2x R5F | 81K | 1 GB LPDDR4 | 1x GbE, SYZYGY | ~$159 [^2226^] |
| **KV260** | XCK26 | 4x A53 + 2x R5F | 256K | 4 GB DDR4 | 1x GbE, HDMI, DP, MIPI | ~$249 [^2264^][^2265^] |
| Ultra96-V2 | ZU3EG | 4x A53 + 2x R5F | 154K | 2 GB LPDDR4 | WiFi, BT, DP | ~$313 [^2226^] |
| PYNQ-ZU | ZU5EG | 4x A53 + 2x R5F | 256K | 4 GB DDR4 | FMC, DP, HDMI | ~$199 [^2226^] |
| ZCU104 | ZU7EV | 4x A53 + 2x R5F | 504K | 2 GB + SODIMM | Video codec, SFP+ | ~$1,899 [^2226^] |
| ZCU102 | ZU9EG | 4x A53 + 2x R5F | 600K | 4.5 GB DDR4 | 4x GbE, PCIe Gen3 x4 | ~$2,995 [^2226^] |

**HelixCluster Assessment:** The KV260 is the premier choice for AI-accelerated cluster nodes. It features a DPU (Deep Learning Processing Unit) IP that delivers up to 0.92 TOPS peak (INT8) with B3136 configuration at ~7.9W [^2357^]. The K26 SOM has a clear production path for volume deployment. The ZUBoard 1CG at $159 is the most accessible UltraScale+ entry point.

### 1.3 AMD Versal AI Edge Series (Next-Gen ACAP)

Versal represents AMD's Adaptive Compute Acceleration Platform, integrating CPU cores, FPGA fabric, and dedicated AI Engines.

| Board | Device | AI Engines | Logic Cells | RAM | Networking | Price |
|-------|--------|------------|-------------|-----|------------|-------|
| **VEK280** | VE2802 | 152 AIE-ML | 899K | 12 GB LPDDR4 | SFP28, 40G MAC | ~$6,995 [^2263^] |
| **VCK190** | VC1902 | 400 AIE | 1.96M | DDR4 SODIMM | QSFP28, FMC+ | ~$13,195 [^2267^] |

**HelixCluster Assessment:** The Versal platform is overkill for typical cluster edge nodes. At $7K-13K, these are specialized AI research/development platforms. The AI Edge series claims 4x AI performance per watt compared to leading GPUs [^2263^], but the ecosystem is still maturing and requires proprietary Vitis tools. Not recommended for general HelixCluster deployment unless specific AI acceleration requirements justify the cost.

### 1.4 Intel/Altera Cyclone V SoC

| Board | Device | ARM Cores | Logic Elements | RAM | Ethernet | Price |
|-------|--------|-----------|----------------|-----|----------|-------|
| **DE10-Nano** | 5CSEBA6U23 | 2x A9 @ 800 MHz | 110K | 1 GB DDR3 | 1x GbE | ~$190-225 [^2244^][^2251^] |
| DE0-Nano-SoC | 5CSEMA4U23 | 2x A9 | 85K | 1 GB DDR3 | 1x GbE | ~$130-170 |

**HelixCluster Assessment:** The DE10-Nano is arguably the best value for a Linux-capable FPGA board. At $190 academic pricing, it provides dual A9 cores, 1GB RAM, GbE, HDMI, and a substantial FPGA fabric. It runs Angstrom, Yocto, or custom Linux and has been widely used in cluster computing research [^2244^]. A strong recommendation for HelixCluster semi-trusted edge nodes.

### 1.5 Lattice ECP5 (Open-Source Champion)

| Board | Device | LUTs | Features | Price |
|-------|--------|------|----------|-------|
| **Colorlight 5A-75B** | ECP5-25K | 25K | 2x GbE, 2MB SDRAM, HUB75 IO | ~$15-25 [^2268^][^2277^] |
| **ULX3S** | ECP5 12K-85K | 12K-84K | WiFi (ESP32), 32MB SDRAM, OLED, USB | ~$155-250 [^2245^][^2255^] |
| Colorlight i5 | ECP5U-25K | 25K | DDR3 SODIMM, JTAG points | ~$50 [^2278^] |

**HelixCluster Assessment:** The Colorlight 5A-75B is the absolute cheapest entry into FPGA computing -- at $15-25 you get a Lattice ECP5 with dual GbE, making it ideal for network-accelerated edge applications [^2268^]. The limitation is only 2MB SDRAM, requiring mods for general-purpose computing. The ULX3S is the most capable open-source FPGA board, fully supported by Yosys/nextpnr/prjtrellis, with enough resources to run a VexRiscv Linux SoC [^2250^]. The onboard ESP32 provides WiFi connectivity.

### 1.6 Alchitry Au/Au+ (Artix-7 Entry)

| Board | Device | Logic Cells | RAM | IO Pins | Price |
|-------|--------|-------------|-----|---------|-------|
| **Alchitry Au** | XC7A35T | 33,280 | 256 MB DDR3 | 102 | ~$115 [^2297^][^2301^] |
| **Alchitry Au+** | XC7A100T | 101,440 | 256 MB DDR3 | 102 | ~$TBD [^2308^] |

**HelixCluster Assessment:** The Alchitry Au is a compact Artix-7 board good for soft-core experimentation. However, it lacks built-in Ethernet, limiting cluster connectivity to USB-serial or add-on modules. The 256MB DDR3 is sufficient for LiteX+VexRiscv Linux systems. Requires Vivado (free WebPACK license). Not ideal as a primary cluster node but useful for accelerator experimentation.

### 1.7 Trenz Electronic Modules (UltraScale+)

| Board | Device | A53 Cores | RAM | Features | Price |
|-------|--------|-----------|-----|----------|-------|
| **TE0802** | ZU2CG | 2x A53 + 2x R5F | 1 GB LPDDR4 | GbE, DP, USB 3.0 | ~$200-300 [^2235^] |
| **TE0808** | ZU9EG | 4x A53 | 4 GB DDR4 | 16x GTH transceivers | ~$800-1200 [^2362^] |

**HelixCluster Assessment:** Trenz modules are industrial-grade SOMs designed for integration. The TE0802 is a compact, capable entry point to UltraScale+. The TE0808 with its 16 GTH transceivers is suited for high-bandwidth cluster backplane applications. German-made with long-term availability guarantees.

---

## 2. Soft-Core CPUs on FPGA: RISC-V for HelixCluster

### 2.1 Key RISC-V Soft-Core Options

| Core | LUTs (Artix-7) | Fmax | Coremark/MHz | Linux? | Notes |
|------|---------------|------|--------------|--------|-------|
| **VexRiscv** (small) | 504 | 243 MHz | -- | No | Tiny, fast, no MMU [^2299^] |
| **VexRiscv** (full) | 1,840 | 199 MHz | 2.30 | Bare metal | With caches, debug [^2299^] |
| **VexRiscv** (linux) | 2,883 | 180 MHz | 2.27 | **Yes** | MMU, 4KB I$/D$ [^2299^] |
| **VexRiscv** (max perf) | 1,935 | 200 MHz | 2.57 | Bare metal | Dynamic branch prediction [^2299^] |
| PicoRV32 | ~1,000 | 170 MHz | ~0.20-0.52 | No | Single-file, small [^2229^] |
| NaxRiscv (OoO) | ~13,300 | 155 MHz | 5.02 | Yes | Superscalar, larger [^2298^] |
| SERV | 125 | >100 MHz | Very low | No | Bit-serial, minimal [^2298^] |
| CVA6 (Ariane) | ~5,000+ | ~100 MHz | ~3.0+ | **Yes** | 64-bit, application class |
| MicroBlaze (Xilinx) | ~1,000 | 212 MHz | 1.04 (DMIPS) | No | Proprietary, optimized [^2228^] |
| Rocket Chip | ~5,000+ | ~50-100 MHz | ~2.0+ | **Yes** | 64-bit, Berkeley reference |

### 2.2 Multi-Core VexRiscv on FPGA

Antmicro demonstrated a **quad-core SMP VexRiscv** system on the Digilent Arty A7 (35T) FPGA, taking only 70% of the device at 100 MHz, booting Linux in ~4 seconds [^2276^]. This achievement established VexRiscv as the first multi-core capable FPGA-optimized RISC-V CPU. Key features implemented:

- Inter-processor communication via IPI
- Cache-coherence mechanisms
- Out-of-order bus access for memory efficiency
- CPU consistency for load/store ordering

**Implication for HelixCluster:** A single mid-range FPGA (e.g., Artix-7 100T or ECP5-85K) could host 4-8 VexRiscv cores, each running a minimal Linux or Zephyr RTOS instance, effectively creating a "many-small-nodes" architecture on a single chip. At ~2,900 LUTs per Linux-capable core, an ECP5-85K (84K LUTs) could theoretically host 20+ cores, though practical limits (memory, interconnect) suggest 4-8 is more realistic.

### 2.3 LiteX: Build Custom SoCs

LiteX is a Python-based SoC builder framework that assembles complete FPGA systems from reusable components [^2266^]. With LiteX, you can construct a Linux-capable SoC including:

- **CPU:** VexRiscv, Rocket, PicoRV32, or LM32
- **Memory:** LiteDRAM controller for DDR2/3/4
- **Network:** LiteEth Ethernet MAC with MII/RMII/RGMII PHY support
- **Storage:** LiteSDCard, LiteSPI for SPI Flash
- **Debug:** LiteScope logic analyzer, JTAG
- **Video:** LiteVideo framebuffer

**Key insight:** LiteX + VexRiscv on an ECP5 FPGA with the open-source toolchain (Yosys/nextpnr/prjtrellis) enables building a **fully open-source SoC** -- from HDL to bitstream to Linux kernel -- with no proprietary tools at any stage [^2274^]. This is revolutionary for trustworthiness and auditability in cluster deployments.

---

## 3. FPGA as Cluster Compute: Practical Integration

### 3.1 Can an FPGA Board Run Linux and Join a Standard Cluster?

**Yes -- in three distinct ways:**

| Mode | Requirements | Boards | Complexity |
|------|-------------|--------|------------|
| **Hard Processor** | Zynq or Cyclone V SoC with ARM cores | DE10-Nano, PYNQ-Z2, KV260, ZUBoard | Low -- standard ARM Linux |
| **Soft-Core + LiteX** | FPGA with 25K+ LUTs, external RAM | ULX3S, Arty A7, Colorlight i5 | Medium -- build SoC |
| **Custom Accelerator** | Any FPGA + separate CPU host | All FPGA boards | High -- design custom logic |

For HelixCluster integration, **hard processor FPGAs** (Zynq, Cyclone V SoC) are the immediate path -- they run standard Linux distributions, support standard networking stacks, and can run cluster agent software without modification.

**Soft-core approaches** require building a custom SoC but offer complete control and the potential for multiple cores per FPGA. The Linux-on-LiteX-VexRiscv project demonstrates booting Linux on various FPGA boards with as little as 32MB RAM [^2274^].

### 3.2 Network Connectivity on FPGA Boards

| Board | Ethernet | Notes |
|-------|----------|-------|
| DE10-Nano | 1x GbE (HPS) | Standard Linux networking |
| PYNQ-Z2 | 1x GbE (PS) | Via Zynq hard MAC |
| KV260 | 1x GbE (PS) | Standard |
| ZUBoard 1CG | 1x GbE | Standard |
| **Colorlight 5A-75B** | **2x GbE (PL)** | Dual Ethernet in FPGA fabric |
| ULX3S | WiFi (ESP32) | Not suitable for cluster backplane |
| Arty A7 | Via Pmod/LiteEth | LiteEth MAC in FPGA fabric |

The **Colorlight 5A-75B's dual GbE** is particularly interesting -- both Ethernet PHYs connect directly to the FPGA fabric, enabling custom network switching, packet processing, or distributed computing topologies implemented directly in hardware [^2268^]. LiteEth (part of LiteX) provides a full open-source Ethernet MAC implementation for FPGA fabric [^2247^].

### 3.3 FPGA Cluster Computing Research

Research has demonstrated FPGA clusters for deep learning with up to 12 Zynq-7020 boards and 5 UltraScale+ boards connected via Ethernet switch, running the VTA (Versatile Tensor Accelerator) framework [^2410^]. Key findings:

- FPGA clusters can execute diverse NN models simultaneously
- Computation graphs can be arranged in pipeline structures
- Resources can be manually allocated to the most compute-intensive layers
- Optimal cluster topology varies with batch size and network architecture [^2409^]

The RIKEN supercomputer center in Japan has explored FPGA cluster "ESSPER" connected to the Fugaku supercomputer, focusing on reconfigurable HPC [^2232^].

---

## 4. FPGA as AI/ML Accelerator

### 4.1 Kria KV260 DPU Performance

The KV260's DPU (DPUCZDX8G) provides hardware-accelerated neural network inference. Benchmarks with YOLOX object detection [^2355^]:

| DPU Arch | Clock | Inference Time | FPS |
|----------|-------|---------------|-----|
| B512 | 150 MHz | 34.8 ms | ~14 FPS |
| B1024 | 150 MHz | 25.5 ms | ~16 FPS |
| B2304 | 150 MHz | 20.7 ms | ~17 FPS |
| B3136 | 150 MHz | 19.5 ms | ~17 FPS |
| B4096 | 150 MHz | 17.0 ms | ~18 FPS |
| B4096 | 300 MHz | 13.7 ms | ~20 FPS |

For ResNet-50 (224x224), benchmarks reach **~140 FPS** with B4096 DPU [^2367^]. The KV260 DPU B3136 achieves **0.92 TOPS peak** at an estimated **7.9W total board power** [^2357^].

### 4.2 FPGA vs Jetson for AI Inference

| Platform | TOPS (INT8) | Power | TOPS/W | Notes |
|----------|-------------|-------|--------|-------|
| KV260 (B3136) | 0.92 | ~7.9W | ~0.12 | DPU-accelerated |
| Jetson Nano | 0.5 | 5-10W | ~0.05 | GPU-based |
| Jetson Orin Nano | 40-67 | 7-25W | ~2.7-9.6 | Ampere GPU + DLA |
| Jetson AGX Orin | 275 | 15-60W | ~4.6-18.3 | Maximum performance |

**Key insight:** Research shows the KV260 consumes approximately **5x less energy** than Jetson Nano for equivalent INT8 quantized inference workloads [^2392^]. However, raw TOPS comparisons favor Jetson Orin by a large margin. Where FPGAs excel is in:

- **Custom quantization:** TerEffic demonstrated 467 tokens/sec/W on LLM inference, 19x better than Jetson Orin Nano, using ternary quantization [^2393^]
- **Deterministic latency:** FPGA inference has consistent, predictable timing
- **Power efficiency at low batch sizes:** FPGAs don't suffer from GPU underutilization
- **Custom operators:** Any operation can be implemented in hardware fabric

### 4.3 Workloads Uniquely Suited to FPGA Acceleration

| Workload | Why FPGA Excels | Example Implementation |
|----------|----------------|----------------------|
| **Cryptography** | Bit-level operations, custom pipelines | AES/SHA accelerators in fabric |
| **Signal Processing** | Deterministic latency, streaming data | FIR filters, FFT, SDR |
| **Custom ML inference** | Quantization flexibility, operator fusion | DPU, custom CNN engines |
| **Network packet processing** | Line-rate processing, custom protocols | Open vSwitch, custom NICs |
| **Financial computing** | Deterministic latency, ultra-low jitter | Monte Carlo, option pricing |
| **Industrial control** | Real-time determinism, safety | Motor control, PLCs |
| **Video processing** | Parallel pixel pipelines, custom codecs | H.264 encode/decode (EV devices) |

---

## 5. Open-Source FPGA Toolchain: 2025 Status

### 5.1 Fully Open-Source Flow (No Proprietary Tools)

| FPGA Family | Synthesis | Place & Route | Bitstream | Status |
|-------------|-----------|---------------|-----------|--------|
| **Lattice iCE40** | Yosys | nextpnr | IceStorm | **Mature, complete** [^2300^] |
| **Lattice ECP5** | Yosys | nextpnr | prjtrellis | **Mature, complete** [^2252^] |
| **Lattice Nexus** | Yosys | nextpnr | Project Oxide | Good progress |
| **Gowin GW1N** | Yosys | nextpnr | Project Apicula | Active development |
| **Xilinx 7-Series** | Yosys | nextpnr-xilinx | prjxray | **Advancing rapidly (OpenXC7)** [^2402^][^2403^] |
| Cologne Chip GateMate | Yosys | nextpnr | Project Peppercorn | Early support |

### 5.2 Key Tools

| Tool | Function | License |
|------|----------|---------|
| **Yosys** | RTL synthesis (Verilog 2005) | ISC [^2300^] |
| **nextpnr** | Timing-driven place & route | ISC |
| **GHDL** | VHDL simulation/synthesis | GPL-2.0+ |
| **LiteX** | SoC builder framework | BSD |
| **VexRiscv** | RISC-V CPU generator | MIT |
| **OSS CAD Suite** | One-stop distribution | Various [^2303^] |
| **OpenXC7** | Xilinx 7-series toolchain installer | BSD-3 |

### 5.3 What Still Requires Proprietary Tools

- **AMD UltraScale+ (Zynq MPSoC):** Still requires Vivado for PL design
- **AMD Versal:** Requires Vitis + Vivado
- **Intel Agilex/Stratix:** Requires Quartus Prime
- **Timing closure on complex designs:** Vendor tools still superior for max-performance designs
- **Hard IP blocks:** PCIe Gen3/4, GTH/GTY transceivers, HBM controllers require vendor tools

### 5.4 The OpenXC7 Breakthrough

The OpenXC7 project (actively developed as of 2025) has achieved a milestone: a **fully open-source toolchain for Xilinx 7-Series including Zynq-7000** [^2402^][^2403^]. At FOSDEM 2025, a demonstration showed building a complete BOOT.BIN for Zynq in 5 minutes using OpenXC7 + GenZ (an open-source BSP generator) [^2406^][^2407^]. This enables:

- Zynq development on ARM laptops (Apple Silicon, Raspberry Pi)
- Docker-based FPGA build pipelines
- No vendor license required, ever

---

## 6. Partial Reconfiguration: "FPGA Containers"

### 6.1 Technology Overview

Dynamic Partial Reconfiguration (DPR) allows swapping out portions of the FPGA fabric at runtime without disrupting other operations. A static region manages the reconfiguration interface while dynamic "reconfigurable partitions" can be hot-swapped [^2231^].

### 6.2 Container Analogy

| Concept | Software Container | FPGA Partial Reconfiguration |
|---------|-------------------|------------------------------|
| **Image** | Docker image (.tar) | Bitstream file (.bit/.bin) |
| **Runtime** | Container engine | ICAP/PCAP reconfiguration port |
| **Isolation** | Namespaces, cgroups | Physical spatial separation |
| **Orchestration** | Kubernetes | Custom scheduler |
| **Swap time** | Seconds | Milliseconds to seconds |

### 6.3 Practical Status

Partial reconfiguration is supported on Xilinx 7-Series, UltraScale, and UltraScale+ devices through Vivado [^2231^]. Research has explored:

- **DPR for CNN accelerators:** Dynamically swapping convolution engines based on layer requirements [^2231^]
- **Overlay architectures:** Coarse-grained overlays for rapid just-in-time accelerator compilation
- **PYNQ + DPR:** Dynamic overlays in the Python FPGA framework

**HelixCluster Assessment:** While the concept of "FPGA containers" via partial reconfiguration is compelling, it remains complex to implement in practice. Each reconfigurable module must be pre-compiled for specific partition boundaries, and timing closure across reconfigurable boundaries is challenging. For HelixCluster, a simpler approach of full bitstream swapping (rebooting the FPGA with a new configuration) is more practical in the near term.

---

## 7. Power Consumption and Thermal Characteristics

| Board/Platform | Typical Power | Max Power | Cooling |
|---------------|--------------|-----------|---------|
| Colorlight 5A-75B | ~1-3W | ~5W | Passive |
| ULX3S | ~0.5-2W | ~5W | Passive |
| DE10-Nano | ~3-5W | ~10W | Passive |
| PYNQ-Z2 | ~3-5W | ~10W | Passive |
| KV260 (idle) | ~5W | ~15W | Active fan |
| KV260 (DPU load) | ~8W | ~20W | Active fan |
| ZCU102 | ~15-25W | ~40W | Active fan |
| VEK280 | ~20-40W | ~60W | Active cooling |

FPGA power consumption scales directly with logic utilization and clock frequency. For cluster deployments, this provides an advantage over GPUs: FPGAs consume only the power needed for the active design, while GPUs have a higher baseline power draw even when underutilized [^2392^][^2393^].

---

## 8. HelixCluster Integration Strategy

### 8.1 Recommended FPGA Tiers

| Tier | Role | Recommended Boards | Workloads |
|------|------|-------------------|-----------|
| **Edge (Semi-Trusted)** | Sensor/gateway nodes | Colorlight 5A-75B, ULX3S | Packet processing, signal acquisition, protocol conversion |
| **Worker (Trusted)** | General compute | DE10-Nano, PYNQ-Z2, ZUBoard 1CG | Embedded Linux tasks, lightweight inference, crypto |
| **AI Accelerator** | ML inference | KV260, Ultra96-V2 | Vision AI, DPU-accelerated inference, video analytics |
| **HPC (Specialized)** | Research/development | ZCU102, TE0808 | Custom accelerator research, high-bandwidth applications |

### 8.2 Cluster Agent Feasibility

**Hard Processor FPGAs (Zynq, Cyclone V SoC):** Can run standard ARM Linux and HelixCluster agents without modification. GbE networking is native. These are the easiest integration path.

**Soft-Core RISC-V FPGAs (LiteX+VexRiscv):** Require a custom Linux build (via Linux-on-LiteX-VexRiscv) and a RISC-V-compiled agent binary. Performance is modest (~200 MHz, ~2.57 Coremark/MHz) but sufficient for lightweight agent tasks. The main challenge is limited RAM (typically 32-64MB on affordable boards).

**Pure FPGA Accelerators:** A separate CPU host runs the agent; the FPGA acts as a co-processor. This is the most complex integration but offers maximum performance for custom workloads.

### 8.3 Security Model

| Security Level | Applicability | Notes |
|---------------|--------------|-------|
| **Trusted** | DE10-Nano, PYNQ-Z2, KV260 with verified boot | Secure boot available on Zynq US+ |
| **Semi-Trusted** | Open-source FPGA boards (ULX3S, Colorlight) | Full hardware auditability with open toolchains |
| **Edge/Untrusted** | Repurposed/second-hand boards (EBAZ4205) | Cost-optimized, may lack security features |

The open-source toolchain (Yosys/nextpnr/OpenXC7) provides an **unprecedented trust advantage**: every gate in the design is auditable, and the entire tool chain can be verified, eliminating supply-chain risks from proprietary synthesis tools [^2400^].

---

## 9. Cost Comparison: Building an FPGA Cluster

### 9.1 Budget Cluster Options

| Configuration | Nodes | Total Cost | Total Power | Notes |
|-------------|-------|-----------|-------------|-------|
| 4x Colorlight 5A-75B + switch | 4 FPGA nodes | ~$100 | ~15W | Custom soft-cores, minimal RAM |
| 4x DE10-Nano | 4 ARM+FPGA nodes | ~$760 | ~20-40W | Full Linux, GbE each |
| 4x PYNQ-Z2 | 4 ARM+FPGA nodes | ~$520-800 | ~20-40W | Python prototyping capable |
| 4x ZUBoard 1CG | 4 A53+FPGA nodes | ~$636 | ~20-40W | Modern UltraScale+ |
| 2x KV260 | 2 AI-accelerated nodes | ~$498 | ~16-30W | DPU for vision AI |
| 1x Jetson Orin Nano | 1 GPU node | ~$249-499 | 7-25W | Much higher TOPS, less flexibility |

### 9.2 Price-Performance Analysis

For HelixCluster's distributed computing model, FPGAs offer:
- **Unique workload specialization** not possible on fixed architectures
- **Deterministic latency** for real-time applications
- **Superior power efficiency** for specific quantized inference tasks
- **Hardware auditability** via open-source toolchains
- **Lower per-node cost** for basic compute (with EBAZ4205 or Colorlight)

However, for general-purpose AI inference, Jetson Orin Nano currently offers dramatically higher TOPS per dollar. The FPGA advantage is in custom, latency-sensitive, or specialized precision workloads.

---

## 10. Limitations and Gotchas

1. **Toolchain complexity:** Even with open-source tools, FPGA development requires HDL knowledge (Verilog/VHDL) or high-level tools (Vitis HLS, Amaranth). This is a steeper learning curve than embedded Linux or CUDA.

2. **Compilation time:** Synthesis and place-and-route can take minutes to hours depending on design complexity. This is fundamentally different from software compilation and impacts CI/CD pipelines.

3. **Memory bandwidth:** Many affordable FPGA boards have limited external RAM (2MB on Colorlight, 256MB on DE10-Nano). This constrains the size of models and datasets that can be processed.

4. **Vendor lock-in for high-end:** UltraScale+ and Versal devices still require Vivado/Vitis, which are multi-hundred-gigabyte installations requiring substantial host resources [^2226^].

5. **Bitstream storage:** Each FPGA configuration requires its own bitstream file. Managing bitstreams across a cluster adds operational complexity compared to software deployment.

6. **Debugging:** FPGA debugging requires JTAG access and specialized tools (OpenOCD, ILA cores). Remote debugging of FPGA clusters is more complex than software-only nodes.

7. **I/O limitations:** Many affordable FPGA boards (Alchitry Au, ULX3S) lack built-in Ethernet, requiring add-on modules or alternative connectivity for cluster integration.

8. **Partial reconfiguration complexity:** While "FPGA containers" are conceptually appealing, DPR requires careful floorplanning and is not yet a "push-button" technology for dynamic workload scheduling.

---

## 11. Recommendations

### Immediate Actions (Phase 1)
1. **Procure 2-3 DE10-Nano boards** ($190 academic each) as the baseline FPGA cluster testbed -- hard ARM cores, GbE, proven Linux support.
2. **Procure 1-2 KV260 starter kits** ($249 each) to evaluate DPU-accelerated AI inference in the cluster context.
3. **Experiment with LiteX+VexRiscv** on ULX3S or Colorlight i5 to assess soft-core viability for lightweight agent nodes.

### Medium-Term (Phase 2)
4. **Develop FPGA accelerator IP** for HelixCluster-specific workloads (e.g., cryptographic hashing, packet filtering, custom inference engines).
5. **Evaluate partial reconfiguration** for dynamic workload switching on Zynq UltraScale+ platforms.
6. **Build a mixed cluster** with FPGA nodes alongside SBC and Jetson nodes, using HelixCluster's tier system to route appropriate workloads.

### Long-Term (Phase 3)
7. **Contribute to openXC7** to improve Zynq-7000 support for fully open-source cluster nodes.
8. **Investigate custom RISC-V extensions** in VexRiscv for HelixCluster-specific operations (e.g., distributed consensus acceleration, cryptographic primitives).

---

## Raw Evidence Log

### Official Documentation & Specs
- [^2226^] Zynq UltraScale+ Board Comparison (pcbsync.com): Comprehensive board comparison including ZUBoard 1CG, Ultra96-V2, KV260, ZCU102/104/106
- [^2244^] DE10-Nano vs DE0-Nano specs and pricing comparison
- [^2251^] Terasic official DE10-Nano product page: $225 retail, $190 academic
- [^2263^] AMD Versal AI Edge/AI Core series overview including VEK280 ($6,995) and VCK190 ($13,195)
- [^2265^] AMD official KV260 product page: $249 MSRP, full specifications
- [^2267^] AMD official VCK190 product page: $13,195
- [^2297^] Alchitry Au V2 official product page: $114.99, XC7A35T
- [^2308^] Alchitry Au+ official page: XC7A100T, 101,440 logic cells
- [^2362^] Trenz TE0808 module specs: ZU9EG, 4GB DDR4, 16x GTH transceivers

### RISC-V Soft-Cores & LiteX
- [^2299^] VexRiscv GitHub: Detailed area/frequency benchmarks across Artix-7, Cyclone V, Cyclone IV, iCE40
- [^2276^] Antmicro quad-core VexRiscv SMP on Arty A7: 70% of 35T FPGA, 100 MHz, Linux in 4 seconds
- [^2266^] LiteX PyPI page: SoC builder framework overview and supported cores
- [^2274^] Linux-on-LiteX-VexRiscv GitHub wiki: Running Linux on minimal FPGA resources
- [^2228^] NSF SHREC paper: Fault injection comparison of RISC-V soft-cores (PicoRV32, VexRiscv, Taiga, Kronos)
- [^2229^] FPGA Softcore SoC shootout: Practical comparison of VexRiscv and PicoRV32
- [^2302^] TU Darmstadt RISC-V catalog: In-hardware evaluation of open-source soft-cores

### Open-Source Toolchain
- [^2300^] YosysHQ official open-source tools page: Tool descriptions and capabilities
- [^2303^] OSS CAD Suite GitHub: Actively maintained open-source FPGA toolchain distribution
- [^2252^] Project Trellis (prjtrellis): Open-source ECP5 bitstream tools
- [^2402^] OpenXC7 GitHub organization: Active development of Xilinx 7-series open toolchain
- [^2404^] FOSDEM 2025 session: All Open Source Toolchain for ZYNQ 7000 SoCs
- [^2406^] FOSDEM 2025 event details: OpenXC7 + GenZ demonstration
- [^2398^] Antmicro F4PGA blog: Open-source flow for Xilinx 7-Series
- [^2395^] F4PGA (formerly SymbiFlow) official website

### AI/ML Performance & Benchmarks
- [^2355^] Hackster.io DPU benchmark on KR260: YOLOX inference across B512-B4096 DPU configurations
- [^2357^] AMD official KV260 docs: DPU B3136 performance (0.92 TOPS peak, 7.9W)
- [^2367^] Element14 Kria benchmark blog: ResNet-50 at ~140 FPS
- [^2392^] ResearchGate power consumption benchmark: KV260 5x more energy efficient than Jetson Nano
- [^2393^] arXiv TerEffic paper: 467 tokens/sec/W on FPGA, 19x better than Jetson Orin Nano
- [^2391^] Raspberry Pi vs Jetson comparison: TOPS and power data for 2025

### FPGA Cluster Computing
- [^2410^] arXiv: Reconfigurable Distributed FPGA Cluster for DL (12 Zynq-7020 + 5 UltraScale+)
- [^2409^] UT Austin: Improving CNN Performance on FPGA Clusters
- [^2232^] RIKEN ESSPER FPGA cluster connected to Fugaku supercomputer
- [^2231^] Springer: Dynamic and Partial Reconfiguration of FPGAs survey

### Affordable/Open Hardware
- [^2268^] YosysHQ blog: Colorlight 5A-75B as $15-25 ECP5 dev board with dual GbE
- [^2277^] Hackaday: LED driver as FPGA dev board in disguise
- [^2250^] ULX3S official page: Open-source ECP5 board comparison chart
- [^2245^] Hackster.io: ULX3S overview and features
- [^2407^] FOSDEM 2025 slides: Zynq-as-MCU with upcycled boards (EBAZ4205 ~EUR10)

### General FPGA Resources
- [^2246^] Comprehensive FPGA development board guide: Price ranges and categories
- [^2254^] IJACT: Comparative study of FPGA and GPU -- energy efficiency analysis
- [^2358^] Fidus: FPGA role in AI acceleration -- TCO and security advantages
- [^2396^] EEVBlog forum: Open source tools and FPGA in 2026 discussion
- [^2400^] Red Hat Research: RISC-V for FPGAs -- benefits and opportunities
