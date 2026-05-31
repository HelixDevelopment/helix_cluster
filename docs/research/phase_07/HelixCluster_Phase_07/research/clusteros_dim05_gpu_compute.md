# Dimension 05: GPU Virtualization & Heterogeneous Compute Engine

## Key Findings

### NVIDIA CUDA Architecture
- CUDA maintains a **three-layer compatibility model**: backward compatibility (newer drivers run older apps), minor version compatibility (apps built within same major CUDA release run on sufficiently new drivers), and forward compatibility (via `cuda-compat-<major>-<minor>` package allowing newer toolkit apps on older drivers across major release families) [^652^].
- **CUDA 12.x requires driver >= 525.60.13**; CUDA 11.x requires driver >= 450.80.2 [^653^]. Forward compatibility is datacenter-GPU-only and requires specific licensing.
- NVIDIA's driver compatibility across frameworks creates a "compatibility matrix dilemma" — PyTorch, TensorFlow, and JAX each have nuanced driver requirements that must be managed in multi-framework environments [^504^]. Container-first strategies with version pinning are recommended best practices.
- **CUDA applications compiled with PTX code** can JIT-compile to new GPU architectures, enabling forward compatibility at the binary level [^660^].

### AMD ROCm
- **ROCm 7.2.0 shipped January 2026** (with 7.11.0 as technology preview) [^682^]. ROCm 6.0 (late 2023) was the milestone adding consumer RDNA 3 GPU support (RX 7900 XTX/XT/GRE) [^682^].
- **MI300X (192GB HBM3) costs ~$15,000 vs H100 (80GB) at ~$32,000**, delivering competitive hardware but with a significant "CUDA gap" in software optimization [^680^].
- Despite 32% theoretical TFLOPS advantage, MI300X achieves only **37-66% of H100/H200 performance in LLM inference** due to the "CUDA gap" — NVIDIA's software ecosystem delivering performance gains beyond hardware specifications [^684^].
- **CUDA gap increases with scale**: 2-GPU configs show 29% NVIDIA advantage; 8-GPU configs show 46% advantage [^684^].
- **ROCm 7.0 delivered 4.6x inference performance improvement** over prior versions [^519^]. PyTorch, JAX, vLLM, SGLang, and DeepSpeed all have official ROCm backends [^681^].
- **HIP (Heterogeneous-compute Interface for Portability)** provides CUDA translation with ~90% automatic conversion for simple kernels [^680^]. The `hipify-perl` tool converts CUDA code to HIP.
- ROCm is **fully open-source** from kernel driver to math libraries; CUDA is proprietary closed-source [^682^].
- Stack Overflow has **50,000+ CUDA questions vs ~500 for ROCm** — a 100x community knowledge gap [^680^].

### Intel oneAPI / SYCL / Level Zero
- **Intel oneAPI is the cornerstone of the UXL Foundation**, hosted by Linux Foundation's JDF, with partners including Arm, Fujitsu, Google Cloud, Intel, Qualcomm, and Samsung [^567^].
- **SYCL 2020 conformance** certified for Intel oneAPI DPC++ Compiler 2025.0.0 on Intel Iris Xe Graphics, Core Ultra (Arc Graphics), and Data Center GPU Max Series [^494^].
- **Level Zero** is Intel's low-level direct-to-metal API providing: device discovery, memory allocation, peer-to-peer communication, inter-process sharing, kernel submission, async execution, synchronization, and metrics [^590^] [^591^].
- SYCL explicitly **disclaims performance portability** in its specification — performance varies by up to **40x depending on abstraction choice, memory model, and backend** [^502^]. Work-group kernels achieve up to 71% of theoretical FP64 peak; basic data-parallel kernels consistently deliver worst performance.
- **oneAPI DPC++ has plugins for NVIDIA and AMD GPU targets**, making it the most complete cross-vendor SYCL implementation [^500^].
- llama.cpp SYCL backend is primarily designed for Intel GPUs; cross-platform capabilities enable NVIDIA support with limited AMD support [^500^].

### Apple Metal / MPS / MLX
- **MLX framework** is purpose-built for Apple Silicon, using unified memory natively. Delivers **10-25% faster inference than llama.cpp/Ollama** on same Mac hardware [^495^].
- On M4 Max 64GB: MLX achieves **68 tok/s** for Llama 3.2 7B vs Ollama 58 tok/s and llama.cpp 55 tok/s [^495^].
- **MLX provides NumPy-like array framework** optimized for Apple's unified memory; designed by Apple's ML researchers and fully open source [^592^].
- **Apple Neural Engine (ANE)** delivers up to 38 TOPS (INT8) across 16 cores on M4, but actual fp16 throughput is ~19 TFLOPS [^586^]. ANE remains a "dark accelerator" for LLMs — no public framework supports LLM training on ANE.
- **Orion** (MIT licensed) is the first open system combining direct ANE execution, compiler pipeline, and multi-step training, achieving **170+ tokens/s for GPT-2 124M inference** on M4 Max [^586^].
- ANE has **32 MB SRAM performance cliff** (30% throughput drop when exceeded), ~0.095ms dispatch overhead, and ~119 compilation-per-process limit [^586^].
- CoreML operates as a black-box scheduler deciding at runtime whether to dispatch to CPU, GPU, or ANE — developers cannot force ANE execution [^586^].

