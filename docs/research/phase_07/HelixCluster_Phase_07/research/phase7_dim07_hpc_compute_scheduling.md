# Dimension 7: HPC Schedulers & Distributed Compute Frameworks

## Research Report for HelixCluster Compute Scheduling & Workload Management

---

## Executive Summary

This report analyzes seven major distributed computing schedulers and frameworks: Apache Spark, Apache Mesos, SLURM, BOINC, Apache Flink, HashiCorp Nomad, and Google's Omega scheduling architecture. Each system offers distinct lessons for HelixCluster's compute scheduling layer. Key findings: Spark's DAG scheduler demonstrates the power of execution plan optimization; SLURM's backfill scheduling achieves 90%+ cluster utilization; Nomad's lightweight architecture and device plugin model align closely with HelixCluster's philosophy; BOINC's heterogeneous device management provides a blueprint for diverse device support; and Omega's shared-state approach offers the best scalability model for parallel schedulers.

---

## 1. Apache Spark: In-Memory Batch Compute

### Architecture Overview

Apache Spark operates on a master-worker architecture with three core components: the **Driver Program**, **Cluster Manager**, and **Executors** [^3326^]. The Driver is the central coordinator that manages execution across worker nodes. It contains the `SparkContext`, translates user code into a physical execution plan, and coordinates with the cluster manager to schedule tasks [^3327^].

```
+-------------------+     +------------------+     +------------------+
|   Driver Program  |     |  Cluster Manager |     |   Worker Nodes   |
|                   |     |                  |     |                  |
|  +-------------+  |     |  (Standalone/    |     |  +-------------+ |
|  | SparkContext|  |<--->|   YARN/Mesos/   |<--->|  | Executor 1  | |
|  +-------------+  |     |   Kubernetes)    |     |  +-------------+ |
|  +-------------+  |     |                  |     |  +-------------+ |
|  | DAGScheduler|  |     |                  |     |  | Executor 2  | |
|  +-------------+  |     |                  |     |  +-------------+ |
|  +-------------+  |     |                  |     |  +-------------+ |
|  |TaskScheduler|  |     |                  |     |  | Executor N  | |
|  +-------------+  |     |                  |     |  +-------------+ |
+-------------------+     +------------------+     +------------------+
```

### DAG Scheduler: The Key Innovation

Spark's most significant architectural contribution is its **DAG Scheduler**. When a user submits an application, the Driver translates code into a logical execution plan represented as a Directed Acyclic Graph (DAG) of stages [^3329^]. The DAG scheduler:

1. **Builds stage boundaries** at shuffle operations (wide dependencies like `groupByKey`, `join`) [^3329^]
2. **Pipelines narrow transformations** within each stage to minimize task overhead
3. **Submits stages in dependency order**, dynamically reacting to completions
4. **Tracks RDD lineage** for fault tolerance, enabling recomputation of lost partitions [^3331^]

The DAG scheduler converts logical operations into **TaskSets** — one task per data partition — and hands them to the TaskScheduler for physical placement [^3329^]. This two-level scheduling (logical then physical) is what enables Spark's 100x performance improvement over Hadoop MapReduce [^3350^].

### Source Code Patterns

Key files in the Spark codebase [^3431^][^3433^]:

```scala
// DAGScheduler.scala - Core scheduling logic
class DAGScheduler(
    private[scheduler] val sc: SparkContext,
    private[scheduler] val taskScheduler: TaskScheduler,
    ...
) {
  // Submits stages to TaskScheduler in dependency order
  def submitStage(stage: Stage): Unit = { ... }
  
  // Handles task completion and triggers dependent stages
  def handleTaskCompletion(event: CompletionEvent): Unit = { ... }
}

// TaskSchedulerImpl.scala - Physical task placement
private[spark] class TaskSchedulerImpl(
    val sc: SparkContext,
    val maxTaskFailures: Int,
    isLocal: Boolean = false)
  extends TaskScheduler with Logging {
  
  // Schedules tasks on executors considering data locality
  def resourceOffers(offers: Seq[WorkerOffer]): Seq[Seq[TaskDescription]] = { ... }
}
```

