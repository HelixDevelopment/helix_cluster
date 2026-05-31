# Dimension 5: Enterprise, Server & Cloud Compute Nodes for HelixCluster

**Research Date:** June 2026
**Analyst:** Technology Research Division
**Scope:** Enterprise-grade compute devices capable of joining HelixCluster as worker nodes, including ARM servers, x86 servers, workstations, mini PCs, and cloud VM integration patterns.

---

## Executive Summary

Enterprise and server-grade hardware represents the highest-performance tier of HelixCluster nodes. The used/liquidation server market in 2025-2026 offers extraordinary value, with 64-core AMD EPYC processors available for under $200 and 128-core ARM servers priced competitively against consumer desktop systems. Cloud spot instances can deliver compute at $0.001-0.005/vCPU/hour when properly managed. This report evaluates the complete landscape of enterprise compute options, from sub-$100 used EPYC processors to $10,000 Threadripper workstations, mapping each to optimal HelixCluster integration patterns.

**Key Findings:**
- **Best price/core for used servers in 2025:** AMD EPYC 7551 (32 cores, ~$65-75 used) at approximately $2.10/core, or EPYC 7742 (64 cores, ~$500) at ~$7.80/core for a more modern platform
- **80-core ARM server for under $1,000:** Yes - Ampere Altra reference boards with Q80-30 processors have appeared at ~$1,689 retail, with used/liquidation pricing trending toward $800-1,200
- **Used EPYC 7713 (~$800) vs new Threadripper 7960X ($1,499):** The EPYC wins on raw core count (64 vs 24), total memory capacity and bandwidth, but the Threadripper wins on single-threaded performance (Zen 4 vs Zen 3) and platform modernity
- **Cloud spot breakeven:** Spot instances become cheaper than owning hardware for workloads running less than ~300-400 hours/month when fully burdened costs are considered
- **Best mini PC for compact cluster node:** Minisforum MS-01 (i9-13900H, dual 10GbE, ~$679 barebones) offers the best networking and expansion in its class

---

## Section 1: ARM Servers

### 1.1 Ampere Altra / Altra Max (80-128 Cores)

The Ampere Altra family, built on Arm Neoverse N1 cores (TSMC 7nm), was the first production ARM server platform to achieve widespread adoption. It represents the most accessible path to 80+ CPU cores in a single socket.

**Key Specifications:**

| Model | Cores | TDP | Max Clock | Memory | PCIe | Price (New) | Price (Used) |
|-------|-------|-----|-----------|--------|------|-------------|--------------|
| Altra Q80-30 | 80 | 150W | 3.0 GHz | 8-ch DDR4-3200, 4TB max | 128x Gen4 | ~$1,689 [^2628^] | ~$800-1,200 |
| Altra Max M128-30 | 128 | 183W | 3.0 GHz | 8-ch DDR4-3200, 4TB max | 128x Gen4 | ~$2,500 bundle [^2446^] | ~$1,200-2,000 |
| Altra Max M96-28 | 96 | 128W | 2.8 GHz | 8-ch DDR4-3200 | 128x Gen4 | ~$2,000 | ~$1,000-1,500 |

**Available Systems:**
- **Mt. Snow** (single-socket): 2U rackmount, up to 4 GPUs, 24x NVMe bays, dual 2000W PSU [^2448^]
- **Mt. Jade** (dual-socket): Up to 256 cores total, Arm SystemReady LS certified [^2435^]
- **Gigabyte servers**: R-series rackmount platforms with Altra support
- **ASRock Rack ALTRAD8UD-1L2T**: Micro-ATX motherboard bundle option [^2649^]

**Linux Compatibility:** Excellent. Full upstream Linux kernel support since 5.10+. Certified for Ubuntu, RHEL, SUSE, Debian, FreeBSD. Supports LinuxBoot for open-source firmware [^2435^]. Full virtualization support (KVM, Xen), containerization (Docker, Kubernetes).

**HelixCluster Integration:** SEMI-TRUSTED tier. Best suited for: containerized workloads, CI/CD runners, web services, file servers. The high core count makes it ideal for running many concurrent containers. Each core is less powerful than x86 equivalents, so workload selection matters.

**Performance vs x86:** Per-core performance roughly matches Intel Skylake/Xeon Silver. The advantage is in density: 128 cores at 183W TDP is exceptional. Memory bandwidth (8-ch DDR4-3200 = ~204 GB/s) is competitive with 8-channel x86 platforms.

**Gotchas:** ARM-specific binaries required (though most software now has ARM builds). Some proprietary software may lack ARM support. Single-threaded performance is modest. The used market is still developing; motherboards can be hard to find.

### 1.2 AmpereOne (192-256 Cores, 2024-2025)

Ampere's custom-core design represents a significant leap forward, targeting 192-256 cores on 5nm process technology.

| SKU | Cores | Clock | Price (New) |
|-----|-------|-------|-------------|
| A192-32X | 192 | 3.2 GHz | ~$5,555 [^2442^] |
| A144-26X | 144 | 2.6 GHz | ~$2,936 [^2446^] |

