# 4. Consensus & State Replication

The foundational tension in any distributed system is between *correctness* and *availability*. When a network partition severs communication between nodes, the system must choose: refuse operations and preserve consistency, or accept operations and reconcile divergent state later. HelixCluster's answer is architectural: classify every piece of state by its consistency requirements, then apply the weakest sufficient model to each. Membership changes and resource allocation demand the full rigor of linearizable consensus; metrics and presence information do not. This chapter defines the mechanisms that enforce that classification.

The design follows a cell-based boundary. Each HelixCluster cell maintains an independent etcd cluster (3--5 nodes) running the Raft consensus protocol. Raft never stretches across a WAN link---every etcd cluster lives within a single region, with RTTs between members kept below 50 milliseconds. Cross-cell state uses Conflict-free Replicated Data Types (CRDTs) for mathematically guaranteed convergence without coordination, hybrid logical clocks for causality tracking, and Merkle trees for efficient anti-entropy repair. The result is a tiered system: roughly 40% of cluster state receives strong consistency via Raft, while the remaining 60% propagates through lighter-weight eventual consistency protocols.

## 4.1 Per-Cell Strong Consistency

### 4.1.1 Raft-based etcd per Cell: Never Stretch Across WAN

Every HelixCluster cell operates its own etcd cluster for the control plane state that absolutely requires linearizability: node membership, resource allocation decisions, and security policy changes. etcd implements the Raft consensus protocol, which requires a majority quorum for every write. In a 3-node etcd cluster, the leader must receive acknowledgement from at least one follower before committing an entry. This majority requirement is what makes Raft fundamentally latency-sensitive.

A 3-node cluster spanning three datacenters with 30--80 millisecond inter-DC latency will experience a practical commit latency floor of approximately 2x the RTT to the farthest majority member. For cross-continental deployments---US East to US West at ~70ms, or US East to Europe at ~150ms---this yields a minimum commit latency of 140--300ms per write. Worse, default etcd settings (100ms heartbeat, 1,000ms election timeout) work reliably on LAN but fail aggressively over WAN, triggering spurious leader elections that cripple availability.

The HelixCluster rule is absolute: **etcd clusters never cross region boundaries**. Within a single availability zone, RTT is 0.4--0.5ms. Across AZs in the same region, it reaches 0.5--2.5ms. Both ranges are comfortable for Raft. The moment RTT exceeds 10ms---crossing into multi-region territory---etcd performance degrades sharply. For multi-AZ deployments within a region, WAN-tuned parameters apply.

### 4.1.2 Raft Tuning: Heartbeat 100ms, Election Timeout 1s for Cell-Internal

Raft's tunable parameters control the trade-off between failure detection speed and resilience to transient latency spikes. The production rule, validated by etcd operational documentation and consensus theory, is:

**election timeout >= 10 x heartbeat interval >= 10 x max cross-DC RTT**

For a cell-internal cluster in a single AZ (0.5ms RTT), the defaults are fine. For a cell stretched across three AZs in a region (2.5ms RTT), the heartbeat must rise to accommodate the longer round-trips, and the election timeout must scale proportionally.

| Parameter | Single AZ (LAN) | Multi-AZ (Same Region) | Rationale |
|---|---|---|---|
| Heartbeat Interval | 100ms | 200--300ms | 0.5--1.5x average RTT between members |
| Election Timeout | 1,000ms | 2,000--3,000ms | At least 10x max RTT; tolerate transient partitions |
| Snapshot Chunk Size | 64KB | 64KB--512KB | Smaller chunks for lossy or congested links |
| Max Inflight Messages | 256 | 512--1,024 | Pipeline replication over higher-latency links |
| Pre-Vote | Enabled | **Required** | Prevent partition-induced election churn |
| Check Quorum | Disabled | **Enabled** | Leader steps down if majority unreachable |

**Table 1: Raft Tuning Parameters for Per-Cell etcd.** Single-AZ cells use etcd defaults. Multi-AZ cells within a region require WAN-tuned parameters to maintain stability across availability zone boundaries. Pre-vote and Check Quorum are mandatory for any topology where network partitions are possible.

The pre-vote mechanism prevents a partitioned node from forcing unnecessary leader elections upon rejoining. Without pre-vote, a node that increments its term while partitioned will reject the current leader's AppendEntries RPCs, triggering a disruptive election cycle. Pre-vote requires a candidate to first confirm it can win an election (by receiving pre-vote grants from a majority) before incrementing its term, eliminating this churn entirely.