### Performance Characteristics

| Metric | Spark | Hadoop MapReduce |
|--------|-------|-----------------|
| In-memory caching | Yes | No |
| Task launch overhead | ~5ms | ~10s |
| Shuffle optimization | Sort-based, consolidated files | Simple hash-based |
| Stage pipelining | Yes | No (strict map-then-reduce) |
| Fault recovery | RDD lineage recomputation | Disk-based replication |

### What HelixCluster Should Adopt

- **DAG-based execution planning**: HelixCluster should model compute jobs as directed graphs with explicit dependencies, enabling automatic stage optimization and parallelism detection.
- **Lazy evaluation**: Delay execution until results are actually needed, allowing the scheduler to optimize the entire plan.
- **Data locality awareness**: Schedule tasks on nodes where input data already resides, minimizing network transfers.

---

## 2. Apache Mesos: Two-Level Scheduling Pioneer

### Architecture and Resource Offers

Apache Mesos introduced the innovative **two-level scheduling** model. The Mesos Master manages slave daemons on each cluster node and offers available resources to framework schedulers [^3336^]. Frameworks consist of a scheduler (registers with master) and an executor (runs on slave nodes).

The resource offer flow [^3336^]:

```
1. Slave reports free resources to Master
2. Master's allocation module decides which framework gets the offer
3. Framework scheduler accepts/declines the offer
4. If accepted, framework describes tasks to launch
5. Master sends tasks to slave; executor launches them
```

### Dominant Resource Fairness (DRF)

Mesos implements **Hierarchical DRF** for fair multi-resource allocation [^3370^]. DRF computes each framework's **dominant share** (maximum share of any resource type) and equalizes dominant shares across all frameworks [^3330^]:

```
S_i = max(u_{i,j} / r_j)  for all resource types j

Where:
  u_{i,j} = user i's usage of resource j
  r_j     = total available resource j
```

DRF satisfies four key properties: **sharing incentive**, **strategy-proofness**, **envy-freeness**, and **Pareto efficiency** [^3371^]. The implementation lives in `src/master/allocator/mesos/sorter/drf/sorter.hpp` [^3417^].

### Why Mesos Declined

Despite its elegant architecture, Mesos lost to Kubernetes for several reasons:

1. **Kubernetes ecosystem**: K8s had a larger community, more integrations, and cloud provider support [^3388^]
2. **Complexity**: Two-level scheduling required frameworks to implement their own schedulers, increasing complexity
3. **Resource hoarding**: Frameworks could hold offers indefinitely, causing fragmentation [^3330^]
4. **Limited visibility**: Frameworks only see offered resources, not the full cluster state [^343^]

### What Mesos Did Right That K8s Does Poorly

- **Fine-grained resource sharing**: Mesos could share resources at the task level; Kubernetes allocates at the pod level [^3336^]
- **Framework independence**: Multiple frameworks (Spark, Hadoop, custom) could coexist
- **Resource offer filters**: Frameworks could specify which nodes they didn't want offers from, reducing overhead [^3330^]

### What HelixCluster Should Adopt

- **Pluggable allocation modules**: Allow custom scheduling policies via plugins, like Mesos's `Allocator` interface [^3419^]
- **Multi-dimensional fairness**: Use DRF or similar algorithms when scheduling across heterogeneous resources (CPU, GPU, memory, bandwidth)
- **Resource offer filters**: Let devices specify constraints to reduce unnecessary scheduling attempts

### What HelixCluster Should Avoid

- **Two-level scheduling complexity**: The framework-scheduler model adds too much overhead for edge devices
- **Resource hoarding**: Prevent any single workload from monopolizing offers

