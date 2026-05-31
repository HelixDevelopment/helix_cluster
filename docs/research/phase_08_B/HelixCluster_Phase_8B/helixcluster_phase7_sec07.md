# 7. HPC Scheduling: SLURM, Nomad, Spark, BOINC

The scheduling layer is where HelixCluster's design philosophy faces its most demanding test. Every cycle wasted on a head-of-line blocking decision, every GPU left idle by a rigid allocation policy, and every failed task on an untrusted edge device erodes the economic and technical case for a decentralized compute grid. This chapter examines four systems that have solved distinct pieces of the scheduling puzzle at massive scale: SLURM, the de facto standard for supercomputing; HashiCorp Nomad, the lightweight orchestrator built for heterogeneity; Apache Spark, the data-engineering framework that redefined execution planning; and BOINC, the volunteer-computing platform that learned to trust unreliable hardware. For each, we extract concrete algorithms, data structures, and configuration patterns that HelixCluster can adopt, adapt, or avoid.

## 7.1 SLURM

SLURM (Simple Linux Utility for Resource Management) schedules workloads on roughly 60 % of the TOP500 supercomputers, including the two largest publicly known systems as of 2025. Its staying power is not accidental. Three decades of iterative refinement have produced a scheduler that sustains 90 % cluster utilization on machines with hundreds of thousands of cores, while enforcing complex policies for competing research groups, national laboratories, and commercial tenants. Understanding how SLURM achieves this is prerequisite to designing any serious compute scheduler.

### 7.1.1 Backfill Scheduling: 90 %+ Utilization

SLURM's architecture rests on three daemons: `slurmctld` (the central controller), `slurmd` (the per-node execution agent), and `slurmdbd` (the accounting database). Controller high-availability is provided by a warm standby that takes over within seconds of a primary failure. Node-level execution continues even if the local `slurmd` restarts, because job processes are placed inside `cgroup` containers that outlive their supervising daemon.

| Daemon | Responsibility | Fault-Tolerance Strategy |
|---|---|---|
| `slurmctld` | All scheduling decisions, job state, partition configuration | Warm standby with automatic failover; state checkpointed every few seconds |
| `slurmd` | Per-node job launch, signal forwarding, resource accounting | Job continues inside cgroup if daemon restarts; no scheduler involvement required |
| `slurmdbd` | Historical accounting, fair-share quotas, banked priority | Database replication via MySQL/MariaDB streaming replication |

The backfill scheduler is SLURM's most impactful feature. Without it, a cluster running a small number of long, wide jobs would spend large fractions of each day with idle nodes waiting for the next top-priority job to fit. Backfill solves this by allowing lower-priority jobs to slip into gaps, provided they do not delay any higher-priority job.

The algorithm works as follows:

1. **Build a resource-availability timeline.** For every partition and resource dimension (CPUs, memory, GPUs), construct a time-indexed table of when currently running jobs are expected to complete and when already scheduled pending jobs will start.

2. **Sort the pending queue by priority.** The highest-priority job is tentatively assigned a start time by scanning the timeline forward until sufficient resources are free.

3. **Fill gaps.** For each lower-priority job, test whether its declared maximum wall time fits entirely inside an idle window that precedes the start of any higher-priority job. If the test passes, the job is started immediately.

4. **Respect limits.** Configuration caps prevent starvation: `bf_max_job_test` limits how many pending jobs are evaluated (default 5,000), `bf_max_job_user` caps per-user evaluations, and `bf_window` bounds how far into the future the timeline is projected.

The Go implementation below captures the core logic. A production system would add preemption, reservation handling, and multi-dimensional resource accounting, but the structural skeleton is identical.