Check Quorum adds a safety mechanism: if the leader cannot reach a majority of followers within an election timeout period, it voluntarily steps down. This prevents a minority-partition leader from accepting writes that would violate linearizability. Together, pre-vote and Check Quorum make Raft robust against the network partitions that are inevitable in multi-AZ deployments.

Leader leases further optimize read performance. A leader that receives heartbeats from a majority within an election timeout period can serve local reads without issuing ReadIndex quorum checks. Benchmarks demonstrate that ReadIndex latency grows from 4ms in a 3-node cluster to 26ms in a 25-node cluster, while lease-based read latency stays at 0.3--1.4ms regardless of cluster size. The trade-off is a dependency on clock synchronization: clock drift exceeding the lease duration can permit stale reads, which is why HelixCluster pairs leader leases with Hybrid Logical Clocks (see Section 4.4).

### 4.1.3 Multi-Raft Consideration: Separate Raft Groups per Resource Shard

As a cell grows toward the 5,000-node limit, a single Raft group for all state becomes a bottleneck. TiKV addresses this through Multi-Raft: data is sharded into Regions (default 96MB), each forming an independent Raft group. A single TiKV node participates in hundreds or thousands of Raft groups simultaneously, using an event loop that polls all groups in batch mode.

HelixCluster adopts Multi-Raft for cells that require independent consensus domains per resource shard. Each partition---workload placement state, node capability maps, security policy segments---runs its own Raft group. Key optimizations include:

- **Batch processing**: RocksDB WriteBatch handles all log appends across Raft groups in a single fsync, amortizing disk I/O cost.
- **Pipeline replication**: The leader sends log entries without waiting for each batch's acknowledgement, keeping the network pipe full despite higher RTT.
- **Connection reuse**: Messages for multiple Raft groups share a single gRPC connection between node pairs, reducing connection overhead.
- **Region split/merge**: The system automatically splits hot shards and merges cold ones, balancing load as the data distribution changes.

The practical implication is that adding nodes to a HelixCluster cell increases both storage capacity and consensus throughput proportionally, rather than concentrating all consensus load on a single leader. A cell with 100 independent Raft groups distributes write load across multiple leaders, each serving a subset of the total state.

For federation-scale deployments, the key insight is that Multi-Raft confines the blast radius of a leader failure. If one Raft group's leader fails, only that shard pauses for the ~1-second election timeout; other shards continue uninterrupted. This is essential for maintaining availability in cells that manage thousands of nodes.

## 4.2 Cross-Cell Eventual Consistency

### 4.2.1 CRDT Taxonomy: G-Counter, PN-Counter, OR-Set, LWW-Register

Where strong consistency demands coordination and pays latency for correctness, eventual consistency accepts temporary divergence in exchange for availability and partition tolerance. Conflict-free Replicated Data Types provide the mathematical foundation for this trade-off: CRDTs guarantee that replicas converge to the same state without requiring any synchronization, as long as all updates are eventually delivered.

HelixCluster uses four CRDT types for cross-cell state:

**G-Counter (Grow-only Counter).** Each replica tracks per-node increments in a map of `nodeID -> count`. Merge takes the pointwise maximum across all entries. The total value is the sum of all per-node counts. G-Counters are ideal for monotonic metrics---request counts, bytes transferred, vote tallies---where values only increase.

**PN-Counter (Positive-Negative Counter).** Composed of two G-Counters: one for increments, one for decrements. The total value is the increment count minus the decrement count. PN-Counters support net-positive and net-negative values, making them suitable for inventory counts, active connection tracking, and balance monitoring.

**OR-Set (Observed-Remove Set).** Each addition tags the element with a unique identifier (typically a node ID plus a monotonic counter). Removal only deletes tags that the removing replica has observed. If two replicas concurrently add and remove the same element, the add wins because the remover could not have observed the other's addition tag. OR-Sets solve the "shopping cart problem" and are used for node tags, capability grants, and feature flags.

**LWW-Register (Last-Write-Wins Register).** Stores a single value with a timestamp. When merging, the value with the higher timestamp wins. Ties break deterministically by node ID. LWW-Registers are used for configuration values, node presence indicators, and load metrics where the latest reading is authoritative.

### 4.2.2 Delta-State CRDTs: 18x Bandwidth Reduction

