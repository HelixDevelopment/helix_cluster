## Facet: Resource Abstraction, Aggregation & Unified Scheduler

### Key Findings

1. **Kubernetes Scheduling Framework V2 defines 12 extension points** — not 10 as commonly cited — including PreEnqueue, QueueSort, PreFilter, Filter, PostFilter, PreScore, Score, Reserve, Permit, PreBind, Bind, and PostBind. The framework enables asynchronous preemption (alpha since v1.32) and dynamic resource allocation (GA since v1.34). [^336^] [^337^]

2. **SLURM's backfill scheduler improves system utilization significantly** by starting lower-priority jobs if doing so does not delay the expected start time of any higher-priority job. Without backfill, each partition is scheduled strictly in priority order, resulting in "significantly lower system utilization." [^340^] [^342^]

3. **HTCondor ClassAds provide a unique matchmaking paradigm** where both resource providers (machines) and consumers (jobs) advertise constraints and preferences. The matchmaker uses bilateral matching with `Requirements` and `Rank` expressions, enabling policy-heterogeneous distributed ownership. [^338^] [^339^]

4. **Apache Mesos's two-level scheduling failed** because the pessimistic offer model made it hard for second-level schedulers to make optimal decisions — they might not get the right offer. The first-level scheduler lacked application-specific information. A former Mesos developer noted: "We evaluated many first level scheduling algorithms, and ironically found that 'random' first level scheduler sometimes works better than DRF for long running services scheduling." [^105^]

5. **Omega introduced shared-state optimistic concurrency** as a third way between monolithic and two-level scheduling. Multiple schedulers compete in a "free-for-all" with complete cluster visibility, using atomic transactions to update shared cell state. Performance is "determined by the frequency at which transactions fail and the costs of such failures." [^343^] [^428^]

6. **Google Borg manages cells of up to tens of thousands of machines** with a monolithic scheduler using score caching, equivalence classes, and relaxed randomization. A busy Borgmaster uses 10-14 CPU cores and up to 50 GiB RAM, handling arrival rates above 10,000 tasks per minute. [^378^] [^379^] [^385^]

7. **Ray achieves sub-millisecond task scheduling and millions of tasks per second** through its distributed bottom-up scheduler architecture. The Global Control Store (GCS) decouples control state from scheduling logic, enabling horizontal scalability. Ray exceeds 1 million tasks/second at 60 nodes and 1.8 million at 100 nodes. [^363^] [^426^]

8. **HAMi (CNCF Sandbox) improves GPU utilization from 13% to 37%** (nearly 3x) through fine-grained vGPU memory and compute slicing with CUDA API interception. A Ke Holdings case study demonstrated 10,000+ pods running concurrently across 5 clusters with heterogeneous GPU models (H200/H100/A100/V100/4090). [^416^]

9. **NUMA-aware scheduling is essential for performance** — cross-NUMA memory access can be 2-3x slower. Kubernetes addresses this through Topology Manager (single-numa-node policy), CPU Manager (static policy for exclusive CPUs), and Memory Manager (static policy for cpuset.mems). The NodeResourceTopology scheduler plugin adds NUMA-aware scoring strategies. [^359^] [^431^] [^433^]

10. **Gang scheduling prevents deadlock and resource fragmentation** in distributed training. Kubernetes 1.35 introduced native gang scheduling (alpha) via the Workload API and PodGroup, with the `GangScheduling` plugin implementing a `Permit` extension point for atomic all-or-nothing scheduling decisions. [^387^] [^383^]

11. **Dominant Resource Fairness (DRF)** generalizes max-min fairness to multiple resources by equalizing each user's dominant share (their largest share of any resource). DRF provides strategy-proofness, share guarantee, Pareto optimality, and envy-freeness — properties that slot-based fairness mechanisms lack. [^398^]

12. **Kubernetes Dynamic Resource Allocation (DRA) graduated to GA in v1.34**, replacing the device plugin framework for advanced use cases. DRA uses ResourceClaim, ResourceSlice, and DeviceClass objects with CEL selection criteria, enabling the scheduler to handle resource allocation for multiple pods in parallel. [^401^] [^392^]