### SYCL as Cross-Platform Model
- **Best current cross-platform abstraction** for heterogeneous computing, despite the 40x performance variance caveat [^502^].
- Single-source C++17 programming model targeting CPUs, GPUs, and FPGAs [^500^].
- DPC++ (Data Parallel C++) is Intel's SYCL implementation; **AdaptiveCpp** (formerly hipSYCL) provides alternative implementation supporting NVIDIA, AMD, Intel, and CPUs.
- Key memory models: USM (Unified Shared Memory) vs buffer-accessors — USM preferred for productivity but with performance tradeoffs [^502^].

### OpenCL Status 2025-2026
- **OpenCL 3.1 released May 2026** by Khronos, graduating widely-deployed capabilities into core spec including SPIR-V ingestion [^531^].
- OpenCL ecosystem growing with implementations from multiple silicon vendors, particularly in mobile and embedded markets [^531^].
- Higher-level frameworks (SYCL, chipStar) increasingly target OpenCL as acceleration backend [^531^].
- Layered implementations of OpenCL over Vulkan and DirectX 12 widening cross-platform availability [^531^].
- AMD dropped PAL OpenCL driver for consumers in 2020; Mesa's Rusticl provides modern OpenCL 3.0 implementation; ROCm includes its own OpenCL runtime [^519^].
- **Generally slower than vendor-specific solutions** (ROCm/HIP for AMD, CUDA for NVIDIA) [^519^].

### Vulkan Compute
- **Vulkan compute shaders are competitive with/sometimes beat ROCm on AMD GPUs** for LLM inference; llama.cpp Vulkan backend actively developed [^633^].
- Flash Attention implemented as custom Vulkan shader using cooperative matrix extensions (abstraction for tensor cores) [^633^].
- Vulkan is **driver-sensitive** with behavioral discrepancies across vendors; lacks tooling on NVIDIA Nsight level [^633^].
- Best use case: **cross-vendor inference on AMD/Intel GPUs** where CUDA/ROCm are unavailable; mobile inference on Android [^631^].
- SPIR-V is the standardized intermediate representation consumed by Vulkan, enabling cross-compilation from GLSL, HLSL, C++, and Rust [^526^].
- **Microsoft announced plans (September 2024) to adopt SPIR-V** as Direct3D interchange format beginning Shader Model 7 [^526^].

### Apache TVM
- TVM is a **hardware-agnostic ML compiler stack** ingesting models from PyTorch, TensorFlow, ONNX and generating optimized code for CPUs, GPUs (CUDA, Metal, Vulkan, OpenCL), and specialized accelerators [^521^] [^523^].
- Key components: Relay (graph-level optimization), AutoTVM (ML-based cost models for schedule search), and a growing list of backends.
- **Unity/Relax** is the next-generation IR with heterogeneous execution support via `VDevice` abstraction for targeting multiple devices [^628^].
- End-to-end speedups of **1.2-3.8x** across standard and exotic architectures, surpassing vendor-tuned baselines [^523^].
- FPGA backend for Vanilla DLA implemented in under 2,000 lines of Python, achieving 40x acceleration on conv layers [^523^].
- **Performance portability caveat**: AutoTVM requires 2,000 trials per operator, adding compilation overhead [^523^].

### MLIR
- **MLIR (Multi-Level Intermediate Representation)** is an open-source compiler infrastructure sub-project of LLVM, developed by Chris Lattner at Google in 2018 [^530^].
- Supports multiple abstraction levels via **dialects** — tensor/lin ops at high level, GPU/TPU/FPGA at low level — with progressive lowering between them [^518^] [^522^].
- Used in TensorFlow/XLA, IREE, torch-mlir, TPU-MLIR, and Mojo [^530^].
- **IREE** is an end-to-end ML compiler/runtime built entirely on MLIR, compiling models from TensorFlow/TFLite to optimized executables for CPUs, GPUs, and accelerators [^530^].
- **torch-mlir** integrates MLIR into PyTorch ecosystem with Torch and TorchConversion dialects [^530^].
- Five-stage pass pipeline (C1-C5) enables incremental compiler construction; nearly same pipelines reused across different hardware up to mid-level stages [^522^].

### rCUDA (Lessons for Design)
- **rCUDA** (remote CUDA) was an academic GPU virtualization project supporting **CUDA 9.2** (not 2.3 as mentioned in context — that may refer to very early version) [^655^].
- Architecture: distributed client-server — client middleware intercepts CUDA calls and forwards to server owning the GPU [^655^].
- Supports TCP/IP and InfiniBand/RoCE (with RDMA) interconnects; achieves near-native performance [^655^].
- **rCUDA is proprietary software** distributed free under specified terms; GVirtuS is open-source Apache 2.0 alternative [^664^].
- **Key limitation**: Only supports CUDA Runtime API (not graphics/OpenGL/Direct3D); partial UVM support [^655^].
- **Lessons**: API remoting introduces latency overhead that compounds at scale; requires matching CUDA versions exactly; security/isolation via GPU context per client. For a modern design, kernel-mode interception or library preloading (like HAMi) is more practical than TCP remoting.