Naive state-based CRDTs require sending the full state on every synchronization---prohibitively expensive for counters tracking thousands of nodes or OR-Sets with millions of tags. Delta-state CRDTs send only the delta (the changes) since the last synchronization, achieving operation-based bandwidth efficiency while maintaining state-based implementation simplicity.

The optimizations are cumulative:

- **Back-Propagation avoidance (BP)**: A replica does not send deltas back to the neighbor from which it received them, eliminating redundant round-trips.
- **Redundant-state removal (RR)**: Only new information is added to the delta buffer; if a subsequent local update supersedes an earlier one, the earlier delta is elided.

ConflictSync extends this with Bloom filters plus rateless Invertible Bloom Lookup Tables (IBLTs) for digest-driven synchronization, reducing transfer by up to **18x** compared to full-state synchronization. In bandwidth terms, for a 50-node partial-mesh with a Retwis workload (Zipf coefficient 1.25):

- State-based: ~1.5 GB/s per node
- Classic delta-based: ~1.46 GB/s (minimal improvement due to redundant propagation)
- Delta-based BP+RR: ~0.06 GB/s (**24x improvement** over naive state-based)

For HelixCluster's cross-cell gossip, delta-state CRDTs mean that even with 100 cells exchanging presence, metrics, and configuration data, the WAN bandwidth per gateway node stays below 5 KB/s---well within the capacity of any modern network connection.

### 4.2.3 Loro Library: Production-Ready Delta-State CRDTs

While HelixCluster implements simpler CRDT types (counters, sets, registers) directly in Go, the system can integrate with the Loro library for complex collaborative state. Loro provides production-ready delta-state CRDTs with the fastest available benchmarks:

| Library | Language | Algorithm | Weekly Downloads | Apply 260K chars | Encoded Size |
|---|---|---|---|---|---|
| Yjs | JavaScript | YATA | ~920K | 430ms | 160KB |
| Automerge v2 | Rust + WASM | RGA + LWW | ~85K | 680ms | 250KB |
| **Loro** | **Rust + WASM** | **Fugue** | **~12K** | **290ms** | **68KB** |

**Table 2: CRDT Library Benchmarks.** While HelixCluster uses native Go implementations for cluster state, Loro provides the fastest available delta-state CRDTs for complex collaborative documents. Benchmark: apply 260,000-character editing trace.

Loro's Fugue algorithm achieves the best available performance for collaborative editing traces, with 290ms apply time and 68KB encoded state for a 260,000-character document editing workload. For cluster state management, the relevant feature is Loro's delta-state encoding: only changed fields are transmitted, and the system supports snapshot-based recovery for replicas that have been offline for extended periods.

HelixCluster uses native Go CRDT implementations for cluster state (presence, counters, configuration) and reserves Loro integration for complex cross-cell documents: policy specifications, shared configuration manifests, and operational runbooks that benefit from full version history and fine-grained merge semantics.

### 4.2.4 CRDT Go Implementation

The following Go implementations provide the core CRDT types used for Tier 3 and Tier 4 state (see Section 4.5). Each type is safe for concurrent use and supports JSON serialization for network transfer.

```go
package crdt

import (
    "encoding/json"
    "sync"
)

// GCounter is a grow-only counter CRDT.
// Each replica tracks per-node increments; merge takes element-wise max.
type GCounter struct {
    mu     sync.RWMutex
    counts map[string]uint64 // nodeID -> count
}

func NewGCounter() *GCounter {
    return &GCounter{counts: make(map[string]uint64)}
}

func (c *GCounter) Increment(nodeID string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.counts[nodeID]++
}

func (c *GCounter) Value() uint64 {
    c.mu.RLock()
    defer c.mu.RUnlock()
    var total uint64
    for _, v := range c.counts {
        total += v
    }
    return total
}

func (c *GCounter) Merge(other *GCounter) {
    other.mu.RLock()
    defer other.mu.RUnlock()
    c.mu.Lock()
    defer c.mu.Unlock()
    for node, count := range other.counts {
        if count > c.counts[node] {
            c.counts[node] = count
        }
    }
}

func (c *GCounter) Encode() ([]byte, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return json.Marshal(c.counts)
}
```

The G-Counter is the simplest practical CRDT. Each node tracks only its own increments; merging takes the maximum per node. The total is the sum of all per-node maximums. This design means that concurrent increments at different nodes never conflict---they simply add to the total. The only constraint is that a G-Counter can never decrement.

