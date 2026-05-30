# Chapter 2: Console Hardware Deep Dive

## 2.1 The Jailbreak Ecosystem

### GoldHen (PS4)

GoldHen is the primary homebrew enabler for PS4 consoles. It is a kernel-level payload that patches the PlayStation 4 operating system to enable:

- **Homebrew execution** — Run unsigned code
- **Debug settings** — Full system debugging access
- **FTP server** — Remote file transfer (port 2121)
- **BinLoader** — Load additional payloads (port 9090)
- **Plugin system** — Persistent plugins across sleeps
- **Remote Package Installer** — Install packages over network
- **Rest Mode support** — Jailbreak survives sleep/REST mode

GoldHen supports firmwares 5.05 through 9.60, with the **9.00 firmware** being the sweet spot for stability and homebrew compatibility.

### etaHEN / UMTX2 (PS5)

For PS5, the jailbreak landscape is evolving rapidly:

- **UMTX2 exploit** (2024) — Full kernel exploit up to firmware 7.61
- **etaHEN** — Homebrew enabler supporting firmwares up to 10.01
- **Userland Lua exploit** — Up to firmware 10.40 on PS5 Pro (no kernel)
- **kstuff payload** — Enables PS4 FPKG backward compatibility

The **PS5 Pro security co-processor** (ARM Cortex-A53) independently handles PKG authentication, making native PS5 FPKG installation impossible even with full kernel access. However, this does NOT affect Linux/homebrew compute.

### Auto-Exploit Hardware

The primary operational challenge with console nodes is the **semi-tethered jailbreak** — a cold boot loses the jailbreak and requires re-exploitation. Two hardware solutions automate this:

1. **ESP32-based auto-exploit** (~$5) — Programmable microcontroller that sends the USB exploit payload automatically when the console boots
2. **Luckfox MCU** (~$8) — Similar approach with more sophisticated timing control

These devices connect to the PS4/PS5 USB port and act as a "jailbreak dongle" — the console boots, the MCU detects power-on, and automatically sends the exploit sequence. Combined with REST mode persistence (which preserves the jailbreak across days of sleep), the console is effectively always jailbroken.

## 2.2 PS4 Base (CUH-10xx/11xx) — Tier 3

### System-on-Chip: AMD Liverpool

The PS4 uses a semi-custom AMD APU codenamed "Liverpool," built on a 28nm process. It combines:

- **CPU**: 8-core AMD Jaguar x86-64 at 1.6 GHz (up to 1.75 GHz boost)
- **GPU**: 18 Compute Units of AMD GCN 2.0 architecture
- **Memory**: 8 GB GDDR5 unified memory at 176 GB/s bandwidth
- **Northbridge**: Custom AMD with cache coherency

### CPU Deep Dive

The Jaguar CPU is a low-power x86-64 microarchitecture originally designed for tablets and mobile devices. Eight cores are arranged in two quad-core clusters, each with:

- 32KB L1 instruction cache + 32KB L1 data cache per core
- 2MB L2 cache per quad-core cluster (shared)
- Support for SSE4.1, SSE4.2, AVX, AES-NI
- **NO AVX2** (significant limitation for some optimized code)
- Single-precision floating point throughput: ~115 GFLOPS aggregate

**Performance context**: Geekbench 4 single-core ~1400, multi-core ~7600. Approximately 5-6x slower than a modern Ryzen 5 desktop CPU. Comparable to an AMD Athlon 5370.

### GPU Deep Dive

The Liverpool GPU features:

- 18 Compute Units (CUs) = 1152 streaming processors
- AMD GCN 2.0 architecture (Sea Islands family)
- 1.84 TFLOPS single-precision compute
- 800 MHz base clock, 911 MHz boost
- 32 ROPs, 72 texture units
- Full hardware video encode/decode (H.264, AVC)
- 8 GB GDDR5 shared with CPU at 176 GB/s

### Linux on PS4 Base

Kernel support is excellent:
- Linux 6.15.4 (latest) with full PS4 patches
- AMDGPU kernel driver for GPU acceleration
- Mesa 24.x with RADV (Radeon Vulkan) driver
- Full Gigabit Ethernet support (Realtek RTL8153)
- USB 3.0/2.0 full support
- SATA SSD upgrade supported

## 2.3 PS4 Pro (CUH-70xx) — Tier 2 (RECOMMENDED)

### System-on-Chip: AMD Neo

The PS4 Pro uses an enhanced semi-custom APU codenamed "Neo":

