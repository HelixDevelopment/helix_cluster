# Chapter 6: Implementation Plan

## 6.1 Phase Overview

The Helix Cluster OS implementation follows a rigorously structured nine-phase methodology spanning 50 calendar weeks, encompassing 970 top-level tasks that decompose into over 10,000 granular sub-tasks. This plan represents approximately 3,880 person-hours of engineering effort, organized into 38 sub-phases with explicit dependency chains, risk mitigations, and measurable acceptance criteria for every deliverable. The methodology balances parallel workstream execution with disciplined sequential dependencies, ensuring that each phase produces a verifiable, production-ready increment of the system.

The phasing strategy reflects a deliberate architectural sequencing: foundational libraries and infrastructure must precede distributed systems primitives; resource management capabilities must be established before session scheduling can function; the session manager must be operational before the build service can execute distributed compilation; and the LLM advisory brain requires telemetry streams from all prior subsystems to generate meaningful recommendations. This ordering is not arbitrary---it mirrors the dependency topology of the system itself, where each layer exposes capabilities that the next layer consumes.

| Phase | Name | Weeks | Sub-Phases | Top-Level Tasks | Person-Hours | Cumulative Weeks |
|-------|------|-------|-----------|-----------------|-------------|------------------|
| 0 | Foundation | 1--4 | 4 | 180 | 720 | 4 |
| 1 | Core Infrastructure | 5--12 | 7 | 200 | 800 | 12 |
| 2 | Resource Management | 13--18 | 5 | 140 | 560 | 18 |
| 3 | Session Manager | 19--26 | 6 | 180 | 720 | 26 |
| 4 | Build Service | 27--30 | 3 | 40 | 160 | 30 |
| 5 | LLM Brain | 31--36 | 4 | 60 | 240 | 36 |
| 6 | Security Hardening | 37--40 | 1 | 30 | 120 | 40 |
| 7 | QA & Testing | 41--46 | 4 | 60 | 240 | 46 |
| 8 | Polish & Release | 47--50 | 4 | 80 | 320 | 50 |
| **Total** | | **50** | **38** | **970** | **3,880** | |

The task enumeration methodology employs a hierarchical identifier scheme `[PHASE].[SUB-PHASE].[TASK_NUMBER].[SUB_TASK]` that enables precise dependency tracking. Every task specifies four attributes: estimated duration in hours, priority level (P0 through P3), required skill tags (Go, Zig, C/C++, networking, security, ML, QA, operations, documentation), and concrete acceptance criteria. Dependencies are expressed as directed acyclic graphs, with the build system's task scheduler validating topological ordering before execution.

The 970 top-level tasks decompose into 10,000+ sub-tasks through systematic subdivision. Each top-level task averaging 10+ sub-tasks during sprint planning ensures that every function, every test case, and every documentation page has an explicit tracking identifier. This granularity enables precise progress measurement, bottleneck identification, and resource reallocation when critical path tasks encounter delays.

## 6.2 Phase 0: Foundation (Weeks 1--4)

Phase 0 establishes the technical bedrock upon which all subsequent development depends. Without the build system, core libraries, CI/CD pipeline, and protocol definitions validated in this phase, no meaningful implementation can proceed. This phase demands 180 tasks across four sub-phases: Project Bootstrap (0.1), Core Libraries (0.2), Protocol Definitions (0.3), and Database Setup (0.4).

**Project Bootstrap (Week 1, 40 tasks)** establishes the monorepo structure with directories for commands (`/cmd`), packages (`/pkg`), internal libraries (`/internal`), API definitions (`/api`), web assets (`/web`), documentation (`/docs`), deployment manifests (`/deploy`), and test suites (`/test`). The Go workspace is initialized with `go.work` defining all modules, while the Zig build system (`build.zig`) is configured for cross-compilation targeting x86_64 and aarch64. A Docker Compose development environment provisions PostgreSQL 16, Redis Cluster 7, etcd (3-node), NATS with JetStream, Apache Kafka 4.0 (KRaft mode), Grafana, and Prometheus---all dependencies required for integration testing throughout development.

The CI/CD pipeline (10 tasks) comprises GitHub Actions workflows for Go, Zig, and C/C++ builds, integrated with Codecov for coverage reporting, golangci-lint with 20+ linters, Dependabot for dependency updates, and release automation producing multi-architecture binaries. Pre-commit hooks enforce lint, format, and test execution before any commit reaches the repository. ArgoCD manifests enable GitOps deployment to development environments.

**Core Libraries (Weeks 1--2, 80 tasks)** represent the most labor-intensive sub-phase of Phase 0, producing 25 Go shared libraries, 10 Zig system libraries, and 10 C/C++ GPU libraries. The Go libraries cover foundational concerns: structured error handling with gRPC status mapping (`pkg/errors`), slog-based logging with context propagation (`pkg/log`), Viper-based configuration management (`pkg/config`), AES-GCM and ChaCha20-Poly1305 encryption (`pkg/crypto`), network utilities including NAT detection (`pkg/netutil`), exponential backoff with jitter (`pkg/retry`/`pkg/backoff`), OpenTelemetry tracing (`pkg/tracing`), HTCondor-style ClassAds parsing (`pkg/classads`), and unified serialization adapters for JSON, MessagePack, and Cap'n Proto (`pkg/serde`).