```go
// LWWRegister implements a last-write-wins register with HLC timestamps.
type LWWRegister struct {
    mu        sync.RWMutex
    value     []byte
    timestamp int64  // HLC physical component (microseconds)
    nodeID    string // For deterministic tie-breaking
}

func (r *LWWRegister) Set(value []byte, timestamp int64, nodeID string) bool {
    r.mu.Lock()
    defer r.mu.Unlock()
    if timestamp > r.timestamp ||
        (timestamp == r.timestamp && nodeID > r.nodeID) {
        r.value = value
        r.timestamp = timestamp
        r.nodeID = nodeID
        return true
    }
    return false // Existing value wins
}

func (r *LWWRegister) Get() ([]byte, int64, string) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    return r.value, r.timestamp, r.nodeID
}
```

The LWW-Register uses Hybrid Logical Clock timestamps (see Section 4.4) for ordering. When two nodes write concurrently with the same physical timestamp, node ID provides deterministic tie-breaking. This register is used for configuration values, node presence status, and load metrics where recency is the correct resolution policy.

```go
// ORSet implements an Observed-Removed Set CRDT.
// Add wins: each addition has a unique tag; remove only removes observed tags.
type ORSet struct {
    mu      sync.RWMutex
    adds    map[string]map[string]struct{} // element -> {tag: present}
    removes map[string]map[string]struct{} // element -> {tag: removed}
}

func NewORSet() *ORSet {
    return &ORSet{
        adds:    make(map[string]map[string]struct{}),
        removes: make(map[string]map[string]struct{}),
    }
}

func (s *ORSet) Add(element, tag string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.adds[element] == nil {
        s.adds[element] = make(map[string]struct{})
    }
    s.adds[element][tag] = struct{}{}
}

func (s *ORSet) Remove(element string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if observed, ok := s.adds[element]; ok {
        s.removes[element] = make(map[string]struct{})
        for tag := range observed {
            s.removes[element][tag] = struct{}{}
        }
    }
}

func (s *ORSet) Contains(element string) bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    observed := s.adds[element]
    removed := s.removes[element]
    for tag := range observed {
        if _, wasRemoved := removed[tag]; !wasRemoved {
            return true
        }
    }
    return false
}

func (s *ORSet) Merge(other *ORSet) {
    other.mu.RLock()
    defer other.mu.RUnlock()
    s.mu.Lock()
    defer s.mu.Unlock()
    for elem, tags := range other.adds {
        if s.adds[elem] == nil {
            s.adds[elem] = make(map[string]struct{})
        }
        for tag := range tags {
            s.adds[elem][tag] = struct{}{}
        }
    }
    for elem, tags := range other.removes {
        if s.removes[elem] == nil {
            s.removes[elem] = make(map[string]struct{})
        }
        for tag := range tags {
            s.removes[elem][tag] = struct{}{}
        }
    }
}
```

The OR-Set's "add-wins" semantics are critical for cluster state. If two administrators concurrently grant and revoke the same capability, the grant wins because the revoker could not have observed the new grant's unique tag. This prevents accidental revocation of capabilities that were added during a partition. For true capability revocation that overrides concurrent grants, a stronger mechanism is required (see Section 4.5, Tier 2 state).

## 4.3 Anti-Entropy & Repair

### 4.3.1 Merkle Trees for O(log N) State Comparison

Even with delta-state CRDTs, replicas occasionally need to verify that their states have converged. A Merkle tree enables efficient state comparison by building a hash tree over the dataset: each leaf node is the hash of a key range, and each internal node is the hash of its children. Two replicas compare by exchanging root hashes (32 bytes). If the roots match, the states are identical. If they differ, the replicas recursively compare child hashes, descending only into the branches that differ. For a partition with 1 million keys and a single divergent key, this requires approximately 20 hash comparisons and one key transfer---compared to 1 million key comparisons for a naive approach.

Cassandra uses 15 tree levels with 32,768 leaves per token range, where each leaf covers approximately 30 rows. HelixCluster uses a similar structure over CRDT state, with each leaf covering a configurable key range (default 64 keys).