- **CPU**: 8-core AMD Jaguar x86-64 at 2.13 GHz (overclockable to ~2.6 GHz)
- **GPU**: 36 Compute Units of AMD GCN 4.0 (Polaris) architecture
- **Memory**: 8 GB GDDR5 (218 GB/s) + 1 GB DDR3 for OS
- **Southbridge**: Belize (faster than base PS4's Bora)

### CPU Improvements

- 33% higher clock (2.13 GHz vs 1.6 GHz)
- Overclocking to 2.6 GHz is stable with good cooling
- Same Jaguar architecture — still no AVX2
- AES throughput: ~6.93 GB/s aggregate (vs ~5 GB/s on base)

### GPU Improvements

- **36 CUs** (vs 18 on base) — exactly double
- **GCN 4.0 Polaris** architecture (vs GCN 2.0)
- **4.20 TFLOPS** (vs 1.84) — 2.3x improvement
- 911 MHz clock
- Improved tessellation, delta color compression
- Hardware HEVC (H.265) decode support

### Key Advantages for Cluster Use

1. **Best cost/TFLOP ratio** at ~$59/TFLOP
2. **KVM virtualization confirmed working** (unique among consoles)
3. **WiFi AC** for wireless cluster deployment
4. **USB 3.1 Gen1** x3 ports for storage expansion
5. **Overclockable CPU** to 2.6 GHz for extra performance

## 2.4 PS5 (CFI-10xx/11xx) — Tier 1 (PREMIUM)

### System-on-Chip: AMD Oberon

The PS5 represents a generational leap — a true desktop-class SoC:

- **CPU**: 8-core/16-thread AMD Zen 2 at up to 3.5 GHz
- **GPU**: 36 CUs of AMD RDNA 2 at up to 2.23 GHz
- **Memory**: 16 GB GDDR6 at 448 GB/s
- **Custom I/O Complex**: Hardware decompression, DMA engine
- **Process**: TSMC 7nm

### CPU: Desktop-Class Zen 2

The PS5's CPU is a custom Zen 2 design:
- **8 cores, 16 threads** — same as Ryzen 7 3700X
- **Variable frequency** up to 3.5 GHz (lower than desktop's 4.4 GHz)
- **35% smaller FPU** than desktop Zen 2 — slightly reduced FP performance
- Full AVX2 support (unlike PS4's Jaguar)
- 4MB L3 cache per CCX (8MB total)
- Estimated aggregate FP32: ~450 GFLOPS

**Key limitation**: The 35% smaller FPU means floating-point throughput is reduced compared to an equivalent desktop Ryzen. However, integer performance and general-purpose computing are unaffected.

### GPU: RDNA 2 with Ray Tracing

- 36 Compute Units of AMD RDNA 2
- 10.28 TFLOPS single-precision
- Hardware ray tracing acceleration
- Variable frequency up to 2.23 GHz
- Mesh shaders, variable rate shading support
- 16 GB GDDR6 shared with CPU

### Custom I/O Complex (Unique Advantage)

The PS5's most unique hardware is its **Custom I/O Complex**:

- **Kraken hardware decompressor**: 8-9 GB/s compressed-to-raw
  - Equivalent to ~9 Zen 2 CPU cores of decompression work
  - Supports zlib, Kraken, and Oodle formats
- **Direct Storage architecture**: GPU can access SSD directly
- **Custom DMA engine**: Manages data flow without CPU involvement
- **Custom flash controller**: 5.5 GB/s raw, 8-9 GB/s compressed

**CRITICAL**: The Custom I/O Complex is **NOT accessible from Linux**. It is only available when running native Orbis OS code. For our cluster, this means:
- Console nodes running Linux cannot use hardware decompression
- We deploy a **dual-mode agent**: Linux primary + Orbis OS native fallback
- The Orbis OS agent handles decompression-heavy workloads

### PS5 Linux Status (April 2026)

TheFlow's ps5-linux project achieved:
- Full Linux boot on PS5 Phat (firmwares 3.xx-4.xx)
- Ubuntu 24.04 image available
- 4K60 HDMI output
- Full GPU acceleration via AMDGPU + Mesa
- M.2 NVMe SSD support
- Custom Ethernet driver (Gigabit)
- CPU/GPU boost control utility

**Target firmware for cluster PS5s**: 3.xx-4.xx for best Linux compatibility, 4.51 for best overall jailbreak stability.

## 2.5 Comparative Analysis

### Console vs PC Cost Comparison

| Component | PS4 Pro Build | Equivalent PC | Savings |
|-----------|--------------|---------------|---------|
| System | $200 (used) | — | — |
| CPU | 8x Jaguar 2.1GHz | Athlon 3000G ($50) | Included |
| GPU | 4.2 TFLOPS GCN | RX 570 4GB ($80 used) | Included |
| RAM | 8GB GDDR5 218GB/s | 8GB DDR4 ($25) | Included |
| SSD | 1TB SATA (upgrade) | 1TB SATA ($50) | — |
| PSU | Built-in | $30 | Included |
| Case | Built-in | $30 | Included |
| **Total** | **$250** | **$265+** | **~6%** |

The savings increase dramatically when comparing GPU compute specifically:

| GPU Compute | Cost | TFLOPS | $/TFLOP |
|-------------|------|--------|---------|
| PS4 Pro (full system) | $200 | 4.2 | **$48** |
| RX 580 8GB (GPU only) | $80 | 6.2 | $13 |
| RX 6600 (GPU only) | $180 | 8.9 | $20 |
| Full PC + RX 6600 | $600 | 8.9 | $67 |

When considering the **complete system cost** (not just GPU), the PS4 Pro is competitive, and the PS5 is significantly cheaper per TFLOP than a comparable PC build.

### Power Efficiency

| Device | TFLOPS | Watts (load) | TFLOPS/Watt |
|--------|--------|-------------|-------------|
| PS4 Base | 1.84 | 120W | 0.015 |
| PS4 Pro | 4.20 | 160W | 0.026 |
| PS5 | 10.28 | 200W | 0.051 |
| Desktop (RX 6600) | 8.93 | 250W | 0.036 |

The PS5 is the most power-efficient option, delivering 0.051 TFLOPS/Watt — better than a mid-range desktop GPU setup.
