# Phase 7, Dimension 2: Distributed Databases — Deep Research for HelixCluster Data Layer

> **Research Date**: 2025-06-17  
> **Systems Analyzed**: MySQL Cluster (NDB), PostgreSQL, CockroachDB, Apache Cassandra, TiDB/TiKV  
> **Searches Performed**: 24 independent web searches across architecture, source code, failure modes, and performance  
> **Word Count**: ~3,200+

---

## Table of Contents

1. [MySQL Cluster (NDB)](#1-mysql-cluster-ndb)
2. [PostgreSQL Clustering Ecosystem](#2-postgresql-clustering-ecosystem)
3. [CockroachDB (Primary Reference)](#3-cockroachdb-primary-reference)
4. [Apache Cassandra](#4-apache-cassandra)
5. [TiDB / TiKV](#5-tidb--tikv)
6. [CAP Theorem & Consistency Model Comparison](#6-cap-theorem--consistency-model-comparison)
7. [Architecture Comparison Matrix](#7-architecture-comparison-matrix)
8. [HelixCluster Impact](#8-helixcluster-impact)

---

## 1. MySQL Cluster (NDB)

### 1.1 Architecture Overview

MySQL NDB Cluster is the original telecom-grade distributed database, delivering **99.999% availability** (5 minutes downtime per year) through a shared-nothing architecture with three distinct node types [^1^]:

```
                    +------------------+
                    |  Management Node |  (ndb_mgmd)
                    |  Config + Arbiter|
                    +--------+---------+
                             |
              +--------------+--------------+
              |                             |
       +------v------+             +------v------+
       |  Data Node 1|             |  Data Node 2|  Node Group 0
       |  (ndbd/mtd) | <---------> |  (ndbd/mtd) |  (NoOfReplicas=2)
       +------+------+   2PC Sync  +------+------+
              |                             |
       +------v------+             +------v------+
       |  Data Node 3|             |  Data Node 4|  Node Group 1
       |  (ndbd/mtd) | <---------> |  (ndbd/mtd) |
       +------+------+             +------+------+
              |
    +---------+---------+
    |                   |
+---v----+        +----v---+
| SQL Node|        | SQL Node|  (mysqld with NDB engine)
+--------+        +--------+
```

**Key design decisions** [^2^]:

| Component | Responsibility | Failure Handling |
|-----------|---------------|------------------|
| **Management Node** (`ndb_mgmd`) | Cluster configuration, node arbitration, split-brain prevention | 2+ nodes for redundancy; does not store data |
| **Data Node** (`ndbd`/`ndbmtd`) | Stores partitioned data in-memory; synchronous replication within node groups | Up to 4 replicas; automatic failover within node group |
| **SQL Node** (`mysqld`) | SQL query interface; connects to data nodes for execution | Stateless; any SQL node can serve queries |

### 1.2 NDB Storage Engine & Synchronous Replication

NDB uses a **two-phase commit (2PC)** protocol for synchronous replication within node groups [^3^]:

```sql
-- NDB tables REQUIRE primary key (hash partitioning key)
CREATE TABLE sessions (
  session_id VARCHAR(64) NOT NULL PRIMARY KEY,
  user_id INT NOT NULL,
  data TEXT
) ENGINE = NDBCLUSTER;
```

Data is automatically partitioned using **hash-based sharding** on the primary key. Each partition is stored on one data node with replicas on others, controlled by `NoOfReplicas` (typically 2). All indexed data is held **in memory** with optional disk persistence for non-indexed columns [^4^].

**The 2PC replication flow**:
1. SQL node receives write, determines target data node by hash(partition key)
2. Transaction coordinator sends prepare to all replicas in the node group
3. All replicas write to REDO log and acknowledge prepare
4. Coordinator sends commit; all replicas apply and acknowledge
5. Transaction returns success to client

### 1.3 Performance Characteristics & Limitations

NDB excels at **sub-millisecond, primary-key lookups** but has significant limitations [^5^]:

| Strength | Weakness |
|----------|----------|
| Sub-ms latency for PK reads | Complex JOINs require cross-node network round-trips |
| Linear scalability for writes | All indexed data must fit in memory |
| 99.999% availability proven in telecom | Large transactions slow due to 2PC overhead |
| Automatic partitioning and failover | Limited MySQL feature compatibility |

### 1.4 Lessons for HelixCluster

- **Synchronous replication within failure domains** provides strong consistency but imposes latency costs — HelixCluster should use async replication across regions and sync within a zone
- **Hash-based data distribution** eliminates hot spots but makes range queries expensive — HelixCluster should use a hybrid distribution strategy
- **Dedicated management/arbitration nodes** prevent split-brain — HelixCluster should have a dedicated metadata/consensus layer

---

## 2. PostgreSQL Clustering Ecosystem

### 2.1 Streaming Replication: WAL-Level Internals

PostgreSQL streaming replication is based on **physical WAL (Write-Ahead Log) shipping** [^6^]. Every transaction generates WAL records that are replicated from primary to standby via three cooperative processes:

```
Primary Server                          Standby Server
+--------------+                        +--------------+
|  WAL Sender  | <----- TCP ----->      | WAL Receiver |
|  (walsender) |   streaming WAL        |(walreceiver) |
+--------------+                        +--------------+
     ^                                          |
     |  WAL records (16MB segments)             v
+--------------+                        +--------------+
|   pg_wal/    |                        |   pg_wal/    |
| 00000001...  |                        | 00000001...  |
+--------------+                        +--------------+
                                              |
                                        +-----v--------+
                                        | Startup Proc |
                                        | (replays WAL)|
                                        +--------------+
```

**Key WAL mechanisms** [^7^]:

- **LSN (Log Sequence Number)**: A 64-bit pointer identifying position in WAL (format: `0/169EC40`). Each data page tracks the LSN of the latest WAL record affecting it.
- **Crash Recovery**: PostgreSQL reads `pg_control` to find the last checkpoint, then replays WAL from `redo_lsn` forward. The `pd_lsn >= record LSN` check provides idempotency — pages already flushed are skipped.
- **Synchronous vs Async**: `synchronous_commit = remote_apply` guarantees zero data loss but adds round-trip latency. `synchronous_commit = off` maximizes throughput with minimal durability risk.

```sql
-- Monitor replication lag
SELECT pg_current_wal_lsn();  -- Current LSN on primary
SELECT pg_last_xact_replay_timestamp();  -- Last replay on standby
SELECT pg_wal_lsn_diff('0/22A6400', '0/22A62F0');  -- Bytes of WAL
```

### 2.2 Patroni: Python HA Template

Patroni is the **industry standard** for PostgreSQL high availability, used by GitLab, Zalando, and thousands of production deployments [^8^]:

```
                    etcd Cluster (3-5 nodes)
                    +-----+-----+-----+
                    | RAFT Consensus  |
                    +-----+-----+-----+
                          |
           +--------------+--------------+
           |              |              |
     +-----v-----+  +-----v-----+  +-----v-----+
     |  Patroni  |  |  Patroni  |  |  Patroni  |
     |   Agent   |  |   Agent   |  |   Agent   |
     +-----+-----+  +-----+-----+  +-----+-----+
     |  Primary  |  |  Standby  |  |  Standby  |
     | PostgreSQL|  | PostgreSQL|  | PostgreSQL|
     +-----------+  +-----------+  +-----------+
```

**Patroni design decisions** [^9^]:
- Uses etcd/ZooKeeper/Consul for distributed consensus (Raft internally)
- Only one primary at any time — acquires a leader lock in etcd
- Failover RTO: **20-30 seconds** (configurable via `ttl` and `loop_wait`)
- REST API for health checks; integrates with HAProxy/PgBouncer
- Automatic reinitialization of failed nodes from current primary

```python
# Patroni configuration pattern (YAML)
scope: my_cluster
namespace: /service/
name: node-1

etcd:
  hosts: etcd-1:2379,etcd-2:2379,etcd-3:2379

postgresql:
  listen: 0.0.0.0:5432
  data_dir: /var/lib/postgresql/data
  parameters:
    wal_level: replica
    max_wal_senders: 10
    synchronous_commit: "on"
```

### 2.3 Citus: PostgreSQL Extension for Sharding

Citus transforms PostgreSQL into a distributed database with a **coordinator + worker node** architecture [^10^]:

- **Coordinator node**: SQL parsing, query planning, routing, result aggregation
- **Worker nodes**: Store actual data shards (regular PostgreSQL tables)
- **Two sharding modes**: Row-based (hash on distribution column) and schema-based (each schema = shard)

```sql
-- Citus distributed table creation
SELECT create_distributed_table('orders', 'tenant_id');

-- Automatic query routing to correct shard
SELECT * FROM orders WHERE tenant_id = 123;  -- Single shard
SELECT COUNT(*) FROM orders;                   -- Parallel across all shards
```

### 2.4 Lessons for HelixCluster

- **WAL-based physical replication** is battle-tested and efficient — HelixCluster should implement a WAL/operation log for state machine replication
- **External consensus store (etcd) for leader election** eliminates split-brain — HelixCluster must use a separate consensus layer (Raft/etcd) for cluster coordination
- **Coordinator-worker query routing** pattern can scale reads — HelixCluster's query layer should route to the correct data shard

---

## 3. CockroachDB (Primary Reference)

### 3.1 Layered Architecture

CockroachDB is the most instructive system for HelixCluster. Its architecture consists of five layers [^11^]:

```
+----------------------------------------------------+
| SQL Layer: Parser -> Optimizer (cost-based) -> Exec |
+----------------------------------------------------+
| Transactional KV: Write intents, Timestamp cache,   |
| Transaction records, Concurrency mgr                |
+----------------------------------------------------+
| Distribution: Range partitioning, Span resolver,    |
| Leaseholder routing (64MB default range size)       |
+----------------------------------------------------+
| Replication: Multi-Raft consensus per range,        |
| Lease management, Snapshot transfer, Rebalancing    |
+----------------------------------------------------+
| Storage: Pebble (LSM-tree, RocksDB fork), MVCC,     |
| SSTable compression, Bloom filters                  |
+----------------------------------------------------+
```

**Key source code files** from the CockroachDB repository [^12^]:

| File | Purpose |
|------|---------|
| `pkg/kv/kvserver/replica_send.go` | Entry point for KV requests: `(*Replica).Send()` |
| `pkg/kv/kvserver/replica_write.go` | Write path: intent creation, timestamp cache |
| `pkg/kv/kvserver/replica_raft.go` | Raft integration: proposing commands, applying entries |
| `pkg/kv/kvserver/replica_range_lease.go` | Lease acquisition and management |
| `pkg/kv/kvserver/replica_proposal.go` | Command evaluation and replication proposal |
| `pkg/kv/kvserver/scheduler.go` | MultiRaft scheduler: batching Raft work |
| `pkg/kv/kvserver/store_send.go` | Store-level routing to correct replica |
| `pkg/kv/kvserver/tscache/` | Timestamp cache for conflict detection |

### 3.2 Multi-Raft: Why One Raft Group Per Range

CockroachDB's most important design decision is **Multi-Raft**: each 64MB range forms its own Raft consensus group [^13^]:

```
Traditional Single Raft:              CockroachDB Multi-Raft:
+----------+                          +----------+
|  Raft    |  All data               | MultiRaft|  Per-node coordinator
| (1 group)|                          | Manager  |
+----+-----+                          +----+-----+
     |                                     |
Node A B C                         +------+------+------+
(same log)                         | R1  | R2  | R3   | ... hundreds of
                                   |Raft |Raft |Raft |     ranges per node
                                   |Grp  |Grp  |Grp  |
                                   +-----+-----+-----+
```

**Why Multi-Raft instead of single Raft** [^14^]:

1. **Parallelism**: Independent ranges process reads/writes in parallel — no single consensus bottleneck
2. **Recovery granularity**: When a node fails, only affected ranges need re-replication, not the entire dataset
3. **Load balancing**: Leaseholders for different ranges can be on different nodes, spreading read load
4. **Heartbeat coalescing**: The MultiRaft manager batches heartbeats across all ranges between the same node pairs, keeping overhead constant regardless of range count
5. **Constant goroutines**: Only ~3 goroutines per store instead of one per range

```go
// Key CockroachDB source: replica_send.go
func (r *Replica) Send(ctx context.Context, ba *kvpb.BatchRequest) {
    // 1. Determine if read-only or read-write
    // 2. For writes: acquire latches, check timestamp cache
    // 3. Propose to Raft if mutation
    // 4. Wait for application, return result
}

// Key CockroachDB source: replica_raft.go
func (r *Replica) propose(ctx context.Context, p *ProposalData) {
    // 1. Encode write batch
    // 2. Submit to MultiRaft proposal buffer
    // 3. MultiRaft coalesces and sends to followers
    // 4. Wait for quorum acknowledgement
}
```

### 3.3 Serializable Transactions

CockroachDB defaults to **SERIALIZABLE isolation** — the strongest SQL isolation level. It implements this through a combination of techniques [^15^]:

1. **Write intents**: Uncommitted writes create "intents" (logical locks) on keys
2. **Timestamp cache**: Tracks recent read timestamps; writers check for conflicts
3. **Transaction records**: Stored in a dedicated key; tracks transaction state
4. **Parallel commit protocol**: Reduces commit latency from 2 Raft round-trips to 1

```sql
-- Transaction flow in CockroachDB
BEGIN;
-- Read: acquire read timestamp from gateway node
SELECT * FROM inventory WHERE product_id = 456;
-- Write: create write intent on leaseholder
UPDATE inventory SET quantity = quantity - 1 WHERE product_id = 456;
-- Commit: parallel commit across all touched ranges
COMMIT;
```

### 3.4 Closed Timestamps & Follower Reads

CockroachDB implements **closed timestamps** to enable follower reads [^16^]:

- The leaseholder periodically "closes" a timestamp (e.g., 2-3 seconds in the past)
- This is a **promise** that no new writes will be accepted at or below that timestamp
- Followers can serve reads at or below the closed timestamp without consulting the leaseholder
- This dramatically reduces read latency in geo-distributed deployments

```sql
-- Follower read using bounded staleness
SELECT code FROM postal_codes 
AS OF SYSTEM TIME with_max_staleness('10s') 
WHERE id = 5;

-- The gateway routes to the nearest replica, not the leaseholder
```

### 3.5 Survival Goals

CockroachDB lets databases declare **survival goals** [^17^]:

| Survival Goal | Replicas | Can Survive | Write Latency Impact |
|---------------|----------|-------------|---------------------|
| `ZONE FAILURE` (default) | 3 (1 per zone) | 1 zone failure | Minimal |
| `REGION FAILURE` | 5 (2+2+1 across regions) | 1 region failure | Adds cross-region RTT |

```sql
ALTER DATABASE myapp SURVIVE REGION FAILURE;
-- Automatically increases replication factor from 3 to 5
```

### 3.6 Jepsen Testing & "Causal Reverse" Anomaly

CockroachDB has been extensively tested by Jepsen. The key finding: CRDB provides **serializable isolation but NOT strict serializability** [^18^]:

The "causal reverse" anomaly occurs when:
1. Transaction T1 writes key A and commits
2. Transaction T2 writes key B and commits (after T1)
3. A concurrent read transaction sees T2's write but NOT T1's write

This happens because CRDB uses **Hybrid Logical Clocks (HLC)** for timestamp ordering, and disjoint transactions on different ranges may be ordered differently than wall-clock time. CockroachDB chose this tradeoff for performance — strict serializability would require waiting out clock uncertainty windows [^19^].

### 3.7 Self-Healing & Rebalancing

When a node fails, CockroachDB [^20^]:

1. Waits `server.time_until_store_dead` (default 5 min) before declaring node dead
2. Identifies under-replicated ranges
3. Up-replicates from existing replicas to healthy nodes via snapshots
4. Transfers leases away from failed node's ranges
5. Rebalances based on QPS and replica count when node returns

```
Node Failure Timeline:
T+0s     Node goes down
T+30s    Node marked "suspect" in UI
T+5min   Node declared "dead" (configurable to 1m15s min)
T+5m+1s  Replicate queue starts up-replication
T+varies All ranges re-replicated to target count
```

---

## 4. Apache Cassandra

### 4.1 Gossip Protocol & Phi Accrual Failure Detector

Cassandra uses a **peer-to-peer gossip protocol** for cluster membership and failure detection [^21^]:

```python
# Phi Accrual Failure Detector (simplified)
class PhiAccrualDetector:
    def phi(self, now: float) -> float:
        elapsed = now - self.last_heartbeat
        mean = statistics.mean(self.arrival_times)
        std = statistics.stdev(self.arrival_times)
        z = (elapsed - mean) / std
        # Tail probability of Gaussian
        prob_late = 1 - 0.5 * (1 + math.erf(z / math.sqrt(2)))
        return -math.log10(max(prob_late, 1e-300))
```

**Gossip mechanics**:
- Every second, each node initiates gossip with 1-3 random peers
- Exchanges **EndpointState**: node ID, status, load, schema version, tokens
- New nodes bootstrap via **seed nodes** (static entry points, 2-3 per DC)
- Phi accrual provides a **continuous suspicion level** calibrated to actual network conditions

### 4.2 Consistent Hashing & Virtual Nodes

Cassandra distributes data using **Murmur3 hashing** over a token ring [^22^]:

```
Token Ring (0 to 2^127-1):
  0 -------- N1(vnode) -------- N2(vnode) -------- N3(vnode) -------- MAX
              |                    |                    |
             [A-C]                [D-F]                [G-I]
             replicas             replicas             replicas
             on N2,N3             on N3,N1             on N1,N2
```

- **Virtual nodes (vnodes)**: Each physical node claims many token ranges (default 256), improving balance and recovery
- **Replication factor (N)**: Number of copies; typically 3
- **Quorum condition**: R + W > N for strong consistency (e.g., R=2, W=2, N=3)

### 4.3 Tunable Consistency

Cassandra's defining feature: **per-operation consistency levels** [^23^]:

| Level | Behavior | Use Case |
|-------|----------|----------|
| `ONE` | Wait for 1 replica | Maximum throughput, eventual consistency |
| `QUORUM` | Wait for N/2+1 replicas | Balanced consistency/availability |
| `ALL` | Wait for all N replicas | Strongest consistency, lowest availability |
| `LOCAL_QUORUM` | Quorum within local DC | Low latency + strong consistency per DC |
| `ANY` (writes) | Even hinted handoff counts | Maximum write availability |

### 4.4 LSM-Tree Storage Engine

Cassandra uses **Log-Structured Merge Trees** for write-optimized storage [^24^]:

```
Write Path:                    Read Path:
Client ->                      Check Bloom Filter
  Commit Log (WAL) ->          Check Memtable (in-memory)
    Memtable (sorted map) ->    Check SSTables newest to oldest
      Flush when full ->        Merge results, return latest version
        SSTable on disk
          |
    Compaction (background)
    Merge SSTables, remove old versions
```

**Three compaction strategies**:

| Strategy | Best For | Tradeoff |
|----------|----------|----------|
| **Size-Tiered (STCS)** | Write-heavy workloads | Read amplification spikes |
| **Leveled (LCS)** | Read-heavy workloads | Higher write amplification |
| **Time-Window (TWCS)** | Time-series data with TTL | Inefficient for historical updates |

### 4.5 Repair Mechanisms: Three Layers

Cassandra has three complementary repair mechanisms [^25^]:

1. **Hinted Handoff**: When a replica is down, the coordinator stores a "hint" and replays it when the node recovers. Limited to `max_hint_window` (default 3 hours).

2. **Read Repair**: During reads (with QUORUM+), the coordinator compares digests from all replicas. If divergent, repairs the stale replica(s). Controlled by `read_repair_chance`.

3. **Anti-Entropy Repair** (`nodetool repair`): Manual full comparison using **Merkle trees** to identify divergent data ranges. Must run at least once per `gc_grace_seconds` (default 10 days) to prevent tombstone resurrection.

### 4.6 Lessons for HelixCluster

- **Gossip protocol** is excellent for cluster membership but NOT for consensus — use it for service discovery, not for critical decisions
- **Tunable consistency** gives applications control over consistency/latency tradeoffs — HelixCluster should support configurable read/write quorum levels
- **LSM-trees** provide excellent write throughput but require careful compaction tuning — consider for HelixCluster's write-heavy paths
- **Three-layer repair** (hinted handoff + read repair + anti-entropy) provides progressive data consistency — HelixCluster should implement multiple repair mechanisms

---

## 5. TiDB / TiKV

### 5.1 Architecture: SQL + KV + Placement Driver

TiDB separates concerns into three components [^26^]:

```
+-------------------------+
|    TiDB Server Layer    |  (Stateless SQL frontends)
|  MySQL-compatible, SQL  |  Multiple nodes, any can accept queries
|  Parser, Optimizer, Exec|
+-----------+-------------+
            |
+-----------v-------------+     +---------------------+
|   Placement Driver (PD) |     |      TiFlash        |
|  - Metadata management  |     |  Columnar store     |
|  - Region scheduling    |     |  Raft Learner nodes |
|  - Timestamp oracle     |     |  HTAP analytics     |
|  - Auto-sharding        |     +---------------------+
+-----------+-------------+
            |
+-----------v-------------+
|      TiKV Layer         |  (Distributed transactional KV)
|  - Region = shard unit  |
|  - Raft per Region      |  ~96MB default Region size
|  - RocksDB storage      |
+-------------------------+
```

### 5.2 Placement Driver: The Metadata Brain

The **Placement Driver (PD)** is TiDB's most unique component [^27^]:

- **Cluster membership**: Tracks all TiKV nodes dynamically
- **Region scheduling**: Decides where Regions live; handles splits, merges, rebalancing
- **Leader scheduling**: Orchestrates Raft leader election and placement
- **Timestamp Oracle**: Provides strictly increasing globally unique timestamps (TSO) for transactions
- **Hot spot detection**: Automatically moves hot Regions to balance load
- **No persistent state**: Gathers all state from TiKV nodes on startup

```go
// TiDB transaction model (inspired by Google Percolator)
// Uses TSO for start timestamp + commit timestamp
BEGIN;                    -- Get start TS from PD
SELECT ...;               -- Read at start TS
UPDATE ...;               -- Buffer writes
COMMIT;                   -- 2PC: Prewrite -> Commit
                          -- Uses TiKV's MVCC + Raft
```

### 5.3 HTAP: TiFlash Learner Replication

TiDB's HTAP architecture uses **Raft Learner** nodes for the columnar store [^28^]:

- TiFlash nodes are **non-voting learners** in the Raft group
- They asynchronously replicate Raft logs from TiKV leaders
- Row-format tuples are transformed to **columnar format** on the learner
- **Read-time consistency**: TiFlash validates Raft index + MVCC on reads
- **Workload isolation**: OLTP and OLAP run on separate physical resources

This design means OLTP transactions don't wait for TiFlash replication, yet analytical queries can read consistent data.

### 5.4 Lessons for HelixCluster

- **Separation of SQL and storage layers** enables independent scaling — HelixCluster should separate compute and storage
- **Dedicated metadata/scheduler service (PD)** centralizes complex decisions — HelixCluster needs a placement driver equivalent
- **Raft Learner pattern** for read replicas enables workload isolation without impacting write latency — HelixCluster should use Raft learners for read-only replicas

---

## 6. CAP Theorem & Consistency Model Comparison

### 6.1 Where Each System Sits on CAP

| Database | CAP Choice | Consistency Default | Partition Handling |
|----------|-----------|-------------------|-------------------|
| **MySQL NDB** | CP | Strong (2PC) | Node group quorums |
| **PostgreSQL** | CA (single node) | Strong | Streaming replication (async) |
| **CockroachDB** | **CP** | **Serializable** | Multi-Raft quorums per range |
| **Cassandra** | **AP** | **Eventual** | Tunable: ONE to ALL |
| **TiDB** | CP | Snapshot Isolation | Multi-Raft quorums per Region |

### 6.2 Consistency Models Compared

| Model | Systems | Guarantees | Cost |
|-------|---------|-----------|------|
| **Strict Serializability** | Spanner only | Serializable + real-time order | Clock uncertainty wait |
| **Serializable** | CockroachDB, NDB | No anomalies, but may reorder non-concurrent txs | Timestamp tracking + retries |
| **Snapshot Isolation** | TiDB (default), PostgreSQL | No dirty reads, repeatable reads | MVCC versions |
| **Eventual Consistency** | Cassandra (default) | All replicas converge eventually | Lowest latency |
| **Tunable** | Cassandra (all levels) | Per-operation choice | Varies by operation |

### 6.3 Replication Strategies Compared

| Strategy | Systems | Pros | Cons |
|----------|---------|------|------|
| **Synchronous 2PC** | NDB | Strong consistency | High latency, coordinator bottleneck |
| **WAL Streaming** | PostgreSQL | Simple, efficient | Single primary writes, failover delay |
| **Multi-Raft** | CockroachDB, TiDB | Per-shard consensus, parallel | Complex, many Raft groups |
| **Gossip + Quorum** | Cassandra | Massive scale, no single point | Eventual consistency, complex repairs |

---

## 7. Architecture Comparison Matrix

| Dimension | CockroachDB | Cassandra | TiDB | PostgreSQL+Patroni | MySQL NDB |
|-----------|-------------|-----------|------|-------------------|-----------|
| **Sharding** | Automatic (ranges) | Automatic (consistent hash) | Automatic (Regions) | Manual (Citus) | Automatic (hash) |
| **Consensus** | Multi-Raft | None (gossip) | Multi-Raft | etcd (for HA only) | 2PC within node group |
| **Default Isolation** | Serializable | Eventual | Snapshot Isolation | Read Committed | Read Committed |
| **Storage Engine** | Pebble (LSM) | LSM (custom) | RocksDB (LSM) | Heap/B-tree | In-memory + disk |
| **Max Scale** | 100s of nodes | 10,000s of nodes | 100s of nodes | 10s (Citus: 100s) | 48+ data nodes |
| **Failover Time** | Seconds | Instant (any node) | Seconds | 20-30s (Patroni) | Sub-second |
| **Geo-Distribution** | Native | Native | Native | Via async replication | Native (active-active) |
| **SQL Support** | PostgreSQL-compatible | CQL (limited) | MySQL-compatible | Full PostgreSQL | Full MySQL |
| **Learner Replicas** | Yes (follower reads) | No | Yes (TiFlash) | Physical replicas | No |
| **Self-Healing** | Automatic rebalancing | Hinted handoff + repair | PD scheduling | Manual | Automatic |

---

## 8. HelixCluster Impact

### Specific Improvements to Implement

Based on this research, the following concrete improvements should be made to HelixCluster's data layer:

#### 8.1 MUST Adopt (CockroachDB Patterns)

1. **Multi-Raft for Data Sharding**: Adopt CockroachDB's Multi-Raft pattern — one Raft group per data shard (range/region), with a MultiRaft manager that coalesces heartbeats. This provides parallel consensus, granular recovery, and constant goroutine overhead. [^13^]

2. **Leaseholder Pattern**: For each shard, designate a leaseholder that serves reads without going through Raft. This reduces read latency dramatically. Leaseholders should be transferable to nodes near the traffic source. [^16^]

3. **Closed Timestamps for Follower Reads**: Implement a "closed timestamp" mechanism where the leaseholder promises no new writes below a certain timestamp. Followers can then serve stale reads without leaseholder coordination, enabling geo-distributed read scaling. [^16^]

4. **Parallel Commit Protocol**: Reduce distributed transaction commit from 2 Raft round-trips to 1 by using a parallel commit pattern where intents are replicated in parallel and only the commit record requires consensus. [^15^]

5. **Survival Goals (Zone/Region)**: Allow databases to declare survival goals. Zone survival = 3 replicas (1 per zone). Region survival = 5 replicas (2+2+1). Automatically adjust replication factor and placement. [^17^]

6. **Cost-Based Optimizer with Locality Awareness**: The query optimizer should consider data locality when planning queries, preferring local reads and parallelizing across nodes. [^11^]

#### 8.2 SHOULD Adopt (Multiple Systems)

7. **Tunable Consistency Levels**: Following Cassandra's pattern, support per-operation consistency configuration (ONE, QUORUM, ALL) to let applications trade consistency for latency. [^23^]

8. **Raft Learner Replicas**: Following TiDB's TiFlash pattern, use non-voting Raft learners for read replicas and analytics workloads. This provides workload isolation without impacting write latency. [^28^]

9. **Three-Layer Repair Mechanism**: Implement (a) hinted handoff for transient failures, (b) read repair for hot data consistency, and (c) periodic anti-entropy repair with Merkle trees for full reconciliation. [^25^]

10. **Placement Driver / Metadata Service**: Following TiDB's PD, create a dedicated metadata service that manages shard placement, handles rebalancing, provides timestamps, and orchestrates cluster topology changes. [^27^]

11. **LSM-Tree Storage Engine**: For write-heavy workloads, use an LSM-tree storage engine (like Pebble/RocksDB) with configurable compaction strategies (size-tiered, leveled, time-window). [^24^]

12. **Phi Accrual Failure Detector**: Following Cassandra, use a phi accrual failure detector for node health monitoring instead of simple heartbeats. This adapts to network conditions and reduces false positives. [^21^]

#### 8.3 SHOULD Avoid

13. **Avoid Single Raft for Entire Cluster**: Single Raft groups (like etcd/Consul) become bottlenecks at scale. Always use Multi-Raft for data layer consensus. [^14^]

14. **Avoid Synchronous Replication Across Regions**: MySQL NDB's 2PC across geographic distances adds unacceptable latency. Use async or quorum-based replication across regions. [^3^]

15. **Avoid In-Memory Primary Storage**: NDB's requirement that all indexed data fit in RAM limits scale and increases cost. Use disk-based storage with caching. [^4^]

16. **Avoid Gossip for Critical Decisions**: Cassandra's gossip protocol is eventually consistent and unsuitable for leader election or transaction coordination. Use Raft/etcd for critical consensus. [^21^]

17. **Avoid Coordinator-Botleneck Writes**: Pattern where all writes go through a single coordinator (Citus, early TiDB prototypes). Route writes directly to shard leaseholders. [^10^]

#### 8.4 Implementation Priority for HelixCluster

| Priority | Feature | Source Pattern | Effort |
|----------|---------|---------------|--------|
| P0 | Multi-Raft per shard | CockroachDB | High |
| P0 | Leaseholder with transfer | CockroachDB | Medium |
| P0 | Automatic rebalancing | CockroachDB + TiDB PD | High |
| P1 | Closed timestamps + follower reads | CockroachDB | High |
| P1 | Parallel commit protocol | CockroachDB | Medium |
| P1 | Placement driver service | TiDB PD | High |
| P2 | Raft learner replicas | TiDB TiFlash | Medium |
| P2 | Tunable consistency | Cassandra | Low |
| P2 | Three-layer repair | Cassandra | Medium |
| P3 | LSM-tree storage option | Cassandra + Pebble | High |
| P3 | Phi accrual failure detector | Cassandra | Low |

---

## References

[^1^]: [MySQL NDB Cluster Architecture](https://oneuptime.com/blog/post/2026-03-31-mysql-how-to-understand-mysql-ndb-cluster-architecture/view) — NDB shared-nothing architecture with data nodes, SQL nodes, and management nodes

[^2^]: [MySQL NDB Cluster Official](https://www.mysql.com/products/cluster/) — 99.999% availability, in-memory real-time database

[^3^]: [InnoDB vs NDB Cluster](https://oneuptime.com/blog/post/2026-03-31-mysql-innodb-cluster-vs-ndb-cluster-which/view) — 2PC synchronous replication within node groups

[^4^]: [Getting Started with NDB Cluster](https://genexdbs.com/getting-started-with-mysql-ndb-cluster-a-simple-guide-for-beginners/) — Data nodes store data in memory with optional disk persistence

[^5^]: [NDB vs InnoDB Comparison](https://oneuptime.com/blog/post/2026-03-31-mysql-what-is-ndb-cluster/view) — Sub-millisecond latency but weak JOIN performance

[^6^]: [PostgreSQL WAL Internals](https://sambasivareddyin/blog/postgresql-internals-module-4-write-ahead-logging-checkpoints-and-crash-recovery) — LSN, crash recovery, checkpoint process

[^7^]: [WAL Files and Sequence Numbers](https://www.crunchydata.com/blog/postgres-wal-files-and-sequuence-numbers) — pg_waldump analysis of WAL records

[^8^]: [PostgreSQL HA with Patroni](https://dev.to/philip_mcclarence_2ef9475/postgresql-high-availability-patroni-replication-and-failover-patterns-4f6k) — RTO 20-30 seconds, etcd consensus

[^9^]: [Patroni Architecture](https://severalnines.com/blog/building-high-availability-postgresql-clusters-with-patroni-and-other-integrated-approaches/) — DCS-driven coordination, YAML configuration

[^10^]: [Citus GitHub](https://github.com/citusdata/citus) — Distributed PostgreSQL as extension, coordinator-worker model

[^11^]: [CockroachDB Architecture Paper](https://www.hemantkgupta.com/p/insights-from-paper-cockroachdb-the) — 5-layer architecture: SQL, Transactional KV, Distribution, Replication, Storage

[^12^]: [CockroachDB Source Code](https://github.com/cockroachdb/cockroach) — Key files: replica_send.go, replica_raft.go, scheduler.go

[^13^]: [CockroachDB Scaling Raft](https://www.cockroachlabs.com/blog/scaling-raft/) — MultiRaft design: coalesced heartbeats, constant goroutines

[^14^]: [What is MultiRaft](https://sergeiturukin.com/2017/06/09/multiraft.html) — Comparison of single Raft vs MultiRaft

[^15^]: [CockroachDB Transaction Layer](https://www.cockroachlabs.com/docs/stable/architecture/transaction-layer) — Write intents, timestamp cache, closed timestamps

[^16^]: [Follower Reads Architecture](https://www.cockroachlabs.com/blog/follower-reads-stale-data/) — Closed timestamps, bounded staleness, resolved timestamps

[^17^]: [Multi-Region Survival Goals](https://www.cockroachlabs.com/docs/stable/multiregion-survival-goals) — Zone vs Region failure survival

[^18^]: [CockroachDB Consistency Model](https://www.cockroachlabs.com/blog/consistency-model/) — Serializable but not strict serializable, causal reverse anomaly

[^19^]: [Jepsen CockroachDB Analysis](https://jepsen.io/analyses/cockroachdb-beta-20160829.pdf) — Jepsen testing results and findings

[^20^]: [CockroachDB Resilience Demo](https://www.cockroachlabs.com/docs/stable/demo-cockroachdb-resilience) — Self-healing, up-replication, rebalancing

[^21^]: [Gossip Protocols in Distributed Systems](https://hosseinnejati.medium.com/gossip-protocols-how-services-discover-and-share-state-f4479bc6ac50) — Phi accrual failure detector, Cassandra gossip

[^22^]: [Cassandra Cluster Configuration](https://oneuptime.com/blog/post/2026-01-27-cassandra-cluster-configuration/view) — Gossip protocol details, seed nodes

[^23^]: [Tunable Consistency in Cassandra](https://primitives.pub/distributed-systems/monographs/tunable-consistency) — Consistency levels, quorum condition R+W>N

[^24^]: [Cassandra LSM Tree Internals](https://dev.to/priteshsurana/cassandra-internals-lsm-tree-sstables-and-compaction-2ai8) — SSTables, memtables, compaction strategies

[^25^]: [Anti-Entropy Repair in Cassandra](https://www.datavail.com/blog/anti-entropy-repair-in-cassandra/) — Hinted handoff, read repair, Merkle tree repair

[^26^]: [TiDB Architecture](https://dev.to/godofgeeks/tidb-architecture-332d) — TiDB + TiKV + PD components

[^27^]: [TiKV GitHub](https://github.com/tikv/tikv) — Multi-Raft, Placement Driver, auto-sharding

[^28^]: [TiDB HTAP Paper](https://www.vldb.org/pvldb/vol13/p3072-huang.pdf) — Raft Learner replication for TiFlash columnar store
