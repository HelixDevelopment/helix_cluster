# 3. Messaging & Stream Processing: Kafka, NATS, Pulsar

> *"If you need total ordering for all messages, you're forced to use a single partition. That means a single consumer, and you lose all the parallelism that makes messaging fast."*
>
> — Every distributed messaging system, eventually.

Reliable message delivery is the circulatory system of any distributed platform. While the previous chapter established how HelixCluster nodes agree on *state*, this chapter examines how they exchange *events* — the firehose of telemetry, commands, audit logs, and cross-node traffic that keeps a live system breathing. We analyze three systems that represent distinct philosophical approaches: Apache Kafka (the throughput king), NATS (the speed demon), and Apache Pulsar (the architectural purist). Each makes different tradeoffs among latency, durability, ordering, and operational complexity — tradeoffs that HelixCluster must navigate with eyes open.

---

## 3.1 Apache Kafka

Kafka's dominance in stream processing is no accident. Built around a simple abstraction — the append-only distributed log — it combines zero-copy I/O, OS page cache reliance, and sequential disk access to achieve throughput that would have seemed impossible two decades ago. A modest three-machine cluster can sustain two million writes per second[^179^]. But raw speed is only part of the story. Kafka's real engineering depth lies in its consistency mechanisms, metadata management, and the gradual elimination of operational pain points that plagued early deployments.

### 3.1.1 Exactly-Once Semantics: Idempotent Producers and Transactions

The phrase "exactly-once delivery" is messaging's original sin — a promise that every practitioner learns is theoretically impossible. What Kafka calls Exactly-Once Semantics (EOS) is more precisely *exactly-once processing*: a combination of producer-side idempotency and broker-side transactions that eliminates duplicate writes and enables atomic read-process-write cycles[^3113^]. The consumer must still implement idempotent processing logic, because no messaging system can guarantee end-to-end exactly-once across external databases or side effects[^3162^].

**Idempotent Producers.** When a producer retries a failed send, the broker must distinguish a genuine retry from a new message. Kafka solves this with two primitives: a unique **Producer ID (PID)** assigned by the broker on initialization, and a monotonically increasing **sequence number** maintained per partition. The broker tracks the highest accepted sequence number for each `(PID, partition)` pair. If a retry arrives with a sequence number less than or equal to the last acknowledged, the broker discards the duplicate but still returns success to the producer[^3117^].

The following Go implementation captures the essential logic:

```go
type IdempotentProducer struct {
    brokerConn   net.Conn
    producerID   uint64          // Assigned by broker on InitProducerId
    seqNumbers   map[int32]int64 // Per-partition sequence numbers
    mu           sync.Mutex
}

// Init connects to the broker and obtains a Producer ID
func (p *IdempotentProducer) Init(brokerAddr string) error {
    conn, err := net.Dial("tcp", brokerAddr)
    if err != nil {
        return err
    }
    p.brokerConn = conn
    // Broker assigns unique PID (simplified — actual protocol uses
    // Kafka's InitProducerIdRequest/Response)
    p.producerID = p.requestProducerID()
    p.seqNumbers = make(map[int32]int64)
    return nil
}

// Send delivers a message with automatic sequence numbering
func (p *IdempotentProducer) Send(
    topic string,
    partition int32,
    key, value []byte,
) error {
    p.mu.Lock()
    seqNum := p.seqNumbers[partition]
    p.seqNumbers[partition] = seqNum + 1
    p.mu.Unlock()

    record := &ProduceRecord{
        ProducerID:  p.producerID,
        SequenceNum: seqNum,
        Topic:       topic,
        Partition:   partition,
        Key:         key,
        Value:       value,
        Timestamp:   time.Now().UnixMilli(),
    }

    // Retry loop: on network error, resend with same sequence number
    for attempt := 0; attempt < maxRetries; attempt++ {
        err := p.writeRecord(record)
        if err == nil {
            return nil // Broker acknowledged (or deduplicated)
        }
        time.Sleep(backoff(attempt))
    }
    return fmt.Errorf("produce failed after %d retries", maxRetries)
}

// Broker-side deduplication (runs on each Kafka broker)
type BrokerDedup struct {
    // Map[producerID][partition] -> last accepted sequence number
    lastSeq map[uint64]map[int32]int64
    mu      sync.RWMutex
}

func (b *BrokerDedup) AcceptRecord(r *ProduceRecord) (accepted bool, err error) {
    b.mu.Lock()
    defer b.mu.Unlock()

    if b.lastSeq[r.ProducerID] == nil {
        b.lastSeq[r.ProducerID] = make(map[int32]int64)
    }

    lastAccepted := b.lastSeq[r.ProducerID][r.Partition]

    if r.SequenceNum <= lastAccepted {
        // Duplicate: acknowledge without appending to log
        return false, nil
    }
    if r.SequenceNum != lastAccepted+1 {
        // Gap detected — possible data loss or out-of-order delivery
        return false, fmt.Errorf("sequence gap: expected %d, got %d",
            lastAccepted+1, r.SequenceNum)
    }

    b.lastSeq[r.ProducerID][r.Partition] = r.SequenceNum
    return true, b.appendToLog(r)
}
```