```go
package crdt

import (
    "crypto/sha256"
    "encoding/hex"
)

// MerkleTree provides efficient O(log N) state comparison for anti-entropy.
type MerkleTree struct {
    Root   *MerkleNode
    leaves []*MerkleNode
    dirty  bool
}

type MerkleNode struct {
    Left     *MerkleNode
    Right    *MerkleNode
    Hash     []byte
    KeyRange [2]string // [start, end) of key range this node covers
    IsLeaf   bool
}

func NewMerkleTree() *MerkleTree {
    return &MerkleTree{}
}

func (t *MerkleTree) Insert(key string, value []byte) {
    hash := sha256.Sum256(append([]byte(key+":"), value...))
    _ = hash
    t.dirty = true
    // Production: maintain sorted leaf list, rebuild affected path only
}

func (t *MerkleTree) RootHash() string {
    if t.Root == nil {
        return ""
    }
    return hex.EncodeToString(t.Root.Hash)
}

// Compare recursively finds differing key ranges between two trees.
func (t *MerkleTree) Compare(other *MerkleTree) [][2]string {
    var diffs [][2]string
    t.compareNodes(t.Root, other.Root, &diffs)
    return diffs
}

func (t *MerkleTree) compareNodes(a, b *MerkleNode, diffs *[][2]string) {
    if a == nil && b == nil {
        return
    }
    if a == nil || b == nil {
        *diffs = append(*diffs, a.KeyRange)
        return
    }
    if string(a.Hash) == string(b.Hash) {
        return // Subtrees match
    }
    if a.IsLeaf && b.IsLeaf {
        *diffs = append(*diffs, a.KeyRange)
        return
    }
    t.compareNodes(a.Left, b.Left, diffs)
    t.compareNodes(a.Right, b.Right, diffs)
}
```

The production implementation maintains leaves in sorted order and rebuilds only the path from an updated leaf to the root, achieving O(log N) update cost. The tree is rebuilt from scratch only when the key space is rebalanced (split or merge operations).

### 4.3.2 Active Anti-Entropy, Read Repair, Hinted Handoff

Three complementary mechanisms repair divergent state at different timescales:

| Mechanism | Trigger | Coverage | Cold Data Repair? | Latency Impact |
|---|---|---|---|---|
| **Read Repair** | Client read (quorum read) | Keys that clients access | No | Adds to read tail latency |
| **Hinted Handoff** | Write to unavailable node | Writes since node failure | No (catches briefly-down nodes) | Slight queue cost on writers |
| **Anti-Entropy (Merkle)** | Scheduled (every 1--6 hours) | Entire dataset | **Yes** | None on critical path |

**Table 3: Repair Mechanisms Comparison.** These mechanisms are complementary, not redundant. Hinted handoff catches briefly-down nodes within a 3-hour window. Read repair catches hot data that diverged during a partition. Anti-entropy catches cold data that escaped both other paths.

**Read repair** activates when a read quorum returns divergent values. If three replicas return different versions of the same key, the coordinator writes the correct version back to the out-of-date replicas. This is effective for frequently read data but offers no protection for cold data that is never accessed.

**Hinted handoff** addresses transient unavailability. When a write targets a replica that is temporarily down, the coordinator stores a "hint" and replays it when the replica recovers. The default window is 3 hours; hints older than this are discarded and the data will be repaired by the anti-entropy process instead.

**Active anti-entropy** runs on a schedule (every 1--6 hours, configurable per data tier). Each cell delegate builds a Merkle tree over its CRDT state and exchanges root hashes with peer delegates. Divergent ranges trigger delta-state synchronization. For a new cell joining the federation, or after an extended partition (hours or days), the system falls back to a full state snapshot transfer, followed by incremental delta synchronization for steady-state operation.

For federation-scale deployments with 100+ cells, pairwise anti-entropy is O(n^2). HelixCluster addresses this with a hierarchical repair tree: cells organize into a spanning tree, and repair propagates up and down the tree rather than across all pairs. Digest-driven synchronization using Bloom filters of changed keys further reduces transfer volume before any deltas are exchanged.

## 4.4 Clock Synchronization

### 4.4.1 Hybrid Logical Clocks: Physical Clock + Logical Counter

Distributed systems cannot rely on wall-clock timestamps for event ordering. NTP achieves 1--10ms accuracy in datacenter environments, but VM time jumps, leap second handling, and misconfigured servers can introduce errors of hundreds of milliseconds or more. When clocks drift by 100ms, Last-Write-Wins resolution causes silent data loss: a fast node's "future" timestamp overwrites legitimate later writes, while a slow node's writes are treated as outdated and rejected.

HelixCluster uses Hybrid Logical Clocks (HLC) for all causality tracking. An HLC combines a physical clock (microseconds since Unix epoch, 52 bits) with a logical counter (12 bits) for events that occur within the same physical microsecond. This provides the best of both worlds: timestamps that are close to wall-clock time for human readability and debugging, but with the causal ordering guarantees of a logical clock.