---

## 3. SLURM: The De Facto HPC Scheduler

### Architecture

SLURM (Simple Linux Utility for Resource Management) uses a three-daemon architecture [^3328^][^3325^]:

| Daemon | Role | Fault Tolerance |
|--------|------|-----------------|
| `slurmctld` | Central controller; all scheduling decisions | Backup controller with HA failover |
| `slurmd` | Compute node agent; executes jobs | Job continues even if slurmd fails |
| `slurmdbd` | Accounting database (MySQL/MariaDB) | Database replication |

### Scheduling: Three Loops Working Together

SLURM employs **three scheduling loops** [^3324^]:

1. **Direct scheduling**: Runs on job submission, fast-path for up to 500 jobs
2. **Main scheduling loop**: Runs periodically, orders by priority, schedules until first pending job
3. **Backfill scheduling loop**: The secret sauce — fills gaps between larger jobs

### Backfill Scheduling: Achieving 90%+ Utilization

SLURM's backfill scheduler is one of its most important features [^3335^]. It:

1. Builds a table of expected resource availability through time
2. Traces expected initiation/termination of running and pending jobs
3. Allows lower-priority jobs to run **if they don't delay any higher-priority job**

Key configuration parameters [^3324^]:

```ini
# slurm.conf
SchedulerType=sched/backfill
SchedulerParameters=bf_interval=45,bf_max_time=75,bf_window=2880
bf_max_job_test=2000       # Max jobs to consider
bf_max_job_user=15         # Max jobs per user
bf_resolution=60           # Time resolution in seconds
```

### Multifactor Priority System

SLURM's priority formula [^3402^][^3324^]:

```
Job_priority =
    site_factor +
    (PriorityWeightAge) * (age_factor) +
    (PriorityWeightFairshare) * (fair-share_factor) +
    (PriorityWeightJobSize) * (job_size_factor) +
    (PriorityWeightPartition) * (partition_factor) +
    (PriorityWeightQOS) * (QOS_factor) +
    SUM(TRES_weight_cpu * TRES_factor_cpu, ...) -
    nice_factor
```

The **Fair-Tree** algorithm ensures hierarchical fairness: if an entire organizational branch is underserved, all users in that branch get priority [^3324^].

### GRES: Generic Resource Scheduling for GPUs

SLURM supports GPU scheduling through **GRES** (Generic Resource Scheduling) [^3373^]:

```ini
# slurm.conf
GresTypes=gpu
NodeName=tux[1-16] gres=gpu:a100:4

# Request 2 GPUs in a job
# sbatch --gres=gpu:2 my_job.sh
```

GRES provides full **cgroup isolation**, preventing jobs from exceeding their GPU allocation [^3325^].

### What HelixCluster Should Adopt

- **Backfill scheduling**: This is the single most impactful algorithm for cluster utilization. HelixCluster must implement backfill to maximize device usage.
- **Multifactor priority**: Combine age, fair-share, job size, and QoS factors for nuanced priority.
- **Partition concept**: Group devices into logical partitions with different policies (e.g., "gpu-pool", "edge-devices", "high-memory").
- **GRES-like extensible resources**: Define custom resource types beyond CPU/memory (GPU, TPU, FPGA, bandwidth).

---

## 4. BOINC: Volunteer Computing at Scale

### Architecture and Design Philosophy

BOINC (Berkeley Open Infrastructure for Network Computing) is designed for **high-throughput computing** with large numbers of independent compute-intensive jobs [^3353^]. It was designed for volunteer computing where worker nodes are:

- **Heterogeneous**: Different CPUs, GPUs, operating systems
- **Sporadically available**: Devices come and go unpredictably
- **Untrusted**: May return incorrect results
- **Numerous**: Millions of worker nodes [^3353^]

### Validation Through Redundant Computing

BOINC ensures accuracy through a **quorum system** [^3338^]:

1. Each work unit is assigned to **multiple clients** (typically 3+)
2. Server-side validator compares output files from replicas
3. A **minimum number of consistent results** (quorum) must agree
4. The **canonical result** is selected from majority consensus
5. Dissenting results are discarded

```
Work Unit Server --> Client A --> Result 1 --+
               +--> Client B --> Result 2 --+--> Validator --> Canonical Result
               +--> Client C --> Result 3 --+
```

### Adaptive Replication and Credit System

- **Adaptive replication**: Reduces redundancy for reliable hosts; increases for flaky ones [^3338^]
- **Punitive mechanisms**: Limits task assignments to devices with repeated failures
- **Credit system**: Awards "cobblestones" proportional to validated compute effort [^3338^]:

```
Claimed Credit C = (T x Peak FLOPS) x (200 / 86400 x 10^9)
```

Where 200 cobblestones = daily output of a 1 GFLOPS processor.

### What HelixCluster Should Adopt

- **Redundant execution for critical tasks**: Run critical computations on multiple devices and compare results, especially on untrusted/edge devices.
- **Adaptive trust scoring**: Track device reliability history and adjust replication accordingly.
- **Credit/contribution tracking**: Implement a contribution metric proportional to validated compute delivered, for incentivization.
- **Heterogeneous device abstraction**: BOINC's client-server model cleanly abstracts away device differences.

---

## 5. Apache Flink: Stateful Stream Processing

### Architecture

Flink uses a master-worker model [^3337^]:

| Component | Role |
|-----------|------|
| **JobManager** | Coordinates distributed execution, manages checkpoints, handles scheduling |
| **TaskManagers** | Execute tasks, maintain local state, communicate for data exchange |

### Checkpointing: Exactly-Once Semantics

Flink implements the **Chandy-Lamport algorithm** for distributed snapshots [^3392^][^3340^]:

1. **JobManager** injects checkpoint barriers into source streams
2. Barriers flow through the topology with data
3. When an operator receives barriers from **all** input channels (barrier alignment), it snapshots state
4. State is written asynchronously to the **state backend** (HashMap or RocksDB)
5. Once all operators acknowledge, the checkpoint is complete

```java
// Flink checkpointing configuration
StreamExecutionEnvironment env = StreamExecutionEnvironment.getExecutionEnvironment();
env.enableCheckpointing(60000);  // Every 60 seconds
env.setStateBackend(new HashMapStateBackend());
env.getCheckpointConfig().setCheckpointStorage("s3://bucket/checkpoints");
```

### Event Time and Watermarks

Flink distinguishes between **event time** (when event occurred) and **processing time** (when processed) [^3337^]. **Watermarks** are special timestamps that indicate "all events before this time have been received," enabling correct windowing even with out-of-order data.

### What HelixCluster Should Adopt

- **Checkpointing for long-running jobs**: Periodically snapshot job state to enable recovery without full restart.
- **Event-time processing**: For real-time workloads, track event timestamps separately from processing time.
- **Pluggable state backends**: Allow different storage backends for job state (memory, local disk, remote storage).

---

## 6. HashiCorp Nomad: Lightweight Orchestration

### Architecture Simplicity

Nomad's defining characteristic is its **single binary** architecture [^3388^][^3391^]. Unlike Kubernetes which requires API Server, etcd, Scheduler, Controller Manager, and Kubelet, Nomad is one binary that handles everything:

```
# Start server
nomad agent -server -bootstrap-expect=1 -data-dir=/tmp/nomad

# Start client
nomad agent -client -data-dir=/tmp/nomad

# Deploy job
nomad job run my_job.nomad
```

### Scheduling: Feasibility Checking + Ranking

Nomad's scheduler uses a two-phase approach [^3351^]:

1. **Feasibility checking**: Filter nodes by datacenter, health, drivers, constraints
2. **Ranking**: Score feasible nodes using **bin packing** to optimize density, augmented by affinity/anti-affinity rules

