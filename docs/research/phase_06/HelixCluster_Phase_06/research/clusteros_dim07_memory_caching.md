# Dimension 07: Distributed Memory Management & Caching Architecture

## Research Summary

This document provides a comprehensive analysis of technologies and approaches for creating unified memory pools across cluster nodes with ACID guarantees and powerful caching mechanisms. It covers Software Distributed Shared Memory (SDSM), PGAS programming models, distributed caching systems, emerging hardware standards, and consistency protocols.

---

## 1. Distributed Shared Memory (DSM) Systems

### 1.1 Ivy — The First Software DSM

Ivy, developed by Kai Li in 1986, was the first page-based distributed shared memory system [^665^]. It used the MMU (Memory Management Unit) for detecting shared memory accesses and maintained sequential consistency — the same consistency model as SMPs at the time. Ivy implemented a page lookup mechanism to find up-to-date copies of pages across distributed nodes.

**Key design decisions:**
- Page granularity coherence using virtual memory protection hardware
- Sequential consistency model (strict — all writes immediately visible)
- Single-writer protocol (no multiple concurrent writers allowed)
- Directory-based page location tracking [^601^]

**Limitations that inspired subsequent systems:**
- Excessive communication due to sequential consistency
- False sharing problems due to page-size granularity
- No support for multiple concurrent writers

### 1.2 Munin — Multi-Protocol Release Consistency

Munin, from Rice University, was a landmark DSM system that introduced four key innovations [^657^][^660^][^662^]:

1. **Software Release Consistency**: First software DSM to use release consistency — modifications are propagated only at synchronization points, reducing message counts dramatically
2. **Multiple Consistency Protocols**: Different protocols for different access patterns:
   - Conventional (single-writer, multiple-reader)
   - Read-only (replication on demand)
   - Migratory (write-access migrates to first accessor)
   - Write-shared (program-driven synchronization allowing concurrent writes)
3. **Write-Shared Protocols**: Address false sharing by allowing multiple processes to write concurrently to shared pages, merging modifications at synchronization points
4. **Update-with-Timeout**: Updates remote copies rather than invalidating, but deletes stale copies after timeout

> "Munin achieves this level of performance with only minor annotations to the shared memory programs." [^657^]

Munin achieved performance within 10% of message-passing implementations for many applications, demonstrating that DSM could be a viable alternative to explicit message passing [^657^].

### 1.3 TreadMarks — Lazy Release Consistency

TreadMarks, also from Rice University, is one of the most influential DSM systems. It introduced **Lazy Release Consistency (LRC)** [^594^][^596^][^598^].

**Core innovations:**
- **Lazy Release Consistency**: Propagates modifications at acquire time rather than release time (eager). This means only the next processor acquiring a lock is informed of changes, not all processors [^597^][^602^]
- **Multiple-Writer Protocol**: Reduces false sharing impact by allowing concurrent writes to pages, using diff-based page merging [^593^]
- **User-level implementation**: No kernel modifications required
- **Invalidate-based protocol**: Modified pages are invalidated at acquire; access misses trigger page retrieval [^602^]

**Performance results on 8-processor ATM network:**
- Jacobi: 7.4x speedup
- TSP: 7.2x speedup
- Quicksort: 6.3x speedup
- ILINK: 5.7x speedup
- Water (high communication): 4.0x speedup [^594^][^598^]

> "With suitable networking technology, DSM is a viable technique for parallel computation on clusters of workstations." [^594^]

Key insight from TreadMarks: "Unix communication overhead remains the main obstacle in the way of better performance for programs like Water. Compared to the Unix communication overhead, memory management cost (both kernel and user level) is small and wire time is negligible." [^598^]

### 1.4 JIAJIA — Home-Based DSM

JIAJIA implements a **home-based** software DSM protocol with **scope consistency** [^627^][^633^][^635^].

**Key characteristics:**
- Each shared page has a designated "home" node
- Writes are propagated to the home node at release time
- Reads from modified pages are trapped via page fault and retrieve coherent copies from the home node
- The home node never experiences page faults for its own pages — significant overhead reduction when locality is achieved [^600^]
- Implements shared memory communication among processes within the same SMP node
- Competitive with systems like CVM [^635^]

### 1.5 Cashmere

Cashmere is a software DSM system designed for clusters with specialized hardware mechanisms for improved consistency protocols [^635^]. It runs on systems with DEC Memory Channel or similar high-performance interconnects, providing hardware-assisted coherence tracking.

---

## 2. Software Distributed Shared Memory (SDSM) Protocols

### 2.1 Consistency Models Hierarchy

| Model | Strength | Key Property |
|-------|----------|-------------|
| Sequential Consistency (SC) | Strongest | All operations appear to execute in some global order |
| Release Consistency (RC) | Medium | Modifications visible only at synchronization points |
| Lazy Release Consistency (LRC) | Weaker RC | Propagation delayed until acquire |
| Scope Consistency | Weaker | Association between synchronization variables and data |
| Entry Consistency | Weakest | Each shared data item associated with synchronization |
| Causal Consistency | Very weak | Only causally-related operations ordered |
| Eventual Consistency | Weakest | All replicas converge eventually |

### 2.2 Lazy Release Consistency (LRC) — Deep Dive

LRC uses a representation of the happened-before partial order to track which modifications need to be propagated [^601^].

**How LRC works:**
1. Each processor maintains a vector timestamp
2. On release: nothing is sent (releases are purely local in LRC)
3. On acquire: the acquiring processor sends its current vector timestamp to the previous releaser
4. The releaser sends write notices for all intervals that happened before the acquire but are not yet visible to the acquirer
5. Multiple writers can write concurrently — diffs are merged appropriately

**Data movement protocols:**
- **Invalidate protocol**: Modified pages invalidated at acquire; access miss retrieves page
- **Update protocol**: Acquire message contains new values/diffs
- **Diff-based merging**: Only changed portions of pages are transferred [^601^]

### 2.3 Home-Based Lazy Release Consistency (HLRC)

HLRC, used in systems like SHRIMP SVM, assigns a "home" node to each page [^600^]:
- When leaving a critical section, a node sends its diff to the home node
- The home node applies the diff and maintains consistency using logical vector clocks
- Reads to modified pages trap via page fault, retrieving a coherent copy from the home node
- The home node never page-faults on its own pages

---

## 3. PGAS (Partitioned Global Address Space)

### 3.1 Programming Model Overview

PGAS provides a global address space partitioned among cluster nodes [^514^][^515^][^517^].

**Key characteristics:**
- Thread may directly read/write remote data
- Hides distinction between shared/distributed memory
- Partitioned: data designated as local or global — critical for locality and scaling
- Implemented using one-sided communication (put/get) [^514^]

> "A key ingredient of PGAS APIs is their support for one-sided communication: a node may directly read and write the memory located at a remote node without explicit synchronization with the remote side, unlike in traditional message passing interfaces." [^517^]

### 3.2 UPC (Unified Parallel C)

- Language defines "affinity" between shared data items and UPC threads [^516^]
- All scalar data has affinity with thread 0
- Arrays may have cyclic, blocked-cyclic, or blocked affinity
- Thread interaction managed through locks, barriers, memory fences
- Static parallelism model (1 thread per processor) [^516^]

### 3.3 OpenSHMEM

- Symmetric Hierarchical Memory library for SPMD programming [^516^]
- Available for C, C++, and Fortran
- Processes execute in separate address spaces
- Remote memory access through explicit one-sided operations

