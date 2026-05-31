# Facet: GPU Virtualization & Distributed Compute Across Heterogeneous GPUs

## Key Findings

### rCUDA — Remote CUDA Virtualization
- **rCUDA** intercepts CUDA API calls at the client side and forwards them to a remote GPU server via a client-server architecture, making GPU virtualization transparent to programmers [^1^]. The client emulates local GPUs while the server executes actual GPU code and reports status [^1^].
- Performance over high-speed interconnects (InfiniBand) shows negligible overhead for many workloads; remote GPUs can be faster than local CPU execution for compute-intensive tasks [^1^].
- In a 100-node cluster study, rCUDA enabled significant energy savings: reducing from 100 GPUs to 10 GPUs still allowed all jobs to run, saving 17.4 kW (21.6% reduction) while maintaining throughput through GPU sharing [^1^].
- rCUDA integrates with SLURM to schedule "virtual GPUs" — allowing GPUs to be assigned as exclusive or shared resources, decoupling GPUs from physical nodes and increasing overall cluster throughput [^2^].
- Key limitations: no zero-copy support for virtualized devices, requires plain C API (no CUDA C extensions like `kernel<<<>>>` syntax), no support for precompiled libraries (CUFFT, CUBLAS), and only supports CUDA Runtime API [^1^].
- **Critical note**: rCUDA appears to be a research project from Universitat Jaume I / Universitat Politecnica de Valencia. The latest available version (rCUDA R.C. 1.0) supported only CUDA 2.3. There is **no evidence of active maintenance as of 2024-2025**, making it unsuitable for production use with modern CUDA versions.

### NVIDIA NVLink & NVSwitch — GPU Interconnect
- **NVLink 5.0** (Blackwell generation) delivers **1.8 TB/s per GPU** via 18 links at 100 GB/s each — 14x the bandwidth of PCIe Gen5 [^3^]. NVLink 6.0 (Rubin platform, expected H2 2026) will push this to **3.6 TB/s per GPU** [^4^].
- **NVSwitch** provides full crossbar connectivity, maintaining ~900 GB/s all-to-all bandwidth for 2, 4, or 8 GPUs — effectively flattening communication bottlenecks [^4^]. NVSwitch Gen4 (Blackwell) delivers 14.4 TB/s aggregate switch bandwidth [^3^].
- **NVLink-C2C** (chip-to-chip) enables die-to-die package-level interconnect, eliminating connector parasitics and re-serialization overhead. Supports Address Translation Services (ATS) allowing GPU direct CPU page table access without software copy [^5^].
- **NVLink Fusion** (announced COMPUTEX 2025) opens NVLink to third-party silicon — custom ASICs from Marvell, MediaTek, Alchip; CPUs from Fujitsu, Qualcomm. However, each node must still contain at least one NVIDIA GPU or chiplet [^6^][^7^][^8^].
- **Key limitations**: Vendor lock-in (NVIDIA-only until Fusion), complex topology design, scalability ceiling at 576 GPUs per NVLink fabric (requires InfiniBand/Ethernet beyond), high cost (hundreds of dollars per NVSwitch chip), and significant power consumption [^4^][^3^].
- **Multi-Node NVLink (MNNVL)** with IMEX service enables GPU memory export/import across OS domains for peer-to-peer communication between nodes. Kubernetes 1.32+ with GPU Operator 25.3+ required for MNNVL deployment [^3^][^9^].

### AMD ROCm — GPU Virtualization & Architecture
- **ROCm** supports virtualization for Instinct accelerators (MI300X, MI325X, MI250, MI210) and Radeon PRO GPUs via **SR-IOV** (Single Root I/O Virtualization) and **PCI passthrough** on KVM and Hyper-V hypervisors [^10^].
- **SR-IOV scheduler groups** (Engine Group Scheduling / EGS) allow the driver to subdivide a GPU into engine groups that are independently timesliced across virtual functions, enabling multiple VFs to access hardware simultaneously (available from GuC firmware v70.55.1+) [^11^].
- The **AMD Instinct MI300X** uses a fully-connected 8-GPU topology via **Infinity Fabric links** (XGMI), with each GPU having 192 GB HBM3 and seven high-bandwidth links forming a mesh [^12^][^13^].
- **RCCL** (ROCm Collective Communications Library) is a fork of NCCL, supporting all-reduce, all-gather, broadcast, etc. Performance reaches ~70% of theoretical peak bandwidth on MI300X (vs ~85% for NCCL on H100), with 8-GPU collective bandwidth reaching 448 GB/s [^14^][^13^].
- ROCm requires CPUs supporting PCIe atomics (Zen+ and Intel Haswell or newer) [^10^].
- **Key limitation**: AMD's Infinity Fabric bandwidth per link (128 GB/s bidirectional) is significantly lower than NVLink 4.0 (900 GB/s), creating a performance gap for multi-GPU collectives [^14^].

