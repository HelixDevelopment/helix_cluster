# Dimension 5: In-Memory & Cache Clusters (Redis, Memcached, Hazelcast)

## Executive Summary

This document analyzes production-grade in-memory clustering and caching systems to extract architectural patterns, failure modes, performance characteristics, and operational lessons for HelixCluster's session management and caching layer. We examine Redis Cluster (hash slots, gossip protocol, atomic slot migration), Memcached (slab allocation, consistent hashing), Hazelcast (CP/AP subsystems, Raft-based consistency), Apache Ignite (memory-first data regions), Dragonfly/KeyDB (multi-threaded vertical scaling), and Netflix EVCache (cross-region replication at 400M ops/sec). Every finding maps to a concrete HelixCluster improvement.

---

## 1. Redis Cluster Architecture

### 1.1 Hash Slot Model (16,384 Slots)

Redis Cluster partitions the keyspace into **16,384 hash slots** (2^14), a number chosen as a pragmatic balance: the slot bitmap fits in **2KB of memory** (2048 bytes), making gossip messages compact, while being large enough to accommodate clusters of up to 1,000 nodes with fine-grained data distribution [^505^].

**Source Code - `src/cluster.h`:**

```c
#define CLUSTER_SLOT_MASK_BITS 14 /* Number of bits used for slot id. */
#define CLUSTER_SLOTS (1<<CLUSTER_SLOT_MASK_BITS) /* Total: 16384 */
#define CLUSTER_SLOT_MASK ((unsigned long long)(CLUSTER_SLOTS - 1))

/* Key hash slot computation using CRC16 */
static inline unsigned int keyHashSlot(const char *key, int keylen) {
    int s, e; /* start-end indexes of { and } */
    for (s = 0; s < keylen; s++)
        if (key[s] == '{') break;
    /* No '{' ? Hash the whole key. */
    if (likely(s == keylen)) return crc16(key,keylen) & 0x3FFF;
    /* '{' found? Check for corresponding '}'. */
    for (e = s+1; e < keylen; e++)
        if (key[e] == '}') break;
    /* No '}' or nothing between {} ? Hash the whole key. */
    if (e == keylen || e == s+1) return crc16(key,keylen) & 0x3FFF;
    /* Hash what is between { and }. */
    return crc16(key+s+1,e-s-1) & 0x3FFF;
}
```

**Source Code - `src/crc16.c`:**

```c
uint16_t crc16(const char *buf, int len) {
    int counter;
    uint16_t crc = 0;
    for (counter = 0; counter < len; counter++)
        crc = (crc<<8) ^ crc16tab[((crc>>8) ^ *buf++)&0x00FF];
    return crc;
}
```

The hash slot formula is: `HASH_SLOT = CRC16(key) & 0x3FFF` (equivalent to modulo 16384). The `{...}` hash tag feature forces related keys into the same slot, enabling multi-key operations like `MGET` and Lua scripts on colocated data [^505^].

### 1.2 Cluster Bus: Binary Protocol Over TCP

Redis Cluster nodes communicate via a separate TCP port (client port + 10000) using a **binary protocol** called the Cluster Bus. Every node maintains a full mesh of N-1 outgoing and N-1 incoming connections [^505^].

**Message types on the Cluster Bus:**

| Message Type | Purpose |
|-------------|---------|
| `PING` | Heartbeat with gossip payload (sent every 100ms) |
| `PONG` | Response to PING with sender's cluster view |
| `MEET` | Introduce new node to cluster |
| `FAIL` | Broadcast confirmed node failure immediately |
| `UPDATE` | Propagate config epoch changes post-failover |
| `MFSTART` | Manual failover coordination |
| `PUBLISH` | Pub/Sub message routing |

**Gossip packet structure:**

```
Header (fixed):
  - Sender node ID (40 bytes)
  - Current config epoch (8 bytes)
  - Slot bitmap (2048 bytes = 16384 bits)
  - Sender IP/port (variable)

Gossip section (variable):
  - Up to 1/10th of known nodes
  - Per node: ID, IP, port, flags, ping/pong timestamps
```

The 2KB slot bitmap is why 16384 slots was chosen over 65536 (which would require 8KB) [^3266^]. Information propagates in **O(log N)** rounds via gossip, with FAIL messages broadcast directly for rapid convergence [^3299^].

### 1.3 Failure Detection: PFAIL -> FAIL

Redis Cluster uses a two-phase failure detection mechanism [^505^]:

**Phase 1 - PFAIL (Possible Failure):**
- Node A marks Node B as `PFAIL` if no PONG is received within `cluster-node-timeout` (default 15s)
- Both masters and replicas can flag nodes as PFAIL
- Nodes attempt reconnection at half the timeout to prevent false positives

**Phase 2 - FAIL (Confirmed Failure):**
- PFAIL escalates to FAIL when a majority of masters report PFAIL within `NODE_TIMEOUT * 2`
- The detecting node broadcasts a `FAIL` message to all reachable nodes
- FAIL messages force immediate state update (not gossiped slowly)

**Replica election and promotion** [^505^]:
1. Replica increments `currentEpoch` and broadcasts `FAILOVER_AUTH_REQUEST`
2. Masters vote with `FAILOVER_AUTH_ACK` (one vote per master per `NODE_TIMEOUT * 2` period)
3. Replica needs majority of master votes within `2 * NODE_TIMEOUT` (min 2 seconds)
4. Winning replica promotes itself, claims master's slots with new `configEpoch`

**Config epoch conflict resolution:** If two nodes end with the same `configEpoch`, the node with the lexicographically smaller Node ID auto-increments its epoch, ensuring all masters eventually have unique epochs [^505^].

### 1.4 Atomic Slot Migration (ASM) - Redis 8.4

Redis 8.4 introduced **Atomic Slot Migration (ASM)**, a game-changing improvement [^3271^]:

**Legacy migration (pre-8.4):**
- Key-by-key migration using `CLUSTER GETKEYSINSLOT` + `MIGRATE`
- Generated ASK redirects, broke pipelines, caused TRYAGAIN errors
- 192-219 seconds for typical resharding, 241.6 MOVED redirects/sec

**ASM (Redis 8.4+):**
- Replicates entire slot content like a full-sync (snapshot + live delta)
- Single atomic handoff of ownership at the end
- **30x faster**: 6-8 seconds vs 192-219 seconds
- **98% less disruption**: 2.1 MOVED/sec vs 241.6 MOVED/sec
- **73% lower latency spikes**: <70ms vs 127ms peak
- **94% less network overhead**: 212 messages vs 5,400 messages [^3271^]

```
ASM Sequence:
1. Destination: CLUSTER MIGRATION IMPORT <slot-range>
2. Source forks, sends slot snapshot + replication stream in parallel
3. When replication lag < threshold, source pauses writes briefly
4. Destination takes over slot ownership atomically
5. Source trims old data asynchronously via background thread
```

### 1.5 Performance Characteristics

| Metric | Value |
|--------|-------|
| Max nodes | ~1,000 (recommended) |
| Max masters (theoretical) | 16,384 (one slot each) |
| Ops/sec per node (Redis OSS) | ~200K SET, ~250K GET [^3276^] |
| Ops/sec per node (Redis Enterprise) | ~5M per node (NUMA-tuned) [^3377^] |
| Cluster throughput (40 nodes) | 200M+ ops/sec [^3377^] |
| Failover time | Sub-30 seconds (configurable via down-after-milliseconds) |
| Slot bitmap size | 2KB per heartbeat |

---

## 2. Memcached Architecture

### 2.1 Slab Allocation Memory Management

Memcached uses a **slab allocation** system that divides memory into classes of fixed-size chunks [^3268^]:

```c
/* From memcached slabs.c */
typedef struct {
    unsigned int size;      /* sizes of items */
    unsigned int perslab;   /* how many items per slab */
    void *slots;            /* list of item ptrs */
    unsigned int sl_curr;   /* total free items in list */
    unsigned int slabs;     /* how many slabs allocated */
    void **slab_list;       /* array of slab pointers */
    unsigned int list_size; /* size of prev array */
    unsigned int killing;   /* index+1 of dying slab */
    size_t requested;       /* number of requested bytes */
} slabclass_t;
```

**Key characteristics:**
- **No persistence**: Pure memory cache, data lost on restart (by design)
- **No replication**: Single-node, client handles distribution
- **Extremely fast**: Sub-microsecond latency for simple operations
- **Client-side hashing**: Ketama consistent hashing with 160 virtual nodes per physical server [^3311^]

### 2.2 Netflix EVCache: Memcached at Scale

Netflix's EVCache demonstrates Memcached's scalability potential [^3372^]:

