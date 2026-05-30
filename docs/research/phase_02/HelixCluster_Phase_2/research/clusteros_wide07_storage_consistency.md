# Facet: Storage, Caching, Consistency & ACID Guarantees in Distributed Systems

## Key Findings

### CAP Theorem and Distributed System Trade-offs

- The CAP theorem, formulated by Eric Brewer in 2000 and formally proven in 2002, states that a distributed system can only guarantee two out of three properties: Consistency (all nodes see the same data), Availability (every request receives a response), and Partition Tolerance (the system continues operating despite network failures) [^191^] [^193^]. Because network partitions are inevitable in any distributed system, the practical choice is between CP (Consistency + Partition Tolerance) and AP (Availability + Partition Tolerance) systems [^194^] [^196^].

- PACELC extends CAP by introducing latency considerations: "Else (E), even when the system is running normally in the absence of partitions, one has to choose between latency (L) and consistency (C)" [^194^]. This is critical for Cluster OS design because it reveals that the consistency/latency trade-off exists even during normal operation, not just during partitions.

- CP systems (etcd, ZooKeeper, CockroachDB) sacrifice availability during partitions to maintain consistency. AP systems (Cassandra, DynamoDB, ScyllaDB) sacrifice strong consistency to remain available during partitions, providing eventual consistency instead [^194^] [^196^].

- **For Cluster OS**: Given the requirement for ACID guarantees and data safety across heterogeneous nodes, a CP-oriented approach is most appropriate, with Raft-based consensus for critical cluster state and tunable consistency levels for different data types.

### Consensus Algorithms: Raft, Paxos, and BFT

- **Raft** is the most widely adopted consensus algorithm for production systems, powering etcd, Consul, TiKV, and CockroachDB. In 2024, 68% of distributed systems outages stemmed from consensus layer failures, yet 72% of engineers struggle to implement it correctly [^192^]. A minimal Raft implementation in Go 1.22 achieves 12,400 consensus commits/sec with 3 nodes on AWS c7g.2xlarge instances [^192^].

- Raft uses a strong leader model with three roles (Follower, Candidate, Leader), log replication via AppendEntries RPCs, and safety guarantees that no two leaders can commit different entries for the same log index [^195^] [^201^]. The HashiCorp Raft library v1.7.0 reduced heartbeat overhead by 41% compared to v1.3.0 via batched AppendEntries RPCs [^192^].

- **Paxos and Multi-Paxos** provide the theoretical foundation for distributed consensus. Single-Decree Paxos operates in two phases (Prepare and Accept) but requires multiple round-trips per value. Multi-Paxos optimizes by eliminating repeated Prepare messages once a leader is elected [^190^]. Despite its theoretical importance, "the industry has predominantly adopted the Raft consensus algorithm over MultiPaxos for building consensus modules" due to Raft's superior auditability and debuggability [^190^].

- **Byzantine Fault Tolerance (BFT)** extends consensus to handle malicious nodes. Practical Byzantine Fault Tolerance (PBFT) reduces communication complexity from exponential to polynomial O(n^2) but suffers from scalability issues [^199^]. HotStuff achieves linear O(n) communication complexity through threshold signatures and is used in production blockchain systems [^200^]. The Raft-MPH (Multiple Pipeline HotStuff) model combines Raft's simplicity with HotStuff's multiple-pipeline architecture for improved performance [^200^].

### etcd: The Distributed Key-Value Store

- etcd is a distributed, reliable key-value store written in Go that uses the Raft consensus algorithm to maintain consistency across nodes. It is a CNCF graduated project and serves as the primary data store for Kubernetes, storing all cluster state (pods, services, secrets, configmaps) [^206^] [^208^].

- Key features: strong consistency through Raft, watch functionality for real-time updates, automatic leader election, transactional operations (compare-and-swap), and secure communication via TLS client certificates [^206^] [^215^].

- etcd is typically deployed as a 3 or 5-node cluster (odd number required to avoid split-brain scenarios during leader election). Typical write latency is 1-2ms within a datacenter. Uses linearizable reads via read index protocol to avoid stale reads from followers [^201^] [^215^].

- In Kubernetes architecture, the API server is the only client that connects to etcd (via gRPC). Other components connect to the API server, which translates requests into etcd queries [^213^].

### Consul: Service Discovery and Gossip Protocol

- Consul implements Raft for consensus (service catalog and KV store) and the Serf gossip protocol for membership management and failure detection [^195^] [^207^].

- The Serf gossip protocol has two types of gossip pools: LAN gossip pool (all nodes in a single datacenter, port 8301) and WAN gossip pool (cross-datacenter federation, port 8302) [^207^] [^212^]. Gossip enables efficient, scalable communication with compact encrypted messages over UDP and TCP.

- Consul servers elect a leader via Raft. The quorum requirement is (N/2)+1 members. If a quorum is unavailable, the cluster becomes unavailable [^195^]. Typical election timeout is 150-300ms with leader failover in under 1 second [^201^].