### HAMi (CNCF GPU Sharing)
- **HAMi** is a CNCF Sandbox project for heterogeneous AI computing virtualization — GPU sharing, flexible scheduling, and monitoring in Kubernetes [^558^] [^561^].
- Virtualization via **HAMi-core**: user-space CUDA API interception using LD_PRELOAD [^558^].
  - **Memory limiting**: Intercepts `cuMemAlloc*` calls, tracks usage in shared memory, denies if over quota, fakes `cuMemGetInfo_v2` to reflect virtual quota.
  - **Compute limiting**: Background thread polls GPU utilization via NVML every ~120ms, adjusts global token counter representing "virtual CUDA cores", kernel launches consume tokens.
- **Why HAMi vs alternatives**: Time slicing lacks isolation; MPS lacks memory isolation; MIG only on expensive cards with fixed templates; vGPU requires licensing + VM [^558^].
- Supports **NVIDIA, domestic GPUs (Ascend, Cambricon, Hygon, Iluvatar, MetaX, Moore Threads), AMD planned** [^561^] [^663^].
- v2.9.0 latest release; integrates with Volcano, Koordinator, KAI-scheduler [^561^].
- HAMi presented at KubeCon Europe 2025, KubeCon China 2025, AI_dev (Linux Foundation) [^559^].

### NVIDIA MIG (Multi-Instance GPU)
- **Hardware-level partitioning** available on A100, A30, H100, H200, GH200, B200 GPUs [^556^] [^562^].
- H100 architecture: 7 compute slices + 8 memory slices (80GB) [^216^].
- **MIG profiles** (H100 80GB): 1g.10gb (7 instances), 1g.20gb (4 instances), 2g.20gb (3 instances), 3g.40gb (2 instances), 4g.40gb (1 instance), 7g.80gb (full GPU) [^557^].
- **GPU Instance (GI)**: Primary partition with dedicated memory paths and L2 cache — hard isolation. **Compute Instance (CI)**: Further subdivision of SM resources within a GI — soft isolation sharing memory/copy engines [^216^].
- MIG prevents "noisy neighbor" problem by physically partitioning L2 cache [^216^].
- **Limitations**: No P2P/NVLink between MIG instances on same GPU; no distributed training within partitioned device; reconfiguration requires GPU reset [^562^].
- Managed via `nvidia-smi`, NVML APIs, NVIDIA GPU Operator, or MIG-Adapter [^556^] [^560^].

### GPU Passthrough & SR-IOV
- **GPU Passthrough (DDA - Discrete Device Assignment)**: Assigns whole physical GPU exclusively to one VM. Maximum performance but no sharing [^659^].
- **SR-IOV (Single-Root I/O Virtualization)**: Hardware-enforced isolation dividing GPU into isolated fractions. Each VM gets dedicated resources. Used by both NVIDIA (under the hood for vGPU) and AMD (primary interface) [^659^] [^662^].
- **AMD approach**: GIM (GPU Instance Manager) driver creates Virtual Functions (VFs) via standard PCIe SR-IOV; VFs passed through with managed passthrough. Described as "by far the easiest to implement" of the three modes [^662^].
- **NVIDIA approach**: Layered abstraction — create MIG instances, associate with VFs, set vGPU type via sysfs [^662^].
- GPU-P (GPU Partitioning) in Windows Server 2025 Hyper-V supports live VM mobility and failover clustering; DDA does not [^659^].

### NVIDIA vGPU
- **Per-CCU subscription pricing**: Virtual Applications $10/year, Virtual PC $50/year, RTX Virtual Workstation $250/year [^661^].
- Requires **NVIDIA AI Enterprise (NVAI) license** for compute workloads [^662^].
- Combines MIG hardware partitioning with SR-IOV for VM-level GPU sharing [^662^].
- Significant licensing cost is a barrier to adoption; HAMi specifically designed to avoid this [^558^].

### GPUDirect RDMA
- **GPUDirect RDMA** enables NICs/FPGAs to perform direct DMA to/from GPU memory, bypassing CPU and system memory entirely [^563^] [^569^].
- Provides **10x better performance** vs CPU-mediated transfers by eliminating system memory copies [^569^].
- Requires: GPU and RDMA device on same PCIe root complex; pinned buffer registration; compatible drivers (`nvidia-peermem` or `nv_peer_mem` module) [^563^] [^576^].
- **GPUDirect Storage** enables direct data path between NVMe/NVMe-oF storage and GPU memory [^569^].
- Modern implementations include device-initiated networking (GPU posts RDMA operations directly) via NCCL GIN, GPUVM [^563^].