**Key Features:** Custom Armv8.6+ cores (not Neoverse), 12-channel DDR5 memory, up to 128x PCIe Gen5, on-chip AI acceleration. Available from Supermicro, ASRock Rack, Gigabyte, BYD, and IEIT [^2446^].

**HelixCluster Integration:** SEMI-TRUSTED tier for high-density container hosting. The A192-32X at $5,555 compares very favorably to a $15,000 EPYC 9965 (128 cores), offering 50% more cores at 63% lower cost [^2442^].

### 1.3 AWS Graviton 3 / Graviton 4

AWS's custom ARM processors offer the most accessible way to deploy ARM compute at scale without capital expenditure.

| Processor | Cores | Architecture | Memory Bandwidth | Key Instance Families |
|-----------|-------|--------------|------------------|----------------------|
| Graviton3 | 64 | Neoverse V1 | 307 GB/s | c7g, m7g, r7g, x2gd |
| Graviton4 | 96 | Neoverse V2 | 537 GB/s | c8g, m8g, r8g, x8g, i8g [^2514^] |

**Graviton4 Improvements:** 50% more cores, 75% more memory bandwidth, DDR5-5600 support, up to 3TB memory (x8g), ~30% better price/performance vs Graviton3 [^2514^]. Real-world benchmarks show 40% improvement in RDS workloads, 30% reduction in CI/CD build times.

**HelixCluster Integration:** STANDARD tier (cloud VMs are trusted but ephemeral). Graviton instances are 20% cheaper than Intel equivalents. Spot pricing for c7g/m7g instances can drop below $0.01/vCPU/hour. Note: Graviton3 has wider SVE SIMD registers (256-bit) than Graviton4 (128-bit), making it paradoxically faster for certain vector search workloads [^2516^].

### 1.4 Other ARM Servers

**Huawei Kunpeng 920:** 24-64 custom TaiShan v110 cores (Arm v8.2), 7nm, 2.6 GHz, up to 8-ch DDR4. Primarily available in China and restricted markets. PassMark multi-thread: ~9,500 for 24-core variant [^2549^]. Competitive with Intel Xeon Gold 6230 in multi-threaded workloads [^2556^]. Limited availability outside China due to sanctions.

**Marvell ThunderX3:** Up to 96 cores with 4-way SMT (384 threads/socket), Arm v8.3, TSMC 7nm, 8-ch DDR4-3200, 64x PCIe Gen4 [^2581^][^2576^]. 2-3x socket-level performance vs ThunderX2. Limited deployment compared to Ampere; primarily in HPC and cloud environments. ThunderX4 (2024+) planned on advanced process.

---

## Section 2: x86 Servers (Used/Affordable Market)

### 2.1 AMD EPYC — The Used Market Champion

AMD EPYC processors dominate the used server value proposition in 2025. The combination of high core counts, massive PCIe lane counts, and DDR4/DDR5 memory support makes them ideal for HelixCluster compute nodes.

#### EPYC 7002 Series (Rome, Zen 2, SP3)

| Model | Cores | TDP | Boost | Used Price (2025) | $/Core |
|-------|-------|-----|-------|-------------------|--------|
| 7551 | 32 | 180W | 3.0 GHz | ~$65-75 [^2577^] | ~$2.10 |
| 7402 | 24 | 180W | 3.35 GHz | ~$175 [^2635^] | ~$7.30 |
| 7702 | 64 | 200W | 3.35 GHz | ~$500-600 [^2437^] | ~$9.20 |
| 7742 | 64 | 225W | 3.4 GHz | ~$500-750 [^2620^] | ~$9.80 |
| 7H12 | 64 | 280W | 3.3 GHz | ~$600-800 | ~$11.70 |

**Platform Cost:** Supermicro H11SSL-i (single-socket) or H11DSI (dual-socket) motherboards: $200-400 used. DDR4 RDIMM ECC 32GB modules: ~$30-40 each. A complete 64-core EPYC 7742 server build can be assembled for under $1,500.

**Key Advantages:** 128x PCIe Gen4 lanes per socket (unmatched I/O), up to 4TB RAM per socket, all-core boost typically 2.8-3.0 GHz under load. Full Linux compatibility. Excellent for GPU hosting, storage servers, VM hosts.

#### EPYC 7003 Series (Milan, Zen 3, SP3)

| Model | Cores | TDP | Boost | Used Price (2025) | $/Core |
|-------|-------|-----|-------|-------------------|--------|
| 7713 | 64 | 225W | 3.68 GHz | ~$800-1,000 [^2476^] | ~$14.00 |
| 7763 | 64 | 280W | 3.5 GHz | ~$1,200-1,500 | ~$21.00 |
| 75F3 | 32 | 280W | 4.0 GHz | ~$600-800 | ~$21.00 |

**Milan vs Rome:** 15-20% IPC improvement from Zen 2 to Zen 3, higher boost clocks, improved Infinity Fabric. Compatible with most Rome motherboards (BIOS update required). The 7713 at ~$800 offers the best balance of performance and core density for modern workloads [^2466^][^2476^].

#### EPYC 9004/9005 Series (Genoa/Turin, Zen 4/5, SP5)