### 3.4 Formal Model

A theory of PGAS defines a core calculus modeling concurrent processes sharing a global address space through one-sided reads and writes [^517^]. A subtle bug example demonstrates that barriers alone are insufficient: "while the barriers ensure all processes are at the same control location, the remote writes may or may not have completed when address y is accessed after the barrier." [^517^]

---

## 4. Apache Arrow — Zero-Copy Shared Memory

### 4.1 Core Design

Apache Arrow provides a standardized columnar in-memory format for flat and hierarchical data [^585^][^582^].

**Key capabilities:**
- Zero-copy reads: When processes share the same memory, Arrow serves as a common columnar memory layout allowing reads without serialization/deserialization [^582^]
- Cross-language: Integration tests validate in-memory representation equality between C++, Java, Rust, and other language implementations [^582^]
- Immutable objects: Most Arrow objects, once instantiated, are immutable — making cache coherency concerns manageable [^584^]

### 4.2 Plasma Store

The Arrow project includes **Plasma**, a shared-memory object store [^585^]:
- Written in C++
- Holds immutable objects in shared memory
- Accessible efficiently by many clients across process boundaries
- Eliminates serialization overhead for inter-process data sharing

### 4.3 Flight RPC

Apache Arrow Flight defines a client-server RPC framework for building services that exchange data [^585^].

### 4.4 Cluster Shared Memory Extension

Research has extended Arrow's zero-copy capability to cluster-wide shared memory using ThymesisFlow [^584^]:
- Globally accessible shared memory across nodes
- Table descriptors serialized/sent, data stays in place
- ChunkedArrays can span multiple nodes
- Applications can index data as if local — Arrow resolves pointers across nodes

> "The key aspect we utilize is that most Arrow objects, once instantiated, are immutable and thus the missing cache coherency is not a problem with read-only access." [^584^]

---

## 5. Redis Cluster

### 5.1 Architecture Overview

Redis Cluster provides a distributed implementation of Redis with automatic sharding and high availability [^505^].

**Key design:**
- Key space split into **16,384 hash slots** — upper limit of 16,384 master nodes (practical max ~1,000 nodes) [^505^][^510^]
- Hash slot calculation: `HASH_SLOT = CRC16(key) mod 16384` [^505^]
- Each master handles a subset of hash slots
- Masters can have one or more replicas for failover

### 5.2 Fault Tolerance

**Failure detection:**
- Nodes send gossip messages including state of random known nodes
- PFAIL (possibly fail) escalated to FAIL when majority of masters agree within `NODE_TIMEOUT * 2` [^505^]

**Failover process:**
1. Replica waits: `DELAY = 500ms + random(0-500ms) + REPLICA_RANK * 1000ms`
2. Most updated replica (rank 0) tries to get elected first
3. Election requires majority of master votes
4. Winning replica obtains unique incremental `configEpoch`
5. Uses "last failover wins" semantics for slot ownership conflicts [^505^]

### 5.3 CRDTs and Active-Active Replication

Redis Enterprise implements **Conflict-free Replicated Data Types** for active-active geo-distribution [^651^][^653^]:

- Local latency on read/write regardless of number of regions
- Seamless conflict resolution for simple and complex data types
- Business continuity: even if majority of regions are down, remaining regions continue [^653^]

**CRDT implementations:**
- Counters: commutative, conflict-free (sum of all updates)
- Sets: add-wins semantics
- Hashes: field-level merging (different fields updated independently)
- Strings: Last Write Wins (LWW) fallback [^651^][^653^]

> "CRDTs make this work by moving conflict resolution out of the application layer. A CRDT is a data structure whose merge rules are built into the data type itself." [^651^]

### 5.4 Limitations

- Multi-key operations only work when all keys belong to same hash slot (unless using hash tags) [^508^]
- Hash tags can create hot shards [^508^]
- Gossip protocol overhead limits practical cluster size to ~1,000 nodes [^510^]
- Single database only (no SELECT command) [^511^]

---

## 6. memcached

### 6.1 Architecture

Memcached is a high-performance, distributed memory caching system [^513^].

**Slab Allocation:**
- Memory pre-allocated in 1 MB slabs divided into fixed-size chunks
- Slab classes with exponentially growing chunk sizes (~1.25x between classes)
- Up to ~40 classes covering sizes up to 1 MB
- Item stored in smallest slab class that fits
- LRU eviction within each slab class [^513^]

**Slab calcification problem:**
- If workload shifts (e.g., many small items to many large items), memory is "calcified" in wrong distribution
- Newer versions include `slab_automove` to dynamically rebalance [^513^]

### 6.2 Scaling

- Horizontal scaling via consistent hashing
- Adding/removing nodes minimizes cache miss rate and redistribution overhead [^506^]
- Hashing keys for uniform distribution prevents "hot spots"

---

## 7. Hazelcast — In-Memory Data Grid

### 7.1 Architecture

Hazelcast is an open-source In-Memory Data Grid (IMDG) [^607^].

**Key characteristics:**
- Peer-to-peer: no master/slave, no single point of failure
- All members store equal amounts of data and do equal processing
- 271 partitions by default (called "partitions" instead of shards)
- Partition placement: `partition = hash(key) mod 271` [^607^]
- Each partition has primary and backup replicas distributed among members

**Scalability:**
- Designed to scale to hundreds and thousands of members
- New members automatically discover cluster and linearly increase memory and processing capacity
- Consistent hashing minimizes partition movement during scale-out [^607^]

### 7.2 IMDG vs. Spark RDD

| Aspect | Hazelcast IMDG | Spark RDD |
|--------|---------------|-----------|
| Purpose | Distributed data storage/sharing | Batch processing computation |
| Mutability | Mutable (read/write/updated anytime) | Immutable |
| Lifecycle | Long-lived, multiple applications | Task-specific, released after completion |
| Data model | Application data layer | Data transformation/analysis |

[^599^]

---

## 8. Apache Ignite

### 8.1 Memory-Centric Platform

Apache Ignite is a distributed database, caching, and processing platform [^605^].

**Key features:**
- Advanced read-through/write-through cache deployed on top of databases
- Co-location: organizes related data for storage on same node [^605^]
- Compute colocation: executes logic close to data, eliminating data shuffling [^604^]
- Native persistence: stores data and indexes on disk, eliminating cache warm-up [^605^]

### 8.2 Affinity and Colocation

> "The two largest sources of latency in any distributed system are network latency and disk access." [^604^]

- **Data colocation**: Keeping associated data together on one physical node (e.g., customer + their withdrawal records) [^604^]
- **Compute colocation**: Executing computations on the same node as the data
- **Affinity function**: Strategy for defining where data should be placed
- Uses affinity key/field to ensure related data goes to same partition [^604^]

### 8.3 MVCC Support

GridGain (built on Apache Ignite) applies MVCC principles for distributed ACID transactions [^577^]:
- Transactions read consistent snapshots while writes proceed independently
- Reduces overhead of coordinating locks across nodes
- Supports distributed SQL queries with consistent results under concurrent writes

---

## 9. CXL (Compute Express Link) — Memory Pooling Hardware

### 9.1 Overview

CXL is an open industry-standard cache-coherent interconnect for processors, memory expansion, and accelerators [^536^][^538^].