- HashiCorp scale testing demonstrated that "splitting a large LAN gossip pool into smaller pools with network segments reduces gossip stability risk by making the gossip converge faster" [^219^]. The `consul.serf.queue.Intent` metric was reduced by more than 90% after segment migration.

### Apache ZooKeeper and ZAB Protocol

- ZooKeeper uses the ZooKeeper Atomic Broadcast (ZAB) protocol, a totally ordered broadcast protocol optimized for read-dominant workloads. ZAB provides sequential consistency and high throughput [^216^] [^217^].

- ZAB operates in three phases: Leader Election (nodes compete to become leader), Atomic Broadcast (leader proposes changes, followers replicate by quorum), and Crash Recovery (new leader elected, committed proposals replayed) [^217^].

- ZAB is similar to Paxos but uniquely optimized for ZooKeeper, emphasizing sequential consistency. It can tolerate failures of individual nodes and scales well to large ensembles [^217^].

### CRIU: Checkpoint/Restore in Userspace

- CRIU (Checkpoint/Restore In Userspace) is a Linux software that can freeze a running container or application and checkpoint its state to disk. The saved data can be used to restore the application exactly as it was during the freeze [^147^].

- CRIU is currently integrated into OpenVZ, LXC/LXD/Incus, Docker, Podman, and Kubernetes. It supports live migration, snapshots, remote debugging, process duplication, and accelerated startup [^147^] [^209^].

- CRIU can restore TCP connections using the `TCP_REPAIR` kernel state, handle mount namespaces, cgroup states, and (with CRIU 3.12+) GPU state via NVIDIA plugin [^138^] [^140^].

- For live migration, around 90% of checkpoint time is reading user-space memory. Optimizations include iterative memory copy (copy most memory while app runs, then freeze and copy only dirty pages) [^210^].

### Distributed Filesystems

- **Lustre**: Purpose-built for HPC environments, achieves 300-330 MB/s reads/writes, scales to tens of thousands of clients and hundreds of petabytes. Uses MGS (Management Server), MDS (Metadata Server) with MDTs, and OSS (Object Storage Servers) with OSTs [^222^] [^223^]. Superior for traditional HPC with maximum sequential throughput.

- **Ceph**: General-purpose distributed storage system with excellent Kubernetes support via Rook. Uses CRUSH algorithm for data placement, supports block/file/object from one cluster. Achieves ~180 MB/s concurrent writes, scales to exabytes across thousands of nodes [^222^] [^223^].

- **BeeGFS**: HPC-oriented system demonstrating exceptional aggregate throughput of 1 TB/s in cloud-based parallel setups, emphasizing low-latency parallel access via dynamic striping [^223^].

- **GlusterFS**: Scales to ~1,000 nodes, delivers 10-50 GB/s aggregate throughput, but single-client performance limited without advanced striping [^223^].

### SQLite Distributed Replication

- **rqlite**: Distributed SQLite using Raft consensus. Configurable consistency levels (strong, eventual, none), automatic failover with sub-second election times, HTTP API adds latency but excellent horizontal read scaling [^226^].

- **dqlite**: Canonical's distributed SQLite (used in LXD). Provides C library integration with Raft consensus, strong consistency with linearizable reads/writes, low-level C integration for excellent performance [^226^].

- **Litestream**: Continuous streaming replication and point-in-time recovery. Streams SQLite WAL changes to object storage (S3, Azure Blob, GCS). Eventually consistent replicas, minimal performance impact, sub-second recovery [^226^].

### PostgreSQL Replication

- PostgreSQL offers multiple replication solutions: streaming replication (built-in, WAL shipping), logical replication (built-in, table-level granularity), and trigger-based replication (Londiste, Slony) [^232^].

- Streaming replication supports hot standby for read-only queries. Synchronous replication ensures no data loss on primary failure but adds latency. Asynchronous replication provides better performance but may lose data on failover [^232^].

- Bucardo provides asynchronous multi-master replication at table-row granularity but requires conflict resolution [^232^].

### Vector Clocks and Causality Detection

- Vector clocks track causality and ordering of events across multiple nodes. Each process maintains a vector of logical clocks; by comparing vectors, systems can determine if events are ordered or concurrent [^220^] [^221^].

- Amazon Dynamo uses vector clocks to capture causality between different versions of the same object. A vector clock is a `(node, counter)` pair. If one clock's counters are all <= another's, the first happened before the second. Otherwise, the versions are concurrent and require conflict reconciliation [^220^] [^231^].

- Downsides include client complexity (clients must implement conflict resolution) and growing vector size as more servers update data [^221^].

### CRDTs (Conflict-free Replicated Data Types)

- CRDTs allow replicas to be updated independently and concurrently without coordination, yet guarantee all replicas eventually converge to the same state. They provide Strong Eventual Consistency (SEC) [^224^] [^229^].

