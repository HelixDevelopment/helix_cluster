# 2. Distributed Databases: CockroachDB, Cassandra, PostgreSQL, and TiDB

The data layer is the immutable center of gravity for every distributed system. While messaging fabrics move ephemeral events and scheduling planes place transient workloads, the database persists the ground truth: session states, configuration, topology maps, and audit trails. If the data layer fails — by losing writes under network partition, accepting conflicting mutations, or simply becoming unavailable during a node failure — every dependent subsystem collapses. Kubernetes discovered this when etcd compaction lag triggered cascading API server outages at the 5,000-node wall. Netflix runs Cassandra across tens of thousands of nodes because eventual consistency, when properly tuned, survives datacenter failures that would stall stricter systems.

This chapter examines four architectures: **CockroachDB**, whose Multi-Raft consensus and serializable default make it the gold standard for distributed SQL; **Apache Cassandra**, which demonstrates how gossip-based membership and tunable consistency achieve extreme scale; **PostgreSQL**, whose WAL streaming and Patroni ecosystem represent the most battle-tested path for strong consistency at moderate scale; and **TiDB/TiKV**, whose Placement Driver and Raft Learner pattern show how compute-storage separation enables hybrid transactional-analytical processing (HTAP).

For HelixCluster, the lessons are actionable: adopt CockroachDB's Multi-Raft for data-shard consensus, its leaseholder pattern for local reads, and its parallel commit for low-latency transactions; borrow Cassandra's three-layer repair for edge self-healing; learn from TiDB's Placement Driver for shard scheduling; and study PostgreSQL's WAL streaming as the reference for log-based replication.

| Database | CAP Choice | Default Isolation | Consensus Model | Storage Engine | Max Scale |
|----------|-----------|-------------------|-----------------|----------------|-----------|
| **CockroachDB** | CP | Serializable | Multi-Raft per 64MB range | Pebble (LSM-tree) | 100s of nodes |
| **Cassandra** | AP (tunable) | Eventual | Gossip + quorum reads/writes | LSM-tree (custom) | 10,000s of nodes |
| **PostgreSQL** | CA (single-node) | Read Committed | WAL streaming + etcd (Patroni) | Heap / B-tree | 10s of nodes (100s with Citus) |
| **TiDB/TiKV** | CP | Snapshot Isolation | Multi-Raft per 96MB Region | RocksDB (LSM-tree) | 100s of nodes |

*Table 2.1: Four distributed database architectures compared across consistency model, isolation default, consensus mechanism, storage engine, and demonstrated scale. CockroachDB and TiDB both employ Multi-Raft but differ in isolation defaults and storage engines. Cassandra stands alone in offering tunable per-operation consistency.*

---

## 2.1 CockroachDB — Gold Standard for Distributed SQL

### 2.1.1 SQL to KV to Multi-Raft to RocksDB: Serializable by Default

CockroachDB's architecture is organized into five distinct layers, each transforming the problem one step closer to the physical storage medium. Understanding this stack is essential because HelixCluster will replicate a similar transformation: abstract API calls (SQL for CockroachDB, gRPC for HelixCluster) must ultimately become consensus-backed, durably logged commands.

```
+--------------------------------------------------------------+
| Layer 5 | SQL Layer: Parser -> Cost-Based Optimizer -> Exec  |
|         | PostgreSQL-compatible wire protocol                |
+---------+----------------------------------------------------+
| Layer 4 | Transactional KV: Write intents, Timestamp cache,  |
|         | Transaction records, Concurrency manager           |
+---------+----------------------------------------------------+
| Layer 3 | Distribution: Range partitioning (64MB default),   |
|         | Span resolver, Leaseholder routing                 |
+---------+----------------------------------------------------+
| Layer 2 | Replication: Multi-Raft consensus per range,       |
|         | Lease management, Snapshot transfer, Rebalancing   |
+---------+----------------------------------------------------+
| Layer 1 | Storage: Pebble (RocksDB fork), MVCC, SSTable      |
|         | compression, Bloom filters                         |
+--------------------------------------------------------------+
```