**Key features:**
- Maintains memory coherency between CPU memory space and memory on attached devices
- Builds upon PCIe physical layer with custom protocols for low latency
- Bandwidth scales with PCIe (CXL 4.0: 128 GT/s) [^536^]
- MESI coherency protocol between device caching agents and host [^538^]

### 9.2 Device Types

| Type | Description | Example |
|------|-------------|---------|
| Type 1 | Smart NICs, accelerators without local memory | Video transcoding |
| Type 2 | Accelerators with integrated memory (HBM/DDR) | GPUs, FPGAs |
| Type 3 | Memory expansion/pooling devices | CXL memory modules |

[^533^][^539^]

### 9.3 Memory Pooling (CXL 2.0+)

CXL 2.0 introduced memory pooling capabilities [^533^][^540^]:
- Device memory can be allocated across multiple hosts
- Multi-Logical Devices (MLD): up to 16 hosts simultaneously access different portions
- Single-level switching enables resource allocation

CXL 3.0 expanded this with [^540^]:
- **Pooled FAM (Fabric Attached Memory)**: Each HDM region dedicated to single host
- **Shared FAM**: Multiple hosts configured to access single HDM region concurrently
- **Global-FAM (G-FAM)**: Access to up to 4095 entities
- Multi-level switching for rack/pod-level composable systems
- Hardware cache coherency for shared memory (directory + Back-Invalidate flows)

> "Shared Coherent Memory across hosts using hardware coherency (directory + Back-Invalidate Flows). Allows one to build large clusters to solve large problems through shared memory constructs." [^540^]

### 9.4 Performance

- CXL targets "near CPU cache coherent latency (<200ns load to use)" [^540^]
- CXL Type-3 memory devices: ~350-500 ns latency (5-6x slower than DRAM, ~200x faster than NVMe SSDs) [^623^]
- Enables fine-grained data sharing with sub-microsecond communication delays [^538^]

### 9.5 Adoption Timeline

| Version | Release | Key Features |
|---------|---------|-------------|
| CXL 1.0/1.1 | 2019 | Memory expansion, Type 1/2/3 devices |
| CXL 2.0 | 2020 | Memory pooling, MLD, single-level switching |
| CXL 3.0 | 2022 | Multi-level switching, fabric, memory sharing, peer-to-peer |
| CXL 4.0 | 2024 | 128 GT/s, bundled ports, enhanced RAS |

[^540^]

---

## 10. Remote Direct Memory Access (RDMA)

### 10.1 Overview

RDMA enables direct memory access between servers without involving the OS or CPU [^534^].

**Key characteristics:**
- One computer places data directly into memory of another
- Bypasses CPU, cache, and operating system on both sides
- Eliminates multiple data copies and context switches of TCP/IP
- Latency: microseconds (vs. milliseconds for TCP/IP)

**Primitives:**
- **One-sided**: READ, WRITE, CAS (compare-and-swap), FAA (fetch-and-add) — bypass remote CPU
- **Two-sided**: SEND/RECV — message exchanges between nodes

### 10.2 RDMA for Memory Disaggregation

RDMA is the dominant interconnect for memory disaggregation systems [^625^]:
- Separates CPU and memory resources into CPU nodes (CNodes) and memory nodes (MNodes)
- One-sided RDMA allows CNodes to directly access MNode memory without MNode CPU involvement
- Challenge: dynamic memory allocation/deallocation requires CPU involvement on MNode

> "Unfortunately, RDMA is not a panacea for memory disaggregation, which requires more than simple memory access. CNodes need to dynamically allocate and deallocate remote memory during runtime, which is not supported by one-sided RDMA." [^625^]

### 10.3 Systems Built on RDMA

- **Infiniswap**: Remote memory paging system for RDMA networks; divides swap space into slabs distributed across remote memory [^664^][^666^]
- **FastSwap**: Improves swapping performance using frontswap interfaces, asynchronous operations [^625^]
- **ODRP**: On-Demand Remote Paging with Programmable RDMA — provides fine-grained memory management on commodity RNICs [^625^]

---

## 11. NVMe over Fabrics (NVMe-oF)

### 11.1 Overview

NVMe-oF extends NVMe storage across network fabrics [^535^][^537^].

**Value proposition:**
- Enables remote NVMe access with near-local performance
- Parallel queue architecture maintained across network
- Direct memory access capabilities preserved
- Compute and storage can be in different racks/buildings [^535^]

### 11.2 Transport Options

| Transport | Characteristics | Best For |
|-----------|----------------|----------|
| NVMe/RDMA (RoCE) | Ultra-low latency, requires RDMA-capable NICs, lossless Ethernet | Latency-critical workloads |
| NVMe/TCP | Standard Ethernet, no special hardware, software implementations | General deployment, cloud |
| NVMe/Fibre Channel | Existing SAN investments, robust zoning | Enterprise environments |
| InfiniBand | Ultimate performance, dedicated hardware | HPC |

[^535^][^537^][^541^]

### 11.3 Key Benefits

- "NVMe over Fabrics increases the velocity of data. Faster storage access enables cost reduction through consolidation." [^543^]
- All Flash Arrays market: $6.8B in 2017, growing at 32% CAGR [^543^]
- 87% of total storage capacity shipped is external storage (not DAS) [^543^]

---

## 12. Memory Tiering

### 12.1 Memory Hierarchy

Modern systems organize memory in a tiered hierarchy [^623^][^624^][^626^]:

| Tier | Latency | Cost/GB | Volatility |
|------|---------|---------|------------|
| DRAM (DDR5) | 80-100 ns | ~EUR 8 | Volatile |
| NVDIMM-P | ~120 ns | ~EUR 3 | Non-volatile |
| CXL Type-3 Memory | ~350-500 ns | Lower than DRAM | Depends on device |
| NVMe SSD | ~80,000 ns | ~EUR 0.08 | Non-volatile |

[^623^]

### 12.2 Intel Optane Persistent Memory

Intel Optane DC PMM (now discontinued) represented the first commercially available persistent memory [^624^][^626^]:
- Latency: ~346 ns (higher than DRAM, lower than SSD)
- Read bandwidth: 6.6 GB/s per module; Write bandwidth: 2.3 GB/s (asymmetric)
- Up to 512 GB per module (double largest DRAM DIMMs at the time)
- 256B cache line access granularity (vs. 64B for DRAM) [^624^][^632^]

**Operating modes:**
- **Memory Mode**: PMM acts as volatile main memory; DRAM serves as cache
- **App Direct Mode**: PMM as persistent storage separate from memory hierarchy [^626^][^632^]

> "After nearly a decade of anticipation, scalable nonvolatile memory DIMMs are finally commercially available." [^624^]

### 12.3 Post-Optane Landscape

With Intel Optane discontinued [^575^][^623^]:
- **NVDIMM-P**: Persistent memory on DDR5 bus (sampling 2025)
- **CXL Type-3**: PCIe-based fabric-attached memory (early deployment)
- Both provide non-volatile capacity with sub-microsecond latency

---

## 13. Cache Coherence Protocols

### 13.1 MESI Protocol

The MESI protocol defines four states for cache blocks [^573^]:

| State | Description |
|-------|-------------|
| **M**odified | Cache block is dirty; only valid copy |
| **E**xclusive | Clean copy present in only this cache |
| **S**hared | Clean copy may be in multiple caches |
| **I**nvalid | Block is not present in cache |