The broker-side check is deliberately strict: it detects gaps (`SequenceNum > lastAccepted+1`) in addition to duplicates, alerting operators to potential data loss from bugs or network reordering.

**Transactions.** For the read-process-write pattern — where a consumer reads from topic A, transforms the data, and writes to topic B — idempotent producers alone are insufficient. A failure between the write to B and the commit of A's offset would cause reprocessing. Kafka's Transaction Coordinator implements a two-phase commit protocol: `BeginTransaction` → process and send → `SendOffsetsToTransaction` → `CommitTransaction`[^3113^]. If any step fails, `AbortTransaction` rolls back all writes and offset commits.

**The cost.** EOS is not free. Benchmarks consistently measure a 2--5 ms latency increase and a 10--20% throughput reduction compared to at-least-once delivery[^3113^]. The additional round-trips for PID assignment, sequence tracking, and transaction coordination add unavoidable overhead.

| Configuration | Latency (P50) | Throughput | Duplicate Risk | Use Case |
|:---|:---:|:---:|:---:|:---|
| At-most-once (`acks=0`) | ~1 ms | Highest | High | Metrics, heartbeats, loss-tolerant telemetry |
| At-least-once (`acks=1`, no idempotency) | ~2 ms | High | Medium | Default for many applications; duplicates on retry |
| At-least-once (`acks=all`, idempotent) | ~3 ms | Medium | None (producer retry) | **Recommended default**: durable, no producer duplicates |
| Exactly-once (transactions) | ~5--7 ms | Medium-Low | None (end-to-end) | Financial transactions, compliance audit trails |

*Table 3.1: Kafka delivery guarantee tradeoffs. Latency measured on typical SSD-backed brokers; actual numbers vary by network and hardware.*

### 3.1.2 KRaft: Replacing ZooKeeper

For over a decade, Kafka relied on Apache ZooKeeper for broker metadata, leader election, and configuration storage. This external dependency was Kafka's original sin — separate operational expertise, version compatibility nightmares, watch mechanism bottlenecks at scale, and split-brain scenarios during network partitions[^3115^]. KRaft (Kafka Raft, KIP-500) replaces ZooKeeper with a native Raft-based consensus protocol running inside Kafka itself.

The benefits are substantial and well-documented. A financial services firm running a 50-node Kafka cluster eliminated 15 nodes after migrating to KRaft — they no longer needed separate ZooKeeper ensembles[^3115^]. Controller failover dropped from 5--7 seconds with ZooKeeper to **under 1 second** with KRaft, because controllers push metadata changes to brokers rather than brokers polling ZooKeeper[^3115^]. Perhaps most importantly, KRaft removed the scaling ceiling: where ZooKeeper watch mechanisms bottlenecked at hundreds of thousands of partitions, KRaft deployments successfully manage **millions of partitions**[^3115^]. As of Kafka 4.0, ZooKeeper support has been completely removed.

For HelixCluster, KRaft's lesson is unambiguous: **never depend on an external coordination service for metadata**. An embedded Raft quorum — initialized with the same binary, operated by the same team, scaled by the same automation — eliminates an entire category of operational failure modes.

### 3.1.3 Cooperative Rebalancing: No More Stop-the-World

When a consumer joins or leaves a consumer group, Kafka must reassign partitions among the remaining members. The legacy "eager" strategy revoked *all* partitions from *all* consumers, triggered a full reassignment, and only then resumed processing. This "stop-the-world" approach caused latency spikes, consumer lag, and — in the worst cases — cascading failures during deployments[^3164^].