| Metric | Value |
|--------|-------|
| Regions | 4 (global deployment) |
| Clusters | ~200 Memcached clusters |
| Server instances | ~22,000 |
| Operations/sec | 400 million |
| Items stored | ~2 trillion |
| Total data | 14.3 petabytes |
| Replication events/sec | 30 million globally |

**EVCache cross-region replication architecture:**
1. Client writes mutation to local EVCache
2. Async metadata event (key, TTL, timestamp) sent to Kafka
3. Replication reader polls Kafka, fetches latest value locally
4. Cross-region REST call to writer service in destination
5. Writer stores in destination EVCache [^3372^]

**Key insight**: Only metadata (not values) flows through Kafka, avoiding large payloads. The reader fetches the actual value locally before cross-region transmission.

---

## 3. Hazelcast IMDG

### 3.1 CP Subsystem: Raft-Based Strong Consistency

Hazelcast's **CP Subsystem** provides strongly consistent distributed data structures using the **Raft consensus algorithm** [^3273^] [^3294^]:

**CP data structures:**
- `FencedLock` - Distributed lock with fencing tokens
- `IAtomicLong` - Atomic counter
- `IAtomicReference` - Atomic reference
- `ISemaphore` - Distributed semaphore
- `ICountDownLatch` - Distributed countdown latch
- `CPMap` - Consistent map

**Key properties:**
- **Linearizability**: All operations are linearizable
- **Split-brain protection**: During network partitions, minority side loses availability
- **CP groups**: Data structures are sharded across CP groups (Raft clusters)
- **Persistence**: CP state can be persisted to disk for fast recovery

**Hazelcast's AP subsystem** (default): Provides eventual consistency, prefers availability during partitions - suitable for caching and session management [^3294^].

### 3.2 Partition Table and Rebalancing

Hazelcast uses a **partition table** (default 271 partitions) distributed across cluster members. When members join or leave, partitions are rebalanced automatically. Hazelcast supports **Hot Restart** for fast recovery without data reload from external stores [^3378^].

---

## 4. Apache Ignite

Apache Ignite provides a **memory-first distributed SQL database** with multi-tier storage [^3281^] [^3285^]:

```
Ignite Cluster
├── HotRegion (in-memory only)
│   └── live_sessions_cache
├── PersistentRegion (RAM + Disk)
│   └── orders_cache (durability + speed)
└── AnalyticalRegion (FIFO eviction)
    └── events_cache
```

| Storage Engine | Persistence | Use Case |
|---------------|-------------|----------|
| `aimem` | No | Session cache, microsecond latency |
| `aipersist` | Yes (WAL) | Transactional data, low latency + durability |
| `rocksdb` | Disk | Historical data, cost-effective |

Ignite uses **Raft consensus** for ACID transactions across partitions and **MVCC snapshot isolation** for non-blocking reads [^3286^].

---

## 5. Dragonfly and KeyDB: Multi-Threaded Alternatives

### 5.1 Dragonfly

Dragonfly uses a **shared-nothing, multi-threaded architecture** achieving **25x higher throughput** than single-instance Redis [^3276^] [^3277^]:

| Benchmark | Redis 7 | Dragonfly | Improvement |
|-----------|---------|-----------|-------------|
| SET ops/sec | ~200K | ~4M | 20x |
| GET ops/sec | ~250K | ~5M | 20x |
| Memory (1B keys) | ~185GB | ~120GB | 35% less |

Dragonfly's **Dashtable** data structure uses ~30% less memory and avoids fork() for snapshots (incremental instead) [^3276^].

### 5.2 KeyDB

KeyDB is a multi-threaded fork of Redis with **active replication** (multi-master) and **FLASH storage** support for datasets exceeding RAM [^3282^] [^3291^]:

```conf
# KeyDB FLASH storage configuration
storage-provider flash /path/to/flash
```

### 5.3 Valkey

Valkey (Linux Foundation fork from Redis 7.2.4) adds **Async I/O Threading**, achieving **1M+ requests/sec** on 8-vCPU instances (3x improvement) [^3347^].

---

## 6. Session Management Patterns

### 6.1 Sticky Sessions vs. Distributed Sessions

| Aspect | Sticky Sessions | Distributed Sessions |
|--------|----------------|---------------------|
| Routing | Client always routed to same server | Any server can handle request |
| State | Stored on single server | Replicated/shared across cluster |
| Failover | Session lost on server failure | Session survives server failure |
| Complexity | Simple (load balancer cookie) | Requires cache/coordination |
| Latency | Zero lookup overhead | Network round-trip to session store |