*Figure 2.1: CockroachDB's five-layer architecture. Each SQL query descends through the transactional KV layer (which handles conflict detection), the distribution layer (which maps keys to ranges), the replication layer (which enforces consensus via Multi-Raft), and finally the storage layer (which persists to an LSM-tree).*

When a client issues `BEGIN; SELECT * FROM inventory WHERE product_id = 456; UPDATE inventory SET qty = qty - 1 WHERE product_id = 456; COMMIT;`, the SQL layer parses and optimizes the query with locality awareness (preferring the nearest leaseholder). The transactional KV layer assigns a read timestamp from the gateway's Hybrid Logical Clock (HLC), executes the read against the leaseholder, and buffers writes as "intents" — provisional writes visible only to the transaction. At commit, the parallel commit protocol (Section 2.1.4) replicates the transaction record and intents simultaneously through Raft.

The default isolation level is **SERIALIZABLE** — the strongest SQL guarantee. CockroachDB achieves this through **write intents** (logical locks on uncommitted keys), a **timestamp cache** (tracking recent reads so writers detect conflicts), and **transaction records** (stored in a dedicated key tracking transaction state). When transactions conflict, one retries with a later timestamp. Applications written for single-node PostgreSQL behave correctly under distributed concurrency without explicit locking.

### 2.1.2 Multi-Raft: One Consensus Group Per 64 MB Range

The single most important design decision in CockroachDB — and the one with the greatest implications for HelixCluster — is **Multi-Raft**: instead of one Raft group managing all data, every 64 MB range (a contiguous key slice) forms an independent Raft consensus group.

```
Traditional Single-Raft (etcd/ZooKeeper)          CockroachDB Multi-Raft

  +------------------+                              +------------------+
  |   Single Raft    |                              |   Multi-Raft     |
  |   (1 log, 1 leader|                             |   Manager        |
  |    all data)     |                              |   (per-node      |
  +--------+---------+                              |    coordinator)  |
           |                                        +--------+---------+
           v                                                 |
    Write Bottleneck                    +-------------------+-------------------+
    (all writes flow                    | Range A  | Range B  | Range C | ...   |
     through leader)                    | Raft GRP | Raft GRP | Raft GRP|       |
                                        | (Leader  | (Leader  | (Leader |       |
                                        |  on N1)  |  on N2)  |  on N3) |       |
                                        +----------+----------+---------+-------+
                                        Each range: independent log, leader, quorum
                                        Ranges per node: hundreds to thousands
```

*Figure 2.2: Single-Raft vs. Multi-Raft. In single-Raft systems, one leader serializes all writes, creating a throughput ceiling. In Multi-Raft, each 64 MB range elects its own leader; leaders are distributed across nodes, enabling linear write scaling. The Multi-Raft manager coalesces heartbeats between node pairs, keeping network overhead constant regardless of range count.*

This choice unlocks five properties that single-Raft systems cannot provide:

**Parallelism.** Independent ranges commit concurrently. Range A's Raft group commits writes at the same time Range B commits different writes, with no cross-range coordination except distributed transactions (which use two-phase commit across range boundaries).

**Recovery granularity.** When a node fails, only ranges with replicas on that node require re-replication. In single-Raft, losing the leader stalls the entire cluster; in Multi-Raft, only ranges whose leader was on the failed node experience brief interruption.

**Load balancing.** Leaseholders for different ranges reside on different nodes. A hot range can transfer its leaseholder to a lightly loaded node without affecting other ranges, enabling CPU and I/O self-balancing.

**Heartbeat coalescing.** The naive Multi-Raft implementation — one goroutine per range — would exhaust memory at scale. CockroachDB's Multi-Raft manager batches all heartbeats between the same node pair into a single RPC, regardless of range count. The result is approximately **three goroutines per store** instead of one per range.

**Horizontal write scaling.** Adding nodes adds capacity to host range replicas and elect leaders. There is no single write bottleneck — a property CockroachDB shares with TiKV.

The Go source path through this system starts at `(*Replica).Send()` in `replica_send.go`, which determines if a batch is read-only or read-write. For writes, it acquires latches (fine-grained locks on keys), checks the timestamp cache for conflicts, and proposes to Raft via `(*Replica).propose()` in `replica_raft.go`. The proposal enters a buffer that the Multi-Raft manager coalesces before transmission.