13. **GPU sharing in Kubernetes has three mechanisms**: Time-slicing (CUDA context switching, no isolation), MPS (concurrent kernel execution with memory/compute limits but no fault isolation), and MIG (hardware-level isolation on A100/H100). Time-slicing and MPS are mutually exclusive in the NVIDIA device plugin. [^357^] [^358^]

14. **Volcano scheduler (CNCF incubating)** provides batch scheduling for AI/ML/HPC with gang scheduling, queue-based fair-share (Proportion plugin), and job-level scheduling. PodGroup CRD defines `minMember` for atomic scheduling, and Queue CRD enables weighted resource partitioning across teams. [^414^] [^415^]

15. **Koordinator (Alibaba Cloud)** enhances Kubernetes with QoS-aware co-location, resource overcommitment based on application profiling, fine-grained CPU orchestration, and heterogeneous resource scheduling. It updates the oversubscription formula: `MidAllocatable := min(ProdReclaimable, NodeAllocatable)`. [^443^] [^447^] [^448^]

16. **SLURM's multifactor priority plugin** computes job priority as a weighted sum of 9 factors: age, association, fair-share, job size, nice, partition, QOS, site, and TRES. Fair-share uses an exponential decay formula: `F = 2^(-U/S)` where U is effective usage and S is shares. [^439^] [^440^]

17. **Checkpoint-based preemption** reduces resource wastage by saving preempted task state instead of killing. Analysis of Google cluster traces showed 93% of evictions triggered by priority scheduling, with many low-priority tasks experiencing multiple evictions before completion. [^382^]

18. **Horizontal Pod Autoscaler (HPA) uses the formula**: `desiredReplicas = ceil[currentReplicas * (currentMetricValue / desiredMetricValue)]` with configurable sync period (default 15s), downscale stabilization window (default 5min), and tolerance (default 0.1). The v2 API supports multiple metrics, custom metrics, and configurable scaling behavior. [^393^]

19. **cgroups v2 provides unified resource hierarchy** with Pressure Stall Information (PSI) for measuring resource contention. Kubernetes uses cgroups v2 for precise memory and CPU limit enforcement. eBPF enables kernel-level observability without instrumentation, providing network analysis, syscall tracing, and resource accounting. [^429^] [^430^]

20. **Nomad scheduler** iterates over nodes until finding feasible candidates, then scores them. Without affinity/spread, batch jobs score only 2 nodes; with affinity/spread, it scores up to 100 nodes per allocation. The scheduler supports bin-packing, spread, and affinity/anti-affinity policies. [^362^]

---

### Major Players & Sources

| Entity | Role/Relevance |
|--------|----------------|
| **Kubernetes (SIG-Scheduling)** | Scheduling Framework V2 — 12 extension points, plugin architecture; DRA for device allocation [^336^] [^392^] |
| **SchedMD / SLURM** | Dominant HPC scheduler with multifactor priority, backfill, fairshare, topology-aware scheduling [^340^] [^339^] |
| **HTCondor / CHTC (UW-Madison)** | Pioneer of matchmaking-based resource management with ClassAds [^338^] [^339^] |
| **Google (Borg, Omega)** | Borg: monolithic cell-based scheduling at scale; Omega: shared-state optimistic concurrency [^378^] [^343^] |
| **Apache Mesos (retired to Attic)** | Two-level scheduling architecture; lessons learned informed Kubernetes design [^105^] |
| **Ray / Anyscale** | Distributed bottom-up scheduler with GCS; sub-millisecond task scheduling, millions of tasks/sec [^426^] [^363^] |
| **HashiCorp Nomad** | Lightweight scheduler with bin-packing, spread, affinity policies [^362^] |
| **Volcano (CNCF)** | Batch scheduler for AI/ML/HPC; gang scheduling, queues, fair-share [^414^] |
| **HAMi (CNCF Sandbox)** | Heterogeneous GPU sharing via CUDA API interception; 13% to 37% utilization improvement [^416^] |
| **Koordinator (Alibaba)** | QoS-aware co-location scheduling, resource overcommitment, fine-grained orchestration [^443^] |
| **NVIDIA** | Device plugin, GPU Operator, MIG, MPS, time-slicing for GPU sharing [^357^] [^358^] |
| **Kubernetes Scheduler Plugins SIG** | NodeResourceTopology plugin, Coscheduling plugin, NUMA-aware scoring [^431^] [^433^] |