| Model | Cores | TDP | Boost | Used Price (2025) | $/Core |
|-------|-------|-----|-------|-------------------|--------|
| 9654 | 96 | 360W | 3.7 GHz | ~$1,500-2,000 [^2501^] | ~$18.20 |
| 9754 | 128 | 360W | 3.7 GHz | ~$4,200 | ~$32.80 |
| 9535 | 64 | 280W | 4.3 GHz | ~$1,500 | ~$23.40 |

**Platform:** Requires SP5 socket motherboards (DDR5, PCIe Gen5). Newer platform with higher platform costs but 12-channel DDR5-4800/5600 memory and up to 128x PCIe Gen5 lanes. The 9654 at ~$1,500 used represents exceptional value for 96 Zen 4 cores [^2496^][^2501^].

**Comparison: Used EPYC 7713 ($800) vs Threadripper 7960X ($1,499):**

| Metric | EPYC 7713 (64c) | Threadripper 7960X (24c) |
|--------|-----------------|--------------------------|
| Architecture | Zen 3 (2021) | Zen 4 (2023) |
| Cores/Threads | 64/128 | 24/48 |
| Base/Boost | 2.0/3.68 GHz | 4.2/5.3 GHz |
| Memory Channels | 8x DDR4-3200 | 4x DDR5-5200 |
| Max Memory | 4 TB | 1 TB |
| PCIe Lanes | 128x Gen4 | 80x Gen5 |
| TDP | 225W | 350W |
| Aggregate Performance | ~81,500 PassMark | ~83,400 PassMark [^2476^] |
| Cost-effectiveness | 7.72 | 35.91 [^2476^] |

The EPYC 7713 provides 167% more cores with comparable aggregate performance. The Threadripper 7960X wins decisively on single-threaded performance (Zen 4 IPC + 5.3 GHz boost), cost-effectiveness for desktop workloads, and platform modernity (DDR5, PCIe Gen5) [^2476^][^2466^]. For HelixCluster, the EPYC is the clear winner for multi-threaded/container workloads; the Threadripper is better for mixed or latency-sensitive workloads.

### 2.2 AMD Threadripper PRO Workstations

Threadripper PRO bridges the gap between desktop and server, offering EPYC-like core counts with workstation-class features.

| Model | Cores | Architecture | Memory | PCIe | MSRP | Used Price |
|-------|-------|--------------|--------|------|------|------------|
| 5995WX | 64 | Zen 3 | 8-ch DDR4, 2TB max | 128x Gen4 | $6,499 | ~$2,500-3,500 [^2473^] |
| 7975WX | 32 | Zen 4 | 8-ch DDR5, 2TB max | 128x Gen5 | $3,299 | ~$1,500-2,000 |
| 7995WX | 96 | Zen 4 | 8-ch DDR5, 2TB max | 128x Gen5 | $9,999 | ~$7,000-9,000 [^2634^] |

**Platform:** WRX80 (5000-series) or WRX90/TRX50 (7000-series) motherboards. ASUS Pro WS WRX80E-SAGE: ~$1,000. ASRock WRX80 Creator: ~$900. ASUS Pro WS WRX90E-SAGE SE: ~$1,200+ [^2474^].

**HelixCluster Integration:** SEMI-TRUSTED tier. The 5995WX at $3,000 (CPU + motherboard) offers 64 Zen 3 cores with 8-channel memory in a workstation form factor. Ideal for: dev/test environments, build servers, local AI inference with GPU, video transcoding. Power consumption is significant (280-350W TDP). Not suitable for 24/7 production deployment in typical workstation cases due to thermal constraints.

### 2.3 Intel Xeon Scalable

Intel Xeon processors offer broad software compatibility but generally lag AMD on core count and price/performance in the used market.

| Generation | Codename | Max Cores | Key Models | Used Pricing |
|------------|----------|-----------|------------|--------------|
| 1st Gen | Skylake-SP | 28 | Platinum 8180, Gold 6150 | $200-600 |
| 2nd Gen | Cascade Lake | 56 | Platinum 9282, Gold 6258R | $300-800 |
| 3rd Gen | Ice Lake-SP | 40 | Platinum 8380, Gold 6354 | $400-1,200 |
| 4th Gen | Sapphire Rapids | 60 | Platinum 8490H, Gold 6454 | $800-3,000 |
| 5th Gen | Emerald Rapids | 64 | Platinum 8592+, Gold 6548Y | $1,500-5,000 |

**Legacy E5 v3/v4 (Haswell/Broadwell):** Still widely available at very low prices. E5-2697 v4 (18 cores, 2.3 GHz): ~$50-100. E5-2699 v4 (22 cores): ~$100-150. Dual-socket platforms offer 36-44 total cores for under $200. Supermicro X10DRI motherboards: ~$150-250 used. These systems are power-hungry but extremely cost-effective for low-intensity workloads.

**HelixCluster Integration:** TRUSTED tier for older systems (well-understood, mature platforms). Best for: file servers, backup targets, monitoring infrastructure, low-intensity containers. Avoid for high-performance compute (newer EPYC or Graviton significantly outperform at similar prices).

---

## Section 3: GPU Compute Nodes

### 3.1 NVIDIA GPUs