- Two main types: State-based CRDTs (CvRDTs) propagate full state with associative, commutative, idempotent merge functions; Operation-based CRDTs (CmRDTs) propagate operations [^224^] [^236^].

- CRDT merge operations satisfy three critical properties: Commutative (order doesn't matter), Associative (grouping doesn't matter), and Idempotent (repeating is safe) [^228^].

- CRDTs are ideal for collaborative editing, offline-first applications, distributed counters, and edge/multi-device scenarios [^228^] [^236^].

### ACID in Distributed Systems

- Two-Phase Commit (2PC) is the standard protocol for distributed ACID transactions. Phase 1 (Prepare): coordinator sends PREPARE to all participants; Phase 2 (Commit/Abort): if all agree, coordinator sends COMMIT [^245^] [^247^].

- Problems with 2PC: (1) Blocking protocol -- if coordinator crashes after Phase 1, participants hold locks indefinitely; (2) Single point of failure; (3) Poor performance with two round-trips plus durable writes; (4) Does not work well across WAN [^245^].

- Three-Phase Commit (3PC) adds a pre-commit phase to make the protocol non-blocking but adds complexity and latency. "Rarely used in practice -- adds complexity. Real-world systems use Paxos or Raft for consensus instead of 3PC" [^245^].

- Google Spanner provides externally consistent distributed transactions at global scale using the TrueTime API that exposes clock uncertainty intervals [TT.now() returns [earliest, latest]]. Uses GPS and atomic clocks for time reference [^255^].

- CockroachDB achieves distributed ACID via MultiRaft (multiple Raft groups per data range), with serializable isolation, and supports multi-region topologies for geo-distributed deployments [^246^] [^248^] [^251^].

### Distributed Caching

- Multi-layer caching architecture is essential: L1 (application-level, in-memory, 30s-5min TTL), L2 (distributed cache like Redis Cluster, 10min-24hr TTL), L3 (CDN/Edge cache, hours to days) [^249^].

- Redis Cluster uses 16,384 hash slots distributed across master nodes with client-side routing. Typical production: 3 masters + 3 replicas. Supports automatic failover via Raft-like consensus [^253^].

- Cache invalidation strategies include: direct key invalidation, pattern-based invalidation, tag-based invalidation, and event-driven invalidation using pub/sub [^258^].

- For Cluster OS, a tiered caching approach with explicit invalidation via pub/sub channels, TTL-based expiration, and write-through caching for critical data provides the best balance of performance and consistency.

---

## Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **etcd** (CNCF/CoreOS/Red Hat) | Distributed KV store using Raft; powers Kubernetes; strong consistency; 1-2ms write latency [^206^] |
| **Consul** (HashiCorp) | Service discovery + KV store; Raft + Serf gossip protocol; leader failover < 1s [^195^] [^207^] |
| **Apache ZooKeeper** | Distributed coordination; ZAB protocol; read-dominant workloads; used by Kafka, Hadoop [^216^] |
| **CockroachDB** | Distributed SQL database; MultiRaft consensus; serializable isolation; multi-region [^246^] |
| **Google Spanner** | Globally distributed ACID database; TrueTime API; GPS/atomic clock synchronization [^255^] |
| **Redis Cluster** | Distributed cache; 16384 hash slots; gossip protocol; async replication; auto-failover |
| **CRIU** (Virtuozzo/community) | Process checkpoint/restore; live migration; integrated into Docker, Podman, Kubernetes [^147^] |
| **Ceph** (Red Hat) | Unified distributed storage (block/file/object); excellent K8s support via Rook [^222^] |
| **Lustre** (OpenSFS/Intel) | HPC parallel filesystem; 300-330 MB/s; tens of thousands of clients; bare-metal HPC [^222^] |
| **rqlite** | Distributed SQLite with Raft; HTTP API; auto-failover; horizontal read scaling [^226^] |
| **dqlite** (Canonical) | Embedded distributed SQLite; C library; used by LXD; strong consistency [^226^] |
| **PostgreSQL** | Streaming replication; logical replication; 2PC support for distributed transactions [^232^] |
| **PBFT / HotStuff** | BFT consensus for malicious nodes; PBFT O(n^2); HotStuff O(n) with threshold sigs [^199^] [^200^] |

---

## Trends & Signals

- **Raft dominance over Paxos**: By 2026, an estimated 80% of new distributed SQL databases will adopt Raft over Paxos, driven by Raft's auditability and easier debuggability for on-call engineers [^192^]. This trend favors etcd/Consul-style architectures for Cluster OS.

- **MultiRaft for scalability**: CockroachDB's MultiRaft manages all of a node's ranges as a group, reducing heartbeat overhead by exchanging heartbeats once per tick per node pair regardless of shared ranges [^246^]. This pattern is essential for scaling consensus to large clusters.

- **Cloud-native HPC storage shift**: Modern HPC workloads increasingly use object storage (S3) alongside POSIX filesystems. Ceph's unified architecture supports both, increasing its relevance for cluster computing [^222^].

