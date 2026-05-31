# 1. Federation Topology & Architecture Patterns

> *"The question is not whether to federate, but where to draw the boundaries between autonomous cells."*

Distributed systems at scale rarely fail because individual machines crash — they fail because the mechanisms designed to keep those machines coordinated become overwhelmed by the very scale they were meant to manage. HelixCluster Phase 6 addresses this fundamental tension by adopting a **cell-based hierarchical topology**: independent clusters called *cells* that maintain sovereign control planes while participating in a broader federation for cross-cell workload placement, service discovery, and resource sharing. This chapter establishes the architectural foundations — the topology patterns governing how cells relate to one another, the binding modes determining how tightly they couple, and the lifecycle governing how they enter and exit the collective.

The design is validated by Google's Borg system, which demonstrated at median scale that cells of approximately 10,000 machines operate as the sweet spot between manageability and efficiency. HelixCluster adapts this proven model to a cloud-native world, refining it with modern technologies including eBPF-based networking, SPIFFE identity federation, and CRDT-based state replication.

---

## 1.1 Cell-Based Hierarchical Architecture

### 1.1.1 Google Borg Cell Model — Independent Cells Validated at Scale

Google's Borg system remains the canonical production validation for cell-based cluster architecture. A Borg *cell* is a set of machines managed by a single logically centralized controller — the Borgmaster — with a median cell size of **10,000 machines**, ranging from a few hundred nodes to warehouse-scale installations exceeding 100,000 machines. Each cell operates as an entirely independent failure domain: its own Borgmaster (replicated via Paxos across five replicas), its own scheduler process, its own Borglet agents, and crucially, **no cross-cell coordination at the Borg level whatsoever**.

Several scalability techniques validated at this scale directly inform HelixCluster's design. The Borgmaster delegates scheduling to an independent scheduler process, preventing head-of-line blocking. Score caching eliminates redundant feasibility calculations. Equivalence classes amortize scheduling cost across identical tasks. Relaxed randomization evaluates machines in random order and accepts the first sufficiently good fit — achieving near-optimal bin-packing at a fraction of computational cost.

Borg's cell independence creates hard failure-domain boundaries: a control plane outage affects only that cell. Critically, smaller cells require proportionally more machines due to bin-packing fragmentation, while workload sharing between production and batch jobs improves utilization through complementary resource profiles. Even at Borg's scale, a 2,000-machine service experiences more than ten task exits per day as normal operation — demonstrating that failure is a baseline assumption, not an exception.

### 1.1.2 HelixCluster Adaptation — Cells as Autonomous Blocks with Federated Control Plane

HelixCluster Phase 6 adapts the Borg cell model to the Kubernetes era. A **cell** is a complete, self-managing Kubernetes cluster with an independent control plane and 100-5,000 worker nodes:

- **Cell Control Plane**: Independent etcd cluster (3-5 nodes), API server replicas, and scheduler — all colocated within a single region with sub-5ms RTT between etcd members.
- **Cell Data Plane**: 100-5,000 worker nodes running Cilium eBPF-based CNI for high-performance pod networking and network policy enforcement.
- **Cell Gateway**: 1-3 dedicated WireGuard gateway nodes for encrypting and routing cross-cell traffic.
- **Cell Agent**: Federation agent on every node handling gossip membership, mesh tunnel management, and SPIFFE/SPIRE workload identity attestation.

The federation layer sits above individual cells without supplanting them. An optional meta-cluster control plane — typically Karmada or Open Cluster Management (OCM) — coordinates cross-cell operations. But this control plane is genuinely optional: a cell can operate indefinitely without it, and its absence does not compromise intra-cell functionality. This reflects the principle that **per-cell strong consistency is non-negotiable, while cross-cell coordination is best-effort and gracefully degradable**.

The tiered architecture is visualized as follows:

```
+------------------------------------------------------------------+
|                    HELIXCLUSTER FEDERATION                        |
|                                                                   |
|  Tier 0: Federation Plane (optional governance)                  |
|  +-- Karmada control plane or OCM Hub (3-5 nodes)               |
|  +-- Global policy distribution + cross-cell workload scheduling |
|                                                                   |
|  Tier 1: Cell Layer (independent, 100-5000 nodes each)           |
|  +-- Cell Alpha:  etcd 3-node HA, Cilium  (us-east-1)           |
|  +-- Cell Beta:   etcd 3-node HA, Cilium  (eu-west-1)           |
|  +-- Cell Gamma:  etcd 3-node HA, Cilium  (ap-south-1)          |
|  +-- ... up to 255 cells (511 with Cilium extended)              |
|                                                                   |
|  Tier 2: Mesh Layer (WireGuard + Cilium Cluster Mesh)            |
|  +-- Full or partial mesh between cell gateways                  |
|  +-- Automatic NAT traversal, TURN fallback                      |
|                                                                   |
|  Tier 3: Application Layer                                       |
|  +-- Federated services via Cluster Mesh service discovery       |
|  +-- GitOps (ArgoCD ApplicationSets) for cross-cell deploy       |
+------------------------------------------------------------------+
```

### 1.1.3 Cell Boundaries — Network Topology, Administrative Domain, Trust Zone, Data Sovereignty

Determining where one cell ends and another begins is one of the most consequential decisions in federation design. HelixCluster defines cell boundaries across four dimensions:

| Boundary Dimension | Description | Typical Scope | Key Guidance |
|---|---|---|---|
| **Network Topology** | etcd members must maintain sub-5ms RTT; P99 RTT should stay under 50ms for production | Single AZ or metro area | **Never stretch etcd across regions**; use gateway mesh for cross-region connectivity |
| **Administrative Domain** | Independent teams or operational groups require sovereign control planes for autonomy and change management | Per team, BU, or ops group | Each cell has independent RBAC, policy enforcement, and upgrade schedule |
| **Trust Zone** | Security boundaries between environments (prod/staging/dev) or organizations | Per environment or tenant | Separate SPIFFE trust domains per cell; no shared root CAs across zones |
| **Data Sovereignty** | Regulatory data residency requirements (GDPR, HIPAA, ITAR, PIPL) | Per legal jurisdiction | Cells pinned to geographic regions; cross-cell data flows governed by policy |

These boundaries frequently overlap. A European financial institution might operate cells in Frankfurt and Dublin for data sovereignty, with each further subdivided by environment creating six cells total — each with independent etcd clusters, separate trust domains, and distinct administrative ownership. Every cell is architecturally identical regardless of why the boundary was drawn.

The critical insight is that cell boundaries are **deliberate consistency boundaries**. etcd's Raft consensus requires election timeout >= 10x maximum RTT between members, with a hard maximum of 50 seconds. Cross-country RTT in the US ranges from 50-130ms; US-to-Europe spans 139-152ms. At these latencies, WAN-tuned etcd becomes technically functional but operationally unacceptable — leader failure detection takes seconds rather than milliseconds. The HelixCluster position is unequivocal: **one region equals one cell, and etcd never stretches across regions**.

---

## 1.2 Topology Types Compared

The arrangement of cells into a federation is not one-size-fits-all. HelixCluster supports five distinct topology patterns, each optimized for different scale points, organizational structures, and latency requirements.

### 1.2.1 Flat Federation

In a flat topology, all cells are peers. Every gateway maintains a direct WireGuard tunnel to every other gateway, creating a full mesh with no hierarchical control plane.

```
+--------+     +--------+     +--------+
| Cell A |<--->| Cell B |<--->| Cell C |
+--------+     +--------+     +--------+
   ^    \         ^    \         ^    \
   |     \        |     \        |     \
   +------>+--------+     +--------+     |
           | Cell D |<--->| Cell E |
           +--------+     +--------+
```

Flat federation is conceptually simple — no central coordination point, no policy inheritance complexity. It works well for 2-10 cells, such as hobbyist homelabs. However, O(n²) mesh growth creates a scalability wall: at 20 cells, each gateway maintains 19 tunnels; at 100 cells, 99 tunnels. Beyond approximately ten cells, flat federation becomes operationally untenable.