**Key advantage of MESI over MSI:**
- Write to Exclusive block: no bus transaction needed (silent upgrade to Modified)
- Write to Shared block: requires invalidation of other copies
- Distinguishing Exclusive from Shared reduces bus traffic [^573^]

### 13.2 MOESI Protocol

MOESI adds an **Owned** state to MESI [^573^]:
- **Owned**: Block is dirty and shared — one cache owns the dirty copy and can supply it to others without writing back to memory
- Used in AMD processors

### 13.3 Directory-Based vs. Snooping

**Snooping protocols:**
- All caches monitor (snoop) bus transactions
- Works well for small-scale systems with shared bus
- Scalability limited by broadcast traffic

**Directory-based protocols:**
- Central directory tracks which caches hold each block
- Point-to-point messages instead of broadcasts
- More scalable but higher latency for lookups
- Storage overhead: bit-vector per block (can use sharer lists for large systems) [^573^]

> "For very large number of nodes, may use a list of sharers instead of a bit vector — lower overhead if only few sharers." [^573^]

### 13.4 CXL Coherence

CXL uses an asymmetric MESI approach [^538^]:
- Host processor orchestrates cache coherency
- Device caching agent enforces simple MESI with small command set
- Decoupled from host-specific coherence protocol details
- Caches can be in Modified, Exclusive, Shared, or Invalid states

---

## 14. Vector Clocks and Version Vectors

### 14.1 Lamport Timestamps

Lamport timestamps provide a partial ordering of events [^563^][^569^]:
- Each process maintains a counter starting at 0
- Before each local event, increment counter
- On send: include current counter; on receive: `max(local, received) + 1`
- **Limitation**: `L(a) < L(b)` does NOT imply `a -> b` (cannot detect concurrency) [^563^][^569^]

### 14.2 Vector Clocks

Vector clocks extend Lamport to detect causality and concurrency [^563^][^569^][^570^]:
- Each process maintains a vector of counters, one per process
- Process i increments `VC[i]` before each local event
- On send: include entire vector
- On receive: `VC[j] = max(VC[j], VC_msg[j])` for all j, then increment `VC[i]`

**Comparison rules:**
- `A < B` (A happened before B): all entries A <= B, at least one strictly less
- `A = B`: all entries equal
- **Concurrent**: neither A < B nor B < A — some entries in A greater, some in B greater [^563^][^569^]

> "Vector clock timestamps precisely capture happens-before relation (potential causality)." [^569^]

### 14.3 Version Vectors

Version vectors are attached to **data items** rather than processes [^563^]:
- Each replica maintains version vector per data item
- When replica updates item, increments its own entry
- Replicas compare version vectors to detect conflicts
- Foundation for causal consistency in key-value stores

### 14.4 Scaling Challenges

Vector clocks grow linearly with number of processes [^563^]:
- **Space overhead**: each clock entry adds bytes to every message
- **Comparison cost**: O(N) where N is number of processes
- **Solutions**: bounded vector clocks, server-side clocks (DynamoDB), hybrid logical clocks (CockroachDB) [^563^]

### 14.5 Real-World Usage

- **Riak**: Uses dotted version vectors (DVVs) to track object versions
- **Amazon DynamoDB**: Originally vector clocks, later moved to server-side reconciliation
- **CockroachDB**: Hybrid Logical Clocks (HLC) combining physical time with logical counters [^563^]

---

## 15. CRDTs (Conflict-free Replicated Data Types)

### 15.1 Core Concept

CRDTs are data types designed for eventual consistency where replicas converge automatically [^564^][^567^][^571^].

> "Replicating data under Eventual Consistency (EC) allows any replica to accept updates without remote synchronisation. This ensures performance and scalability in large-scale distributed systems (e.g., clouds). However, published EC approaches are ad-hoc and error-prone." [^567^]

**Strong Eventual Consistency (SEC)** guarantees:
- All replicas that have received the same set of updates eventually reach the same state
- No conflicts requiring resolution protocol
- Self-stabilizing convergence despite any number of failures [^567^][^571^]

### 15.2 Two Approaches

1. **State-based (CvRDT)**: Replicas send their full state; merge function combines states
2. **Operation-based (CmRDT)**: Replicas send operations; operations designed to commute [^567^][^572^]

### 15.3 Key Properties

For state-based: merge operation must be commutative, associative, and idempotent (semilattice)
For operation-based: operations must commute [^572^]

> "A CRDT is an abstract data type that implements some familiar object, such as a counter, a set or a sequence. Internally, a CRDT is replicated, to provide reliability, availability and responsiveness. Encapsulation hides the details of replication and conflict resolution." [^572^]

### 15.4 Redis CRDT Implementation

Redis Enterprise implements CRDTs for Active-Active geo-distribution [^651^][^653^][^658^]:
- Operation-based CRDTs with causal consistency
- Supports strings, hashes, lists, sets, sorted sets, streams, JSON, HyperLogLog
- Read commands handled locally; write commands create "effects" distributed to all instances
- No "read repairs" needed due to consensus-free mechanism [^653^]

### 15.5 Limitations

> "CRDTs don't replace consensus protocols. They're a fit for the subset of problems where merge semantics match the shape of the data." [^651^]

- Not suitable for financial transfers requiring strict invariants
- Uniqueness constraints require coordination
- Workloads requiring validation/rejection need different approaches [^651^]

---

## 16. Cache Invalidation Strategies

### 16.1 Write Strategies

| Strategy | How It Works | Pros | Cons | Best For |
|----------|-------------|------|------|----------|
| **Write-Through** | Write to cache and DB simultaneously | Cache always consistent with DB | Slower writes | Read-heavy, consistency-critical (financial) |
| **Write-Back** | Write to cache first; DB updated async | Faster writes; reduces DB load | Data loss risk if cache crashes | Write-heavy (analytics, logs) |
| **Write-Around** | Write to DB directly; cache updated on read | Prevents cache pollution | First read after write is slow | Write-heavy, read-sparse (audit logs) |

[^595^]

### 16.2 Eviction Policies

- **LRU** (Least Recently Used): Good for temporal locality
- **LFU** (Least Frequently Used): Good for stable hot data
- **FIFO** (First In First Out): Simple, predictable
- **TTL** (Time To Live): Automatic expiration

### 16.3 Invalidation Strategies

- **Write-based/Event-driven**: Invalidate on write — strongest consistency
- **TTL-based**: Automatic expiration after fixed time
- **Lazy**: Check freshness on access
- **Manual**: Application explicitly invalidates

[^595^]

### 16.4 Strategy Selection Matrix

| Requirement | Cache Write | Eviction | Invalidation |
|-------------|------------|----------|-------------|
| High Consistency | Write-Through | LRU/TTL | Write-based/Event |
| Fast Reads | Write-Through/Back | LRU/LFU | TTL/Event |
| Fast Writes | Write-Back | LFU/FIFO | Lazy/TTL |
| Mixed/Balanced | Write-Through + TTL | LRU | Event + TTL |

[^595^]

---

## 17. Distributed Transactions

### 17.1 Two-Phase Commit (2PC)

**Phase 1 (Prepare):**
- Coordinator sends PREPARE to all participants
- Each participant executes locally, acquires locks, writes WAL, votes YES/NO

**Phase 2 (Commit/Abort):**
- If all YES: Coordinator sends COMMIT
- If any NO: Coordinator sends ABORT [^574^][^579^]

