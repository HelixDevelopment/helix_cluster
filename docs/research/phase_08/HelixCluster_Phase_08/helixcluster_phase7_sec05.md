## 5. Cache & Session: Redis Cluster, Hazelcast

GPU cloud platforms live or die by their caching layer. When a researcher reconnects to a tmux session hosting a 48-hour training run, the session metadata must resolve in sub-millisecond time. When a node fails, that session must migrate to a healthy GPU-equipped node without losing scrollback buffer, environment state, or attached processes. When a thousand researchers simultaneously checkpoint their models, the cache must absorb the thundering herd without collapsing the persistence backend.

This chapter dissects how Redis Cluster, Hazelcast, Dragonfly, and KeyDB solve these exact problems. We examine hash-slot partitioning, two-phase failure detection, atomic migration, Raft-based consistency, and multi-threaded vertical scaling—all through the lens of what HelixCluster must implement to handle session-heavy GPU workloads at scale.

### 5.1 Redis Cluster

Redis Cluster is the default answer for distributed caching in production, not because it is perfect, but because its design represents a pragmatic equilibrium: 16,384 hash slots, gossip-based membership, automatic failover, and (as of Redis 8.4) Atomic Slot Migration that makes resharding 30x faster. These patterns map directly to HelixCluster's session routing and migration requirements.

#### 5.1.1 16,384 Hash Slots: CRC16 Routing, Cluster Bus Gossip

Redis Cluster partitions the keyspace into **16,384 hash slots** (2^14). This number is not arbitrary: the slot bitmap consumes exactly **2,048 bytes** (16384 bits), making every gossip heartbeat compact enough to broadcast every 100 milliseconds without saturating network links, while providing fine enough granularity to distribute data evenly across up to 1,000 nodes [^505^]. The hash function uses CRC16 masked to 14 bits:

```go
package router

import "hash/crc16"

const ClusterSlots = 16384

// SlotRouter maps session IDs to hash slots using Redis Cluster's
// CRC16 algorithm. HelixCluster adapts this for GPU session routing.
type SlotRouter struct {
    // slotToNode maps each of the 16384 slots to a responsible node.
    // Client-side caching of this map avoids round-trips.
    slotToNode [ClusterSlots]*NodeInfo

    // epoch tracks the config epoch for detecting stale slot maps.
    epoch uint64
}

type NodeInfo struct {
    NodeID string
    Addr   string
    Healthy bool
}

// ComputeSlot returns the hash slot for a session key.
// Hash tags in {...} force related keys to the same slot,
// enabling multi-key operations on colocated sessions.
func (r *SlotRouter) ComputeSlot(key string) uint16 {
    tag := extractHashTag(key)
    return crc16.ChecksumCCITT([]byte(tag)) & 0x3FFF
}

// extractHashTag finds the substring between { and }.
// If no valid tag exists, the full key is used.
func extractHashTag(key string) string {
    start := -1
    for i := 0; i < len(key); i++ {
        if key[i] == '{' {
            start = i + 1
            break
        }
    }
    if start < 0 {
        return key // No '{': hash the entire key
    }
    for i := start; i < len(key); i++ {
        if key[i] == '}' {
            if i == start {
                return key // Empty tag: hash the entire key
            }
            return key[start:i]
        }
    }
    return key // No closing '}': hash the entire key
}

// Route determines which node owns a given session.
// If the slot cache is stale, it returns MOVED to trigger a refresh.
func (r *SlotRouter) Route(sessionID string) (*NodeInfo, uint16, error) {
    slot := r.ComputeSlot(sessionID)
    node := r.slotToNode[slot]
    if node == nil || !node.Healthy {
        return nil, slot, ErrMoved{Slot: slot}
    }
    return node, slot, nil
}
```

**Cluster Bus Gossip.** Nodes communicate via a dedicated TCP binary protocol (client port + 10,000). Every node maintains a full mesh of N-1 connections to every other node. The gossip protocol carries PING heartbeats every 100 ms, each containing up to one-tenth of known node addresses plus the 2 KB slot bitmap. Information therefore propagates in **O(log N)** rounds rather than linear flooding [^3299^]. For HelixCluster, this design translates to a lightweight session-location gossip protocol where each node periodically shares its view of which sessions it hosts, enabling any node to route a session request to the correct host.

#### 5.1.2 Two-Phase Failure Detection: PFAIL → FAIL with Majority-Master Consensus

Redis Cluster's failure detector operates in two phases, deliberately trading speed for accuracy to avoid false failovers during transient network hiccups.