- **SQLite as distributed database**: rqlite and dqlite demonstrate that even lightweight embedded databases can provide strong consistency via Raft consensus, making them attractive for edge and resource-constrained cluster nodes [^226^].

- **CRIU integration with Kubernetes**: Kubernetes has an active WG Checkpoint Restore working on pod live migration. KubeCon EU 2026 featured "Ctrl-X, Ctrl-V Your Pods" sessions [^147^]. This signals production readiness for container migration.

- **Non-blocking consensus innovations**: 3PC is rarely used; real systems prefer Paxos/Raft. HotStuff's linear communication complexity via threshold signatures is being adopted for blockchain and BFT scenarios [^200^].

- **PACELC over CAP**: Modern analysis uses PACELC (Partition/Availability/Consistency Else Latency/Consistency) rather than pure CAP, because it captures the latency-consistency trade-off during normal operation [^194^].

---

## Controversies & Conflicting Claims

- **CP vs AP for Cluster OS**: CP systems (etcd, ZooKeeper) argue that consistency is essential for correct cluster coordination and that temporary unavailability during partitions is preferable to inconsistent state [^196^]. AP proponents (DynamoDB, Cassandra) argue that availability is paramount and eventual consistency is sufficient for most applications [^194^]. For a Cluster OS managing critical infrastructure, the CP approach is strongly favored.

- **2PC vs consensus protocols**: Some traditional database practitioners advocate for 2PC/3PC for distributed transactions [^247^], but modern distributed systems overwhelmingly prefer Raft/Paxos-based consensus due to better fault tolerance and non-blocking properties [^245^]. The consensus view: "Real-world systems use Paxos or Raft for consensus instead of 3PC" [^245^].

- **Vector clocks vs CRDTs**: Vector clocks require client-side conflict resolution and grow in size with node count [^221^]. CRDTs handle conflicts automatically through mathematically defined merge operations [^228^]. However, CRDTs are limited to specific data types and may not suit all applications. Vector clocks remain more general-purpose for causality tracking.

- **Ceph vs Lustre for HPC**: Lustre advocates argue maximum sequential throughput for HPC; Ceph advocates argue unified storage and cloud-native integration. The consensus: "Lustre remains the superior choice for traditional HPC environments... Ceph CephFS is the better option for cloud-native or Kubernetes-based HPC" [^222^].

---

## Recommended Deep-Dive Areas

1. **MultiRaft implementation for Cluster OS**: CockroachDB's MultiRaft pattern is essential for scaling consensus to large clusters where each node participates in many consensus groups. The heartbeat aggregation and range management techniques warrant detailed study [^246^].

2. **CRIU integration for container migration**: With active Kubernetes WG Checkpoint Restore work, CRIU-based live migration is approaching production readiness. Deep investigation needed for process snapshot consistency across heterogeneous nodes [^147^].

3. **Hybrid consistency model design**: Different data types need different consistency guarantees. Cluster state needs strong consistency (Raft), metrics can use eventual consistency (gossip), and collaborative data can use CRDTs. A unified framework for selecting consistency levels per data type would be valuable.

4. **BFT extensions for untrusted nodes**: If Cluster OS needs to handle potentially malicious or compromised nodes, HotStuff-based BFT consensus with threshold signatures provides O(n) communication complexity [^200^]. The Raft-MPH hybrid model warrants investigation.

5. **Distributed caching with strong invalidation**: For the Cluster OS caching layer, a Redis Cluster-based approach with explicit pub/sub invalidation, write-through for critical data, and CRDT-based merge for cache coherency across partitions would provide both performance and correctness.

---

## Raw Evidence Log

### CAP Theorem and PACELC

**Claim:** The CAP theorem states that in a distributed system, you can only guarantee two out of three: Consistency, Availability, and Partition Tolerance. Network partitions are inevitable, so the real choice is CP vs AP.
**Source:** ScyllaDB Glossary
**URL:** https://www.scylladb.com/glossary/cap-theorem/
**Date:** 2025-11-05
**Excerpt:** "In a distributed database, there is no way to avoid system partitions. So, although CAP theorem stating a CA distributed database is possible exists, there is currently no true CA distributed database system."
**Context:** ScyllaDB is an AP/EL system prioritizing availability and low latency
**Confidence:** High

**Claim:** PACELC extends CAP by introducing latency: even in the absence of partitions, one must choose between latency and consistency.
**Source:** ScyllaDB Glossary
**URL:** https://www.scylladb.com/glossary/cap-theorem/
**Date:** 2025-11-05
**Excerpt:** "PACELC reveals that systems tend either towards strong consistency or latency sensitivity. Even in the absence of partitioning, a trade-off between consistency and latency exists."
**Context:** Critical insight for Cluster OS design - consistency/latency trade-off exists during normal operation
**Confidence:** High

---