Nomad automatically applies **job anti-affinity** to reduce correlated failures while maximizing density [^3351^].

### Optimistic Concurrency and Plan Queue

Multiple schedulers run in parallel without locking. The **plan queue** on the leader node handles conflicts by doing partial or complete rejections of plans. Schedulers get feedback and explore alternate plans if rejected [^3351^].

### Device Plugin System

Nomad 0.9+ supports **device plugins** for hardware like GPUs and FPGAs [^3344^][^107^]. During **fingerprinting**, the plugin reports:

```
Device Group     = nvidia/gpu/Tesla V100-SXM2-16GB
bar1             = 16384 MiB
cores_clock      = 1530 MHz
driver_version   = 418.39
memory           = 16130 MiB
pci_bandwidth    = 15760 MB/s
```

Job specifications can request devices with constraints and affinities [^107^]:

```hcl
resources {
  device "nvidia/gpu" {
    count = 2
    constraint {
      attribute = "${device.attr.memory}"
      operator  = ">"
      value     = "10000 MiB"
    }
    affinity {
      attribute = "${device.model}"
      value     = "Tesla V100"
      weight    = 100
    }
  }
}
```

### Why Nomad Is Closer to HelixCluster's Philosophy

| Aspect | Kubernetes | Nomad | HelixCluster Fit |
|--------|-----------|-------|-----------------|
| Architecture | Complex, multi-component | Single binary | Nomad model |
| Deployment time | Days | Minutes | Nomad model |
| Workload types | Containers only | Containers, binaries, VMs, batch | Nomad model |
| Resource overhead | High | Minimal | Nomad model |
| Scaling | ~5,000 nodes | ~10,000+ nodes, 2M tasks | Nomad model |
| Device support (GPU/FPGA) | Device plugins | Native device plugins | Nomad model |
| Multi-datacenter | Complex federation | Built-in federation | Nomad model |

### What HelixCluster Should Adopt

- **Single-binary deployment**: Minimize deployment friction for edge devices
- **Device plugin model**: Extensible fingerprinting for diverse device types (GPU, TPU, FPGA, custom accelerators)
- **Bin packing + anti-affinity**: Optimize for density while reducing correlated failure risk
- **Optimistic concurrent scheduling**: Multiple parallel schedulers with conflict resolution via plan queue
- **Built-in multi-datacenter federation**: Native support for geographically distributed clusters

---

## 7. Scheduling Algorithm Comparison

### Three Fundamental Approaches

| Approach | Examples | Resource Visibility | Concurrency | Pros | Cons |
|----------|----------|-------------------|-------------|------|------|
| **Monolithic** | K8s original, Borg | Full cluster state | Serialized (one at a time) | Simple, consistent | Scalability bottleneck, head-of-line blocking [^343^] |
| **Two-level** | Apache Mesos | Subset (offers) | Pessimistic (offers lock resources) | Clean separation | Limited visibility, resource hoarding, fragmentation [^343^] |
| **Shared-state** | Google Omega | Full cluster state | Optimistic (transactions) | Parallel, full visibility, scalable | Conflicts require retry, more complex [^343^] |

### Google's Omega: Shared-State Scheduling

Omega's key innovation is giving **every scheduler full visibility** into the entire cluster and using **optimistic concurrency control** to handle conflicts [^448^][^445^]. Multiple schedulers work independently; when they try to claim resources, conflicts are detected at commit time.

**Key insight**: Conflicts are rare in practice. Average job wait times for Omega are similar to optimized monolithic schedulers [^448^].

### Gang Scheduling for GPU Workloads

**Gang scheduling** requires all tasks of a job to start simultaneously [^3372^]. This is essential for:

- **MPI programs**: All processes must initialize together for collective operations
- **Distributed training**: PyTorch/TensorFlow with NCCL all-reduce requires all GPUs present
- **Synchronization barriers**: `dist.barrier()` blocks until all ranks arrive