### 2.1.3 Leaseholder Pattern: Local Reads, Closed Timestamps for Follower Reads

Multi-Raft solves write scaling, but read scaling presents a different challenge. If every read went through the Raft leader for recency, cross-region deployments would suffer read latency equal to the round-trip to the leader's region. CockroachDB solves this with the **leaseholder pattern**.

```
              Leaseholder Pattern: Read Path Optimization

     Client in us-west-2                     Client in us-east-1
            |                                        |
            v                                        v
    +-------+--------+                       +-------+--------+
    | Gateway Node   |                       | Follower Node  |
    | (us-west-2)    |                       | (us-east-1)    |
    +-------+--------+                       +-------+--------+
            |                                        |
            | Read @ leaseholder                     | Read @ closed ts
            | (local, <1ms)                          | (follower, <1ms)
            v                                        v
    +-------+--------+                       +-------+--------+
    | Leaseholder    |    Closed Timestamp   | Follower       |
    | for Range R    |<---(every 2-3s)-------| for Range R    |
    | Has latest data|                       | Serves stale   |
    +----------------+                       | reads <= ts    |
                                             +----------------+
```

*Figure 2.3: The leaseholder pattern. The leaseholder serves fresh reads locally. Followers serve stale reads using closed timestamps — periodic promises from the leaseholder that no new writes will appear below a specified timestamp.*

For each range, one replica holds the **lease**, giving it exclusive rights to serve reads at the latest timestamp and propose writes to Raft. Because the leaseholder is typically colocated with the Raft leader, writes require only a single Raft round-trip. Reads from the leaseholder require **no Raft round-trip at all** — it returns the latest committed value from local Pebble storage.

For cross-region deployments, **closed timestamps** enable follower reads. Every 2–3 seconds, the leaseholder "closes" a timestamp — promising no new writes below it. Followers receiving this closed timestamp can serve reads at or below it without consulting the leaseholder. A client in `us-east-1` can read from a local follower with bounded staleness of a few seconds, reducing latency from 80 ms to sub-millisecond.

| Survival Goal | Replicas | Failure Tolerance | Write Latency Impact | Use Case |
|---------------|----------|-------------------|---------------------|----------|
| `ZONE FAILURE` (default) | 3 (1 per zone) | 1 zone failure | Minimal — local quorum | Standard OLTP, low-latency gaming |
| `REGION FAILURE` | 5 (2+2+1 across regions) | 1 region failure | Adds cross-region RTT (~50-150ms) | Compliance, financial data, DR requirements |

*Table 2.2: CockroachDB survival goals. `ALTER DATABASE myapp SURVIVE REGION FAILURE` automatically increases replication from 3 to 5 and spans multiple regions — the application states its durability requirement, and the database adjusts topology.*

### 2.1.4 Parallel Commit: Two RTT to One RTT

Distributed transaction commit is traditionally a two-phase process that costs at least two round-trip times (RTT): first to prepare all participants, then to commit. CockroachDB's **parallel commit protocol** collapses this to one RTT in the common case.

```
Traditional 2PC (2 RTT minimum)                    Parallel Commit (1 RTT)

Coordinator                Participants          Coordinator        Participants
     |                          |                      |                  |
     |--- 1. PREPARE ---------->|                      |--- 1. COMMIT +   |
     |<-- 2. PREPARED OK -------| (all nodes)          |    INTENTS ----->| (all nodes)
     |                          |                      |                  |
     |--- 3. COMMIT ------------>|                      |<-- 2. ACK -------| (parallel)
     |<-- 4. ACKED --------------|                      |                  |
     |                          |                      | (if all ACK, done)
     | (latency = 2x RTT)       |                      | (latency = 1x RTT)
```

*Figure 2.4: Traditional two-phase commit vs. CockroachDB parallel commit. In 2PC, the coordinator waits for PREPARE responses before sending COMMIT, serializing two network round-trips. In parallel commit, the coordinator sends commit intents and the final commit record simultaneously. If all participants acknowledge, the transaction is committed in a single round-trip.*