**Phase 1: PFAIL (Possible Failure).** Node A marks Node B as `PFAIL` when `cluster-node-timeout` (default 15 seconds) elapses without a PONG response. Both masters and replicas can flag nodes as PFAIL. Nodes proactively attempt reconnection at half the timeout to prevent false positives from asymmetric partitions [^505^].

**Phase 2: FAIL (Confirmed Failure).** A PFAIL flag escalates to FAIL only when a **majority of masters** in the cluster independently report the same node as PFAIL within `2 * NODE_TIMEOUT`. The node that first observes the majority broadcasts a `FAIL` message to all reachable peers, forcing immediate state update rather than gradual gossip convergence [^505^].

The following Go code implements the core two-phase logic:

```go
package failure

import (
    "context"
    "sync"
    "time"
)

const (
    NodeTimeout        = 15 * time.Second
    FailReportValidity = 2
)

type NodeState uint8

const (
    NodeHealthy NodeState = iota
    NodePFail             // Phase 1: possible failure detected locally
    NodeFail              // Phase 2: majority-masters confirmed failure
)

type FailureDetector struct {
    mu       sync.RWMutex
    nodes    map[string]*Node   // all known nodes
    masters  map[string]*Node   // master subset for quorum
    failures map[string]time.Time // when each FAIL was declared
}

type Node struct {
    ID        string
    Addr      string
    IsMaster  bool
    State     NodeState
    LastPong  time.Time
    PFailFrom map[string]bool // which masters reported this node as PFAIL
}

// OnHeartbeatTimeout triggers Phase 1: mark node as PFAIL.
func (fd *FailureDetector) OnHeartbeatTimeout(ctx context.Context, nodeID string) {
    fd.mu.Lock()
    defer fd.mu.Unlock()

    node := fd.nodes[nodeID]
    if node == nil || node.State >= NodePFail {
        return
    }
    node.State = NodePFail
    go fd.gossipPFail(ctx, nodeID)
}

// ProcessPFailReport handles incoming PFAIL gossip from another master.
func (fd *FailureDetector) ProcessPFailReport(fromNodeID, failedNodeID string) {
    fd.mu.Lock()
    defer fd.mu.Unlock()

    reporter := fd.nodes[fromNodeID]
    failed := fd.nodes[failedNodeID]
    if reporter == nil || failed == nil || !reporter.IsMaster {
        return // Only master reports count toward quorum
    }
    failed.PFailFrom[fromNodeID] = true

    // Phase 2: check if majority of masters reported PFAIL.
    masterCount := len(fd.masters)
    pfailCount := 0
    for mid := range fd.masters {
        if failed.PFailFrom[mid] {
            pfailCount++
        }
    }
    if pfailCount > masterCount/2 && failed.State < NodeFail {
        failed.State = NodeFail
        fd.failures[failedNodeID] = time.Now()
        go fd.broadcastFail(failedNodeID)
    }
}

// ProcessFailMessage handles a FAIL broadcast from another node.
// FAIL messages force immediate state update, bypassing gossip.
func (fd *FailureDetector) ProcessFailMessage(nodeID string) {
    fd.mu.Lock()
    defer fd.mu.Unlock()
    if node := fd.nodes[nodeID]; node != nil && node.State < NodeFail {
        node.State = NodeFail
        fd.failures[nodeID] = time.Now()
    }
}

// broadcastFail sends FAIL to all reachable nodes and triggers
// session migration for the failed node's sessions.
func (fd *FailureDetector) broadcastFail(nodeID string) {
    // Broadcast to all nodes; initiate failover for each slot
    // hosted by the failed master.
}
```

**Replica promotion.** Once a master is declared FAIL, its replicas race to be elected. Each replica increments the cluster's `currentEpoch`, broadcasts a `FAILOVER_AUTH_REQUEST`, and collects votes (`FAILOVER_AUTH_ACK`) from masters. A replica needs a majority of master votes within `2 * NODE_TIMEOUT` (minimum 2 seconds). The winning replica promotes itself and claims the failed master's slots with a new `configEpoch` [^505^].

#### 5.1.3 Atomic Slot Migration (ASM): Snapshot + Live Replication + Atomic Transfer

Before Redis 8.4, resharding was agonizingly slow: `CLUSTER GETKEYSINSLOT` fetched keys one by one, `MIGRATE` moved them individually, and ASK redirects broke client pipelines. A typical resharding operation took **192–219 seconds**, generated **241.6 MOVED redirects per second**, and caused latency spikes to 127 ms [^3271^].

Redis 8.4's **Atomic Slot Migration (ASM)** reimagines the process as a replication problem rather than a key-by-key copy:

1. **Destination initiates:** `CLUSTER MIGRATION IMPORT <slot-range>` prepares the target node.
2. **Source forks** a background process to capture a point-in-time snapshot of the slot.
3. **Snapshot + live replication** stream in parallel: the source streams the snapshot while simultaneously buffering live writes to a replication backlog.
4. **Replication lag threshold:** When lag drops below a configurable threshold, the source briefly pauses writes (typically sub-second).
5. **Atomic handoff:** Ownership transfers to the destination in a single metadata operation.
6. **Asynchronous cleanup:** The source trims old data in a background thread, with no client-visible disruption [^3271^].

The results are dramatic: **6–8 seconds** instead of 192–219 (30x faster), **2.1 MOVED/sec** instead of 241.6 (98% less disruption), **<70 ms** peak latency instead of 127 ms (73% lower), and **212 messages** instead of 5,400 (94% less network overhead) [^3271^].

```
ASM Migration Sequence
----------------------
Phase 1: IMPORT
  [Destination] CLUSTER MIGRATION IMPORT slots [100-200]

Phase 2: SNAPSHOT
  [Source] Fork background process
  [Source] Serialize slot range to binary snapshot
  [Source] Begin streaming snapshot to destination

Phase 3: LIVE REPLICATION
  [Source] Buffer all writes to slots [100-200] in backlog
  [Source] Stream snapshot chunks + live updates to destination
  [Destination] Apply snapshot, then apply live updates

Phase 4: PAUSE & HANDOFF
  [Source] Brief write pause (< 1 second)
  [Source] Drain final updates from backlog
  [Destination] Apply final delta atomically
  [Cluster] Update slot ownership bitmap in single transaction

Phase 5: CLEANUP
  [Source] Trim migrated data asynchronously
  [Destination] Begin serving client requests for new slots
```

For HelixCluster, this pattern maps directly to **atomic session migration**: capture a tmux session's snapshot (scrollback buffer, environment variables, process state), stream it while the session remains live, then atomically hand off routing. The "30x faster" principle means a session can migrate from a failing GPU node to a healthy one in sub-10-second timeframes rather than minutes.

#### 5.1.4 Config Epoch: Conflict Resolution for Simultaneous Failovers

Every master in Redis Cluster maintains a monotonic **config epoch**, incremented on slot ownership changes. If two replicas simultaneously promote themselves for the same failed master—a rare but possible event during network partitions—they may end with the same config epoch. Redis resolves this deterministically: **the node with the lexicographically smaller Node ID auto-increments its epoch**, yielding a strict ordering without human intervention [^505^]. This elegant conflict resolution ensures the cluster converges to a single source of truth for every slot, a pattern HelixCluster adopts for session ownership disputes.

### 5.2 Hazelcast

Where Redis Cluster prioritizes availability and performance, Hazelcast offers a **CP subsystem** providing linearizable consistency through the Raft consensus algorithm. This is critical for HelixCluster components requiring strong guarantees: distributed locks for GPU allocation, atomic counters for job scheduling, and consistent session state during leader elections.

#### 5.2.1 CP Subsystem: Raft-Based FencedLock, AtomicReference

Hazelcast's CP Subsystem partitions data structures across **CP groups**, each a separate Raft cluster of 3–7 members. Operations within a CP group are linearizable, and during network partitions the minority side loses availability—a deliberate design choice for correctness [^3294^].

Key CP data structures include:

| CP Structure | Purpose | HelixCluster Mapping |
|-------------|---------|---------------------|
| `FencedLock` | Distributed lock with monotonic fencing token | GPU allocation lock preventing double-assignment |
| `IAtomicLong` | Atomic counter across all CP members | Global job ID sequencer, task counter |
| `IAtomicReference` | Atomic reference with compare-and-set | Session routing table pointer swap |
| `CPMap` | Consistent key-value map | Critical session metadata (auth state, GPU binding) |

The **fencing token** pattern is particularly important. When a client acquires a `FencedLock`, it receives a monotonically increasing token. Every subsequent operation includes this token; if a stale lock holder (whose network partition healed) attempts an operation, its outdated token is rejected. This prevents the split-brain scenarios that plague weaker locking systems [^3273^].

Hazelcast's default **AP subsystem** provides eventual consistency with high availability, suitable for caching and session management where brief staleness is acceptable. HelixCluster's hybrid approach uses the AP subsystem for general session caching and the CP subsystem for GPU allocation decisions and migration coordination.

#### 5.2.2 WAN Replication: Cross-Datacenter Sync