The **CooperativeStickyAssignor**, default since Kafka 3.0, implements incremental cooperative rebalancing. Only partitions that actually need to move are revoked; processing continues uninterrupted on unaffected assignments[^3120^][^3121^]. The algorithm works in two phases:

```go
// Cooperative Rebalancing Algorithm (simplified)
type CooperativeRebalancer struct {
    currentAssignment map[string][]int // consumer -> partitions
}

func (r *CooperativeRebalancer) Rebalance(
    members []string,
    partitions []int,
) map[string][]int {
    // Phase 1: Identify only the partitions that MUST move
    // (e.g., new consumer needs some, departed consumer's partitions)
    toRevoke := r.computeRevocations(members, partitions)

    // Phase 2: Remaining partitions stay assigned —
    //          processing continues uninterrupted
    result := make(map[string][]int)
    for member, parts := range r.currentAssignment {
        result[member] = filterOut(parts, toRevoke[member])
    }

    // Phase 3: Reassign revoked partitions only
    newAssignments := r.assignRevoked(toRevoke, members, partitions)
    for member, parts := range newAssignments {
        result[member] = append(result[member], parts...)
    }

    return result
}
```

For stateful applications like Kafka Streams — where consumers maintain local RocksDB state stores derived from changelog topics — cooperative rebalancing is not a luxury but a requirement. Eager rebalancing invalidates local state and forces full changelog replay, turning a routine membership change into a multi-minute recovery event[^3120^].

| Rebalance Strategy | Partitions Revoked | Latency Spike | State Invalidation | Default Since |
|:---|:---:|:---:|:---:|:---:|
| RangeAssignor (eager) | All | High (pause all) | Full replay required | Pre-2.4 |
| RoundRobinAssignor (eager) | All | High (pause all) | Full replay required | Pre-2.4 |
| StickyAssignor (eager) | All | High (pause all) | Reduced movement | 2.x |
| **CooperativeStickyAssignor** | **Affected only** | **None on stable** | **No invalidation** | **3.0+** |

*Table 3.2: Kafka partition assignment strategies. Cooperative rebalancing eliminates stop-the-world pauses by only moving partitions that must change ownership.*

### 3.1.4 Tiered Storage: S3-Backed Infinite Retention

Kafka's original retention model was simple: keep data on broker disks until it ages out or exceeds a byte threshold. For organizations needing months or years of retention, this meant provisioning enormous disk arrays — expensive to buy, painful to expand, and often underutilized for cold data rarely accessed.

KIP-405 introduces a **pluggable remote storage tier** that offloads older log segments to object storage (S3, GCS, Azure Blob) while keeping recent data on local disk[^3142^]. The architecture is transparent to producers and consumers: the broker fetches cold segments from remote storage on demand, caching aggressively in a local shadow tier.

```
┌─────────────────────────────────────────────────────────────┐
│                    Kafka Broker                             │
│  ┌──────────────────┐          ┌──────────────────────┐    │
│  │ Hot Tier (Local) │          │ Shadow Cache (Local) │    │
│  │ Active segment   │◄────────►│ Recently fetched     │    │
│  │ 7-day retention  │          │ remote segments      │    │
│  │ P50: 1.8 ms      │          │ P50: 15 ms           │    │
│  └──────────────────┘          └──────────────────────┘    │
│           │                              ▲                  │
│           │  Older segments offloaded    │  On-demand fetch │
│           ▼                              │                  │
│  ┌───────────────────────────────────────┘                  │
│  │           RemoteStorageManager                           │
│  └────────────────────────────────────────────────────────┘ │
│                              │                              │
└──────────────────────────────┼──────────────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │   S3 / GCS / Azure  │
                    │   Cold Tier         │
                    │   365-day+ retention│
                    │   P50: 85 ms        │
                    │   $0.35/GB-month    │
                    └─────────────────────┘
```

The cost reduction is dramatic: from approximately $8/GB-month for broker-local SSD to roughly $0.35/GB-month for S3 — a **20x reduction**[^3142^]. The latency cost for cold reads is real but acceptable for analytical workloads: P50 jumps from ~2 ms for hot data to ~85 ms for S3-fetched segments[^3142^]. For HelixCluster, tiered storage means audit logs and historical telemetry can be retained indefinitely without provisioning petabytes of broker-local storage.