When a transaction commits, the coordinator writes **write intents** to all affected keys and a **transaction record** to a dedicated key, all in parallel through Raft. Each intent includes the transaction ID and a provisional flag; the transaction record starts in `PENDING` state.

If all intents achieve Raft consensus, the coordinator flips the record to `COMMITTED` and acknowledges success. If a reader encounters a provisional intent, it checks the record: `COMMITTED` means the intent is valid; `ABORTED` means ignore it; `PENDING` means wait and retry.

The critical optimization: **the client receives acknowledgment as soon as the transaction record commits**, without waiting for intent cleanup, which happens lazily in background. The common case — a transaction touching multiple ranges — completes in **one Raft round-trip** rather than two.

### 2.1.5 Jepsen Testing History

CockroachDB has been subjected to multiple independent Jepsen analyses. The key finding: it provides **serializable isolation but not strict serializability**. Transactions execute in some serial order, but that order may differ from wall-clock time for disjoint transactions on different ranges.

The "causal reverse" anomaly demonstrated by Jepsen occurs when T1 writes key A and commits, then T2 writes key B after T1, but a concurrent reader sees T2's write without T1's. This happens because HLC timestamp ordering across disjoint ranges may diverge from wall-clock ordering by a window bounded by clock uncertainty (typically 250–500 ms with NTP).

Strict serializability would require waiting out this uncertainty window. CockroachDB chose performance, documents the tradeoff explicitly, and Jepsen confirmed it behaves exactly as specified — no anomalies within the promised contract.

---

## 2.2 Apache Cassandra

### 2.2.1 Gossip, Phi Accrual, Consistent Hashing, and Three-Layer Repair

Cassandra represents the opposite pole from CockroachDB on the consistency-availability spectrum. Where CockroachDB defaults to serializable isolation and synchronous replication, Cassandra defaults to eventual consistency and asynchronous replication — achieving scale that CockroachDB cannot match. Apple and Netflix both run Cassandra clusters exceeding 10,000 nodes.

**Gossip protocol and phi accrual failure detection.** Cassandra nodes discover each other through a peer-to-peer gossip protocol. Every second, each node gossips with 1–3 random peers, exchanging `EndpointState` messages (node ID, status, load, schema version, token ownership). New nodes bootstrap via **seed nodes** — static entry points (2–3 per datacenter).

Failure detection uses the **phi accrual algorithm**, converting heartbeat statistics into a continuous suspicion level. The phi value represents `-log10(P(late))`; when it exceeds a threshold (8–12), the node is marked down. This adapts automatically to network conditions.

```python
class PhiAccrualDetector:
    def phi(self, now: float) -> float:
        elapsed = now - self.last_heartbeat
        mean = statistics.mean(self.arrival_intervals)
        std = statistics.stdev(self.arrival_intervals)
        z = (elapsed - mean) / std
        prob_late = 1 - 0.5 * (1 + math.erf(z / math.sqrt(2)))
        return -math.log10(max(prob_late, 1e-300))
```

Data distribution uses **Murmur3 consistent hashing** over a token ring (0 to 2^127-1). Each physical node claims many token ranges via **virtual nodes (vnodes)** — default 256 per node — so when a node joins or leaves, only 1/N of ranges need reassignment.

| Consistency Level | Behavior | Use Case | Latency |
|-------------------|----------|----------|---------|
| `ONE` | Wait for 1 replica | High-throughput writes, cache data | Lowest |
| `QUORUM` | Wait for N/2+1 replicas | Balanced consistency and availability | Medium |
| `ALL` | Wait for all N replicas | Strongest consistency, financial data | Highest |
| `LOCAL_QUORUM` | Quorum within local datacenter | Low latency + strong consistency per DC | Medium-low |
| `ANY` (writes only) | Even hinted handoff counts | Maximum write availability during failures | Lowest |

*Table 2.3: Cassandra tunable consistency levels. The quorum condition (R + W > N) guarantees read-your-writes consistency.*

Cassandra's defining feature is **tunable consistency**: each operation specifies its level. A write at `ONE` returns after one replica acknowledges; `QUORUM` waits for majority; `ALL` waits for every replica.

### 2.2.2 Three-Layer Repair: Hinted Handoff, Read Repair, and Anti-Entropy