### 1.2.2 Hierarchical Tree

The hierarchical tree arranges cells into parent-child relationships, with parents aggregating state from children and propagating policy downward. This maps naturally to organizational structures — headquarters parenting regional cells, which parent edge cells.

```
                +------------+
                |  Root Cell |
                | (Governance|
                |   Hub)     |
                +-----+------+
                      |
        +-------------+-------------+
        |             |             |
   +----v----+   +----v----+   +----v----+
   | Cell A  |   | Cell B  |   | Cell C  |
   | (East)  |   | (West)  |   |(Europe) |
   +----+----+   +----+----+   +----+----+
        |             |             |
   +----v----+   +----v----+   +----v----+
   | A-Edge  |   | B-Edge  |   | C-Edge  |
   +---------+   +---------+   +---------+
```

The hierarchical tree excels at policy inheritance and governance — parents enforce security baselines and compliance across descendants. It scales to 100+ cells by containing state propagation within subtrees. The trade-off is that parent failure affects all descendants. Routing between siblings traverses the common ancestor, adding latency.

### 1.2.3 Full Mesh

Every cell gateway connects directly to every other gateway, organized under a meta-control plane (Karmada or OCM) that abstracts mesh complexity from operators.

```
                    +--------+
                    |Karmada |
                    | Control|
                    | Plane  |
                    +---+----+
                        |
            +-----------+-----------+
            |           |           |
       +----v----+ +----v----+ +----v----+
       | Cell A  |<->| Cell B  |<->| Cell C  |
       +---------+   +---------+   +---------+
            |    \       |       /     |
            |     \      |      /      |
            +------+-----+-----+-------+    (full gateway mesh)
```

Full mesh provides the lowest cross-cell latency — direct paths between any two cells. Cilium Cluster Mesh operates in this mode, establishing eBPF-based tunnels between all clusters for pod-to-pod connectivity with only **0.5-1ms p99 overhead**. The limitation remains gateway capacity: each cell's gateways must maintain O(n) connections. Cilium validates this to 250 clusters (100 nodes each). Full mesh is recommended for up to 20 cells where latency dominates.

### 1.2.4 Partial Mesh

Each cell connects to k nearest neighbors (typically k=3-5), with multi-hop routing for non-adjacent pairs. This scales the furthest — to 255 cells (511 with Cilium extended) — because gateway load is O(k) rather than O(n).

```
+--------+      +--------+      +--------+      +--------+
| Cell 1 |<---->| Cell 2 |<---->| Cell 3 |<---->| Cell 4 |
+--------+      +--------+      +--------+      +--------+
   ^                 ^               ^               ^
   |                 |               |               |
   +-----------------+               +---------------+
      (redundant shortcut links)
```

Partial mesh is adaptive: cells in the same region maintain direct links; distant cells route through intermediaries. A packet from Singapore to Frankfurt might traverse Singapore → Tokyo → Frankfurt. For control plane traffic this is acceptable; for high-throughput workloads, direct tunnels can be established on demand. Partial mesh is the recommended default for 20-255 cell deployments.

### 1.2.5 Hub-and-Spoke

A central hub cell provides coordination and routing; spokes connect only to the hub. All cross-spoke traffic flows through the hub.

```
                    +--------+
                    |  Hub   |
                    | Cell   |
                    | (OCM)  |
                    +-+--+-+-
                      |  |  |
                +-----+  |  +-----+
                |        |        |
           +----v----+ +--v---+ +--v----+
           | Spoke A | |Spoke B| |Spoke C|
           | (Edge)  | |(Edge) | |(Edge) |
           +---------+ +-------+ +-------+
```

Hub-and-spoke centralizes governance and simplifies spoke configuration. Spokes can reside behind NAT permitting only outbound hub connections. The hub is a single point of failure — if it goes offline, spokes lose cross-cell coordination but continue operating independently. This topology suits compliance environments where traffic must be auditable at a central point.