Without gang scheduling, partial allocations cause **all-reduce stalls** on InfiniBand fabrics [^3325^].

### Backfill Scheduling

Backfill is the most effective technique for cluster utilization [^3333^]:

- Smaller jobs run ahead of larger queued jobs **if they don't delay higher-priority jobs**
- Requires jobs to declare **maximum execution time** (walltime)
- Builds resource availability timeline to check feasibility

### Dominant Resource Fairness (DRF)

DRF is the most principled fairness algorithm for multi-resource allocation [^3370^][^3371^]:

- For single resource, reduces to max-min fairness
- For multiple resources, equalizes each user's **dominant share**
- Satisfies: sharing incentive, strategy-proofness, Pareto efficiency, envy-freeness

### Which Algorithm Fits HelixCluster?

HelixCluster should adopt a **hybrid approach**:

1. **Shared-state optimistic concurrency** for scalability (like Omega)
2. **Backfill scheduling** for utilization (like SLURM)
3. **Gang scheduling support** for GPU/distributed workloads (like SLURM + MPI)
4. **DRF-based fairness** for multi-dimensional resource allocation (like Mesos)
5. **Device plugin model** for heterogeneous hardware (like Nomad)

---

## HelixCluster Impact: Specific Improvements

### Immediate Implementation Priorities

#### 1. Backfill Scheduler (Priority: Critical)

Implement a backfill scheduling loop that:
- Maintains a resource availability timeline
- Allows small jobs to run in gaps without delaying critical jobs
- Respects job-declared maximum execution times
- Targets 90%+ cluster utilization

```python
# Pseudocode for HelixCluster backfill scheduler
def backfill_schedule(pending_jobs, running_jobs, resources):
    # Build resource timeline from running jobs
    timeline = build_resource_timeline(running_jobs)
    
    # Sort pending by priority
    sorted_jobs = sort_by_priority(pending_jobs)
    
    # Try to schedule highest-priority job
    for job in sorted_jobs:
        if can_schedule_now(job, resources):
            allocate(job, resources)
        else:
            # Try backfill: find jobs that fit in the gap
            earliest_start = estimate_start_time(job, timeline)
            for small_job in sorted_jobs_after(job):
                if (small_job.max_runtime + now < earliest_start and
                    can_schedule_now(small_job, resources)):
                    allocate(small_job, resources)
```

#### 2. Device Plugin System (Priority: High)

Adopt Nomad's device plugin model:
- Each device type has a fingerprinting plugin
- Plugins report: device count, model, capabilities, current utilization
- Scheduler uses device attributes for placement decisions
- Support for GPU, TPU, FPGA, and custom accelerators

#### 3. Multifactor Priority System (Priority: High)

Implement SLURM-style multifactor priority:

```python
job_priority = (
    weight_age * age_factor +
    weight_fairshare * fairshare_factor +
    weight_job_size * job_size_factor +
    weight_qos * qos_factor -
    nice_value
)
```

With configurable weights per deployment.

#### 4. Optimistic Concurrent Scheduling (Priority: Medium)

- Run multiple scheduler instances in parallel
- Use a plan queue with conflict detection
- Allow incremental transactions (accept non-conflicting allocations)
- Support all-or-nothing transactions for gang scheduling

#### 5. Redundant Execution for Untrusted Devices (Priority: Medium)

Inspired by BOINC:
- Track device reliability scores
- Run critical tasks on N devices in parallel
- Use quorum consensus for result validation
- Reduce replication for highly reliable devices

#### 6. DAG-Based Job Composition (Priority: Medium)

Inspired by Spark:
- Model complex jobs as DAGs of stages
- Automatic dependency tracking and parallelization
- Lazy evaluation for plan optimization
- Stage-level fault recovery

#### 7. Checkpointing for Long-Running Jobs (Priority: Low)