---

### Trends & Signals

1. **Unified GPU sharing becoming declarative**: HAMi transforms GPU virtualization from "ad-hoc experimentation into engineering capability that can be declared in YAML, scheduled by policy, and validated by metrics." [^416^]

2. **Native gang scheduling entering Kubernetes core**: Kubernetes 1.35 alpha introduces the Workload API and `GangScheduling` plugin as built-in features, reducing dependency on external schedulers like Volcano. [^387^] [^383^]

3. **DRA replacing device plugins for advanced use cases**: Dynamic Resource Allocation graduated to GA in Kubernetes 1.34, enabling fine-grained device selection with CEL expressions, parallel allocation, and hardware-aware scheduling. [^401^]

4. **QoS-aware co-location gaining traction**: Koordinator and similar projects enable safe resource overcommitment by combining application profiling with priority-based preemption, improving cluster utilization without sacrificing SLOs. [^443^] [^448^]

5. **eBPF revolutionizing resource observability**: Kernel-level instrumentation enables real-time resource accounting without sidecars or application changes. Tools like bpftool, BCC, and bpftrace provide deep visibility. [^396^] [^430^]

6. **NUMA-aware scheduling moving from niche to mainstream**: Cloud providers (Alibaba Cloud ACK, AWS) now offer NUMA topology-aware GPU scheduling. The `LeastNUMANodes` scoring strategy works with all Topology Manager policies. [^432^] [^433^]

7. **Checkpoint-based preemption for reducing waste**: Research shows traditional kill-based preemption wastes significant resources; suspend-resume mechanisms can save task progress and reduce total job completion time. [^382^]

---

### Controversies & Conflicting Claims

1. **Monolithic vs. distributed scheduling**: Google's Borg demonstrated monolithic scheduling scales to 10,000+ nodes per cell, contradicting assumptions that centralized schedulers are inherently unscalable. As noted in the Borg paper: "so far, every time we have approached a limit, we've managed to eliminate it." [^385^] However, Omega's shared-state design was created specifically because Borg's scheduler was "starting to show signs of strain." [^343^]

2. **Mesos two-level scheduling vs. Kubernetes single-level**: Mesos's offer-based model was criticized for restricting second-level scheduler visibility. A former developer admitted: "Mesos's pessimistic two level offer model makes it hard for second level scheduler to make optimal decisions." Yet some argue it provided stronger isolation guarantees. [^105^]

3. **GPU sharing isolation tradeoffs**: Time-slicing (no isolation, works everywhere) vs. MIG (hardware isolation, limited GPUs) vs. MPS (software limits, fault propagation risk). The industry consensus: "You can always loosen isolation later. You can't add it after." [^360^]

4. **DRF vs. slot-based fairness**: DRF provides strategy-proofness and share guarantee but may reduce overall utilization compared to slot-based approaches. Experiments show DRF can be more fair while slot-based approaches achieve better utilization in some scenarios. [^398^]

5. **Oversubscription safety**: Some systems (ROSE, Koordinator) aggressively oversubscribe resources for better utilization, while others are conservative to minimize interference. Coach (Microsoft Research) explicitly trades off additional savings for "minimizing the chance of contention." [^384^] [^388^]

---

### Recommended Deep-Dive Areas

1. **HAMi GPU sharing internals**: How CUDA API interception works at the library level, performance overhead of vGPU memory/compute limiting, and integration with Kubernetes scheduler extenders. Case studies show 3x utilization improvements — warranting deeper technical analysis.

2. **Omega/Borg evolution**: The full design space from Borg's monolithic scheduler to Omega's shared-state to Kubernetes's plugin-based framework. Understanding Google's decade of operational experience would inform any new scheduler design.

3. **NUMA-aware scheduling at scale**: The interaction between cluster-level NUMA-aware scheduling (NodeResourceTopology plugin) and node-level Topology Manager policies. Current limitations around scheduler state lag and cross-NUMA placement penalties.