### 1.2.6 Topology Comparison

| Topology | Description | Gateway Load | Max Cells | Cross-Cell Latency | Best For |
|---|---|---|---|---|---|
| **Flat Federation** | All cells equal; direct full mesh | O(n) per gateway | 2-10 | Lowest (direct) | Homelabs, small deployments |
| **Hierarchical Tree** | Parent-child; policy inheritance | O(n) per cell | 100+ | Medium (via parent) | Enterprise org structures |
| **Full Mesh** | Every gateway to every gateway | O(n) per gateway | 20-250 | Lowest (direct) | Latency-sensitive workloads |
| **Partial Mesh** | k nearest neighbors (k=3-5) | O(k) per gateway | 255-511 | Higher (multi-hop) | Geo-distributed large scale |
| **Hub-and-Spoke** | Central hub; spoke-only edges | O(1) per spoke | 100+ | Highest (via hub) | Governance, edge, compliance |

The topology decision should be revisited as the federation grows. A common evolution: flat federation for 2-5 cells, full mesh as regions are added, partial mesh beyond 20 cells. The topology is runtime-configurable — Cilium Cluster Mesh can shift between full and partial connectivity without workload disruption.

---

## 1.3 Block Binding Modes

While topology defines the physical arrangement of cells, **binding mode** defines the logical coupling semantics — how tightly cells share resources, scheduling, and identity.

### 1.3.1 Cluster-of-Clusters (Default)

Cells remain fully independent. Each runs its own scheduler and makes local decisions without coordination. Cross-cell traffic uses Cilium Cluster Mesh service routing through encrypted gateway tunnels, but workloads are scheduled independently.

```
+------------+                    +------------+
|  Cell A    |   Cilium Cluster   |  Cell B    |
| Pod X runs |<----Mesh Route---->| Pod Y runs |
| (local     |  (WireGuard enc)   | (local     |
| scheduler) |                    | scheduler) |
+------------+                    +------------+
```

This is the safest mode: compromise in one cell cannot escalate through scheduling dependencies; each cell upgrades on its own schedule. The trade-off is suboptimal resource utilization — if Cell A is saturated and Cell B has idle capacity, workloads cannot automatically spill over without explicit PropagationPolicy. This is the default for production federations where security and operational independence outweigh efficiency.

### 1.3.2 Equal-Peer Nodes

Nodes from multiple cells merge into a single logical resource pool presented to a shared scheduler. This requires a shared etcd instance — or strongly synchronized etcd clusters — across all participating cells, because the scheduler must have a unified node view.

The critical constraint is latency: **equal-peer mode is only viable for cells within 10ms RTT**. Beyond this, scheduler latency, etcd write latency, and split-brain risk become unacceptable. In practice, this restricts equal-peer to cells within the same metro area or AZ group.

This mode offers maximum resource pooling efficiency but blurs security boundaries and creates operational coupling: all cells must run compatible Kubernetes versions, share CIDR allocations, and coordinate upgrades. Suitable for trusted environments — research consortiums or friend-to-friend homelabs — but discouraged for multi-tenant deployments.

### 1.3.3 Gateway Bridging

Cells maintain full scheduling independence but establish dedicated gateway-to-gateway tunnels with latency-aware routing. Cross-cell traffic flows through gateway pairs selected by real-time latency and bandwidth measurements.

Gateway bridging enables dynamic path selection: if the direct Cell A-to-Cell C tunnel exceeds a threshold, traffic routes through Cell B. It also supports bandwidth aggregation across multiple gateway pairs. Recommended for geo-distributed deployments with heterogeneous network quality.

### 1.3.4 Cloud Extension

On-premise cells extend into cloud spot instances when local capacity saturates. Cloud nodes join as a sub-cell with cloud-specific scheduling constraints.