The Zig libraries address performance-critical system components: Cap'n Proto message builder/reader with zero-copy semantics (`zig-serde`), TCP/UDP socket abstractions over epoll/kqueue/IOCP (`zig-net`), length-prefixed binary protocol framing (`zig-protocol`), ZeroMQ ZMTP bindings (`zig-zeromq`), ChaCha20-Poly1305 and X25519 for WireGuard (`zig-crypto`), lock-free ring buffers with cache-line padding (`zig-ring`), and pseudo-terminal management (`zig-pty`). The GPU detection library (`zig-gpu`) enumerates all GPU vendors---NVIDIA, AMD, Intel, Apple---querying memory capacity, compute capability, and hardware features through NVML, ROCm-SMI, Level Zero, and MLX APIs.

The C/C++ GPU libraries provide vendor-specific backends: CUDA (`cc-cuda`), ROCm/HIP (`cc-rocm`), Intel oneAPI/Level Zero (`cc-oneapi`), and Apple MLX (`cc-mlx`). Each backend implements device enumeration, unified memory allocation, kernel launch, stream synchronization, and metrics collection. The LD_PRELOAD interception library (`cc-interpose`) enables CUDA call forwarding to remote GPUs, achieving compatibility with the HAMi multi-tenant GPU scheduling model.

**Protocol Definitions (Week 2, 20 tasks)** produce the complete gRPC service contract for the entire system. Nine protobuf definitions specify NodeService (join, heartbeat, leave), SessionService (create, attach, detach, migrate), SchedulerService (schedule, cancel, reserve), HealthService (cluster health, failure prediction), AdvisoryService (LLM-generated advisories), SecurityService (authentication, authorization), and BuildService (RBE execution). Avro schemas define event types for node lifecycle (NodeJoined, NodeLeft, NodeFailed), session events (SessionCreated, SessionMigrated), scheduler events (JobScheduled, JobPreempted), and comprehensive audit events. Kafka topic and NATS stream definitions establish the event bus topology.

**Database Setup (Weeks 2--3, 40 tasks)** implements the persistence layer across three stores. PostgreSQL 16 migrations create 12 core tables---nodes, GPU devices, sessions, windows, panes, reservations, migration history, audit log (monthly partitioned), users, health snapshots, and LLM advisories---with automatic partition management, audit triggers, and update timestamp propagation. The etcd schema implements hierarchical key structures for node registry, session routing tables, distributed locks, leader election, and compare-and-swap transactions. Redis Cluster schemas define session state caches with CRDT vector clocks, resource pool caches with atomic updates, GPU status caches, rate limiters using Lua scripts, and cache invalidation frameworks with multi-level coherence.

## 6.3 Phase 1: Core Infrastructure (Weeks 5--12)

Phase 1 builds the distributed systems substrate: node discovery via SWIM gossip, WireGuard mesh networking, multi-transport messaging, the API gateway, and security infrastructure. These 200 tasks across seven sub-phases produce the cluster's nervous system---the components that transform individual machines into a coherent distributed entity.

**Node Discovery Service (Weeks 5--6, 40 tasks)** implements the complete SWIM gossip protocol with phi-accrual failure detection. The implementation covers six message types (Ping, PingReq, Ack, Suspect, Alive, Dead) over UDP transport with zstd compression achieving 50%+ bandwidth reduction and AES-256-GCM encryption. The phi-accrual detector adapts sensitivity based on observed network conditions, maintaining a false positive rate below 1%. Indirect ping via K random witnesses handles transient network congestion. Node fingerprinting detects CPU architecture (x86_64, ARM64), feature flags (AVX, NEON), memory topology (channels, NUMA), GPU models from all vendors, storage types (NVMe, SSD, HDD), and network interface speeds. Bootstrap discovery uses mDNS with manual IP fallback and rendezvous server options. Split-brain detection implements quorum checks with automatic partition recovery and conflict resolution.

**WireGuard Mesh Network (Weeks 6--7, 30 tasks)** constructs the encrypted overlay network. The implementation uses the `wgctrl` Go bindings for device and peer management, with automatic X25519 key pair generation and rotation. Full mesh topology forms dynamically as nodes join, with automatic subnet allocation from the 100.64.0.0/10 CGNAT range. NAT traversal implements STUN for public IP discovery, UDP hole punching for direct connection through most NAT types, DERP relay fallback for symmetric NAT scenarios, and reverse SSH tunnel as the final fallback. The fallback chain (Direct → Hole Punch → Relay → SSH Tunnel) automatically promotes connections when higher-quality paths become available. Connection health monitoring measures RTT and packet loss, triggering automatic reconnection with exponential backoff and circuit breaker protection.

**Messaging Infrastructure (Week 7, 20 tasks)** integrates two complementary transport systems. NATS with JetStream handles control-plane messaging: connection pooling with auto-reconnect, durable consumers with backoff retry, request-reply with scatter-gather patterns, and dead letter queues with alerting. Apache Kafka 4.0 in KRaft mode handles event sourcing: exactly-once processing via transactional producers and idempotent consumers, event append with snapshot support, and time-based replay capabilities.