4. **Checkpoint-based preemption implementation**: Practical suspend-resume mechanisms for containerized workloads, including integration with CRIU, eBPF-based state capture, and coordination with priority class systems.

5. **DRA driver architecture**: The two-component controller/kubelet-plugin model for device drivers, CEL-based device selection, and how this enables hardware vendors to expose richer scheduling semantics.

6. **QoS-aware co-location safety**: Application profiling mechanisms that enable safe overcommitment, interference detection/mitigation, and the balance between utilization gains and SLO risk.

---

### Raw Evidence Log

#### Kubernetes Scheduling Framework V2 — 12 Extension Points

**Claim**: Kubernetes Scheduling Framework provides 12 extension points (not 10 as commonly cited), forming a complete pod scheduling lifecycle with scheduling cycle (blue) and binding cycle (orange) phases. [^336^]

**Source**: Helayoty's Blog — "Deep Dive into the Kubernetes Scheduler"

**URL**: https://helayoty.org/blog/deep-dive-into-the-kubernetes-scheduler

**Date**: 2025-11-30

**Excerpt**: "The framework orchestrates plugins at various extension points throughout the scheduling process... it includes a Plugin Registry and supports 12 distinct extension points, each serving a specific purpose in the scheduling pipeline." / "This analysis is based on Kubernetes v1.35.0-alpha.0 codebase. The framework continues to evolve, but the core architectural principles remain consistent across versions."

**Context**: Detailed code-level analysis of the scheduler framework architecture

**Confidence**: High

---

#### SLURM Backfill Scheduling

**Claim**: SLURM's backfill scheduler starts lower-priority jobs only if doing so does not delay any higher-priority job's expected start time. Without backfill, utilization is "significantly lower." [^342^]

**Source**: SLURM Workload Manager — Scheduling Configuration Guide (Official Documentation)

**URL**: https://slurm.schedmd.com/sched_config.html

**Date**: Current (official docs)

**Excerpt**: "The backfill scheduling plugin is loaded by default. Without backfill scheduling, each partition is scheduled strictly in priority order, which typically results in significantly lower system utilization and responsiveness than otherwise possible. Backfill scheduling will start lower priority jobs if doing so does not delay the expected start time of any higher priority jobs."

**Context**: Official SLURM scheduling configuration documentation

**Confidence**: High

---

#### HTCondor ClassAds Matchmaking

**Claim**: HTCondor uses a matchmaking paradigm where both jobs and machines advertise classified ads with Requirements and Rank expressions, enabling bilateral matching with policy heterogeneity. [^339^]

**Source**: HTCondor Documentation — "Matchmaking with ClassAds"

**URL**: https://htcondor.readthedocs.io/en/v10_0/users-manual/matchmaking-with-classads.html

**Date**: Current (official docs)

**Excerpt**: "HTCondor simplifies job submission by acting as a matchmaker of ClassAds. HTCondor's ClassAds are analogous to the classified advertising section of the newspaper. Sellers advertise specifics about what they have to sell, hoping to attract a buyer. Buyers may advertise specifics about what they wish to purchase. Both buyers and sellers list constraints that need to be satisfied."

**Context**: Official HTCondor user manual explaining the matchmaking framework

**Confidence**: High

---

#### Apache Mesos Failure — Lessons Learned

**Claim**: Mesos's two-level offer model made it difficult for second-level schedulers to make optimal decisions because they might not receive the right offers, leading to its eventual retirement to the Apache Attic. [^105^]

**Source**: Hacker News Discussion — "Apache Mesos to be moved to Attic"

**URL**: https://news.ycombinator.com/item?id=26713082

**Date**: 2021-04-06

**Excerpt**: "Mesos's pessimistic two level offer model makes it hard for second level scheduler to make optimal decisions because it might not get the right offer it needs. At the same time, first level scheduler lacks application specific information to make the right decision to send the right offer to the second level scheduler, thus the problem. We evaluated many first level scheduling algorithms, and ironically found that 'random' first level scheduler sometimes works better than DRF for long running services scheduling."

**Context**: Comment from a former Mesos developer on the project's retirement

**Confidence**: High (first-hand account)

---

#### Omega Scheduler — Shared-State Design

