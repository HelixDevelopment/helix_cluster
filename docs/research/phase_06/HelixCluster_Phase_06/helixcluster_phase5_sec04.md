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