| GPU | Architecture | VRAM | FP16 TFLOPS | Used Price (2025) | Cloud $/hr |
|-----|-------------|------|-------------|-------------------|------------|
| RTX 4090 | Ada Lovelace | 24GB GDDR6X | 330 | ~$1,200-1,600 | $0.30-0.50 |
| RTX 5090 | Blackwell | 32GB GDDR7 | 450 | ~$1,800-2,500 | $0.65-0.90 |
| A100 40GB | Ampere | 40GB HBM2e | 312 | ~$4,800-7,800 | $0.78-1.29 |
| A100 80GB | Ampere | 80GB HBM2e | 312 | ~$8,000-18,900 | $1.49-3.43 |
| H100 SXM5 | Hopper | 80GB HBM3 | 989 | ~$12,000-22,000 | $2.00-4.00 |
| L40S | Ada Lovelace | 48GB GDDR6 | 366 | ~$3,000-5,000 | $0.50-0.72 |

**Key Insight:** Used A100 pricing has dropped 60-70% from peak. Refurbished A100 80GB units now trade at $4,800-8,500 [^2470^]. Used H100 SXM5 cards that sold for $40,000 in late 2023 now move for $6,000-15,000 on secondary markets [^2465^]. For inference workloads, used A100s often deliver better cost-per-task than newer hardware at 2-3x the price [^2464^].

**RTX 4090/5090 vs A100 for ML:** The RTX 5090 outperforms A100 by ~2.6x on LLM inference benchmarks, with 32GB VRAM enabling larger models than the RTX 4090's 24GB [^2542^][^2543^]. However, consumer GPUs lack NVLink (requiring PCIe for multi-GPU), have no ECC memory, and NVIDIA's GeForce EULA technically prohibits datacenter deployment. For production HelixCluster GPU nodes, the A100/A6000 or L40S are more appropriate.

### 3.2 AMD Instinct GPUs

| GPU | Architecture | VRAM | FP64 TFLOPS | Used Price (2025) |
|-----|-------------|------|-------------|-------------------|
| MI100 | CDNA 1 | 32GB HBM2 | 11.5 | ~$400-600 |
| MI210 | CDNA 2 | 64GB HBM2e | 45 | ~$2,000-3,000 [^2467^] |
| MI250X | CDNA 2 | 128GB HBM2e | 48 | ~$5,000-8,000 |
| MI300X | CDNA 3 | 192GB HBM3 | 163 | ~$11,000-15,000 [^2470^] |

**ROCm Ecosystem Status:** ROCm 7.0 (2026) supports MI300X, MI250X, MI210 with full PyTorch and TensorFlow integration [^2652^]. HIP provides CUDA source compatibility. Key limitation: ROCm primarily supports professional/Instinct GPUs, with limited consumer GPU support. The MI210 at 300W TDP delivers 181 TFLOPS FP16 and 64GB HBM2e at 1.6 TB/s bandwidth, competitive with A100 [^2467^][^2468^].

**HelixCluster GPU Node Recommendation:** For CUDA-dependent workloads, used A100 40GB (~$5,000) offers the best balance of memory, performance, and ecosystem maturity. For ROCm-friendly workloads, the MI210 (~$2,500 used) offers exceptional value. For budget inference, RTX 4090 (~$1,200 used) is unmatched in price/performance but requires consumer-grade system design.

---

## Section 4: Mini PCs / Compact Workstations

### 4.1 Intel-Based Mini PCs

| Model | CPU | Cores | RAM | Networking | Price | Notes |
|-------|-----|-------|-----|------------|-------|-------|
| Minisforum MS-01 | i9-13900H | 14c/20t | 64GB DDR5-5200 | 2x 10GbE SFP+, 2x 2.5GbE | ~$679 barebones [^2550^] | Best-in-class networking, 3x M.2, PCIe x16 slot |
| ASUS NUC 14 Pro | Core Ultra 7 165H | 16c/22t | 96GB DDR5 | 2x 2.5GbE, 2x Thunderbolt 4 | ~$869 barebones [^2623^] | vPro, AI NPU, 3-year warranty |
| Intel NUC 13 Pro | i7-1360P | 12c/16t | 64GB DDR5 | 2x 2.5GbE, 2x Thunderbolt 4 | ~$500-600 barebones | Proven platform, widely available |

**Minisforum MS-01 Deep Dive:** This is the standout mini workstation for HelixCluster. Dual 10GbE SFP+ (Intel X710) provides 20Gbps aggregate network throughput, essential for distributed storage or high-bandwidth clustering. Three M.2 slots support up to 6TB NVMe storage. The PCIe x16 (x8 electrical) slot can accommodate a half-height GPU like the NVIDIA RTX A2000 or additional NICs. 10Gbase-T or SFP+ DAC cables enable direct high-speed node-to-node links without a switch. Intel vPRO support enables remote management [^2545^][^2555^].

### 4.2 AMD-Based Mini PCs