```
+------------------------------------------+
|           On-Premise Cell Alpha          |
|  +---------+  +---------+  +---------+  |
|  | Node 1  |  | Node 2  |  | Node 3  |  |
|  |(bare    |  |(bare    |  |(bare    |  |
|  | metal)  |  | metal)  |  | metal)  |  |
|  +---------+  +---------+  +---------+  |
|       \            |            /       |
|        \           |           /        |
|         +----v-----v-----v----+         |
|              Local Gateway              |
+--------------------+--------------------+
                     |
        +------------v-------------+
        |    Cloud Sub-Cell        |
        |  +--------+ +--------+  |
        |  | AWS    | | Azure  |  |
        |  | Spot   | | Spot   |  |
        |  +--------+ +--------+  |
        |  (auto-scaled,           |
        |   preemption-tolerant)   |
        +-------------------------+
```

Spot instances offer 50-90% discounts but carry 2-minute (AWS) or 30-second (Azure) termination warnings. Workloads must be interruption-tolerant: batch processing, stateless services, or checkpointed compute. Karmada's PropagationPolicy supports this natively by spreading replicas across clusters with different cost profiles.

### 1.3.5 Binding Mode Comparison

| Mode | Coupling | Scheduling | Use Case | Security |
|---|---|---|---|---|
| **Cluster-of-Clusters** | Loose | Independent per cell | Default production; compliance | Strong per-cell isolation |
| **Equal-Peer Nodes** | Tight | Shared pool; unified scheduler | Trusted peers; low-latency | Blurred boundaries |
| **Gateway Bridging** | Medium | Independent with cross-cell routing | Geo-distributed; partial mesh | Encrypted; policy-controlled |
| **Cloud Extension** | Loose | Independent with cloud constraints | Cost optimization; burst | Cloud provider trust required |

---

## 1.4 Cluster Lifecycle

Federating a cell is not an atomic operation — it is a progression through distinct states, each with specific preconditions and failure modes. HelixCluster defines a ten-state lifecycle machine.

### 1.4.1 States: CREATE → DISCOVER → AUTHENTICATE → JOIN → SYNC → OPERATE → PARTITION → RECOVER → LEAVE → CLEANUP

```
+-----------------+     +-----------------+     +-----------------+
|     CREATE      |---->|    DISCOVER     |---->|  AUTHENTICATE   |
| Bootstrap local |     | mDNS/DNS-SD on  |     | SPIFFE/SPIRE    |
| control plane;  |     | LAN; DHT on WAN |     | mutual attesta- |
| generate cell   |     | rendezvous      |     | tion; CA bundle |
| identity        |     |                 |     | exchange        |
+-----------------+     +--------+--------+     +--------+--------+
                                 |                       |
+-----------------+     +--------v--------+     +--------v--------+
|    CLEANUP      |<----|     LEAVE       |<----|    OPERATE      |
| Archive state;  |     | Graceful depart |     | Full federation |
| tombstone ID    |     | handoff; notify |     | workload place  |
|                 |     | peers           |     | service discov  |
+-----------------+     +-----------------+     +--------+--------+
      ^                                                  |
      |     +-----------------+     +-----------------+  |
      |     |   SYNCHRONIZE   |<----|      JOIN       |<-+
      |     | CRDT anti-entro |     | Cell enters     |
      |     | py; Merkle tree |     | mesh; gets ID;  |
      |     | state compare   |     | tunnels estab   |
      |     +--------+--------+     +-----------------+
      |              |                                   |
      +--------------+-----------------------------------+
                     |
              +------v------+
              |  PARTITION  |
              | Detect via  |
              | Phi accrual |
              | + SWIM probe|
              +------+------+
                     |
              +------v------+
              |   RECOVER   |
              | Mesh heals; |
              | CRDT merge; |
              | Merkle sync |
              +-------------+
```

The ten states:

| State | Description | Preconditions | Failure Action |
|---|---|---|---|
| **CREATE** | Local control plane bootstraps; cell generates SPIFFE trust domain, WireGuard keypair, initial config | None (cell may be air-gapped) | Retry bootstrap; alert operator |
| **DISCOVER** | Cell advertises via mDNS/DNS-SD on LAN or queries Kademlia DHT on WAN to find peers | Network connectivity to bootstrap node | Fall back to static seeds; exponential backoff retry |
| **AUTHENTICATE** | Mutual SPIFFE/SPIRE attestation; CA bundle exchange between cells | At least one reachable peer discovered | Reject untrusted peer; log security event; alert |
| **JOIN** | Cell receives unique cell ID (uint8/uint16); establishes WireGuard tunnels; joins gossip pools | Authentication succeeded | Revert to DISCOVER |
| **SYNC** | Synchronizes state: CRDT merge for eventually-consistent data; Merkle tree comparison for divergence | Cell has ID; mesh tunnels active | Request full state sync |
| **OPERATE** | Fully federated; participates in workload placement, service discovery, policy enforcement | Synchronization complete | On partition → PARTITION |
| **PARTITION** | Network partition detected via Phi accrual or SWIM suspicion; cell operates independently | Unreachability exceeds threshold | If heals → RECOVER; if persists → LEAVE |
| **RECOVER** | Partition heals; CRDTs auto-merge; strong-consistency state uses Merkle delta-sync | Connectivity restored | Escalate if irreconcilable |
| **LEAVE** | Graceful departure; drains connections; hands off state; resigns cell ID | Operator-initiated or decommissioned | Force → CLEANUP |
| **CLEANUP** | Terminal; cell removed from peers; state archived; cell ID tombstoned 24h before reuse | LEAVE completed or forced | Manual intervention |

### 1.4.2 Cluster Lifecycle State Machine (Go)

```go
package federation

import "fmt"

// CellState represents the lifecycle state of a cell in the federation.
type CellState int

const (
    CellStateCreating      CellState = iota // 0: Local control plane bootstrapping
    CellStateDiscovering                    // 1: mDNS/DHT discovery active
    CellStateAuthenticating                 // 2: SPIFFE attestation in progress
    CellStateJoining                        // 3: Mesh establishment
    CellStateSynchronizing                  // 4: State sync with existing cells
    CellStateOperating                      // 5: Fully federated
    CellStatePartitioned                    // 6: Network partition detected
    CellStateRecovering                     // 7: Partition healing, reconciliation
    CellStateLeaving                        // 8: Graceful departure
    CellStateCleanup                        // 9: Tombstoned, archived
)

func (s CellState) String() string {
    switch s {
    case CellStateCreating:       return "CREATE"
    case CellStateDiscovering:    return "DISCOVER"
    case CellStateAuthenticating: return "AUTHENTICATE"
    case CellStateJoining:        return "JOIN"
    case CellStateSynchronizing:  return "SYNC"
    case CellStateOperating:      return "OPERATE"
    case CellStatePartitioned:    return "PARTITION"
    case CellStateRecovering:     return "RECOVER"
    case CellStateLeaving:        return "LEAVE"
    case CellStateCleanup:        return "CLEANUP"
    default:                      return fmt.Sprintf("UNKNOWN(%d)", s)
    }
}

// ValidTransitions defines allowed state transitions.
var ValidTransitions = map[CellState][]CellState{
    CellStateCreating:      {CellStateDiscovering},
    CellStateDiscovering:   {CellStateAuthenticating, CellStateCreating},
    CellStateAuthenticating: {CellStateJoining, CellStateDiscovering},
    CellStateJoining:       {CellStateSynchronizing, CellStateDiscovering},
    CellStateSynchronizing: {CellStateOperating, CellStateLeaving},
    CellStateOperating:     {CellStatePartitioned, CellStateLeaving},
    CellStatePartitioned:   {CellStateRecovering, CellStateLeaving},
    CellStateRecovering:    {CellStateOperating, CellStatePartitioned},
    CellStateLeaving:       {CellStateCleanup, CellStateOperating},
    CellStateCleanup:       {}, // Terminal
}

// CanTransition checks if a state change from 'from' to 'to' is valid.
func CanTransition(from, to CellState) bool {
    for _, allowed := range ValidTransitions[from] {
        if allowed == to {
            return true
        }
    }
    return false
}

// IsTerminal returns true if the state has no valid outbound transitions.
func (s CellState) IsTerminal() bool {
    return len(ValidTransitions[s]) == 0
}

// CellStateMachine encapsulates state management with transition hooks.
type CellStateMachine struct {
    state        CellState
    onTransition func(from, to CellState)
}

func NewCellStateMachine() *CellStateMachine {
    return &CellStateMachine{state: CellStateCreating}
}

func (sm *CellStateMachine) Current() CellState { return sm.state }

func (sm *CellStateMachine) Transition(to CellState) error {
    if !CanTransition(sm.state, to) {
        return fmt.Errorf("invalid transition: %s -> %s", sm.state, to)
    }
    from := sm.state
    sm.state = to
    if sm.onTransition != nil {
        sm.onTransition(from, to)
    }
    return nil
}
```

