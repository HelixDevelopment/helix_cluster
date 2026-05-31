# Facet: Resource Aggregation & Scheduling — Heterogeneous Cluster Resource Management

## Key Findings

### Apache Mesos — Lessons from Retirement
- Apache Mesos was officially retired and moved to the Apache Attic in August 2025 [^102^], marking the end of a pioneering cluster manager that originated at UC Berkeley AMPLab in 2009 [^105^]. The project's decline reveals critical lessons for Cluster OS design.
- Mesos introduced the **two-level scheduling architecture** where a resource manager (Mesos master) offered resources to framework-level schedulers (Marathon, Spark, etc.), which could accept or reject offers [^150^]. This was groundbreaking but had fundamental limitations.
- The **pessimistic two-level offer model** made it hard for second-level schedulers to make optimal decisions because they might not get the right offer they needed [^105^]. As one core developer noted: "Mesos's pessimistic two level offer model makes it hard for second level scheduler to make optimal decisions because it might not get the _right_ offer it needs. At the same time, first level scheduler lacks application specific information to make the right decision to send the right offer to the second level scheduler, thus the problem." [^105^]
- Kubernetes won because it used a **shared-state optimistic scheduling model** (inspired by Google's Omega), allowing the scheduler to see the entire cluster state and make optimal decisions [^105^]. Google's experience with Borg directly informed Kubernetes design.
- Mesos also suffered from being built in C++ with a unique threading model (libprocess — "C++ version of Erlang"), making it difficult to integrate third-party libraries. Kubernetes's Go-based ecosystem was far more composable [^105^].
- **Lesson for Cluster OS**: Avoid pessimistic resource locking; use shared-state/optimistic concurrency. Ensure your architecture is extensible without forcing complex multi-level scheduling.

### Kubernetes Scheduler — The Extensible Framework
- Kubernetes uses a **monolithic scheduler architecture** (kube-scheduler) with a **Scheduling Framework V2** that provides 10+ extension points: QueueSort, PreFilter, Filter, PostFilter, PreScore, Score, NormalizeScore, Reserve, Permit, PreBind/Bind/PostBind [^62^].
- Three methods to extend scheduling: (1) configuring existing plugins via YAML, (2) deploying official out-of-tree plugins from kubernetes-sigs/scheduler-plugins, (3) building custom Go plugins [^61^].
- Official out-of-tree plugins include: **Coscheduling** (gang scheduling), **Node Resource Topology** (NUMA-aware scheduling), **Trimaran** (load-aware scheduling), **Network-Aware Scheduling**, and **Capacity Scheduling** [^61^].
- **Dynamic Resource Allocation (DRA)** went GA in Kubernetes 1.34 (2025), replacing the device plugin model with structured resource descriptions. DRA introduces ResourceSlice (device attributes), DeviceClass (device categories), ResourceClaimTemplate (per-pod template), and ResourceClaim (shared device access) [^139^] [^145^].
- DRA enables precise GPU requests: "I want a GPU with Ampere architecture, at least 20 GB of memory, and compute capability 8.0.0" — the scheduler finds the right device, and the autoscaler provisions the right node [^145^].
- **Kueue** is a Kubernetes-native job queueing system for batch, HPC, AI/ML workloads, providing cluster queues, resource flavors, fair sharing, cohorts, multi-cluster dispatching (MultiKueue), and topology-aware scheduling [^148^] [^157^].
- **Lesson for Cluster OS**: Plugin-based extensibility is essential. DRA's approach of structured device attributes with claim-based allocation is the modern pattern for heterogeneous resource scheduling.

### SLURM — The HPC Gold Standard
- SLURM (Simple Linux Utility for Resource Management) is the workload manager for the world's top supercomputers, handling ~10,000 nodes with hundreds of jobs/second as normal operation [^24^] [^59^].
- SLURM distinguishes between **CPU sockets, cores, and hyperthreads**, and supports **GPU sharding** via Generic Resource Scheduling (GRES) [^24^] [^63^]. GRES enables fine-grained GPU allocation: `Name=gpu File=/dev/nvidia0 CPUs=0,1` binds GPU to specific CPUs.
- **Topology-aware scheduling** is a core SLURM feature — it understands network topology and can assign jobs to nodes physically closest in the network fabric, minimizing latency and maximizing bandwidth [^59^] [^63^]. SLURM integrates TreeMatch for topology-aware task placement optimization [^63^].
- SLURM supports **consumable resources**, **gang scheduling** (all-or-nothing), **fair-share scheduling**, **energy-aware scheduling** (power capping, node hibernation), and **checkpointing** [^63^].
- **Slinky** is a new toolkit enabling SLURM operation in Kubernetes environments, bridging traditional HPC and cloud-native [^59^].
- **Lesson for Cluster OS**: Fine-grained resource typing (socket/core/thread/GPU affinity), topology awareness, and consumable resource models are essential for heterogeneous clusters. SLURM's model of resource binding (GPU-to-CPU affinity) directly applies to our multi-vendor GPU support needs.

### HashiCorp Nomad — Lightweight Multi-Datacenter Scheduling
- Nomad is a **single-binary** workload orchestrator supporting Docker containers, raw executables, Java applications, and Windows IIS — enabling mixed workload types without forcing everything into containers [^93^].
- Nomad demonstrated scheduling **2,000,000 Docker containers on 6,100 hosts across 10 AWS regions in 22 minutes using just 3 schedulers** [^94^]. Kubernetes's practical ceiling is ~5,000 nodes.
- Nomad uses a **shared-state scheduler architecture** — between monolithic and fully distributed [^161^]. It supports multi-dimensional resource granularity, pluggable logic, and multi-scheduler deployments.
- **Device plugins** (introduced in Nomad 0.9, 2019) enable extensible hardware discovery and scheduling [^107^]. The NVIDIA GPU device plugin fingerprints GPU model, memory, clock speeds, driver version, and PCI bandwidth for scheduling decisions.
- Nomad supports **multi-region and multi-datacenter deployments** natively with region-aware scheduling and failover patterns [^101^] [^104^].
- **Lesson for Cluster OS**: Single-binary deployment simplicity is powerful. Device plugin architecture for heterogeneous hardware fingerprinting is a proven pattern. Multi-region support matters for distributed clusters.

### HTCondor — Opportunistic Computing Pioneer
- HTCondor pioneered the **ClassAds matchmaking framework** for distributed resource management in heterogeneous, distributively-owned resource pools [^130^].
- ClassAds (Classified Advertisements) use a semi-structured data model that combines schema, data, and query in a single specification language [^130^]. Resources advertise capabilities and constraints; jobs advertise requirements and preferences — the matchmaker pairs compatible agents.
- **Key innovation**: Clean separation of matching and claiming phases. The matchmaker finds compatible pairs; a separate claiming protocol confirms the match and establishes allocation [^130^].
- **Flocking** allows Condor pools to share resources across administrative boundaries, enabling cross-organizational resource sharing [^132^].
- **Opportunistic scheduling**: Condor can harness idle desktop workstations for compute, checkpointing and migrating jobs when the owner returns [^132^].
- **Lesson for Cluster OS**: The ClassAds model of bilateral/multilateral matchmaking with separated matching and claiming phases is directly applicable to our heterogeneous cluster with dynamic join/leave. Resource and job advertisements with constraint-based matching enables truly opportunistic resource aggregation.

### Apache YARN — Hadoop Resource Management
- YARN (Yet Another Resource Negotiator) uses a centralized ResourceManager with NodeManagers on each node and per-job ApplicationMasters [^94^].
- YARN supports pluggable scheduling policies: **CapacityScheduler** (fixed capacity queues), **FairScheduler** (fair share allocation), and **Dominant Resource Fairness (DRF)** [^94^].
- YARN's architecture is monolithic/two-level hybrid — less flexible than Mesos's framework-level scheduling because application logic cannot choose resources freely [^161^].
- **Lesson for Cluster OS**: Queue-based resource management with fair sharing is a proven pattern. DRF (Dominant Resource Fairness) for multi-dimensional resource allocation is the standard fairness algorithm.

### GPU Scheduling — The Multi-Vendor Challenge
- Kubernetes GPU scheduling has evolved through three generations: (1) Device Plugin (count-based), (2) DRA (attribute-based, GA 2025), (3) Software-defined sharing (HAMi, TensorFusion) [^139^] [^145^].
- **NVIDIA device plugin** supports three GPU sharing modes: **Time-Slicing** (context-switching, no isolation), **MPS** (memory/compute partitioning, software-level), **MIG** (hardware isolation, A100/H100 only) [^85^] [^86^].
- **HAMi** (CNCF Sandbox project) achieves GPU virtualization via CUDA API interception — no driver changes, no application changes. Multiple pods share a physical GPU with enforced VRAM limits. Case study: Ke Holdings improved GPU utilization from 13% to 37% with 10,000+ concurrent pods [^150^] [^152^].
- **HAMi supports NVIDIA, AMD, Intel, Huawei Ascend, Baidu Kunlun, and other GPUs** — making it the most vendor-agnostic GPU sharing solution [^153^].
- **Volcano** (CNCF incubating) adds batch scheduling to Kubernetes: gang scheduling, fair-share, queue management, job lifecycle, and heterogeneous device scheduling for GPU/NPU [^149^] [^152^].
- **Lesson for Cluster OS**: HAMi's approach of API interception for cross-vendor GPU virtualization is directly applicable. We need a unified GPU abstraction that works across NVIDIA, AMD, Intel, and Apple GPUs. DRA's structured resource claims are the Kubernetes-native pattern to follow.

### NUMA-Aware Scheduling — Memory Locality Matters
- NUMA (Non-Uniform Memory Access) architecture means CPUs access local memory 2-3x faster than remote memory. Cross-NUMA access destroys performance for high-throughput applications [^96^] [^103^].
- Kubernetes provides NUMA-aware scheduling via: **CPU Manager** (static policy for CPU pinning), **Memory Manager** (NUMA-aware allocation, stable in K8s 1.27+), **Topology Manager** (hint-based coordination), and **Device Manager** [^96^] [^97^] [^103^].
- Topology Manager policies: **best-effort**, **restricted**, **single-numa-node** [^97^]. The scheduler-plugins repo provides NodeResourceTopology for NUMA-aware scoring.
- NUMA-aware scheduling strategies: **LeastAllocated** (spread), **MostAllocated** (consolidate), **BalancedAllocation** (prevent skew) [^99^].
- Scoring formula for NUMA affinity: `score = weight x (100 - 100 x numaNodeNum/maxNumaNodeNum)` — favor pods requiring fewer NUMA nodes [^106^].
- **Lesson for Cluster OS**: NUMA topology discovery and affinity scheduling are essential for performance. For heterogeneous clusters mixing Intel i7, AMD Ryzen 9, and Apple Silicon M3 Pro, each with different NUMA topologies, we must model and schedule for memory locality.

### Distributed Resource Pooling — CPU Aggregation Models
- **Ray** (from UC Berkeley RISELab) achieves **millions of tasks per second with sub-millisecond latency** through distributed scheduling [^125^] [^126^]. Ray uses a bottom-up distributed scheduling strategy with a sharded metadata store and stateless components.
- Ray is **heterogeneity-aware** — it allows resource requirements at task/actor granularity, scheduling CPU-only tasks on cheaper high-CPU instances while reserving GPUs for GPU tasks. This reduced PPO training costs by 4.5x [^126^] [^127^].
- **openMOSIX** used probabilistic, decentralized load information dissemination with process migration for CPU aggregation across network nodes [^131^]. Each node randomly informs two other nodes of its load every second. The system normalizes load to CPU speed using the maximum CPU speed in the system versus the local node's speed.
- **Lesson for Cluster OS**: Ray's distributed scheduling architecture and heterogeneity-aware resource allocation at task granularity is the gold standard for fine-grained workloads. The openMOSIX approach of normalized load metrics accounting for CPU speed differences directly applies to our Intel i7 / AMD Ryzen 9 / Apple M3 Pro heterogeneity.

### Scheduler Architecture Comparison
- **Monolithic** (Kubernetes, Borg): Single scheduler sees all state, good placement quality, potential scalability bottleneck. Borg handles 10K+ nodes, 10K tasks/minute [^150^].
- **Two-level** (Mesos, YARN): Resource manager + framework schedulers. Frameworks hide information, cannot make globally optimal decisions, no preemption support [^150^] [^155^].
- **Shared-state** (Omega, Nomad, Apollo): Multiple schedulers access shared cluster state, optimistic concurrency control, parallelism without head-of-line blocking. Omega demonstrated this is the sweet spot for scalability and placement quality [^155^].
- **Fully-distributed** (Sparrow): Randomized sampling, extremely low latency, but poor placement quality due to limited cluster state visibility [^161^].
- **Lesson for Cluster OS**: Shared-state with optimistic concurrency control (Omega model) offers the best tradeoff. Multiple specialized schedulers (one for latency-sensitive tasks, one for batch jobs) share a common resource state view without locking.

### Multi-Cluster Resource Federation
- **Admiralty** enables multi-cluster pod scheduling across Kubernetes clusters using virtual-kubelet-based delegation. Pods annotated with `multicluster.admiralty.io/elect` are transparently distributed across clusters [^156^].
- **Kueue's MultiKueue** enables searching for capacity across clusters and offloading workloads from the main cluster [^148^].
- **Liqo** provides cluster-to-cluster resource peering, enabling dynamic resource sharing between clusters.
- **Lesson for Cluster OS**: Multi-cluster federation patterns (virtual delegation, capacity search, resource peering) apply directly to our dynamic join/leave architecture. Nodes joining/leaving the cluster can be modeled as mini-clusters with resource delegation.

---

## Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **Apache Mesos** (retired 2025) | Pioneering two-level scheduler; lessons on why pessimistic locking fails and shared-state wins |
| **Kubernetes / kube-scheduler** | Dominant container scheduler with Scheduling Framework V2, 10 extension points, DRA (GA 2025) |
| **kubernetes-sigs/scheduler-plugins** | Official out-of-tree plugins: Coscheduling, NodeResourceTopology, Trimaran, Capacity Scheduling |
| **SLURM** | HPC gold standard: topology-aware, GPU sharding, consumable resources, 10K+ node scale |
| **HashiCorp Nomad** | Lightweight single-binary scheduler, device plugins, multi-region, 2M containers on 6.1K hosts demo |
| **HTCondor** (UW-Madison) | ClassAds matchmaking, opportunistic computing, flocking — foundational for heterogeneous resource pools |
| **Apache YARN** | Hadoop ecosystem resource manager, CapacityScheduler/FairScheduler, DRF fairness |
| **Volcano** (CNCF incubating) | Kubernetes batch scheduler: gang scheduling, fair-share, queue management, GPU/NPU support |
| **Kueue** (Kubernetes SIG) | Kubernetes-native job queueing: resource flavors, cluster queues, multi-cluster (MultiKueue) |
| **Ray** (UC Berkeley / Anyscale) | Distributed AI framework: sub-ms task scheduling, heterogeneity-aware, millions of tasks/sec |
| **HAMi** (CNCF Sandbox) | GPU virtualization via CUDA interception, cross-vendor (NVIDIA/AMD/Intel/Huawei/Baidu), 10K+ pods |
| **NVIDIA GPU Operator** | Full GPU stack: device plugin, MIG, time-slicing, MPS, GPU Feature Discovery, DCGM monitoring |
| **Dynamic Resource Allocation (DRA)** | Kubernetes-native device allocation with structured attributes, GA in K8s 1.34 |
| **OpenMOSIX / LinuxPMI** | Historical process migration for CPU aggregation across heterogeneous nodes |
| **TreeMatch** | Topology-aware task placement integrated into SLURM for network-aware scheduling |
| **Admiralty** | Multi-cluster Kubernetes scheduler using virtual-kubelet delegation |
| **Red Hat / OpenShift NUMA-aware scheduling** | Production NUMA scheduling with CPU Manager, Memory Manager, Topology Manager |

---

## Trends & Signals

1. **DRA replacing Device Plugins**: Kubernetes Dynamic Resource Allocation (GA in 1.34, 2025) replaces the simple count-based device plugin model with structured attribute-based resource claims [^139^] [^145^]. This enables fine-grained heterogeneous device scheduling.

2. **GPU virtualization becoming standard**: HAMi (CNCF Sandbox) and similar projects are making fractional GPU sharing with memory isolation the default. GPU utilization improvements of 2-3x are consistently reported [^150^] [^152^].

3. **Kubernetes-native batch scheduling maturing**: Kueue + Volcano + Coscheduling plugin now provide SLURM-like capabilities within Kubernetes, including gang scheduling, fair-share, and queue management [^148^] [^149^].

4. **NUMA-awareness becoming production requirement**: Topology-aware scheduling with Memory Manager, CPU Manager, and NUMA scoring plugins is now standard for performance-critical workloads [^96^] [^103^].

5. **Multi-cluster resource federation**: MultiKueue, Admiralty, and Liqo enable treating multiple clusters as unified resource pools — directly applicable to dynamic node join/leave scenarios [^148^] [^156^].

6. **Shared-state scheduler architectures winning**: The monolithic vs. two-level debate has resolved in favor of shared-state (Omega model) with optimistic concurrency control, adopted by Kubernetes (Borg heritage) and Nomad [^155^] [^161^].

7. **Heterogeneity-aware scheduling at task granularity**: Ray demonstrated that scheduling CPU-only tasks on cheaper instances while reserving GPUs for GPU tasks reduces costs by 4.5x [^126^]. Fine-grained resource requirements per task (not per job) is the future.

---

## Controversies & Conflicting Claims

1. **Kubernetes vs. SLURM for HPC**: Kubernetes lacks native gang scheduling (being addressed in K8s 1.35+ with built-in gang scheduling [^144^] and by Volcano/Kueue). SLURM purists argue Kubernetes will never match SLURM's topology-aware scheduling and MPI integration. However, Slinky bridges SLURM into Kubernetes, suggesting convergence rather than competition [^59^] [^66^].

2. **GPU sharing: hardware vs. software isolation**: NVIDIA's MIG provides hardware isolation but only on A100/H100. Time-slicing works everywhere but has no isolation. MPS provides memory/compute limits but no fault isolation. HAMi provides VRAM limits via software interception but adds overhead. The industry has not converged on a single approach [^86^] [^150^].

3. **Mesos two-level scheduling: elegant but impractical?**: Mesos's two-level scheduling was academically elegant but "the pessimistic two level offer model makes it hard for second level scheduler to make optimal decisions" [^105^]. Some argue Mesos was "doomed from the start" because it was an academic project without production hardening [^105^]. Others (including Mesos co-founder) argue it was simply mismanaged by Mesosphere and couldn't compete with Google's Borg experience backing Kubernetes [^105^].

4. **Centralized vs. distributed scheduling**: Google's Borg uses a centralized scheduler at 10K+ node scale successfully, disproving the claim that monolithic schedulers can't scale [^150^]. However, Google also developed Omega (shared-state) to address Borg's limitations, and Kubernetes inherited these lessons [^155^].

5. **Nomad vs. Kubernetes simplicity**: Nomad advocates claim single-binary simplicity with 2M container demo proves Kubernetes is overengineered [^94^]. Kubernetes advocates argue Nomad's smaller ecosystem and reliance on Consul/Vault make it less suitable for complex deployments [^93^].

---

## Recommended Deep-Dive Areas

1. **Ray's distributed scheduling architecture**: Ray's bottom-up distributed scheduling with a sharded metadata store achieves sub-millisecond task scheduling at millions of tasks/sec. This architecture is directly applicable to our Cluster OS for fine-grained heterogeneous task scheduling. The heterogeneity-aware cost reduction (4.5x) proves the value of task-level resource specification [^126^] [^127^].

2. **HTCondor ClassAds matchmaking for dynamic pools**: The ClassAds framework of bilateral constraint matching with separated matching/claiming phases is the most mature approach for opportunistic resource aggregation with dynamic join/leave. The matchmaking language can express complex resource constraints and preferences [^130^] [^133^].

3. **HAMi GPU virtualization for multi-vendor GPU support**: HAMi's CUDA API interception approach enables GPU sharing across NVIDIA, AMD, Intel, Huawei Ascend, and Baidu Kunlun without driver changes. For our Cluster OS needing to support NVIDIA, AMD, Intel, and Apple GPUs, understanding HAMi's architecture is critical [^150^] [^152^].

4. **SLURM's consumable resource model and GRES**: SLURM's approach to consumable resources (including GPU-to-CPU affinity binding) and Generic Resource Scheduling provides a proven model for fine-grained heterogeneous resource allocation. The topology-aware TreeMatch integration is essential for network-aware placement [^63^].

5. **Kubernetes DRA + Kueue integration**: DRA's structured resource claims combined with Kueue's job queueing, resource flavors, and multi-cluster dispatching represent the state-of-the-art for Kubernetes-native heterogeneous resource management. The ResourceFlavor concept for distinguishing x86 vs ARM, different GPU types, and pricing models is directly applicable [^154^] [^157^].

6. **openMOSIX load normalization for heterogeneous CPUs**: The approach of normalizing load metrics to CPU speed using max(system_speed)/local_speed for probabilistic load balancing across heterogeneous CPUs is directly applicable to Intel i7 / AMD Ryzen 9 / Apple M3 Pro mixing [^131^].

7. **Nomad device plugin architecture**: Nomad's approach to hardware fingerprinting with vendor/type/model hierarchy, affinities, and constraints provides a clean model for heterogeneous device discovery and scheduling. The single-binary deployment model is also worth emulating [^107^] [^155^].

---

## Raw Evidence Log

### Evidence 1: Mesos Retirement and Two-Level Scheduling Limitations
**Claim**: Mesos's pessimistic two-level offer model made it hard for second-level schedulers to make optimal decisions, and the C++ threading model limited ecosystem growth.
**Source**: Hacker News — Apache Mesos to be moved to Attic
**URL**: https://news.ycombinator.com/item?id=26713082
**Date**: 2021-04-06
**Excerpt**: "Mesos's pessimistic two level offer model makes it hard for second level scheduler to make optimal decisions because it might not get the _right_ offer it needs. At the same time, first level scheduler lacks application specific information to make the right decision to send the right offer to the second level scheduler, thus the problem. We evaluated many first level scheduling algorithms, and ironically found that 'random' first level scheduler sometimes works better than DRF for long running services scheduling." / "Mesos uses a component called libprocess (think of it as C++ version of erlang). Each actor in the system (mesos master, mesos agent) is single threaded. Thus, all i/o operations need to be non-blocking to not block the actor. This makes it hard to integrate 3rdparty C++ libraries, especially those that involves I/O as they might have a different threading model."
**Context**: Core Mesos developer explaining fundamental architectural limitations that led to Kubernetes's success
**Confidence**: High

### Evidence 2: Kubernetes Scheduling Framework V2
**Claim**: Kubernetes Scheduling Framework V2 provides 10 extension points enabling flexible scheduler customization without forking.
**Source**: ScaleOps Blog — The Kubernetes Scheduler
**URL**: https://scaleops.com/blog/kubernetes-scheduler/
**Date**: 2026-05-25
**Excerpt**: "The framework is what makes the scheduler extensible without forking the binary. Want gang scheduling for ML batch jobs that must start together? Add the Coscheduling plugin at Permit. Need topology-aware placement for NUMA-sensitive workloads? Use NodeResourceTopology at Filter and Score. Network-aware scheduling for low-latency services? The NetworkAware plugin scores Nodes by inter-Pod latency."
**Context**: Comprehensive overview of Kubernetes scheduler architecture and extension points
**Confidence**: High

### Evidence 3: SLURM Topology-Aware Scheduling
**Claim**: SLURM understands network topology and assigns jobs to physically close nodes, with GPU resource management and TreeMatch integration.
**Source**: NVIDIA — Slurm: Open Source HPC and AI Workload Manager
**URL**: https://www.nvidia.com/en-us/software/slurm/
**Date**: 2026-05-27
**Excerpt**: "Leverage Slurm's understanding of complex network and system topologies to enable efficient workload placement on multi-tier interconnects. Minimize latency, maximize bandwidth, and improve end-to-end job performance." / "Combined with GPU-aware and policy-driven resource allocation, teams can run distributed workloads predictably without waiting on lower-priority or poorly placed jobs."
**Context**: Official NVIDIA documentation for SLURM GPU scheduling
**Confidence**: High

### Evidence 4: SLURM Architecture — 10,000 Nodes Normal
**Claim**: SLURM handles ~10,000 nodes with hundreds of jobs/second and can distinguish CPU sockets, cores, hyperthreads with GPU sharding support.
**Source**: Rafay — Slurm Architecture Explained for HPC Workloads
**URL**: https://rafay.co/ai-and-cloud-native-blog/introduction-to-slurm-the-backbone-of-hpc
**Date**: 2025-11-03
**Excerpt**: "Handling ~10,000 nodes with 100s of jobs/second would be considered normal for Slurm. User can configure Slurm in a very fine grained manner. For example, Slurm can even distinguish between CPU sockets, cores and hyperthreads. As a user, you can select a CPU that is in proximity to a PCI bus (reducing latency). Slurm also supports GPU sharding. Slurm understands network topology."
**Context**: Architecture overview of SLURM for HPC workloads
**Confidence**: High

### Evidence 5: Nomad Device Plugins and GPU Scheduling
**Claim**: Nomad 0.9+ device plugins enable extensible hardware discovery and scheduling with fingerprinting of GPU model, memory, clock speeds, driver version.
**Source**: NVIDIA Developer Blog — Using HashiCorp Nomad to Schedule GPU Workloads
**URL**: https://developer.nvidia.com/blog/hashicorp-nomad-gpu-scheduling/
**Date**: 2023-02-13 (original 2019-05-06)
**Excerpt**: "The NVIDIA device plugin first scans the client node for suitable NVIDIA GPUs, then fingerprints their hardware and capabilities, including clock speeds, driver version, and memory size. The plugin ultimately reports discovered devices as NVIDIA resources for the node." / "Users can indicate their requirements to varying degrees of specificity. For example, a user can specify `nvidia/gpu` to get any NVIDIA GPU, or they can specify the exact model they want, such as `nvidia/gpu/1080ti`."
**Context**: Official NVIDIA blog on Nomad GPU scheduling architecture
**Confidence**: High

### Evidence 6: Nomad Scale — 2M Containers on 6,100 Hosts
**Claim**: Nomad scheduled 2,000,000 Docker containers on 6,100 hosts in 10 AWS regions in 22 minutes using 3 schedulers.
**Source**: Devoteam — Is HashiCorp Nomad Right for Your Container Orchestration?
**URL**: https://www.devoteam.com/expert-view/is-hashicorp-nomad-a-smart-choice-for-your-container-orchestration/
**Date**: 2024-11-06
**Excerpt**: "During a demonstration in 2020, HashiCorp was able to schedule 2,000,000 Docker containers on 6,100 hosts in 10 AWS regions in 22 minutes using just three schedulers. More than a little impressive when you consider that the operating ceiling for Kubernetes is 5,000 nodes and 300,000 containers."
**Context**: Comparison of Nomad and Kubernetes scalability
**Confidence**: Medium (HashiCorp's own demo, not independent)

### Evidence 7: HTCondor ClassAds Matchmaking
**Claim**: ClassAds provide a flexible semi-structured data model combining schema, data, and query for distributed resource matchmaking.
**Source**: HPDC '98 Paper — Matchmaking: Distributed Resource Management for High Throughput Computing
**URL**: https://chtc.cs.wisc.edu/doc/hpdc98.pdf
**Date**: 1998
**Excerpt**: "We argue that this paradigm does not adapt well to distributed systems, particularly those built to support high-throughput computing. Obstacles include heterogeneity of resources, which make uniform allocation algorithms difficult to formalize, and distributed ownership, leading to widely varying allocation policies."
**Context**: Foundational paper on matchmaking for heterogeneous distributed systems
**Confidence**: High

### Evidence 8: Ray Distributed Heterogeneity-Aware Scheduling
**Claim**: Ray achieves millions of tasks per second with sub-millisecond latency and reduces costs by 4.5x through heterogeneity-aware scheduling.
**Source**: Ray Paper (UC Berkeley EECS Technical Report)
**URL**: https://escholarship.org/content/qt3r5069pj/qt3r5069pj_noSplash_e9af79b51aa5bfb303f19234f1e4c665.pdf
**Date**: 2019
**Excerpt**: "Ray's ability to handle resource heterogeneity also decreased PPO's cost by a factor of 4.5, since CPU-only tasks can be scheduled on cheaper high-CPU instances. In contrast, MPI applications often exhibit symmetric architectures, in which all processes run the same code and require identical resources, in this case preventing the use of CPU-only machines for scale-out."
**Context**: Academic paper on Ray's distributed scheduling architecture
**Confidence**: High

### Evidence 9: Kubernetes DRA Goes GA
**Claim**: Dynamic Resource Allocation became GA in Kubernetes 1.34, replacing device plugins with structured resource descriptions.
**Source**: The New Stack — Kubernetes: Get the Most from Dynamic Resource Allocation
**URL**: https://thenewstack.io/kubernetes-get-the-most-from-dynamic-resource-allocation/
**Date**: 2025-12-23
**Excerpt**: "DRA is a richer replacement for device plug-ins. The old school of plug-ins could only provide a count of how many devices were available on a node. With DRA, each device is described with a set of attributes, called ResourceSlice, that may include the amount of memory available, or number of compute cores." / "You can arbitrarily mix-and-match at arbitrarily as needed by your workload."
**Context**: DRA GA announcement and technical overview
**Confidence**: High

### Evidence 10: HAMi GPU Virtualization — Cross-Vendor Support
**Claim**: HAMi achieves GPU virtualization via CUDA API interception, supports 10,000+ concurrent pods, and works across NVIDIA, AMD, Intel, Huawei, and Baidu GPUs.
**Source**: CNCF Case Studies / HAMi documentation
**URL**: https://project-hami.io/docs/next/core-concepts/gpu-virtualization
**Date**: 2026
**Excerpt**: "HAMi takes a different approach: no driver changes, no application changes — it achieves GPU virtualization at the software layer through CUDA API interception. Multiple Pods share the same physical GPU, and each Pod can only 'see' the VRAM it requested."
**Context**: CNCF Sandbox project documentation
**Confidence**: High

### Evidence 11: GPU Sharing Comparison — MIG vs MPS vs Time-Slicing
**Claim**: Three GPU sharing approaches have different isolation/fault models: Time-slicing (no isolation), MIG (hardware isolation, A100/H100 only), MPS (software partitioning, no fault isolation).
**Source**: ScaleOps — GPU Sharing in Kubernetes
**URL**: https://scaleops.com/blog/kubernetes-gpu-sharing/
**Date**: 2026-05-26
**Excerpt**: "Time-slicing trades the memory and fault-isolation that is provided by MIG for the ability to share a GPU by a larger number of users. Time-slicing also provides a way to provide shared access to a GPU for older generation GPUs that do not support MIG."
**Context**: Comprehensive comparison of GPU sharing methods in Kubernetes
**Confidence**: High

### Evidence 12: NUMA-Aware Scheduling Strategies
**Claim**: NUMA-aware scheduling uses scoring strategies (LeastAllocated, MostAllocated, BalancedAllocation) to optimize workload placement across NUMA zones.
**Source**: OKD Documentation — Scheduling NUMA-aware workloads
**URL**: https://docs.okd.io/4.21/scalability_and_performance/cnf-numa-aware-scheduling.html
**Date**: 2019-09-10
**Excerpt**: "A CPU processing a workload using memory that is outside its NUMA zone is slower than a workload processed in a single NUMA zone. For I/O-constrained workloads, the network interface on a distant NUMA zone slows down how quickly information can reach the application."
**Context**: Official OKD documentation on NUMA scheduling
**Confidence**: High

### Evidence 13: Omega Shared-State Scheduler Architecture
**Claim**: Omega's shared-state architecture with optimistic concurrency control addresses both monolithic scalability limits and two-level scheduling information hiding.
**Source**: Omega Paper Presentation — flexible, scalable schedulers for large compute clusters
**URL**: https://csc.csudh.edu/btang/seminar/slides/Omega-Matt_Levan.pdf
**Date**: 2013 (paper)
**Excerpt**: "Monolithic schedulers risk becoming scalability bottlenecks. Two-level schedulers limit resource visibility and parallelism. A new scheduler architecture (Omega, heir of Borg) utilizes shared-state, parallelism, and optimistic concurrency control to enable implementation extensibility and performance scalability."
**Context**: Presentation of the Omega paper from Google
**Confidence**: High

### Evidence 14: openMOSIX Load Normalization for Heterogeneous CPUs
**Claim**: openMOSIX normalized load to CPU speed using max(system_speed)/local_speed for probabilistic load balancing across heterogeneous processors.
**Source**: Load Balancing Experiments in openMOSIX
**URL**: https://aritter.github.io/tuning_mosix.pdf
**Date**: N/A (research paper)
**Excerpt**: "The accumulated load is first normalized to the CPU speed of this processor using the maximum CPU speed in the system versus the local node's calculated CPU speed. The load is computed as a combination of the old load modified by a decay value added to the newly accumulated load number."
**Context**: Research paper on openMOSIX load balancing for heterogeneous clusters
**Confidence**: High

### Evidence 15: Kueue Resource Flavors for Heterogeneous Resources
**Claim**: Kueue's ResourceFlavor enables fine-grained resource management by associating workloads with specific node characteristics including architecture (x86 vs ARM).
**Source**: Microsoft AKS Documentation — Schedule and Deploy Batch Jobs with Kueue
**URL**: https://learn.microsoft.com/en-us/azure/aks/deploy-batch-jobs-with-kueue
**Date**: 2025-09-26
**Excerpt**: "ResourceFlavors can define the characteristics like pricing, availability, brands, models, and architecture (that is, x86 versus ARM CPUs). A ClusterQueue uses these flavors to manage quotas and admission policies for workloads."
**Context**: Official Microsoft documentation for Kueue on AKS
**Confidence**: High

### Evidence 16: Scheduler Architecture Comparison Table
**Claim**: Different scheduler architectures (monolithic, two-level, shared-state, distributed) have distinct tradeoffs across features.
**Source**: CamSaS Blog — The evolution of cluster scheduler architectures
**URL**: https://www.cl.cam.ac.uk/research/srg/netos/camsas/blog/2016-03-09-scheduler-architectures.html
**Date**: 2016-03-09
**Excerpt**: "Kubernetes: monolithic, multi-dimensional resources, pluggable logic, oversubscription supported. Mesos: two-level, multi-dimensional, framework-level pluggable, no preemption. Nomad: shared-state, multi-dimensional, pluggable, no preemption. Borg: monolithic, multi-dimensional, priority preemption, re-scheduling, resource estimation. Omega: shared-state, multi-dimensional, all features."
**Context**: Academic comparison of cluster scheduler architectures from Cambridge
**Confidence**: High

### Evidence 17: Volcano — CNCF Batch Scheduler
**Claim**: Volcano provides gang scheduling, fair-share, queue management, and heterogeneous device scheduling (GPU/NPU) as a Kubernetes-native batch system.
**Source**: Volcano Official Documentation
**URL**: https://volcano.sh/en/docs/
**Date**: 2019-2026
**Excerpt**: "Volcano is a cloud native system for high-performance workloads. Volcano supports popular computing frameworks such as Spark, TensorFlow, PyTorch, Flink, Argo, MindSpore, PaddlePaddle and Ray. Volcano also provides various scheduling capabilities including heterogeneous device scheduling, network topology-aware scheduling, multi-cluster scheduling."
**Context**: Official Volcano project documentation
**Confidence**: High

### Evidence 18: Apache Mesos Officially Retired (Attic)
**Claim**: Apache Mesos was moved to the Apache Attic in August 2025 after years of decline.
**Source**: Apache Attic — Mesos
**URL**: https://attic.apache.org/projects/mesos.html
**Date**: 2025
**Excerpt**: "Mesos became a Top Level Project in June 2013, retired in August 2025 and the move to the Attic was completed in October 2025. Mesos was a cluster manager that provides efficient resource isolation and sharing across distributed applications."
**Context**: Official Apache Attic page for retired Mesos project
**Confidence**: High

### Evidence 19: Ray Sub-Millisecond Scheduling for Heterogeneous Workloads
**Claim**: Ray achieves millions of tasks per second with sub-millisecond latency through distributed scheduling and sharded metadata store.
**Source**: Introl — Ray Clusters for AI
**URL**: https://introl.com/blog/ray-clusters-distributed-ai-computing-infrastructure-guide-2025
**Date**: 2026-01-15
**Excerpt**: "Achieving millions of tasks per second with sub-millisecond latency—order of magnitude faster than Spark for AI patterns. Native heterogeneous compute supporting CPU/GPU workload mixing."
**Context**: Architecture guide for Ray clusters
**Confidence**: Medium

### Evidence 20: Kubernetes Gang Scheduling in 1.35
**Claim**: Kubernetes 1.35 introduced built-in workload-aware scheduling with gang scheduling support.
**Source**: Kubernetes Blog — Kubernetes v1.35: Introducing Workload Aware Scheduling
**URL**: https://kubernetes.io/blog/2025/12/29/kubernetes-v1-35-introducing-workload-aware-scheduling/
**Date**: 2026-01-03
**Excerpt**: "The `gang` policy enforces all-or-nothing placement. Without gang scheduling, a Job might be partially scheduled, consuming resources without being able to run, leading to resource wastage and potential deadlocks."
**Context**: Official Kubernetes blog on new scheduling features
**Confidence**: High

---

## Cross-Cutting Analysis for Cluster OS Design

### Architecture Recommendations

Based on this research, the Cluster OS resource scheduler should adopt a **shared-state architecture with optimistic concurrency control** (the Omega model), combining the best aspects of Kubernetes, Nomad, and SLURM:

1. **Shared Resource State**: Maintain a consistent view of all resources (Intel i7, AMD Ryzen 9, Apple M3 Pro, various GPUs) in a sharded metadata store, following Ray's and Omega's approach.

2. **Plugin-Based Extensibility**: Implement a scheduling framework with extension points (Filter, Score, Bind) similar to Kubernetes Scheduling Framework V2, enabling custom schedulers for different workload types.

3. **ClassAds-Style Matchmaking**: Adopt HTCondor's ClassAds model for resource/job advertisement with constraint-based matching, enabling opportunistic dynamic join/leave.

4. **Normalized Resource Model**: Follow openMOSIX's approach of normalizing compute capacity across heterogeneous CPUs (accounting for different performance levels of Intel i7, AMD Ryzen 9, Apple M3 Pro).

5. **Device Plugin Architecture**: Implement Nomad-style device plugins for hardware fingerprinting, enabling automatic discovery of GPU models, capabilities, and topology.

6. **DRA-Style Resource Claims**: Use structured resource attributes with claim-based allocation (like Kubernetes DRA) rather than simple count-based allocation.

7. **NUMA Topology Awareness**: Integrate topology discovery and NUMA-aware scoring for memory locality optimization.

8. **Multi-Level Resource Typing**: Follow SLURM's model distinguishing sockets/cores/threads with GPU affinity binding.

9. **Queue-Based Fair Sharing**: Implement Kueue-style cluster queues with resource flavors, cohorts, and DRF-based fair sharing.

10. **Heterogeneity-Aware Task Scheduling**: Follow Ray's model of per-task resource requirements enabling CPU tasks on cheaper/available cores while reserving specialized hardware for appropriate workloads.

### Open Questions Requiring Further Investigation

1. How to model Apple Silicon's unified memory architecture (CPU and GPU share the same memory pool) in a resource scheduler designed primarily for discrete GPU systems?

2. What is the optimal granularity of resource description for a personal cluster (nodes of 4-10) versus data center scale (1000+ nodes)?

3. How to handle the fundamental difference between x86_64 and ARM64 instruction sets — should the scheduler treat ISA as a hard constraint or use some form of binary translation/ emulation capability?

4. What process migration mechanisms (if any) are viable for live workload redistribution across heterogeneous nodes with different ISAs and GPU vendors?

5. How to incorporate thermal and power constraints into scheduling decisions for a personal/homelab cluster where power budget is limited?