```go
package scheduler

import (
    "sort"
    "time"
)

// Job represents a pending or running workload.
type Job struct {
    ID        string
    Priority  int
    CPUs      int
    GPUs      int
    MemMB     int
    MaxDur    time.Duration // user-declared wall time
    SubmitTime time.Time
}

// TimelineEntry marks a resource change at a specific time.
type TimelineEntry struct {
    At      time.Time
    CPUs    int // negative = freed, positive = claimed
    GPUs    int
    MemMB   int
}

// BackfillScheduler holds the resource-availability horizon.
type BackfillScheduler struct {
    TotalCPUs int
    TotalGPUs int
    TotalMem  int
}

// backfill returns a list of job IDs that may start now.
func (bf *BackfillScheduler) backfill(
    pending []Job,
    running []Job,
    now time.Time,
) []string {
    // Sort pending jobs by descending priority.
    sort.Slice(pending, func(i, j int) bool {
        return pending[i].Priority > pending[j].Priority
    })

    // Build timeline from running jobs.
    timeline := bf.buildTimeline(running, now)

    var scheduled []string
    freeCPUs := bf.TotalCPUs
    freeGPUs := bf.TotalGPUs
    freeMem  := bf.TotalMem

    // First pass: schedule highest-priority jobs that fit now.
    var stillPending []Job
    for _, job := range pending {
        if job.CPUs <= freeCPUs && job.GPUs <= freeGPUs && job.MemMB <= freeMem {
            scheduled = append(scheduled, job.ID)
            freeCPUs -= job.CPUs
            freeGPUs -= job.GPUs
            freeMem  -= job.MemMB
        } else {
            stillPending = append(stillPending, job)
        }
    }

    // Second pass: backfill around the highest-priority blocked job.
    if len(stillPending) > 0 {
        head := stillPending[0]
        earliest := bf.earliestStart(head, timeline, now)

        for _, job := range stillPending[1:] {
            if now.Add(job.MaxDur).Before(earliest) ||
               now.Add(job.MaxDur).Equal(earliest) {
                if job.CPUs <= freeCPUs && job.GPUs <= freeGPUs &&
                   job.MemMB <= freeMem {
                    scheduled = append(scheduled, job.ID)
                    freeCPUs -= job.CPUs
                    freeGPUs -= job.GPUs
                    freeMem  -= job.MemMB
                }
            }
        }
    }
    return scheduled
}

// buildTimeline creates time-ordered resource events from running jobs.
func (bf *BackfillScheduler) buildTimeline(
    running []Job,
    now time.Time,
) []TimelineEntry {
    var tl []TimelineEntry
    for _, job := range running {
        // In production, remaining duration comes from actual elapsed time.
        tl = append(tl, TimelineEntry{
            At:   now.Add(job.MaxDur),
            CPUs: -job.CPUs,
            GPUs: -job.GPUs,
            MemMB: -job.MemMB,
        })
    }
    sort.Slice(tl, func(i, j int) bool {
        return tl[i].At.Before(tl[j].At)
    })
    return tl
}

// earliestStart finds the first time a job can acquire its resources.
func (bf *BackfillScheduler) earliestStart(
    job Job,
    timeline []TimelineEntry,
    now time.Time,
) time.Time {
    availCPU := bf.TotalCPUs
    availGPU := bf.TotalGPUs
    availMem := bf.TotalMem

    for _, e := range timeline {
        availCPU += e.CPUs
        availGPU += e.GPUs
        availMem += e.MemMB
        if job.CPUs <= availCPU && job.GPUs <= availGPU &&
           job.MemMB <= availMem {
            return e.At
        }
    }
    return now.Add(24 * time.Hour) // fallback horizon
}
```

The configuration knobs matter. On Frontera at TACC, `bf_interval=30` means the backfill loop runs every 30 seconds, `bf_max_time=60` limits each loop to one minute of wall-clock time, and `bf_window=4320` projects two days forward. These parameters trade scheduling quality for controller CPU usage; a smaller window evaluates fewer jobs but may miss opportunities, while a larger window improves packing at the cost of O(n log n) timeline scans.