Because Cassandra accepts writes at `ONE` by default, replicas diverge — temporarily during failures, permanently if a node is down longer than the hint window. Cassandra addresses this with **three complementary repair mechanisms** at different timescales.

```
Three-Layer Repair Mechanism in Cassandra

Layer 1: Hinted Handoff (seconds to hours)
┌─────────────┐     ┌─────────┐     ┌─────────────┐
│ Coordinator │────>| Replica │     │ Down Node   |
│ (stores hint│     | (ACKs)  |     | (receives   |
│  for N3)    |     +---------+     |  hint replay|
└-------------+           |         |  on recovery)
                          v         └-------------+
                   ┌-------------+
                   | Hint Store  |   (max_hint_window: 3 hours)
                   | (on N1, N2) |
                   +-------------+

Layer 2: Read Repair (every read at QUORUM+)
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ Coordinator |---->| Replica N1  |---->| Replica N2  |
│ (compares   |     | (digest)    |     | (digest)    |
│  digests)   |     +-------------+     +-------------+
└------+------+           |                    |
       | (N2 stale)        v                    v
       +-----------> ┌-------------+     ┌-------------+
                     | Repair N2   |     | Return data |
                     | (async)     |     | to client   |
                     +-------------+     +-------------+

Layer 3: Anti-Entropy Repair (periodic, full reconciliation)
┌─────────────┐     ┌─────────────────────────────┐     ┌─────────────┐
│ Node N1     |<--->| Merkle Tree Comparison      |<--->| Node N2     |
│ (builds     |     | - Hash tree of key ranges   |     | (builds     |
│  tree)      |     | - Compare top-down          |     |  tree)      |
└-------------+     | - Transfer only divergent   |     └-------------+
                    |   subtrees                  |
                    └─────────────────────────────┘
                    (triggered by nodetool repair;
                     must run within gc_grace_seconds: 10 days)
```

*Figure 2.5: Cassandra's three-layer repair. Layer 1 (hinted handoff) stores missed writes for replay during transient failures. Layer 2 (read repair) compares digests during reads. Layer 3 (anti-entropy) performs full Merkle-tree reconciliation periodically.*

**Layer 1 — Hinted Handoff.** When a coordinator receives acks from some but not all replicas, it stores a "hint" — a record of the missed write — on a live node. When the failed node recovers, hints are replayed. Hints expire after `max_hint_window_in_ms` (default three hours).

**Layer 2 — Read Repair.** On reads at `QUORUM` or higher, the coordinator compares digests (Murmur3 hashes) from multiple replicas. If digests differ, the coordinator asynchronously writes the latest value to stale replicas while returning correct data to the client.

**Layer 3 — Anti-Entropy Repair.** For divergence surviving the first two layers, `nodetool repair` builds **Merkle trees** over key ranges. Nodes exchange tree roots; if roots match, subtrees are identical. Differences recurse down until divergent ranges are identified, and only those ranges stream across the network. This reduces bandwidth from O(all data) to O(divergent data).

| Repair Layer | Trigger | Timescale | Coverage | Overhead |
|-------------|---------|-----------|----------|----------|
| **Hinted Handoff** | Write to unavailable replica | Seconds to 3 hours | Transient failures only | Low — local hint storage |
| **Read Repair** | Read at QUORUM+ with digest mismatch | Every read (hot data) | Hot data inconsistencies | Medium — extra digest comparison |
| **Anti-Entropy** | `nodetool repair` or scheduled | Hours to days | Full dataset reconciliation | High — Merkle tree build + stream |

*Table 2.4: Cassandra's three-layer repair mechanisms. The layers are complementary: hinted handoff catches brief failures, read repair catches hot-data divergence, and anti-entropy ensures eventual full consistency.*

Anti-entropy repair must complete at least once per `gc_grace_seconds` (default 10 days). If a tombstone expires before all replicas receive it, a rejoining node may "resurrect" deleted data — it never saw the tombstone, so its older copy becomes the latest version.

---

## 2.3 PostgreSQL

### 2.3.1 WAL Streaming, Patroni HA, and Citus Sharding