**Claim**: Omega introduced a shared-state scheduler architecture with optimistic concurrency control, where multiple schedulers compete in a free-for-all with complete cluster visibility, using atomic transactions to update shared cell state. [^343^]

**Source**: Presentation slides — "Omega: flexible, scalable schedulers for large compute clusters"

**URL**: https://csc.csudh.edu/btang/seminar/slides/Omega-Matt_Levan.pdf

**Date**: Unknown (paper from 2013)

**Excerpt**: "New scheduler architecture must meet these requirements simultaneously: 1. High resource utilization 2. Job-specific placement and policy constraints 3. Fast decision making 4. Varying degrees of fairness 5. Highly available and robust" / "Schedulers are omniscient; compete in free-for-all. Schedulers have complete freedom; use cell state. Only one update to global cell state accepted at a time. If denied resources, try again."

**Context**: Academic presentation on the Omega scheduler paper

**Confidence**: High

---

#### Google Borg — Monolithic Scheduler at Scale

**Claim**: Borg uses a monolithic scheduler with score caching, equivalence classes, and relaxed randomization to schedule in cells of up to tens of thousands of machines, handling arrival rates above 10,000 tasks per minute. [^385^]

**Source**: Murat Demirbas's blog — "Large-scale cluster management at Google with Borg"

**URL**: http://muratbuffalo.blogspot.com/2015/04/large-scale-cluster-management-at.html

**Date**: 2015-04-28

**Excerpt**: "'Centralized is not necessarily less scalable than decentralized' is a pet peeve of mine. So, I went all ears when I read this section. The paper said: 'We are not sure where the ultimate scalability limit to Borg's centralized architecture will come from; so far, every time we have approached a limit, we've managed to eliminate it.'" / "A single Borgmaster can manage many thousands of machines in a cell, and several cells have arrival rates above 10000 tasks per minute. A busy Borgmaster uses 10-14 CPU cores and up to 50 GiB RAM."

**Context**: Blog post summarizing the EuroSys 2015 Borg paper

**Confidence**: High

---

#### Ray Distributed Scheduler — Millions of Tasks/Second

**Claim**: Ray's distributed bottom-up scheduler exceeds 1 million tasks per second at 60 nodes and 1.8 million at 100 nodes, with sub-millisecond scheduling latency. [^426^]

**Source**: Ray paper — "Ray: A Distributed Framework for Emerging AI Applications" (OSDI 2018)

**URL**: https://sands.kaust.edu.sa/classes/CS345/S19/papers/ray.pdf

**Date**: 2018

**Excerpt**: "Ray exceeds 1 million tasks per second throughput at 60 nodes and continues to scale linearly beyond 1.8 million tasks per second at 100 nodes. The rightmost datapoint shows that Ray can process 100 million tasks in less than a minute (54s), with minimum variability." / "Ray implements a unique distributed bottom-up scheduler that is horizontally scalable, and can handle dynamically constructed task graphs."

**Context**: Academic paper (OSDI 2018) presenting Ray's architecture and evaluation

**Confidence**: High

---

#### HAMi — GPU Utilization 13% to 37%

**Claim**: HAMi (CNCF Sandbox) enables fine-grained heterogeneous GPU sharing via CUDA API interception, improving GPU utilization from 13% to 37% (nearly 3x) at scale. [^416^]

**Source**: Jimmy Song's Blog — "When GPUs Move Toward Open Scheduling"

**URL**: https://jimmysong.io/blog/gpu-open-scheduling-hami-2025/

**Date**: 2026-02-13

**Excerpt**: "CNCF public case studies provide concrete answers: in a hybrid, multi-cloud platform built on Kubernetes and HAMi, 10,000+ Pods run concurrently, and GPU utilization improves from 13% to 37% (nearly 3x)." / "Case Study 1: Ke Holdings (February 5, 2026) — Environment: 5 clusters spanning public and private clouds, GPU models: H200/H100/A100/V100/4090 and more, Concurrent scale: 10,000+ Pods, Outcome: Overall GPU utilization improved from 13% to 37%"

**Context**: Technical blog analyzing HAMi's role in GPU resource management

**Confidence**: High (cites CNCF case studies)