**Gang scheduling** is a natural complement to backfill for distributed training workloads. An MPI job or a PyTorch DistributedDataParallel training run cannot start until every requested rank has been allocated. If the scheduler assigns four nodes to an eight-node job, the allocated four sit idle while waiting for the remainder, wasting GPU-hours. SLURM implements gang scheduling through job reservations: the scheduler reserves a future time slice at which all resources will be simultaneously available, then backfills around that reservation. A simplified gang-allocation algorithm is shown below.

```go
// gangSchedule attempts an all-or-nothing allocation.
func (bf *BackfillScheduler) gangSchedule(
    job Job,
    nodes []Node,
    required int,
) ([]Node, bool) {
    var selected []Node
    for _, n := range nodes {
        if n.FreeCPUs >= job.CPUs && n.FreeGPUs >= job.GPUs &&
           n.FreeMem >= job.MemMB {
            selected = append(selected, n)
            if len(selected) == required {
                // Deduct resources atomically.
                for i := range selected {
                    selected[i].FreeCPUs -= job.CPUs
                    selected[i].FreeGPUs -= job.GPUs
                    selected[i].FreeMem  -= job.MemMB
                }
                return selected, true
            }
        }
    }
    return nil, false // all-or-nothing failed
}
```

### 7.1.2 Multifactor Priority: Age + Fair-Share + Job-Size + QoS

SLURM does not use a single scalar priority value. Its multifactor priority plugin computes a weighted sum of six orthogonal components:

```
JobPriority =
    SiteFactor
  + PriorityWeightAge      x age_factor(job)
  + PriorityWeightFairshare x fairshare_factor(user)
  + PriorityWeightJobSize   x job_size_factor(job)
  + PriorityWeightPartition x partition_factor(partition)
  + PriorityWeightQOS       x qos_factor(qos)
  + SUM_i( TRES_weight_i  x TRES_factor_i )
  - nice_factor
```

- **Age factor** increases linearly from 0 to 1 as a job waits in the queue, preventing starvation.
- **Fair-share factor** decreases as a user consumes more than their entitled share, implementing the Fair-Tree algorithm that guarantees hierarchical fairness across organizational branches.
- **Job-size factor** rewards larger jobs (more nodes, longer wall times) to incentivize high-throughput work.
- **Partition factor** penalizes or favors specific node pools.
- **QOS factor** enforces service tiers; a "premium" QOS can add a fixed offset that overrides other components.
- **TRES factors** allow fine-grained weighting of individual resource types (GPU-hours, memory-hours).

All weights are normalized so that the maximum theoretical priority is bounded, and administrators can toggle components on or off without restarting `slurmctld`.

### 7.1.3 GRES: Generic Resource Scheduling for GPU/FPGA

SLURM handles GPUs, FPGAs, and other non-standard resources through **GRES** (Generic Resource Scheduling). Nodes declare available devices in `slurm.conf`:

```ini
GresTypes=gpu,mic
NodeName=node[01-16] Gres=gpu:a100:4,gpu:h100:2
```

Jobs request resources via the command line (`sbatch --gres=gpu:a100:2`), and SLURM enforces isolation through Linux cgroups, ensuring a job cannot access a GPU it did not request. GRES plugins are dynamically loaded, so adding support for a new accelerator requires only a shared library that implements the GRES API -- counting devices, reporting health, and performing pre-job setup such as setting CUDA_VISIBLE_DEVICES.

## 7.2 HashiCorp Nomad

If SLURM represents the heavyweight, policy-rich end of the scheduling spectrum, Nomad occupies the opposite pole: a single, sub-50 MB binary that can deploy a multi-region cluster in minutes. Nomad's relevance to HelixCluster is direct. Where Kubernetes ships as a constellation of API servers, etcd clusters, controller managers, and kubelets, Nomad ships as one file. That design choice enables deployment scenarios -- edge nodes, air-gapped environments, rapid disaster recovery -- that HelixCluster must also support.

### 7.2.1 Single Binary < 50 MB

Nomad's server and client modes are toggled by command-line flags, not by separate binaries:

```bash
# Start a server (bootstrap leader)
nomad agent -server -bootstrap-expect=3 -data-dir=/var/nomad

# Start a client (any machine that will run workloads)
nomad agent -client -servers=192.168.1.10 -data-dir=/var/nomad
```

A three-server cluster can be operational in under five minutes, with no external dependencies beyond a gossip protocol for membership and Raft for consensus. This is not merely a packaging convenience; it fundamentally changes the operational model. Upgrades are in-place binary swaps. Recovery from total control-plane loss is `nomad server force-leader` on the most recent data directory. For HelixCluster, which targets edge data centers with limited on-site expertise, this operational simplicity is a hard requirement.

### 7.2.2 Device Plugins: Extensible GPU/FPGA/NPU Discovery

Nomad's device plugin system, introduced in version 0.9, is the cleanest extensible-hardware abstraction in production orchestration. During the **fingerprinting** phase, which runs periodically on every client node, each loaded plugin enumerates the devices it manages and reports a structured capability vector:

```go
package device

// PluginAPI is the interface that every device plugin must implement.
type PluginAPI interface {
    // Name returns the canonical device type, e.g. "nvidia/gpu".
    Name() string

    // Fingerprint streams detected devices to the Nomad client.
    // Called at startup and at configurable intervals thereafter.
    Fingerprint(ctx context.Context) ([]*DeviceGroup, error)

    // Reserve is invoked by the client before launching a task
    // that requested this device. The plugin returns environment
    // variables and host-specific paths (e.g. /dev/nvidia0).
    Reserve(deviceIDs []string) (*ContainerReservation, error)
}

// DeviceGroup describes a homogeneous set of devices.
type DeviceGroup struct {
    Vendor string          // "nvidia", "amd", "xilinx"
    Type   string          // "gpu", "fpga", "npu"
    Name   string          // "Tesla V100-SXM2-16GB"
    Devices []*DeviceInfo
    Attributes map[string]*Attribute // e.g. memory_clock, pci_bandwidth
}

type DeviceInfo struct {
    ID         string
    Health     HealthState // Healthy | Unhealthy
    Resources  *Resources  // allocatable units
}
```

The scheduler uses these attributes for placement decisions without knowing anything about the underlying hardware. A job specification can request two GPUs with more than 10 GiB of memory and express an affinity for V100-class accelerators:

```hcl
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
```

The device plugin model decouples hardware support from the core scheduler. Adding a new TPU generation or a custom inference accelerator requires only a plugin binary that implements `Fingerprint` and `Reserve`; no changes to the Nomad server are necessary. HelixCluster should adopt this exact pattern: a gRPC-based device plugin protocol with standardized capability advertisement and per-task reservation hooks.

### 7.2.3 Bin Packing + Anti-Affinity

Nomad's scheduler uses a two-phase approach. **Feasibility checking** filters nodes by hard constraints: datacenter membership, health status, driver availability, and resource sufficiency. **Ranking** scores the remaining nodes using a bin-packing heuristic that prefers the node with the least remaining capacity after the allocation, thereby maximizing density. Anti-affinity rules are automatically applied to spread instances of the same job across failure domains, reducing correlated outages.

Optimistic concurrency enables multiple scheduler workers to run in parallel. Each worker constructs an allocation plan against a cached copy of cluster state, then submits the plan to a centralized **plan queue** on the leader. The leader detects conflicts (two workers assigning the same GPU slot) and rejects the offending plan partially or entirely. Schedulers receive feedback and explore alternate placements. This architecture, inspired by Google's Omega, yields near-linear scalability with the number of scheduler instances.

## 7.3 Apache Spark

Apache Spark is not a cluster scheduler in the traditional sense; it is a data-processing engine that embeds its own scheduling logic. Yet its DAG scheduler and data-locality optimizations are among the most influential scheduling designs in modern distributed computing, directly inspiring the execution engines of TensorFlow, Ray, and Dask. Understanding Spark's two-level scheduling -- logical planning followed by physical placement -- is essential for any compute framework that will run data-intensive workloads.