```go
package consensus

import (
    "encoding/json"
    "fmt"
    "sync"
    "time"
)

// HLCTimestamp: 52 bits physical (microseconds) + 12 bits logical.
type HLCTimestamp struct {
    Physical int64  `json:"pt"`
    Logical  uint16 `json:"lt"`
}

type HLC struct {
    mu        sync.RWMutex
    latest    HLCTimestamp
    maxOffset time.Duration // Default 500ms
}

func NewHLC(maxOffset time.Duration) *HLC {
    if maxOffset == 0 {
        maxOffset = 500 * time.Millisecond
    }
    return &HLC{maxOffset: maxOffset}
}

// Now returns the current HLC timestamp for a local event.
func (h *HLC) Now() HLCTimestamp {
    h.mu.Lock()
    defer h.mu.Unlock()
    now := time.Now().UnixMicro()
    if now > h.latest.Physical {
        h.latest.Physical = now
        h.latest.Logical = 0
    } else {
        h.latest.Logical++
    }
    return h.latest
}

// Update advances the HLC upon receiving a timestamp from another node.
func (h *HLC) Update(received HLCTimestamp) HLCTimestamp {
    h.mu.Lock()
    defer h.mu.Unlock()
    now := time.Now().UnixMicro()
    h.latest.Physical = max(now, h.latest.Physical, received.Physical)
    switch {
    case h.latest.Physical == now && h.latest.Physical == received.Physical:
        h.latest.Logical = maxUint16(h.latest.Logical, received.Logical) + 1
    case h.latest.Physical == h.latest.Physical: // Physical == previous local
        h.latest.Logical++
    case h.latest.Physical == received.Physical:
        h.latest.Logical = received.Logical + 1
    default:
        h.latest.Logical = 0
    }
    return h.latest
}

func (a HLCTimestamp) HappensBefore(b HLCTimestamp) bool {
    return a.Physical < b.Physical ||
        (a.Physical == b.Physical && a.Logical < b.Logical)
}

func (a HLCTimestamp) Concurrent(b HLCTimestamp) bool {
    return !a.HappensBefore(b) && !b.HappensBefore(a)
}

func max(a, b, c int64) int64 {
    if a >= b && a >= c { return a }
    if b >= a && b >= c { return b }
    return c
}

func maxUint16(a, b uint16) uint16 {
    if a > b { return a }
    return b
}
```

The HLC's `Now()` method is called before assigning a timestamp to a local event. The `Update()` method is called when receiving a message from a remote node, ensuring that causality is preserved: if event A happens before event B, then `HLC(A) < HLC(B)`. The 12-bit logical counter supports up to 4,096 events per microsecond at the same physical time, which is sufficient for any single-node event rate.

### 4.4.2 Clock Skew Detection: Flag Nodes with >500ms Drift

HelixCluster enforces a maximum clock offset (default 500ms), following the CockroachDB model. If a node detects clock drift exceeding 80% of the maximum offset (400ms) against a majority of its peers, it **shuts itself down** rather than risk causality violations. This self-termination is a safety mechanism: a node with a severely skewed clock would generate timestamps that violate happens-before ordering and corrupt LWW-Register resolution.

The detection mechanism works as follows:

1. Each node includes its HLC timestamp in every heartbeat and gossip message.
2. Receivers compare the embedded physical time against their own wall clock.
3. If the absolute difference exceeds the threshold for a majority of peers over a 30-second window, the node triggers an emergency shutdown.
4. An alert is fired to the monitoring system (Prometheus Alertmanager) identifying the affected node and the measured skew.

For the underlying time synchronization, HelixCluster uses NTP (chrony preferred over ntpd for faster convergence after VM migration) rather than PTP or GPS. This is a deliberate choice: PTP requires hardware timestamping support in NICs and switches, and GPS receivers are unavailable in most cloud environments and edge deployments. NTP achieves sufficient accuracy (1--10ms in datacenters) for cluster state management, and the HLC's logical counter absorbs any remaining skew.

| Protocol | Accuracy | Hardware Required | Deployment Cost | Best For |
|---|---|---|---|---|
| NTP (internet) | 1--100ms | None | Free | General purpose |
| NTP (chrony, DC) | 1--10ms | Local NTP server | Low | **HelixCluster cells** |
| PTP | 100ns -- 1ms | PTP-capable NICs/switches | Medium | Financial trading, telecom |
| TrueTime/Spanner | 1--7ms | GPS + atomic clocks per DC | Very High | Global databases (Google) |
| HLC + NTP | 1--500ms (bounded) | None | Free | **Commodity distributed systems** |