**API Gateway (Weeks 7--8, 30 tasks)** provides the unified external interface. The Gin HTTP server implements a middleware stack including mTLS authentication with SPIFFE ID extraction, Open Policy Agent (OPA) authorization with RBAC enforcement, sliding-window rate limiting, structured request/response logging with correlation IDs, panic recovery, CORS, gzip/Brotli compression, and Prometheus metrics exposition. WebSocket upgrade handling supports subprotocol negotiation for I/O streaming. The gRPC-Gateway layer proxies REST calls to backend gRPC services with round-robin load balancing across healthy instances.

**Security Manager (Weeks 8--9, 20 tasks)** implements the SPIFFE/SPIRE identity framework. Node attestation uses challenge-response proof-of-possession during join. X.509 SVIDs are issued with 24-hour TTL and automatic rotation at 80% expiry. Workload identity propagation carries SPIFFE IDs through gRPC metadata and HTTP headers. OPA integration provides policy evaluation with RBAC policies (Admin, Operator, User roles), node-level policies restricting cross-node modifications, and hot-reload of policy changes without restart.

**Control Plane Deployment (Weeks 9--10, 20 tasks)** orchestrates service lifecycle management. An ordered startup manager verifies dependencies and health before declaring services ready. Graceful shutdown handles signal propagation, connection draining, and resource cleanup. Leader election via etcd ensures active-standby control plane pairs. Helm charts and Docker Compose production configurations enable deployment to Kubernetes or bare-metal environments.

**Integration & Testing (Weeks 11--12, 40 tasks)** validates the complete Phase 1 stack. End-to-end tests verify node join flows (setup → network → register → mesh), network partition recovery (partition → detect → recover → verify consistency), and security (mTLS → attestation → authorization → rotation). Chaos engineering experiments inject random node failures and network partitions, measuring cluster recovery time. Performance benchmarks establish baseline metrics for node join latency, gossip bandwidth consumption, and mesh throughput.

## 6.4 Phase 2: Resource Management (Weeks 13--18)

Phase 2 implements the cluster's economic engine---the resource aggregator, Omega-model scheduler, GPU compute engine, and health monitor. These 140 tasks across five sub-phases determine how efficiently the cluster utilizes its heterogeneous hardware, directly impacting user-visible performance for every workload type.

**Resource Aggregator (Week 13, 20 tasks)** collects hardware telemetry through multiple probes. The cgroups v2 reader captures per-cgroup CPU, memory, and I/O utilization. A /proc reader provides system-level CPU and memory statistics. An eBPF probe loader attaches kernel-level programs for high-frequency metrics collection without /proc polling overhead. GPU resource collection aggregates per-device memory, utilization, temperature, and power draw across all vendor backends. The aggregation pipeline sums resources across nodes, tracks available versus total capacity, and propagates updates to the scheduler via pub/sub channels. Historical usage tracking with time-series storage enables trend analysis and capacity planning projections.

**Scheduler (Weeks 13--16, 60 tasks)** implements the Omega-model shared-state design, a production-proven architecture from Google's cluster management research that maintains a cached cluster state snapshot in memory, allowing scheduling decisions to proceed in parallel with optimistic concurrency control via etcd compare-and-swap operations. The scheduling queue separates active and backoff queues with priority ordering (FIFO within priority). Multiple scheduling goroutines operate in parallel with automatic conflict resolution.

Eleven scheduling plugins implement the decision logic: NodeResourcesFit filters and scores by CPU/memory/GPU availability; NodeAffinity enforces required and preferred placement constraints; TopologyAware places workloads with NUMA and PCI locality awareness; CapabilityMatch evaluates ClassAds expressions for GPU and hardware feature matching; GangScheduling enables all-or-nothing coscheduling for multi-pane sessions; LoadAware preferentially targets underutilized nodes; and PrioritySort, LocalityAware, InterPodAffinity, and VolumeBinding plugins address specialized scheduling concerns. The plugin registration framework supports dynamic loading and ordering.

Scheduling operations include pessimistic resource reservations with TTL and automatic release, preemption logic for lower-priority eviction with graceful termination, and atomic binding commits that deduct resources from the cluster pool. The scheduler's performance targets---1,000 scheduling decisions per second with sub-10ms p99 latency---are validated through dedicated benchmark suites.

**GPU Compute Engine (Weeks 15--16, 30 tasks)** implements the vendor-specific compute backends. Each backend (CUDA, ROCm, oneAPI, MLX) provides device enumeration, unified memory allocation, kernel launch, stream synchronization, and metrics collection. The GPU backend manager auto-detects vendor hardware and loads the correct backend. Memory management implements allocation tracking, out-of-memory prevention, and garbage collection. NVIDIA Multi-Process Service (MPS) enables fractional GPU sharing, while time-slicing provides configurable GPU sharing intervals. The gRPC handler exposes Execute, Allocate, and Metrics RPCs for remote GPU access.