### Raft Consensus Algorithm

**Claim:** In 2024, 68% of distributed systems outages stemmed from consensus layer failures, yet 72% of engineers struggle to implement Raft correctly.
**Source:** dev.to - The Ultimate Guide to Raft
**URL:** https://dev.to/johalputt/the-ultimate-guide-to-raft-everything-you-need-o04
**Date:** 2024
**Excerpt:** "In 2024, 68% of distributed systems outages stem from consensus layer failures, according to the Chaos Engineering Institute's annual report. Raft is the most widely adopted consensus algorithm for production systems, powering etcd, TiKV, and CockroachDB, yet 72% of engineers we surveyed still struggle to implement it correctly without relying on off-the-shelf libraries."
**Context:** Strong case for using mature Raft libraries (etcd, HashiCorp) rather than implementing from scratch
**Confidence:** Medium (the specific statistics may be from a single source)

**Claim:** A minimal Raft implementation in Go 1.22 achieves 12,400 consensus commits/sec with 3 nodes on 16 vCPU AWS c7g.2xlarge instances, 22% faster than etcd 3.5.12.
**Source:** dev.to - The Ultimate Guide to Raft
**URL:** https://dev.to/johalputt/the-ultimate-guide-to-raft-everything-you-need-o04
**Date:** 2024
**Excerpt:** "A minimal Raft implementation in Go 1.22 achieves 12,400 consensus commits/sec with 3 nodes on 16 vCPU AWS c7g.2xlarge instances, 22% faster than etcd 3.5.12 under identical workloads."
**Context:** Performance baseline for Raft-based consensus
**Confidence:** Medium

**Claim:** By 2026, 80% of new distributed SQL databases will adopt Raft over Paxos.
**Source:** dev.to - The Ultimate Guide to Raft
**URL:** https://dev.to/johalputt/the-ultimate-guide-to-raft-everything-you-need-o04
**Date:** 2024
**Excerpt:** "By 2026, 80% of new distributed SQL databases will adopt Raft over Paxos, driven by Raft's auditability and easier debuggability for on-call engineers."
**Context:** Industry trend strongly favoring Raft
**Confidence:** Medium (projection)

---

### Consul and Gossip Protocol

**Claim:** Consul uses Serf gossip protocol for membership management with LAN and WAN gossip pools, and Raft for consensus.
**Source:** HashiCorp Consul Documentation
**URL:** https://developer.hashicorp.com/consul/docs/concept/gossip
**Date:** 2025-11-05
**Excerpt:** "Serf is a gossip protocol that Consul implements in datacenter operations. Consul uses it to manage membership and broadcast messages to the cluster. There are two types of gossip pools available to Consul: The LAN gossip pool... The WAN gossip pool..."
**Context:** Official documentation on Consul's dual-protocol approach
**Confidence:** High

**Claim:** Splitting large LAN gossip pools into network segments reduced the `consul.serf.queue.Intent` metric by more than 90%.
**Source:** HashiCorp Blog - Consul Scale Test Report
**URL:** https://www.hashicorp.com/en/blog/consul-scale-test-report-to-observe-gossip-stability
**Date:** (no date)
**Excerpt:** "The primary goal was to observe a reduction in the `consul.serf.queue.Intent` metric, which was reduced by more than 90% post-20-segment migration and even further reduced by 4-5% after the 64-segment configuration."
**Context:** Production-validated technique for scaling gossip in large clusters
**Confidence:** High

---

### etcd Implementation

**Claim:** etcd uses Raft consensus, provides watch functionality, automatic leader election, and transactional operations. It is the primary data store for Kubernetes.
**Source:** ByteSizeGo Blog
**URL:** https://www.bytesizego.com/blog/the-distributed-key-value-store-that-powers-kubernetes
**Date:** 2026-02-10
**Excerpt:** "Etcd uses the Raft consensus algorithm to maintain consistency across nodes. This means even if some nodes fail, your data stays consistent and available. The key features are: Strong consistency through Raft, Watch functionality for real-time updates, Automatic leader election, Transactional operations (compare-and-swap)"
**Context:** Comprehensive overview of etcd's key features for distributed systems
**Confidence:** High

**Claim:** In Kubernetes, the API server is the only client that connects to etcd via gRPC. Other components connect to the API server.
**Source:** DigiHunch
**URL:** https://www.digihunch.com/2022/06/etcd-the-key-value-store-for-kubernetes/
**Date:** 2025-04-02
**Excerpt:** "In Kubernetes architecture, etcd is the data store. It stores the desired state of Kubernetes object. API server is the only client that connects to etcd (via gRPC protocol)."
**Context:** Important architectural pattern for Cluster OS - single point of access to distributed state
**Confidence:** High

---

### Apache ZooKeeper and ZAB