### AMD Infinity Fabric
- AMD's scalable interconnect linking CPUs, GPUs, APUs with high-bandwidth, low-latency connectivity [^566^].
- **MI300A**: Up to 128 GB/s per link; MI250x has heterogeneous link configurations (quad/dual/single) [^566^].
- **RCCL delivers 88-90 GB/s** for large messages; MPI vs RCCL tradeoffs depend on message size (MPI wins <4KB, RCCL 5-38x lower latency for large messages) [^566^].
- **Direct kernel access** achieves 103-104 GB/s (81% of Infinity Fabric peak on MI300A) [^566^].
- **MI300X/MI355X**: Fully connected mesh with 7 high-speed links per card; ~153 GB/s per link pair, ~1.075 TB/s total peer-to-peer bandwidth [^568^].
- Peer Memory Direct works via PCIe P2P for GPU-NIC direct transfer; requires PCIe ACS disabled on switch [^571^].

### Intel Xe Link
- Intel Xe Link edge connector features **six 53 Gbps SerDes ports** for all-to-all connections between 2 or 4 cards [^690^].
- Present on Intel Data Center GPU Max series (Ponte Vecchio); supports CXL 1.1 [^624^].
- Max 1100: Three 16GB HBM2e stacks delivering 1.6 TB/s peak bandwidth [^690^].
- Xe Link enables GPU-to-GPU communication without host CPU involvement.

### Apple Neural Engine
- Present in Apple Silicon since A11 Bionic (2017); M4 generation delivers **up to 38 TOPS INT8** across 16 cores [^586^].
- **Over 2 billion active Apple devices** carry ANE hardware [^586^].
- CoreML is the only public ANE interface; operates as black-box scheduler — developers cannot force ANE execution [^586^].
- ANE optimized for small-batch inference, not LLM workloads; MLX and other frameworks bypass ANE entirely, targeting GPU via Metal [^586^].
- Key constraints: 32MB SRAM limit, fixed computation shapes, compile-time weight baking, no public training API [^586^] [^588^].

### UXL Foundation
- **Unified Acceleration (UXL) Foundation**: Linux Foundation-hosted open standard accelerator programming model [^572^] [^573^].
- Steering members: Arm, Fujitsu, Google Cloud, Imagination Technologies, Intel, Qualcomm, Samsung, VMware [^567^].
- Effectively an evolution of Intel's oneAPI initiative [^572^].
- Goals: multi-architecture multi-vendor software ecosystem; unify heterogeneous compute around open standards; expand open-source projects for accelerated computing [^573^].
- **Spec release targeted for Q4 2024** with implementations already in use [^572^].
- Codeplay (acquired by Intel 2022) provides SYCL expertise central to UXL [^572^].

### GPU Pool Scheduling
- **Kubernetes DRA graduated to GA in v1.34 (September 2025)** [^685^]. DRA is now stable, enabled by default, with community commitment to avoid breaking changes.
- DRA replaces opaque integer GPU allocation (`nvidia.com/gpu: 1`) with structured resource claims using CEL expressions — e.g., request GPUs by memory capacity, compute capability, product name [^627^] [^632^].
- **NVIDIA donated DRA driver to CNCF** at KubeCon Europe 2026 [^627^].
- **KAI Scheduler** (open-sourced from Run:ai, Apache 2.0, 2025): fractional GPU allocation, topology-aware scheduling, hierarchical queue management [^589^].
- **Kueue**: Kubernetes-native job queuing with cluster-wide queues, tenant quotas, cohort borrowing, atomic admission control [^589^].
- **Exostellar AIM** (launched November 2025): Industry's first unified AI infrastructure management for heterogeneous compute — manages NVIDIA, AMD, Qualcomm, Intel from single platform [^657^].
- **HAMi** signals structural shift: GPUs moving from proprietary scheduling to open scheduling, analogous to CNI for networking [^416^].
- CDI (Container Device Interface) now default in NVIDIA GPU Operator 25.10.0 — standardizes device injection [^416^].

## Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **NVIDIA** | CUDA ecosystem, MIG, GPUDirect, vGPU, KAI Scheduler, DRA driver — dominant GPU compute vendor |
| **AMD** | ROCm/HIP, Infinity Fabric, SR-IOV GPU virtualization, MI300X/MI325X/MI355X — primary CUDA challenger |
| **Intel** | oneAPI, SYCL/DPC++, Level Zero, UXL Foundation, Data Center GPU Max/Xe Link — standards-driven approach |
| **Apple** | Metal/MPS, MLX framework, ANE, unified memory — closed but highly optimized ecosystem |
| **Khronos Group** | OpenCL, SYCL, SPIR-V, Vulkan standards — cross-platform standardization body |
| **Apache TVM** | Hardware-agnostic ML compiler stack — open-source deployment flexibility |
| **MLIR/LLVM** | Compiler infrastructure substrate — used by TensorFlow/XLA, IREE, torch-mlir |
| **HAMi/CNCF** | Open-source GPU sharing with API interception — Kubernetes-native virtualization |
| **UXL Foundation** | Linux Foundation open accelerator standard — Intel-led CUDA alternative |
| **Exostellar** | Commercial heterogeneous GPU orchestration — AIM platform for multi-vendor management |
| **Run:ai/NVIDIA** | KAI Scheduler — commercial-grade GPU workload scheduling, now open-source |
| **Kubernetes SIG-Scheduling** | DRA, Device Plugin, CDI — de facto orchestration layer for GPU workloads |