PostgreSQL is the most widely deployed open-source relational database. Its clustering ecosystem demonstrates strong consistency through log shipping rather than distributed consensus at the data layer. The foundation is **Write-Ahead Log (WAL) streaming**: every transaction generates WAL records replicated from primary to standby via TCP, using three cooperative processes — `walsender` on the primary, `walreceiver` on the standby, and a `startup` process that replays WAL into the standby's data files.

The critical primitive is the **Log Sequence Number (LSN)** — a 64-bit pointer in the WAL stream (format: `0/169EC40`). Each data page tracks the LSN of the latest WAL record affecting it. During crash recovery, PostgreSQL reads `pg_control` to find the last checkpoint, then replays WAL from `redo_lsn` forward. The check `pd_lsn >= record LSN` provides idempotency: pages already flushed are skipped.

```sql
-- Monitor replication lag
SELECT pg_current_wal_lsn();                          -- Current LSN on primary
SELECT pg_last_xact_replay_timestamp();               -- Last replay on standby
SELECT pg_wal_lsn_diff('0/22A6400', '0/22A62F0');    -- Bytes of WAL lag
```

Synchronous replication is controlled by `synchronous_commit`: `remote_apply` guarantees zero data loss but adds round-trip latency; `remote_write` acknowledges when the standby has received WAL (not yet applied); `off` maximizes throughput with minimal durability.

**Patroni** is the industry-standard HA template for PostgreSQL, used by GitLab, Zalando, and thousands of deployments. Patroni agents run alongside each PostgreSQL instance and use **etcd, ZooKeeper, or Consul** for distributed consensus. Only one agent holds the leader lock at any time; if the primary fails, Patroni detects this through etcd TTL expiration and promotes the most advanced standby (highest LSN) within 20–30 seconds.

**Citus** extends PostgreSQL into a distributed database using a coordinator-worker architecture. The coordinator handles SQL parsing, query planning, and result aggregation; worker nodes store data shards. Tables are distributed via `create_distributed_table('orders', 'tenant_id')`, after which queries route automatically to the correct shards.

For HelixCluster, PostgreSQL's WAL streaming proves that **log-based replication is battle-tested** — HelixCluster's storage engine should implement a WAL for state machine replication. Patroni's use of etcd shows that **external consensus stores eliminate split-brain without modifying the database core**. Citus validates that **query routing to the correct shard** is scalable, though HelixCluster will push routing into the client layer.

---

## 2.4 TiDB/TiKV

### 2.4.1 Placement Driver, Raft Learner Replicas

TiDB separates concerns into three components: stateless TiDB servers handle MySQL-compatible SQL parsing and query execution; TiKV nodes provide distributed transactional storage; and the **Placement Driver (PD)** serves as the metadata brain that holds everything together.

```
+-------------------------+
|    TiDB Server Layer    |  (Stateless SQL frontends, MySQL-compatible)
|  SQL Parser, Optimizer  |
+-----------+-------------+
            |
+-----------v-------------+     +---------------------+
|   Placement Driver (PD) |     |      TiFlash        |
|  - Metadata management  |     |  Columnar store     |
|  - Region scheduling    |     |  Raft Learner nodes |
|  - Timestamp oracle     |     |  HTAP analytics     |
|  - Hot spot detection   |     +---------------------+
+-----------+-------------+
            |
+-----------v-------------+
|      TiKV Layer         |  (Distributed transactional KV)
|  - Region = 96MB shard  |
|  - Raft per Region      |
|  - RocksDB storage      |
+-------------------------+
```

*Figure 2.6: TiDB architecture. Stateless TiDB servers handle SQL; the Placement Driver manages metadata, scheduling, and timestamps; TiKV provides the distributed storage with one Raft group per 96 MB Region. TiFlash adds columnar analytics via Raft Learner replicas that do not participate in write consensus.*

The Placement Driver has five responsibilities relevant to HelixCluster's metadata service:

**Cluster membership.** PD tracks all TiKV nodes dynamically, maintaining a real-time view of node liveness, capacity, and Region hosting.

**Region scheduling.** PD decides where Regions live, handles splits at 96 MB, merges small adjacent Regions, and rebalances hot Regions to less loaded nodes.

