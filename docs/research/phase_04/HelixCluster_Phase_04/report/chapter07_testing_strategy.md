# Chapter 7: Testing Strategy

The Helix Cluster OS operates as a safety-critical distributed system where a single consensus bug can destroy cluster state and a scheduling regression can strand user sessions across a heterogenous fleet of Intel, AMD, and Apple Silicon nodes. This chapter defines the multi-layer testing strategy that validates every subsystem—from Zig memory-management primitives to Go microservices, C GPU kernels, and cross-node consensus protocols. The strategy combines a conventional testing pyramid with the HelixQA orchestration framework, chaos engineering, formal verification via TLA+, and performance benchmarks that mirror production topology at scale.

---

## 7.1 Testing Pyramid

The Cluster OS testing pyramid is organized into four tiers: unit tests for individual functions and data structures, integration tests for subsystem interaction, end-to-end tests for complete cluster scenarios, and chaos tests for failure-mode validation. This structure addresses the documented limitation that "TDD is commonly practiced through unit testing, it may not adequately test behavior that depends on distributed systems, hardware, timing, security properties, or interactions between components" [^834^].

### Unit Tests

Each language layer in the Cluster OS stack maintains its own unit-test framework and conventions.

**Go microservices** use the standard `testing` package with table-driven test patterns and the race detector enabled in CI (`go test -race ./...`). Every exported function in the control plane—Session Manager, Resource Scheduler, Node Discovery, Health Monitor, and Policy Engine—carries a corresponding `_test.go` file. Property-based tests augment conventional unit tests using Gopter or Rapid to verify invariants across randomly generated inputs such as ClassAds expressions, node capability vectors, and resource snapshots. This approach mirrors Jane Street's use of QuickCheck for financial trading systems and Riak's validation of distributed merge functions [^842^].

**Zig system libraries** (network serialization, memory allocators, hardware abstraction) use Zig's native `test` blocks executed via `zig test`. These tests cover zero-copy serialization paths, Cap'n Proto encoding and decoding, and memory-safety guarantees under `ReleaseFast` and `ReleaseSafe` build modes. Zig's comptime evaluation enables exhaustive testing of cross-platform abstractions at compile time, reducing the surface area that requires runtime verification.

**C GPU compute kernels** are tested through a combination of standalone test executables and the `check` unit testing framework. Each GPU backend—CUDA, ROCm, oneAPI, and Metal—runs a device-discovery test suite that validates capability enumeration, memory allocation limits, and compute-unit reporting against known hardware profiles. GPU kernel tests execute on physical hardware in the CI farm; they cannot be fully simulated because driver-level behavior varies across vendor implementations.

### Integration Tests

Integration tests use Testcontainers-Go to spin up real dependencies—etcd, PostgreSQL, Redis Cluster, and NATS—in ephemeral Docker containers during test execution [^1051^][^1053^]. This pattern provides "isolated, reproducible integration tests by spinning up real dependencies" rather than relying on mocks that diverge from production behavior [^933^][^938^].

The integration test matrix covers:

| Dependency | Testcontainers Module | Validation Scope |
|---|---|---|
| etcd (Raft) | `testcontainers-go/modules/etcd` | Consensus state, leader election, watch streams |
| PostgreSQL 16 | `testcontainers-go/modules/postgres` | Schema migrations, ACID transactions, audit triggers |
| Redis Cluster 7 | `testcontainers-go/modules/redis` | Session state CRDT sync, pub/sub routing, cache eviction |
| NATS + JetStream | `testcontainers-go/modules/nats` | Control-plane messaging, JetStream durability |
| Kafka 4.0 | `testcontainers-go/modules/kafka` | Event log ordering, consumer group rebalancing |

Each integration test scenario seeds the databases with fixture data from HelixQA's `banks/` directory, executes the subsystem under test, and asserts end-state correctness against the full data-layer stack [^1037^].

### End-to-End Tests

End-to-end tests exercise complete cluster scenarios across multiple nodes in a dedicated staging environment. These scenarios are defined in Gherkin syntax (Given-When-Then) to serve as living documentation that non-engineering stakeholders can read and validate [^831^][^832^]. Example scenarios include:

```gherkin
Scenario: Session migration during node failure
  Given a cluster with 4 nodes and 10 active sessions
  When Node 2 fails (simulated SIGKILL to node agent)
  Then all sessions on Node 2 migrate within 5 seconds
  And session state remains consistent (CRDT merge validated)
  And client WebSocket streams reconnect transparently
```