**Health Monitor (Weeks 17--18, 20 tasks)** implements predictive failure detection and self-healing. A Prometheus metrics collector aggregates custom service metrics. An eBPF probe loader attaches kernel-level observability programs. Composite health scores (0--100) weight CPU, memory, disk, network, GPU, and service health components. An LSTM-based failure prediction model targets 85%+ accuracy with a 30--90 day prediction horizon. An isolation forest anomaly detector provides real-time scoring with false positive rates below 5%. The self-healing executor triggers automatic actions: memory pressure triggers session migration, GPU panics trigger workload evacuation, and disk exhaustion triggers log rotation and cleanup.

**Phase 2 Integration (Week 18, 10 tasks)** validates the complete resource management stack through end-to-end scheduling tests (submit → schedule → bind → execute → complete), GPU job scheduling (request → match capability → allocate → execute), preemption verification (low priority evicted for high priority), and health prediction accuracy testing.

## 6.5 Phase 3: Session Manager (Weeks 19--26)

Phase 3 delivers the system's primary user-facing abstraction---the distributed session manager that extends tmux semantics across the entire cluster. These 180 tasks across six sub-phases implement four backend multiplexer integrations, distributed session lifecycle management, I/O forwarding over WebSocket, transparent session migration via CRIU and DMTCP, and the `htmux` CLI.

**Session Backend Abstraction (Weeks 19--20, 20 tasks)** defines the `SessionBackend` interface specifying lifecycle, I/O, and migration contracts. A backend factory creates instances by type (tmux, Zellij, screen, native). Each backend implements health checking and metrics collection. The tmux backend (12 tasks) implements process management, control mode client with response parsing, session/window/pane CRUD, I/O capture via `%output` events, input injection with special key handling, resize handling, layout management, and notification subscription. The Zellij backend (6 tasks) implements IPC client communication, session/tab/pane management, and WebAssembly plugin support. The screen backend (5 tasks) provides session and window management with I/O handling. The native backend (4 tasks) implements direct PTY allocation without multiplexer intermediation.

**Distributed Session Core (Weeks 20--22, 40 tasks)** implements cluster-aware session management. Session creation allocates resources through the scheduler and starts the selected backend. Attachment establishes WebSocket I/O streams with state resumption. Detachment allows client disconnect while the session continues execution. A state machine governs transitions: CREATING → RUNNING → MIGRATING → PAUSED → TERMINATED. Window management implements CRUD operations, layout management (tiled, floating, custom), and Yjs-style CRDT synchronization for distributed window state. Pane scheduling places panes on optimal nodes with GPU allocation when required. Pane I/O proxies PTY data across nodes via ZeroMQ with CRDT synchronization maintaining consistency.

**I/O Forwarding (Weeks 22--24, 30 tasks)** manages the data path from backend PTY to client terminal. Platform-specific PTY allocation handles Linux and macOS variants. A PTY I/O multiplexer uses select/epoll for non-blocking reads and writes. The WebSocket server upgrades HTTP connections, implements binary message framing for output and text framing for commands, per-message deflate compression achieving 60%+ bandwidth reduction, heartbeat ping/pong for stale connection detection, and seamless client reconnection with position resumption. Input handling processes keyboard events, special keys, and mouse events. Output rendering supports VT100, xterm, and 256-color terminal emulation with ring buffer flow control.

**Session Migration (Weeks 24--26, 40 tasks)** enables transparent workload relocation. The CRIU integration (6 tasks) wraps checkpoint and restore operations with TCP repair mode handling, PTY state capture, and Arrow Flight streaming of checkpoint files. The DMTCP integration (3 tasks) provides an alternative checkpoint mechanism. The migration orchestration layer (6 tasks) implements a decision engine triggering migration on node failure or resource pressure, a coordinator managing the checkpoint → transfer → restore → handover sequence, client I/O redirection to the new node, and automatic rollback on migration failure. Migration metrics track duration, data transferred, and success rates.

**HTMUX CLI (Weeks 25--26, 30 tasks)** provides the user interface. Built on the Cobra framework, the CLI implements `htmux ls` (session listing), `htmux new` (session creation with GPU and mode specification), `htmux attach` (WebSocket I/O attachment), `htmux kill-session`, window commands (`new-window`, `select-window`, `kill-window`), pane commands (`split-window`, `resize-pane`, `send-keys`), `htmux status` (cluster overview), node management (`node list`, `node show`, `node remove`), GPU inventory display, and shell completions for bash, zsh, and fish.

**Phase 3 Integration (Week 26, 20 tasks)** validates the complete session lifecycle (create → attach → I/O → detach → reattach → kill), end-to-end migration with active client reconnection, distributed panes across multiple nodes, and chaos tests killing nodes during active sessions to verify automatic migration.

## 6.6 Phase 4: Build Service (Weeks 27--30)

Phase 4 implements the Remote Build Execution (RBE) service and Android Open Source Project (AOSP) integration, comprising 40 tasks across three sub-phases. This phase leverages the distributed infrastructure established in prior phases to deliver the first high-value user-facing application: massively parallel compilation.