**For HelixCluster's tmux sessions:** Sticky sessions with session migration on node failure is the optimal pattern. A tmux session is inherently tied to a specific node (the one hosting the GPU), so sticky routing is natural. The challenge is graceful session migration when a node fails.

### 6.2 Cache Patterns Comparison

| Pattern | Read Speed | Write Speed | Consistency | Best For |
|---------|-----------|-------------|-------------|----------|
| Cache-Aside | Fast hit / Slow miss | Medium | Eventual | General purpose (90% of workloads) |
| Write-Through | Fast | Slow (both paths) | Strong | Read-your-writes required |
| Write-Behind | Fast | Fastest | Eventual (risky) | High-throughput counters, telemetry |
| Read-Through | Fast hit / Slow miss | Medium | Eventual | CDN, ORM caching |

**Critical lessons from Facebook [^3322^]:**
- Use `delete` (not `set`) on write to avoid stale-set races under concurrency
- Always set TTL as a blast-radius cap on invalidation bugs
- A 1% hit-rate drop from 99% to 98% **doubles** database load
- Monitor hit rate as a first-class SLI

### 6.3 GPU Workload Session Affinity

GPU workloads have unique scheduling requirements [^3295^] [^3305^]:

**Gang Scheduling**: Distributed training jobs require all GPUs simultaneously. Without it, partial allocation causes deadlock. Kubernetes plugins like Volcano implement this via PodGroups.

**Topology-Aware Scheduling**: GPUs connected via NVLink achieve 600GB/s vs 32GB/s over PCIe. Poor topology causes 3-8x performance degradation.

**Bin Packing vs. Spreading**: Training jobs benefit from bin packing (cache locality, reduced network traffic), while inference favors spreading (fault tolerance, thermal management).

---

## 7. System Comparison Matrix

| Dimension | Redis Cluster | Memcached | Hazelcast | Apache Ignite | Dragonfly |
|-----------|--------------|-----------|-----------|--------------|-----------|
| **Partitioning** | 16,384 hash slots | Client-side Ketama | 271 partitions | Data regions | Thread-local shards |
| **Consistency** | Eventual (AP) | None (cache only) | CP (Raft) or AP | Strong (Raft) | Single-node strong |
| **Replication** | Master-replica | None (by design) | Sync/async | Async/sync | Master-replica |
| **Persistence** | RDB + AOF | None | Hot Restart | Native + WAL | Snapshots |
| **Cross-DC** | Redis Enterprise CRR | EVCache + Kafka | WAN replication | Not native | Not native |
| **Failover** | Automatic (sub-30s) | Client retry | Automatic | Automatic | Manual/replica |
| **Max throughput** | 5M ops/sec/node | 400M ops/sec (Netflix) | Millions | Millions | 4M+ ops/sec |
| **Best for** | General cache, sessions | Simple caching | Coordination, locks | SQL + transactions | Vertical scaling |

---

## 8. Failure Modes and Lessons Learned

### 8.1 Redis Cluster

**Split-brain during partition**: Redis Cluster uses majority-master consensus for FAIL declaration. Minority partitions enter `CLUSTERDOWN` state, preventing split-brain writes [^505^].

**Hot key problem**: A single popular key always hashes to the same slot, creating hotspots. Solutions: hash tags for colocation, read replicas, or client-side caching.

**Large key migration**: MIGRATE timeouts on large keys. Solution: ASM in Redis 8.4 uses chunked AOF-style format for large keys [^3271^].

### 8.2 Memcached/EVCache

**Slab imbalance**: Different-sized objects can leave some slab classes full while others have free memory. Solution: slab reassignment (Memcached 1.4.11+) and automove [^3268^].

**Cold start after restart**: All data lost on restart. Solution: EVCache uses cross-region replication to repopulate; consider persistent warm cache with Redis/RocksDB backend.

### 8.3 Hazelcast

**CP subsystem minority partition**: During network splits, minority-side CP operations block. This is by design for linearizability [^3294^].

**Jepsen testing of Redis-Raft**: Found 21 issues in Redis-Raft ranging from transient unavailability to complete data loss, highlighting the difficulty of building strongly consistent systems [^3288^].

---

## 9. What HelixCluster Should Adopt

### 9.1 Session Management Architecture

Based on our analysis, HelixCluster should implement a **hybrid session management** system:

```
┌─────────────────────────────────────────────────────────────┐
│                    HelixCluster Session Layer                │
├─────────────────────────────────────────────────────────────┤
│  L1: Node-local session cache (Caffeine/similar)            │
│      - Holds active tmux sessions for that node              │
│      - Sub-millisecond access, no network hop               │
│                                                             │
│  L2: Distributed session index (Redis Cluster or embedded)  │
│      - Maps session_id -> node_id (sticky routing)          │
│      - Session metadata (GPU affinity, workload type)       │
│      - Heartbeat timestamps for failure detection           │
│                                                             │
│  L3: Cross-node session replication                         │
│      - Async replication of session state for failover      │
│      - Kafka-based metadata propagation (EVCache pattern)   │
│                                                             │
│  L4: Persistent session log (optional durability)           │
│      - AOF-style append log for session history             │
│      - Enables post-mortem analysis and replay              │
└─────────────────────────────────────────────────────────────┘
```

### 9.2 GPU Workload Affinity

**Adopt gang scheduling pattern**: GPU training workloads require all requested GPUs simultaneously. HelixCluster should implement resource reservation that either allocates all requested GPUs or queues the entire job [^3305^].

**Topology-aware placement**: Track GPU interconnect topology (NVLink, PCIe) and place related workloads on GPUs with fast interconnects. A topology misstep causes 3-8x performance degradation [^3305^].

**Session-node affinity**: Each tmux session with GPU access is pinned to its node. The distributed session index routes all requests for that session to the correct node. On node failure, sessions are migrated to a node with equivalent GPU capacity.

### 9.3 Failure Detection

**Adopt Redis Cluster's PFAIL/FAIL mechanism** for HelixCluster node monitoring:

```python
# HelixCluster adaptation of Redis failure detection
class NodeFailureDetector:
    NODE_TIMEOUT = 15000  # ms, configurable
    FAIL_REPORT_VALIDITY_MULT = 2
    
    async def on_heartbeat_timeout(self, node_id: str):
        """Phase 1: Mark node as PFAIL"""
        self.nodes[node_id].flags |= PFAIL
        await self.gossip_failure(node_id)
    
    async def check_fail_upgrade(self, node_id: str):
        """Phase 2: Upgrade PFAIL to FAIL if majority confirms"""
        pfail_count = sum(1 for n in self.masters 
                         if n.flags & PFAIL and n.id == node_id)
        if pfail_count > len(self.masters) / 2:
            self.nodes[node_id].flags |= FAIL
            await self.broadcast_fail(node_id)
            await self.initiate_session_migration(node_id)
```

### 9.4 Slot-Based Resource Partitioning

**Adopt Redis Cluster's hash slot model** for workload distribution:

```python
# HelixCluster workload slot assignment
WORKLOAD_SLOTS = 16384  # Same as Redis for proven scalability

def workload_slot(session_id: str, gpu_id: str) -> int:
    """Compute the slot for a session-GPU pair."""
    key = f"{session_id}:{gpu_id}"
    return crc16(key.encode()) & 0x3FFF

class WorkloadRouter:
    """Routes sessions to nodes based on slot ownership."""
    
    def route(self, session_id: str, gpu_id: str) -> Node:
        slot = workload_slot(session_id, gpu_id)
        # Check local cache first (like Redis clients)
        if slot in self.slot_cache:
            node = self.slot_cache[slot]
            if node.is_healthy():
                return node
        # Refresh slot mapping from cluster
        self.refresh_slot_map()
        return self.slot_cache[slot]
```

### 9.5 Atomic Migration for Session Handoff

**Adopt Redis 8.4 ASM pattern** for seamless session migration:

```python
class SessionMigration:
    """Atomically migrate a session from source to destination node."""
    
    async def migrate_session(self, session_id: str, 
                             source: Node, dest: Node):
        # 1. Begin replication stream from source
        snapshot = await source.capture_session_snapshot(session_id)
        
        # 2. Stream live updates while copying snapshot
        replication_conn = await source.open_replication_stream(session_id)
        await dest.apply_snapshot(snapshot)
        
        # 3. Apply live delta
        await dest.catch_up_replication(replication_conn)
        
        # 4. Brief pause: atomically transfer ownership
        await source.pause_session(session_id, timeout_ms=1000)
        final_updates = await replication_conn.drain()
        await dest.apply_updates(final_updates)
        
        # 5. Update routing table atomically
        await self.update_slot_ownership(session_id, dest)
        
        # 6. Resume on destination
        await dest.resume_session(session_id)
        await source.cleanup_session(session_id)
```