## Trends & Signals

- **ROCm rapidly maturing but "CUDA gap" persists**: Gap narrowed from 2-3x to 10-30% for common frameworks (PyTorch, vLLM) but widens with scale and concurrency [^684^] [^686^].
- **Kubernetes DRA is the new foundation**: Graduated GA in v1.34; replaces decade-old device plugin model; enables attribute-based GPU selection, fractional allocation, topology awareness [^685^] [^687^].
- **SYCL best cross-platform abstraction despite 40x performance variance** — most practical path for write-once-run-anywhere GPU code [^502^].
- **Vulkan compute emerging as AMD inference alternative**: Sometimes exceeds ROCm performance (up to 50% in some LLM benchmarks), better consumer GPU support [^519^] [^633^].
- **API interception (HAMi) becoming production-viable**: CUDA API remoting at library level (not kernel/TCP) is practical for Kubernetes multi-tenancy [^558^].
- **Open standards gaining traction**: OpenCL 3.1 (May 2026), UXL Foundation spec maturation, SPIR-V adopted by Microsoft for Direct3D [^531^] [^526^].
- **Apple MLX demonstrates unified memory advantage**: 10-25% faster than cross-platform alternatives by avoiding memory copies [^495^].
- **GPU virtualization moving from hardware to software**: MIG provides strong isolation but limited flexibility; HAMi provides flexible sharing with soft isolation; trend is toward software-defined GPU [^558^] [^562^].
- **Heterogeneous GPU pools becoming reality**: Exostellar AIM, HAMi multi-vendor support, DRA device classes — scheduling across NVIDIA+AMD+Intel from single control plane now possible [^657^] [^663^].

## Controversies & Conflicting Claims

- **ROCm "practically closed the gap" vs. persistent CUDA gap**: Some claim AMD has caught up (ROCm 7.x, PyTorch native support) [^688^]; quantitative benchmarks show 10-30% gap for single-GPU, growing to 46%+ at multi-GPU scale [^684^]. Reality: framework parity is close, but library/tooling/community gap remains significant.
- **ANE usefulness for LLMs**: Some enthusiasts demonstrate viable ANE inference (170 tok/s GPT-2) [^586^]; others note ANE's low memory bandwidth, 512-token context limits, and ~2.3ms per-dispatch IOSurface overhead makes GPU (via MLX) generally superior [^587^].
- **vGPU licensing vs. open-source alternatives**: NVIDIA vGPU requires expensive licensing ($50-250/CCU/year) [^661^]; HAMi and SR-IOV approaches avoid licensing but provide weaker isolation guarantees [^558^] [^662^].
- **SYCL performance portability claim**: SYCL spec explicitly disclaims performance portability, yet marketing suggests cross-vendor deployment. Academic benchmarks confirm 40x variance across backends [^502^].
- **rCUDA viability**: Academic project achieving near-native performance [^655^], but proprietary license, limited CUDA version support, and TCP/IP overhead make it unsuitable for modern production. HAMi's LD_PRELOAD approach is the evolution of this concept.
- **GPU passthrough vs. virtualization**: Passthrough (DDA) gives maximum performance but no sharing; SR-IOV enables sharing but with vendor-specific limitations; MIG provides best isolation but only on expensive datacenter GPUs [^659^] [^662^].

## Recommended Deep-Dive Areas

1. **Kubernetes DRA driver development**: DRA is the future of GPU scheduling in Kubernetes. Writing a custom DRA driver for our compute engine would provide first-class K8s integration. Key: DRA v1 API is stable as of 1.34 [^685^].

2. **SYCL + oneAPI as abstraction layer**: Despite 40x performance variance, SYCL is the only vendor-agnostic approach covering NVIDIA (via Codeplay plugin), AMD (via AdaptiveCpp), Intel (native), and CPU. Deep dive on AdaptiveCpp vs DPC++ for multi-vendor deployment [^502^].

3. **HAMi-core CUDA interception architecture**: LD_PRELOAD approach with memory/compute limiting is proven in production. Extending to AMD (ROCm API interception) and Intel (Level Zero) would create unified virtualization layer. Key challenge: vendor API differences [^558^].

4. **MLX unified memory model for Apple Silicon**: Zero-copy unified memory between CPU/GPU/ANE is architectural advantage. Design implications for heterogenous compute: can other vendors (AMD APU, Intel Core Ultra) replicate this? [^495^] [^586^].

5. **TVM Relax heterogeneous execution**: TVM's VDevice abstraction and Unity compiler pipeline provide a principled way to distribute model subgraphs across different accelerators [^628^]. Integration with DRA scheduling could enable automatic workload placement.

6. **AMD Infinity Fabric vs NVIDIA NVLink topology awareness**: For multi-GPU workloads, interconnect topology is the binding constraint. Scheduling must be topology-aware — placing communicating jobs on connected GPUs [^566^] [^568^].

7. **OpenCL 3.1 + SPIR-V as lowest-common-denominator**: With Microsoft adopting SPIR-V and OpenCL 3.1 maturing, this may be the most portable (if not highest performance) target for universal GPU compute [^531^] [^526^].

## Raw Evidence Log