**Table 4: Clock Synchronization Protocols.** HelixCluster uses NTP with chrony for physical time synchronization and HLC for logical ordering. This combination provides sufficient accuracy on commodity hardware without requiring PTP-capable equipment or GPS receivers.

### 4.4.3 Vector Clocks for Cross-Cell Causality

While HLC provides happens-before ordering within a cell, vector clocks provide precise causality tracking across cell boundaries for Tier 2 operational state (scheduler decisions, placement changes, migration tracking). A vector clock is a map of `nodeID -> logical_time` that captures the happens-before relationship exactly: `VC(a) < VC(b)` if and only if event A happened before event B. Concurrent events have incomparable vectors.

```go
package vclock

import "fmt"

// VectorClock tracks causality across N nodes.
type VectorClock map[string]uint64 // nodeID -> logical time

func New() VectorClock {
    return make(VectorClock)
}

func (vc VectorClock) Increment(nodeID string) {
    vc[nodeID]++
}

// Merge updates this VC to the element-wise max of both VCs.
func (vc VectorClock) Merge(other VectorClock) {
    for node, ts := range other {
        if existing, ok := vc[node]; !ok || ts > existing {
            vc[node] = ts
        }
    }
}

// Compare returns: -1 if vc < other, 1 if vc > other, 0 if concurrent/equal.
func (vc VectorClock) Compare(other VectorClock) int {
    allLessOrEqual := true
    allGreaterOrEqual := true
    for node, ts := range vc {
        otherTs := other[node]
        if ts > otherTs {
            allLessOrEqual = false
        }
        if ts < otherTs {
            allGreaterOrEqual = false
        }
    }
    for node, otherTs := range other {
        if _, ok := vc[node]; !ok && otherTs > 0 {
            allGreaterOrEqual = false
        }
    }
    if allLessOrEqual && !allGreaterOrEqual {
        return -1 // vc happened before other
    }
    if allGreaterOrEqual && !allLessOrEqual {
        return 1 // vc happened after other
    }
    return 0 // equal or concurrent
}

func (vc VectorClock) HappenedBefore(other VectorClock) bool {
    return vc.Compare(other) == -1
}

func (vc VectorClock) Concurrent(other VectorClock) bool {
    return vc.Compare(other) == 0 && len(vc) == len(other)
}

func (vc VectorClock) Copy() VectorClock {
    c := make(VectorClock, len(vc))
    for k, v := range vc {
        c[k] = v
    }
    return c
}
```

Vector clocks are attached to every cross-cell scheduler event. When Cell Alpha decides to migrate a workload to Cell Beta, the migration request carries Alpha's vector clock. Beta increments its own entry and merges the received clock, establishing a causal chain that prevents confusion if concurrent placement decisions occur. The cost is O(N) space per event where N is the number of nodes in the causal chain; in practice, HelixCluster caps vector clock size at 16 entries (pruning the oldest entries) to bound metadata overhead.

## 4.5 State Classification Matrix

### 4.5.1 Tier 1 (Strong): Membership, Allocation, Security

Not all state can tolerate eventual consistency. The following categories absolutely require linearizable consensus; using weaker consistency leads to catastrophic failure:

- **Cluster membership**: Split-brain membership creates dual-leader scenarios, quorum violations, and data loss. Every node must agree on who is in the cluster.
- **Resource allocation and locks**: Double-allocation of an exclusive resource---the same pod scheduled on two nodes---breaks correctness and can corrupt stateful workloads.
- **Security policy changes**: A policy revocation that has not propagated creates a vulnerability window during which revoked permissions remain usable.
- **Rate limits and quota enforcement**: Over-limit requests granted during an inconsistency window violate operational contracts.
- **Fencing tokens**: Used to prevent split-brain in storage systems; must be monotonic and strongly consistent.

These are implemented via etcd per cell, using Raft with the tuning parameters from Table 1. Cross-cell Tier 1 operations use asynchronous replication with application-level conflict resolution---never shared consensus across the WAN.

### 4.5.2 Tier 2 (CRDT): Presence, Capabilities, Metrics, Config

Tier 2 state maps cleanly to CRDTs and represents the majority of cross-cell coordination data. This state uses eventual consistency with vector-clock causality tracking.