### 9.6 Caching Strategy

**Use Cache-Aside as default** with hybrid patterns for specific needs:

| Data Type | Pattern | TTL | Invalidation |
|-----------|---------|-----|--------------|
| Session metadata | Cache-Aside | 1 hour | On session end |
| GPU assignment | Write-Through | None | Immediate on change |
| Workload metrics | Write-Behind | 5 min | Batch flush |
| Node health | Cache-Aside | 30 sec | Heartbeat-driven |
| Routing table | Write-Through | None | Atomic updates |

---

## 10. What HelixCluster Should Avoid

### Anti-Patterns Identified

1. **Don't use client-side consistent hashing alone** (Memcached pattern) for stateful sessions - it loses session data on node failure with no recovery path

2. **Don't allow split-brain writes** - If a node is partitioned, sessions on that node must not accept writes that could conflict when the partition heals. Use Redis's `CLUSTERDOWN` approach: enter read-only or unavailable state

3. **Don't migrate large sessions without chunking** - Redis ASM's chunked AOF-style format for large keys prevents timeouts. Similarly, large tmux scrollback buffers should be migrated in chunks

4. **Don't use write-behind for critical session state** - Write-behind can silently lose data on crash. Session authentication state and GPU assignments require write-through consistency

5. **Don't ignore topology for GPU placement** - A naive scheduler that ignores NVLink/PCIe topology causes 3-8x performance degradation for distributed training workloads

6. **Don't use a single epoch counter without conflict resolution** - Redis's config epoch conflict resolution (lexicographically smaller Node ID loses) is essential for convergence

7. **Don't forget the thundering herd problem** - When a popular session's cache entry expires, multiple concurrent requests can overwhelm the backend. Use lease-based refills (Facebook Memcache pattern)

---

## HelixCluster Impact

### Specific Improvements for Implementation

1. **Session Router Module**: Implement a hash-slot-based router (16384 slots) mapping session IDs to nodes using CRC16, with client-side slot caching and MOVED/ASK-style redirection handling

2. **Cluster Bus Protocol**: Implement a lightweight binary gossip protocol over TCP for inter-node communication, including heartbeat (PING/PONG), failure detection (PFAIL/FAIL), and configuration propagation

3. **Atomic Session Migration**: Implement ASM-style session handoff: snapshot + live replication + atomic ownership transfer + async cleanup, achieving sub-10-second migration with minimal client disruption

4. **Failure Detector**: Implement two-phase failure detection (PFAIL -> FAIL with majority consensus), with configurable `node-timeout` and automatic session failover to healthy replicas

5. **GPU Topology Manager**: Track GPU interconnect topology (NVLink, PCIe switches) and implement topology-aware session placement with gang scheduling for multi-GPU workloads

6. **Tiered Cache Layer**: Implement L1 (node-local Caffeine), L2 (distributed Redis-backed session index), L3 (cross-node async replication) cache hierarchy

7. **Epoch-Based Conflict Resolution**: Use monotonic config epochs for session ownership, with automatic conflict resolution via lexicographic node ID comparison

8. **Session Affinity Load Balancer**: Implement sticky session routing with cookie-based or consistent hashing approaches, with automatic rebalancing on cluster topology changes

9. **Monitoring & Observability**: Track cache hit rate as first-class SLI, session migration duration, failover time, and GPU utilization efficiency per topology placement

10. **Write-Through for Critical State**: Use write-through caching for session authentication and GPU assignment metadata; use cache-aside for session content and write-behind for metrics/telemetry

### Architecture Decision Records

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Partitioning model | 16384 hash slots | Proven at scale, compact bitmaps, fine-grained |
| Consistency model | Eventual (AP) with CP option | Matches session cache needs, CP for coordination |
| Failover mechanism | Majority-vote replica promotion | Redis-proven, sub-30s recovery |
| Migration protocol | ASM-style atomic handoff | 30x faster, 98% less disruption |
| GPU scheduling | Gang + topology-aware | Required for training workloads |
| Cache pattern | Cache-Aside hybrid | 90% default, write-through for critical data |
| Inter-node protocol | Binary gossip over TCP | O(log N) convergence, 2KB heartbeat |
| Conflict resolution | Config epoch + node ID | Automatic, no human intervention |