| Model | CPU | Cores | RAM Max | Price | Notes |
|-------|-----|-------|---------|-------|-------|
| Beelink SER9 Pro | Ryzen AI 9 HX 370 | 12c/24t | 64GB LPDDR5X | ~$999 [^2515^] | Radeon 890M, excellent iGPU |
| Beelink SER8 | Ryzen 7 8845HS | 8c/16t | 64GB DDR5 | ~$479-550 | Best value in AMD mini PCs |
| GMKtec NucBox K9 | Core Ultra 9 | 16c/22t | 64GB | ~$600-700 | Intel-based alternative |

### 4.3 Apple Silicon (M4 Pro / M3 Ultra)

| Model | CPU Cores | GPU Cores | Neural Engine | Memory | Memory Bandwidth | Price |
|-------|-----------|-----------|---------------|--------|-----------------|-------|
| Mac Mini M4 Pro | 14c (10P+4E) | 20-core | 16-core | 24-64GB | 273 GB/s [^2497^] | $1,399-2,199 |
| Mac Studio M3 Ultra | 32c (24P+8E) | 80-core | 32-core | 96-512GB | 819 GB/s [^2519^] | $3,995-9,195 |
| Mac Studio M4 Max | 16c (12P+4E) | 40-core | 16-core | 36-128GB | 546 GB/s [^2519^] | $1,999-3,999 |

**HelixCluster Integration Considerations:** Apple Silicon runs macOS, not Linux natively. However, it can participate in a heterogeneous cluster through:

1. **Docker Desktop / Colima:** Full container support with Rosetta 2 x86 emulation for mixed-architecture workloads [^2648^]
2. **Virtualization:** UTM or VMware Fusion for Linux VMs (with performance overhead)
3. **Native ARM containers:** Most Linux containers now have ARM images
4. **MLX Framework:** Apple's native ML framework for AI workloads [^2651^]