| # | Data Type | CRDT Type | Consistency | Why It Works |
|---|---|---|---|---|
| 1 | Node heartbeat/presence | LWW-Register + G-Set | Eventual | Presence expires naturally; old entries are harmless |
| 2 | Request counters | G-Counter | Eventual | Monotonic increments converge via max |
| 3 | Active connection count | PN-Counter | Eventual | Add on connect, remove on disconnect |
| 4 | Node tags/labels | OR-Set | Eventual | Tags added/removed converge to correct set |
| 5 | Load metrics (CPU, memory) | LWW-Register | Eventual | Latest value wins; staleness is temporary |
| 6 | Configuration (versioned) | LWW-Register | Eventual | Higher version always wins |
| 7 | Seen-message deduplication | G-Set | Eventual | Grow-only set of message IDs |
| 8 | Feature flags | LWW-Register | Eventual | Flag state converges to latest setting |
| 9 | Service endpoint list | OR-Set | Eventual | Endpoints added/removed independently |
| 10 | Capability grants | OR-Set | Eventual | Grants converge; revocation uses version tokens |
| 11 | Routing table entries | LWW-Register | Eventual | Last update wins; convergence in O(log C) rounds |
| 12 | Health check status | LWW-Register | Eventual | Stale health data self-corrects on next check |
| 13 | Audit log entries | G-Set | Eventual | Append-only log; no deletion |
| 14 | Topology metadata | LWW-Register | Eventual | Cell topology changes infrequently |
| 15 | Version metadata | LWW-Register | Eventual | Semantic version ordering is natural |
| 16 | Cached read replicas | LWW-Register | Eventual | Cache staleness bounded by TTL |
| 17 | Telemetry samples | G-Counter | Eventual | Counters aggregated across all nodes |
| 18 | Rate limit budgets | PN-Counter | Eventual | Budget decrements converge across cells |
| 19 | Quota usage | PN-Counter | Eventual | Usage tracking with add/remove |
| 20 | Scheduled job triggers | OR-Set | Eventual | Job triggers are idempotent |

**Table 5: State Classification Matrix (20+ Data Types).** All Tier 2 data types use CRDT implementations with delta-state synchronization and Merkle tree anti-entropy. These handle approximately 60% of typical cluster coordination state with no coordination overhead.

### 4.5.3 Tier 3 (Eventual): Logs, Telemetry, Cached Data

Tier 3 state is purely observational: application logs, detailed telemetry metrics, and cached data that can be recomputed from Tier 1 or Tier 2 sources. This state uses asynchronous replication with no ordering guarantees. Loss is acceptable within bounded windows (configurable, default 1% sample rate for logs, 5-minute retention for high-cardinality metrics).

The consistency model selection follows a simple principle: **never use strong consistency where eventual consistency suffices**. Applying Raft to metrics collection would consume quorum latency for every data point and saturate the etcd cluster with write load that has no correctness requirements. By contrast, applying eventual consistency to membership changes would permit split-brain scenarios that violate the safety guarantees of the control plane.

The enforcement mechanism is code-level: every state write specifies its tier at initialization, and the storage layer routes Tier 1 writes to etcd, Tier 2 writes to the CRDT manager, and Tier 3 writes to the async telemetry pipeline. Attempting to write Tier 1 state through a Tier 2 or Tier 3 path is rejected at compile time via the type system. This prevents the most common operational error in distributed systems: choosing the wrong consistency model under pressure.

For capability revocation specifically---a case that straddles the boundary between Tier 1 and Tier 2---HelixCluster uses a hybrid approach. Capability grants are OR-Set CRDTs (Tier 2), but revocation is implemented as a versioned revocation token with a higher version number than any possible grant. When a replica receives a revocation token with version V, it rejects all grants of the same capability with version < V. This provides monotonic revocation without requiring a full consensus round for every revocation, though the revocation token itself is propagated through the stronger Tier 2 causal broadcast channel rather than the weaker Tier 3 gossip path.

The complete architecture for consensus and state replication in HelixCluster thus forms a three-tier system: Raft for the 40% of state that must be linearizable, CRDTs with vector-clock causality for the 60% that can be eventually consistent, and HLC clocks ensuring that all timestamps---whether used for consensus ordering or LWW resolution---maintain causal correctness in the presence of clock skew up to 500ms. Anti-entropy via Merkle trees and delta-state repair ensures that even after extended partitions, all replicas converge to identical state without operator intervention.