**Claim:** ZAB is a totally ordered broadcast protocol optimized for ZooKeeper, emphasizing sequential consistency and high throughput for read-dominant workloads.
**Source:** Medium - Understanding the ZAB Protocol
**URL:** https://medium.com/@jitenderkmr/understanding-the-zab-protocol-the-foundation-of-apache-zookeeper-d335dbae3ec9
**Date:** 2024-12-15
**Excerpt:** "While similar to Paxos in many ways, ZAB is uniquely optimized for Zookeeper, emphasizing sequential consistency and high throughput for read-dominant workloads."
**Context:** ZAB as an alternative to Raft/Paxos for read-heavy coordination workloads
**Confidence:** High

---

### CRIU for Live Migration

**Claim:** CRIU can freeze a running container and checkpoint its state to disk, enabling live migration, snapshots, and remote debugging.
**Source:** CRIU Official Website
**URL:** https://criu.org/Main_Page
**Date:** 2026-04-27
**Excerpt:** "Checkpoint/Restore In Userspace, or CRIU (pronounced kree-oo), is a Linux software. It can freeze a running container (or an individual application) and checkpoint its state to disk. The data saved can be used to restore the application and run it exactly as it was during the time of the freeze."
**Context:** Core capability for Cluster OS node migration and process mobility
**Confidence:** High

**Claim:** CRIU is integrated into OpenVZ, LXC/LXD/Incus, Docker, Podman, and Kubernetes. Kubernetes WG Checkpoint Restore is working on pod live migration.
**Source:** CRIU Official Website
**URL:** https://criu.org/Main_Page
**Date:** 2026-04-27
**Excerpt:** "It is currently used by (integrated into) OpenVZ, LXC/LXD/Incus, Docker, Podman, Kubernetes and other software... KubeCon EU 2026: Ctrl-X, Ctrl-V Your Pods: WG Checkpoint Restore in Kubernetes"
**Context:** CRIU is production-ready and being actively integrated into Kubernetes
**Confidence:** High

**Claim:** CRIU can restore TCP connections using TCP_REPAIR kernel state.
**Source:** Liu Junming's Notes on CRIU
**URL:** http://liujunming.github.io/2025/10/01/Notes-about-CRIU-Checkpoint-Restore-In-Userspace/
**Date:** 2025-10-01
**Excerpt:** "CRIU can also restore the state of TCP connections, a crucial feature for live migration of distributed applications. The Linux kernel introduced a new TCP connection state, `TCP_REPAIR`, for that purpose."
**Context:** Critical for transparent live migration of networked applications
**Confidence:** High

---

### Distributed Filesystems

**Claim:** Lustre achieves 300-330 MB/s reads/writes and scales to tens of thousands of clients; Ceph achieves ~180 MB/s concurrent writes with excellent Kubernetes support.
**Source:** Grokipedia - Comparison of distributed file systems
**URL:** https://grokipedia.com/page/Comparison_of_distributed_file_systems
**Date:** 2026-01-14
**Excerpt:** "Lustre, a parallel file system often used in high-performance computing (HPC), supports scalability to tens of thousands of client nodes and hundreds of petabytes of storage, with sequential read throughputs up to 330 MB/s... Ceph leverages the CRUSH algorithm... with throughput peaks around 180 MB/s for concurrent writes"
**Context:** Performance comparison for selecting cluster filesystem
**Confidence:** High

**Claim:** Lustre remains superior for traditional HPC; Ceph is better for cloud-native or Kubernetes-based HPC.
**Source:** OneUptime Blog - Ceph vs Lustre
**URL:** https://oneuptime.com/blog/post/2026-03-31-rook-compare-ceph-vs-lustre-hpc-storage/view
**Date:** 2026-03-31
**Excerpt:** "Lustre remains the superior choice for traditional HPC environments requiring maximum sequential throughput and parallel MPI-IO performance at scale. Ceph CephFS is the better option for cloud-native or Kubernetes-based HPC."
**Context:** Clear guidance for Cluster OS filesystem selection based on workload type
**Confidence:** High

---

### SQLite Distributed Replication

**Claim:** rqlite provides distributed SQLite with Raft consensus, configurable consistency levels, and automatic failover with sub-second election times.
**Source:** Onidel Blog - LiteFS vs Litestream vs rqlite vs dqlite
**URL:** https://onidel.com/blog/sqlite-replication-vps-2025
**Date:** 2025-10-01
**Excerpt:** "rqlite implements a distributed relational database using SQLite as the storage engine and the Raft consensus protocol for clustering and leader election... Configurable consistency levels (strong, eventual, none)... Automatic failover through Raft consensus with sub-second election times"
**Context:** Attractive option for lightweight cluster nodes needing ACID guarantees
**Confidence:** High

**Claim:** dqlite provides strong consistency with linearizable reads/writes via C library integration with Raft.
**Source:** Onidel Blog - LiteFS vs Litestream vs rqlite vs dqlite
**URL:** https://onidel.com/blog/sqlite-replication-vps-2025
**Date:** 2025-10-01
**Excerpt:** "dqlite is Canonical's distributed SQLite implementation... Strong consistency with linearizable reads and writes... Low-level C integration provides excellent performance... Best for: System-level applications and infrastructure software requiring embedded distributed SQLite"
**Context:** Ideal for embedded distributed state in Cluster OS agents
**Confidence:** High