### 7.3.1 DAG Scheduler, Data Locality

When a Spark application starts, the Driver Program creates a `SparkContext` and translates user code into a **logical execution plan** represented as a Directed Acyclic Graph of stages. The DAG scheduler draws stage boundaries at shuffle operations -- wide dependencies such as `groupByKey` or `join` -- and pipelines narrow transformations (map, filter) within each stage. This pipelining is the fundamental optimization: instead of writing intermediate results to disk between each operator, as Hadoop MapReduce does, Spark threads operators together into a single execution unit, reducing task-launch overhead from approximately 10 seconds per MapReduce task to roughly 5 milliseconds per Spark task.

The DAG scheduler converts logical stages into **TaskSets** -- one task per data partition -- and hands them to the `TaskSchedulerImpl` for physical placement. Physical scheduling is where data locality enters. The TaskScheduler queries each executor for which data blocks it holds (via the BlockManager) and attempts to schedule tasks on nodes that already possess the input data. The locality levels, in order of preference, are:

1. **PROCESS_LOCAL** -- data is in the JVM heap of the target executor.
2. **NODE_LOCAL** -- data is on the same physical node, in a different process.
3. **RACK_LOCAL** -- data is on a different node in the same network rack.
4. **ANY** -- data must be fetched over the network.

Spark waits a configurable delay at each level before falling back, trading latency for locality. On HDFS-backed clusters, this optimization routinely reduces network traffic by 70 % or more. For HelixCluster, where edge devices may hold local shards of a distributed dataset, the same principle applies: scheduling a compute task on a node that already possesses the input data avoids a cross-network transfer that could saturate a low-bandwidth last-mile link.

The fault-tolerance model is equally instructive. Spark tracks the **lineage** of every RDD partition -- the chain of transformations that produced it -- so that lost partitions can be recomputed from their parent datasets rather than recovered from replication. This design assumes that re-computation is cheaper than storage, a trade-off that holds for in-memory transforms but not for long-running iterative algorithms. For the latter, Spark provides explicit checkpointing to persistent storage.

| Characteristic | Spark | Hadoop MapReduce |
|---|---|---|
| In-memory caching between stages | Yes | No |
| Task launch overhead | ~5 ms | ~10 s |
| Shuffle strategy | Sort-based with consolidated output files | Simple hash-based |
| Stage pipelining | Narrow ops fused into single tasks | Strict map-then-reduce |
| Fault recovery | RDD lineage recomputation | Disk-based replication |

## 7.4 BOINC

The Berkeley Open Infrastructure for Network Computing (BOINC) orchestrates millions of heterogeneous, sporadically available, and fundamentally untrusted worker nodes for scientific computing projects such as SETI@home and Rosetta@home. Its scheduling innovations -- redundant execution, adaptive trust scoring, and a credit-based incentive system -- are directly applicable to HelixCluster's edge-computing tier, where devices may be consumer GPUs, mobile phones, or Raspberry Pi clusters with no administrative oversight.

### 7.4.1 Redundant Execution for Untrusted Devices

BOINC's core insight is that correctness cannot be assumed from the edge. Volunteers might overclock hardware, run modified clients, or simply have failing memory. The solution is a **quorum validator**: every work unit is dispatched to at least three independent clients, and the server-side validator compares returned result files byte-for-byte (or via application-specific equivalence functions). Once a minimum quorum of matching results is achieved, one is designated the **canonical result** and credited to the participants. Dissenting results are discarded.

### 7.4.2 Adaptive Trust Scoring

Blindly triplicating every work unit is wasteful. BOINC implements **adaptive replication**: clients with a history of consistent results are gradually assigned fewer replicas, while new or erratic clients receive more. The trust-scoring algorithm maintains a per-host reliability score and dynamically adjusts replication depth.