---

## 3.2 NATS

If Kafka is a freight train — heavy, reliable, optimized for bulk throughput — NATS is a sports motorcycle: lightweight, breathtakingly fast, and designed for agility over cargo capacity. Written in Go as a single binary with no external dependencies (no JVM, no ZooKeeper, no BookKeeper), NATS achieves **tens of millions of messages per second per node** at **microsecond latency**[^3144^]. Its memory footprint is measured in tens of megabytes, not gigabytes.

### 3.2.1 Fire-and-Forget: The Speed of Simplicity

NATS Core operates on a publish-subscribe model with a remarkably simple text-based wire protocol. Core commands — `PUB`, `SUB`, `UNSUB`, `MSG`, `CONNECT`, `PING`/`PONG` — fit on a postcard[^3144^]. Publishers send messages to **subjects** using hierarchical dot-notation (`orders.created.us-east`), and subscribers use wildcards (`orders.*.>` for recursive matching) to receive matching messages. This subject-based addressing provides *location transparency*: publishers do not know where subscribers are, and the server routes automatically.

The cost of this speed is delivery guarantees. NATS Core is strictly **at-most-once**: if a subscriber is offline when a message arrives, that message is gone forever. This is not a bug but a design choice. For telemetry, heartbeats, real-time dashboards, and metrics pipelines — where freshness matters more than completeness — at-most-once is the correct semantic[^3144^].

### 3.2.2 JetStream: Adding Durability

JetStream layers persistence, replay, and stronger delivery guarantees on top of NATS Core without sacrificing its operational simplicity. Key concepts include **Streams** (message stores with configurable retention policies), **Consumers** (stateful views that track delivery and acknowledgment), and **subject-based filtering** within streams[^3146^].

Exactly-once in JetStream uses a different mechanism than Kafka: server-side **message deduplication** combined with explicit consumer acknowledgments. The publisher includes a unique message ID; the server maintains a deduplication window (default 2 minutes) and discards duplicates[^3146^]. JetStream clustering replicates each stream via an embedded Raft group — a per-stream consensus model that isolates failure domains[^3166^].

Consumer acknowledgment in JetStream merits attention. Unlike Kafka's offset commits (which acknowledge all messages up to a point), JetStream consumers acknowledge each message individually via `ACK`, `NAK`, or `TERM` responses. A `NAK` triggers redelivery with optional backoff delay; a `TERM` tells the server the message is unprocessable and should be moved to a dead-letter queue. This per-message granularity provides finer control over poison-pill handling but adds protocol overhead compared to Kafka's batched offset commits.

### 3.2.3 Leaf Nodes: Edge-to-Cloud Topology

NATS Leaf Nodes are one of the most elegant solutions to the edge computing connectivity problem. A leaf node is a local NATS server that transparently routes messages between local clients and a remote (cloud) NATS cluster over a persistent connection[^3172^]. The design follows three principles:

1. **Local traffic stays local** — messages between edge devices never traverse the WAN
2. **Selective cloud sync** — only designated subjects flow to/from the cloud cluster
3. **Queue semantics preserved** — local consumers are served first; cloud consumers act as overflow

This is the correct pattern for edge systems: *local-first processing with controlled synchronization*, not fragile retry loops against dead cloud connections. The following configuration illustrates a typical leaf node setup:

```yaml
# /etc/nats/leaf-edge.yaml — Edge site configuration
server_name: edge_node_us_west_2a

# Local JetStream for durable edge storage
jetstream {
    store_dir: "/var/lib/nats/edge-store"
    max_memory_store: 1GB
    max_file_store: 50GB
}

# Leaf node connection to cloud cluster
leafnodes {
    remotes: [
        {
            url: "tls://connect.ngs.global:7422"
            credentials: "/etc/nats/cloud.creds"

            # Only sync these subject hierarchies to cloud
            # Local devices publish here; cloud analytics subscribes
            export_subjects: [
                "telemetry.>.aggregated",
                "alerts.critical.>",
                "audit.events.>"
            ]

            # Pull these subjects from cloud down to edge
            import_subjects: [
                "commands.devices.>",
                "config.global.>"
            ]

            # TLS configuration for WAN security
            tls: {
                cert_file: "/etc/nats/certs/edge.crt"
                key_file:  "/etc/nats/certs/edge.key"
                ca_file:   "/etc/nats/certs/ca-chain.crt"
                verify_and_map: true
            }

            # Reconnect with exponential backoff
            reconnect_interval: 5s
            max_reconnect:      100
        }
    ]
}

# Local accounts and permissions
accounts {
    EDGE: {
        jetstream: enabled
        users: [
            {user: device, password: $DEVICE_PASS}
        ]
        exports: [
            {service: "telemetry.*", response_type: Stream}
        ]
    }
}

# Clustering for local HA (3 leaf nodes at edge)
cluster {
    name: edge_us_west_2a
    listen: 0.0.0.0:6222
    routes: [
        "nats://leaf-1:6222",
        "nats://leaf-2:6222",
        "nats://leaf-3:6222"
    ]
}
```