**Bazel RBE Implementation (Weeks 27--28, 20 tasks)** implements the Remote Execution API server with Execute, WaitExecution, and GetCapabilities RPCs. The action cache provides content-addressed storage with GetActionResult and UpdateActionResult operations. The Content-Addressed Storage (CAS) implements FindMissingBlobs, BatchUpdateBlobs, BatchReadBlobs, and GetTree operations. The execution service queues jobs and streams responses to clients. Worker pool management handles registration, lease management, and health checks. Buildbarn-compatible API integration ensures interoperability with existing Bazel RBE clients.

**AOSP Integration (Weeks 29--30, 15 tasks)** specializes the RBE service for Android builds. Build detection identifies Android.bp and Android.mk files. Soong/Blueprint analysis extracts module dependencies for optimized scheduling. A distcc worker pool distributes C/C++ compilation across cluster nodes. ccache/sccache integration provides shared caching with hit rate tracking. Ninja job distribution breaks build actions into cluster-schedulable units with progress reporting.

**Phase 4 Integration (5 tasks)** validates the complete pipeline through end-to-end AOSP build tests distributing compilation across three cluster nodes.

## 6.7 Phase 5: LLM Brain (Weeks 31--36)

Phase 5 integrates the LLMsVerifier framework and implements the advisory system that provides intelligent cluster optimization. These 60 tasks across four sub-phases transform raw telemetry into actionable recommendations with constitutional safety guarantees.

**LLMsVerifier Integration (Week 31, 15 tasks)** creates the Go SDK wrapper for the LLMsVerifier framework, implementing provider adapters for Kimi (Moonshot AI), DeepSeek V4, and Claude (Anthropic). A circuit breaker pattern provides fail-fast behavior during provider outages with automatic recovery. Exponential backoff retry with configurable timeouts handles transient failures. Response caching with TTL-based invalidation reduces API costs for similar prompts.

**Advisory System (Weeks 32--34, 25 tasks)** builds the recommendation pipeline. A RAG (Retrieval-Augmented Generation) knowledge base ingests cluster documentation and operational guides, storing vector embeddings for similarity search. Context window management implements token counting, intelligent truncation, and summarization to fit within model constraints. Chain-of-thought generation produces step-by-step reasoning traces for each advisory. The advisory creation pipeline transforms metrics and events into structured advisories with risk assessment scoring (impact × probability classification). Auto-approval logic automatically applies low-risk, high-confidence recommendations, while a human review queue notifies operators of medium and high-risk items requiring approval.

**Learning & Adaptation (Week 35, 10 tasks)** implements continuous improvement. A metrics ingestion pipeline pulls Prometheus data for normalization and storage. Pattern recognition detects recurring operational patterns and correlates seemingly unrelated events. A reinforcement learning feedback loop learns from approved and rejected advisories to improve future recommendations. Configuration optimization suggests tunable parameter changes with predicted impact analysis.

**Constitutional Enforcement (Week 36, 10 tasks)** implements safety guardrails. The HelixConstitution parser extracts rules from the constitutional document. A safety constraint validator checks every advisory against constitutional constraints, rejecting violations before they reach the approval pipeline. An explanation generator produces human-readable justifications for every approval or rejection decision.

## 6.8 Phase 6: Security Hardening (Weeks 37--40)

Phase 6 hardens the system for production deployment through comprehensive security measures. These 30 tasks focus on Zero Trust implementation, certificate lifecycle management, secrets handling, runtime protection, and third-party security validation.

Certificate rotation automation ensures X.509 SVIDs rotate before expiry with zero downtime. HashiCorp Vault integration provides dynamic secrets with automatic revocation. Network policies implement micro-segmentation with ingress and egress rules restricting inter-service communication. Runtime security deploys seccomp system call filtering and AppArmor profiles. Supply chain security generates Software Bill of Materials (SBOM) artifacts, achieves SLSA compliance, and integrates sigstore for artifact signing. A third-party security audit and penetration test produces a findings report with all critical and high-severity items remediated before release.

## 6.9 Phase 7: QA & Testing (Weeks 41--46)

Phase 7 validates production readiness through comprehensive testing at multiple levels of abstraction. These 60 tasks across four sub-phases ensure that the system meets reliability, performance, and correctness requirements before release.

**HelixQA Integration (20 tasks)** integrates the HelixQA test runner for challenge-based evaluation and constructs a comprehensive test suite covering unit, integration, and end-to-end tests for all components. Mutation testing with go-mutesting targets a 70%+ mutation score, ensuring test suite effectiveness at detecting logic changes. Property-based testing applies Hypothesis/QuickCheck-style randomized testing to core scheduling, consensus, and state machine logic.

**Chaos Engineering (15 tasks)** deploys Chaos Mesh for systematic fault injection. Node failure experiments randomly kill cluster nodes, measuring recovery time and session survival. Network partition experiments verify split-brain prevention and automatic merge behavior. Resource exhaustion experiments apply CPU and memory pressure, verifying graceful degradation rather than cascading failures. A 48-hour continuous chaos run validates that the system maintains a failure rate below 0.1% under sustained perturbation.