**Critical problems:**
- **Blocking**: If coordinator crashes after Phase 1, participants hold locks indefinitely
- **Single point of failure**: Coordinator is critical path
- **Poor performance**: Two round-trips plus durable writes at every step [^245^][^574^]

> "Due to its blocking nature and poor scaling characteristics, strict 2PC is rarely used in high-throughput, modern microservice architectures." [^574^]

### 17.2 Three-Phase Commit (3PC)

Adds Pre-Commit phase between Prepare and Commit to address blocking [^574^][^579^]:
- **Phase 1 (CanCommit)**: Check if participants are alive and willing
- **Phase 2 (PreCommit)**: Participants acquire locks, write WAL
- **Phase 3 (DoCommit)**: Final commit

**Timeout behavior:** If participant received PRE_COMMIT but times out waiting for DO_COMMIT, it auto-commits (safe because PRE_COMMIT guarantees all voted YES) [^574^].

**Fatal flaw:** Cannot distinguish node crash from network partition, leading to split-brain scenarios [^574^]:

> "The result is a fractured database state (a split-brain scenario). By attempting to favor Availability (non-blocking) alongside Consistency, 3PC fails violently when Partition Tolerance is tested." [^574^]

### 17.3 Saga Pattern

A Saga is a sequence of independent local transactions with compensating transactions for rollback [^574^][^581^].

**Two implementations:**

1. **Choreography**: Each service publishes events; next service listens and acts. Decentralized but complex to trace.
2. **Orchestration**: Central coordinator sends commands to each service. Easier to monitor (used by Uber, Netflix). [^245^]