When the WAN link fails, the edge leaf node continues operating: local devices publish to local JetStream streams, messages accumulate on disk, and when connectivity is restored, accumulated messages flow to the cloud transparently[^3172^]. This store-and-forward durability is the difference between a resilient edge system and one that generates alerts every time a rural cell tower hiccups.

---

## 3.3 Apache Pulsar

Pulsar occupies a distinct architectural position: it separates compute (stateless brokers) from storage (Apache BookKeeper), enabling independent scaling of each layer[^3145^]. Where adding a Kafka broker requires rebalancing data across nodes, adding a Pulsar broker requires only updating service discovery — no data movement at all.

### 3.3.1 BookKeeper, ZooKeeper, and Geo-Replication

**BookKeeper** provides the storage layer. Messages are organized into **ledgers** — append-only sequences of entries distributed across **bookies** (storage nodes) with configurable replication. Each ledger is replicated to multiple bookies before the broker acknowledges the write, providing durability without the tight compute-storage coupling of Kafka's design[^3145^].

**Geo-replication** is a first-class feature. Pulsar supports asynchronous replication (messages persisted locally, then forwarded to remote clusters) and synchronous replication via BookKeeper (waiting for cross-region bookie acknowledgment)[^3150^]. During network partitions, both regions continue accepting writes locally and reconcile asynchronously when connectivity returns. The consistency model is pragmatic: linearizable within a region, eventually consistent across regions[^3150^].

Pulsar's tiered storage offloads old data to object storage automatically, similar to Kafka KIP-405[^3145^]. Where Pulsar truly excels is **historical reads** — reading old data from BookKeeper or object storage at up to 3.2 GB/s, approximately 60% faster than Kafka for catch-up consumption[^3178^]. The tradeoff is latency: Pulsar's compute-storage separation adds hop latency, producing P50 latencies of 5--10 ms versus Kafka's 2--5 ms for tail-read workloads[^191^].

**Pulsar Functions** offer lightweight serverless compute that runs directly within broker processes, consuming from input topics and publishing to output topics without requiring external stream processing infrastructure like Flink or Spark[^3175^]. While convenient for simple transformations, Pulsar Functions lack the maturity and ecosystem of dedicated stream processing frameworks. For HelixCluster, the more relevant lesson is Pulsar's architectural separation of concerns: brokers handle routing, BookKeeper handles durability, and ZooKeeper (or etcd) handles coordination. This separation enables sophisticated multi-tenancy — tenants, namespaces, and topics form a resource hierarchy that SaaS providers find valuable — but at the cost of operational complexity that rivals Kafka's pre-KRaft era.

| System | P50 Latency | P99 Latency | Max Throughput | Operational Complexity | Best For |
|:---|:---:|:---:|:---:|:---:|:---|
| **Kafka** | 2--5 ms | 10--20 ms | 2M+ msg/s cluster | High (JVM, KRaft tuning) | High-throughput streaming, event sourcing |
| **NATS Core** | Sub-ms | Sub-ms | 10M+ msg/s per node | Very Low (single binary) | Real-time signaling, RPC, telemetry |
| **NATS JetStream** | 1--3 ms | 5--10 ms | 1M+ msg/s cluster | Low (single binary + persistence) | Edge-to-cloud, lightweight persistence |
| **Pulsar** | 5--10 ms | 20--50 ms | 1.8M+ msg/s cluster | High (Brokers + Bookies + ZK) | Geo-replication, long-term retention |

*Table 3.3: Messaging system comparison. Throughput measured on comparable 3-node clusters; complexity reflects deployment and operational burden.*