---

### PostgreSQL Replication

**Claim:** PostgreSQL supports multiple replication solutions: streaming replication (WAL shipping), logical replication (table-level), trigger-based (Slony), and async multi-master (Bucardo).
**Source:** PostgreSQL Official Documentation
**URL:** https://www.postgresql.org/docs/current/different-replication-solutions.html
**Date:** 2026-05-14
**Excerpt:** "Table 26.1 summarizes the capabilities of the various solutions... Popular examples: built-in streaming repl., built-in logical repl., pglogical, Londiste, Slony, pgpool-II, Bucardo"
**Context:** Comprehensive replication options for PostgreSQL-based cluster services
**Confidence:** High

---

### Vector Clocks

**Claim:** Vector clocks help determine whether two versions of data are ordered (one happened after another) or concurrent (conflict exists).
**Source:** Dev.to - How Vector Clocks Work in Distributed Systems
**URL:** https://dev.to/rajat10/how-vector-clocks-work-in-distributed-systems-5b4j
**Date:** 2026-03-06
**Excerpt:** "A vector clock helps determine whether two versions of data are: Ordered (one happened after another), Concurrent (a conflict exists)... Two versions are siblings if neither dominates the other."
**Context:** Foundation for causality tracking in distributed systems
**Confidence:** High

**Claim:** Amazon Dynamo uses vector clocks and resolves conflicts at read-time, returning multiple versions to the client when causality cannot be determined.
**Source:** Educative.io - How are vector clocks used in Dynamo?
**URL:** https://www.educative.io/answers/how-are-vector-clocks-used-in-dynamo
**Date:** 2026-05-27
**Excerpt:** "Dynamo uses vector clocks to capture causality between different versions of the same object... Dynamo resolves these conflicts at read-time... Either server gets a read request for key k1. It sees the same key with different versions [A:3] and [A:2][B:1], but it does not know which one is newer. It returns both and tells the client to figure out the version."
**Context:** Practical application of vector clocks in production distributed database
**Confidence:** High

---

### CRDTs

**Claim:** CRDTs guarantee that replicas converge to the same state without coordination, providing Strong Eventual Consistency (SEC).
**Source:** arXiv - Approaches to Conflict-free Replicated Data Types
**URL:** https://arxiv.org/html/2310.18220v2
**Date:** (no date)
**Excerpt:** "A CRDT is exposed as a standard data type, providing operations. Each data type object is replicated and accessed locally... Operations are always available, not depending on synchronization. A CRDT object is highly available, even under partitions, with essentially zero operation response time (only local computation)."
**Context:** CRDTs as a fundamental building block for partition-tolerant data types
**Confidence:** High

**Claim:** CRDT merge operations must satisfy three properties: Commutative, Associative, and Idempotent.
**Source:** Dev.to - Conflict-free Replicated Data Types (CRDTs)
**URL:** https://dev.to/learnwithvikzzy/conflict-free-replicated-data-types-crdts-ij6
**Date:** 2026-02-11
**Excerpt:** "CRDTs rely on mathematically defined merge operations with three critical properties: 1. Commutative: merge(A, B) = merge(B, A), 2. Associative: merge(A, merge(B, C)) = merge(merge(A, B), C), 3. Idempotent: merge(A, A) = A"
**Context:** Mathematical foundation ensuring convergence
**Confidence:** High

---

### ACID Distributed Transactions

**Claim:** 2PC is blocking and has a single point of failure (coordinator). 3PC is rarely used; real-world systems use Paxos or Raft for consensus instead.
**Source:** TechInterview.org - System Design: Distributed Transactions
**URL:** https://www.techinterview.org/post/3233464776/system-design-distributed-transaction/
**Date:** (no date)
**Excerpt:** "Problems with 2PC: (1) Blocking protocol -- if the Coordinator crashes after Phase 1 but before Phase 2, participants are stuck holding locks indefinitely. (2) Single point of failure... Three-Phase Commit (3PC): Adds a pre-commit phase... Rarely used in practice -- adds complexity and latency. Real-world systems use Paxos or Raft for consensus instead of 3PC."
**Context:** Clear guidance to use Raft/Paxos rather than 2PC/3PC for Cluster OS consensus
**Confidence:** High

**Claim:** CockroachDB uses MultiRaft -- each data range has its own Raft group, and heartbeats are aggregated across ranges sharing node pairs.
**Source:** CockroachDB Architecture Blog
**URL:** https://www.cockroachlabs.com/blog/distributed-database-architecture/
**Date:** 2025-05-07
**Excerpt:** "In CockroachDB, the data is divided into ranges, each with its own consensus group... MultiRaft manages all of a node's ranges as a group. This means that each pair of nodes only needs to exchange heartbeats once per tick, regardless of how many ranges they share."
**Context:** Key architectural pattern for scaling Raft to large clusters
**Confidence:** High