---

#### NUMA-Aware Scheduling — Cross-NUMA Penalty

**Claim**: Cross-NUMA resource placement causes significant performance degradation for latency-critical and high-throughput applications; the NodeResourceTopology plugin provides `LeastNUMANodes` scoring to minimize NUMA span. [^433^]

**Source**: Kubernetes Scheduler Plugins KEP — "LeastNUMANodes ScoringStrategy"

**URL**: https://scheduler-plugins.sigs.k8s.io/docs/kep/454-numa-nodes-scoring/readme/

**Date**: 2023-08-06

**Excerpt**: "Consuming resources from multiple NUMA nodes can cause significant performance degradation in latency-critical execution and high-throughput applications. Topology Manager assigns resources from least amount of NUMA nodes but the scheduler is unaware of different NUMA topologies. The best case scenario would be to schedule pod on the node that can satisfy resource requirements from least amount of NUMA nodes to minimize latency."

**Context**: Kubernetes Enhancement Proposal for NUMA-aware scheduling

**Confidence**: High

---

#### Gang Scheduling — Kubernetes 1.35 Native Support

**Claim**: Kubernetes 1.35 introduces native gang scheduling (alpha) through the `GangScheduling` plugin and `PodGroup` API, implementing all-or-nothing scheduling at the `Permit` extension point. [^387^]

**Source**: Kubernetes Official Documentation — "Gang Scheduling"

**URL**: https://kubernetes.io/docs/concepts/scheduling-eviction/gang-scheduling/

**Date**: 2026-04-09

**Excerpt**: "Gang scheduling ensures that a group of Pods are scheduled on an 'all-or-nothing' basis. If the cluster cannot accommodate the entire group (or a defined minimum number of Pods), none of the Pods are bound to a node." / "`GangScheduling` plugin implements a `Permit` extension point that is evaluated for each schedulable Pod during the cycle. This is used to determine whether the `minCount` constraint is satisfied."

**Context**: Official Kubernetes documentation for gang scheduling feature

**Confidence**: High

---

#### Dominant Resource Fairness (DRF)

**Claim**: DRF generalizes max-min fairness to multiple resources by equalizing dominant shares, providing strategy-proofness, share guarantee, Pareto optimality, and envy-freeness. [^398^]

**Source**: Ali Ghodsi et al. — "Dominant Resource Fairness: Fair Allocation of Multiple Resource Types" (NSDI 2011)

**URL**: https://www.usenix.org/event/nsdi11/tech/slides/ghodsi.pdf

**Date**: 2011

**Excerpt**: "Apply max-min fairness to dominant shares. Equalize the dominant share of the users." / "Can we find a fair sharing policy that provides strategy-proofness and share guarantee? Max-min fairness for a single resource had these properties. Can we generalize max-min fairness to multiple resources?"

**Context**: Academic presentation at NSDI 2011 introducing DRF

**Confidence**: High

---

#### SLURM Multifactor Priority — Fairshare Formula

**Claim**: SLURM computes job priority as a weighted sum of 9 factors (age, association, fair-share, job size, nice, partition, QOS, site, TRES), with fair-share using exponential decay: `F = 2^(-U/S)`. [^439^]

**Source**: SLURM Workload Manager — "Multifactor Priority Plugin" (Official Documentation)

**URL**: https://slurm.schedmd.com/priority_multifactor.html

**Date**: Current (official docs)

**Excerpt**: "Job_priority = site_factor + (PriorityWeightAge) * (age_factor) + (PriorityWeightFairshare) * (fair-share_factor) + (PriorityWeightJobSize) * (job_size_factor) + (PriorityWeightPartition) * (partition_factor) + (PriorityWeightQOS) * (QOS_factor) + SUM(TRES_weight_cpu * TRES_factor_cpu, TRES_weight_<type> * TRES_factor_<type>, ...) - nice_factor"

**Context**: Official SLURM documentation for multifactor priority scheduling

**Confidence**: High

---

#### Checkpoint-Based Preemption — Google Trace Analysis

**Claim**: Analysis of Google cluster traces shows 93% of evictions triggered by priority scheduling, with many low-priority tasks experiencing multiple evictions; checkpoint-based preemption reduces wasted work. [^382^]