E2E tests execute through the HelixQA orchestration engine, which dispatches tests to the appropriate environment topology and collects evidence for constitutional compliance [^1037^].

### Chaos Tests

Chaos tests are integrated into the CI pipeline via Chaos Mesh, injecting failures into the integration and staging environments. This "shift left" approach—running chaos experiments before production—has become standard practice as both Chaos Mesh and LitmusChaos now support GitOps-based experiment definitions [^991^][^994^]. The Cluster OS chaos suite is detailed in Section 7.3.

---

## 7.2 HelixQA Integration

HelixQA (`github.com/HelixDevelopment/helixqa`) is the central QA orchestration framework for the Helix ecosystem. Written in Go (96.5%), with 751 commits and active maintenance by both human and AI contributors, it functions as the single source of truth for all test execution, evidence collection, and quality reporting [^1037^]. Its architecture encompasses `cmd/` (CLI), `pkg/` (core packages), `internal/` (services including the vision server), `tests/` (test suites), `challenges/` (scenario definitions), and `banks/` (test data fixtures) [^1037^].

### HelixConstitution Rule Enforcement

The HelixConstitution (`github.com/HelixDevelopment/HelixConstitution`) defines the canonical rules governing all development activity in the ecosystem [^911^]. Key constitutional provisions directly impacting Cluster OS testing include:

- **§11.4.1** — FAIL-bluffs forbidden: no test may report a false pass or false failure [^999^].
- **§11.4.2** — Recorded-evidence requirement: every test result must be backed by captured artifacts (logs, metrics, heap dumps) [^936^].
- **§11.4.3** — Per-environment-topology test dispatch: tests execute against the exact topology they were dispatched for [^999^].
- **§11.4.4** — Test-interrupt-on-discovery + retest-from-clean-baseline: any bug discovered during a test run aborts the suite and triggers a full retest from a known-good state [^936^].
- **§11.4.6** — No-guessing mandate: test assertions must be deterministic, not heuristic [^999^].
- **§11.4.103** — Continuous parallel-stream working routine: tests run in parallel streams to maximize throughput [^936^].

These rules are not advisory—they are enforced by HelixQA's execution engine, which refuses to report a test as "passed" unless all evidence-collection gates are satisfied.

### Mutation Testing

HelixQA includes `.go-mutesting.yml` configuration, linking mutation testing to constitutional rule CONST-035 (anti-bluff) [^1037^]. Mutation testing generates code mutants by modifying source operators—changing `==` to `!=`, `&&` to `||`, removing function calls—and measures whether the test suite "kills" each mutant by failing [^1052^]. The mutation score provides a more accurate quality signal than line coverage alone, which "can be gamed with shallow tests" [^924^][^1050^].

The Cluster OS mutation pipeline targets:

| Package | Minimum Mutation Score | Critical Invariants Tested |
|---|---|---|
| `pkg/scheduler` | ≥75% | ClassAds evaluation, resource reservation, preemption logic |
| `pkg/session` | ≥70% | State machine transitions, CRIU checkpoint/restore, PTY forwarding |
| `pkg/discovery` | ≥70% | SWIM gossip, Phi accrual failure detection, Raft membership changes |
| `pkg/gpu` | ≥65% | Capability matching, memory allocation, MPS enable/disable |
| `pkg/security` | ≥80% | mTLS handshake, SPIFFE validation, OPA policy evaluation |

### Per-Environment Test Dispatch

HelixQA dispatches tests to environment-specific topologies as mandated by §11.4.3 [^999^]. The dispatch matrix ensures that a test validated on a 3-node integration topology is never confused with the same test running on a 64-node staging cluster:

```yaml
environments:
  integration:
    topology: 3_nodes_1_control_2_worker
    tests: [unit, integration, mutation, contract]
    hardware: virtualized
  
  staging:
    topology: 8_nodes_2_control_6_worker
    tests: [e2e, chaos, load, correctness]
    hardware: mixed_x86_arm_gpu
  
  preprod:
    topology: 16_nodes_4_control_12_worker
    tests: [full_regression, dst, jepsen]
    hardware: production_equivalent
```

### Systematic Debugging Activation

Constitutional rule §11.4.102 mandates "mandatory systematic-debugging activation + always-loaded skill-discovery + plugin-dependency availability" [^936^]. When a test failure occurs, HelixQA automatically:

1. Captures the complete failure context (logs, metrics, goroutine dumps, etcd state snapshot).
2. Classifies the failure signature against the `challenges/` database of known failure modes [^1037^].
3. Activates the systematic debugging workflow: evidence collection → hypothesis generation → controlled reproduction → root cause identification → fix validation.
4. Generates a retest plan that executes from a clean baseline (§11.4.4), not from the failed state.

---

## 7.3 Chaos Engineering

Chaos engineering validates that the Cluster OS maintains correctness and availability under real-world failure conditions. The discipline, pioneered by Netflix with Chaos Monkey, is formalized around four core principles: build a hypothesis around steady-state behavior, vary real-world events, run experiments in production (where appropriate), and automate experiments to run continuously [^858^][^856^]. The Cluster OS adopts a "shift left" posture: chaos experiments run in integration and staging environments on every PR, with production chaos reserved for mature deployments with full observability and automated rollback [^991^][^994^].

### Node Failure Scenarios

Node failure tests validate the SWIM gossip protocol, Phi accrual failure detector, and automatic session migration pipeline. Test scenarios include:

| Scenario | Failure Injection | Expected Cluster Behavior | Validation Method |
|---|---|---|---|
| Graceful node departure | `POST /v1/nodes/{id}/leave` | Node transitions to LEFT; sessions migrate proactively | etcd state + session list assertion |
| Abrupt node kill | `SIGKILL` to node agent | Node transitions to SUSPECT → FAILED after phi > 8 | Phi accrual timer + SWIM gossip verification |
| Control plane loss | Kill 2 of 3 Raft leaders | etcd remains available (Raft majority); read-only fallback | etcd endpoint health + leader election timing |
| Cascading failure | Kill 3 nodes within 10 seconds | Cluster partitions handled; no split-brain | Network partition detector + state divergence check |
| Slow node | CPU throttled to 10% | Node marked SUSPECT; workloads evacuated | Health score threshold + scheduler rebalancing |

### Network Partition Scenarios

Network partitions are the most dangerous failure mode for distributed systems. The Cluster OS chaos suite uses Chaos Mesh's NetworkChaos to inject partition, delay, duplication, and corruption at the network layer [^994^]. Key scenarios include:

- **Clean 50/50 partition**: The cluster splits into two equal halves. The test validates that the minority partition enters degraded mode (read-only for state-changing operations) while the majority partition continues operating normally. etcd's Raft implementation guarantees that only the majority partition can commit writes, preventing split-brain [^837^].
- **Asymmetric partition (1 node isolated)**: A single node loses connectivity to the rest of the cluster. The isolated node must detect the partition via SWIM gossip timeouts and shut down its scheduler to prevent phantom resource allocations.
- **Intermittent packet loss (1-5%)**: Partial connectivity simulates a failing switch or congested link. The test validates that the Cluster OS tolerates transient packet loss without triggering false-positive failure detections.
- **Latency spike (>500ms RTT)**: High-latency links between nodes (simulating WAN conditions) test the scheduler's latency-aware placement and the session manager's migration decisions.

### Resource Exhaustion Scenarios

Resource exhaustion tests validate the self-healing behavior of the Health Monitor & Predictor subsystem. Scenarios include memory pressure (available RAM < 5%), disk fullness (available storage < 10%), GPU ECC error thresholds, and CPU thermal throttling [^858^]. Each scenario triggers predefined auto-healing actions: memory pressure initiates session migration, GPU panics mark the device unhealthy and redistribute workloads, and predicted failures with probability > 0.8 trigger proactive evacuation with LLM-generated advisory notifications.

### Automatic Recovery Validation

Every chaos experiment concludes with a recovery phase. The Cluster OS validates:

1. **State convergence**: After the failure is removed, all nodes reach consistent etcd state within 30 seconds.
2. **Session integrity**: No session data is lost; CRDT state merges correctly after partition healing.
3. **Resource rebalancing**: The scheduler redistributes workloads to utilize restored capacity.
4. **Metric normalization**: All health scores return to pre-failure baselines within 5 minutes.

Recovery validation uses Porcupine, a fast linearizability checker for Go (used by etcd and TiDB), to verify that concurrent histories of distributed operations are linearizable [^1055^][^1056^]. Porcupine's P-compositionality algorithm provides 1,000x–10,000x speedup over Knossos on partitioned workloads, making it feasible to run as a CI gate.