Hazelcast's **WAN replication** replicates map and cache data across geographically distributed clusters, targeting a Recovery Point Objective (RPO) of zero with asynchronous replication [^3294^]. While not used for synchronous session migration, this pattern informs HelixCluster's cross-zone GPU session backup strategy: asynchronous replication of session metadata to a standby zone, with manual failover during zone outages.

### 5.3 Dragonfly/KeyDB

#### 5.3.1 Multi-Threaded: 25x Throughput, Dashtable 30% Less Memory

Redis Cluster scales horizontally by adding nodes, but single-node throughput is bottlenecked by its single-threaded event loop (~200K SET, ~250K GET ops/sec) [^3276^]. Dragonfly and KeyDB attack this problem through vertical scaling: multi-threaded architectures that exploit modern many-core servers.

| System | Architecture | Throughput (SET) | Throughput (GET) | Memory (1B keys) | Consistency Model |
|--------|-------------|------------------|------------------|------------------|-------------------|
| Redis 7 OSS | Single-threaded + IO threads | ~200K ops/sec | ~250K ops/sec | ~185 GB | Eventual (AP) |
| Redis Enterprise | NUMA-tuned, multi-process | ~5M ops/sec | ~5M ops/sec | ~150 GB | Eventual (AP) |
| Dragonfly | Shared-nothing multi-thread | ~4M ops/sec | ~5M ops/sec | ~120 GB | Single-node strong |
| KeyDB | Multi-threaded fork | ~1M ops/sec | ~1.2M ops/sec | ~160 GB | Active replication |
| Valkey | Async I/O threading | ~1M ops/sec | ~1M ops/sec | ~150 GB | Eventual (AP) |

Dragonfly achieves **20x higher throughput** than Redis OSS by using a **shared-nothing, multi-threaded architecture** where each thread owns a subset of keys. Its **Dashtable** data structure uses **~30% less memory** than Redis's hash table by employing a two-level design: a dense array of small buckets for hot entries and a sparse secondary table for overflows, eliminating the pointer-chasing overhead of traditional chaining [^3276^]. Dragonfly also avoids `fork()` for snapshots, using incremental background serialization instead—critical for systems where `fork()` latency would stall the event loop.

KeyDB, a multi-threaded Redis fork, adds **active replication** (multi-master) and **FLASH storage backend** for datasets exceeding RAM. Valkey (the Linux Foundation fork from Redis 7.2.4) introduces **Async I/O Threading**, achieving 1M+ requests/sec on 8-vCPU instances [^3347^].