**Formal Verification (10 tasks)** applies TLA+ specifications to the consensus and scheduling algorithms. The consensus specification model-checks the etcd/Raft implementation, verifying that all safety properties hold under crash, partition, and delay scenarios. The scheduling specification model-checks the Omega shared-state scheduler, proving that binding conflicts resolve correctly and that no session is scheduled to an unavailable node.

**Phase 7 Completion (15 tasks)** executes the final acceptance criteria. Performance acceptance testing validates 64-node clusters supporting 1,000 concurrent sessions with 99.9% availability. Security acceptance confirms penetration test pass with no critical vulnerabilities. Documentation of the complete test strategy captures all test levels, coverage targets, and execution procedures.

## 6.10 Phase 8: Polish & Release (Weeks 47--50)

Phase 8 prepares the system for general availability through setup automation, packaging, documentation, and release engineering. These 80 tasks across four sub-phases ensure that the first user experience is seamless and that ongoing operations are well-supported.

**Setup Wizard (15 tasks)** implements a single-command installation (`curl ... | bash`) with hardware auto-detection for CPU, GPU, memory, and network. Automatic driver installation for detected GPU hardware simplifies the most error-prone configuration step. WireGuard mesh auto-formation detects peers and establishes encrypted connections. Progress reporting provides real-time installation status with ETA and clear error messages. Error recovery implements rollback on failure with retry options. A non-interactive `--yes` mode enables CI/CD and headless deployments.

**Packaging (15 tasks)** produces distribution artifacts for all target platforms: Debian/Ubuntu packages with systemd service configuration, macOS Homebrew formulas with launchd services, multi-architecture Docker images (amd64, arm64) with optimized layer caching, and Helm charts for complete Kubernetes deployment. Release automation triggers artifact generation and distribution on version tag push.

**Documentation (20 tasks)** delivers three comprehensive guides: the User Guide covering installation, configuration, and daily usage; the Administrator Guide covering deployment, cluster management, and troubleshooting; and the Developer Guide covering build procedures, contribution guidelines, and extension patterns. API documentation generates from OpenAPI specifications. Architecture diagrams use Mermaid and C4 notation. A troubleshooting guide addresses 20+ common issues, and a FAQ answers the most frequent user questions.

**Release (30 tasks)** executes the release sequence: Release Candidate 1 tagging with full regression testing, bug remediation for all P0 and P1 findings, Release Candidate 2, final v1.0.0 tagging with artifact publication, 48-hour post-release monitoring, and a comprehensive project retrospective.

## 6.11 Critical Path Analysis

The critical path determines the minimum project duration and identifies tasks where delays directly impact the final delivery date. The following Gantt-style diagram illustrates phase dependencies, parallel workstreams, and milestone markers across the 50-week timeline.

```mermaid
gantt
    title Helix Cluster OS Implementation Timeline
    dateFormat YYYY-MM-DD
    axisFormat Week %W

    section Phase 0
    Foundation          :phase0, 2025-01-06, 4w

    section Phase 1
    Discovery           :phase1a, after phase0, 2w
    WireGuard Mesh      :phase1b, after phase0, 2w
    Messaging           :phase1c, after phase1a, 1w
    API Gateway         :phase1d, after phase1c, 2w
    Security Manager    :phase1e, after phase1d, 2w
    Control Plane       :phase1f, after phase1e, 2w
    P1 Integration      :phase1g, after phase1f, 2w

    section Phase 2
    Resource Aggregator :phase2a, after phase1g, 1w
    Scheduler           :phase2b, after phase1g, 4w
    GPU Engine          :phase2c, after phase2a, 2w
    Health Monitor      :phase2d, after phase2c, 2w

    section Phase 3
    Session Backends    :phase3a, after phase2b, 2w
    Distributed Session :phase3b, after phase3a, 3w
    I/O Forwarding      :phase3c, after phase3b, 2w
    Session Migration   :phase3d, after phase3c, 2w
    HTMUX CLI           :phase3e, after phase3d, 2w

    section Phase 4
    Bazel RBE           :phase4a, after phase3e, 2w
    AOSP Integration    :phase4b, after phase4a, 2w

    section Phase 5
    LLMsVerifier        :phase5a, after phase4b, 1w
    Advisory System     :phase5b, after phase5a, 3w
    Learning            :phase5c, after phase5b, 1w
    Constitution        :phase5d, after phase5c, 1w

    section Phase 6
    Security Hardening  :phase6, after phase5d, 4w

    section Phase 7
    HelixQA & Testing   :phase7a, after phase6, 2w
    Chaos Engineering   :phase7b, after phase7a, 2w
    Formal Verification :phase7c, after phase6, 2w
    Acceptance          :phase7d, after phase7b, 2w

    section Phase 8
    Setup & Packaging   :phase8a, after phase7d, 2w
    Documentation       :phase8b, after phase8a, 2w
    Release             :phase8c, after phase8b, 2w

    milestones
    M1: Foundation Complete    :milestone, after phase0, 0d
    M2: Cluster Bootstrap      :milestone, after phase1g, 0d
    M3: Scheduling Ready       :milestone, after phase2b, 0d
    M4: Session Manager Ready  :milestone, after phase3e, 0d
    M5: Build Service Ready    :milestone, after phase4b, 0d
    M6: LLM Brain Active       :milestone, after phase5d, 0d
    M7: Security Hardened      :milestone, after phase6, 0d
    M8: QA Complete            :milestone, after phase7d, 0d
    M9: v1.0.0 Release         :milestone, after phase8c, 0d
```