### Evidence 1: CUDA Compatibility Documentation
Claim: CUDA provides three-tier compatibility model (backward, minor version, forward) with specific driver requirements [^652^].
Source: NVIDIA Official Documentation (CUDA Compatibility)
URL: https://docs.nvidia.com/deploy/cuda-compatibility/
Date: Current
Excerpt: "CUDA Compatibility includes Minor Version Compatibility, available starting with CUDA 11, which allows applications built within the same major CUDA release family to run on a sufficiently new driver, with some feature limitations. It also includes Forward Compatibility, which uses the cuda-compat-<major>-<minor> package to allow applications built with a newer toolkit to run on older base drivers across major release families, subject to platform and GPU support."
Context: Essential for designing multi-CUDA-version clusters
Confidence: High

### Evidence 2: ROCm 7.2.0 Release Status
Claim: ROCm 7.2.0 shipped January 2026 with 4.6x inference improvement [^682^] [^519^].
Source: Kunal Ganglani Blog / AMD ROCm Documentation
URL: https://www.kunalganglani.com/blog/amd-rocm-vs-cuda-local-ai-open-source-guide
Date: March 2026
Excerpt: "The current production release is ROCm 7.2.0, which shipped January 2026, with version 7.11.0 available as a technology preview... ROCm 7.0: 4.6x inference performance improvement"
Context: ROCm is rapidly evolving; version numbering follows AMD's scheme
Confidence: High

### Evidence 3: SYCL 40x Performance Variance
Claim: SYCL performance varies by over 40x depending on abstraction choice, memory model, and backend [^502^].
Source: IEEE TPDS / arXiv (Evaluating SYCL as a Unified Programming Model)
URL: https://arxiv.org/html/2604.16043v1
Date: April 2026
Excerpt: "SYCL explicitly disclaims performance portability in its specification, and our results validate that decision. Performance varies widely depending on abstraction choice, memory model, and backend, with differences reaching over 40x in some cases... On GPUs (NVIDIA A100 and AMD MI210), the basic data-parallel kernels consistently delivered the worst performance across all platforms."
Context: Critical for setting realistic expectations on SYCL abstraction layer
Confidence: High

### Evidence 4: HAMi Architecture
Claim: HAMi uses LD_PRELOAD CUDA API interception with memory limiting (cuMemAlloc*) and compute limiting (NVML token-based) [^558^].
Source: Reddit / HAMi Maintainer Post
URL: https://www.reddit.com/r/kubernetes/comments/1kvy06i/seeking_advice_cncf_sandbox_project_hami_why/
Date: August 2025
Excerpt: "HAMi's virtualization layer is implemented in HAMi-core, a user-space CUDA API interception library. It works like this: LD_PRELOAD hijacks CUDA calls and tracks resource usage per process. Memory limiting: Intercepts memory allocation calls (cuMemAlloc*) and checks against tracked usage in shared memory. Compute limiting: A background thread polls GPU utilization (via NVML) every ~120ms and adjusts a global token counter representing 'virtual CUDA cores'."
Context: Proven approach for GPU sharing without hardware support
Confidence: High

### Evidence 5: MIG Hardware Isolation
Claim: MIG provides hardware-level isolation of compute, memory, and cache resources; prevents noisy neighbor via physical L2 cache partitioning [^216^].
Source: Medium (Sagar Parmar) - Practical Guide to GPU Partitioning with MIG
URL: https://sagar-parmar.medium.com/a-practical-guide-to-gpu-partitioning-with-mig-on-on-prem-servers-and-kubernetes-797ccea7e1c7
Date: February 2026
Excerpt: "Hard separation in MIG ensures that workloads in one MIG instance cannot use SMs or access memory assigned to another, delivering strong isolation, predictable performance, and enterprise-grade security. MIG also prevents the noisy neighbour problem by physically partitioning the L2 cache."
Context: Gold standard for GPU isolation; template-based approach limits flexibility
Confidence: High

### Evidence 6: CUDA Gap Quantification
Claim: Despite 32% theoretical TFLOPS advantage, MI300X achieves 37-66% of H100 performance; gap increases from 29% (2 GPU) to 46% (8 GPU) [^684^].
Source: AIMultiple (Cem Dilmegani)
URL: https://aimultiple.com/cuda-vs-rocm
Date: January 2026
Excerpt: "Despite MI300X's clear theoretical advantage, NVIDIA maintains a growing throughput lead as GPU count increases. CUDA gap scores in the 61-78 range reflect how NVIDIA's software stack unlocks performance far beyond hardware expectations... At 512 concurrent users: H100: +67.0% more throughput, H200: +37.4%, B200: +77.9%."
Context: Software ecosystem advantage compounds at scale
Confidence: High

### Evidence 7: MLX Unified Memory Performance
Claim: MLX delivers 10-25% faster inference than llama.cpp/Ollama on same Apple Silicon hardware [^495^].
Source: Local AI Master
URL: https://localaimaster.com/blog/apple-silicon-ai-buying-guide
Date: May 2026
Excerpt: "MLX delivers 10-25% faster inference than llama.cpp/Ollama on the same Mac hardware... MLX is faster because it was designed from scratch for unified memory. It avoids unnecessary memory copies and uses Metal compute shaders optimized for the specific GPU core counts in each chip."
Context: Unified memory architecture is a genuine performance advantage
Confidence: Medium