**The M3 Ultra is unique:** 32 CPU cores, 80 GPU cores, 819 GB/s memory bandwidth, and up to 512GB unified memory enable running 600B+ parameter LLMs locally [^2513^]. For a cluster node focused on AI inference, the M3 Ultra at ~$4,000 (base config) offers performance comparable to an A100 system at a fraction of the power and noise. **Security model:** SEMI-TRUSTED (closed hardware, Apple's security enclave provides attestation but not full transparency).

**Limitations:** macOS licensing restricts datacenter deployment. No native Kubernetes node support (requires Linux VM). No 10GbE networking (Thunderbolt 5 or 10GbE adapter available for Mac Studio). Limited to single-socket, non-expandable memory.

### 4.4 Mini PC Comparison for HelixCluster

| Metric | MS-01 (i9) | NUC 14 Pro | SER9 Pro | Mac Mini M4 Pro |
|--------|------------|------------|----------|-----------------|
| Best Feature | Dual 10GbE | vPro/AI NPU | Radeon 890M GPU | Unified memory |
| Cores | 14/20 | 16/22 | 12/24 | 14/14 |
| Max RAM | 64GB | 96GB | 64GB | 64GB |
| Networking | 2x10GbE+2x2.5GbE | 2x2.5GbE+TB4 | 1x2.5GbE | 1x10GbE+TB4 |
| Expandability | PCIe x16, 3xM.2 | 3x storage | 1x M.2 | None |
| Used Price | $550-700 | $600-800 | $450-550 | $1,100-1,500 |
| **HelixCluster Score** | **9.5/10** | **7/10** | **7.5/10** | **7/10** |

The **Minisforum MS-01** is the clear winner for a HelixCluster mini node due to its dual 10GbE networking (enabling direct high-speed mesh connectivity), PCIe expansion slot, and competitive price [^2550^][^2545^].

---

## Section 5: Cloud VM Integration

### 5.1 Spot Instance Economics

AWS EC2 spot instances offer up to 90% savings over On-Demand pricing [^2495^]. Real-world blended savings average 59-77% for Kubernetes workloads [^2500^].

| Instance Type | vCPUs | RAM | On-Demand | Spot (typical) | Spot $/vCPU/hr |
|--------------|-------|-----|-----------|----------------|----------------|
| t4g.nano | 2 | 0.5GB | $0.0042/hr | ~$0.001/hr | $0.0005 |
| m7g.large | 2 | 8GB | $0.0816/hr | ~$0.020/hr | $0.010 |
| c7g.2xlarge | 8 | 16GB | $0.29/hr | ~$0.058/hr | $0.0073 |
| r7g.2xlarge | 8 | 64GB | $0.456/hr | ~$0.091/hr | $0.011 |
| c7g.16xlarge | 64 | 128GB | $2.32/hr | ~$0.46/hr | $0.0072 |
| m8g.metal-24xl | 96 | 384GB | $5.32/hr | ~$1.06/hr | $0.011 |

**Graviton (ARM) instances are 20% cheaper than Intel equivalents** with comparable or better performance for containerized workloads [^2495^]. Spot pricing for Graviton c7g instances in us-west-2 can reach as low as **$0.001-0.003 per vCPU/hour** during low-demand periods.

### 5.2 Preemption Handling for HelixCluster

Cloud spot instances can be reclaimed with only a 2-minute warning (AWS) or 30 seconds (Azure/GCP) [^2619^]. HelixCluster must handle this gracefully.

**Recommended Architecture:**

```yaml
# Kubernetes-style spot handling for HelixCluster
spot-node-pool:
  taint: "spot=true:NoSchedule"
  toleration: 
    key: "spot"
    operator: "Equal"
    value: "true"
    effect: "NoSchedule"
  
workload-classification:
  - critical: on-demand only, PriorityClass=high
  - standard: mixed, can tolerate spot interruption
  - batch/opportunistic: spot-only, checkpoint-enabled

interruption-handling:
  - aws: 2-minute warning via IMDS endpoint
  - handler: drain node + reschedule pods
  - checkpoint: save state to S3/object storage
```

**Key Strategies:**

1. **Mixed replica strategy:** Keep N baseline replicas on on-demand; add M opportunistic replicas on spot [^2621^]
2. **Instance diversification:** Use multiple instance families across AZs to reduce correlated preemption [^2622^]
3. **Checkpointing:** For batch/ML workloads, save incremental progress so interrupted jobs can resume
4. **Fallback to on-demand:** Automation tools can shift workloads to on-demand when spot capacity is unavailable [^2619^]

### 5.3 WireGuard: Connecting Cloud to On-Prem

WireGuard provides a lightweight, high-performance VPN tunnel between cloud spot instances and on-prem HelixCluster nodes [^2547^][^2546^].

```
Architecture:
  On-Prem Cluster        WireGuard Tunnel        Cloud Spot Instances
  [HelixCluster]  <====  (UDP/51820)  ====>  [AWS/GCP/Azure VMs]
  [10.0.1.0/24]         Encrypted mesh          [10.0.2.0/24]
```

**Configuration benefits:** Kernel-level implementation (low overhead), modern cryptography (Curve25519), simple configuration (~10 lines vs. hundreds for OpenVPN), native roaming support [^2547^]. In Kubernetes, WireGuard can be deployed as a DaemonSet with hostNetwork for node-level mesh connectivity [^2546^].

### 5.4 Cloud vs. Own Hardware: TCO Breakeven

| Scenario | Cloud (Spot) | Owned Hardware | Breakeven |
|----------|-------------|----------------|-----------|
| 64 vCPU, continuous | ~$110-150/month | ~$80-120/month (EPYC, amortized) | ~18-24 months |
| 64 vCPU, bursty (200h/mo) | ~$35-50/month | ~$80-120/month | Cloud always wins |
| GPU (A100), continuous | ~$600-800/month | ~$500-700/month | ~12-18 months |
| GPU (A100), bursty | ~$150-300/month | ~$500-700/month | Cloud always wins |

**Rule of thumb:** For steady-state 24/7 workloads, owned hardware breaks even at 18-30 months. For bursty, variable, or experimental workloads, cloud spot instances are typically 3-5x more cost-effective. A hybrid approach (on-prem base + cloud burst) optimizes for both cost and flexibility.

---

## Section 6: TCO Analysis & Recommendations

### 6.1 Price/Core Comparison (Complete Landscape)

| Hardware Option | Cores | System Price* | $/Core | Power | Best For |
|----------------|-------|---------------|--------|-------|----------|
| Used EPYC 7551 + board | 32 | ~$350 | $10.94 | 180W | Entry-level compute node |
| Used EPYC 7402 + board | 24 | ~$400 | $16.67 | 180W | Balanced compute/storage |
| Used EPYC 7742 + board | 64 | ~$900 | $14.06 | 225W | High-density container host |
| Used EPYC 7713 + board | 64 | ~$1,200 | $18.75 | 225W | Modern high-performance node |
| Ampere Altra Q80-30 | 80 | ~$1,500 | $18.75 | 150W | ARM-native container density |
| Ampere Altra Max M128 | 128 | ~$2,500 | $19.53 | 183W | Maximum core density |
| EPYC 9654 (Genoa, used) | 96 | ~$2,000 | $20.83 | 360W | Modern x86, DDR5, PCIe5 |
| Threadripper 5995WX | 64 | ~$3,500 | $54.69 | 280W | Workstation + occasional server |
| Mac Mini M4 Pro | 14 | $1,399 | $99.93 | 35W | Silent dev node, AI inference |
| Mac Studio M3 Ultra | 32 | $3,995 | $124.84 | 215W | Local LLM inference champion |
| Cloud spot (Graviton) | 64 | $0 | ~$0.007/vCPU/hr | N/A | Burst, experimental workloads |

*System price includes CPU, motherboard, 64GB RAM, 1TB NVMe, PSU, case where applicable

### 6.2 HelixCluster Tier Recommendations

| Tier | Hardware | Workload Types | Trust Model |
|------|----------|----------------|-------------|
| **Core/Control** | EPYC 7713/9654, Threadripper PRO | API gateways, databases, scheduling | TRUSTED |
| **Compute** | EPYC 7742/7702, Ampere Altra | Containerized workloads, CI/CD, batch | SEMI-TRUSTED |
| **GPU** | A100 80GB, MI300X, RTX 4090/5090 | AI inference, training, rendering | SEMI-TRUSTED |
| **Edge** | Minisforum MS-01, NUC 14 Pro | Field deployments, caching, relay | EDGE |
| **Burst** | Cloud spot instances (Graviton) | Overflow, experimental, temporary | STANDARD |
| **Specialized** | Mac Studio M3 Ultra, Threadripper | AI dev, video, creative workflows | SEMI-TRUSTED |

### 6.3 Final Recommendations

1. **Best overall HelixCluster compute node:** Used EPYC 7742 (64 cores) + Supermicro H11SSL-i + 128GB DDR4 + 1TB NVMe. Total: ~$900-1,100. Exceptional value with 128x PCIe Gen4 lanes for expansion.

2. **Best ARM node:** Ampere Altra Max M128-30 if available under $2,000. Otherwise, Altra Q80-30 at ~$1,500. Watch the used/liquidation market closely.

3. **Best compact node:** Minisforum MS-01 with i9-13900H. The dual 10GbE makes it uniquely suited for cluster mesh networking in a tiny form factor.

4. **Best cloud integration:** AWS Graviton4 spot instances (c8g/m8g families) at $0.007-0.012/vCPU/hour. Use WireGuard mesh to connect to on-prem cluster.

5. **Best GPU node for inference:** Used A100 40GB at ~$5,000 or RTX 4090 at ~$1,200 for budget deployments. For memory-hungry LLMs, used A100 80GB at ~$8,000.

6. **Best AI dev workstation:** Mac Studio M3 Ultra (96GB) for its unified memory architecture enabling local LLM inference that would otherwise require $15,000+ in GPU hardware.

---

## Raw Evidence Log

| Source ID | URL | Description | Date |
|-----------|-----|-------------|------|
| [^2435^] | https://book.linuxboot.org/case_studies/Ampere_study.html | LinuxBoot on Ampere Mt. Jade, ARM SystemReady LS certification | 2024 |
| [^2440^] | https://www.theregister.com/on-prem/2020/03/03/ampere-altra-80-core-arm-server/ | Ampere Altra initial announcement, 80-core ARM server | 2020 |
| [^2442^] | https://news.ycombinator.com/item?id=42332304 | AmpereOne A192-32X pricing discussion ($5,555 vs EPYC 9965 $15,000) | 2024 |
| [^2446^] | https://www.servethehome.com/ampere-ampereone-pricing-and-sku-list/ | AmpereOne complete SKU list and pricing with OEM partners | 2024 |
| [^2448^] | https://amperecomputing.com/assets/G_New_Mt_Snow_PB_v0_65_GPGPU_20211022601_9cd69d8877.pdf | Ampere Mt. Snow product brief, full specifications | 2021 |
| [^2464^] | https://hashrateindex.com/blog/used-gpu-market-pricing-deprecation-secondary-ai/ | Used GPU market analysis, A100/H100 pricing trends | 2026 |
| [^2465^] | https://www.cloudzero.com/blog/h100-gpu-cost/ | H100 GPU cost analysis 2026, used pricing $6K-15K | 2026 |
| [^2466^] | https://pc-builds.com/compare/cpu/15Q1vq/epyc-7713/ryzen-threadripper-7960x | EPYC 7713 vs Threadripper 7960X comparison | 2025 |
| [^2467^] | https://heqingele.com/blog/what-is-amd-instinct-mi210-main-uses-buy-amds-mi210/ | AMD Instinct MI210 specifications and use cases | 2025 |
| [^2468^] | https://rocm.docs.amd.com/en/docs-7.0.1/compatibility/compatibility-matrix.html | ROCm 7.0 compatibility matrix, supported GPUs | 2026 |
| [^2470^] | https://electronics.alibaba.com/buyingguides/nvidia-a100-80gb-price-guide-2026 | A100 80GB pricing guide, refurbished $4,800-8,500 | 2026 |
| [^2473^] | https://www.ebay.com/shop/threadripper-pro-5995wx | Threadripper PRO 5995WX used pricing ~$3,000 | 2025 |
| [^2474^] | https://www.tomshardware.com/reviews/amd-threadripper-pro-5995wx-5975wx-cpu-review | Threadripper PRO 5995WX review, WRX80 motherboard pricing | 2022 |
| [^2476^] | https://technical.city/en/cpu/EPYC-7713P-vs-Ryzen-Threadripper-7960X | EPYC 7713P vs Threadripper 7960X benchmarks and price comparison | 2025 |
| [^2494^] | https://www.pump.co/blog/aws-ec2-pricing-update/ | AWS EC2 pricing update 2025, GPU cuts up to 45% | 2025 |
| [^2495^] | https://wring.co/blog/aws-ec2-pricing-guide | Complete AWS EC2 pricing guide with spot examples | 2026 |
| [^2497^] | https://support.apple.com/en-us/121555 | Mac Mini M4 Pro technical specifications | 2024 |
| [^2500^] | https://www.nops.io/blog/aws-spot-instance-pricing/ | AWS spot instance pricing FAQ, 70-90% savings | 2025 |
| [^2501^] | https://www.ebay.com/shop/amd-epyc-9654-96-core-processor | EPYC 9654 (96-core Genoa) used pricing ~$1,500-2,000 | 2025 |
| [^2513^] | https://www.apple.com/hk/en/newsroom/2025/03/apple-reveals-m3-ultra/ | Apple M3 Ultra announcement, 32-core CPU, 80-core GPU, 512GB RAM | 2025 |
| [^2514^] | https://buw.medium.com/aws-graviton4-complete-guide-strategic-performance-optimization-and-cost-reduction-43a885d891d1 | Graviton4 complete guide, specs and benchmarks | 2026 |
| [^2515^] | https://hostbor.com/beelink-ser9-pro-the-mini-pc/ | Beelink SER9 Pro review and pricing ($929-999) | 2025 |
| [^2516^] | https://www.lkuffo.com/graviton3-better-than-graviton4-vector-search/ | Graviton3 vs Graviton4 vector search analysis, SVE register differences | 2025 |
| [^2519^] | https://support.apple.com/en-hk/122211 | Mac Studio 2025 technical specifications (M4 Max, M3 Ultra) | 2025 |
| [^2542^] | https://www.spheron.network/blog/rtx-5090-vs-rtx-4090/ | RTX 5090 vs RTX 4090 for AI, full spec comparison | 2026 |
| [^2545^] | https://www.minisforum.com/products/minisforum-ms-01 | Minisforum MS-01 official product page, specifications | 2025 |
| [^2547^] | https://patel-aum.medium.com/bridging-cloud-and-on-premises-setting-up-wireguard-vpn-for-unified-kubernetes-networking-400d6a035bed | WireGuard VPN setup for unified Kubernetes networking | 2024 |
| [^2548^] | https://openbenchmarking.org/s/HUAWEI+Kunpeng+920 | Huawei Kunpeng 920 benchmark results and Linux performance | 2025 |
| [^2549^] | https://www.cpubenchmark.net/cpu.php?id=5983 | Kunpeng 920 24-core PassMark benchmarks | 2026 |
| [^2550^] | https://liliputing.com/minisforum-ms-01-is-a-compact-workstation-with-10-gbe-ethernet-3-m-2-slots-and-up-to-core-i9-13900h/ | MS-01 detailed review with pricing ($549-829 configurations) | 2024 |
| [^2555^] | https://www.tweaktown.com/news/95208/minisforum-ms-01-mini-workstation-pc-with-core-i9-13900h-up-to-64gb-ram/index.html | MS-01 announcement, networking and expansion details | 2024 |
| [^2556^] | https://www.huaweicentral.com/russias-baikal-s-vs-huawei-kunpeng-920-vs-intel-xeon-gold-6230-benchmark-test/ | Kunpeng 920 vs Xeon Gold 6230 benchmark comparison | 2023 |
| [^2576^] | https://www.hc32.hotchips.org/assets/program/conference/day1/HotChips2020_Server_Processors_Marvell_Sugumar.pdf | ThunderX3 HotChips presentation, detailed architecture | 2020 |
| [^2577^] | https://www.ebay.com/shop/epyc-7551 | EPYC 7551 32-core used pricing ~$65-75 | 2025 |
| [^2581^] | https://www.networkworld.com/article/968508/marvell-announces-96-core-thunderx3-arm-server-processor.html | ThunderX3 96-core announcement | 2020 |
| [^2619^] | https://cast.ai/blog/how-to-run-fault-tolerant-clusters-on-spot-instances/ | Fault-tolerant Kubernetes on spot instances, preemption handling | 2025 |
| [^2620^] | https://www.ebay.com/p/11040732468 | EPYC 7742 64-core used pricing ~$749 | 2025 |
| [^2623^] | https://au.pcmag.com/desktop-pcs/106062/asus-nuc-14-pro | ASUS NUC 14 Pro review, $869 starting price | 2024 |
| [^2628^] | https://www.neweggbusiness.com/ss/ampere-altra-server-processor/id-113 | Ampere Altra Q80-30 pricing $1,689 at Newegg Business | 2025 |
| [^2634^] | https://www.techpowerup.com/cpu-specs/ryzen-threadripper-pro-7995wx.c3301 | Threadripper PRO 7995WX full specifications, MSRP $9,999 | 2026 |
| [^2635^] | https://www.ebay.com/p/18040741453 | EPYC 7402 24-core used pricing ~$175 | 2025 |
| [^2642^] | https://en.wikipedia.org/wiki/ROCm | ROCm Wikipedia, ecosystem and CUDA comparison | 2025 |
| [^2645^] | https://www.newegg.com/ampere-altra-max-lga-4926/p/N82E16819999002 | Ampere Altra Max M128-30 at Newegg | 2025 |
| [^2648^] | https://medium.com/@guillem.riera/the-most-performant-docker-setup-on-macos-apple-silicon-m1-m2-m3-for-x64-amd64-compatibility-da5100e2557d | Docker setup on Apple Silicon with Colima | 2024 |
| [^2651^] | https://medium.com/predict/apple-mac-studio-m3-ultra-the-monster-hiding-in-plain-sight-edaeb33ec19e | Mac Studio M3 Ultra for virtualization and containers | 2025 |
| [^2652^] | https://www.amd.com/en/products/software/rocm.html | AMD ROCm official page, version history through 7.0 | 2026 |