The state machine enforces safe transitions: CREATE only moves forward to DISCOVER — no shortcuts; AUTHENTICATE failure reverts to DISCOVER rather than CREATE, preserving bootstrapped local state; PARTITION is reachable only from OPERATE and exits through RECOVER or LEAVE — it is never terminal; CLEANUP is terminal — manual operator intervention is required to reintroduce the cell. The `onTransition` hook enables custom logic: joining the mesh on JOIN entry, initiating Merkle comparison on SYNC entry, or draining connections on LEAVE entry.

---

## 1.5 Federation Technologies Evaluation

HelixCluster Phase 6 integrates three production-validated open-source projects rather than reinventing federation from first principles: Karmada for cluster lifecycle and workload placement, Cilium Cluster Mesh for inter-cluster networking, and ArgoCD ApplicationSets for GitOps-driven application distribution.

### 1.5.1 Karmada — 100 Clusters / 500,000 Nodes / 2,000,000 Pods

Karmada (Kubernetes Armada), a CNCF project developed by Huawei, serves as HelixCluster's primary federation control plane. It is the spiritual successor to deprecated KubeFed v2, using native Kubernetes APIs for resource templates — no "federated CRD" learning curve. The `PropagationPolicy` API controls placement with cluster affinity and spread constraints; `OverridePolicy` enables per-cell configuration customization. Both Push and Pull modes are supported, with Pull mode recommended beyond 100 clusters to reduce hub etcd pressure.

Karmada has been tested at **100 clusters x 5,000 nodes x 20,000 pods = 500,000 nodes and 2,000,000 pods** total, maintaining scheduler SLO of 99.9% within 512ms and propagation SLO of 99.9% within 1.024 seconds. Karmada 1.3 reduced memory consumption by 85% and CPU by 32% at scale versus 1.2. These metrics confirm comfortable operation within HelixCluster's 255-cell target.

For minimal deployments, Karmada is unnecessary — Cilium Cluster Mesh and WireGuard provide sufficient connectivity. For centralized workload placement and policy propagation, Karmada deploys as a 3-5 node control plane in a hub region.

### 1.5.2 Cilium Cluster Mesh — 0.5-1ms P99 Overhead, eBPF-Based

Cilium Cluster Mesh provides HelixCluster's networking foundation. Built on eBPF, it enables direct pod-to-pod connectivity across clusters at kernel level without sidecar proxies.

Cilium Cluster Mesh adds **0.5-1ms p99 latency overhead** — the lowest among all evaluated solutions. This is measured for L3/L4 forwarding without WireGuard; enabling WireGuard adds ~99% latency overhead but provides cryptographic protection for cross-datacenter links. CPU consumption is lowest of any service mesh because processing occurs in-kernel via eBPF.

Scale validation confirms **255 clusters by default**, configurable to **511** with `maxConnectedClusters`. CI testing validates **250 clusters x 100 nodes = 25,000 nodes** (~250,000 endpoints). Production deployments of 100+ clusters are reported. These limits align with HelixCluster's target scale.

Cilium Cluster Mesh replaces the service mesh for L3/L4 networking but does not provide advanced L7 traffic management. Canary deployments, fault injection, and header-based routing require Linkerd (33% P99 overhead, lowest among L7 meshes) or Istio Ambient mode.