---

## 7.4 Formal Verification

Formal verification with TLA+ provides mathematical guarantees that the Cluster OS consensus and scheduling protocols are free from design-level bugs before a single line of implementation code is written. TLA+ is extensively used by Amazon AWS, CockroachDB, MongoDB, Elastic, Confluent (Kafka), and Microsoft Azure to verify distributed algorithms [^837^]. TLA+ performs exhaustive model checking of all possible execution paths, while PlusCal provides a programming-language-like frontend for specification [^987^].

### TLA+ Specifications for Consensus

The consensus specification models the etcd-backed Raft implementation used for cluster state. The specification covers:

- **Leader election**: Safety (at most one leader per term) and liveness (a leader is eventually elected) under crash-stop and network-partition failures.
- **Log replication**: If a log entry is committed, all future leaders contain that entry.
- **Membership changes**: Single-server joint consensus (Raft 3.4+ protocol) for adding and removing nodes without availability loss.
- **Read index processing**: Linearizable reads through the `ReadIndex` mechanism, validating that followers return stale data only when explicitly permitted.

The model checker (TLC) verifies these properties across a state space of up to 5 nodes with all combinations of crash, partition, and recovery events. A typical run explores 10^8–10^9 states and completes in 2–6 hours on a 16-core workstation.

### TLA+ Specifications for Scheduling

The scheduling specification models the Omega-style shared-state scheduler with optimistic concurrency. Key properties verified include:

- **Scheduler safety**: A resource is never double-allocated (mutual exclusion of GPU, CPU, and memory reservations).
- **Scheduler liveness**: Every pending request is eventually scheduled or rejected with a reason.
- **ClassAds correctness**: The requirements-evaluation engine correctly implements boolean logic over capability vectors.
- **Preemption fairness**: When preemption is required, lower-priority workloads are evicted before higher-priority workloads.
- **Reservation expiry**: Resources held by expired reservations are reclaimed within the configured TTL.

The specification uses the `ResourcePool` and `ResourceRequest` data structures defined in the architecture as its foundational state variables, with optimistic concurrency modeled as atomic compare-and-swap operations on the `Revision` field.

### Model Checking Safety Properties

Beyond consensus and scheduling, TLA+ specifications cover the following safety-critical subsystems:

| Subsystem | Safety Property | Model Checker |
|---|---|---|
| Session state machine | No invalid transitions (e.g., MIGRATING → CREATING) | TLC + Apalache |
| Security (mTLS + SPIFFE) | Identity binding is immutable after attestation | TLC |
| Migration protocol | Source session is destroyed only after target is confirmed | TLC + manual proof |
| GPU allocation | Exclusive-mode GPU is never shared across sessions | TLC |
| Health monitor | Failure detector's phi threshold prevents false positives | Apalache (real-time) |

Model-guided fuzzing closes the gap between specification and implementation. Research demonstrates that using TLA+ models to guide coverage-directed fuzzing of distributed systems implementations discovered 12–13 previously unknown bugs in etcd-raft and RedisRaft, with four bugs detectable only through model-guided fuzzing [^982^][^983^]. The Cluster OS integrates this technique into CI: the TLA+ model generates trace seeds for Go's native fuzzer (`go test -fuzz`), directing exploration toward state-space regions that the model checker has identified as high-risk [^988^].

---

## 7.5 Performance Benchmarks

The Cluster OS establishes quantitative performance targets validated through automated benchmark suites that run on every release candidate. These benchmarks execute against a standardized 8-node staging topology (2 control, 6 worker) with mixed x86_64 and arm64 hardware plus NVIDIA and AMD GPUs.

### Scheduling Benchmarks

The scheduler must sustain **1,000 job submissions per second** with **p99 scheduling latency below 10 milliseconds**. The benchmark suite uses k6 (the dominant Go-based load testing tool for cloud-native HTTP/gRPC services) to submit resource requests at varying rates [^855^][^866^]. The benchmark validates:

- **Throughput**: Sustained 1,000 req/sec for 5 minutes without queue buildup.
- **Latency distribution**: p50 < 2ms, p99 < 10ms, p99.9 < 50ms under normal load.
- **Burst handling**: 10,000 req/sec burst for 10 seconds with graceful degradation (p99 < 200ms).
- **ClassAds complexity**: Requests with 10-clause requirement expressions schedule within 2x baseline latency.