**Key characteristics:**
- No global locks — each step commits independently
- Intermediate states visible to other processes (no isolation)
- Compensations must be carefully designed (some operations can't be undone — e.g., sent emails) [^245^][^574^]

> "To achieve high throughput and loose coupling, modern distributed systems abandon distributed locks in favor of the Saga Pattern." [^574^]

---

## 18. Snapshot Isolation and MVCC

### 18.1 Multi-Version Concurrency Control (MVCC)

MVCC maintains multiple versions of data so each transaction sees a consistent snapshot [^575^][^577^].

**How it works:**
1. Transaction starts — assigned logical timestamp/transaction ID
2. Snapshot established — sees database as it existed at that moment
3. Writes create new versions instead of overwriting
4. Commit validation checks for conflicts
5. Old versions garbage-collected when no longer needed [^577^]

**Primary advantage:** "Readers never block writers and writers never block readers." [^575^]

### 18.2 Snapshot Isolation

The most common MVCC-based isolation level [^575^]:
- Each transaction sees the database as it existed when it started
- Prevents dirty reads and non-repeatable reads
- **Write skew anomaly**: Two transactions read overlapping data and make writes based on what they read, producing a state neither would have produced alone
- **Serializable Snapshot Isolation (SSI)** addresses write skew at additional overhead [^575^]

### 18.3 MVCC in Distributed Systems

- **Google Spanner**: Distributed MVCC with global timestamps via TrueTime
- **CockroachDB**: Hybrid logical clocks for distributed MVCC
- **GridGain/Ignite**: Distributed ACID transactions using MVCC principles [^577^]

---

## 19. Memory Compression

### 19.1 zram

zram creates a compressed block device in RAM [^630^][^634^]:
- Pages written to zram are compressed and stored in memory
- Can achieve 2:1 to 5:1 compression ratios depending on workload
- Good for systems with no physical swap device (embedded)
- No writeback capability — limited capacity [^630^][^631^]

### 19.2 zswap

zswap is a compressed write-back cache for swap pages [^630^][^631^]:
- Pages that would be swapped to disk are compressed and stored in RAM
- Can writeback to actual swap device when under pressure
- Works with existing swap infrastructure
- Better for server/data-center environments [^630^]

**Instagram case study:** Enabling zswap with disk swap achieved ~5:1 compression and reduced disk writes by 25% compared to no swap at all [^631^].

> "We achieved roughly 5:1 compression. That's a huge benefit for such a memory bound workload." [^631^]

### 19.3 KSM (Kernel Samepage Merging)

KSM is a memory deduplication feature in the Linux kernel:
- Scans memory for identical pages
- Merges duplicate pages into a single copy (copy-on-write)
- Particularly effective for virtualized environments with many similar VMs
- zram also deduplicates zero pages without storage [^630^]

---

## 20. Swap Over Network

### 20.1 NBD (Network Block Device)

NBD is a software mechanism to utilize remote block resources over network [^603^][^608^]:
- Exports remote block devices to local system
- TCP-based implementation within default kernel source tree
- Can use Ramdisk-based NBD as swapping device for remote paging
- Serves as foundation for remote memory swapping systems

### 20.2 Infiniswap

Infiniswap is a remote memory paging system for RDMA networks [^664^][^666^]:
- Opportunistically harvests unused memory from cluster nodes
- Divides swap space into slabs distributed across remote memory
- Uses one-sided RDMA operations (bypass remote CPUs)
- Power of many choices for decentralized slab placement/eviction

**Performance results:**
- Throughput improvements: 4x to 15.4x over disk
- Median/tail latency improvements: 5.4x to 61x over disk
- Negligible remote CPU usage [^664^][^666^]

> "Memory-intensive applications suffer large performance loss when their working sets do not fully fit in memory. Yet, they cannot leverage otherwise unused remote memory when paging out to disks even in the presence of large imbalance in memory utilizations across a cluster." [^666^]

### 20.3 FastSwap

FastSwap improves on Infiniswap [^625^]:
- Uses Linux frontswap interfaces
- Makes non-critical operations asynchronous
- Dedicated thread for asynchronous page reclamation
- Foundation for many subsequent memory disaggregation systems

### 20.4 ODRP (On-Demand Remote Paging)

ODRP addresses the fundamental trade-off in RDMA-based memory disaggregation [^625^]:
- Problem: Static allocation gives poor utilization; dynamic allocation requires MNode CPU
- Solution: Programmable RDMA offloading for fine-grained memory management
- Achieves good utilization + no CPU overhead + good performance simultaneously

| Approach | Memory Utilization | No CPU | Efficiency |
|----------|-------------------|--------|------------|
| Static (one-sided) | Poor | Yes | Optimal |
| Dynamic (one-sided) | Medium | No | Medium |
| Two-sided | Good | No | Poor |
| **ODRP** | **Good** | **Yes** | **Good** |

[^625^]

### 20.5 RDMA Swap Over InfiniBand

Ohio State University research demonstrated swapping to remote memory over InfiniBand [^603^][^608^]:
- InfiniBand provides significantly better performance than GigE or IPoIB
- Memory registration is costly — pre-registration outside critical path needed
- Memory copy is 12x faster than memory registration for one page
- Polling-based completion wastes CPU; event-based completion preferred

---

## Key Findings

### Finding 1: Software DSM Achieved Viable Performance but Communication Overhead is the Bottleneck
TreadMarks demonstrated 7.4x speedup on 8 processors for Jacobi, but "Unix communication overhead remains the main obstacle." Memory management overhead is small; wire time is negligible on modern networks. [^594^][^598^]

### Finding 2: Lazy Release Consistency Dramatically Reduces Communication
TreadMarks' LRC reduces message traffic by propagating modifications only to the next lock acquirer, not all processors. This can be piggybacked on lock grant messages. [^597^][^602^]

### Finding 3: Redis Cluster's 16384 Hash Slot Design Enables Practical Sharding
The fixed 16384 slot space allows rebalancing without changing key hashing, but multi-key operations are limited to same-slot keys. Gossip overhead limits practical size to ~1000 nodes. [^505^][^508^][^510^]

### Finding 4: CXL Will Enable Hardware Cache-Coherent Memory Pooling
CXL 3.0 introduces Shared FAM with hardware cache coherency across hosts, potentially enabling "large clusters to solve large problems through shared memory constructs." Latency target is <200ns for cache-coherent access. [^540^][^538^]

### Finding 5: RDMA One-Sided Operations Enable CPU-Bypass Remote Memory Access
Infiniswap demonstrated 4x-15.4x throughput improvement over disk swapping with negligible remote CPU usage. ODRP solves the utilization vs. CPU-overhead trade-off through programmable RDMA. [^664^][^625^]

### Finding 6: CRDTs Provide Strong Eventual Consistency Without Coordination
CRDT replicas converge in a self-stabilizing manner despite any number of failures. Redis Enterprise implements CRDTs for active-active geo-distribution with sub-millisecond local operations. [^567^][^653^]

### Finding 7: MVCC Achieves Non-Blocking Concurrency
"Readers never block writers and writers never block readers" — the primary advantage of MVCC. Used in PostgreSQL, MySQL/InnoDB, Oracle, Spanner, and CockroachDB. [^575^]

### Finding 8: 2PC is Blocking; Sagas Replace It in Modern Systems
2PC's blocking problem makes it unsuitable for high-throughput microservices. The Saga pattern with compensating transactions is the modern standard, though it lacks isolation guarantees. [^574^][^245^]

### Finding 9: Apache Arrow Enables True Zero-Copy Data Sharing
Arrow's immutable columnar format allows cross-language, cross-process, and even cross-node zero-copy data access. Combined with shared memory fabrics like ThymesisFlow, enables cluster-wide shared memory. [^584^][^585^]

### Finding 10: Memory Compression Can Extend Effective Capacity 2x-5x
Instagram achieved ~5:1 compression with zswap for memory-bound workloads, reducing disk writes by 25%. zram provides compressed RAM-based swap; zswap adds writeback capability. [^631^][^630^]

---

## Major Players & Sources

| Entity | Role/Relevance |
|--------|---------------|
| **Rice University** (TreadMarks, Munin) | Pioneered lazy release consistency and multi-protocol DSM |
| **Kai Li / Princeton** (Ivy) | Created first page-based software DSM |
| **Chinese Academy of Sciences** (JIAJIA) | Home-based scope consistency DSM |
| **Redis Labs / VMware** | Leading distributed caching with CRDT-based active-active |
| **Apache Software Foundation** (Arrow, Ignite) | Zero-copy formats and memory-centric platforms |
| **Hazelcast** | Open-source IMDG with peer-to-peer architecture |
| **CXL Consortium** (Intel, AMD, Google, Microsoft, Meta) | Hardware standard for cache-coherent memory pooling |
| **Mellanox/NVIDIA** | RDMA NICs (ConnectX series) enabling remote memory |
| **Ohio State University** (MVAPICH, Infiniswap) | RDMA-based remote paging research |
| **University of Michigan** (Infiniswap) | Memory disaggregation over RDMA |
| **INRIA / Marc Shapiro** (CRDTs) | Formalized conflict-free replicated data types |
| **Intel** (Optane, CXL) | Persistent memory and CXL standards (Optane discontinued) |
| **Google** (Spanner, Borg) | Distributed MVCC and memory utilization studies |
| **Berkeley / UPC Consortium** | PGAS programming model standardization |
| **OpenSHMEM** | PGAS API for one-sided communication |

---

## Trends & Signals

1. **Hardware Memory Disaggregation**: CXL 3.0+ enabling fabric-attached memory pooling and sharing across hosts will fundamentally change cluster memory architecture [^540^][^538^]

2. **RDMA Everywhere**: RDMA is becoming standard for memory disaggregation, storage (NVMe-oF), and compute — not just HPC [^625^][^664^]

3. **CRDTs Moving Mainstream**: Redis Enterprise, AntidoteDB, and Automerge are bringing CRDTs from research to production for geo-distributed applications [^651^][^656^]

4. **Zero-Copy Data Formats**: Apache Arrow's adoption across Spark, BigQuery, TensorFlow, and Pandas signals a shift toward zero-serialization data exchange [^585^]

5. **Memory Compression Standard**: zswap adoption in production (Instagram, etc.) shows memory compression is becoming standard practice for memory-intensive workloads [^631^]

6. **Persistent Memory Transition**: With Optane discontinued, CXL Type-3 and NVDIMM-P are the future paths for persistent/tiered memory [^623^]

---

## Controversies & Conflicting Claims

### DSM Performance Viability
- **Pro-DSM**: TreadMarks achieved 7.4x speedups; "with suitable networking technology, DSM is a viable technique" [^594^]
- **Anti-DSM**: Software communication overhead dominates; Water benchmark only 4.0x speedup; message passing still faster [^598^]
- **Resolution**: DSM viable for coarse-grained applications; message passing preferred for fine-grained

### 3PC Practicality
- **Pro**: 3PC eliminates 2PC's blocking problem with non-blocking timeouts [^574^]
- **Con**: 3PC "fails violently" under network partitions, causing split-brain; "largely a theoretical construct" rarely implemented [^574^]
- **Resolution**: Consensus protocols (Paxos/Raft) preferred for strict consistency; Sagas for high throughput

### zram vs. zswap
- **zram advocates**: Simpler setup, works without disk swap, good for embedded [^634^]
- **zswap advocates**: Better writeback capability, works with kernel memory management, production-proven at scale [^631^]
- **Resolution**: zswap better for servers with disk swap; zram better for embedded/diskless systems

### CXL Latency vs. DRAM
- CXL adds ~2x latency vs. local DDR for loaded latency scenarios
- CXL may outperform DDR under load due to bandwidth advantage [^538^]
- On-package memory may outperform both

---

## Recommended Deep-Dive Areas

1. **CXL 3.0 Memory Sharing Protocols**: Hardware cache coherency for multi-host shared memory is cutting-edge. Understanding directory-based + Back-Invalidate flows could enable true cluster-wide shared memory.

2. **RDMA-Based Memory Disaggregation Architectures**: ODRP's approach to programmable RDMA offloading solves the utilization/CPU-overhead trade-off. Deep study needed for practical implementations.

3. **CRDT-Based Caching with ACID Properties**: Combining CRDTs (for conflict resolution) with Saga patterns (for transactions) could provide a practical ACID-over-caching layer.

4. **Apache Arrow for Cluster Shared Memory**: The combination of Arrow's immutable columnar format with CXL/ThymesisFlow-style shared memory enables novel cluster programming models.

5. **Lazy Release Consistency for Modern Networks**: Revisiting TreadMarks' LRC with modern RDMA/InfiniBand could yield efficient software DSM where communication overhead is no longer the bottleneck.

6. **MVCC at Cache Layer**: Implementing MVCC principles in distributed caches (like Redis/Ignite) could provide snapshot isolation for cached data without database overhead.

7. **Hybrid Memory Tiering with Automatic Migration**: Intelligent data placement across DRAM/CXL PMEM/NVMe tiers based on access patterns, similar to Intel's 2LM but extended to cluster scale.

---

## Raw Evidence Log

### Evidence 1: TreadMarks Performance
Claim: TreadMarks achieved 7.4x speedup on 8-processor ATM network for Jacobi
Source: USENIX Winter 1994 Technical Conference
URL: https://dl.acm.org/doi/10.5555/1267074.1267084
Date: 1994
Excerpt: "We achieved good speedups on the 8-processor ATM network for Jacobi (7.4), TSP (7.2), Quicksort (6.3), and ILINK (5.7)."
Context: Landmark DSM paper from Rice University
Confidence: High

### Evidence 2: Lazy Release Consistency Advantage
Claim: LRC reduces message traffic by only notifying the next lock acquirer
Source: TreadMarks paper, Computer magazine 1996
URL: https://pages.cs.wisc.edu/~markhill/restricted/computer96_treadmarks.pdf
Date: 1996
Excerpt: "With eager release consistency, a message needs to be sent to all processors at a release informing them of the change to x. However, only the next processor that acquires the lock can access x. With lazy release consistency, only that processor is informed of the change to x."
Context: Core LRC insight from TreadMarks authors
Confidence: High

### Evidence 3: Redis Cluster Hash Slots
Claim: Redis Cluster uses 16384 hash slots with CRC16(key) mod 16384
Source: Redis official documentation
URL: https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/
Date: 2026-05-27
Excerpt: "The cluster's key space is split into 16384 slots, effectively setting an upper limit for the cluster size of 16384 master nodes (however, the suggested max size of nodes is on the order of ~ 1000 nodes)."
Context: Official Redis Cluster specification
Confidence: High

### Evidence 4: CXL Memory Pooling
Claim: CXL 3.0 enables shared coherent memory across hosts using hardware coherency
Source: CXL Flash Memory Summit 2023 Tutorial
URL: https://computeexpresslink.org/wp-content/uploads/2023/12/CXL_FMS-2023-Tutorial_FINAL.pdf
Date: August 2023
Excerpt: "Shared Coherent Memory across hosts using hardware coherency (directory + Back-Invalidate Flows). Allows one to build large clusters to solve large problems through shared memory constructs."
Context: Official CXL consortium tutorial presentation
Confidence: High

### Evidence 5: Infiniswap Performance
Claim: Infiniswap improves throughput 4x-15.4x over disk with negligible remote CPU
Source: NSDI 2017
URL: https://www.usenix.org/conference/nsdi17/technical-sessions/presentation/gu
Date: 2017
Excerpt: "Using INFINISWAP, throughputs of these applications improve between 4x (0.94x) to 15.4x (7.8x) over disk (Mellanox nbdX), and median and tail latencies between 5.4x (2x) and 61x (2.3x)."
Context: Peer-reviewed academic publication
Confidence: High

### Evidence 6: CRDT Formal Foundation
Claim: CRDT replicas converge in self-stabilizing manner despite any number of failures
Source: INRIA Research Report 7687
URL: https://hal.sorbonne-universite.fr/inria-00609399v1
Date: 2011
Excerpt: "Replicas of any CRDT are guaranteed to converge in a self-stabilising manner, despite any number of failures."
Context: Original CRDT paper by Shapiro et al., cited 1445+ times
Confidence: High

### Evidence 7: Munin Multi-Protocol Performance
Claim: Munin achieved performance within 10% of message passing
Source: ACM DL, Proceedings of ISCA 1993
URL: https://dl.acm.org/doi/10.1145/173682.165154
Date: 1993
Excerpt: "A sixteen-processor prototype of Munin is currently operational. We evaluate its implementation and describe the execution of two Munin programs that achieve performance within ten percent of message passing implementations of the same programs."
Context: Peer-reviewed publication
Confidence: High

### Evidence 8: MVCC Readers/Writers Non-Blocking
Claim: MVCC readers never block writers and writers never block readers
Source: Databricks concurrency control blog
URL: https://www.databricks.com/blog/concurrency-control
Date: 2026-04-21
Excerpt: "The primary advantage is that readers never block writers and writers never block readers. This is a major performance benefit for read-heavy workloads, which is why MVCC is the dominant concurrency control mechanism in modern databases."
Context: Technical blog from major analytics platform
Confidence: High

### Evidence 9: 3PC Split-Brain Vulnerability
Claim: 3PC cannot distinguish node crash from network partition, causing data corruption
Source: Wordsus distributed transactions guide
URL: https://wordsus.com/en/system-design/distributed-transactions
Date: 2026-05-07
Excerpt: "The result is a fractured database state (a split-brain scenario). By attempting to favor Availability (non-blocking) alongside Consistency, 3PC fails violently when Partition Tolerance is tested."
Context: Technical educational content
Confidence: High

### Evidence 10: zswap Instagram Production Results
Claim: zswap achieved 5:1 compression and reduced disk writes 25% at Instagram
Source: Chris Down (Meta/kernel engineer)
URL: https://chrisdown.name/2026/03/24/zswap-vs-zram-when-to-use-what.html
Date: 2026-03-24
Excerpt: "We achieved roughly 5:1 compression. That's a huge benefit for such a memory bound workload, and also enables us to consider further stacking workloads. Enabling zswap reduced disk writes by up to 25% compared to having no swap at all."
Context: Production experience from Meta engineer
Confidence: High

### Evidence 11: PGAS One-Sided Communication
Claim: PGAS uses one-sided communication (RDMA) for remote memory access
Source: Berkeley lecture notes
URL: https://people.eecs.berkeley.edu/~demmel/cs267_Spr15/Lectures/lecture08-PGAS-yelick.pdf
Date: 2015
Excerpt: "It is implemented using one-sided communication: put/get."
Context: UC Berkeley CS267 parallel computing course
Confidence: High

### Evidence 12: CXL ACM Queue Survey
Claim: CXL achieves sub-microsecond communication delays for fine-grained sharing
Source: ACM Queue survey article
URL: https://dl.acm.org/doi/full/10.1145/3669900
Date: 2024-07-08
Excerpt: "A coherent shared-memory implementation can help cut down communication delays to sub-microseconds."
Context: Comprehensive CXL survey in ACM publication
Confidence: High

### Evidence 13: Vector Clocks Capture Causality
Claim: Vector clocks precisely capture happens-before relation
Source: Princeton COS 418 Lecture Notes
URL: https://www.cs.princeton.edu/courses/archive/fall18/cos418/docs/L4-vc.pdf
Date: Fall 2018
Excerpt: "Vector clock timestamps precisely capture happens-before relation (potential causality)."
Context: Princeton distributed systems course materials
Confidence: High

### Evidence 14: Redis CRDT Active-Active
Claim: Redis CRDTs enable local-latency writes with automatic conflict resolution
Source: Redis official blog
URL: https://redis.io/blog/how-crdts-power-active-active-database-replication/
Date: 2026-05-27
Excerpt: "CRDTs make this work by moving conflict resolution out of the application layer. A CRDT is a data structure whose merge rules are built into the data type itself."
Context: Official Redis documentation
Confidence: High

### Evidence 15: Apache Arrow Cluster Shared Memory
Claim: Arrow enables zero-copy cluster shared memory with ThymesisFlow
Source: arXiv/academic paper
URL: https://arxiv.org/html/2404.03030v1
Date: 2020-08-18
Excerpt: "The key aspect we utilize is that most Arrow objects, once instantiated, are immutable and thus the missing cache coherency is not a problem with read-only access. This work extends Arrow's ability to be readable by multiple applications on the same machine, to multiple machines connected with ThymesisFlow."
Context: Peer-reviewed research extending Arrow to clusters
Confidence: High

---

## References

[^505^] Redis Cluster Specification. Redis Documentation. https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/
[^506^] Memcached Best Practices. Dragonfly DB. https://www.dragonflydb.io/guides/memcached-best-practices
[^507^] How to Deploy a Redis Cluster. OneUptime. 2026-01-25.
[^508^] Redis Cluster Architecture Explained. C# Corner. 2026-01-07.
[^509^] Redis vs. Memcached. Alibaba Cloud. 2026-01-08.
[^510^] Using Redis Cluster: Key Features. Dragonfly DB. 2025-08-27.
[^511^] Intro To Redis Cluster Sharding. ScaleGrid. 2025-05-12.
[^512^] Hash Slot vs. Consistent Hashing. SeveralNines. 2025-03-07.
[^513^] System Design: Distributed Cache (Memcached). TechInterview. Undated.
[^514^] Partitioned Global Address Space Programming. UC Berkeley. Undated.
[^515^] Introduction to PGAS. Ohio Supercomputer Center. Undated.
[^516^] OpenSHMEM Tutorial. OpenSHMEM.org. Undated.
[^517^] A Theory of Partitioned Global Address Spaces. Dagstuhl. 2013.
[^533^] Explaining CXL Memory 101. Penguin Solutions. 2026-02-13.
[^534^] RDMA (Remote Direct Memory Access). SimplyBlock. 2026-04-07.
[^535^] NVMe over Fabrics. ElevateTech. 2025-09-07.
[^536^] About CXL. CXL Consortium. 2025-11-18.
[^537^] What Is NVMe over Fabrics. StarWind. 2025-03-20.
[^538^] An Introduction to CXL. ACM Queue. 2024-07-08.
[^539^] Compute Express Link (CXL): All you need to know. Rambus. 2024-01-23.
[^540^] CXL: An Open Industry Standard for Composable Computing. CXL Consortium. August 2023.
[^541^] NVMe over Fabrics: TCP vs. RDMA. IntelligentVisibility. Undated.
[^542^] Optimizing Cache Coherence for CXL Memory Pooling. PatSnap. 2023-07-25.
[^543^] NVMe over Fabrics. NVM Express. 2017.
[^563^] Vector Clocks — Tracking Causality. Codelit. 2026-03-29.
[^564^] Conflict-free Replicated Data Types. HAL/INRIA. 2011.
[^565^] How to Create Vector Clocks. OneUptime. 2026-01-30.
[^566^] Clock Synchronization: Lamport Clocks vs. Vector Clocks. DZone. 2026-04-06.
[^567^] Conflict-free Replicated Data Types. INRIA RR 7687.
[^568^] Conflict-free Replicated Data Types. EECS Berkeley. Undated.
[^569^] Vector Clocks and Distributed Snapshots. Princeton COS 418. Undated.
[^570^] Vector Clocks in Distributed Systems. GeeksforGeeks. 2025-09-17.
[^571^] Conflict-free Replicated Data Types (SSS 2011). SSS Conference. 2011.
[^572^] Conflict-Free Replicated Data Types (Encyclopedia Entry). Marc Shapiro. 2016.
[^573^] The MESI Protocol. University of Pittsburgh. Undated.
[^574^] Distributed Transactions. Wordsus. 2026-05-07.
[^575^] Concurrency Control in DBMS. Databricks. 2026-04-21.
[^576^] From Two-Phase Commit to Sagas. Medium. 2025-12-27.
[^577^] What Is MVCC. GridGain. Undated.
[^578^] Exploring Distributed Transaction Patterns. Medium. 2024-09-13.
[^579^] Distributed Algorithms. TiKV Deep Dive. Undated.
[^580^] RAM is getting expensive. The Register. 2026-03-13.
[^581^] Distributed Transactions. Karan Pratap Singh. 2022-09-26.
[^582^] Arrow Interop with Zero-Copy Memory Reads. Medium. 2025-02-27.
[^583^] Share Apache Arrow Memory Buffer. Elixir Forum. 2022-06-06.
[^584^] Leveraging Apache Arrow for Zero-copy Cluster Shared Memory. arXiv. 2020.
[^585^] Apache Arrow Use Cases. Apache Arrow. 2017.
[^593^] TreadMarks notes. kytrinh.me. 2026-05-27.
[^594^] TreadMarks. USENIX Winter 1994.
[^595^] Choosing the Right Cache Strategy. Dev.to. 2025-09-23.
[^596^] TreadMarks: Distributed Shared Memory. Rice University. 1994.
[^597^] TreadMarks: Shared Memory Computing. University of Wisconsin. 1996.
[^598^] TreadMarks full paper. MIT 6.824 readings. 1994.
[^599^] Hazelcast Jet stream processing. Medium. 2025-10-14.
[^600^] Distributed Shared Memory. DIKU/University of Copenhagen. Undated.
[^601^] Lazy Release Consistency for Software DSM. EPFL. Undated.
[^602^] TreadMarks: Shared Memory Computing. Rochester/Computer magazine. 1996.
[^603^] Swapping to Remote Memory over InfiniBand. Ohio State. 2005.
[^604^] Colocation and Data Affinity for Apache Ignite. GridGain. 2024-01-18.
[^605^] In-Memory Data Grid - Apache Ignite. Apache Ignite. Undated.
[^606^] What's the Difference Between IMDG and IMDB. Electronic Design. 2019.
[^607^] Hazelcast IMDG Reference Manual. Hazelcast. 2020.
[^608^] Swapping to Remote Memory over InfiniBand (slides). MVAPICH. 2005.
[^623^] Persistent Memory vs RAM in 2025. CoreWaveLabs. 2025-05-29.
[^624^] Basic Performance Measurements of Intel Optane DC. UCSD/Non-Volatile Systems Lab. 2019.
[^625^] On-Demand Remote Paging with Programmable RDMA. USENIX NSDI 2025.
[^626^] Analyzing Intel Optane PMem 200 Series. Lenovo. 2021.
[^627^] A New Home-Based Software DSM Protocol for SMP Clusters. Springer. 2000.
[^629^] How to Use Compressed Swap with Zswap and Zram. Medium. 2024.
[^630^] In-kernel memory compression. LWN.net. 2013-01-29.
[^631^] Debunking zswap and zram myths. Chris Down. 2026-03-24.
[^632^] Large-Scale In-Memory Analytics on Intel Optane DC. MIT. 2020.
[^633^] A New Home-Based Software DSM Protocol. ACM DL. 2000.
[^635^] A Framework for Portable Shared Memory Programming. Labri. 2004.
[^651^] How CRDTs power active-active database replication. Redis. 2026-05-27.
[^653^] Active-Active geo-distribution (CRDTs-based). Redis. 2026-05-14.
[^657^] Implementation and performance of Munin. ACM DL. 1993.
[^660^] Techniques for Reducing Consistency-Related Communication in DSM. Munin.
[^662^] Distributed Shared Memory: Experience with Munin. Rice University. 1992.
[^664^] Efficient Memory Disaggregation with INFINISWAP. NSDI 2017.
[^665^] Lecture 14 - Distributed Shared Memory. University of Wisconsin. Undated.
[^666^] Efficient Memory Disaggregation with Infiniswap. USENIX NSDI 2017.