Inspired by Flink:
- Periodic state snapshots for streaming/long-running jobs
- Pluggable state backends (memory, local disk, S3)
- Barrier-based consistent snapshots

### Architecture Recommendation for HelixCluster

```
+-----------------------+
|   HelixCluster API    |
+-----------+-----------+
            |
+-----------v-----------+     +------------------+
|   Shared State Store  |<--->| Scheduler Pool   |
|   (Cluster State)     |     | (Parallel OCS)   |
+-----------+-----------+     +--------+---------+
            |                            |
+-----------v-----------+     +--------v---------+
|   Backfill Engine     |     | Plan Queue       |
|   (SLURM-inspired)    |     | (Conflict Res.)  |
+-----------+-----------+     +--------+---------+
            |                            |
+-----------v-----------+     +--------v---------+
|   Device Plugin       |     | Execution Agents |
|   Registry            |     | (per device)     |
+-----------------------+     +------------------+
```

### Anti-Patterns to Avoid

1. **Monolithic single-scheduler**: Will become a bottleneck at scale [^343^]
2. **Two-level resource offers**: Too complex, causes fragmentation [^343^]
3. **Kubernetes-level complexity**: HelixCluster should remain deployable in minutes, not days
4. **Ignoring walltime declarations**: Backfill requires jobs to declare max runtime
5. **Slot-based fairness**: Use DRF for multi-resource fairness, not equal slot counts [^3371^]

---

## References

[^3324^] GWDG HPC Documentation: "How scheduling works" - https://docs.hpc.gwdg.de/how_to_use/slurm/how_scheduling_works/index.html

[^3325^] Hyperstack: "What is Slurm and Why It Matters for HPC Workloads" - https://www.hyperstack.cloud/blog/case-study/what-is-slurm-and-why-it-matters-for-hpc-workloads

[^3326^] Flexera: "Apache Spark architecture 101" - https://www.flexera.com/blog/finops/apache-spark-architecture/

[^3327^] Instaclustr: "Apache Spark architecture: Concepts, components, and best practices" - https://www.instaclustr.com/education/apache-spark/apache-spark-architecture-concepts-components-and-best-practices/

[^3328^] Abhik.ai: "Slurm Fundamentals: Job Scheduling on HPC Clusters" - https://www.abhik.ai/concepts/gpu-computing/slurm-fundamentals

[^3329^] Luminousmen: "Anatomy of Apache Spark Application" - https://luminousmen.com/post/spark-anatomy-of-spark-application/

[^3330^] Saha et al.: "Exploring the Fairness and Resource Distribution in an Apache Mesos Environment" - https://arxiv.org/pdf/1905.08388

[^3331^] RCET: "Data Processing with Apache Spark: Spark Architecture" - https://www.rcet.org.in/uploads/academics/regulation2024/rohini_42392099967.pdf

[^3333^] Slurm Advanced Scheduling (UST4HPC) - https://ust4hpc.sciencesconf.org/data/pages/Slurm_UST4HPC_final.pdf

[^3334^] Linux Foundation: "Distributed Resource Scheduling Frameworks" - http://events17.linuxfoundation.org/sites/events/files/slides/Distributed%20Resource%20Scheduling%20Frameworks.pdf

[^3335^] Jette et al.: "Architecture of the Slurm Workload Manager" (JSSPP 2023 Keynote) - https://jsspp.org/papers23/JSSPP_2023_keynote_SLURM.pdf

[^3336^] Hindman et al.: "Mesos: A Platform for Fine-Grained Resource Sharing" - https://people.eecs.berkeley.edu/~alig/papers/mesos.pdf

[^3337^] Conduktor: "What is Apache Flink? Stateful Stream Processing" - https://www.conduktor.io/glossary/what-is-apache-flink-stateful-stream-processing

[^3338^] Grokipedia: "Berkeley Open Infrastructure for Network Computing" - https://grokipedia.com/page/Berkeley_Open_Infrastructure_for_Network_Computing