**Leader balancing.** PD orchestrates Raft leader placement, ensuring leaders distribute evenly rather than clustering on one node.

**Timestamp Oracle.** PD provides strictly increasing globally unique timestamps (TSO) for transactions — Spanner TrueTime's equivalent at millisecond precision.

**Stateless recovery.** PD has no persistent state; it gathers all cluster state from TiKV nodes on startup. A failed PD node can be replaced without data migration, reconstructing topology from heartbeats.

TiDB's most distinctive feature is **Raft Learner replication** for TiFlash, its columnar analytics engine. TiFlash nodes are **non-voting learners** in the Raft group: they asynchronously replicate logs from TiKV leaders without participating in quorum. Row-format tuples transform to columnar format on the learner. OLTP transactions never wait for TiFlash — write latency is unaffected — yet analytics queries read consistent data by validating Raft index and MVCC timestamp on read. Workload isolation is complete: OLTP and OLAP run on separate physical resources, sharing only the replication log.

---

## 2.5 Database Lessons for HelixCluster

### 2.5.1 Multi-Raft, Three-Layer Repair, Placement Driver, and Leaseholder

The four databases represent four philosophies of distributed data management. CockroachDB proves distributed SQL with serializable isolation is achievable globally through Multi-Raft, parallel commit, and closed timestamps. Cassandra proves extreme scale through eventual consistency and three-layer repair. PostgreSQL proves simplicity and strong consistency are operational virtues through WAL streaming. TiDB proves compute-storage separation enables workload-specific optimization through its Placement Driver.

HelixCluster's data layer synthesizes these lessons:

**Multi-Raft for data shards.** Every HelixCluster data shard forms an independent Raft group, with a Multi-Raft manager coalescing heartbeats between node pairs. This provides linear write scaling, avoiding the single-Raft bottleneck that limits etcd. Each cell runs its own Raft groups for local data, with cross-cell synchronization via background CRDT merge.

**Leaseholder with closed timestamps.** One replica per shard holds the lease and serves local reads without consensus overhead. Closed timestamps enable follower reads at the edge — a remote node serves stale reads with bounded staleness rather than forwarding every request. This is critical for geo-distributed gaming workloads, where 80 ms cross-region latency makes sessions unplayable.

**Placement Driver for metadata.** Following TiDB, HelixCluster implements a dedicated PD service managing shard placement, automatic split/merge, leader balancing, hot-spot detection, and timestamps. The PD is stateless and self-healing, reconstructing topology from node heartbeats on restart.

**Three-layer repair.** Following Cassandra, HelixCluster implements hinted handoff for transient failures, read repair for hot data, and periodic anti-entropy repair with Merkle trees. Edge nodes with intermittent connectivity converge to consistent state without manual intervention.

| Priority | Feature | Source Pattern | Effort | Impact |
|----------|---------|---------------|--------|--------|
| P0 | Multi-Raft per shard | CockroachDB | High | Linear write scaling |
| P0 | Leaseholder with transfer | CockroachDB | Medium | Sub-millisecond local reads |
| P0 | Automatic rebalancing | CockroachDB + TiDB PD | High | Self-healing data placement |
| P1 | Closed timestamps + follower reads | CockroachDB | High | Geo-distributed read scaling |
| P1 | Parallel commit protocol | CockroachDB | Medium | 1 RTT distributed transactions |
| P1 | Placement Driver service | TiDB PD | High | Shard scheduling, hot-spot detection |
| P2 | Raft Learner replicas | TiDB TiFlash | Medium | Workload isolation for analytics |
| P2 | Three-layer repair | Cassandra | Medium | Self-healing at the edge |
| P2 | Tunable consistency | Cassandra | Low | Per-operation latency control |
| P3 | Phi accrual failure detector | Cassandra | Low | Adaptive node health monitoring |

*Table 2.5: HelixCluster data layer implementation priorities, mapped to source patterns from production databases. P0 features are required for initial release; P1 features deliver competitive advantage; P2 and P3 features differentiate at scale.*

The Go implementation of HelixCluster's data layer begins with the shard and leaseholder abstractions:

```go
package helixdata

import (
	"context"
	"sync"
	"time"

	"github.com/helixcluster/helixdata/raft"
)

// Shard represents a single data partition with its own Raft group.
type Shard struct {
	id        uint64           // Globally unique shard ID
	rangeStart []byte          // Inclusive start of key range
	rangeEnd   []byte          // Exclusive end of key range
	raft      *raft.Node       // Underlying Raft consensus group
	lease     *Lease           // Current leaseholder state
	storage   *PebbleEngine    // LSM-tree storage engine
	mu        sync.RWMutex
}

// Lease tracks which replica holds the read/write lease for this shard.
type Lease struct {
	Holder    string    // Node ID of leaseholder
	Start     time.Time // Lease start time
	Expiration time.Time // Lease expires if not renewed
	ClosedTS  time.Time // No writes below this timestamp
}

// MultiRaftManager coalesces heartbeats across all shards on a node.
type MultiRaftManager struct {
	nodeID     string
	shards     map[uint64]*Shard       // shardID -> Shard
	peers      map[string]*PeerConn    // nodeID -> connection
	heartbeatMu sync.Mutex
	heartbeatBuf map[string]*RaftHeartbeatBatch // pending batches
}

// Send coalesces a heartbeat into the batch for the target node,
// flushing the batch if it exceeds the size or time threshold.
func (m *MultiRaftManager) Send(targetNode string, hb RaftHeartbeat) error {
	m.heartbeatMu.Lock()
	defer m.heartbeatMu.Unlock()

	batch := m.heartbeatBuf[targetNode]
	batch.Append(hb)

	if batch.Size() >= MaxBatchSize || batch.Age() >= MaxBatchDelay {
		return m.flush(targetNode, batch)
	}
	return nil
}

// flush sends the coalesced heartbeat batch and resets the buffer.
func (m *MultiRaftManager) flush(target string, batch *RaftHeartbeatBatch) error {
	conn := m.peers[target]
	if conn == nil {
		return fmt.Errorf("no connection to node %s", target)
	}
	return conn.SendRaftHeartbeats(batch)
}

// Read executes a read against the shard, using the leaseholder
// optimization for local reads and closed timestamps for follower reads.
func (s *Shard) Read(ctx context.Context, key []byte, asOf time.Time) ([]byte, error) {
	s.mu.RLock()
	lease := s.lease
	s.mu.RUnlock()

	// Case 1: We are the leaseholder — serve fresh read locally.
	if lease.Holder == localNodeID {
		return s.storage.Get(key)
	}

	// Case 2: Request timestamp is at or below closed timestamp —
	// serve as follower read without leaseholder coordination.
	if !asOf.IsZero() && !lease.ClosedTS.IsZero() && asOf.Before(lease.ClosedTS) {
		return s.storage.GetMVCC(key, asOf)
	}

	// Case 3: Fresh read and we are not leaseholder — forward.
	return s.forwardToLeaseholder(ctx, key, lease.Holder)
}

// ProposeWrite submits a write to the Raft group for consensus.
// If this node is the leader, it appends to the local log;
// otherwise it forwards to the leader.
func (s *Shard) ProposeWrite(ctx context.Context, batch WriteBatch) error {
	return s.raft.Propose(ctx, batch.Encode())
}
```

The `Shard` struct encapsulates the core abstraction: a data partition with independent consensus, local storage, and lease management. The `MultiRaftManager` batches heartbeats across all shards, keeping the per-node goroutine count constant. The `Read` method implements the leaseholder pattern: if the local node holds the lease, the read is served from local Pebble storage without network round-trip; if the caller accepts staleness and the timestamp is closed, a follower read is served from local MVCC state; otherwise, the request is forwarded to the leaseholder. The `ProposeWrite` method submits writes to Raft for consensus, ensuring durability through quorum replication before acknowledgment.

This architecture gives HelixCluster the write scaling of CockroachDB's Multi-Raft, the read latency of its leaseholder pattern, the self-healing of Cassandra's three-layer repair, the metadata management of TiDB's Placement Driver, and the log-based durability of PostgreSQL's WAL streaming — combined into a data layer that scales from a single edge node to a globally distributed fleet of thousands. The next chapter examines the messaging and streaming systems that move events between these data-bearing nodes.