---

### Google Spanner and TrueTime

**Claim:** Spanner uses the TrueTime API with GPS and atomic clocks to assign globally-meaningful commit timestamps, providing external consistency for distributed transactions.
**Source:** MIT Course Slides on Spanner
**URL:** https://people.csail.mit.edu/matei/courses/2015/6.S897/slides/spanner.pdf
**Date:** (no date)
**Excerpt:** "To support externally consistent distributed transactions at a global scale, it uses the TrueTime API that exposes clock uncertainty... TT.now() is guaranteed to include the absolute time within the interval. There are two forms of time reference, GPS and atomic clocks, because they have different modes of failure."
**Context:** Gold standard for distributed ACID, though requires specialized hardware
**Confidence:** High

---

### Distributed Caching

**Claim:** Multi-layer caching (L1 application, L2 distributed like Redis Cluster, L3 CDN) with event-driven invalidation provides scalable cache consistency.
**Source:** Ayush Mourya Blog - Distributed Caching Strategies
**URL:** https://ayushmourya.com/posts/system-design-distributed-caching-strategies
**Date:** 2025-08-05
**Excerpt:** "The key to effective distributed caching is implementing multiple cache layers, each optimized for different access patterns: Layer 1: Application-Level Cache (L1)... Layer 2: Distributed Cache (L2)... Layer 3: CDN/Edge Cache (L3)..."
**Context:** Architecture pattern for Cluster OS caching subsystem
**Confidence:** High

**Claim:** Redis Cluster distributes data across 16,384 hash slots with client-side routing. Production setup typically uses 3 masters + 3 replicas.
**Source:** HostMyCode - Distributed Caching Strategies 2026
**URL:** https://www.hostmycode.com/blog/distributed-caching-strategies-high-performance-web-applications-2026
**Date:** 2026-04-20
**Excerpt:** "Redis Cluster distributes data across multiple nodes using consistent hashing. Each key gets assigned to one of 16,384 hash slots, and these slots are distributed across master nodes. A typical production setup uses six nodes: three masters and three replicas."
**Context:** Production-proven distributed caching architecture
**Confidence:** High

---

### Byzantine Fault Tolerance

**Claim:** PBFT handles up to f Byzantine faults in a 3f+1 node system but suffers from O(n^2) communication complexity. HotStuff achieves linear O(n) complexity via threshold signatures.
**Source:** ScienceDirect - Practical Byzantine improved algorithm
**URL:** https://www.sciencedirect.com/science/article/abs/pii/S1389128625002750
**Date:** 2025-05-09
**Excerpt:** "PBFT reduces the communication complexity of Byzantine fault tolerance (BFT) from exponential to polynomial level for the first time... However, with the number of nodes increasing, the quadratic communication complexity causes performance bottleneck of PBFT. To address the problem, HotStuff and its variants achieve a linear communication complexity through the utilization of the threshold signature technology."
**Context:** BFT options for Cluster OS if malicious node tolerance is required
**Confidence:** High

---

## Cross-Reference with Phase 1 Context

The Phase 1 context highlighted several requirements that directly inform our Cluster OS storage and consistency design:

1. **Redis Cluster (16384 hash slots, gossip protocol, auto-failover)**: This pattern can serve as the distributed caching layer. Redis's asynchronous replication is acceptable for cache data but not for critical cluster state. For cluster state, etcd's Raft-based strong consistency is more appropriate.

2. **ACID guarantees required**: CockroachDB's MultiRaft approach with serializable isolation provides the best reference architecture. For lighter-weight scenarios, dqlite (embedded distributed SQLite with Raft) offers strong consistency with lower overhead.

3. **Nodes joining/leaving/going offline**: etcd handles dynamic membership changes via joint consensus (transitioning through C_old + C_new configurations) [^201^]. Consul's gossip protocol with Lifeguard enhancements handles failure detection in dynamic clusters [^207^]. CRIU enables stateful migration of running processes when nodes go offline [^147^].

4. **Unified storage (NFS, Lustre, GFS)**: For POSIX-compatible shared storage, Ceph provides the best Kubernetes integration via Rook. For maximum HPC throughput, Lustre remains the leader. For the Cluster OS itself, a metadata service (like etcd or Consul KV store) provides the coordination layer while data is stored on the distributed filesystem.

5. **Powerful caching with validation/invalidation**: A multi-layer approach using L1 (in-process), L2 (Redis Cluster with pub/sub invalidation), and L3 (disk/page cache) provides comprehensive caching. Event-driven invalidation via pub/sub channels ensures consistency across cache nodes [^258^].

---

*Research compiled from 14 independent search areas across academic papers, official documentation, technical blogs, and production system documentation.*