### Intel oneAPI — Level Zero & SYCL
- **Level Zero** is Intel's low-level API for heterogeneous computing, providing explicit control for device discovery, memory allocation, peer-to-peer communication, inter-process sharing, kernel submission, and asynchronous execution. It sits below oneAPI/SYCL and supports GPUs, FPGAs, and other accelerators [^15^].
- **SYCL** (via Intel's DPC++ compiler) enables cross-platform code using standard ISO C++, with host and kernel code in the same source file. DPC++ adds SYCL support to the LLVM C++ compiler [^16^].
- Intel's open-source **Xe driver** in Linux 6.19+ enables **SR-IOV GPU virtualization** with zero proprietary licensing — a significant challenge to NVIDIA's vGPU licensing model [^17^].
- **Multi-device SVM** (Shared Virtual Memory) support coming in Linux 6.20/7.0 enables multi-GPU AI and compute workloads with Level Zero or OpenCL, important for Intel's Project Battlematrix [^18^].
- **Key limitation**: SYCL explicitly disclaims "performance portability" — performance varies widely depending on abstraction choice, memory model, and backend, with differences reaching **over 40x** in some cases [^19^]. One study found work-group kernels achieved up to 71% of theoretical FP64 peak, while basic data-parallel kernels delivered worst performance [^19^].

### Apple Metal — Unified Memory Architecture
- **Apple Silicon** uses a unified memory architecture where CPU and GPU share the same memory pool, eliminating data copies between system RAM and GPU VRAM [^20^].
- **MTLStorageMode.shared** in Metal enables system memory to be directly mapped for both CPU and GPU access, simplifying memory management and reducing overhead [^20^].
- On recent M5 chips, unified memory bandwidth reaches **up to 153 GB/s**, more than double earlier generations [^20^].
- **Zero-copy advantage**: A video frame decoded by the CPU can be processed on the GPU without copying into a GPU-dedicated memory pool — critical for inference pipelines [^20^].
- **No direct equivalent of NVLink/Infinity Fabric** for multi-GPU scaling. Apple's approach focuses on single-chip integration rather than multi-GPU interconnects.

### VirtualGL — Remote OpenGL Rendering
- **VirtualGL** is an open-source toolkit that enables remote display of OpenGL applications with full hardware-accelerated 3D rendering on server-side GPUs [^21^].
- Uses **interposition** via `LD_PRELOAD` to inject `libvglfaker.so`, intercepting GLX/EGL calls and redirecting them to a 3D-accelerated X server on the remote host [^21^].
- Captures rendered framebuffer via `glReadPixels` or Pixel Buffer Objects (PBOs) for asynchronous transfer, then compresses (JPEG/YUV) and streams to clients [^21^].
- Supports multi-user GPU sharing, stereo rendering, and integration with TurboVNC for collaborative sessions [^21^].
- Primarily useful for remote visualization workloads (scientific viz, CAD), not general-purpose compute. Does not virtualize CUDA/compute APIs.

### GPU Passthrough & SR-IOV
- **GPU passthrough** (PCI passthrough / VFIO) allows a VM direct access to a physical GPU via IOMMU. Provides near-native performance but dedicates the entire GPU to one VM [^22^].
- **SR-IOV** (Single Root I/O Virtualization) allows one physical GPU to present itself as multiple virtual functions (VFs), each assignable to a different VM. Supported by AMD MI300X/MI325X (on KVM), Intel Xe (Linux 6.19+), and limited NVIDIA datacenter GPUs [^10^][^17^][^22^].
- **NVIDIA vGPU** requires proprietary licensing and compatible hardware. SR-IOV for NVIDIA is limited to specific datacenter GPUs and requires NVIDIA AI Enterprise licensing [^22^][^23^].
- **Key limitation**: Most SR-IOV implementations are designed to dedicate entire GPUs to individual VMs rather than allowing fine-grained sharing of a single GPU across multiple VMs with compute/memory isolation [^10^].
- Intel's Xe SR-IOV with zero licensing fees represents a significant disruption to the GPU virtualization economics [^17^].

### NVIDIA vGPU & MIG (Multi-Instance GPU)
- **MIG** (Multi-Instance GPU) provides **hardware-level spatial partitioning** of the GPU die, creating up to 7 fully isolated instances, each with dedicated HBM, cache, and compute cores on A100/H100 [^24^][^25^].
- MIG instances have physically separate paths through the entire memory system (crossbar ports, L2 cache banks, memory controllers, DRAM buses) ensuring fault isolation and deterministic QoS [^26^].
- Supported on A100, A30, H100, H200, and Blackwell (GB200, B200, RTX PRO 6000) GPUs [^26^].
- **Time-slicing** (alternative to MIG) shares GPU via rapid context switching but provides no memory isolation — one process can crash others [^24^][^27^].
- **GPU utilization crisis**: Over 75% of organizations report GPU utilization below 70% at peak load; GPT-4 trained on 25,000 A100s with only 32-36% average utilization [^27^].
- MIG can reconfigure dynamically: 7 instances for daytime inference → 1 large instance at night for training [^24^].
- **Limitation**: MIG instances on the same GPU cannot communicate via P2P or NVLink, so multi-GPU training via NCCL is not supported within a single partitioned device [^28^].

### Distributed Deep Learning — DeepSpeed, FSDP, Horovod
- **DeepSpeed** (Microsoft) provides ZeRO (Zero Redundancy Optimizer) with three stages: Stage 1 (optimizer state partitioning), Stage 2 (+ gradient partitioning), Stage 3 (+ model parameter partitioning). Supports CPU/NVMe offload for training models with trillions of parameters [^29^][^30^].
- **FSDP** (Fully Sharded Data Parallel, PyTorch) shards model parameters, gradients, and optimizer states across GPUs. FSDP2 uses per-parameter sharding with DTensor, improving computation-communication overlap [^29^].
- **Horovod** (Uber) uses ring-allreduce for distributed training. Simpler than DeepSpeed but less memory-efficient for very large models.
- Training strategy by model size: <7B uses DDP/ZeRO-1; 7B-13B uses FSDP2/ZeRO-2; 30B-70B uses FSDP2/ZeRO-3 across 8 GPUs; 70B+ requires multi-node Megatron-Core TP+PP or ZeRO-3+DP [^31^].
- **Key challenge for heterogeneous GPUs**: DeepSpeed and FSDP assume homogeneous GPUs. Network speed between nodes is a major bottleneck — a user reported enormous slowdown with ZeRO-3 on 1000 MB/s network between two nodes [^30^].

### Apache TVM — Unified Compiler for Heterogeneous Hardware
- **TVM** is a full-stack compiler that combines code generation and automatic program optimization (AutoTVM) to generate kernels comparable to hand-optimized libraries [^32^].
- Supports hardware platforms including ARM CPUs, Intel CPUs, Mali GPUs, NVIDIA GPUs, and AMD GPUs [^32^].
- **Heterogeneous runtime** enables scheduling different operators across different devices (CPU, GPU, accelerator) within a single model [^32^].
- **Relay IR** provides a differentiable programming intermediate representation with end-to-end compilation from frontends (TensorFlow, ONNX, MXNet, etc.) [^33^].
- AutoTVM uses machine learning-based tuners with a transferable cost model, profiling candidate kernels on real hardware iteratively [^32^].
- **Note**: TVM primarily targets inference optimization, not training. It does not solve the problem of distributing training across heterogeneous GPUs.

### SYCL & OpenCL — Cross-Platform GPU Computing
- **SYCL** (via DPC++ / AdaptiveCpp) enables writing code once for CPUs, GPUs, and FPGAs using standard C++. A 2025 study demonstrated SYCL implementations on CPU, iGPU, dGPU (NVIDIA), and Intel FPGA simultaneously [^34^].
- SYCL achieved comparable performance to CUDA on NVIDIA GPUs while achieving similar architectural efficiency on AMD and Intel GPUs in most test cases [^35^].
- However, **performance portability remains an open problem** — performance varies by up to 40x depending on abstraction choice, memory model, and backend. Work-group kernels consistently outperformed basic data-parallel kernels [^19^].
- **OpenCL** remains relevant as a backend for SYCL implementations but has been deprecated on some platforms (e.g., AMD CPUs). Level Zero is emerging as Intel's preferred low-level backend.

### GPUDirect RDMA — Direct GPU Memory Transfer
- **GPUDirect RDMA** enables a direct data path between GPU memory and third-party peer devices (NICs, storage adapters) via PCI Express, completely bypassing host CPU and system memory [^36^][^37^].
- Eliminates the "RDMA NIC <-> system memory <-> GPU VRAM" copy path that adds significant latency and bandwidth bottlenecks [^38^].
- **NVIDIA's implementation**: Close collaboration between GPU driver and RDMA driver (Mellanox OFED) via `nvidia_p2p_get_pages` for address translation. The application passes GPU device pointers directly to `ibv_reg_mr` [^38^].
- **AMD's equivalent (ROCnRDMA)**: Uses Peer Memory Client API — when `ibv_reg_mr` receives an AMD GPU pointer, `ib_core` calls ROCnRDMA's callbacks (`acquire`, `get_pages`) to obtain physical addresses for the RDMA NIC [^38^].
- **Kernel-standardized approach (dma-buf)**: Linux kernel's `dma-buf` framework provides a vendor-neutral solution. GPU exports memory as `dmabuf_fd`, RDMA driver imports via `ibv_reg_dmabuf_mr()`. The GPU driver handles address translation in its `map_dma_buf` callback [^38^].
- GPUDirect RDMA requires both devices share the same upstream PCI Express root complex [^37^].

### UALink — Open Alternative to NVLink
- **UALink 1.0** (published April 2025) defines a low-latency, high-bandwidth interconnect supporting up to **1,024 accelerators** per fabric at 800 GB/s (x4 config) [^39^][^40^].
- UALink Consortium includes AMD, Intel, Google, Microsoft, Meta, AWS, Cisco, HPE, Apple, and others [^39^].
- UALink 2.0 (published April 2026) adds 200G Data Link/Physical Layer and in-network compute support [^41^].
- **Key advantage over NVLink**: Supports heterogeneous accelerators (AMD MI300X, Intel Gaudi, custom ASICs) with no vendor lock-in. Supports nearly 2x the maximum cluster size (1,024 vs 576 GPUs) [^39^][^40^].
- Hardware availability expected late 2026/2027 from AMD, Intel, and Astera Labs [^39^].
- UALink is **not compatible with NVIDIA GPUs** — targets non-NVIDIA accelerators only [^40^].

### Kubernetes GPU Scheduling & Heterogeneous Management
- **HAMi** (Heterogeneous AI Computing Virtualization Middleware, CNCF Sandbox) is the leading open-source solution for heterogeneous GPU sharing on Kubernetes. Supports NVIDIA GPU, Cambricon MLU, Hygon DCU, Iluvatar CoreX, Moore Threads, Huawei Ascend, and MetaX [^42^][^43^].
- HAMi uses **CUDA API interception** (LD_PRELOAD of `libhami-core.so`) to track memory and compute usage per process, enabling vGPU creation with custom memory/SM limits without application changes [^44^].
- Production use cases include: banking (GPU utilization <20% → 60%), GPU cloud providers (tripled revenue per card), and R&D platforms (safe GPU sharing for Jupyter notebooks) [^44^].
- **DRA** (Dynamic Resource Allocation, Kubernetes 1.32+) is the emerging standard for GPU resource management, with NVIDIA's DRA driver providing ComputeDomain support for MNNVL [^9^][^45^].
- GPU scheduling moving toward standardized interfaces (DRA + CDI) reducing vendor lock-in for heterogeneous accelerator fleets [^45^].

### GOGH — Correlation-Guided Orchestration for Heterogeneous Clusters
- **GOGH** uses two neural networks to predict job throughput across heterogeneous GPU types, achieving prediction errors as low as 5% [^46^][^47^].
- Formulates GPU allocation as an ILP problem jointly optimizing throughput, energy efficiency, and minimum performance guarantees [^46^].
- Exploits two correlations: similarity between different jobs and variation in throughput of the same job across different GPU types [^46^].
- Evaluated on Gavel benchmark dataset with K80, P100, and V100 GPUs, showing significant improvements over baseline schedulers [^46^].
- Related work: **Gavel** introduced effective throughput for accelerator-aware scheduling; **Pollux** co-adapts resource allocation and training configurations (batch size, learning rate) using GNS (gradient noise scale) for goodput optimization [^48^][^49^].

---

## Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **NVIDIA** | Dominant GPU vendor. NVLink/NVSwitch interconnect, MIG partitioning, vGPU licensing, GPUDirect RDMA. Moving toward semi-open with NVLink Fusion. |
| **AMD** | ROCm stack, Instinct MI300X GPUs, Infinity Fabric/XGMI interconnect, SR-IOV virtualization. Leading UALink consortium. |
| **Intel** | oneAPI/SYCL/Level Zero programming model, Xe GPU SR-IOV (zero licensing), Project Battlematrix multi-GPU AI. |
| **Apple** | Unified memory architecture (CPU/GPU zero-copy), Metal framework. No multi-GPU interconnect strategy. |
| **NVIDIA + Kubernetes** | GPU Operator, DRA driver, ComputeDomain CRD for MNNVL, IMEX service for cross-node GPU memory sharing. |
| **HAMi (CNCF)** | Leading open-source heterogeneous GPU virtualization for Kubernetes. API interception approach for vGPU. |
| **DeepSpeed (Microsoft)** | ZeRO optimizer for distributed training across multi-node GPU clusters. Assumes homogeneous GPUs. |
| **Apache TVM** | Unified compiler stack for inference on heterogeneous hardware. AutoTVM for automatic kernel optimization. |
| **Khronos Group / Intel** | SYCL standard and DPC++ compiler for cross-platform GPU computing. Performance portability remains challenge. |
| **UALink Consortium** | AMD, Intel, Google, Microsoft, Meta, AWS, Cisco, Apple, and others developing open GPU interconnect standard. |
| **Universitat Jaume I** | rCUDA research project — transparent CUDA virtualization. Not actively maintained. |
| **VirtualGL Project** | Open-source remote OpenGL rendering via API interposition. Mature but compute-focused only. |

---

## Trends & Signals

1. **GPU virtualization moving from hardware partitioning (MIG) to software API interception (HAMi)** — Software approaches offer more flexibility (custom memory/SM ratios, any GPU model) but provide weaker isolation than hardware MIG [^42^][^44^].

2. **Open interconnect standards challenging NVIDIA's NVLink dominance** — UALink 1.0 supports 1,024 accelerators vs NVLink's 576; CXL 4.0 adds multi-rack memory pooling. Hardware expected 2026-2027 [^39^][^40^].

3. **NVLink Fusion represents NVIDIA's strategic pivot** — Acknowledging heterogeneous future by allowing third-party CPUs/accelerators into NVLink fabric, while maintaining requirement for NVIDIA silicon in each node [^6^][^7^].

4. **Intel SR-IOV with zero licensing disrupting GPU virtualization economics** — Intel Xe driver in Linux 6.19+ enables multi-VM GPU virtualization without proprietary licensing, challenging NVIDIA's vGPU licensing model [^17^].

5. **Kubernetes becoming the GPU control plane** — DRA (Dynamic Resource Allocation), CDI (Container Device Interface), and topology-aware scheduling evolving Kubernetes from container orchestrator to AI computing control plane [^45^][^50^].

6. **SYCL emerging as the leading cross-platform GPU programming model** — DPC++ compiler supports NVIDIA, AMD, Intel GPUs plus FPGAs from single source. But performance portability gaps (up to 40x variance) remain significant [^19^][^34^][^35^].

7. **GPU utilization crisis driving sharing technologies** — 75%+ of organizations report <70% GPU utilization at peak; GPT-4 training used only 32-36% of 25,000 A100s average [^27^].

8. **Heterogeneous GPU clusters becoming the norm** — GOGH, HAMi, and UALink all reflect industry shift from homogeneous NVIDIA-only clusters to mixed-vendor deployments [^46^][^42^].

9. **API remoting (rCUDA-style) largely superseded by native distributed frameworks** — DeepSpeed, FSDP, and Ray handle distribution at the framework level rather than transparent API interception [^29^][^30^].

10. **Multi-Node NVLink with IMEX enabling rack-scale GPU memory pools** — NVIDIA's ComputeDomain and IMEX service allow Kubernetes workloads to treat 72-GPU NVL72 racks as unified memory domains [^9^][^51^].

---

## Controversies & Conflicting Claims

### GPU Sharing: API Interception vs Hardware Partitioning
- **HAMi** (and similar approaches like Orion, cGPU) use CUDA API interception via `LD_PRELOAD` to provide flexible vGPU sharing with zero hardware requirements. This provides "soft isolation" — brief overages possible, long-term usage stays within target [^44^].
- **NVIDIA MIG** provides hardware-level isolation with guaranteed QoS and fault isolation, but only on expensive datacenter GPUs (A100/H100/Blackwell) with rigid partitioning templates [^24^][^26^].
- **Controversy**: NVIDIA's vGPU licensing model creates significant cost barriers. Intel's zero-license SR-IOV approach and open-source API interception tools challenge this model [^17^][^44^].

### Performance Portability: SYCL vs Native APIs
- **Pro-SYCL**: "SYCL stands as a promising unified programming model for heterogeneous computing environments, particularly for bioinformatic applications" — achieved comparable performance to CUDA on NVIDIA GPUs [^35^].
- **Anti-SYCL**: "SYCL explicitly disclaims performance portability in its specification, and our results validate that decision. Performance varies widely depending on abstraction choice, memory model, and backend, with differences reaching over 40x in some cases" [^19^].
- Work-group kernels achieved 71% of FP64 peak vs basic data-parallel kernels showing worst performance across all platforms [^19^].

### AMD ROCm vs NVIDIA CUDA Ecosystem Maturity
- **AMD claims**: MI300X offers competitive compute with 192 GB HBM3 per GPU and fully-connected Infinity Fabric topology at lower TCO [^12^].
- **Reality check**: RCCL achieves only ~70% of theoretical bandwidth vs NCCL's ~85%. Infinity Fabric link bandwidth (128 GB/s) is significantly below NVLink 4.0 (900 GB/s). "There remains headroom for improvement particularly in optimizing RCCL to better exploit AMD's mesh interconnect layout" [^14^].

### NVLink Fusion: Genuine Openness or Vendor Control?
- **NVIDIA's framing**: "NVLink Fusion opens NVIDIA's AI platform and rich ecosystem for partners to build specialized AI infrastructures" [^8^].
- **Critical view**: "System vendors will not be able to build NVLink-enabled systems using both third-party CPUs and third-party GPUs — they must still have a piece of NVIDIA silicon on their nodes... this seems to be strictly a licensing limitation rather than a technical limitation" [^7^].

### UALink: True NVLink Alternative or Compromise?
- **UALink advantage**: 1,024 accelerator scale, open standard, multi-vendor support, no lock-in [^39^].
- **UALink disadvantage**: Per-GPU bandwidth (800 GB/s) is less than half of NVLink 5.0 (1.8 TB/s). No NVIDIA GPU support. Hardware not available until late 2026/2027 [^40^].

---

## Recommended Deep-Dive Areas

1. **HAMi/CUDA API Interception Architecture**: HAMi's approach of LD_PRELOAD-based CUDA call interception with memory/compute limiting is directly applicable to Cluster OS. The mechanism for tracking GPU memory usage via shared memory and polling GPU utilization for compute limiting provides a model for transparent GPU virtualization without hardware support [^44^].

2. **IMEX/ComputeDomain for Multi-Node GPU Memory Sharing**: NVIDIA's IMEX service and ComputeDomain CRD represent the state of the art in rack-scale GPU memory virtualization. Understanding how IMEX domains are dynamically created, managed, and torn down for Kubernetes workloads is critical for distributed compute design [^9^][^51^].

3. **SYCL Performance Portability Deep-Dive**: The 40x performance variance finding warrants investigation. Understanding which SYCL abstractions (work-group vs basic data-parallel, USM vs buffer-accessors) work reliably across NVIDIA/AMD/Intel GPUs is essential for writing portable compute kernels [^19^].

4. **GOGH/Pollux Scheduling for Heterogeneous GPUs**: GOGH's neural-network-based throughput prediction and ILP formulation for heterogeneous GPU allocation, combined with Pollux's co-adaptive resource and batch size tuning, provide a foundation for Cluster OS scheduling decisions [^46^][^49^].

5. **SR-IOV vs API Interception Trade-offs**: Intel's SR-IOV (hardware-level, zero license) vs HAMi's API interception (software-level, any GPU) represent two poles of GPU virtualization. Understanding isolation guarantees, overhead, and security implications is critical [^17^][^42^].

6. **UALink vs NVLink for Cluster Design**: With UALink hardware arriving in 2026-2027, understanding how UALink fabrics will coexist with NVLink in heterogeneous datacenter environments is important for long-term cluster architecture [^39^][^40^].

7. **Apple Silicon Unified Memory for Compute Workloads**: Apple's zero-copy unified memory architecture offers significant advantages for inference pipelines. Understanding how to leverage this for distributed compute (e.g., via Ray or custom networking) would fill a gap in current research [^20^].

---

## Raw Evidence Log

### Evidence 1: rCUDA Architecture and Performance
- **Claim**: rCUDA uses client-server architecture with API remoting to virtualize GPUs remotely, with negligible overhead over InfiniBand and ability to increase cluster throughput by decoupling GPUs from nodes.
- **Source**: rCUDA Overview Presentation, Universitat Jaume I / HPCA
- **URL**: https://www.hpca.uji.es/wp-content/uploads/recent_talks/quintana/rCUDA_overview.pdf
- **Date**: Undated (research project, ~2010 era)
- **Excerpt**: "Client: Emulates local GPUs, Handles communication with the server. Server: Waits for requests, Executes GPU code (kernel), Reports status." And: "Remote GPU faster than local CPU" and "rCUDA+SLURM: GPUs are logically decoupled from nodes and therefore any GPU can be assigned to any job, independently of their location. In this way overall cluster throughput is increased."
- **Confidence**: High (for architecture description), Low (for current relevance — project appears unmaintained)

### Evidence 2: rCUDA Integration with SLURM
- **Claim**: rCUDA extends SLURM to schedule virtual GPUs as exclusive or shared resources, enabling virtual and physical GPUs to be used simultaneously.
- **Source**: NVIDIA Whitepaper — The rCUDA middleware and applications
- **URL**: https://network.nvidia.com/pdf/whitepapers/rCUDA_Middleware_and_Applications.pdf
- **Date**: Undated
- **Excerpt**: "The new extension of SLURM allows: Scheduling virtual GPUs by SLURM in a user transparent way; Scheduling virtual GPUs either as exclusive or shared resources; Virtual GPUs and standard physical GPUs can be used at the same time"
- **Confidence**: Medium

### Evidence 3: NVLink 5 Bandwidth and Scaling
- **Claim**: NVLink 5 delivers 1.8TB/s per GPU (18 links x 100GB/s) — 14x PCIe Gen5 bandwidth. GB200 NVL72 provides 130TB/s aggregate. Scale-up bandwidth is ~18x scale-out.
- **Source**: Introl Blog — NVLink and scale-up networking
- **URL**: https://introl.com/blog/nvlink-scale-up-networking-gpu-interconnect-infrastructure-2025
- **Date**: 2026-02-04
- **Excerpt**: "NVLink 5 delivers 1.8TB/s per GPU (18 links x 100GB/s)—14x PCIe Gen5 bandwidth; GB200 NVL72 provides 130TB/s aggregate. Scale-up (NVLink within rack) delivers ~18x bandwidth of scale-out (InfiniBand/Ethernet between racks)"
- **Confidence**: High

### Evidence 4: NVSwitch Performance Uniformity
- **Claim**: NVSwitch maintains ~900 GB/s all-to-all bandwidth regardless of GPU count (2, 4, or 8), completely flattening communication bottlenecks.
- **Source**: IntuitionLabs — NVIDIA NVLink Explained
- **URL**: https://intuitionlabs.ai/articles/nvidia-nvlink-gpu-interconnect
- **Date**: 2025-10-22
- **Excerpt**: "Even as GPU count rises, NVSwitch holds the per-GPU interconnect bandwidth nearly constant (900 GB/s for 2, 4, or 8 GPUs), whereas a fixed topology degrades... the NVSwitch completely flattens the communication bottleneck."
- **Confidence**: High

### Evidence 5: NVLink-C2C ATS Feature
- **Claim**: NVLink supports Address Translation Services allowing GPU to directly access CPU page table without pinning memory or software copy.
- **Source**: Patsnap Blog — NVLink-C2C eliminates latency
- **URL**: https://www.patsnap.com/resources/blog/articles/nvlink-c2c-eliminates-latency-in-multi-chip-gpu-modules/
- **Date**: 2026-04-15
- **Excerpt**: "NVLink's Address Translation Services (ATS) support — documented in NVIDIA's 2024 patent — allows the GPU to directly access the CPU's page table without pinning memory regions or marshalling data through software copy engines."
- **Confidence**: High

### Evidence 6: NVLink Fusion Third-Party Integration
- **Claim**: NVLink Fusion opens NVLink to third-party CPUs (Fujitsu, Qualcomm) and ASICs (Marvell, MediaTek) but each node must still contain NVIDIA silicon.
- **Source**: NVIDIA Press Release
- **URL**: https://investor.nvidia.com/news/press-release-details/2025/NVIDIA-Unveils-NVLink-Fusion-for-Industry-to-Build-Semi-Custom-AI-Infrastructure-With-NVIDIA-Partner-Ecosystem/default.aspx
- **Date**: 2025-05-18
- **Excerpt**: "NVIDIA today unveiled NVIDIA NVLink Fusion — new silicon that lets industries build semi-custom AI infrastructure... MediaTek, Marvell, Alchip Technologies, Astera Labs, Synopsys and Cadence are among the first to adopt NVLink Fusion"
- **Confidence**: High

### Evidence 7: AMD ROCm Virtualization Support
- **Claim**: ROCm supports SR-IOV and passthrough virtualization for MI300X, MI325X, MI250, MI210 on KVM and Hyper-V.
- **Source**: ROCm Documentation — System Requirements
- **URL**: https://rocm.docs.amd.com/projects/install-on-linux/en/docs-6.4.1/reference/system-requirements.html
- **Date**: 2025-06-18
- **Excerpt**: "ROCm supports virtualization for the Instinct accelerators... These virtualization technologies are designed to dedicate entire GPUs to individual virtual machines (VMs), rather than allowing a single GPU to be shared across multiple VMs."
- **Confidence**: High

### Evidence 8: AMD MI300X Infinity Fabric Topology
- **Claim**: MI300X uses fully-connected 8-GPU topology with 7 Infinity Fabric links per GPU, achieving 448 GB/s collective bandwidth (8 GPUs).
- **Source**: AMD ROCm Blog — Understanding RCCL Bandwidth and xGMI Performance
- **URL**: https://rocm.blogs.amd.com/software-tools-optimization/mi300x-rccl-xgmi/README.html
- **Date**: 2025-03-02
- **Excerpt**: "The MI300X architecture features dedicated links between GPUs, forming a fully connected topology. For collective operations, the highest performance is achieved when all 8 GPUs are used, ensuring all inter GPU links are active."
- **Confidence**: High

### Evidence 9: RCCL vs NCCL Performance Comparison
- **Claim**: RCCL on MI300X achieves ~70% of theoretical peak bandwidth vs ~85% for NCCL on H100. 8-GPU collective bandwidth: 448 GB/s (AMD) vs significantly higher (NVIDIA).
- **Source**: arXiv — AMD MI300X GPU Performance Analysis
- **URL**: https://arxiv.org/pdf/2510.27583
- **Date**: 2025
- **Excerpt**: "NVIDIA GPUs achieve approximately 85% of their theoretical peak bandwidth... For AMD, bandwidth scales from 64 GB/s (2 GPUs) to 192 GB/s (4 GPUs) and 448 GB/s (8 GPUs), reaching about 70% of theoretical peak bandwidth."
- **Confidence**: High

### Evidence 10: Intel Level Zero Architecture
- **Claim**: Level Zero provides direct-to-metal interfaces for heterogeneous hardware, used to implement oneAPI middleware and language runtimes.
- **Source**: oneAPI Blog — Level Zero: Latest Developments
- **URL**: https://oneapi.io/blog/level-zero-latest-developments/
- **Date**: 2022-09-29
- **Excerpt**: "The objective of Level Zero is to provide direct-to-the-metal interfaces to offload accelerator devices... Frequently, Level Zero is not used directly by oneAPI developers, although it is used to implement oneAPI middleware and language runtimes."
- **Confidence**: High

### Evidence 11: Intel Xe SR-IOV Mainline Support
- **Claim**: Linux 6.19 brings Intel Xe VFIO support enabling SR-IOV GPU virtualization for multiple VMs with zero proprietary licensing.
- **Source**: Medium — Intel Xe VFIO Driver: GPU Virtualization Enters the Mainstream
- **URL**: https://canartuc.medium.com/intel-xe-vfio-driver-gpu-virtualization-enters-the-mainstream-09982a2f6cd5
- **Date**: 2025-12-30
- **Excerpt**: "Linux 6.19 brings Intel Xe VFIO support to the mainline kernel. One GPU, multiple virtual machines, zero proprietary licensing. This is the kind of upstream contribution that shifts entire industry economics."
- **Confidence**: High

### Evidence 12: SYCL Performance Portability Study
- **Claim**: SYCL performance varies by up to 40x depending on abstraction choice, memory model, and backend. Work-group kernels achieve 71% of FP64 peak.
- **Source**: arXiv — Evaluating SYCL as a Unified Programming Model for Heterogeneous Systems
- **URL**: https://arxiv.org/html/2604.16043v1
- **Date**: 2026-04-17
- **Excerpt**: "SYCL explicitly disclaims performance portability in its specification, and our results validate that decision. Performance varies widely depending on abstraction choice, memory model, and backend, with differences reaching over 40x in some cases."
- **Confidence**: High

### Evidence 13: Apple Unified Memory Architecture
- **Claim**: Apple Silicon unified memory eliminates CPU-GPU data copies, with up to 153 GB/s bandwidth on M5 chips. MTLStorageMode.shared enables zero-copy access.
- **Source**: AppleMagazine — Apple GPU Memory and the Unified Architecture
- **URL**: https://applemagazine.com/apple-gpu-memory-92b/
- **Date**: 2026-04-08
- **Excerpt**: "Apple Silicon integrates memory directly into the system-on-chip package, giving both the CPU and GPU access to the same unified memory pool... On recent chips such as M5, Apple's unified memory bandwidth reaches up to 153 GB/s"
- **Confidence**: High

### Evidence 14: VirtualGL Architecture
- **Claim**: VirtualGL uses LD_PRELOAD to intercept GLX/EGL calls, redirecting rendering to server-side GPU, then captures frames via glReadPixels/PBOs for streaming.
- **Source**: Grokipedia — VirtualGL
- **URL**: https://grokipedia.com/page/VirtualGL
- **Date**: 2026-01-14
- **Excerpt**: "VirtualGL employs a dynamic library injection technique using the LD_PRELOAD environment variable to preload its core library... This injection overrides key functions in the GLX and EGL APIs... redirecting these commands to a designated 3D-accelerated X server on the remote host"
- **Confidence**: High

### Evidence 15: NVIDIA MIG Hardware Isolation
- **Claim**: MIG provides hardware-level partitioning with isolated memory paths — each instance's processors have separate paths through crossbar ports, L2 cache banks, memory controllers, and DRAM buses.
- **Source**: NVIDIA MIG User Guide
- **URL**: https://www.escape-technology.de/images/news/2025/NVIDIA_MIG/MIG_User_Guide.pdf
- **Date**: 2025-05-27
- **Excerpt**: "With MIG, each instance's processors have separate and isolated paths through the entire memory system - the on-chip crossbar ports, L2 cache banks, memory controllers, and DRAM address busses are all assigned uniquely to an individual instance."
- **Confidence**: High

### Evidence 16: GPU Utilization Crisis Statistics
- **Claim**: 75%+ of organizations report GPU utilization below 70% at peak. GPT-4 trained on 25,000 A100s with only 32-36% average utilization.
- **Source**: Introl Blog — GPU Memory Pooling and Sharing
- **URL**: https://introl.com/blog/gpu-memory-pooling-sharing-multi-tenant-kubernetes-2025
- **Date**: 2026-01-17
- **Excerpt**: "December 2025 Update: 75%+ of organizations reporting GPU utilization below 70% at peak load. GPT-4 trained on 25,000 A100s with only 32-36% average utilization."
- **Confidence**: Medium (statistics attributed to unnamed report)

### Evidence 17: DeepSpeed ZeRO Multi-Node Training
- **Claim**: DeepSpeed supports single-GPU through multi-node training with ZeRO stages 1-3 for optimizer/gradient/parameter partitioning, plus CPU/NVMe offload.
- **Source**: DeepSpeed Training Overview
- **URL**: https://www.deepspeed.ai/training/
- **Date**: 2020-09-08
- **Excerpt**: "Single-GPU, Multi-GPU, and Multi-Node Training: Easily switch between single-GPU, single-node multi-GPU, or multi-node multi-GPU execution by specifying resources with a hostfile."
- **Confidence**: High

### Evidence 18: Apache TVM Heterogeneous Runtime
- **Claim**: TVM provides heterogeneous compilation and runtime, supporting NVIDIA GPUs, AMD GPUs, ARM/Intel CPUs, Mali GPUs with AutoTVM optimization.
- **Source**: Apache TVM Blog — Automatic Kernel Optimization
- **URL**: https://tvm.apache.org/2018/10/03/auto-opt-all
- **Date**: 2018-10-03
- **Excerpt**: "TVM takes a full stack compiler approach. TVM combines code generation and automatic program optimization to generate kernels that are comparable to heavily hand-optimized libraries, obtaining state-of-the-art inference performance on hardware platforms including ARM CPUs, Intel CPUs, Mali GPUs, NVIIDA GPUs and AMD GPUs."
- **Confidence**: High

### Evidence 19: GPUDirect RDMA Technical Evolution
- **Claim**: GPUDirect RDMA enables RDMA NICs to directly access GPU VRAM, bypassing CPU and system memory. NVIDIA uses `nvidia_p2p_get_pages`; AMD uses Peer Memory Client (ROCnRDMA); kernel uses `dma-buf` standard.
- **Source**: Medium — The Evolution and Implementation of GPUDirect RDMA
- **URL**: https://medium.com/@datenlord/the-evolution-and-implementation-of-gpudirect-rdma-19751f7b9413
- **Date**: 2025-05-18
- **Excerpt**: "The core goal of GPUDirect RDMA technology was developed: to enable RDMA NICs to directly and securely access the VRAM of local or remote GPUs, completely bypassing the host CPU and system memory."
- **Confidence**: High

### Evidence 20: UALink vs NVLink Comparison
- **Claim**: UALink 1.0 delivers 800 GB/s (x4), supports 1,024 accelerators, open standard. NVLink 5.0 delivers 1.8 TB/s, supports 576 GPUs, NVIDIA-only.
- **Source**: Introl Blog — UALink and CXL 4.0
- **URL**: https://introl.com/blog/ualink-cxl-4-gpu-interconnect-memory-pooling-guide-2025
- **Date**: 2026-02-06
- **Excerpt**: "UALink 1.0 delivers 200 GT/s per lane with support for up to 1,024 accelerators across a single fabric, directly challenging Nvidia's proprietary NVLink and NVSwitch ecosystem."
- **Confidence**: High

### Evidence 21: HAMi Heterogeneous GPU Sharing Architecture
- **Claim**: HAMi uses LD_PRELOAD-based CUDA API interception (hami-core) to track memory and compute usage, enabling vGPU with custom limits on heterogeneous GPUs without application changes.
- **Source**: HAMi GitHub / Reddit Maintainer Post
- **URL**: https://github.com/project-hami/hami and https://www.reddit.com/r/kubernetes/comments/1kvy06i/
- **Date**: 2025-08-05
- **Excerpt**: "HAMi's virtualization layer is implemented in HAMi-core, a user-space CUDA API interception library... LD_PRELOAD hijacks CUDA calls and tracks resource usage per process. Memory limiting: Intercepts memory allocation calls (cuMemAlloc*) and checks against tracked usage in shared memory."
- **Confidence**: High

### Evidence 22: HAMi Production Use Case — GPU Cloud Provider
- **Claim**: A GPU cloud provider used HAMi to move from whole-card pricing to fractional GPU offerings, tripling revenue per card and supporting up to 26 concurrent users on a single H800.
- **Source**: Reddit — HAMi Maintainer Post
- **URL**: https://www.reddit.com/r/kubernetes/comments/1kvy06i/
- **Date**: 2025-08-05
- **Excerpt**: "A cloud vendor used HAMi to move from whole-card pricing (e.g., H800 @ $2/hr) to fractional GPU offerings (e.g., 3GB @ $0.26/hr). This drastically improved user affordability and tripled their revenue per card, supporting up to 26 concurrent users on a single H800."
- **Confidence**: High (anecdotal from maintainer)

### Evidence 23: GOGH Heterogeneous GPU Scheduling
- **Claim**: GOGH uses two neural networks (P1 for initial throughput estimation, P2 for refinement) plus ILP optimizer for scheduling DL jobs across heterogeneous GPUs, achieving 5% prediction error.
- **Source**: arXiv — GOGH: Correlation-Guided Orchestration of GPUs in Heterogeneous Clusters
- **URL**: https://arxiv.org/abs/2510.15652
- **Date**: 2025-10-17
- **Excerpt**: "We propose GOGH, a novel framework for orchestrating deep learning workloads across heterogeneous GPU clusters. GOGH leverages historical performance data to guide scheduling decisions, accounting for both inter-job and inter-GPU correlations."
- **Confidence**: High

### Evidence 24: Pollux Co-Adaptive Scheduling
- **Claim**: Pollux co-adaptively allocates GPUs and tunes batch size/learning rate using gradient noise scale (GNS), achieving 72% shorter average JCT vs Optimus and 73% vs Tiresias.
- **Source**: USENIX OSDI 2021 — Pollux Paper
- **URL**: https://www.usenix.org/system/files/osdi21-qiao.pdf
- **Date**: 2021
- **Excerpt**: "Pollux has 72% and 73% shorter average JCT, 50% and 56% shorter tail JCT, and 43% and 48% shorter makespan, in comparison to Optimus+Oracle and Tiresias, respectively."
- **Confidence**: High

### Evidence 25: Kubernetes MNNVL with IMEX and ComputeDomains
- **Claim**: NVIDIA IMEX service enables GPU memory export/import across OS domains; ComputeDomain CRD dynamically manages IMEX domains for multi-node NVLink on Kubernetes.
- **Source**: NVIDIA Developer Blog — Enabling Multi-Node NVLink on Kubernetes
- **URL**: https://developer.nvidia.com/blog/enabling-multi-node-nvlink-on-kubernetes-for-gb200-and-beyond/
- **Date**: 2025-11-25
- **Excerpt**: "ComputeDomains dynamically create, manage, and tear down IMEX domains as multi-node workloads are scheduled to nodes and run to completion... each workload gets its own isolated IMEX domain and shared IMEX channel, ensuring GPU-to-GPU communication between all workers of a job."
- **Confidence**: High

### Evidence 26: Kubernetes GPU Scheduling Convergence
- **Claim**: GPU resource management converging on DRA and CDI as standardized interfaces, enabling management of heterogeneous accelerator fleets through unified Kubernetes API.
- **Source**: CIO.com — How Kubernetes is solving the GPU utilization crisis
- **URL**: https://www.cio.com/article/4152554/how-kubernetes-is-finally-solving-the-gpu-utilization-crisis-to-save-your-ai-budget.html
- **Date**: 2026-03-31
- **Excerpt**: "GPU resource management is moving toward standardized interfaces through DRA and the Container Device Interface. This reduces vendor lock-in and lets organizations manage heterogeneous accelerator fleets — including AMD, Intel and custom silicon — through a unified Kubernetes API."
- **Confidence**: High

### Evidence 27: NVIDIA IMEX Service Technical Details
- **Claim**: IMEX service manages memory sharing between compute nodes, handles lifecycle of shared memory, registers for import/unimport events, uses TCP/IP and gRPC for cross-node communication.
- **Source**: NVIDIA IMEX Service Documentation
- **URL**: https://docs.nvidia.com/multi-node-nvlink-systems/imex-guide/overview.html
- **Date**: 2025-07-02
- **Excerpt**: "The IMEX service supports GPU memory export and import (NVLink P2P) and shared memory operations across OS domains in an NVLink multi-node deployment... Communicates across nodes using the compute node's network by using TCP/IP and gRPC connections."
- **Confidence**: High

### Evidence 28: Intel Multi-Device SVM for Multi-GPU AI
- **Claim**: Intel's Xe driver adds multi-device SVM support in Linux 6.20/7.0 for multi-GPU AI workloads with Level Zero or OpenCL, important for Project Battlematrix.
- **Source**: Phoronix — Intel's Xe Linux Driver Ready With Multi-Device SVM
- **URL**: https://www.phoronix.com/news/Intel-Multi-Device-SVM-Linux-7
- **Date**: 2025-12-30
- **Excerpt**: "With this updated Xe driver code the next version of the Linux kernel will support multi-device SVM for Shared Virtual Memory across Intel graphics cards. This is important for multi-device AI and GPU compute workloads with Level Zero or OpenCL."
- **Confidence**: High

### Evidence 29: GPU Virtualization Comprehensive Survey
- **Claim**: GPU virtualization methods span API remoting (rCUDA), device emulation, para-virtualization, pass-through, and SR-IOV. Hardware-level approaches (MIG) provide best isolation; software approaches provide most flexibility.
- **Source**: HAL Science — A comprehensive review of GPU virtualization and sharing
- **URL**: https://hal.science/hal-05429292v1/file/main.pdf
- **Date**: Undated (comprehensive survey)
- **Excerpt**: (References to) "GPU Virtualization and Scheduling Methods: A Comprehensive Survey" (ACM Computing Surveys, 2017); GOGH paper; "Improving GPU Multi-Tenancy Through Dynamic MIG Reconfiguration"; "GPU context-aware preemptive priority-based scheduling"
- **Confidence**: High

### Evidence 30: VMware Bitfusion Discontinued
- **Claim**: VMware discontinued vSphere Bitfusion (GPU virtualization service) and replaced it with VMware Private AI Foundation with NVIDIA.
- **Source**: Medium — GPU Virtualization for IaaS and PaaS Service Providers
- **URL**: https://evren-baycan.medium.com/gpu-virtualization-for-iaas-and-paas-service-providers-deep-dive-7f296e10392f
- **Date**: 2026-03-17
- **Excerpt**: "NOTE: VMware has discontinued (EOA — End of Availability) the vSphere Bitfusion service and replaced it with VMware Private AI Foundation with NVIDIA Add-on."
- **Confidence**: High

---

## Technology Matrix for Cluster OS

| Technology | Virtualization Type | Heterogeneous | Maturity | License | Best For |
|-----------|-------------------|--------------|----------|---------|----------|
| rCUDA | API remoting | No (CUDA only) | Research/unmaintained | Open | Transparent CUDA forwarding |
| NVIDIA MIG | Hardware spatial partition | No (NVIDIA only) | Production | Hardware-included | Strong isolation, QoS guarantees |
| NVIDIA vGPU | Para-virtualization | No (NVIDIA only) | Production | Proprietary ($$) | VM-based GPU sharing |
| HAMi | API interception | Yes (10+ vendors) | Production (CNCF) | Apache 2.0 | K8s-native GPU sharing |
| Intel SR-IOV | Hardware VF | No (Intel only) | Linux 6.19+ | Zero license | Multi-VM GPU virtualization |
| AMD SR-IOV | Hardware VF | No (AMD only) | Production (ROCm 6.x) | Zero license | Multi-VM GPU virtualization |
| GPU Passthrough | Full passthrough | Yes (any PCIe) | Production | Free | Near-native performance |
| VirtualGL | GL API remoting | Yes (OpenGL) | Mature | Open | Remote visualization |
| DeepSpeed ZeRO | Framework-level | No (assumes homogenous) | Production | MIT | Large model distributed training |
| FSDP | Framework-level | No (assumes homogenous) | Production (PyTorch) | BSD | PyTorch model sharding |
| TVM | Compiler/runtime | Yes (multi-hardware) | Production | Apache 2.0 | Inference optimization |
| SYCL/DPC++ | Programming model | Yes (NVIDIA/AMD/Intel) | Production | Open | Cross-platform kernels |
| NVLink/NVSwitch | Physical interconnect | No (NVIDIA only) | Production | Proprietary | Rack-scale GPU fabric |
| UALink | Physical interconnect | Yes (non-NVIDIA) | Spec only (HW 2026) | Open standard | Future open GPU fabric |
| GPUDirect RDMA | Direct memory access | Partial (same root complex) | Production | Free | NIC-GPU direct transfer |
| IMEX/ComputeDomain | Cross-node GPU memory | No (NVIDIA only) | Production (K8s 1.32+) | Free | MNNVL memory sharing |
| GOGH | Scheduling framework | Yes | Research (2025) | Unknown | Heterogeneous GPU scheduling |

---

## Key Implications for Cluster OS Design

1. **No silver bullet for heterogeneous GPU virtualization**: The ideal solution would combine HAMi's flexibility (any GPU, custom ratios) with MIG's isolation strength (hardware-level) and SR-IOV's zero licensing. This combination does not exist today.

2. **Kubernetes is the de facto orchestration layer**: DRA + CDI + GPU Operator represent the standard stack. Cluster OS should align with these APIs rather than reinventing resource management.

3. **SYCL is the most promising cross-platform programming model**: Despite performance portability challenges, SYCL/DPC++ is the only solution that supports NVIDIA + AMD + Intel + FPGA from a single codebase. For Cluster OS compute kernels, SYCL should be the primary target.

4. **Network interconnect is the binding constraint**: Whether using NVLink (NVIDIA), Infinity Fabric (AMD), UALink (future), or InfiniBand/Ethernet, distributed performance is dominated by interconnect bandwidth. Cluster OS must be topology-aware.

5. **Apple Silicon requires a different approach**: Apple's unified memory and lack of multi-GPU interconnect means distributed compute must use higher-level networking (Thunderbolt, Ethernet) rather than native GPU links. Ray-style task distribution is more appropriate than NCCL-style collectives.

6. **The scheduling problem is harder than the virtualization problem**: GOGH and Pollux demonstrate that optimal job-to-GPU matching requires prediction and co-adaptation. Cluster OS scheduling should incorporate throughput prediction for heterogeneous hardware.

7. **API remoting (rCUDA-style) is not the right abstraction for modern workloads**: Modern frameworks (PyTorch, DeepSpeed, Ray) handle distribution at the application level. Transparent API remoting adds complexity without clear benefits over explicit distribution.

---

*Research compiled: 2025*
*Sources: 30+ primary sources including NVIDIA/AMD/Intel documentation, academic papers (OSDI, arXiv), CNCF projects, and technical blogs*