### Evidence 8: Orion ANE Direct Programming
Claim: Orion is the first open system for direct ANE execution + training; achieves 170 tok/s GPT-2 124M on M4 Max; delta compilation reduces recompile 8.5x [^586^].
Source: arXiv (Orion: Characterizing and Programming Apple's Neural Engine)
URL: https://arxiv.org/html/2603.06728v1
Date: March 2026
Excerpt: "On an M4 Max, Orion achieves 170+ tokens/s for GPT-2 124M inference and demonstrates stable training of a 110M-parameter transformer... Delta compilation reduces recompilation from 4,200ms to 494ms per step (8.5x), yielding a 3.8x total training speedup."
Context: ANE programming is possible but requires private APIs; CoreML is insufficient
Confidence: High

### Evidence 9: DRA Graduated GA Kubernetes 1.34
Claim: Kubernetes DRA core graduated to GA in v1.34 (September 2025); stable, enabled by default, no breaking changes commitment [^685^].
Source: Kubernetes Official Blog
URL: https://kubernetes.io/blog/2025/09/01/kubernetes-v1-34-dra-updates/
Date: September 2025
Excerpt: "The headline feature of the v1.34 release is that the core of DRA has graduated to General Availability. With the graduation to GA, DRA is stable and will be part of Kubernetes for the long run. The community can still expect a steady stream of new features being added to DRA over the next several Kubernetes releases, but they will not make any breaking changes to DRA."
Context: DRA is now the standard for GPU resource management in Kubernetes
Confidence: High

### Evidence 10: rCUDA Architecture
Claim: rCUDA supports CUDA 9.2, uses client-server TCP/IP or IB/RoCE with RDMA, achieves near-native performance [^655^].
Source: RiuNet (Universitat Politecnica de Valencia)
URL: https://riunet.upv.es/bitstream/3fecd1f2-d0c4-4182-829d-91bfdb40cc4b/download
Date: Unknown (academic paper)
Excerpt: "rCUDA supports version 9.2 of CUDA, being binary compatible with it... rCUDA provides specific support for different interconnects. Currently, two modules are available: one intended for TCP/IP compatible networks, and another one specifically designed for the InfiniBand and RoCE interconnects, which make use of RDMA."
Context: Academic project showing API remoting is viable but version-limited
Confidence: Medium

### Evidence 11: Vulkan Compute Competitive with ROCm
Claim: Vulkan sometimes exceeds ROCm by up to 50% in LLM inference; driver-sensitive with behavioral discrepancies [^519^] [^633^].
Source: AMD GPU Acceleration Technologies Explained / FOSDEM 2026 Notes
URL: https://gist.github.com/danielrosehill/8793e2028ef4bd08c6ca955a38b40e5b / https://philpax.me/notes/other-peoples-talks/fosdem-2026/vulkan-api-for-machine-learning-competing-with-cuda-and-rocm-in-llamacpp/
Date: November 2025 / January 2026
Excerpt: "In some LLM inference benchmarks, Vulkan outperforms ROCm by up to 50%... Vulkan is very driver-sensitive; all kinds of behavioural discrepancies and incompatibilities. Some drivers are worse than others."
Context: Vulkan is viable cross-vendor fallback, especially for inference
Confidence: Medium

### Evidence 12: OpenCL 3.1 Release
Claim: OpenCL 3.1 released May 2026 with SPIR-V ingestion in core spec, layered implementations over Vulkan/DirectX 12 [^531^].
Source: Khronos Blog
URL: https://www.khronos.org/blog/opencl-3.1-is-here
Date: May 2026
Excerpt: "On the eve of IWOCL 2026, the Khronos OpenCL Working Group has released OpenCL 3.1, bringing widely deployed, field-proven capabilities into the core specification to expand functionality, including SPIR-V ingestion... The open-source compiler and runtime ecosystem around OpenCL also continues to mature with layered implementations of OpenCL over Vulkan and DirectX 12."
Context: OpenCL is not dead; being revitalized as backend for higher-level frameworks
Confidence: High

### Evidence 13: UXL Foundation as CUDA Alternative
Claim: UXL Foundation (Linux Foundation) brings together Arm, Fujitsu, Google Cloud, Intel, Qualcomm, Samsung to build open accelerator standard based on oneAPI [^567^] [^572^].
Source: Intel / The Register
URL: https://www.intel.com/content/www/us/en/developer/articles/technical/oneapi-a-viable-alternative-to-cuda-lock-in.html / https://www.theregister.com/software/2024/03/26/uxl-foundation-readying-cuda-alternative-for-this-year/740329
Date: June 2025 / March 2024
Excerpt: "Since its inception, the oneAPI programming model has garnered considerable momentum and is a cornerstone of the Unified Acceleration Foundation (UXL). Hosted by the Linux Foundation's Joint Development Foundation (JDF), UXL brings together ecosystem participants to establish an open accelerated computing standard."
Context: Standards-based alternative to CUDA; backed by major vendors (not NVIDIA)
Confidence: High

### Evidence 14: NVIDIA vGPU Pricing
Claim: NVIDIA vGPU licensing costs $50-250 per CCU per year depending on tier [^661^].
Source: NVIDIA Official Documentation
URL: https://docs.nvidia.com/vgpu/packaging-pricing-licensing-guide/latest/index.html
Date: Current
Excerpt: "NVIDIA Virtual Applications: $10 per CCU subscription; NVIDIA Virtual PC: $50 per CCU subscription; NVIDIA RTX Virtual Workstation: $250 per CCU subscription"
Context: Significant licensing cost motivates open-source alternatives
Confidence: High

### Evidence 15: AMD SR-IOV GPU Virtualization
Claim: AMD SR-IOV is "by far the easiest to implement" — standard PCIe mechanism, no licensing, managed passthrough [^662^].
Source: CloudRift Blog
URL: https://www.cloudrift.ai/blog/gpu-virtualization-qemu-kvm-nvidia-amd
Date: Unknown
Excerpt: "Of the three virtualization modes we support, AMD SR-IOV was by far the easiest to implement. Standard PCIe mechanism, managed passthrough, no nvidia-smi invocations, no licensing hurdles, all images are easy to download and install, and it supports fractional GPU allocation out of the box."
Context: AMD's open approach to GPU virtualization is operationally simpler
Confidence: Medium

### Evidence 16: TVM Heterogeneous Execution Design
Claim: TVM Relax introduces VDevice abstraction for heterogeneous execution; reuses Target, enables multi-device compilation [^628^].
Source: Apache TVM RFC
URL: https://discuss.tvm.apache.org/t/rfc-unity-relax-heterogeneous-execution-for-relax/14670
Date: April 2023
Excerpt: "VDevice, a subclass of GlobalInfo, will be introduced, it denotes the data storage representation during compilation and outlines how to compile and compute it... To help create VDevice in the IR, we will introduce a new syntactic sugar, R.VDevice."
Context: TVM's compiler infrastructure supports multi-device targeting
Confidence: High

### Evidence 17: Kubernetes DRA NVIDIA Donation
Claim: NVIDIA donated DRA driver to CNCF at KubeCon Europe 2026, replacing decade-old device plugin [^627^].
Source: Spheron Network Blog
URL: https://www.spheron.network/blog/kubernetes-gpu-orchestration-2026/
Date: April 2026
Excerpt: "At KubeCon Europe 2026, NVIDIA donated its Dynamic Resource Allocation (DRA) driver to CNCF. That single event changes what Kubernetes GPU scheduling looks like for platform engineers... If you have been running GPU workloads on Kubernetes using the NVIDIA device plugin, you are working with tooling that is nearly a decade old."
Context: Significant ecosystem shift toward standardized GPU scheduling
Confidence: High

### Evidence 18: GPUDirect RDMA Architecture
Claim: GPUDirect RDMA enables NIC-to-GPU DMA bypassing CPU; 10x performance improvement; requires same PCIe root complex [^563^] [^569^].
Source: NVIDIA Official / Emergent Mind
URL: https://developer.nvidia.com/gpudirect / https://www.emergentmind.com/topics/gpu-direct-rdma
Date: Current / December 2025
Excerpt: "GPUDirect RDMA provides direct communication between NVIDIA GPUs in remote systems. This eliminates the system CPUs and the required buffer copies of data via the system memory, resulting in 10X better performance."
Context: Critical for high-performance distributed GPU workloads
Confidence: High

### Evidence 19: Infinity Fabric Performance
Claim: Infinity Fabric on MI300A delivers 128 GB/s per link; RCCL 88-90 GB/s for large messages; direct kernel access 103-104 GB/s [^566^].
Source: Emergent Mind (Infinity Fabric)
URL: https://www.emergentmind.com/topics/infinity-fabric
Date: November 2025
Excerpt: "Infinity Fabric delivers communication rates up to 128 GB/s per link (MI300A)... RCCL delivers 88-90 GB/s for large messages... Direct kernel access (STREAM-type or custom kernels) can achieve near maximum bandwidth for remote memory."
Context: AMD's interconnect is competitive but topology is more complex than NVLink
Confidence: Medium

### Evidence 20: Intel Xe Link Specification
Claim: Intel Xe Link has six 53 Gbps SerDes ports for all-to-all connections between 2-4 cards; Max 1100 has 1.6 TB/s HBM2e bandwidth [^690^].
Source: Intel Official Datasheet
URL: https://cdrdv2-public.intel.com/817799/817799_Intel%20Data%20Center%20GPU%20Max%201100%20Datasheet_Rev_1_0.pdf
Date: Current
Excerpt: "The Intel Xe Link edge connector features six 53 Gbps Serializer/Deserializer (SerDes) ports for all-to-all connections between two or four cards... Three 16 GB HBM2e memory stacks (3.2 GT/s per HBM2e stack) (1.6 TB/s peak BW)."
Context: Intel's GPU interconnect is less performant than NVLink/Infinity Fabric
Confidence: High