---

## 3.4 Messaging Lessons for HelixCluster

The systems analyzed in this chapter offer a buffet of architectural patterns, not all of which belong on HelixCluster's plate. The following table distills the highest-impact lessons:

| Pattern | Source System | HelixCluster Priority | Implementation Effort | Impact |
|:---|:---|:---:|:---:|:---|
| Idempotent producer (PID + sequence numbers) | Kafka | **P0** | 2 weeks | Eliminates duplicate writes on retry; foundational reliability primitive |
| Embedded Raft metadata quorum (no external ZK) | Kafka KRaft | **P0** | 3 weeks | 30--40% infrastructure reduction; <1s failover vs. 5--7s |
| Cooperative incremental rebalancing | Kafka 3.0+ | **P0** | 2 weeks | Eliminates stop-the-world latency spikes during membership changes |
| Subject-based hierarchical routing | NATS | **P1** | 2 weeks | Natural multi-tenancy; no explicit topic creation overhead |
| Single binary + embedded persistence | NATS | **P1** | 3 weeks | Dramatically reduces operational complexity; enables edge deployment |
| Pluggable tiered storage (hot/cold) | Kafka KIP-405 | **P1** | 4 weeks | 20x cost reduction for long-term retention; infinite audit log history |
| Leaf node edge-to-cloud topology | NATS | **P2** | 3 weeks | Local-first operation during partitions; store-and-forward resilience |
| Compute-storage separation | Pulsar | **P3** | 8+ weeks | Independent scaling; consider only for multi-tenant SaaS offering |

*Table 3.4: Messaging patterns prioritized for HelixCluster adoption. P0 = build without these and you will feel pain; P1 = significant competitive advantage; P2 = important for differentiation; P3 = future consideration.*

**Idempotent producers are non-negotiable.** This is the single most important primitive for reliable messaging. It costs nothing in the at-least-once path, eliminates an entire class of duplicate-data bugs, and makes retries safe. Every producer in HelixCluster should assign itself a PID and sequence number; every broker should maintain the deduplication table.

**Embedded Raft over external coordination.** Kafka's decade-long ZooKeeper dependency was a cautionary tale. The multi-year KRaft migration (KIP-500) consumed enormous engineering resources that could have built customer-facing features. HelixCluster must use an internal Raft quorum for all metadata from day one — event-sourced, replicated, and managed by the same binary that handles messaging[^3115^].

**Cooperative rebalancing for stateful consumers.** Stop-the-world rebalancing caused production incidents at major companies[^3164^]. For HelixCluster's consumer groups — especially those maintaining local state stores or shard caches — incremental cooperative rebalancing prevents membership changes from becoming availability events.

**At-least-once as default, exactly-once as opt-in.** The 2--5 ms latency cost and 10--20% throughput reduction of exactly-once processing is appropriate for financial transactions and compliance audit trails, but wasteful for the majority of telemetry and command traffic[^3113^]. HelixCluster should default to at-least-once with idempotent producers, making exactly-once transactions an explicit per-stream configuration.

**NATS leaf nodes for edge deployments.** In a distributed system spanning data centers, cloud regions, and edge locations, network partitions are not exceptional — they are the norm. NATS leaf nodes provide the correct abstraction: local durability, selective synchronization, and transparent store-and-forward when the WAN is interrupted[^3172^].

The messaging layer is where HelixCluster's theoretical reliability meets the messy reality of network partitions, broker restarts, and producer retries. By combining Kafka's producer idempotency with NATS's operational simplicity and subject routing, HelixCluster can offer durability guarantees that are both robust and deployable — without requiring a team of JVM-tuning specialists or ZooKeeper whisperers to keep it running.

---

*This chapter analyzed Apache Kafka's exactly-once semantics, KRaft metadata management, cooperative rebalancing, and tiered storage; NATS Core's fire-and-forget performance, JetStream durability, and leaf node edge topology; and Apache Pulsar's compute-storage separation with BookKeeper-backed geo-replication. The prioritized pattern table (Table 3.4) guides HelixCluster's messaging implementation: P0 patterns (idempotent producers, embedded Raft, cooperative rebalancing) form the non-negotiable foundation; P1 patterns (subject routing, single binary, tiered storage) provide competitive advantage; P2 and P3 patterns address specialized edge and multi-tenant scenarios.*