### 1.5.3 ArgoCD ApplicationSets — Multi-Cluster GitOps

ArgoCD ApplicationSets provide declarative application distribution across the federation. The Cluster generator auto-discovers registered clusters via cluster secrets; the Git generator creates parameters from repository structure; the Matrix generator combines both for combinatorial deployment.

ApplicationSets reduce multi-cluster deployment from 30+ minutes to approximately 5 minutes — an 83% reduction. They support pruning, self-healing, and automated rollback via Git revert. AppProjects provide multi-tenant RBAC for team-level application management.

ArgoCD runs in the hub cell, managing ApplicationSets targeting all member cells. Karmada handles cluster-scoped infrastructure; ArgoCD handles namespace-scoped applications. This mirrors the separation between Tier 0 (federation governance) and Tier 3 (application layer).

### 1.5.4 Consistency Model Tiering

No single consistency model serves all use cases. HelixCluster implements a tiered approach:

| State Tier | Consistency | Technology | Rationale | Example Data |
|---|---|---|---|---|
| **Tier 1: Critical** | Linearizable (CP) | Raft (etcd per cell) | Split-brain causes data loss or security compromise | Membership, resource allocation, security policies |
| **Tier 2: Operational** | Causal | Vector clocks + Karmada | Ordering matters; brief inconsistency tolerable | Scheduling queue, policy version |
| **Tier 3: Observable** | Eventual (AP) | CRDTs (delta-sync) | Transient divergence acceptable | Node presence, load metrics, health status |
| **Tier 4: Config** | Eventual (AP) | CRDTs or Propagation | Converges quickly; all parties must progress | Feature flags, tunable parameters |
| **Tier 5: Telemetry** | Eventual (AP) | Anti-entropy repair | Volume dominates; individual loss acceptable | Metrics, logs, traces |

The 60/40 split — approximately 60% of cluster state suitable for CRDT-based eventual consistency and 40% requiring strong consistency — emerges from empirical classification. Delta-state CRDTs achieve up to 24x bandwidth reduction over full-state sync. Merkle trees provide O(log N) state comparison for anti-entropy repair between cells.

### 1.5.5 Technology Evaluation Summary

| Technology | Role | Max Scale | Latency | Maturity | Integration |
|---|---|---|---|---|---|
| **Karmada** | Cluster lifecycle, workload placement | 100+ clusters, 500K nodes | 512ms scheduler P99 | CNCF, production-ready | PropagationPolicy + OverridePolicy |
| **Cilium Cluster Mesh** | Pod-to-pod connectivity, policies | 255-511 clusters | 0.5-1ms p99 eBPF | CNCF, production-ready | Requires Cilium CNI |
| **ArgoCD ApplicationSets** | GitOps app distribution | Unlimited | ~5 min deploy | CNCF Graduated | Cluster generator auto-discovers |
| **OCM (alternative)** | Governance hub-spoke | 100+ clusters | Hub-spoke dependent | CNCF, active | Stronger policy than Karmada |

These technologies are composable. A minimal federation requires only Cilium Cluster Mesh — cells discover each other via the mesh without higher-level coordination. Adding Karmada enables intelligent placement. Adding ArgoCD enables GitOps. For governance-heavy environments, OCM complements or substitutes for Karmada. This composability ensures operators start simple and add capability incrementally.

---

*This chapter established HelixCluster's architectural foundations: the cell-based hierarchy validated by Google's Borg at 10,000 machines per cell; five topology patterns from flat federation to partial mesh; four binding modes from loosely-coupled cluster-of-clusters to tightly-integrated equal-peer; a ten-state lifecycle machine with Go implementation; and an evaluation of Karmada, Cilium Cluster Mesh, and ArgoCD confirming production readiness at 100-255 cell scale. Chapter 2 examines the network mesh and connectivity layer — the WireGuard tunnels, NAT traversal, and Cilium Cluster Mesh implementation that transform logical topology into packet flows between cells.*