For HelixCluster, these systems inform the **node-local caching layer**: use multi-threaded caching (similar to Dragonfly's thread-local shards) on each GPU node to maximize session data throughput without adding network hops.

### 5.4 Session Management Patterns

#### 5.4.1 Sticky Sessions for GPU Workloads

GPU workloads exhibit inherent session affinity. A tmux session attached to GPU #3 on Node A cannot arbitrarily move to Node B's GPU #7 without losing device context, CUDA state, and in-progress computation. **Sticky sessions**—routing all requests for a given session to the same node—are therefore not merely an optimization but a requirement.

However, sticky sessions create a fault-tolerance problem: if Node A fails, sessions on Node A are lost unless replicated. The solution is a **hybrid sticky-distributed** pattern: route sticky to the owning node while asynchronously replicating session state to one or more standby nodes. On failure, the session migrates to a node with equivalent GPU capacity using the ASM-style atomic handoff pattern.

#### 5.4.2 Distributed Sessions: JWT + Cache-Side State

For state that transcends a single GPU session—authentication tokens, user preferences, job queue positions—distributed sessions are appropriate. The recommended pattern combines **JWT (JSON Web Tokens)** for client-carried session identity with **cache-side state** for server-side session data:

| Pattern | Routing | State Storage | Failover Behavior | Latency | Best For |
|---------|---------|--------------|-------------------|---------|----------|
| Sticky Session | Hash-based to owning node | Node-local cache | Session lost without replication | Sub-ms lookup | GPU-attached tmux sessions |
| Sticky + Replication | Hash-based to primary | Node-local + async replica | Failover to replica, possible data loss | Sub-ms primary, ~1ms replica | Production GPU workloads |
| Distributed JWT | Any node via JWT validation | Central cache (Redis) | Automatic, no data loss | ~1-2ms cache round-trip | Auth tokens, user profiles |
| Distributed + Sticky | JWT validated, then routed to GPU node | Hybrid: local GPU state + central metadata | Graceful degradation to local | Sub-ms after validation | HelixCluster default |

Facebook's cache research [^3322^] provides critical guidance for distributed session implementations: use `delete` (not `set`) on writes to avoid stale-set races under concurrency; always set TTL as a blast-radius cap on invalidation bugs; monitor hit rate as a first-class Service Level Indicator; and recognize that a 1% hit-rate drop from 99% to 98% doubles database load.

For HelixCluster, the default pattern is **Distributed + Sticky**: JWT authentication at the edge, then sticky routing to the GPU node owning the session. Session metadata (GPU assignment, job state, heartbeat timestamps) resides in a central distributed cache using the hash-slot router. The actual GPU session state (tmux scrollback, environment) remains node-local, replicated asynchronously for failover.

### 5.5 Cache Lessons for HelixCluster

#### 5.5.1 Hash Slot Router: CRC16 mod 16384

HelixCluster adopts Redis Cluster's hash slot model for workload distribution. Every session is mapped to one of 16,384 slots via CRC16. Slots are assigned to GPU nodes, and the assignment is cached client-side to avoid lookup overhead on every request. When cluster topology changes (node added, node removed, slot rebalanced), the slot cache invalidates and refreshes—similar to Redis's MOVED/ASK redirection handling.

The Go implementation in Section 5.1.1 demonstrates the core routing logic: `ComputeSlot` uses CRC16-CCITT masked to 14 bits, hash tags force related keys (e.g., a user's sessions) to colocate on the same node, and client-side slot caching eliminates network round-trips for the common case.

#### 5.5.2 Atomic Session Migration: ASM-Style Sub-10-Second

HelixCluster's session migration engine adapts Redis 8.4's ASM pattern to GPU workload handoff:

1. **Capture snapshot:** Serialize the tmux session state (scrollback buffer, environment variables, attached process metadata).
2. **Open replication stream:** While the snapshot transfers, buffer all new session output to a delta queue.
3. **Apply snapshot:** The destination node reconstructs the session from the snapshot.
4. **Catch up replication:** Stream buffered deltas to bring the destination to near-real-time.
5. **Atomic handoff:** Briefly pause the source session (<1 second), drain final deltas, atomically update the routing table to point to the destination, and resume.
6. **Cleanup:** The source asynchronously removes old session data.

This sequence achieves **sub-10-second migration** with minimal client disruption—critical when a GPU node fails mid-training and the researcher must reconnect seamlessly.

#### 5.5.3 Tiered Cache: Hot/Warm/Cold Data Tiers

HelixCluster implements a three-tier cache hierarchy informed by the systems in this chapter:

| Tier | Technology | Data | Latency | Consistency | Eviction |
|------|-----------|------|---------|-------------|----------|
| L1 Hot | Node-local Caffeine/Dragonfly-style | Active tmux sessions, GPU bindings | Sub-ms | Strong (single node) | LRU, size-bound |
| L2 Warm | Distributed slot-based cache (Redis-style) | Session metadata, routing table, heartbeats | ~1ms | Eventual (AP) | TTL + LRU |
| L3 Cold | Persistent log (AOF-style) | Session history, audit trail, post-mortem data | ~10ms | Strong (fsync) | Append-only compaction |

The **L1 hot tier** holds data for sessions actively running on the node's GPUs. This tier uses a multi-threaded cache similar to Dragonfly's Dashtable for maximum throughput. The **L2 warm tier** distributes session metadata across the cluster using the 16,384-slot router, with gossip-based topology propagation and automatic rebalancing on node changes. The **L3 cold tier** provides durability through an append-only log, enabling session replay and forensic analysis without impacting hot-path performance.

**Failure detection** adopts Redis Cluster's PFAIL/FAIL two-phase mechanism (Section 5.1.2) with HelixCluster-specific adaptations: GPU health metrics (temperature, memory errors, utilization) feed into the heartbeat timeout calculation, so a node with failing GPUs is flagged for migration earlier than a healthy node with merely slow network responses.

**Conflict resolution** uses config epochs with lexicographic node ID tiebreaking, exactly as Redis Cluster does. When two nodes simultaneously claim ownership of a session slot, the higher-epoch node wins; if epochs collide, the smaller node ID auto-increments and retries. This deterministic resolution prevents the split-brain writes that would corrupt GPU state.

Together, these patterns—hash slot routing, two-phase failure detection, atomic session migration, and tiered caching—form the foundation of HelixCluster's session management architecture. They are proven in production at scale (Redis Cluster handles millions of operations per second; Hazelcast's CP subsystem passes Jepsen linearizability tests) and directly adapted to the unique demands of GPU workload orchestration.