The critical path follows the sequence: Phase 0 → Phase 1 → Phase 2 (Scheduler) → Phase 3 → Phase 4 → Phase 5 → Phase 6 → Phase 7 (Chaos & Acceptance) → Phase 8. Several parallel workstreams reduce overall duration: the WireGuard mesh builds concurrently with node discovery; GPU engine development overlaps with scheduler implementation; formal verification proceeds in parallel with chaos engineering.

Key dependency chains include:

1. **Foundation → All**: Phase 0 libraries and protocols are prerequisites for every subsequent phase. Any delay in core libraries (pkg/errors, pkg/log, pkg/config) or protocol definitions blocks all implementation work.

2. **Discovery → Mesh → Messaging → Gateway → Security → Control Plane**: Phase 1 has an internal critical path requiring sequential completion of its sub-phases. The API Gateway depends on messaging infrastructure; security depends on the gateway; control plane deployment depends on all prior sub-systems.

3. **Scheduler → Session Core → I/O → Migration**: Phase 3 session management cannot function without the Phase 2 scheduler providing node placement decisions. Session migration depends on I/O forwarding for client handover.

4. **Health Monitor → LLM Brain**: The advisory system requires health metrics and event streams from the health monitor to generate meaningful recommendations.

5. **Security Hardening → QA → Release**: Security hardening must complete before formal security acceptance testing. All prior phases must be functionally complete before chaos engineering and performance acceptance tests can execute meaningfully.

| Milestone | Week | Deliverable | Exit Criteria |
|-----------|------|-------------|---------------|
| M1: Foundation Complete | 4 | Core libraries, CI/CD, protocols | All 180 tasks pass; build succeeds; integration tests green |
| M2: Cluster Bootstrap | 12 | Node discovery, mesh, gateway | 5-node cluster forms; join/leave/fail handled; partition recovery verified |
| M3: Scheduling Ready | 18 | Omega scheduler, GPU engine | 1,000 jobs/sec; sub-10ms p99; GPU scheduling works |
| M4: Session Manager Ready | 26 | Distributed sessions, migration | Full session lifecycle; transparent migration; distributed panes |
| M5: Build Service Ready | 30 | RBE, AOSP integration | AOSP build distributes across 3+ nodes |
| M6: LLM Brain Active | 36 | Advisory system, learning | Advisories generated; auto-approval working; constitution enforced |
| M7: Security Hardened | 40 | Zero Trust, audit complete | Pen test passed; no critical findings; all P0 remediated |
| M8: QA Complete | 46 | All acceptance tests passed | 64-node, 1,000 session, 99.9% availability validated |
| M9: v1.0.0 Release | 50 | General availability | RC tested; docs complete; artifacts published |

## 6.12 Resource Requirements

The implementation requires coordinated investment across three resource dimensions: engineering personnel, hardware infrastructure, and operational budget.

**Engineering Team (12-15 FTE)**

| Role | Count | Primary Phases | Key Skills |
|------|-------|----------------|------------|
| Distributed Systems Engineer (Senior) | 2 | 0, 1, 2 | Go, etcd, consensus, networking |
| Backend Engineer (Mid-Senior) | 3 | 0, 1, 2, 3 | Go, gRPC, databases, Kubernetes |
| Systems Engineer (C/C++ & Zig) | 2 | 0, 2 | C, Zig, CUDA, GPU drivers, eBPF |
| Session/UX Engineer | 1 | 3 | Go, terminal emulators, WebSocket |
| DevOps/SRE Engineer | 1 | 0, 1, 8 | Kubernetes, Helm, CI/CD, monitoring |
| Security Engineer | 1 | 1, 6 | SPIFFE/SPIRE, WireGuard, OPA, Vault |
| ML Engineer | 1 | 2, 5 | Python, LSTM, RL, LLM integration |
| QA Engineer | 1 | 7 | TLA+, chaos engineering, test automation |
| Technical Writer | 1 | 8 | Developer documentation, runbooks |
| Engineering Manager | 1 | All | Project planning, stakeholder communication |

The team scales with project phases. Phase 0 requires 6-8 engineers focused on foundation work. Phase 1-3 represent peak staffing at 15 FTE as distributed systems, backend, systems, and session engineers work in parallel. Phases 4-6 scale back to 10-12 FTE. Phase 7-8 require the QA engineer and technical writer at full capacity alongside the core team performing remediation.

**Hardware Infrastructure**

| Environment | Node Count | GPU | Purpose |
|-------------|-----------|-----|---------|
| Development (per engineer) | 1-2 | 1x consumer GPU | Local development and unit testing |
| Integration Testing | 5-10 | Mixed (NVIDIA, AMD, Intel) | Continuous integration, integration tests |
| Staging | 10-20 | 2-4x datacenter GPUs | Performance testing, chaos engineering |
| Production Acceptance | 64+ | 8-16x datacenter GPUs | Final acceptance: 1,000 concurrent sessions |