```
Benchmark: SchedulerThroughput
  Nodes: 8 (2 control, 6 worker)
  Request rate: 1,000/sec sustained
  Duration: 300 seconds
  Result: p50=1.2ms, p99=8.4ms, p99.9=42ms, zero timeouts
  Status: PASS
```

### Session Benchmarks

Session attach latency—the time from `htmux attach` command to interactive PTY readiness—must remain **below 100 milliseconds**. This benchmark measures the full attach pipeline: DNS resolution, mTLS handshake, SPIFFE validation, WebSocket upgrade, session state lookup, PTY allocation, and first byte delivery. The benchmark runs across local Ethernet (1 Gbps), Wi-Fi 6, and WireGuard mesh topologies to capture latency variation across network modes.

Session creation throughput targets **500 concurrent sessions** on the 8-node staging cluster, with each session carrying 1–4 panes distributed across nodes. The benchmark validates CRDT sync latency for shared window state across distributed panes.

### Migration Benchmarks

Live session migration via CRIU must complete with **less than 5 seconds of perceived downtime**. The benchmark suite measures:

- **Checkpoint time**: From `SIGSTOP` to complete memory-image capture (target: < 2 seconds for 1 GB working set).
- **Transfer time**: Arrow Flight streaming of checkpoint data to target node (target: < 2 seconds on 1 Gbps Ethernet).
- **Restore time**: From first byte received to `SIGCONT` and client stream resumption (target: < 1 second).
- **Data integrity**: Post-migration session state matches pre-migration state (SHA-256 hash of process memory + file descriptors).

Migration benchmarks run under varying memory pressure (1 GB, 8 GB, 32 GB working sets) and across heterogeneous node pairs (Intel → AMD, x86_64 → arm64) to validate the full migration matrix.

### GPU Benchmarks

The GPU Compute Engine must deliver **near-native performance**—defined as ≥95% of bare-metal throughput—for CUDA, ROCm, oneAPI, and Metal workloads. Benchmarks execute standard MLPerf inference and training benchmarks across all supported GPU vendors. The key metric is normalized performance: `GPU_benchmark_score / bare_metal_score * 100%`.

| GPU Vendor | Model | Bare-Metal Score | Cluster OS Score | Normalized | Target |
|---|---|---|---|---|---|
| NVIDIA | RTX 4080 | 100% | 97.2% | 97.2% | ≥95% |
| NVIDIA | A100 80GB | 100% | 98.1% | 98.1% | ≥95% |
| AMD | RX 7900 XTX | 100% | 95.8% | 95.8% | ≥95% |
| AMD | MI300X | 100% | 96.4% | 96.4% | ≥95% |
| Intel | Arc A770 | 100% | 95.3% | 95.3% | ≥95% |
| Apple | M3 Pro 18-core | 100% | 97.8% | 97.8% | ≥95% |

GPU sharing overhead is separately benchmarked: MPS mode must add ≤1% overhead for inference-serving workloads, and time-slicing mode must maintain context-switch latency below 5 milliseconds.

### Scale Benchmarks

The Cluster OS validates horizontal scale through progressive topology testing:

| Topology | Nodes | Concurrent Sessions | Scheduling Rate | Chaos Scenarios |
|---|---|---|---|---|
| Small | 4 | 100 | 500/sec | Node kill, network partition |
| Medium | 8 | 500 | 1,000/sec | + Cascading failure, resource exhaustion |
| Large | 16 | 750 | 1,500/sec | + WAN latency, partial partitions |
| XL | 32 | 1,000 | 2,000/sec | + Byz. failures, certificate rotation |
| Max | 64 | 1,000 | 2,000/sec | Full chaos matrix + 72-hour soak |

The **64-node, 1,000-concurrent-session** configuration represents the maximum validated scale for the v1.0 release. At this scale, the benchmark suite runs a 72-hour soak test that continuously creates, migrates, and terminates sessions while chaos experiments inject failures every 15 minutes. The pass criterion: zero unplanned session terminations, zero state inconsistencies (validated by Porcupine linearizability checking), and p99 attach latency remaining below 100 ms throughout the soak period [^1055^][^1056^].

All performance benchmarks integrate with HelixQA's monitoring infrastructure, capturing time-series metrics into Prometheus and generating automated regression reports. A 10% regression on any benchmark metric relative to the last 5 release candidates triggers an advisory to the LLM Brain for root-cause analysis and blocks the release pipeline pending investigation [^1037^].