**Source**: Jack Li — "Improving Preemptive Scheduling with Application-Awareness" (Middleware 2015)

**URL**: https://thejackli.com/papers/li-middleware-2015.pdf

**Date**: 2015

**Excerpt**: "Prior analysis has shown that the task eviction event in the trace (accounting for 93% of evictions) is primarily triggered by priority scheduling in Google's cluster scheduler to handle task congestion or resource contention." / "Most cluster schedulers preempt a job or task by simply killing it. Alternatively, we propose to save the progress of a preempted task by suspending or checkpointing its state and resuming it later when resources are available."

**Context**: Academic paper analyzing Google cluster traces and proposing checkpoint-based preemption

**Confidence**: High

---

#### Kubernetes DRA — GPU Workloads

**Claim**: DRA (Dynamic Resource Allocation) graduated to GA in Kubernetes 1.34, enabling hardware-aware device selection through CEL expressions with a two-component controller/plugin driver architecture. [^401^]

**Source**: The New Stack — "Kubernetes Primer: Dynamic Resource Allocation (DRA) for GPU Workloads"

**URL**: https://thenewstack.io/kubernetes-primer-dynamic-resource-allocation-dra-for-gpu-workloads/

**Date**: 2025-09-05

**Excerpt**: "DRA drivers follow a two-component architecture that separates control plane and node-level operations. The controller component runs centrally and manages ResourceSlice creation and updates. The kubelet plugin component runs on each node as a DaemonSet and implements the gRPC interface for device preparation and cleanup. This separation enables better scalability and cleaner architectural boundaries than the monolithic device plugin approach."

**Context**: Technical article explaining DRA architecture for GPU scheduling

**Confidence**: High

---

#### GPU Sharing — MIG vs MPS vs Time-Slicing

**Claim**: Kubernetes GPU sharing has three mechanisms with distinct isolation properties: Time-slicing (no isolation, any GPU), MPS (memory/compute limits but no fault isolation), and MIG (hardware isolation, A100/H100 only). Time-slicing and MPS are mutually exclusive. [^357^]

**Source**: NVIDIA Kubernetes Device Plugin (GitHub — Official)

**URL**: https://github.com/nvidia/k8s-device-plugin

**Date**: 2026-05-27

**Excerpt**: "Time-slicing and MPS are mutually exclusive. In the case of time-slicing, CUDA time-slicing is used to allow workloads sharing a GPU to interleave with each other. However, nothing special is done to isolate workloads that are granted replicas from the same underlying GPU... In the case of MPS, a control daemon is used to manage access to the shared GPU. In contrast to time-slicing, MPS does space partitioning and allows memory and compute resources to be explicitly partitioned."

**Context**: Official NVIDIA device plugin documentation

**Confidence**: High

---

#### Nomad Scheduler Performance

**Claim**: Nomad's scheduler iterates over nodes to find feasible candidates, then scores them. Without affinity/spread, batch jobs score only 2 nodes; with affinity/spread, scores up to 100 nodes per allocation, causing "order-of-magnitude increases in scheduling times." [^362^]

**Source**: HashiCorp Nomad Documentation — "Advanced job scheduling"

**URL**: https://developer.hashicorp.com/nomad/docs/job-scheduling

**Date**: 2026-02-16

**Excerpt**: "When you include the `affinity` or `spread` block, the scheduler scores a number of nodes in the datacenter and node pool equal to the task group count, with a maximum of 100 per allocation. This can result in order-of-magnitude increases in scheduling times."

**Context**: Official HashiCorp Nomad documentation on scheduling performance

**Confidence**: High

---

#### Horizontal Pod Autoscaler Algorithm

**Claim**: HPA uses the formula `desiredReplicas = ceil[currentReplicas * (currentMetricValue / desiredMetricValue)]` with configurable sync period (15s default), downscale stabilization (5min default), and tolerance (0.1 default). [^393^]

**Source**: Kubernetes Official Documentation — "Horizontal Pod Autoscaling"

**URL**: https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/

**Date**: 2026-03-15