```python
# Simplified BOINC adaptive trust scoring
class HostTrust:
    def __init__(self):
        self.successes = 0
        self.failures  = 0
        self.replica_target = 3  # initial quorum size

    def update(self, result_agrees_with_quorum: bool):
        if result_agrees_with_quorum:
            self.successes += 1
        else:
            self.failures  += 1

        # Reliability ratio drives replica depth.
        total = self.successes + self.failures
        if total == 0:
            return

        reliability = self.successes / total
        if reliability > 0.99 and total > 100:
            self.replica_target = 1      # fully trusted
        elif reliability > 0.95 and total > 20:
            self.replica_target = 2      # mostly trusted
        else:
            self.replica_target = 3      # default or penalized

        # Hard floor for new hosts.
        if total < 5:
            self.replica_target = max(self.replica_target, 3)

    def should_blacklist(self) -> bool:
        # Persistent dissenters are removed from the pool.
        return self.failures > 10 and \
               self.successes / (self.successes + self.failures) < 0.5
```

BOINC's credit system provides the economic layer. Each validated result earns **cobblestones**, a normalized unit proportional to the product of CPU time and benchmarked FLOPS. One cobblestone equals the daily output of a 1 GFLOPS processor running for 86,400 seconds. This metric enables cross-device, cross-platform contribution tracking without revealing sensitive workload details.

## 7.5 Scheduling Lessons

After examining these four systems, a set of unifying principles emerges for HelixCluster's scheduler design.

| System | Core Innovation | HelixCluster Adoption Priority |
|---|---|---|
| SLURM | Backfill scheduling + multifactor priority | Critical -- implement immediately for cluster utilization |
| Nomad | Single-binary device plugin architecture | Critical -- adopt for edge deployment and hardware abstraction |
| Spark | DAG-based execution planning + data locality | High -- adapt for workload dependency graphs and locality-aware placement |
| BOINC | Redundant execution + adaptive trust scoring | Medium-High -- essential for untrusted/edge device tiers |

The architecture that synthesizes these lessons is a **hybrid shared-state scheduler**. Multiple scheduler instances run in parallel with optimistic concurrency control, each operating against a cached, eventually consistent view of cluster state. The backfill engine continuously scans for packing opportunities, using user-declared wall times to build the resource-availability timeline. Gang scheduling reservations are treated as atomic blocks that backfill must not violate. The multifactor priority formula orders the pending queue, with configurable weights for age, fair-share, job size, and QOS tier. Device plugins, following Nomad's gRPC model, fingerprint GPUs, FPGAs, NPUs, and future accelerators without core scheduler changes. And for workloads dispatched to untrusted edge devices, the BOINC-inspired quorum validator and adaptive trust scorer determine replication depth dynamically.

| Scheduling Pattern | When to Use | Implementation Approach |
|---|---|---|
| Backfill | Cluster has jobs with diverse sizes and durations | Resource-availability timeline + gap-fitting loop |
| Gang scheduling | MPI or distributed training workloads | All-or-nothing reservation with atomic resource deduction |
| Device plugins | Heterogeneous hardware (GPU/FPGA/NPU) | gRPC fingerprinting + per-task reservation hooks |
| Multifactor priority | Multiple tenants with competing fairness criteria | Weighted sum of normalized age, fair-share, job-size, QOS factors |
| Redundant execution | Untrusted or failure-prone worker nodes | Quorum validation + adaptive replication depth |
| Data locality | Data-intensive workloads with large inputs | Schedule on nodes that already hold required blocks/partitions |

The Go code presented in this chapter -- the backfill timeline builder, the gang-allocation routine, and the device plugin interface -- form the structural skeleton of HelixCluster's scheduling subsystem. They are not production-complete; a real implementation must add preemption, node affinity, topology awareness, and graceful degradation under partition failures. But they encode the right invariants: do not delay a higher-priority job to backfill a lower one, do not start a distributed job until all its ranks can be satisfied, and never couple hardware-specific logic into the core scheduling loop. These invariants, proven across decades of supercomputing and millions of volunteer devices, are the foundation on which HelixCluster's compute layer is built.