The staging and production acceptance environments require heterogeneous hardware configurations to validate cross-vendor GPU scheduling, mixed CPU architectures (x86_64 and ARM64), and various storage types (NVMe, SSD, HDD). Network testing requires the ability to simulate partitions, latency, and packet loss.

**Budget Estimate (50 weeks)**

| Category | Estimated Cost | Notes |
|----------|---------------|-------|
| Personnel (12-15 FTE) | $1.8M - $2.5M | Senior engineers at $150-200K, mid-level at $100-150K |
| Cloud Infrastructure | $80K - $120K | AWS/GCP for CI runners, test clusters, staging |
| GPU Hardware | $100K - $200K | Mix of consumer and datacenter GPUs for testing |
| LLM API Costs | $20K - $40K | Kimi, DeepSeek, Claude API usage during development |
| Security Audit | $50K - $80K | Third-party penetration testing and code review |
| Development Tools | $10K - $15K | JetBrains licenses, GitHub Enterprise, monitoring SaaS |
| **Total** | **$2.06M - $2.96M** | |

## 6.13 Success Criteria

Each phase defines measurable Key Performance Indicators (KPIs) that must be achieved before the phase is considered complete. These criteria serve as objective gates controlling progression to subsequent phases.

| Phase | KPI | Target | Measurement Method |
|-------|-----|--------|---------------------|
| **0: Foundation** | Build success rate | >99% | CI pipeline pass rate over 2-week window |
| | Test coverage | >80% line coverage | Codecov report |
| | Library API stability | Zero breaking changes | API compatibility checks |
| | Documentation completeness | 100% of libraries documented | docs/ coverage audit |
| **1: Core Infrastructure** | Cluster formation time | <30 seconds for 5-node cluster | Automated benchmark |
| | Gossip false positive rate | <1% | Failure injection test |
| | Mesh throughput | >1 Gbps between any two nodes | iperf3 benchmark |
| | Gateway request latency | p99 <10ms | Load test (10K req/sec) |
| | Security attestation success | 100% | All join attempts pass attestation |
| | Partition recovery time | <5 seconds | Network partition test |
| **2: Resource Management** | Scheduling throughput | 1,000 decisions/sec | Scheduler benchmark |
| | Scheduling latency | p99 <10ms | Scheduler benchmark |
| | GPU detection coverage | 100% of attached GPUs | GPU enumeration test |
| | Health prediction accuracy | >85% | Historical failure correlation |
| | Anomaly detection false positive | <5% | Labeled anomaly dataset |
| | Self-healing response time | <30 seconds | Simulated failure test |
| **3: Session Manager** | Session creation time | <2 seconds | End-to-end benchmark |
| | I/O latency | <50ms character echo | Terminal input benchmark |
| | Migration downtime | <5 seconds visible | Migration test with active client |
| | Migration success rate | >99% | 100 migration trials |
| | CLI command coverage | 100% of commands tested | Integration test suite |
| | Concurrent sessions | 100 per node | Load test |
| **4: Build Service** | RBE action cache hit rate | >70% | Repeated build analysis |
| | AOSP build speedup | 3x with 3 nodes vs. 1 node | Wall-clock comparison |
| | Worker utilization | >80% average | Metrics dashboard |
| **5: LLM Brain** | Advisory generation latency | p99 <5 seconds | Load test |
| | Advisory accuracy | >90% approved by operators | Human review tracking |
| | Auto-approval rate | >60% of low-risk advisories | Pipeline metrics |
| | Constitutional violation catch | 100% | Adversarial test suite |
| | RL convergence | Improved approval rate over time | Historical trend |
| **6: Security Hardening** | Penetration test findings | Zero critical, zero high | Third-party report |
| | Certificate rotation downtime | 0 seconds | Rotation event monitoring |
| | SLSA compliance level | Level 3 | SLSA attestation verification |
| | SBOM generation | 100% of artifacts | Build pipeline audit |
| **7: QA & Testing** | Test coverage | >85% line, >70% mutation | Coverage reports |
| | Mutation score | >70% | go-mutesting report |
| | Chaos survival rate | >99.9% over 48 hours | Chaos experiment report |
| | Formal verification | All safety properties proven | TLA+ model checker output |
| | Performance acceptance | 64 nodes, 1,000 sessions, 99.9% availability | Load test report |
| **8: Polish & Release** | Install success rate | >95% on supported platforms | Analytics from install script |
| | Documentation completeness | 100% of features documented | Feature-to-docs traceability |
| | RC bug count | Zero P0, <5 P1 | Bug tracker report |
| | Post-release incident count | Zero critical in 48 hours | Incident tracking |

The gating criteria for release to production require that all P0 (critical path) tasks in every phase achieve their KPI targets, all P1 (essential for MVP) tasks are complete with no outstanding blockers, and the 48-hour chaos engineering run completes with a failure rate below 0.1%. Any phase failing to meet its KPIs triggers a remediation sprint before progression to the next phase, with escalation to the engineering manager for resource reallocation decisions.