[^3340^] Software Frontier: "Understanding Apache Flink: Architecture, Event-Time Processing, and State Management" - https://softwarefrontier.substack.com/p/understanding-apache-flink-architecture

[^3343^] BOINC Wiki: "BOINC overview" - https://github.com/BOINC/boinc/wiki/BOINC-overview

[^3344^] HashiCorp Blog: "Using HashiCorp Nomad to Schedule GPU Workloads" - https://www.hashicorp.com/en/blog/using-hashicorp-nomad-to-schedule-gpu-workloads

[^3345^] Schwarzkopf et al.: "Omega: flexible, scalable schedulers for large compute clusters" (EuroSys 2013) - https://wiki.epfl.ch/edicpublic/documents/Candidacy%20exam/PR13PamelaDelgado.pdf

[^3350^] IEEE: "Shuffle phase optimization in Spark" - https://ieeexplore.ieee.org/document/8125977/

[^3351^] HashiCorp Docs: "How Nomad job scheduling works" - https://developer.hashicorp.com/nomad/docs/concepts/scheduling/how-scheduling-works

[^3353^] BOINC GitHub Wiki: "BOINC overview" - https://github.com/BOINC/boinc/wiki/BOINC-overview

[^3370^] Ghodsi et al.: "Dominant Resource Fairness" (NSDI 2011) - https://dl.acm.org/doi/10.5555/1972457.1972490

[^3371^] Ghodsi et al.: "Dominant Resource Fairness: Fair Allocation of Multiple Resource Types" - https://amplab.cs.berkeley.edu/wp-content/uploads/2011/06/Dominant-Resource-Fairness-Fair-Allocation-of-Multiple-Resource-Types.pdf

[^3372^] Velda Docs: "Gang Scheduling" - https://docs.velda.io/user-guide/gang-scheduling/

[^3373^] Slurm Documentation: "Generic Resource (GRES) Scheduling" - https://slurm.schedmd.com/gres.html

[^3387^] Conduktor: "Flink State Management and Checkpointing" - https://www.conduktor.io/glossary/flink-state-management-and-checkpointing

[^3388^] Opstree: "Nomad vs Kubernetes: Which One Should You Use?" - https://opstree.com/blog/nomad-vs-kubernetes-which-one-should-you-use/

[^3391^] Overcast Blog: "Kubernetes vs Hashicorp Nomad: A Comparison" - https://overcast.blog/kubernetes-vs-hashicorp-nomad-a-practical-comparison-d8308ef7c952

[^3392^] Apache Flink Docs: "Fault Tolerance" - https://nightlies.apache.org/flink/flink-docs-stable/docs/learn-flink/fault_tolerance/

[^3402^] Slurm Documentation: "Multifactor Priority Plugin" - https://slurm.schedmd.com/priority_multifactor.html

[^3417^] CSDN: "Apache Mesos Resource Scheduling Principles" - https://blog.csdn.net/gitblog_01052/article/details/144393888

[^3419^] Apache Mesos Documentation: "Allocation Modules" - https://mesos.apache.org/documentation/latest/allocation-module/

[^3431^] GitHub: "TaskSchedulerImpl.scala - apache/spark" - https://github.com/apache/spark/blob/master/core/src/main/scala/org/apache/spark/scheduler/TaskSchedulerImpl.scala

[^3433^] Japila Books: "DAGScheduler - The Internals of Spark Core" - https://books.japila.pl/apache-spark-internals/scheduler/DAGScheduler/

[^445^] Google Research: "Omega: flexible, scalable schedulers for large compute clusters" - https://research.google/pubs/omega-flexible-scalable-schedulers-for-large-compute-clusters/

[^448^] Anant Jain: "Omega: flexible, scalable schedulers for large compute clusters" - https://www.anantjain.dev/posts/omega