**Excerpt**: "Once during each period, the controller manager queries the resource utilization against the metrics specified in each HorizontalPodAutoscaler definition." / "desiredReplicas = ceil[currentReplicas * (currentMetricValue / desiredMetricValue)]"

**Context**: Official Kubernetes HPA documentation

**Confidence**: High

---

#### cgroups v2 and eBPF for Resource Monitoring

**Claim**: cgroups v2 provides unified resource hierarchy with PSI (Pressure Stall Information) for measuring resource contention; eBPF enables kernel-level observability for network analysis, syscall tracing, and resource accounting. [^429^] [^430^]

**Source**: Medium — "cgroups v2 for Resource Isolation in Linux" / Wiz — "Using eBPF in Kubernetes: A Security Overview"

**URL**: https://medium.com/@springmusk/cgroups-v2-for-resource-isolation-in-linux-c413d11cd36f / https://www.wiz.io/academy/container-security/ebpf-in-kubernetes

**Date**: 2025-11-22 / 2025-11-17

**Excerpt**: "One of the newer features tied to v2 is Pressure Stall Information, which lets you measure how long tasks are stalled waiting for a resource (CPU, memory, I/O) in a cgroup." / "By monitoring network packets at the kernel level, eBPF tools provide a granular view of network traffic within Kubernetes. This kernel-level access allows teams to identify performance bottlenecks, troubleshoot network issues, and ensure compliance with security policies."

**Context**: Technical articles on Linux cgroups v2 and eBPF in Kubernetes

**Confidence**: High

---

#### Volcano Scheduler — Gang Scheduling for AI/ML

**Claim**: Volcano provides job-level scheduling with PodGroup CRD for gang scheduling (`minMember` for atomic scheduling) and Queue CRD for weighted resource partitioning, targeting AI/ML/HPC workloads. [^414^]

**Source**: Youngju's Blog — "Kubernetes AI Training Pipeline: Analyzing Volcano, Training Operator, and Kueue"

**URL**: https://www.youngju.dev/blog/kubernetes/ai_training_pipeline_k8s.en

**Date**: 2026-03-01

**Excerpt**: "Volcano Scheduler: A scheduler that replaces or runs alongside kube-scheduler. It performs scheduling at the Job/PodGroup level rather than the Pod level." / "Queue is a CRD that logically partitions cluster resources to provide resource isolation in multi-tenant environments." / "PodGroup is a CRD that defines a group of tightly coupled Pods and serves as the core unit for Gang Scheduling."

**Context**: Technical blog analyzing batch scheduling solutions for Kubernetes

**Confidence**: High

---

#### Koordinator — QoS-Aware Co-Location

**Claim**: Koordinator enables safe resource overcommitment through application profiling, fine-grained CPU orchestration, and QoS-aware scheduling for co-located microservices, AI, and big data workloads. [^443^]

**Source**: Koordinator Documentation — "Introduction"

**URL**: https://koordinator.sh/docs/v1.7

**Date**: 2025-11-26

**Excerpt**: "Koordinator is a QoS-based scheduling for efficient orchestration of microservices, AI, and big data workloads on Kubernetes. It aims to improve the runtime efficiency and reliability of both latency sensitive workloads and batch jobs, simplify the complexity of resource-related configuration tuning, and increase pod deployment density to improve resource utilizations."

**Context**: Official Koordinator project documentation

**Confidence**: High

---

#### Resource Quotas and Capacity Planning

**Claim**: Kubernetes capacity planning uses a three-level framework (cluster, workload, time-horizon) with ResourceQuota for per-namespace caps and LimitRange for default/min/max resource constraints. [^418^] [^420^]

**Source**: Groundcover — "Capacity Planning in Kubernetes" / Kubernetes Official Docs — "Resource Quotas"

**URL**: https://www.groundcover.com/learn/cost-optimization/capacity-planning-kubernetes / https://kubernetes.io/docs/concepts/policy/resource-quotas/

**Date**: Unknown / 2025-11-20

**Excerpt**: "Capacity planning in Kubernetes happens along three main levels: cluster level, workload level, and time-horizon level." / "You can define a LimitRange to force defaults on pods that make no compute resource requirements."

**Context**: Technical guides on Kubernetes capacity management

**Confidence**: High
