# Helix Cluster OS — Technical Research Compendium

> **Document ID:** TRC-001
> **Revision:** 1
> **Date:** 2026-03-05
> **Authority:** HXC-1639, Constitution §11.4.106
> **Scope:** 12-domain deep technical analysis for the Helix Cluster OS project

---

## Table of Contents

1. [Domain 1: Distributed Cluster OS Architecture](#domain-1-distributed-cluster-os-architecture)
2. [Domain 2: Deterministic Simulation Testing](#domain-2-deterministic-simulation-testing)
3. [Domain 3: GPU Orchestration](#domain-3-gpu-orchestration)
4. [Domain 4: WireGuard Mesh VPN](#domain-4-wireguard-mesh-vpn)
5. [Domain 5: SPIFFE/SPIRE Identity Framework](#domain-5-spiffespire-identity-framework)
6. [Domain 6: SWIM Gossip Protocol](#domain-6-swim-gossip-protocol)
7. [Domain 7: Chaos Engineering](#domain-7-chaos-engineering)
8. [Domain 8: Post-Quantum Cryptography](#domain-8-post-quantum-cryptography)
9. [Domain 9: Heterogeneous Compute Management](#domain-9-heterogeneous-compute-management)
10. [Domain 10: Etcd Integration Patterns](#domain-10-etcd-integration-patterns)
11. [Domain 11: Omega-Model Scheduler](#domain-11-omega-model-scheduler)
12. [Domain 12: Production Readiness Review](#domain-12-production-readiness-review)

---

# Domain 1: Distributed Cluster OS Architecture

## 1.1 Technical Background

### 1.1.1 The Cluster Operating System Concept

A cluster operating system (Cluster OS) is a software layer that abstracts a collection of networked machines into a unified computing platform. Unlike traditional operating systems that manage a single machine's resources (CPU, memory, I/O), a Cluster OS manages distributed resources across multiple nodes, providing primitives for scheduling, identity, networking, storage, and fault tolerance. The key insight is that cluster-level concerns — resource allocation, service discovery, consensus, and failure recovery — are sufficiently universal to warrant a dedicated operating system layer, just as process scheduling and virtual memory warranted Unix.

The concept has evolved through several generations. Early distributed systems like Locus (1983) and Sprite (1988) attempted transparent distributed file systems and process migration. The modern era began with Google's internal cluster management stack (Borg, Omega, Kubernetes) and Apache Mesos at UC Berkeley, both recognizing that multi-tenant resource sharing at datacenter scale requires OS-like abstractions.

### 1.1.2 Kubernetes Architecture Patterns

Kubernetes (K8s) is the dominant open-source cluster OS, originally developed at Google based on lessons from Borg. Its architecture follows a hub-and-spoke model with a centralized API server as the single source of truth.

**Core Components:**

- **API Server (kube-apiserver):** The front-end for the Kubernetes control plane. All communication between components flows through the API server, which validates and stores API objects in etcd. It implements a declarative REST API with watch support for real-time change notification. The API server is stateless — all persistent state lives in etcd — enabling horizontal scaling via load balancers.

- **etcd:** A distributed key-value store providing strong consistency via Raft consensus. etcd stores all Kubernetes cluster state: pod specifications, service definitions, config maps, secrets, and cluster metadata. It uses MVCC (multi-version concurrency control) with revision-based watches, enabling clients to observe state changes from any historical revision. The default 2GB quota can be increased but etcd is designed for small, frequently-updated values — large blobs should live in external storage.

- **Controller Manager (kube-controller-manager):** A daemon that embeds the core control loops (controllers). Each controller watches the API server for changes to a specific resource type and drives the current state toward the desired state. Examples: ReplicaSet controller (ensures the right number of pod replicas), Node controller (responds to node failures), Service Account controller (creates default accounts). Controllers are idempotent — they can be restarted and resume from any state without side effects.

- **Scheduler (kube-scheduler):** Assigns newly created pods to nodes based on resource requirements, affinity/anti-affinity rules, taints and tolerations, and custom extension points. The default scheduler uses a two-phase algorithm: **Filtering** (eliminate nodes that don't meet requirements) then **Scoring** (rank remaining nodes by preference). Scheduling Framework (introduced in v1.15) allows plugins at 9 extension points: QueueSort, PreFilter, Filter, PostFilter, PreScore, Score, NormalizeScore, Reserve, Permit, PreBind, Bind, PostBind.

- **Kubelet:** The node agent that runs on every worker node. It registers the node with the API server, reports node status and resource usage, and ensures pods are running in containers according to their specifications. The kubelet uses a pod lifecycle event generator (PLEG) to detect container state changes.

- **Kube-proxy:** Implements network routing for Kubernetes Services. It maintains iptables or IPVS rules on each node to redirect traffic from service ClusterIPs to backend pods. In newer versions, eBPF-based implementations (Cilium) provide better performance.

**Kubernetes Scheduling Deep Dive:**

The Kubernetes scheduler operates on a shared-state model but with a single scheduler instance (or active-passive HA). Each scheduling cycle:

1. A pod enters the scheduling queue (priority queue with backoff).
2. The scheduler reads the current cluster state from the API server (cached informer).
3. Filter phase: For each node, run all Filter plugins. A node that passes all filters is "feasible."
4. Score phase: For each feasible node, run all Score plugins. Each returns a 0–100 score.
5. NormalizeScore: Normalize scores across plugins.
6. Select the highest-scoring node.
7. Reserve the node (optimistic; may fail if state changed since read).
8. Bind the pod to the node via the API server.

**Limitations of Kubernetes for Helix:**

1. **Single-scheduler bottleneck:** Only one scheduler can make binding decisions at a time. The scheduling framework allows customization but not parallelism.
2. **No GPU-first scheduling:** GPU scheduling is bolted on via device plugins. There's no native support for MIG, time-slicing, or multi-GPU gang scheduling.
3. **No heterogeneous compute:** Kubernetes assumes x86_64 Linux containers. ARM and edge devices are second-class citizens.
4. **No dual-revenue model:** Kubernetes doesn't understand marketplace economics or bidirectional resource monetization.
5. **Flat identity model:** Service accounts are limited; no SPIFFE/SPIRE integration by default.

### 1.1.3 Apache Mesos: Two-Level Scheduling

Apache Mesos, developed at UC Berkeley (2011), introduced the two-level scheduling model that influenced both Omega and Kubernetes. Mesos itself does not schedule tasks — it offers resources to frameworks (like Marathon, Chronos, or Spark), which decide whether to accept or decline.

**Architecture:**

- **Mesos Master:** The central arbitrator. It collects resource offers from agents and forwards them to registered frameworks. The master uses ZooKeeper for leader election and failover.
- **Mesos Agent:** Runs on each worker node, reports available resources (CPU, memory, ports, GPU via GRES), and launches tasks as directed by frameworks.
- **Framework Scheduler:** A framework-specific scheduler that receives resource offers and decides how to use them. It can accept an offer (launch a task), decline it, or set a filter for future offers.
- **Framework Executor:** Runs on the agent and manages the framework's tasks. The default executor launches processes; custom executors can launch containers, JVM processes, etc.

**Resource Offer Protocol:**

```
Agent → Master: "I have 4 CPUs, 16GB RAM, 1 GPU available"
Master → Framework: "Here's an offer: 4 CPUs, 16GB RAM, 1 GPU on agent-3"
Framework → Master: "I'll take 2 CPUs, 8GB RAM for my Spark executor"
Master → Agent: "Launch task 'spark-exec-1' with 2 CPUs, 8GB RAM"
```

**Key Insight — Resource Allocation Module (DRF):**

Mesos uses Dominant Resource Fairness (DRF) to allocate resources across frameworks. DRF identifies each framework's dominant resource (the one it uses the most of, relative to cluster capacity) and equalizes dominant shares. For a cluster with 100 CPUs and 100GB RAM, if Framework A uses 50 CPUs (50%) and 20GB (20%), its dominant share is 50%. If Framework B uses 20 CPUs (20%) and 50GB (50%), its dominant share is 50%. DRF ensures both frameworks have equal dominant shares.

**Limitations of Mesos for Helix:**

1. **Framework latency:** The offer-accept cycle adds latency (round-trip to framework scheduler). Not suitable for sub-second scheduling decisions.
2. **No optimistic concurrency:** If a framework declines an offer, those resources sit idle until the next offer cycle. No framework can optimistically claim resources and resolve conflicts later.
3. **ZooKeeper dependency:** ZooKeeper is operationally complex; Helix uses etcd.
4. **Limited GPU support:** GPU scheduling via GRES is primitive; no MIG or time-slicing.

### 1.1.4 Nomad: Hash-Based Scheduling

HashiCorp Nomad uses a simpler architecture based on Serf (SWIM-based gossip) for membership and a single active scheduler (with HA standby) for placement decisions.

**Architecture:**

- **Nomad Server:** Runs the Raft consensus protocol (via hashicorp/raft) for consistent state. One server is the leader; others are followers. The leader evaluates all scheduling decisions.
- **Nomad Client:** Runs on each node, registers node resources, and executes task drivers (Docker, exec, Java, QEMU, raw fork).
- **Serf Gossip:** Used for cluster membership and LAN/WAN topology. Nodes detect failures via SWIM and propagate membership changes.

**Scheduling Algorithm:**

Nomad's scheduler uses a bin-packing approach with scoring:

1. **Feasibility check:** Eliminate nodes that don't meet job constraints (resource, constraint, affinity).
2. **Scoring:** Score remaining nodes using a bin-packing algorithm that prefers already-loaded nodes (to consolidate workloads and free empty nodes).
3. **Allocation:** The highest-scoring node gets the allocation. If the node's available resources changed between evaluation and allocation, the allocation is rejected and re-queued.

**Limitations of Nomad for Helix:**

1. **Single scheduler:** Like Kubernetes, only one scheduler makes decisions at a time.
2. **No GPU marketplace integration:** Nomad has device plugins but no economic model.
3. **No CRDT-based sessions:** Nomad's task model doesn't support collaborative terminal sessions.

### 1.1.5 SLURM: HPC Scheduling

SLURM (Simple Linux Utility for Resource Management) is the dominant scheduler in HPC (High-Performance Computing) environments. It's used on 60% of the Top500 supercomputers.

**Architecture:**

- **slurmctld:** The central controller daemon. It manages the job queue, schedules jobs, and tracks cluster state. It can operate in HA with a standby controller.
- **slurmd:** The compute node daemon. It launches and monitors tasks, reports node status, and manages local resources.
- **slurmdbd:** The accounting database daemon. It stores job history, resource usage, and fair-share calculations.
- **Partition:** A logical grouping of nodes (similar to a Kubernetes namespace). Jobs are submitted to partitions.

**Key Features:**

- **Gang scheduling:** SLURM can simultaneously allocate all required resources for a parallel job. If any component fails, the entire allocation is held until all components can run simultaneously.
- **Job array:** Submit thousands of similar jobs as a single array.
- **Job steps:** A job can have multiple steps (e.g., compile → run → post-process), each with different resource requirements.
- **GRES (Generic Resources):** Extensible resource tracking for GPUs, NICs, licenses, etc. Each GRES has a count and can be allocated with file specifications (e.g., `--gres=gpu:tesla:2`).
- **MIG support:** SLURM 22.05+ supports NVIDIA MIG via `--gres=gpu:tesla:1g.5gb` syntax.
- **Fair-share scheduling:** Priority calculation based on historical usage, ensuring equitable resource distribution.

**SLURM Scheduling Algorithms:**

- **FIFO with backfill:** Default. Jobs run in submission order, but lower-priority jobs can be backfilled if they finish before a higher-priority job would start.
- **Multifactor priority:** Priority based on age, fair-share, job size, partition, and QOS.
- **Conservation:** Guaranteed access to a fraction of cluster resources.

**Limitations of SLURM for Helix:**

1. **Batch-oriented:** SLURM excels at batch jobs but has limited support for interactive sessions and real-time terminal multiplexing.
2. **Centralized scheduler:** Single controller (with HA standby); not designed for multi-scheduler parallelism.
3. **No CRDT or collaborative sessions:** SLURM has no concept of shared terminal state.
4. **No identity framework:** SLURM uses Linux PAM; no SPIFFE/SPIRE integration.
5. **No mesh VPN:** SLURM assumes flat network; no WireGuard or overlay networking.

### 1.1.6 Google Borg/Omega: Shared-State Scheduling

Google's internal cluster management has evolved through three generations: Borg → Omega → Kubernetes. The key architectural shift was from Borg's centralized scheduler to Omega's shared-state model with optimistic concurrency control.

**Borg Architecture:**

Borg manages Google's entire production workload. Key characteristics:

- **Single master** (with replicas for HA) that makes all scheduling decisions.
- **Cells:** A cell is a cluster of machines managed by one Borg master. Google operates dozens of cells, each with 10,000+ machines.
- **Allocs:** Resource reservations that can be shared across tasks (similar to Kubernetes pods).
- **Priority and preemption:** Tasks have priorities. Lower-priority tasks can be preempted to make room for higher-priority ones.
- **Borglets:** Per-node agents that report status and start/stop tasks.

**Omega Architecture:**

Omega (described in the 2013 SOSP paper "Omega: flexible, scalable schedulers for large compute clusters" by Schwarzkopf et al.) replaced Borg's centralized scheduler with a shared-state model:

- **Shared state:** All cluster state (node resources, task assignments, constraints) is stored in a centrally-managed, transactionally-consistent data store (based on Google's Chubby/Megastore).
- **Multiple schedulers:** Multiple independent schedulers read the shared state, make placement decisions, and attempt to commit their decisions using optimistic concurrency control (OCC).
- **Conflict detection:** If two schedulers try to assign tasks to the same resources simultaneously, the transaction system detects the conflict and one scheduler must retry. In practice, conflict rates are <5% because schedulers tend to target different resources.
- **Cell-level transactions:** All state changes are committed atomically. This enables gang scheduling — a scheduler can allocate resources for all tasks in a job in a single transaction, guaranteeing that either all tasks start or none do.

**Why Omega Matters for Helix:**

The Omega model is directly applicable to Helix's requirements:

1. **Heterogeneous schedulers:** Helix needs different scheduling strategies for GPU workloads (gang scheduling, MIG-aware), batch jobs (fair-share, backfill), interactive sessions (low-latency placement), and edge workloads (bandwidth-aware). Omega allows each strategy to be implemented as an independent scheduler.
2. **Optimistic concurrency:** The OCC model allows schedulers to operate in parallel without global locks. The <5% conflict rate observed at Google is acceptable for Helix.
3. **Atomic gang scheduling:** GPU training jobs require all GPUs simultaneously. Omega's transaction model enables this naturally.
4. **Extensibility:** New scheduling strategies can be added without modifying existing schedulers. Helix can add marketplace-aware scheduling, carbon-aware scheduling, or TAO-reward scheduling as independent schedulers.

### 1.1.7 Applying the Omega Model to Heterogeneous GPU Clusters

Helix's GPU cluster presents unique challenges that the original Omega paper didn't address:

1. **GPU fragmentation:** Unlike CPU and memory, GPUs cannot be trivially time-sliced. A node with 4 GPUs each 75% allocated has 0 usable GPUs, not 1. The scheduler must understand GPU granularity.

2. **MIG partitioning:** NVIDIA MIG (Multi-Instance GPU) allows an A100 to be partitioned into up to 7 instances. Each instance has dedicated memory and compute. The scheduler must understand MIG topologies and place workloads on compatible instances.

3. **GPU affinity:** Some workloads require GPUs on the same node (for NVLink interconnect). The scheduler must support gang scheduling across GPUs with affinity constraints.

4. **Dual-revenue nodes:** GPU nodes serve both HLX (internal Helix) workloads and TAO (external marketplace) workloads. The scheduler must understand priority preemption across revenue streams.

5. **Heterogeneous GPU vendors:** A cluster may have NVIDIA (CUDA), AMD (ROCm), Apple (Metal), and Intel (oneAPI) GPUs. The scheduler must match workload API requirements to GPU capabilities.

**Helix's Omega Implementation:**

Helix implements the Omega model using etcd as the shared state store and optimistic concurrency control via `mod_revision`:

```
┌─────────────┐   ┌─────────────┐   ┌─────────────┐
│ GPU Scheduler│   │ Batch Sched.│   │ Edge Sched. │
│  (gang, MIG) │   │ (fair-share)│   │ (bandwidth) │
└──────┬───────┘   └──────┬──────┘   └──────┬──────┘
       │                  │                  │
       └─────────────┬────┴──────────────┬───┘
                     │                   │
              ┌──────┴───────────────────┴──────┐
              │          etcd (shared state)       │
              │  /clusteros/scheduler/pool/*       │
              │  /clusteros/scheduler/queue/*      │
              │  /clusteros/scheduler/bindings/*   │
              │  OCC via mod_revision CAS          │
              └───────────────────────────────────┘
```

Each scheduler:
1. Reads the current resource pool from etcd.
2. Evaluates pending scheduling requests from its queue.
3. Proposes bindings via an etcd transaction (compare `mod_revision` of pool state).
4. If the CAS succeeds, the binding is committed.
5. If the CAS fails (conflict), the scheduler re-reads the pool and retries.

### 1.1.8 Best Practices for Distributed Cluster OS

**Idempotent Operations:**

Every operation in a distributed system must be idempotent — executing it multiple times must produce the same result as executing it once. This is critical because:

- Network retries can cause duplicate requests.
- Crash recovery may re-apply the same operation.
- Multiple schedulers may attempt the same binding (OCC conflict).

Implementation patterns:
- Use deterministic request IDs (UUID v5 from inputs).
- Check current state before applying changes (read-modify-write with CAS).
- Design state machines where transitions are self-correcting (e.g., deleting an already-deleted resource is a no-op).

```go
// Helix idempotent binding pattern
func (s *GPUScheduler) Bind(req ScheduleRequest) error {
    txn := etcd.Txn(ctx).
        If(clientv3.Compare(clientv3.ModRevision(poolKey), "=", req.PoolRev)).
        Then(
            clientv3.OpPut(bindingKey, bindingJSON),
            clientv3.OpPut(poolKey, updatedPoolJSON),
        )
    resp, err := txn.Commit()
    if !resp.Succeeded {
        return ErrConflictRetry // OCC conflict; re-read and retry
    }
    return nil
}
```

**Eventual Consistency:**

Helix uses a three-tier consistency model:

1. **Strong consistency** (etcd, PostgreSQL): Cluster metadata, scheduling state, audit logs.
2. **Session consistency** (Redis with CRDTs): Terminal session state, real-time UI updates.
3. **Eventual consistency** (SWIM gossip): Node membership, resource availability hints.

The key insight is that not all data needs strong consistency. Node heartbeats, resource utilization metrics, and cache aggregates can tolerate brief inconsistencies. Only scheduling decisions and identity assertions require strong consistency.

**Conflict Resolution:**

When concurrent schedulers conflict (<5% expected):

1. Detect: CAS failure in etcd transaction.
2. Back off: Exponential backoff with jitter (prevent thundering herd).
3. Re-read: Get fresh state from etcd.
4. Re-evaluate: Re-run scheduling logic with updated state.
5. Retry: Attempt binding again.

For CRDT conflicts in session state:

1. Use vector clocks to detect concurrent modifications.
2. Apply last-writer-wins (LWW) for non-critical state (layout, active window).
3. Use operational transforms for critical state (terminal I/O — merged via CRDT algorithm).

---

# Domain 2: Deterministic Simulation Testing

## 2.1 Technical Background

### 2.1.1 The FoundationDB Approach

FoundationDB (acquired by Apple in 2015) pioneered deterministic simulation testing (DST) as the primary quality assurance mechanism for distributed databases. The approach is radical in its completeness: **every** external interaction is mediated by a simulation layer, and the entire system is tested by running simulated workloads against simulated networks with injected faults.

**Core Principles:**

1. **Simulate everything:** All I/O — network sends/receives, file reads/writes, timer events — goes through a simulation transport. The real network, disk, and clock are never touched during simulation.

2. **Single-threaded execution:** The simulation runs on a single thread with a deterministic scheduler. There are no races, no non-determinism. Given the same random seed, the simulation produces the same execution trace every time.

3. **Time compression:** Simulated time advances independently of wall-clock time. A 10-day simulation might complete in 10 minutes. The simulation can fast-forward through periods of inactivity and slow down during bursts of activity.

4. **Fault injection:** The simulation can inject any fault at any point: network partitions, node crashes, disk failures, clock skew, slow I/O. These faults are deterministic — the same seed produces the same faults.

5. **Continuous simulation:** FoundationDB runs simulation continuously in CI. Every code change triggers a simulation that runs billions of operations. A production bug that isn't caught by simulation is considered a bug in the simulation, not just the system.

**The Simulation Transport Seam:**

FoundationDB's codebase has a clean seam between business logic and I/O:

```cpp
// Pseudo-code for FoundationDB's simulation transport
class SimTransport {
    // Instead of real network:
    Future<Void> send(NetworkAddress to, Packet packet) {
        // Schedule delivery at a simulated future time
        // With probability p, drop the packet (network fault)
        // With probability q, delay the packet (network latency)
        sim.schedule(to, packet, sim.now() + latency);
    }

    // Instead of real timers:
    Future<Void> delay(double seconds) {
        // Return a future that resolves at simulated time
        return sim.at(sim.now() + seconds);
    }

    // Instead of real disk:
    Future<Void> read(FileID file, offset, length) {
        // Simulate disk latency (10ms HDD, 0.1ms SSD)
        // With probability p, return I/O error
        sim.delay(diskLatency);
    }
};
```

**Results:**

FoundationDB has not had a data-loss bug in production since 2014. The simulation runs 1 billion+ operations per test cycle and catches bugs that would be impossible to reproduce in non-deterministic testing.

### 2.1.2 CockroachDB's Metamorphic Testing

CockroachDB uses a different but complementary approach called metamorphic testing. Instead of simulating the entire system, it runs multiple instances of the database with different configurations and compares results.

**Metamorphic Testing:**

1. **Run the same workload** against multiple CockroachDB clusters with different configurations:
   - Different numbers of nodes (3, 5, 7)
   - Different replication factors (3, 5)
   - Different leaseholder placement strategies
   - Different network latencies

2. **Compare results:** If two configurations produce different results for the same query, one of them has a bug.

3. **Differential testing:** Run the same SQL against CockroachDB and a reference database (PostgreSQL). If results differ, CockroachDB has a correctness bug.

4. **TLP (TLa+ Proof):** CockroachDB has formal specifications of key algorithms (transaction recovery, range split/merge) verified with TLA+.

### 2.1.3 Jepsen Testing Methodology

Jepsen (by Kyle Kingsbury/Aphyr) is an independent testing framework that evaluates distributed databases for consistency anomalies under fault conditions.

**Methodology:**

1. **Set up a cluster** of the database under test.
2. **Generate concurrent operations** (reads, writes, transactions) against the cluster.
3. **Inject faults:** Network partitions, node kills, clock skew, disk failures.
4. **Collect a history** of all operations and their results.
5. **Analyze the history** using linearizability checking (Knossos) to find consistency violations.

**Key Findings:**

Jepsen has found consistency bugs in virtually every distributed database tested, including:
- MongoDB (2013, 2015, 2020): Lost updates, stale reads under partition
- Redis Cluster (2016): Split-brain, lost writes
- Cassandra (2013, 2020): Lost writes, stale reads
- etcd (2017): Stale reads under partition (fixed)
- RabbitMQ (2014): Lost messages under partition
- Dgraph (2018): Lost updates, stale reads

**Linearizability Checking:**

Jepsen uses the Knossos checker, which implements the Wing & Gong algorithm for linearizability verification. Given a concurrent history of operations, it determines whether there exists a sequential interleaving that:
1. Respects the real-time ordering of non-overlapping operations.
2. Satisfies the sequential specification of each operation.

This is computationally expensive (NP-complete in the general case) but tractable for small histories (thousands of operations).

### 2.1.4 Rust Turmoil Framework

Turmoil is a deterministic simulation framework for Rust distributed systems, developed by the Tokio team. It provides a simulated network mesh and timer system that enables deterministic testing of distributed protocols.

**Key Features:**

- **Simulated network:** All network I/O goes through a simulated mesh. The test controls message delivery order, latency, and loss.
- **Deterministic timers:** Timer operations resolve in simulation time, not wall-clock time.
- **Host abstraction:** Each simulated host has its own network interface and can crash/restart independently.
- **Hold/Release:** The test can "hold" messages between specific hosts (simulating partitions) and "release" them later.

```rust
// Turmoil example (Rust)
#[test]
fn test_leader_election_under_partition() {
    let mut sim = turmoil::Builder::new()
        .hosts(5)
        .build();

    // Host 0 starts as leader
    sim.host(0, |h| async move {
        run_raft(h, Role::Leader).await;
    });

    // Partition hosts 0-1 from hosts 2-4
    sim.partition(vec![0, 1], vec![2, 3, 4]);

    // Hosts 2-4 should elect a new leader
    sim.run_until(|| {
        sim.host(2).role == Role::Leader
    });

    // Heal partition — verify cluster converges
    sim.heal_partition(vec![0, 1], vec![2, 3, 4]);
    sim.run_until(|| cluster_is_consistent(&sim));
}
```

### 2.1.5 Building DST for Helix

Helix's deterministic simulation framework (`pkg/dst/`, `cmd/dst-sim`) adapts FoundationDB's approach to the Go ecosystem and Helix's specific requirements.

**SimTransport Seam:**

The simulation replaces all I/O with a simulated transport:

```go
// pkg/dst/transport.go
type SimTransport struct {
    sim     *Simulation
    hostID  string
    inboxes map[string][]*Packet  // hostID → pending packets
}

func (t *SimTransport) Send(to string, msg proto.Message) error {
    latency := t.sim.networkLatency(t.hostID, to)
    deliveryTime := t.sim.now().Add(latency)

    packet := &Packet{
        From:    t.hostID,
        To:      to,
        Message: msg,
        DeliverAt: deliveryTime,
    }

    // BUGGIFY: maybe drop the packet
    if t.sim.buggify("network_drop") {
        return nil // silent drop
    }

    t.sim.schedule(packet)
    return nil
}

func (t *SimTransport) Receive() (<-chan proto.Message, error) {
    ch := make(chan proto.Message, 1)
    go func() {
        // Wait for next scheduled packet
        packet := t.sim.nextPacket(t.hostID)
        ch <- packet.Message
    }()
    return ch, nil
}
```

**BUGGIFY Hooks:**

BUGGIFY is a macro/function that returns true with a configured probability, enabling fault injection at any point in the code:

```go
// pkg/dst/buggify.go
func BUGGIFY(label string) bool {
    if sim == nil {
        return false // production mode: never inject
    }
    return sim.rollDice(label, 0.01) // 1% probability per call
}

// Usage in production code:
func (n *Node) SendHeartbeat() error {
    if BUGGIFY("heartbeat_drop") {
        return nil // drop heartbeat silently
    }
    return n.transport.Send(n.coordinator, &Heartbeat{})
}
```

**Time Compression:**

The simulation controls the clock:

```go
// pkg/dst/clock.go
type SimClock struct {
    sim *Simulation
}

func (c *SimClock) Now() time.Time {
    return c.sim.now() // simulated time, not time.Now()
}

func (c *SimClock) Sleep(d time.Duration) {
    c.sim.advance(d) // advance simulated time
}
```

**Simulation Loop:**

```go
func (s *Simulation) Run() {
    for !s.complete() {
        // 1. Advance time to next event
        s.advanceToNextEvent()

        // 2. Deliver scheduled packets
        s.deliverPackets()

        // 3. Process timers
        s.fireTimers()

        // 4. Maybe inject faults
        if s.tick % 1000 == 0 {
            s.maybeInjectFault()
        }

        // 5. Check invariants
        s.checkInvariants()
    }
}
```

### 2.1.6 Key Insight: Every Production Bug Should Get a Simulation Test

The meta-principle from FoundationDB: **every production bug should result in a new simulation test.** This ensures:

1. The bug is reproducible in simulation.
2. The fix is verified in simulation.
3. The bug cannot regress (the simulation runs on every commit).

Helix applies this principle:
- When a scheduling conflict causes a GPU double-allocation, write a simulation test that reproduces the conflict.
- When a network partition causes split-brain in session state, write a simulation test that reproduces the partition.
- When a node crash causes orphaned reservations, write a simulation test that reproduces the crash.

---

# Domain 3: GPU Orchestration

## 3.1 Technical Background

### 3.1.1 NVIDIA MIG (Multi-Instance GPU) Management

NVIDIA MIG, introduced with the A100 GPU (2020), allows a single GPU to be partitioned into multiple isolated instances, each with dedicated memory and compute resources. This is fundamentally different from time-slicing or MPS — MIG provides hardware-level isolation.

**MIG Profiles:**

| Profile | Compute | Memory | Instances per A100 80GB |
|---------|---------|--------|------------------------|
| 1g.5gb | 1/8 SMs | 5 GB | Up to 7 |
| 1g.10gb | 1/8 SMs | 10 GB | Up to 7 |
| 2g.10gb | 2/8 SMs | 10 GB | Up to 3 |
| 2g.20gb | 2/8 SMs | 20 GB | Up to 3 |
| 3g.20gb | 3/8 SMs | 20 GB | Up to 2 |
| 3g.40gb | 3/8 SMs | 40 GB | Up to 2 |
| 4g.20gb | 4/8 SMs | 20 GB | Up to 1 |
| 4g.40gb | 4/8 SMs | 40 GB | Up to 1 |
| 7g.40gb | 7/8 SMs | 40 GB | Up to 1 |
| 8g.40gb | Full | 40 GB | 1 (entire GPU) |

**MIG Management Commands:**

```bash
# Enable MIG mode on GPU 0
nvidia-smi -i 0 -mig 1

# Create a 1g.5gb MIG instance
nvidia-smi mig -cgi 1g.5gb -C

# List MIG instances
nvidia-smi mig -lgi

# List MIG compute instances
nvidia-smi mig -lci
```

**MIG Scheduling Considerations for Helix:**

1. **Profile selection:** The scheduler must understand which MIG profiles are available on each GPU and match workload requirements to compatible profiles.
2. **Gang scheduling:** A multi-GPU training job may need multiple MIG instances simultaneously. The scheduler must allocate all instances atomically.
3. **Fragmentation:** MIG instances cannot overlap. A GPU with 3 × 1g.5gb instances cannot also host a 3g.20gb instance. The scheduler must manage MIG "inventory" carefully.
4. **Live reconfiguration:** MIG profiles can only be changed when no instances are active. The scheduler must drain a GPU before re-partitioning it.

### 3.1.2 GPU Sharing: Time-Slicing vs MPS vs MIG

| Approach | Isolation | Performance | Overhead | Complexity |
|----------|-----------|-------------|----------|------------|
| Time-slicing | Fair (temporal) | Variable (context switch) | High (10-30%) | Low |
| MPS (Multi-Process Service) | Weak (shared memory) | Good (concurrent execution) | Low (5-10%) | Medium |
| MIG | Strong (hardware) | Predictable (dedicated SMs) | None (HW isolated) | High |
| vGPU (NVIDIA) | Strong (hypervisor) | Good (mediated) | Medium | High |

**Time-Slicing:**

NVIDIA's default GPU sharing for Kubernetes. The GPU driver multiplexes between processes in round-robin fashion, giving each a time quantum. Each process sees the full GPU memory (no isolation). Context switches are expensive (~10-30% throughput degradation for 2 processes).

**MPS:**

NVIDIA's Multi-Process Service allows multiple CUDA processes to share a single GPU context. Clients submit work to a shared server that forwards it to the GPU. Benefits: concurrent execution (no context switching), lower overhead. Drawbacks: no memory isolation (one process can corrupt another's memory), no fault isolation (one process crash can crash the server).

**MIG:**

The gold standard for GPU sharing. Each instance has dedicated SMs and memory. Full isolation, predictable performance. Limited to A100, H100, and newer datacenter GPUs. Not available on consumer GPUs (RTX 4080, etc.).

**Helix GPU Sharing Strategy:**

```
IF GPU supports MIG (A100, H100):
    Use MIG for multi-tenant GPU sharing
    Scheduler maps MIG profiles to workload requirements
ELSE IF GPU supports MPS (datacenter GPUs):
    Use MPS for same-node multi-process sharing
    With memory isolation via CUDA MPS server per session
ELSE:
    Use time-slicing as fallback
    With fair-share scheduling and per-process memory limits
```

### 3.1.3 Kubernetes Device Plugins and GRES Descriptors

Kubernetes uses the Device Plugin framework to advertise and allocate extended resources (GPUs, NICs, FPGAs). A device plugin:

1. Registers with the kubelet via gRPC.
2. Reports available devices (e.g., "gpu-vendor-nvidia-model-A100-0").
3. Responds to Allocate requests (returns device paths, mount points, env vars).

**NVIDIA Device Plugin:**

```yaml
# nvidia-device-plugin ConfigMap
apiVersion: v1
kind: ConfigMap
metadata:
  name: nvidia-device-plugin-config
data:
  config.yaml: |
    version: v1
    flags:
      migStrategy: mixed
      failOnInitError: true
    resources:
      - name: nvidia.com/gpu
        rename: nvidia.com/A100
      - name: nvidia.com/mig-1g.5gb
        rename: nvidia.com/mig-1g-5gb
```

**GRES (Generic Resource Scheduling):**

Originated in SLURM, GRES allows arbitrary resources to be tracked and scheduled. A GRES is defined by:

- Name (e.g., "gpu", "license:matlab")
- Count (e.g., 2)
- Type (e.g., "tesla", "v100")
- File specification (e.g., "/dev/nvidia0")

Helix uses a similar concept in its scheduler but extends it with:
- GPU API compatibility (CUDA, ROCm, Metal)
- MIG profile awareness
- Marketplace pricing metadata

### 3.1.4 SLURM GPU Scheduling with Gang Scheduling

SLURM's approach to GPU scheduling combines GRES tracking with gang scheduling for parallel jobs:

```bash
# Submit a job requiring 4 GPUs across 2 nodes (gang scheduled)
sbatch --gres=gpu:2 --nodes=2 --ntasks-per-node=1 my_gpu_job.sh

# Submit a job requiring a specific MIG instance
sbatch --gres=gpu:1g.5gb:2 my_mig_job.sh
```

**Gang Scheduling in SLURM:**

When a job requires resources on multiple nodes, SLURM's backfill scheduler ensures that all required resources are available simultaneously before launching the job. If any node's resources are occupied, the job waits. This prevents partial allocation where some tasks start while others can't.

### 3.1.5 Multi-Marketplace Economics

Helix operates in a multi-marketplace environment where GPU nodes can serve workloads from different sources:

**Marketplace Comparison:**

| Marketplace | Pricing Model | GPU Types | Payment | SLA |
|-------------|---------------|-----------|---------|-----|
| Chutes (Subnet) | Per-inference | A100, H100 | Crypto (TAO) | Best-effort |
| io.net | Per-GPU-hour | Mixed | USD/Crypto | 99.5% |
| Akash | Per-GPU-hour (bid) | Mixed | AKT/USD | Best-effort |
| RunPod | Per-GPU-hour | A100, H100, RTX | USD | 99.9% |
| Vast.ai | Per-GPU-hour (bid) | Mixed | USD | Best-effort |

**Helix Dual-Revenue Model:**

Helix GPU nodes operate in dual-revenue mode:

1. **HLX Internal:** Helix Cluster OS workloads (sessions, builds, training). Priced at internal cost (near-zero for owned hardware).
2. **TAO External:** Subnet/Chutes inference workloads. Priced at market rate, earning TAO rewards.

The scheduler optimizes for total revenue:
- When HLX demand is low, fill capacity with TAO workloads.
- When HLX demand is high, preempt TAO workloads (with grace period).
- Priority: P0 HLX > P0 TAO > P1 HLX > P1 TAO > ...

```go
// Helix dual-revenue scheduling priority
func revenuePriority(workload Workload) int {
    basePriority := workload.Priority // 0-100

    if workload.Source == HLX {
        // Internal workloads get a boost
        return basePriority + 50
    }

    // TAO workloads: prioritize by revenue
    revenuePerGPUHour := workload.BidPrice / workload.GPUHours
    return basePriority + int(revenuePerGPUHour * 10)
}
```

---

# Domain 4: WireGuard Mesh VPN

## 4.1 Technical Background

### 4.1.1 WireGuard Protocol Overview

WireGuard is a modern VPN protocol designed by Jason Donenfeld, emphasizing simplicity, performance, and cryptographic correctness. It consists of approximately 4,000 lines of kernel code (compared to 100,000+ for IPsec and 400,000+ for OpenVPN).

**Key Properties:**

- **Cryptokey routing:** Packets are routed based on cryptographic identity (public key), not IP addresses. Each peer has a list of allowed IPs; packets from that peer must have a source IP in the allowed-IPs list.
- **Noise protocol framework:** WireGuard uses the Noise_IKpsk2 pattern (Immediate initiation, Known static key, pre-shared key). This provides:
  - Mutual authentication (both sides know each other's static public key)
  - Forward secrecy (ephemeral Diffie-Hellman per session)
  - Replay protection (nonce-based, TAI64N timestamps)
- **UDP-only:** WireGuard exclusively uses UDP. There are no TCP fallback or ICMP modes.
- **Session establishment:** 1-RTT handshake. After the initial handshake, data flows immediately.
- **Rekeying:** Sessions rekey every 2-3 minutes (120 seconds for data, 180 seconds for handshake). Old keys are securely zeroed.

**WireGuard Packet Format:**

```
┌─────────────────────────────────────────┐
│ 4-byte: Message Type (1=data, 2,3=handshake) │
├─────────────────────────────────────────┤
│ 4-byte: Receiver Index                  │
├─────────────────────────────────────────┤
│ 8-byte: Nonce / Counter                 │
├─────────────────────────────────────────┤
│ N bytes: Encrypted payload (AEAD)       │
└─────────────────────────────────────────┘
```

### 4.1.2 Full Mesh vs Hub-Spoke Topologies

**Full Mesh:**

Every node connects to every other node. O(n²) connections. Benefits: lowest latency (direct path), no single point of failure, no bandwidth bottleneck at a hub. Drawbacks: doesn't scale past ~1000 nodes due to connection count and key distribution complexity.

**Hub-Spoke:**

All nodes connect through a central relay. O(n) connections. Benefits: simple key management, works at any scale. Drawbacks: hub is a single point of failure and bandwidth bottleneck; latency is 2× (client→hub→client).

**Helix's Approach: Adaptive Mesh**

Helix uses a full-mesh topology for clusters up to ~100 nodes, with automatic relay fallback for larger clusters or nodes behind symmetric NAT:

```
if cluster_size <= 100:
    topology = FULL_MESH  # every node peers with every other
else:
    topology = HYBRID     # regional hubs with mesh within regions
    if node.behind_symmetric_nat:
        use_relay = true  # relay through a hub node
```

### 4.1.3 NAT Traversal: STUN, TURN, Hole Punching

**NAT Types:**

| Type | Description | Traversal Method |
|------|-------------|-----------------|
| Full Cone | Any external host can reach mapped port | Easy (STUN) |
| Restricted Cone | Only hosts that received packets from internal host | Moderate (STUN + ping) |
| Port-Restricted Cone | Like restricted but port-specific | Moderate (STUN + ping) |
| Symmetric | Different mapping per destination | Hard (TURN relay) |

**STUN (Session Traversal Utilities for NAT):**

STUN helps a node discover its public IP:port mapping. The node sends a STUN request to a STUN server; the server responds with the source IP:port it sees. This tells the node its external address.

```go
// Helix STUN client (pkg/wireguard/stun.go)
func DiscoverPublicAddr(stunServer string) (net.UDPAddr, error) {
    conn, _ := net.DialUDP("udp4", nil, &net.UDPAddr{
        IP:   net.ParseIP(stunServer),
        Port: 3478,
    })
    // Send STUN Binding Request
    // Parse XOR-MAPPED-ADDRESS from response
    return publicAddr, nil
}
```

**UDP Hole Punching:**

Two nodes behind NAT can establish a direct connection by simultaneously sending UDP packets to each other's public address. The NAT on each side creates a mapping when the packet goes out; the incoming packet from the peer matches this mapping and is forwarded.

```
Node A (behind NAT_A)          STUN Server          Node B (behind NAT_B)
       |                            |                         |
       |--- STUN Request ---------->|                         |
       |<-- Public IP_A:PortA ------|                         |
       |                            |<-------- STUN Request --|
       |                            |--------- Public IP_B:PortB
       |                            |                         |
       |--- UDP to IP_B:PortB ----->| (via internet) -------->|
       |<-- UDP from IP_B:PortB ----| (hole punched!) --------|
```

**TURN (Traversal Using Relays around NAT):**

When hole punching fails (symmetric NAT), traffic must be relayed through a TURN server. This adds latency and bandwidth costs but guarantees connectivity.

**Helix NAT Traversal Strategy (pkg/wireguard/nat_traversal.go):**

1. Try STUN-based public address discovery.
2. Try UDP hole punching with discovered addresses.
3. If hole punching fails, fall back to relay through a hub node.
4. Periodically retry hole punching (NAT mappings may change).

### 4.1.4 Key Rotation Strategies

WireGuard uses long-term static keys for peer authentication and ephemeral keys for session encryption. Static key rotation requires reconfiguring all peers.

**Time-Based Rotation:**

Rotate keys on a fixed schedule (e.g., every 24 hours). Simple to implement but may rotate unnecessarily if keys haven't been compromised.

```go
// pkg/wireguard/keyrotation.go
func (m *KeyRotationManager) ShouldRotate(lastRotation time.Time) bool {
    return time.Since(lastRotation) > m.rotationInterval
}
```

**Usage-Based Rotation:**

Rotate keys after a certain amount of traffic has been encrypted. Reduces unnecessary rotations but adds complexity.

**Compromise-Triggered Rotation:**

Rotate keys immediately when a compromise is detected (e.g., via SPIRE SVID revocation, or a leaked key detected in a breach database).

**Helix Key Rotation (pkg/wireguard/keyrotation.go):**

Helix uses a hybrid approach:
- **Default:** Time-based rotation every 24 hours.
- **Accelerated:** Usage-based rotation after 100 GB of traffic.
- **Emergency:** Compromise-triggered rotation via SPIRE SVID revocation events.

Key rotation is coordinated via etcd:
```
1. Node generates new keypair
2. Node publishes new public key to /clusteros/security/wireguard/peers/{node_id}
3. All peers watch this key and update their WireGuard configuration
4. Old key is revoked after a grace period (5 minutes)
```

### 4.1.5 Performance Benchmarks

| Protocol | Throughput (Gbps) | Latency (ms) | CPU Usage | Handshake (ms) |
|----------|-------------------|---------------|-----------|----------------|
| WireGuard | 4.1 | 0.3 | 2% | 0.5 |
| IPsec (IKEv2) | 2.8 | 0.5 | 8% | 45 |
| OpenVPN | 0.9 | 1.2 | 25% | 250 |
| IPSec (ESP) | 3.5 | 0.4 | 6% | 30 |

WireGuard consistently outperforms alternatives due to:
- Modern cryptography (ChaCha20-Purvepipe vs AES-GCM for non-AES-NI hardware)
- Kernel implementation (no userspace context switches)
- Minimal packet overhead (60 bytes vs 80+ for IPsec)

### 4.1.6 gVisor Netstack Userspace WireGuard

For platforms where kernel WireGuard is unavailable (macOS, containers without NET_ADMIN, unprivileged users), Helix uses gVisor's userspace network stack to run WireGuard entirely in userspace.

**How It Works:**

```go
// pkg/wireguard/netstack_darwin.go
func CreateUserspaceTunnel(localIP net.IP, peers []PeerConfig) (net.Listener, error) {
    // Create a TUN device via gVisor netstack (no root required)
    tun, _ := netstack.CreateNetTUN(
        []net.IP{localIP},
        []net.IP{{8, 8, 8, 8}}, // DNS
        1420, // MTU
    )

    // Create WireGuard device using wireguard-go
    device := device.NewDevice(tun, conn.NewDefaultBind(), logger)

    // Configure peers
    for _, peer := range peers {
        device.IpcSetOperation(fmt.Sprintf(
            "public_key=%s\nallowed_ip=%s/32\nendpoint=%s\n",
            peer.PublicKey, peer.AllowedIP, peer.Endpoint,
        ))
    }

    // Create TCP listener on the userspace stack
    ln, _ := tun.CreateListener(":0")
    return ln, nil
}
```

Helix's `pkg/wireguard/netstack_darwin.go` implements this for macOS development, enabling the full mesh VPN without kernel support or root privileges.

### 4.1.7 Helix-Specific: SWIM-Gossip-Discovered Peers, ML-KEM-768 PSKs

**Peer Discovery via SWIM:**

Instead of manually configuring WireGuard peers, Helix uses SWIM gossip to automatically discover and configure peers:

1. When a node joins the cluster, it announces its WireGuard public key and endpoint via SWIM metadata.
2. All existing nodes receive the new peer's information via gossip dissemination.
3. Each node automatically configures a WireGuard peer for the new node.
4. When a node leaves the cluster, its WireGuard peer configuration is removed.

**ML-KEM-768 PSK Exchange:**

WireGuard supports a pre-shared key (PSK) in addition to the Noise protocol's asymmetric keys. Helix uses ML-KEM-768 (post-quantum KEM) to establish PSKs:

1. Two nodes perform an ML-KEM-768 key encapsulation over the gRPC control channel (protected by SPIFFE mTLS).
2. The resulting shared secret becomes the WireGuard PSK.
3. The PSK provides post-quantum confidentiality even if the Noise protocol's Curve25519 ECDH is broken by a quantum computer.

---

# Domain 5: SPIFFE/SPIRE Identity Framework

## 5.1 Technical Background

### 5.1.1 SPIFFE Specification

SPIFFE (Secure Production Identity Framework for Everyone) is a set of open-source standards for identifying and securing software services. It defines:

1. **SPIFFE ID:** A URI-formatted identifier (e.g., `spiffe://helix.cluster/nodes/alpha`). Similar to a principal name in Kerberos but globally unique and hierarchical.

2. **SVID (SPIFFE Verifiable Identity Document):** A cryptographic document that binds a SPIFFE ID to a public key. Two SVID types:
   - **X.509-SVID:** An X.509 certificate with the SPIFFE ID in the Subject Alternative Name (SAN) URI field. Standard TLS libraries can verify it.
   - **JWT-SVID:** A JSON Web Token with the SPIFFE ID in the `sub` claim. Used for service-to-service authentication where TLS is not available (e.g., pub/sub messages, API tokens).

3. **Trust Bundle:** A collection of public keys (root certificates for X.509-SVIDs, JWK set for JWT-SVIDs) that establishes the trust root for a SPIFFE trust domain. All members of a trust domain share the same trust bundle.

4. **Workload API:** A local gRPC/Unix socket API that provides SVIDs and trust bundles to workloads. Workloads don't need to know how to authenticate to the SPIRE server — they just call the Workload API.

### 5.1.2 SPIRE Architecture

SPIRE (SPIFFE Runtime Environment) is the reference implementation of SPIFFE. It consists of:

**SPIRE Server:**

- Manages the trust bundle (root CA key).
- Signs SVIDs for registered workloads.
- Stores registration entries (workload selector → SPIFFE ID mappings).
- Supports multiple upstream authorities (AWS PCA, Vault, self-signed).
- Provides federation APIs for cross-trust-domain trust.

**SPIRE Agent:**

- Runs on every node.
- Authenticates to the server using a node attestation mechanism (e.g., AWS IID, Kubernetes ServiceAccount, X.509 node SVID).
- Caches SVIDs and trust bundles for local workloads.
- Exposes the Workload API on a Unix socket.
- Selectors: `unix:uid:1000`, `k8s:ns:default`, `k8s:sa:my-service`.

**Registration Entries:**

```hcl
# SPIRE registration entry for a Helix node agent
entry {
    spiffe_id = "spiffe://helix.cluster/nodes/alpha"
    parent_id = "spiffe://helix.cluster/nodes"
    selectors = [
        "unix:uid:1000",
        "unix:path:/opt/helix/agent",
    ]
}

# SPIRE registration entry for a session
entry {
    spiffe_id = "spiffe://helix.cluster/sessions/dev-shell"
    parent_id = "spiffe://helix.cluster/nodes/alpha"
    selectors = [
        "unix:uid:1001",
    ]
    ttl = 3600  # 1 hour
}
```

### 5.1.3 SVID Types and Usage in Helix

**X.509-SVID (Primary):**

Used for all gRPC communication between Helix services. Each service presents its X.509-SVID during the TLS handshake, providing mutual TLS (mTLS) without manual certificate management.

```
Node Agent ←→ Session Manager    [mTLS with X.509-SVIDs]
Session Manager ←→ Scheduler     [mTLS with X.509-SVIDs]
Scheduler ←→ etcd                [mTLS with X.509-SVIDs]
API Gateway ←→ Client            [mTLS with X.509-SVID (client cert optional)]
```

**JWT-SVID (Supplementary):**

Used for authentication in contexts where TLS is not available or practical:
- Redis pub/sub message authentication
- Audit log actor identification
- API tokens for external integrations

### 5.1.4 Trust Bundle Distribution

The trust bundle must be distributed to all nodes and services. SPIRE handles this automatically:

1. The SPIRE server publishes the trust bundle via its bundle endpoint.
2. SPIRE agents fetch and cache the trust bundle.
3. Agents serve the trust bundle via the Workload API.
4. When the root CA rotates, agents automatically update their cached trust bundles.

For federation (cross-trust-domain), SPIRE servers exchange trust bundles:
1. Server A publishes its bundle at a well-known endpoint.
2. Server B configures a federation relationship with Server A's endpoint.
3. Server B periodically fetches Server A's bundle and makes it available to its agents.
4. Workloads in trust domain B can verify SVIDs from trust domain A.

### 5.1.5 ML-KEM-768 Post-Quantum TLS Extension

Helix extends the standard TLS 1.3 handshake with ML-KEM-768 (Kyber-768) key encapsulation for post-quantum confidentiality:

**Standard TLS 1.3 Key Exchange:**

```
Client → Server: ClientHello (key_share: X25519)
Server → Client: ServerHello (key_share: X25519)
                   [Shared secret computed via X25519 ECDH]
```

**Helix Hybrid PQ Key Exchange (pkg/hybridkex/):**

```
Client → Server: ClientHello (key_share: X25519 + ML-KEM-768 encapsulation)
Server → Client: ServerHello (key_share: X25519 + ML-KEM-768 ciphertext)
                   [Shared secret = SHA256(X25519_secret || ML-KEM-768_secret)]
```

This hybrid approach provides:
1. **Classical security:** If PQ is broken, X25519 still provides security.
2. **Post-quantum security:** If X25519 is broken by a quantum computer, ML-KEM-768 still provides security.
3. **NIST standard:** ML-KEM-768 is FIPS 203 (2024).

### 5.1.6 SPIFFE as Helix's Universal Identity Backbone

In Helix, SPIFFE is the single identity mechanism used everywhere:

- **Node identity:** `spiffe://helix.cluster/nodes/{hostname}` — used for WireGuard mesh authentication, gRPC mTLS, etcd authentication.
- **User identity:** `spiffe://helix.cluster/users/{username}` — used for API authentication, audit logging, RBAC.
- **Session identity:** `spiffe://helix.cluster/sessions/{session_id}` — used for session-specific permissions, GPU access control.
- **Service identity:** `spiffe://helix.cluster/services/{service_name}` — used for service-to-service mTLS.
- **Build identity:** `spiffe://helix.cluster/builds/{job_id}` — used for build artifact signing, SBOM attestation.

This uniform identity model simplifies:
- Access control: One policy framework (OPA/Rego) for all identity types.
- Audit logging: Every action has a SPIFFE ID actor.
- Certificate management: One rotation mechanism (SPIRE) for all certificates.
- Federation: Cross-cluster trust via SPIFFE federation.

---

# Domain 6: SWIM Gossip Protocol

## 6.1 Technical Background

### 6.1.1 SWIM Basics

SWIM (Scalable Weakly-consistent Infection-style Membership) is a membership protocol for distributed systems, designed by Das, Gupta, and Motivala at Cornell (2002). It provides eventually consistent membership with O(1) bandwidth per member per round.

**Protocol Mechanics:**

SWIM operates in rounds. In each round, member M picks a random target T and:

1. **Ping:** M sends a ping to T.
2. **Ack:** If T responds, T is confirmed alive.
3. **Indirect probe (on ping failure):** M asks K random members to ping T on its behalf. If any reports T alive, T is confirmed alive (the direct path was broken but T is reachable indirectly).
4. **Suspicion:** If neither direct nor indirect probe succeeds, M marks T as "suspect." Suspect members are not immediately declared dead.
5. **Dissemination:** Membership changes (alive, suspect, dead) are piggybacked on protocol messages (ping, ack, indirect probe). This is the "infection-style" part — news spreads like a gossip epidemic.

**Why Suspicion?**

Without suspicion, a single lost ping could cause a false positive (declared dead but actually alive). Suspect members get a timeout period (e.g., 30 seconds). If another member pings the suspect and gets a response, the suspicion is refuted. Only after the timeout expires with no refutation is the member declared dead.

### 6.1.2 Lifeguard Extensions

Lifeguard (2018, HashiCorp) extends SWIM with three mechanisms that reduce false positives by up to 50×:

1. **Local Health Aware Probing (LHA-Probe):** A node that has been recently suspected by others should probe more aggressively (shorter probe interval). A healthy node should probe less aggressively. This ensures that slow nodes are given more chances to respond before being declared dead.

2. **Local Health Aware Suspect (LHA-Suspect):** When a node suspects another, it includes its own health score in the suspicion message. A node with poor health (recently suspected itself) should have its suspicions discounted. A healthy node's suspicion carries more weight.

3. **Local Health Aware Refute (LHA-Refute):** A suspected node immediately broadcasts a "refutation" message instead of waiting for the next probe cycle. The refutation includes the node's health score, so other nodes can weight the refutation accordingly.

**Impact on False Positives:**

| Scenario | Standard SWIM | SWIM + Lifeguard |
|----------|---------------|------------------|
| Temporary network slowdown | ~5% FP rate | ~0.1% FP rate |
| CPU starvation (GC pause) | ~10% FP rate | ~0.2% FP rate |
| Packet loss (10%) | ~3% FP rate | ~0.06% FP rate |

### 6.1.3 SWIM v2 with Piggybacked Metadata

SWIM v2 extends the basic protocol with metadata piggybacking. Instead of just carrying membership state, protocol messages carry arbitrary metadata:

- **Node resource information:** CPU, memory, GPU count, GPU type.
- **WireGuard endpoints:** Public key, endpoint address, allowed IPs.
- **SPIFFE IDs:** Node identity for mTLS.
- **Custom labels:** Arbitrary key-value pairs for scheduling.

This eliminates the need for a separate metadata dissemination mechanism — the SWIM protocol itself serves as the transport for all cluster metadata.

**Helix SWIM Metadata Format:**

```go
type NodeMetadata struct {
    // Identity
    SPIFFEID string `json:"spiffe_id"`
    WGPubKey  string `json:"wg_pubkey"`

    // Resources
    CPUCores    int   `json:"cpu_cores"`
    CPUThreads  int   `json:"cpu_threads"`
    MemoryBytes int64 `json:"memory_bytes"`
    GPUCount    int   `json:"gpu_count"`
    GPUVendor   string `json:"gpu_vendor"`  // NVIDIA, AMD, etc.
    GPUModel    string `json:"gpu_model"`

    // Network
    WGEndpoint  string `json:"wg_endpoint"`   // e.g., "1.2.3.4:51820"
    Region      string `json:"region"`

    // Health
    HealthScore int   `json:"health_score"`

    // Labels
    Labels map[string]string `json:"labels"`
}
```

### 6.1.4 Production Deployments

**HashiCorp Serf:**

Serf is HashiCorp's implementation of SWIM (with Lifeguard extensions). It's used in:
- Consul (service discovery and health checking)
- Nomad (cluster membership)
- Vault (HA cluster)
- Terraform (remote state coordination)

**memberlist:**

memberlist is Go's SWIM implementation (used by Consul, Cortex, Thanos). Key features:
- Push-pull state synchronization (periodic full state exchange)
- Gossip protocol for broadcast messages
- Dead node reaping
- Custom event delegates

**Helix's Implementation (pkg/swim/):**

Helix implements SWIM with Lifeguard extensions and metadata piggybacking:

```go
// pkg/swim/swim.go
type SWIM struct {
    config     *Config
    members    *Membership
    transport  Transport
    metadata   *MetadataStore

    // Lifeguard
    localHealth *LocalHealthScore

    // Probe state
    probeIndex int
    probeSeq   uint32
}

func (s *SWIM) probeOne() {
    target := s.members.GetMember(s.probeIndex)
    s.probeIndex = (s.probeIndex + 1) % s.members.Size()

    // Ping with metadata piggyback
    meta := s.metadata.GetLocal()
    ping := &Ping{
        Seq:      s.probeSeq,
        Metadata: meta,
    }

    ack, err := s.transport.Ping(target.Addr, ping, s.config.ProbeTimeout)
    if err == nil {
        // Direct success
        s.members.ConfirmAlive(target.ID, ack.Metadata)
        return
    }

    // Indirect probe
    s.indirectProbe(target)
}
```

---

# Domain 7: Chaos Engineering

## 7.1 Technical Background

### 7.1.1 Chaos Engineering Principles

Chaos Engineering is the discipline of experimenting on a system to build confidence in its capability to withstand turbulent conditions. The core principles (from priciplesofchaos.org):

1. **Build a hypothesis** about steady-state behavior.
2. **Vary real-world events** (not just theoretical failures).
3. **Run experiments in production** (or as close to production as possible).
4. **Automate experiments** to run continuously.
5. **Minimize blast radius** (start small, expand gradually).

### 7.1.2 25+ Fault Types for Helix

**Network Faults (8 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| Network partition | Split cluster into two halves | Split-brain, scheduling conflicts |
| Packet loss | Drop X% of packets between nodes | Increased latency, timeouts |
| Network latency | Add Xms latency between nodes | Slow scheduling, stale heartbeats |
| Network jitter | Variable latency (0–Xms) | Unpredictable behavior |
| DNS failure | DNS resolution fails | Service discovery failures |
| Bandwidth throttle | Limit bandwidth between nodes | Slow replication, large transfers |
| Port exhaustion | Exhaust ephemeral ports | Connection failures |
| Connection reset | RST packets on established connections | Abrupt disconnections |

**Compute Faults (5 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| CPU throttle | Limit CPU to X% | Slow processing, missed heartbeats |
| CPU spike | Sudden CPU usage to 100% | Scheduling delays, GC pauses |
| OOM kill | Kill process on OOM | Service restart, data loss |
| Process kill | Kill a specific process | Service downtime |
| Kernel panic | Crash the OS kernel | Node failure |

**Storage Faults (4 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| Disk I/O error | Return EIO on read/write | Database corruption, data loss |
| Disk latency | Add Xms to every I/O operation | Slow queries, timeout cascades |
| Disk full | Fill disk to 100% | Write failures, log truncation |
| File corruption | Corrupt random bytes in files | Checksum failures, data inconsistency |

**Time Faults (3 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| Clock skew | Offset clock by X seconds | Lease expiry, ordering violations |
| Clock stop | Freeze system clock | Heartbeat timeout, scheduling stall |
| Leap second | Insert or remove a second | Time-based logic errors |

**GPU Faults (3 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| GPU hang | GPU becomes unresponsive | Training job stall, CUDA errors |
| GPU memory error | ECC errors on GPU memory | Computation errors, job failure |
| GPU driver crash | Driver process crashes | All GPU workloads fail |

**Identity Faults (2 types):**

| Fault | Description | Impact |
|-------|-------------|--------|
| Certificate expiry | Expire a service certificate | mTLS failure, service isolation |
| SPIRE server down | SPIRE server unreachable | Certificate renewal failure |

### 7.1.3 Chaos Tools Comparison

| Tool | Type | Fault Injection | Orchestration | Integration |
|------|------|-----------------|---------------|-------------|
| Chaos Monkey | Random pod kill | Limited (kill only) | Simple | Kubernetes |
| Litmus | Full suite | Network, compute, storage, GPU | Rich | Kubernetes |
| Gremlin | Enterprise | All fault types | Enterprise features | Kubernetes, Linux |
| Chaos Mesh | Open source | Network, I/O, time, stress | Workflow-based | Kubernetes |
| Helix chaosexp | Custom | 5 (expanding to 25+) | DST-integrated | Go native |

### 7.1.4 GameDay Exercise Template

**Helix GPU Cluster GameDay:**

1. **Objective:** Validate that the scheduler correctly handles GPU node failures and migrates sessions.
2. **Hypothesis:** When a GPU node fails, sessions on that node are migrated within 60 seconds, GPU allocations are released within 10 seconds, and no GPU is double-allocated.
3. **Steady-state:** 4 GPU nodes, 10 running sessions, 8 active GPU allocations.
4. **Experiment:**
   a. Kill node-beta (holds 3 sessions, 2 GPU allocations).
   b. Verify sessions enter MIGRATING status within 30 seconds.
   c. Verify GPU allocations are released within 10 seconds.
   d. Verify sessions are re-scheduled on remaining nodes within 60 seconds.
   e. Verify no GPU double-allocation during recovery.
5. **Blast radius:** One node; remaining 3 nodes unaffected.
6. **Rollback:** Restart node-beta agent if recovery fails.
7. **Success criteria:** All verifications pass.

### 7.1.5 Helix-Specific: pkg/chaosexp 5 Faults → 25+ Expansion Plan

**Current Faults (5):**

1. `NetworkPartition` — simulate network partition between specified nodes
2. `NodeCrash` — simulate node crash (stop agent)
3. `DiskError` — inject disk I/O errors
4. `ClockSkew` — offset system clock
5. `ProcessKill` — kill a specific process

**Expansion Plan (20 additional faults):**

Phase 1 (Network): PacketLoss, NetworkLatency, NetworkJitter, DNSFailure, BandwidthThrottle
Phase 2 (Compute): CPUThrottle, CPUSpike, OOMKill, KernelPanic
Phase 3 (Storage): DiskLatency, DiskFull, FileCorruption
Phase 4 (GPU): GPUHang, GPUMemoryError, GPUDriverCrash
Phase 5 (Identity): CertExpiry, SPIREServerDown, TokenRevocation
Phase 6 (Time): ClockStop, LeapSecond

**Integration with DST:**

Each chaos fault is also available as a BUGGIFY hook in the deterministic simulation:

```go
// pkg/chaosexp/gpu_hang.go
func GPUHang(gpuID string) error {
    if BUGGIFY("gpu_hang") {
        return simulateGPUHang(gpuID)
    }
    return injectGPUHang(gpuID) // real fault injection in production
}
```

---

# Domain 8: Post-Quantum Cryptography

## 8.1 Technical Background

### 8.1.1 NIST PQC Standards

In August 2024, NIST published three post-quantum cryptography standards:

- **FIPS 203 (ML-KEM):** Module-Lattice-Based Key-Encapsulation Mechanism. Based on Kyber. Three parameter sets: ML-KEM-512, ML-KEM-768, ML-KEM-1024. ML-KEM-768 provides ~192-bit classical security and ~128-bit quantum security.

- **FIPS 204 (ML-DSA):** Module-Lattice-Based Digital Signature Algorithm. Based on Dilithium. Three parameter sets: ML-DSA-44, ML-DSA-65, ML-DSA-87. ML-DSA-65 provides ~128-bit quantum security.

- **FIPS 205 (SLH-DSA):** Stateless Hash-Based Digital Signature Algorithm. Based on SPHINCS+. Provides hash-based signatures with minimal security assumptions.

### 8.1.2 Hybrid Key Exchange: X25519 + ML-KEM-768 for TLS 1.3

The recommended migration path is hybrid key exchange — combining classical and post-quantum algorithms so that security is maintained even if one is broken.

**Helix's pkg/hybridkex Implementation:**

```go
// pkg/hybridkex/hybridkex.go
type HybridKEX struct {
    classical *ecdh.Curve    // X25519
    pq        *mlkem.MLKEM768 // ML-KEM-768
}

func (h *HybridKEX) ClientHello() ([]byte, error) {
    // Generate X25519 ephemeral keypair
    x25519Pub, x25519Priv, _ := ecdh.X25519.GenerateKey(rand.Reader)

    // Encapsulate with ML-KEM-768 using server's public key
    mlkemCT, mlkemSS, _ := h.pq.Encapsulate(serverMLKEMPub)

    // Combined shared secret
    x25519SS, _ := x25519Priv.ECDH(serverX25519Pub)
    combinedSS := sha256.Sum256(append(x25519SS, mlkemSS...))

    // ClientHello payload: X25519_pub || ML-KEM_ciphertext
    payload := append(x25519Pub, mlkemCT...)
    return payload, nil
}
```

### 8.1.3 WireGuard PSK Exchange with ML-KEM-768

WireGuard's pre-shared key (PSK) is normally configured manually. Helix automates PSK exchange using ML-KEM-768:

1. Node A requests a PSK exchange with Node B via the gRPC control channel (protected by SPIFFE mTLS).
2. Node B generates an ML-KEM-768 keypair and returns the public key.
3. Node A encapsulates a shared secret using ML-KEM-768 and sends the ciphertext to Node B.
4. Both nodes derive the WireGuard PSK from the ML-KEM-768 shared secret.
5. The PSK is configured on the WireGuard interface for the corresponding peer.

### 8.1.4 Migration Strategy

**Three-Phase Migration:**

1. **Phase 1 (Current):** Classical + PQ hybrid. All new connections use X25519 + ML-KEM-768. Existing connections continue with X25519 only. This provides PQ confidentiality for new sessions.

2. **Phase 2 (2027):** PQ-preferred. New connections default to ML-KEM-768 + X25519. Classical-only connections are deprecated. WireGuard PSKs are always PQ-exchanged.

3. **Phase 3 (2029+):** Pure PQ. If NIST confirms no practical attacks on ML-KEM-768, remove X25519 fallback. Use ML-KEM-1024 for higher security. Use ML-DSA-65 for all digital signatures (replacing Ed25519).

### 8.1.5 Helix-Specific: pkg/hybridkex and e2ee-proxy

**e2ee-proxy (cmd/e2ee-proxy):**

The e2ee-proxy is a sidecar that provides end-to-end encryption for all gRPC communication:

```
Client → e2ee-proxy (encrypt with ML-KEM-768 + X25519) → Network → e2ee-proxy (decrypt) → Server
```

This ensures that even if the underlying network is compromised (including TLS termination at load balancers), the payload remains encrypted with post-quantum security.

---

# Domain 9: Heterogeneous Compute Management

## 9.1 Technical Background

### 9.1.1 ARM SBC Clusters

Single-board computers (SBCs) provide low-power, low-cost compute nodes for edge deployments.

**Raspberry Pi 5:**
- Broadcom BCM2712, 4× Cortex-A76 @ 2.4 GHz
- 4–8 GB LPDDR4X
- PCIe 2.0 x1 (for NVMe or GPU)
- 802.11ac WiFi, GbE
- Power: 5V/5A (25W peak)

**Orange Pi 5:**
- Rockchip RK3588S, 4× Cortex-A76 + 4× Cortex-A55
- 4–32 GB LPDDR4x
- Mali-G610 GPU
- PCIe 3.0 x4 (for NVMe or GPU)
- GbE, 802.11ac
- Power: 5V/4A (20W peak)

**Turing Pi 2:**
- 4× NVIDIA Jetson/NVIDIA SoM slots
- BMC management controller
- 1 GbE per node + 10 GbE uplink
- Designed for edge AI inference

**Helix on SBC:**

Helix supports ARM SBCs as edge nodes (role=EDGE). The agent binary cross-compiles for arm64 and runs with reduced resource requirements:
- No GPU orchestration (unless PCIe GPU attached)
- Reduced health snapshot frequency (60s instead of 30s)
- SWIM gossip with longer probe intervals (10s instead of 5s)

### 9.1.2 Console Compute (PS4/PS5 Linux)

Helix supports PlayStation consoles as compute nodes via Linux installation, providing high-performance compute at extremely low cost per FLOP.

**PS4 (CUH-1000/CUH-2000):**
- 8× AMD Jaguar @ 1.6 GHz
- 8 GB GDDR5 (unified)
- 18 CU AMD GCN GPU (~1.84 TFLOP)
- FreeBSD-based Orbis OS → Linux via ps4-linux

**PS5 (CFI-1000/CFI-2000):**
- 8× AMD Zen 2 @ 3.5 GHz (variable frequency)
- 16 GB GDDR6
- 36 CU AMD RDNA 2 GPU (~10.28 TFLOP)
- FreeBSD-based Prospero OS → Linux via ps5-linux

**Jailbreak Landscape:**

| Firmware | Exploit | Status |
|----------|---------|--------|
| PS4 9.00 | GoldHEN | Stable |
| PS4 11.00 | PPPwn | Stable |
| PS5 4.03–4.50 | BD-J | Stable |
| PS5 5.50+ | N/A | No public exploit |

**Helix Console Support (internal/console/):**

The console detector identifies PlayStation hardware and configures the node appropriately:

```go
// internal/console/detector.go
func DetectConsole() (ConsoleType, error) {
    // Read /proc/device-tree/compatible
    // Read /proc/cpuinfo for SoC identification
    // Check for GameDisc presence (anti-piracy)
    // Verify Linux boot method (not game exploit)
    return consoleType, nil
}
```

**Important Boundary:** Per `docs/NODE_PROVISIONING_BOUNDARY.md`, Helix does NOT support jailbreak exploits. Console nodes must run Linux via an authorized boot path (OtherOS++, Linux boot, or Petitboot). The detector explicitly refuses to register nodes that appear to be running via game-level exploits.

### 9.1.3 FPGA Acceleration

Field-Programmable Gate Arrays provide reconfigurable hardware acceleration for specific workloads (encryption, signal processing, ML inference).

**Xilinx Vitis:**
- Development framework for Xilinx FPGAs (Alveo, Versal)
- HLS (High-Level Synthesis): C/C++ → HDL
- Vitis AI: ML inference acceleration on DPU

**Intel oneAPI:**
- Development framework for Intel FPGAs (Stratix, Agilex)
- DPC++ (Data Parallel C++): SYCL-based programming
- oneAPI AI Analytics Toolkit

**Helix FPGA Support:**

FPGA devices are tracked in the `gpu_devices` table with `vendor='OTHER'` and `api='Vulkan'` or `api='OpenCL'`. The scheduler treats FPGAs as specialized accelerators:

```go
func isFPGA(device GPUDevice) bool {
    return device.Vendor == "XILINX" || device.Vendor == "INTEL_FPGA"
}
```

### 9.1.4 Helix 8-Tier Device Taxonomy

Helix classifies compute devices into 8 tiers based on capability, cost, and reliability:

| Tier | Device | CPU | GPU | Memory | Power | Cost/hr | Use Case |
|------|--------|-----|-----|--------|-------|---------|----------|
| T0 | H100 cluster | 128+ cores | 8× H100 | 1TB+ | 10kW+ | $30+ | Training |
| T1 | A100 server | 64+ cores | 4× A100 | 512GB+ | 5kW+ | $15+ | Training/Inference |
| T2 | RTX workstation | 32+ cores | 1-2× RTX | 128GB+ | 1kW+ | $3+ | Inference/Build |
| T3 | Apple M-series | 12+ cores | M GPU | 32GB+ | 100W+ | $1+ | Dev/Build |
| T4 | ARM server | 64+ cores | N/A | 128GB+ | 200W+ | $0.50+ | Batch/Build |
| T5 | SBC cluster | 4-8 cores | Mali | 4-8GB | 20W+ | $0.05+ | Edge/IoT |
| T6 | Console (PS5) | 8 cores | RDNA2 | 16GB | 200W+ | $0.10+ | Inference/Build |
| T7 | FPGA | Variable | FPGA | Variable | Variable | Variable | Accel |

---

# Domain 10: Etcd Integration Patterns

## 10.1 Technical Background

### 10.1.1 etcd v3 API Overview

etcd v3 provides a gRPC API with the following operations:

- **Put:** Store a key-value pair. Optionally with a lease.
- **Get:** Retrieve a key or range of keys. Supports prefix, range, and limit queries.
- **Delete:** Remove a key or range of keys.
- **Txn:** Atomic transaction with compare-and-swap. Up to 128 operations per transaction.
- **Watch:** Observe changes to keys or prefixes. Returns events with type (PUT/DELETE) and key-value data.
- **Lease:** Create, revoke, and keep-alive leases. Keys attached to a lease are deleted when the lease expires.
- **Lock:** Distributed mutex based on leases. Fair FIFO ordering.
- **Election:** Leader election based on leases. Campaign, proclaim, resign.

### 10.1.2 Distributed Locking Best Practices

**Pattern 1: Simple Mutex:**

```go
session, _ := concurrency.NewSession(client, concurrency.WithTTL(30))
mutex := concurrency.NewMutex(session, "/clusteros/locks/scheduler/")
mutex.Lock(ctx)
defer mutex.Unlock(ctx)
// Critical section
```

**Pattern 2: Compare-and-Swap:**

```go
txn := client.Txn(ctx).
    If(clientv3.Compare(clientv3.ModRevision(key), "=", expectedRev)).
    Then(clientv3.OpPut(key, newValue))
resp, _ := txn.Commit()
if !resp.Succeeded {
    // Conflict: another writer modified the key
}
```

**Pattern 3: Lease-based Lock with Heartbeat:**

```go
lease, _ := client.Grant(ctx, 30) // 30-second TTL
client.Put(ctx, "/clusteros/locks/build-coordinator",
    myNodeID, clientv3.WithLease(lease.ID))

// Keep alive in background
keepAliveCh, _ := client.KeepAlive(ctx, lease.ID)

// If process crashes, lease expires and lock is released
```

### 10.1.3 Leader Election with etcd

```go
session, _ := concurrency.NewSession(client)
election := concurrency.NewElection(session, "/clusteros/leader/scheduler")

// Campaign for leadership
err := election.Campaign(ctx, myNodeID)
if err != nil {
    // Lost election; become follower
    return
}

// I am the leader
defer election.Resign(ctx)

// Watch for leadership loss
watchCh := client.Watch(ctx, "/clusteros/leader/scheduler",
    clientv3.WithPrefix())
for resp := range watchCh {
    for _, ev := range resp.Events {
        if ev.Type == clientv3.EventTypeDelete {
            // Lost leadership; re-campaign
        }
    }
}
```

### 10.1.4 Performance Tuning

**Compaction:**

etcd stores all historical revisions. Without compaction, the database grows unbounded. Compaction removes old revisions:

```bash
# Compact to current revision
REV=$(etcdctl endpoint status --write-out=json | jq '.[0].Status.header.revision')
etcdctl compact $REV

# Defragment to reclaim disk space
etcdctl defrag
```

**Quota:**

Default 2GB quota. For Helix, increase to 8GB:

```yaml
# etcd.yaml
storage:
  quota-backend-bytes: 8589934592  # 8 GB
```

**Auto-Compaction:**

```yaml
# Auto-compact every hour
auto-compaction-mode: periodic
auto-compaction-retention: 1h
```

### 10.1.5 Helix-Specific: etcd as Unified State Store

Helix uses etcd as the single source of truth for all cluster state that requires strong consistency:

- **Node registry:** `/clusteros/nodes/{id}` — node identity, status, resources
- **Session state:** `/clusteros/sessions/{id}` — session definition, routing, bindings
- **Scheduler state:** `/clusteros/scheduler/*` — resource pool, queue, bindings
- **Security state:** `/clusteros/security/*` — SPIFFE IDs, WireGuard config, ACLs
- **Build state:** `/clusteros/builds/{id}` — build job definitions, status, artifacts
- **Config:** `/clusteros/config/*` — cluster settings, quotas

The Omega scheduler uses etcd's OCC (via `mod_revision` CAS) for conflict-free parallel scheduling.

---

# Domain 11: Omega-Model Scheduler

## 11.1 Technical Background

### 11.1.1 Google Omega Paper: Shared State + Optimistic Concurrency

The Omega paper (Schwarzkopf, Kingsbury, Malani, "Omega: flexible, scalable schedulers for large compute clusters", SOSP 2013) describes a shared-state scheduling architecture that replaces Borg's centralized scheduler.

**Key Insight:** In a large cluster, scheduling conflicts are rare (<5%) because:
1. Different schedulers target different resource types (GPU vs CPU vs memory).
2. Different schedulers target different nodes (partitioned by affinity).
3. The cluster has many more resources than schedulers, so the probability of two schedulers choosing the same resources is low.

This insight enables parallel scheduling without global locks. Each scheduler:
1. Reads the current cluster state (shared state).
2. Makes placement decisions independently.
3. Commits decisions via optimistic concurrency control (CAS).
4. If the commit fails (conflict), re-reads and retries.

**Conflict Detection and Resolution:**

When two schedulers conflict (both try to allocate the same resources), the resolution strategy matters:

1. **First-committer-wins:** The first CAS succeeds; the second must retry. This is the simplest and most common approach.
2. **Priority-based:** The scheduler with higher priority wins. The lower-priority scheduler's CAS fails.
3. **Gang-aware:** If a gang-scheduled job's partial allocation conflicts, the entire gang is aborted and retried. This prevents partial allocation of multi-GPU jobs.

Helix uses first-committer-wins with gang-aware abort:

```go
func (s *OmegaScheduler) CommitGang(bindings []Binding) error {
    txn := s.etcd.Txn(ctx)

    // Check all resource mod_revisions
    for _, b := range bindings {
        txn = txn.If(clientv3.Compare(
            clientv3.ModRevision(b.PoolKey), "=", b.PoolRev))
    }

    // Apply all bindings atomically
    ops := make([]clientv3.Op, 0, len(bindings)*2)
    for _, b := range bindings {
        ops = append(ops,
            clientv3.OpPut(b.BindingKey, b.ToJSON()),
            clientv3.OpPut(b.PoolKey, b.UpdatedPoolJSON()),
        )
    }
    txn = txn.Then(ops...)

    resp, err := txn.Commit()
    if !resp.Succeeded {
        // Gang conflict: abort all bindings, retry
        return ErrGangConflictRetry
    }
    return nil
}
```

### 11.1.2 Conflict Rate Analysis

The Omega paper reports <5% conflict rate at Google's scale (10,000+ machines). For Helix, the expected conflict rate is even lower because:

1. **Smaller cluster:** Helix targets 10-100 nodes (vs Google's 10,000+).
2. **Fewer schedulers:** 2-3 schedulers (GPU, batch, edge) vs Google's many framework schedulers.
3. **Diverse resources:** GPU, CPU, and edge schedulers target different resource pools.

Estimated conflict rates for Helix:

| Cluster Size | Schedulers | Conflict Rate |
|-------------|------------|---------------|
| 10 nodes | 2 | <1% |
| 50 nodes | 3 | <2% |
| 100 nodes | 3 | <3% |
| 500 nodes | 4 | <5% |

### 11.1.3 Gang Scheduling via Atomic Transactions

Gang scheduling is critical for multi-GPU training jobs where all GPUs must be allocated simultaneously. Without gang scheduling, partial allocation can occur:

```
Node A: GPU 0 → Job X (allocated), GPU 1 → Job Y (allocated)
Node B: GPU 0 → available, GPU 1 → Job Y (allocated)

Job X needs 2 GPUs on the same node → FAILS (only 1 free on each node)
But: Job X would have succeeded if Job Y hadn't taken GPU 1 on Node A
```

Omega's transaction model solves this:

```go
func (s *GPUScheduler) ScheduleGang(req GangRequest) error {
    // Find nodes with enough free GPUs
    candidates := s.findGangCandidates(req)

    for _, candidate := range candidates {
        // Attempt atomic allocation
        bindings := make([]Binding, req.GPUCount)
        for i, gpu := range candidate.FreeGPUs[:req.GPUCount] {
            bindings[i] = Binding{
                PoolKey:  gpu.PoolKey(),
                PoolRev:  gpu.ModRevision,
                SessionID: req.SessionID,
            }
        }

        err := s.CommitGang(bindings)
        if err == nil {
            return nil // Success!
        }
        // Conflict: try next candidate
    }
    return ErrNoGangCandidate
}
```

### 11.1.4 Multi-Scheduler Extensibility

Helix's Omega model supports multiple independent schedulers, each optimized for a specific workload type:

**GPU Scheduler (pkg/scheduler/):**
- Gang scheduling for multi-GPU training.
- MIG-aware placement.
- GPU affinity (NVLink, PCIe topology).
- Marketplace-aware priority.

**Batch Scheduler:**
- Fair-share scheduling (DRF).
- Backfill optimization.
- Build job dependencies.
- Priority preemption.

**Edge Scheduler:**
- Bandwidth-aware placement.
- Latency minimization.
- SBC resource constraints.
- Console GPU limitations.

Each scheduler reads from the same shared state (etcd) and commits via OCC. New schedulers can be added without modifying existing ones.

### 11.1.5 Helix-Specific: Filter/Score/Bind Plugins, Preemption with Resource Reclamation

Helix's scheduler implements a Filter/Score/Bind plugin model inspired by Kubernetes Scheduling Framework:

```go
// pkg/scheduler/plugins.go
type FilterPlugin interface {
    Name() string
    Filter(state *ClusterState, req *ScheduleRequest, node *Node) (bool, error)
}

type ScorePlugin interface {
    Name() string
    Score(state *ClusterState, req *ScheduleRequest, node *Node) (int64, error)
}

type BindPlugin interface {
    Name() string
    Bind(state *ClusterState, req *ScheduleRequest, node *Node) error
}

// Pre-installed plugins:
// Filter: GPUAvailability, GPUMIGCompatibility, GPUVendorMatch,
//         ResourceFit, AffinityMatch, TaintToleration
// Score:  GPUAffinity, BinPacking, LoadBalance, RevenueOptimize
// Bind:   EtcdOCCBind, GPUClaimBind
```

**Preemption with Resource Reclamation:**

When a high-priority workload needs resources occupied by a lower-priority workload:

1. Identify victims: lower-priority workloads on nodes that could satisfy the request.
2. Select the minimum set of victims to free enough resources.
3. Send preemption notices (grace period: 30 seconds for HLX, 5 minutes for TAO).
4. After grace period, forcibly terminate victims.
5. Bind the high-priority workload to the freed resources.
6. Re-schedule victims on other nodes (if possible).

```go
func (s *GPUScheduler) Preempt(req *ScheduleRequest) error {
    victims := s.selectVictims(req)
    for _, v := range victims {
        s.sendPreemptionNotice(v, gracePeriod(req))
    }
    // Wait for grace period
    time.Sleep(gracePeriod(req))
    // Bind preempted resources
    return s.Bind(req)
}
```

---

# Domain 12: Production Readiness Review

## 12.1 Technical Background

### 12.1.1 Google SRE PRR Methodology

Google's Site Reliability Engineering (SRE) team defines Production Readiness Review (PRR) as a systematic assessment of a service's readiness for production. The PRR covers 9 dimensions:

1. **Architecture:** Is the service designed for reliability? Does it handle failures gracefully?
2. **On-call:** Is there a defined on-call rotation? Are runbooks available?
3. **Monitoring:** Are there dashboards, alerts, and SLOs?
4. **Incident response:** Is there a defined incident process? Are postmortems conducted?
5. **Capacity planning:** Can the service handle projected load? Are there scaling procedures?
6. **Change management:** Are deployments automated and reversible? Can rollbacks be performed?
7. **Security:** Is the service secure? Are vulnerabilities tracked?
8. **Performance:** Does the service meet latency and throughput requirements?
9. **Operability:** Can the service be operated by someone other than the original author?

### 12.1.2 The 95% Closure Bar

Google SRE recommends a 95% closure bar for PRR items. The rationale:

- **5% uncertainty:** Even with thorough review, some risks remain unknown.
- **Diminishing returns:** Going from 95% to 100% requires exponentially more effort.
- **Continuous PRR:** The remaining 5% is addressed through continuous review and incident-driven improvement.

**Why not 100%?**

1. Some risks are inherently unknowable (black swan events).
2. Some mitigations require production traffic to validate.
3. Some items depend on external systems outside your control.
4. Perfect is the enemy of good — a 95% service in production is better than a 100% service that never launches.

### 12.1.3 GameDay Exercises for the Remaining 5%

The remaining 5% of PRR items are validated through GameDay exercises — controlled failure scenarios run in production (or production-like environment).

**GameDay Template:**

1. **Objective:** What are we testing?
2. **Hypothesis:** What do we expect to happen?
3. **Steady-state:** What does normal operation look like? (metrics baseline)
4. **Experiment:** What failure will we inject?
5. **Observation:** What metrics/logs will we watch?
6. **Criteria:** What constitutes success/failure?
7. **Rollback:** How do we restore normal operation?
8. **After-action:** What did we learn? What needs to change?

### 12.1.4 Continuous PRR Automation

PRR should not be a one-time gate. It should be continuously validated:

1. **Automated PRR checks:** Run as part of CI/CD. Fail the build if PRR items regress.
2. **SLO monitoring:** Track SLO compliance in real-time. Alert on degradation.
3. **Chaos engineering:** Continuously inject faults and verify recovery.
4. **Capacity forecasting:** Model load growth and verify capacity plans.
5. **Security scanning:** Continuously scan for vulnerabilities (govulncheck, SBOM).

### 12.1.5 Helix-Specific: 80-Item PRR

Helix's PRR (documented in `docs/PRODUCTION_READINESS_REVIEW.md`, ticket HXC-1286) consists of 80 items across 10 categories:

| Category | Items | PASS | PARTIAL | NOT-READY |
|----------|-------|------|---------|-----------|
| A. Build & Local Gates | 10 | 6 | 2 | 2 |
| B. Test Coverage & Anti-Bluff | 12 | 8 | 2 | 0 |
| C. Security / E2EE / Attestation | 12 | 9 | 2 | 1 |
| D. Observability & Metrics | 9 | 9 | 0 | 0 |
| E. Data / Registry / Schema | 8 | 8 | 0 | 0 |
| F. Documentation Sync | 9 | 7 | 2 | 0 |
| G. Cross-Platform Parity | 8 | 8 | 0 | 0 |
| H. Resource / Host Safety | 5 | 5 | 0 | 0 |
| I. Deployment | 5 | 5 | 0 | 0 |
| J. Dependency Hygiene | 2 | 2 | 0 | 0 |
| **Total** | **80** | **66** | **10** | **4** |

**Current status: 66/80 = 82.5% PASS (target: ≥95%)**

**Critical Gaps (NOT-READY):**

1. **CI/CD pipeline ACTIVE** (Item 8): All GitHub Actions workflows are disabled. No automated quality gate runs on push/PR.
2. **Release pipeline active** (Item 9): Release automation is disabled.
3. **mTLS everywhere not confirmed** (Item 33): HXC-600 still Queued.

**Path to 95% (76/80 PASS):**

To reach 95%, Helix needs 10 more PASS items from the current PARTIAL/NOT-READY pool:

1. Re-enable CI/CD workflows (Items 8, 9 → PASS): 2 items
2. Execute VM integration tests (Item 16 → PASS): 1 item
3. Complete mTLS deployment (Item 33 → PASS): 1 item
4. Add continuous SBOM + dependabot (Item 34 → PASS): 1 item
5. Wire coverage gate into CI (Item 7 → PASS): 1 item
6. Execute HelixQA challenges with evidence (Item 21 → PASS): 1 item
7. Add docs-verify CI gate (Item 60 → PASS): 1 item
8. Verify Docker image build in CI (Item 10 → PASS): 1 item
9. One more PARTIAL → PASS: 1 item

**Total after remediation: 76/80 = 95% PASS**

---

## References

### Domain 1: Distributed Cluster OS Architecture
- Schwarzkopf et al., "Omega: flexible, scalable schedulers for large compute clusters," SOSP 2013. https://research.google/pubs/pub41684/
- Verma et al., "Large-scale cluster management at Google with Borg," EuroSys 2015. https://research.google/pubs/pub43438/
- Burns et al., "Borg, Omega, and Kubernetes," ACM Queue, 2016. https://queue.acm.org/detail.cfm?id=2898444
- Hindman et al., "Mesos: A Platform for Fine-Grained Resource Sharing in the Data Center," NSDI 2011. https://people.eecs.berkeley.edu/~alig/papers/mesos.pdf
- Kubernetes Documentation. https://kubernetes.io/docs/

### Domain 2: Deterministic Simulation Testing
- Apple FoundationDB Simulation. https://www.foundationdb.org/files/talks/concurrency-isnt-parallelism.pdf
- Kingsbury, "Jepsen: Testing Distributed Systems." https://jepsen.io/
- Mu et al., "Uncle-Nephew: Metamorphic Testing for CockroachDB." https://www.cockroachlabs.com/blog/metadata-queries/
- Tokio Turmoil. https://github.com/tokio-rs/turmoil

### Domain 3: GPU Orchestration
- NVIDIA MIG User Guide. https://docs.nvidia.com/datacenter/tesla/mig-user-guide/
- NVIDIA MPS Documentation. https://docs.nvidia.com/deploy/mps/
- SLURM GRES Documentation. https://slurm.schedmd.com/gres.html
- Kubernetes Device Plugins. https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/

### Domain 4: WireGuard Mesh VPN
- Donenfeld, "WireGuard: Next Generation Kernel Network Tunnel." https://www.wireguard.com/papers/wireguard.pdf
- RFC 8489: STUN. https://datatracker.ietf.org/doc/html/rfc8489
- Ford et al., "Peer-to-Peer Communication Across Network Address Translators." https://pdos.csail.mit.edu/papers/p2pnat.pdf

### Domain 5: SPIFFE/SPIRE
- SPIFFE Specification. https://github.com/spiffe/spiffe/blob/main/standards/SPIFFE.md
- SPIRE Documentation. https://spiffe.io/docs/latest/spire-about/

### Domain 6: SWIM Gossip Protocol
- Das et al., "SWIM: Scalable Weakly-consistent Infection-style Membership." https://www.cs.cornell.edu/~asdas/research/dsn02-swim.pdf
- Lifeguard: SWIM's Lifeguard. https://www.hashicorp.com/blog/hashicorp-serf-1-3-0

### Domain 7: Chaos Engineering
- Principles of Chaos Engineering. https://principlesofchaos.org/
- Basiri et al., "Chaos Engineering." https://dl.acm.org/doi/10.1145/3010326
- Gremlin. https://www.gremlin.com/
- Chaos Mesh. https://chaos-mesh.org/

### Domain 8: Post-Quantum Cryptography
- NIST FIPS 203 (ML-KEM). https://csrc.nist.gov/pubs/fips/203/final
- NIST FIPS 204 (ML-DSA). https://csrc.nist.gov/pubs/fips/204/final
- NIST PQC Standardization. https://csrc.nist.gov/Projects/post-quantum-cryptography

### Domain 9: Heterogeneous Compute Management
- Raspberry Pi Documentation. https://www.raspberrypi.com/documentation/
- PS4 Linux Project. https://github.com/ps4-linux
- Xilinx Vitis. https://www.xilinx.com/products/design-tools/vitis.html
- Intel oneAPI. https://www.intel.com/content/www/us/en/developer/tools/oneapi/overview.html

### Domain 10: Etcd Integration Patterns
- etcd Documentation. https://etcd.io/docs/
- Howard et al., "Raft: Consensus for Raft-based Key-Value Stores." https://raft.github.io/raft.pdf

### Domain 11: Omega-Model Scheduler
- Schwarzkopf et al., "Omega." (Same as Domain 1 reference)
- Ghodsi et al., "Dominant Resource Fairness: Fair Allocation of Multiple Resource Types." NSDI 2011.
- Kubernetes Scheduling Framework. https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/

### Domain 12: Production Readiness Review
- Beyer et al., "Site Reliability Engineering," O'Reilly, 2016.
- Beyer et al., "The Site Reliability Workbook," O'Reilly, 2018.
- Helix PRR Document. `docs/PRODUCTION_READINESS_REVIEW.md`

---

*End of Technical Research Compendium*
