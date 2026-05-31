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
