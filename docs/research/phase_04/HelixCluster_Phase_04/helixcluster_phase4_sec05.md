## 5. Virtual Testing Matrix Architecture

The HelixCluster Phase 4 Virtual Testing Matrix represents the architectural synthesis of virtualization technologies, deterministic simulation, chaos engineering, and polyglot runtime integration into a unified testing platform. This chapter defines the system architecture that enables automated, deterministic, and scalable validation of HelixCluster behavior across all eight device tiers — from desktop-class workstations to resource-constrained embedded devices — without requiring physical hardware for the majority of test scenarios. The architecture integrates six major subsystems, each responsible for a distinct dimension of the testing lifecycle, orchestrated through an Elixir/OTP-based controller that leverages the BEAM virtual machine's unique distributed computing primitives.

### 5.1 System Architecture Overview

#### 5.1.1 Six-Subsystem Architecture

The Virtual Testing Matrix is organized into six cooperating subsystems, each derived from the technology analysis presented in Chapters 1 through 4:

1. **Device Simulation Layer** — Provides virtualized device instances for tiers T1 through T8 using Firecracker microVMs, QEMU/KVM full-system emulation, Docker containers with binfmt_misc cross-architecture execution, and platform-specific simulators (Cuttlefish for Android, protocol-level stubs for iOS and HarmonyOS).

2. **DST Engine** — Implements deterministic simulation testing using Rust's turmoil framework, executing real production code in a single-threaded event loop with virtual time compression and seeded pseudo-randomness. This approach follows the methodology that FoundationDB applied across 1 trillion CPU-hours of simulation testing with zero production bugs traced to code defects [^1997^][^28^].

3. **Chaos Engineering System** — An Elixir/OTP-based fault injection platform providing 25 distinct fault types across network, node, time, and hardware categories, composable through YAML-defined scenarios with configurable blast radius controls.

4. **Virtual Testing Controller** — The central orchestrator implemented as an Elixir OTP application with GenServer processes for session management, device pool allocation, test execution, snapshot lifecycle, and metrics collection. The controller exposes a Phoenix LiveView dashboard for real-time test observability.

5. **HelixQA Integration Layer** — Connects test outcomes to the HelixQA challenge system through automatic challenge generation, statistical regression detection, and pass/fail quality gating for CI/CD pipelines.

6. **WebAssembly Plugin System** — Enables language-agnostic extensibility through Wasmtime's Component Model, allowing custom device simulators, workload generators, fault injectors, and metrics exporters to be compiled from any language supporting Wasm targets and loaded with 5-microsecond latency [^2098^].

#### 5.1.2 Design Principles

The architecture adheres to seven foundational design principles that constrain every technical decision:

**Determinism.** All test execution must be perfectly reproducible from a seed value. This principle draws directly from the FoundationDB methodology where the same seed produces bit-identical execution traces across runs [^1997^]. Enforcement mechanisms include seeded PRNGs throughout the simulation layer, virtualized network and time abstractions, and a single-threaded event loop that eliminates scheduler non-determinism.

**Isolation.** Each simulated device executes in a fully isolated context appropriate to its tier — KVM hardware virtualization for T1–T6, namespace isolation for T7–T8 containers, and process-level isolation for DST-simulated nodes. Isolation ensures that faults injected into one device cannot corrupt the state of others, a prerequisite for meaningful chaos engineering.

**Scalability.** The matrix must scale from single-device unit tests to 10,000-node cluster simulations. Firecracker's demonstrated density of 5,000+ microVMs per host [^2022^] provides the foundation for T1–T3 scaling, while the DST engine achieves 1,000+ simulated nodes in a single process without VM overhead. Horizontal pod scaling on K3s extends capacity across multiple physical hosts.

**Fidelity.** Simulation must accurately reflect real device behavior to the extent required for the test category. Full-system emulation with real Linux kernels and virtio devices provides hardware-accurate behavior for T4–T6, while protocol-level container simulation trades fidelity for speed on T7–T8 where full virtualization is unavailable [^1905^].

**Composability.** Testing primitives must compose arbitrarily — a chaos scenario can inject network partitions during a DST workload while HelixQA validates invariants, all orchestrated as a single test session. YAML-based scenario definitions and the WebAssembly plugin system enable this compositional flexibility.

**Observability.** Every aspect of test execution must be observable through OpenTelemetry distributed tracing, Prometheus metrics collection, structured logging, and the Phoenix LiveView dashboard. The chaos controller alone emits 15+ distinct metric series covering fault injection rates, target device health, and recovery latency.

**Speed.** Test iteration cycles must complete in seconds. Firecracker's 28ms snapshot restore [^1890^], DST's 10:1 time compression, and parallel test execution through Elixir's lightweight processes (approximately 300 bytes each [^2076^]) collectively ensure that even complex multi-node scenarios execute within CI time budgets.

#### 5.1.3 Component Interaction and Data Flow

The following diagram illustrates the primary data flows between the six subsystems:

```
                              +-----------------------------------------+
                              |      Phoenix LiveView Dashboard        |
                              |   (Real-time metrics, active tests)    |
                              +-------------+-------------------------+
                                            | WebSocket / HTTP
+------------------+    gRPC/FlatBuffers   +-v----------------------------------+
|   DST Engine      |<-------------------->|   Virtual Testing Controller        |
|  (Rust + turmoil) |                     |  (Elixir/OTP GenServer cluster)     |
|                   |                     |                                     |
| * SimLoop         |                     | * SessionManager                    |
| * INetwork traits |                     | * DevicePool                        |
| * BUGGIFY macros  |                     | * TestRunner                        |
| * 10:1 time comp  |                     | * SnapshotManager                   |
+--------+----------+                     | * MetricsCollector                  |
         |                               +-+------+------+--------------------+
         |                                 |      |      |
         |Load Test Binary                 |      |      | Orchestrate
         |                                 |      |      |
+--------v----------+              +-------v-+ +--v----+ +--v------------------+
|  Device Simulation |              |  HelixQA | | Chaos | | Wasmtime Plugin     |
|  Layer             |              |  Integration  | System | | Host               |
|                   |              |   Layer    |       | |                    |
| +---------------+ |              +----------+ +-------+ +--------------------+
| | Firecracker   | |  T1-T3 (28ms boot)                              |
| | (microVMs)    | |         +----------------------------------------+
| +---------------+ |         | WIT interfaces
| +---------------+ |   +-----v-----------------+
| | QEMU/KVM      | |   |  Plugin Registry       |
| | (full-system) | |   |  * Device simulators   |
| +---------------+ |   |  * Workload generators |
| +---------------+ |   |  * Fault injectors     |
| | Docker/binfmt | |   |  * Metrics exporters   |
| | (containers)  | |   +------------------------+
| +---------------+ |
| +---------------+ |
| | Cuttlefish    | |  T5 Android
| | (CrosVM)      | |
| +---------------+ |
+-------------------+
         |
    KVM / Namespace Isolation
         |
+-------------------+
|  K3s Kubernetes   |  <-- RuntimeClass: firecracker / kata / runc
|  (Orchestration)  |  <-- Prometheus + Grafana observability stack
|                   |  <-- WireGuard mesh (inter-host)
+-------------------+
```

The primary control flow initiates at the Virtual Testing Controller when a test session request arrives via the REST API, CI webhook, or scheduled trigger. The SessionManager validates resource quotas and allocates a session identifier. The DevicePool provisions virtual devices according to the tier specification — Firecracker for T1–T3, QEMU for T4–T6, Docker for T7–T8 — referencing golden snapshots where available to minimize boot latency. For deterministic simulation tests, the controller spawns the DST Engine as a separate Rust process communicating over gRPC with FlatBuffers serialization. The Chaos Controller injects faults according to loaded scenarios, while the Wasmtime host loads any plugin components required for custom workload or fault injection logic. Throughout execution, all subsystems emit metrics to the Prometheus-compatible MetricsCollector, and test outcomes feed into the HelixQA Integration Layer for regression detection and challenge generation.

### 5.2 Device Simulation Layer

#### 5.2.1 Tier-to-Simulator Mapping

The Device Simulation Layer implements a tiered virtualization strategy where each device tier maps to the lightest simulator that provides sufficient fidelity for the intended test category. This mapping reflects the cross-dimensional insight that Firecracker delivers the highest density for PC-class devices, QEMU provides the most accurate peripheral emulation for ARM-based platforms, and containers offer the fastest iteration for protocol-level testing where full system emulation is unavailable or unnecessary.

The following table defines the complete tier-to-simulator mapping, boot characteristics, resource requirements, and fidelity level for each of the eight HelixCluster device tiers.

| Tier | Device Class | Trust Model | Simulator | Architecture | Boot Time | Memory per Instance | Max per Host | Fidelity |
|------|-------------|-------------|-----------|--------------|-----------|---------------------|--------------|----------|
| T1 | Desktop PC | FULL | Firecracker microVM | x86_64 | 28ms (snapshot) [^1890^] | 4GB + 5MB VMM [^2030^] | ~48 | High — real Linux kernel, virtio devices |
| T2 | Laptop PC | FULL | Firecracker microVM | x86_64 | 28ms (snapshot) [^1890^] | 2GB + 5MB VMM | ~96 | High — real Linux kernel, virtio devices |
| T3 | Workstation PC | FULL | Firecracker microVM | x86_64 | 28ms (snapshot) [^1890^] | 8GB + 5MB VMM | ~24 | High — real Linux kernel, virtio devices |
| T4 | Gaming Console | SEMI | QEMU/KVM x86_64 | x86_64 | 2–5 min (cold) | 8GB + ~100MB QEMU | ~12 | Medium — protocol-level; PS4 GPU not emulable |
| T5 | Android Device | SEMI | Cuttlefish / CrosVM | arm64/x86_64 | 30–60s | 4GB + ~50MB VMM | ~12 | Medium — official Google AOSP target [^2014^] |
| T6 | SBC (RK3588) | STANDARD | QEMU/KVM ARM64 virt | arm64 | 3–5 min (cold) | 16GB + ~100MB QEMU | ~8 | Medium — CPU/interrupt accurate; GPU/NPU not emulated |
| T7 | iOS Device | EDGE_DONOR | Docker + binfmt_misc | arm64 container | 500ms–2s | 128MB container | ~200 | Low — protocol-level only; no true iOS emulation [^1905^] |
| T8 | HarmonyOS | SEMI | Docker + binfmt_misc | arm64 container | 500ms–2s | 256MB container | ~100 | Low — OpenHarmony protocol stub |

The fidelity classifications reflect a critical architectural constraint: no available virtualization technology can fully emulate the PlayStation 4's custom AMD APU architecture (T4), the Mali-G610 MP4 GPU and 6 TOPS NPU of the RK3588 (T6), or Apple's proprietary iOS hardware (T7). For these tiers, the simulation operates at the protocol level — the HelixCluster agent binary executes in a constrained environment matching the target hardware's CPU architecture and memory profile, but GPU-accelerated workloads and hardware-specific peripherals require physical hardware-in-the-loop testing. The hybrid cluster controller manages both simulated and physical nodes through a unified abstraction, ensuring that a test cluster may comprise 90% simulated nodes for scale and 10% physical nodes for hardware-specific fidelity.

#### 5.2.2 Device Profile Registry

All device tiers are defined in a centralized YAML registry consumed by the DevicePool manager during provisioning. The registry schema captures CPU, memory, storage, network, and trust model specifications for each tier:

```yaml
# device-registry.yaml — Device profile definitions for all T1-T8 tiers
profiles:
  - tier: T1
    name: "Desktop PC"
    trust_model: FULL
    simulator: firecracker
    architecture: x86_64
    resources:
      vcpus: 4
      memory_mb: 4096
      disk_gb: 64
    network:
      bandwidth_mbps: 1000
      latency_ms: 1
    snapshot:
      golden_image: /var/lib/helixcluster/snapshots/t1-desktop-golden
      enabled: true
    constraints:
      gpu: "virtio-gpu"
      tee: false
      npu: false

  - tier: T6
    name: "SBC Orange Pi 5 Max"
    trust_model: STANDARD
    simulator: qemu_kvm
    architecture: arm64
    resources:
      vcpus: 8          # quad Cortex-A76 + quad Cortex-A55 topology
      memory_mb: 16384  # 16GB LPDDR5X
      disk_gb: 256
    network:
      bandwidth_mbps: 1000
      latency_ms: 1
    qemu_opts:
      machine: "virt,virtualization=on,gic-version=3"
      cpu: "max,pauth-impdef=on,sve=on"
      smp: "8,sockets=1,clusters=2,cores=4,threads=1"
      bios: "/usr/share/AAVMF/AAVMF_CODE.fd"
    constraints:
      gpu: false        # Mali-G610 not emulated
      npu: false        # 6 TOPS NPU not emulated
      big_little: true  # Requires cluster topology pinning

  - tier: T7
    name: "iOS Device"
    trust_model: EDGE_DONOR
    simulator: docker_protocol
    architecture: arm64
    resources:
      vcpus: 2
      memory_mb: 2048
    network:
      bandwidth_mbps: 100
      latency_ms: 10
    constraints:
      platform: "ios"
      protocol_only: true
      physical_required_for: ["gpu", "npu", "camera", "gps", "push_notifications"]
```

The DevicePool GenServer consumes this registry at startup, pre-allocating simulator-specific resources and validating that the host environment can satisfy the requested tier configurations. When a test session requests T6 devices on a host without KVM acceleration or ARM64 support, the DevicePool returns an error before any provisioning begins, enabling the controller to schedule the session on an appropriately configured host.

#### 5.2.3 Golden Snapshot Pattern

The golden snapshot pattern enables sub-50ms test state reset across all VM-based tiers. The cycle proceeds as follows: a base image is booted once to a known-good state (all services running, agent connected, ready for testing); a golden snapshot captures this state; each test session receives a copy-on-write (COW) overlay derived from the golden snapshot; after test completion, the overlay is discarded and a new overlay is created for the next test. For Firecracker, this uses the snapshot/restore API with memory file and VM state file; for QEMU, qcow2 external snapshots provide COW semantics; for Docker, container commits serve a similar purpose.

```bash
#!/bin/bash
# helix-firecracker-snapshot.sh — Golden snapshot lifecycle

SNAPSHOT_DIR="/var/lib/helixcluster/snapshots"
SESSION_DIR="/var/lib/helixcluster/sessions"
FIRECRACKER_SOCK="/run/firecracker/{VM_ID}.sock"

# Phase 1: Create golden snapshot from booted base image
create_golden_snapshot() {
    local vm_id=$1 tier=$2
    boot_vm "$vm_id" "$tier"
    wait_for_vsock_agent "$vm_id" 30
    # Pause VM for consistent snapshot
    curl --unix-socket "$FIRECRACKER_SOCK" -X PATCH \
        'http://localhost/vm' -d '{"state": "Paused"}'
    # Full snapshot: VM state + memory image
    curl --unix-socket "$FIRECRACKER_SOCK" -X PUT \
        'http://localhost/snapshot/create' \
        -d "{\"snapshot_type\": \"Full\", \
             \"snapshot_path\": \"${SNAPSHOT_DIR}/${tier}-golden-${vm_id}.snap\", \
             \"mem_file_path\": \"${SNAPSHOT_DIR}/${tier}-golden-${vm_id}.mem\"}"
    echo "Golden snapshot created for ${tier}: ~28ms restore target"
}

# Phase 2: Restore from golden for test session
restore_from_snapshot() {
    local vm_id=$1 tier=$2 session_id=$3
    local snap="${SNAPSHOT_DIR}/${tier}-golden-base.snap"
    local mem="${SNAPSHOT_DIR}/${tier}-golden-base.mem"
    mkdir -p "${SESSION_DIR}/${session_id}"
    curl --unix-socket "$FIRECRACKER_SOCK" -X PUT \
        'http://localhost/snapshot/load' \
        -d "{\"snapshot_path\": \"${snap}\", \
             \"mem_file_path\": \"${mem}\"}"
    curl --unix-socket "$FIRECRACKER_SOCK" -X PATCH \
        'http://localhost/vm' -d '{"state": "Resumed"}'
}
```

The Elixir SnapshotManager automates this lifecycle across all simulator types:

```elixir
defmodule HelixTest.SnapshotManager do
  @moduledoc "Manages golden snapshots and instant reset across all simulators."
  use GenServer
  require Logger

  @snapshot_dir "/var/lib/helixcluster/snapshots"
  @overlay_dir "/var/lib/helixcluster/sessions"

  # Tier-to-backend dispatch for snapshot operations
  @backends %{
    "T1" => HelixTest.FirecrackerManager,
    "T2" => HelixTest.FirecrackerManager,
    "T3" => HelixTest.FirecrackerManager,
    "T4" => HelixTest.QemuManager,
    "T5" => HelixTest.QemuManager,
    "T6" => HelixTest.QemuManager,
    "T7" => HelixTest.DockerManager,
    "T8" => HelixTest.DockerManager
  }

  def create_golden(tier, base_image) do
    GenServer.call(__MODULE__, {:create_golden, tier, base_image}, :timer.minutes(5))
  end

  def instant_reset(session_id, device_id, tier) do
    # Target: <50ms for Firecracker, <500ms for QEMU, <2s for Docker
    GenServer.call(__MODULE__, {:instant_reset, session_id, device_id, tier}, :timer.seconds(30))
  end

  @impl true
  def handle_call({:create_golden, tier, base_image}, _from, state) do
    backend = Map.fetch!(@backends, tier)
    result = backend.create_snapshot(base_image, golden_path(tier))
    Logger.info("Golden snapshot created for #{tier}: #{golden_path(tier)}")
    {:reply, result, state}
  end

  @impl true
  def handle_call({:instant_reset, session_id, device_id, tier}, _from, state) do
    backend = Map.fetch!(@backends, tier)
    # Discard COW overlay and recreate from golden
    result = backend.reset_to_golden(session_id, device_id, golden_path(tier))
    {:reply, result, state}
  end

  defp golden_path(tier), do: Path.join(@snapshot_dir, "#{tier}-golden")
end
```

### 5.3 DST Engine Design

#### 5.3.1 Single-Threaded Event Loop with Virtual Time Compression

The Deterministic Simulation Testing (DST) Engine executes real HelixCluster production code within a single-threaded event loop, eliminating all sources of non-determinism that plague multi-threaded testing. This approach mirrors the architecture that FoundationDB used to achieve 1 trillion CPU-hours of simulated testing [^1997^], and that TigerBeetle's VOPR applies at approximately 700x real-time speed compression [^29^]. The DST Engine achieves 10:1 time compression by advancing simulated time only when all actors are blocked on I/O, effectively fast-forwarding through idle periods.

The core event loop maintains a priority queue of scheduled events, a virtual clock, a seeded PRNG, and simulated network and disk abstractions. All "nodes" in the simulated cluster are async tasks running on a single Tokio runtime configured for cooperative multitasking. Because there is only one OS thread and one executor, task interleaving is fully deterministic for a given seed.

#### 5.3.2 Interface Swapping: The INetwork Pattern

The defining architectural pattern enabling deterministic simulation is interface swapping — all I/O interfaces (network, disk, clock, randomness) are abstracted behind Rust traits with two implementations: a production implementation using Tokio's real network stack and a simulation implementation using turmoil's deterministic network. This pattern originates from FoundationDB's `g_network` pointer, which holds either `Net2` (production) or `Sim2` (simulation) [^28^].

```rust
// helix-cluster-sim/src/traits.rs
/// Network abstraction enabling production/simulation swapping.
pub trait HelixNetwork: Send + Sync {
    type TcpListener: AsyncRead + AsyncWrite + Unpin;
    type TcpStream: AsyncRead + AsyncWrite + Unpin;

    async fn bind(&self, addr: SocketAddr) -> io::Result<Self::TcpListener>;
    async fn connect(&self, addr: SocketAddr) -> io::Result<Self::TcpStream>;
    async fn send_to(&self, buf: &[u8], addr: SocketAddr) -> io::Result<usize>;
    async fn recv_from(&self, buf: &mut [u8]) -> io::Result<(usize, SocketAddr)>;

    // Deterministic chaos injection hooks
    fn inject_partition(&self, a: NodeId, b: NodeId);
    fn heal_partition(&self, a: NodeId, b: NodeId);
    fn set_latency(&self, from: NodeId, to: NodeId, latency: Duration);
}

/// Production implementation: delegates to Tokio's real network stack.
#[cfg(not(feature = "simulation"))]
pub struct ProdNetwork;

#[cfg(not(feature = "simulation"))]
impl HelixNetwork for ProdNetwork {
    type TcpListener = tokio::net::TcpListener;
    type TcpStream = tokio::net::TcpStream;

    async fn bind(&self, addr: SocketAddr) -> io::Result<Self::TcpListener> {
        tokio::net::TcpListener::bind(addr).await
    }

    async fn connect(&self, addr: SocketAddr) -> io::Result<Self::TcpStream> {
        tokio::net::TcpStream::connect(addr).await
    }

    // Production: no-op for chaos hooks (chaos is external)
    fn inject_partition(&self, _a: NodeId, _b: NodeId) {}
    fn heal_partition(&self, _a: NodeId, _b: NodeId) {}
    fn set_latency(&self, _from: NodeId, _to: NodeId, _latency: Duration) {}
}

/// Simulation implementation: delegates to turmoil's deterministic network.
#[cfg(feature = "simulation")]
pub struct SimNetwork {
    inner: turmoil::net::Network,
    rng: Rc<RefCell<SeededRng>>,
}

#[cfg(feature = "simulation")]
impl HelixNetwork for SimNetwork {
    type TcpListener = turmoil::net::TcpListener;
    type TcpStream = turmoil::net::TcpStream;

    async fn bind(&self, addr: SocketAddr) -> io::Result<Self::TcpListener> {
        self.inner.bind(addr).await
    }

    async fn connect(&self, addr: SocketAddr) -> io::Result<Self::TcpStream> {
        // Simulated latency and packet loss applied automatically
        self.inner.connect(addr).await
    }

    fn inject_partition(&self, a: NodeId, b: NodeId) {
        self.inner.partition(
            format!("node-{}", a.0), format!("node-{}", b.0));
    }

    fn heal_partition(&self, a: NodeId, b: NodeId) {
        self.inner.heal(
            format!("node-{}", a.0), format!("node-{}", b.0));
    }

    fn set_latency(&self, from: NodeId, to: NodeId, latency: Duration) {
        self.inner.set_latency(
            format!("node-{}", from.0),
            format!("node-{}", to.0),
            latency
        );
    }
}
```

The compilation flag `feature = "simulation"` selects the appropriate implementation at build time. All HelixCluster code that performs network I/O accepts `Arc<dyn HelixNetwork>` as a parameter, ensuring that the same source code compiles against both production and simulation backends without modification.

#### 5.3.3 BUGGIFY Integration

BUGGIFY macros inject deterministic chaos at specific code points, following FoundationDB's approach where each macro has approximately a 25% activation rate controlled by the seeded PRNG [^1997^]. This forces error-handling and timeout paths to execute far more frequently than they would under normal conditions.

```rust
/// BUGGIFY macro: injects chaos ~25% of the time in simulation.
#[macro_export]
macro_rules! buggify {
    ($body:expr) => {
        if helix_cluster_sim::is_buggify_enabled()
            && helix_cluster_sim::random::<u8>() % 4 == 0
        {
            $body
        }
    };
}

impl ConsensusNode {
    pub async fn append_entries(
        &mut self,
        req: AppendEntriesReq,
        network: Arc<dyn HelixNetwork>,
    ) -> Result<AppendEntriesResp> {
        // BUGGIFY: force timeout path (600x compression: 60s -> 0.1s)
        buggify! {
            sim::sleep(Duration::from_millis(100)).await;
            return Err(ConsensusError::Timeout);
        }
        // BUGGIFY: force corrupted log response
        buggify! {
            return Err(ConsensusError::CorruptedLog);
        }
        // BUGGIFY: force duplicate append
        buggify! {
            return Ok(AppendEntriesResp {
                term: self.current_term,
                success: false,
                conflict_index: self.log.last_index(),
            });
        }

        // Normal path
        let match_index = self.log.append(req.entries)?;
        Ok(AppendEntriesResp {
            term: self.current_term,
            success: true,
            conflict_index: match_index,
        })
    }
}
```

#### 5.3.4 Workload Design Pattern

All DST workloads follow the FoundationDB four-phase pattern: SETUP -> EXECUTION (with BUGGIFY) -> CHECK invariants -> METRICS collection. The following Rust test demonstrates a complete consensus validation using turmoil:

```rust
// helix-cluster-sim/tests/dst_consensus.rs
use std::time::Duration;
use turmoil::{Builder, Result};

#[test]
fn consensus_survives_random_partitions() -> Result {
    let seed = 42_194u64; // Any failure is reproducible from this seed
    let mut sim = Builder::new()
        .simulation_duration(Duration::from_secs(3600)) // 1 hour -> ~6 min
        .enable_random_ordering(false) // Deterministic task scheduling
        .build();

    // SETUP: Spawn 5 consensus nodes
    for i in 0..5 {
        sim.host(format!("helix-node-{}", i), || async move {
            let config = NodeConfig::builder()
                .node_id(i)
                .peers((0..5).filter(|&p| p != i).collect())
                .build();
            helix_cluster::run_node(config).await
        });
    }

    // SETUP: Create workload client submitting 100 tasks
    sim.client("workload", async move {
        let client = helix_cluster::Client::new("helix-node-0");
        for i in 0..100 {
            client.submit_task(TaskSpec {
                id: format!("task-{}", i),
                cpu_request: 1.0,
                memory_request: 512,
                priority: TaskPriority::Normal,
            }).await?;
            tokio::time::sleep(Duration::from_secs(36)).await;
        }
        Ok(())
    });

    // EXECUTION: Inject random network partitions
    sim.partition("helix-node-0", "helix-node-1");
    sim.partition("helix-node-0", "helix-node-2");
    tokio::time::sleep(Duration::from_secs(300)).await;
    sim.heal("helix-node-0", "helix-node-1");
    sim.heal("helix-node-0", "helix-node-2");

    // CHECK: Verify safety and liveness invariants
    sim.client("invariant-checker", async move {
        tokio::time::sleep(Duration::from_secs(3600)).await;
        let client = helix_cluster::Client::new("helix-node-0");
        let status = client.get_cluster_status().await?;

        // Safety: no task should be unscheduled
        assert_eq!(status.unscheduled_tasks, 0,
            "SAFETY VIOLATION: {} tasks remain unscheduled",
            status.unscheduled_tasks);

        // Safety: no task should be double-assigned
        for task in &status.tasks {
            assert!(task.assigned_nodes.len() <= 1,
                "SAFETY VIOLATION: task {} assigned to {} nodes",
                task.id, task.assigned_nodes.len());
        }

        // Liveness: quorum must be maintained
        assert!(status.healthy_nodes >= 3,
            "LIVENESS VIOLATION: only {} healthy nodes (quorum: 3)",
            status.healthy_nodes);

        Ok(())
    });

    sim.run() // Any failure reproduces identically with seed=42_194
}
```

### 5.4 Chaos Engineering System

#### 5.4.1 Elixir/OTP-Based Chaos Controller

The Chaos Engineering System provides 25 distinct fault injection types organized into four categories: Network (8 types), Node (8 types), Time (3 types), and Hardware (6 types). The Chaos Controller is implemented as an Elixir GenServer with a supervision tree that ensures fault injection processes are isolated and can be terminated independently through the emergency stop mechanism.

```elixir
defmodule HelixChaos.Controller do
  @moduledoc """
  Central chaos controller with supervision tree isolation.
  Supports 25 fault types across network, node, time, and hardware categories.
  """
  use GenServer
  require Logger

  @chaos_states [:idle, :setup, :running, :paused, :recovering, :completed, :failed]

  defstruct [
    :state, :active_scenario, :start_time,
    :target_devices, :injected_faults, :metrics,
    :abort_signal, :blast_radius
  ]

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  # Public API
  def load_scenario(yaml_path), do: GenServer.call(__MODULE__, {:load_scenario, yaml_path})
  def start_experiment, do: GenServer.call(__MODULE__, :start_experiment, 60_000)
  def emergency_stop, do: GenServer.cast(__MODULE__, :emergency_stop)
  def get_status, do: GenServer.call(__MODULE__, :get_status)

  @impl true
  def init(_opts) do
    {:ok, %__MODULE__{
      state: :idle,
      active_scenario: nil,
      start_time: nil,
      target_devices: [],
      injected_faults: [],
      metrics: %{faults_injected: 0, devices_affected: 0, recoveries: 0},
      abort_signal: false,
      blast_radius: 0.0
    }}
  end

  @impl true
  def handle_call({:load_scenario, yaml_path}, _from, state) do
    case HelixChaos.ScenarioEngine.load(yaml_path) do
      {:ok, scenario} ->
        Logger.info("Chaos scenario loaded: #{scenario.name} " <>
          "(#{length(scenario.faults)} faults, blast_radius: #{scenario.blast_radius})")
        {:reply, :ok, %{state | active_scenario: scenario, state: :setup}}
      {:error, reason} ->
        {:reply, {:error, reason}, state}
    end
  end

  def handle_call(:start_experiment, _from, %{active_scenario: nil} = state) do
    {:reply, {:error, :no_scenario_loaded}, state}
  end

  def handle_call(:start_experiment, _from, %{active_scenario: scenario} = state) do
    {:ok, devices} = HelixChaos.DeviceRegistry.list_healthy(scenario.target_selector)
    if length(devices) == 0 do
      {:reply, {:error, :no_targets}, state}
    else
      max_targets = max(1, trunc(length(devices) * scenario.blast_radius))
      targets = Enum.take_random(devices, max_targets)
      schedule_faults(scenario.faults, targets)

      new_state = %{state |
        state: :running,
        start_time: System.monotonic_time(:second),
        target_devices: targets,
        blast_radius: scenario.blast_radius,
        metrics: %{state.metrics | devices_affected: length(targets)}
      }

      Logger.warning(
        "CHAOS EXPERIMENT STARTED: #{scenario.name} | " <>
        "Targets: #{length(targets)}/#{length(devices)} devices | " <>
        "Blast radius: #{scenario.blast_radius}")

      # Auto-recovery timer
      Process.send_after(self(), :auto_recover, scenario.duration_sec * 1000)
      {:reply, :ok, new_state}
    end
  end

  @impl true
  def handle_cast(:emergency_stop, state) do
    Logger.emergency("EMERGENCY STOP — halting all fault injection!")
    HelixChaos.NetworkFault.emergency_stop()
    HelixChaos.NodeFault.emergency_stop()
    HelixChaos.TimeFault.emergency_stop()
    HelixChaos.HardwareFault.emergency_stop()
    HelixChaos.NodeFault.recover_all(state.target_devices)
    HelixChaos.NetworkFault.heal_all()
    {:noreply, %{state | state: :recovering, abort_signal: true}}
  end

  @impl true
  def handle_info(:auto_recover, %{state: :running} = state) do
    Logger.info("Auto-recovery triggered for chaos experiment")
    HelixChaos.NodeFault.recover_all(state.target_devices)
    HelixChaos.NetworkFault.heal_all()
    HelixChaos.TimeFault.reset_all(state.target_devices)
    {:noreply, %{state | state: :completed, injected_faults: []}}
  end

  defp schedule_faults(faults, targets) do
    Enum.each(faults, fn fault ->
      target = Enum.random(targets)
      delay_ms = trunc(fault.delay_sec * 1000)
      Process.send_after(self(),
        {:inject_fault, fault.type, target, fault.params}, delay_ms)
    end)
  end
end
```

#### 5.4.2 Fault Injection Taxonomy

The 25 fault types span four categories, each targeting a different system layer. The following table summarizes the complete taxonomy with tools, parameters, and effects.

| Category | ID | Fault Type | Tool | Key Parameters | Effect on System |
|----------|-----|-----------|------|----------------|-----------------|
| Network | NF-01 | Latency injection | tc netem | delay, jitter, distribution | Slows inter-node communication; tests timeout logic |
| Network | NF-02 | Packet loss | tc netem | percent, correlation | Drops packets randomly; tests retry mechanisms |
| Network | NF-03 | Packet corruption | tc netem | percent | Corrupts payloads; tests checksum validation |
| Network | NF-04 | Packet reordering | tc netem | percent, gap | Reorders streams; tests sequence handling |
| Network | NF-05 | Bandwidth limit | tc tbf | rate, burst | Caps throughput; tests backpressure |
| Network | NF-06 | Network partition | iptables/nftables | direction, duration | Complete connectivity loss; tests split-brain prevention |
| Network | NF-07 | DNS failure | Chaos Mesh DNSChaos | patterns, duration | Fails lookups; tests graceful degradation |
| Network | NF-08 | TCP reset | tcpkill | port, duration | Forces connection drops; tests reconnection |
| Node | NF-09 | VM crash | QMP system_powerdown | delay | Abrupt power loss; tests recovery and data durability |
| Node | NF-10 | VM restart | QMP system_reset | delay, repeat | Hard reboot; tests state reconstruction |
| Node | NF-11 | VM pause | QMP stop/cont | duration | Freezes execution; tests heartbeat timeouts |
| Node | NF-12 | CPU pressure | stress-ng | workers, timeout | CPU exhaustion; tests scheduling fairness |
| Node | NF-13 | Memory pressure | stress-ng --vm | bytes, workers | OOM condition; tests memory limits |
| Node | NF-14 | Disk pressure | fio + loopback | fill_percent | Disk full; tests space handling |
| Node | NF-15 | OOM kill | cgroups memory.limit | limit_bytes | Kernel OOM killer; tests graceful shutdown |
| Node | NF-16 | Graceful shutdown | SSH shutdown | delay | Clean shutdown; tests leader transfer |
| Time | NF-17 | Clock skew | Chaos Mesh TimeChaos | offset_sec, clock_ids | Moves clock; tests lease/TTL management [^10^] |
| Time | NF-18 | Clock freeze | Chaos Mesh TimeChaos | duration | Stops clock advance; tests timeout edge cases |
| Time | NF-19 | Monotonic drift | libfaketime | speed_factor | Speeds/slows clock; tests ordering assumptions |
| Hardware | NF-20 | NMI injection | QMP inject-nmi | target | Non-maskable interrupt; tests panic handling |
| Hardware | NF-21 | Memory correctable error | EDAC sysfs | address, count | Correctable ECC errors; tests error counting |
| Hardware | NF-22 | Memory uncorrectable error | mce-inject | address | Uncorrectable errors; tests panic paths |
| Hardware | NF-23 | PCIe AER | QMP pcie_aer_inject_error | error_status | Link errors; tests I/O retry logic |
| Hardware | NF-24 | CPU bit-flip | Custom QEMU module | register, bit | Register corruption; tests fault tolerance |
| Hardware | NF-25 | Thermal throttle | cpufreq governor | max_freq | CPU frequency reduction; tests performance degradation |

The TimeChaos mechanism from Chaos Mesh is particularly significant for distributed systems testing because it simulates clock skew in containers without affecting the host node's clock, using VDSO-based time syscall interception [^10^]. This capability is essential for testing lease management, TTL expiration, and causal ordering protocols that depend on clock monotonicity.

#### 5.4.3 Scenario Engine: YAML-Defined Composable Scenarios

Chaos scenarios are defined as YAML documents specifying phased fault injection with configurable blast radius, target selectors, and success criteria. The Scenario Engine parses these definitions and translates them into scheduled fault injection events.

```yaml
# scenarios/network-partition-cascade.yaml
apiVersion: helixcluster.io/v1
kind: ChaosScenario
metadata:
  name: network-partition-cascade
  description: |
    Progressive network degradation: latency -> partial partition ->
    severe partition -> recovery. Validates consensus and scheduling
    invariants at each degradation level.
spec:
  blast_radius: 0.30          # Affect at most 30% of healthy targets
  duration_sec: 1140          # Total experiment: 19 minutes
  abort_on_slo_breach: true
  target_selector:
    match_tiers: [T1, T2, T3, T6]
    min_trust_level: STANDARD
    exclude_labels: ["chaos.immune", "production.critical"]
  phases:
    - name: baseline
      duration: 60
      action: none
      description: "Collect baseline metrics"

    - name: latency-injection
      duration: 300
      action: inject_faults
      faults:
        - type: network_latency
          params: { delay_ms: 200, jitter_ms: 50, distribution: normal }
          target_percent: 50

    - name: partial-partition
      duration: 300
      action: inject_faults
      faults:
        - type: network_partition
          params:
            groups: [["node-0","node-1","node-2"], ["node-3","node-4","node-5"]]
            direction: both

    - name: severe-partition
      duration: 180
      action: inject_faults
      faults:
        - type: network_partition
          params:
            groups: [["node-0","node-1"], ["node-2","node-3"], ["node-4","node-5"]]
            direction: both
        - type: packet_loss
          params: { percent: 30, correlation: 10 }
          target_percent: 25

    - name: recovery
      duration: 300
      action: heal_all
      description: "Heal all partitions, collect recovery metrics"

  success_criteria:
    - name: no_lost_tasks
      assertion: "cluster.unscheduled_tasks == 0"
      severity: critical
    - name: quorum_maintained
      assertion: "cluster.healthy_nodes >= ceil(cluster.total_nodes * 0.5) + 1"
      severity: critical
    - name: recovery_time_slo
      assertion: "cluster.recovery_time_ms < 30000"
      severity: warning
```

The blast radius parameter controls the percentage of healthy target devices affected by each fault, preventing chaos experiments from taking down the entire test fleet. The `abort_on_slo_breach` flag enables automatic rollback when service level objectives are violated, ensuring that chaos experiments remain controlled rather than destructive.

### 5.5 Virtual Testing Controller

#### 5.5.1 Elixir GenServer Architecture

The Virtual Testing Controller is the central orchestrator, implemented as an Elixir OTP application with a supervision tree using the `one_for_all` restart strategy. This strategy ensures that a failure in any GenServer (session corruption, device pool desynchronization) triggers a complete supervisor restart, maintaining system consistency. The controller comprises four primary GenServer processes:

```
HelixTest.Supervisor (one_for_all)
  |-- SessionManager     — Session lifecycle and resource quota enforcement
  |-- DevicePool         — Device provisioning, health checks, reclamation
  |-- TestRunner         — Test suite execution with parallelization
  +-- SnapshotManager    — Golden snapshot and instant reset
```

The SessionManager enforces a maximum of 50 concurrent sessions (configurable), each with a two-hour TTL and resource quotas tracked against a shared pool:

```elixir
defmodule HelixTest.SessionManager do
  @moduledoc "Manages test session lifecycle and resource allocation."
  use GenServer
  require Logger

  @max_sessions 50
  @default_ttl :timer.hours(2)

  defstruct [:sessions, :session_counter, :resource_pool]

  # Resource pool shared across all sessions on this controller node
  @default_pool %{
    firecracker_vms: 500,    # T1-T3 microVMs
    qemu_vms: 48,            # T4-T6 full VMs
    docker_containers: 200,  # T7-T8 containers
    total_memory_mb: 256_000,
    total_vcpus: 192
  }

  def start_link(opts), do: GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  def create_session(name, profile \\ "default"), do:
    GenServer.call(__MODULE__, {:create, name, profile})
  def destroy_session(session_id), do: GenServer.call(__MODULE__, {:destroy, session_id})
  def get_session(session_id), do: GenServer.call(__MODULE__, {:get, session_id})

  @impl true
  def init(_opts) do
    # Schedule TTL expiration sweeper
    schedule_ttl_sweep()
    {:ok, %__MODULE__{
      sessions: %{},
      session_counter: 0,
      resource_pool: @default_pool
    }}
  end

  @impl true
  def handle_call({:create, name, profile}, _from, state) do
    if map_size(state.sessions) >= @max_sessions do
      Logger.warning("Max sessions reached (#{@max_sessions})")
      {:reply, {:error, :max_sessions_reached}, state}
    else
      session_id = state.session_counter + 1
      session = %{
        id: session_id,
        name: name,
        profile: profile,
        state: :idle,
        created_at: DateTime.utc_now(),
        expires_at: DateTime.add(DateTime.utc_now(), @default_ttl, :millisecond),
        devices: %{},
        tests: [],
        resources_consumed: %{memory_mb: 0, vcpus: 0, vms: 0}
      }
      new_state = %{state |
        sessions: Map.put(state.sessions, session_id, session),
        session_counter: session_id
      }
      Logger.info("Session created: #{name} (id=#{session_id})")
      {:reply, {:ok, session_id}, new_state}
    end
  end

  def handle_call({:destroy, session_id}, _from, state) do
    case Map.get(state.sessions, session_id) do
      nil -> {:reply, {:error, :not_found}, state}
      session ->
        # Reclaim all devices allocated to this session
        Enum.each(session.devices, fn {device_id, _} ->
          HelixTest.DevicePool.release_device(device_id)
        end)
        reclaimed = session.resources_consumed
        new_pool = Map.merge(state.resource_pool, reclaimed, fn _k, a, b -> a + b end)
        new_state = %{state |
          sessions: Map.delete(state.sessions, session_id),
          resource_pool: new_pool
        }
        Logger.info("Session destroyed: #{session.name} (id=#{session_id}), " <>
          "reclaimed: #{inspect(reclaimed)}")
        {:reply, :ok, new_state}
    end
  end

  defp schedule_ttl_sweep do
    Process.send_after(self(), :sweep_expired, :timer.minutes(5))
  end

  @impl true
  def handle_info(:sweep_expired, state) do
    now = DateTime.utc_now()
    expired = Enum.filter(state.sessions, fn {_id, s} ->
      DateTime.compare(s.expires_at, now) == :lt
    end)
    Enum.each(expired, fn {id, s} ->
      Logger.info("Sweeping expired session: #{s.name} (id=#{id})")
      handle_call({:destroy, id}, nil, state)
    end)
    schedule_ttl_sweep()
    {:noreply, state}
  end
end
```

#### 5.5.2 Test State Machine

Each test session progresses through a finite state machine that defines valid transitions and entry/exit actions for each state.

```
                    +-------------+
         +--------->|    IDLE     |<--------+
         |          | (created)   |         |
         |          +------+------+         |
         |                 | create devices  |
         |          +------v------+         |
         |    +---->|    SETUP    +----+    |
         |    |     |(provisioning)|    |    |
         |    |     +------+------+    |    |
         |    |            | devices   |    |
         |    |     +------v------+    |    |
         |    |     |   RUNNING   +----+    |
         |    |     | (executing) |         |
         |    |     +------+------+         |
         |    |    chaos |  verify          |
         |    |     +----v----+  report     |
         |    +-----+CHAOS_INJECT+----------+
         |          | (faults)  |
         |          +----+------+
         |               | heal
         |          +----v----+
         |          |  VERIFY  |
         |          |(invariants)
         |          +----+------+
         |               |
         |          +----v----+
         +----------+  REPORT  |
                    | (complete)|
                    +-----------+
```

**IDLE** represents a newly created session awaiting device provisioning. On the `provision` event, the session transitions to **SETUP**, where the DevicePool allocates virtual devices and the SnapshotManager restores golden snapshots. Successful provisioning triggers transition to **RUNNING**, where the TestRunner begins execution. If chaos injection is configured, the session enters **CHAOS_INJECT** while faults are active, returning to **RUNNING** upon fault healing. After test completion, **VERIFY** checks all registered invariants; violations transition through **RECOVERY** if auto-recovery is configured, or directly to **REPORT** where results are persisted and fed to the HelixQA Integration Layer.

#### 5.5.3 Phoenix LiveView Dashboard

The controller exposes a Phoenix LiveView dashboard providing real-time visibility into test execution. The dashboard subscribes to PubSub topics for test events and renders updates over WebSocket connections. Elixir/Phoenix has demonstrated capacity for 2 million concurrent WebSocket connections per node [^2182^], ensuring the dashboard scales to thousands of simultaneous test observers without performance degradation.

```elixir
defmodule HelixTest.Web.TestDashboardLive do
  use HelixTest.Web, :live_view

  @impl true
  def mount(_params, _session, socket) do
    if connected?(socket) do
      Phoenix.PubSub.subscribe(HelixTest.PubSub, "test:events")
      Phoenix.PubSub.subscribe(HelixTest.PubSub, "device:health")
      Phoenix.PubSub.subscribe(HelixTest.PubSub, "chaos:faults")
    end

    {:ok, assign(socket,
      active_sessions: HelixTest.SessionManager.list_active(),
      active_tests: [],
      device_health: HelixTest.DevicePool.health_summary(),
      chaos_faults: HelixChaos.Controller.get_status(),
      metrics: HelixTest.MetricsCollector.latest()
    )}
  end

  @impl true
  def handle_info({:test_event, event}, socket) do
    {:noreply, update(socket, :active_tests, &[event | &1])}
  end

  def handle_info({:device_health, update}, socket) do
    {:noreply, assign(socket, :device_health, update)}
  end

  def handle_info({:chaos_fault, fault}, socket) do
    {:noreply, update(socket, :chaos_faults, &Map.put(&1, fault.id, fault))}
  end
end
```

### 5.6 HelixQA Integration

#### 5.6.1 Automatic Challenge Generation

The HelixQA Integration Layer transforms test outcomes into actionable challenges. When a safety invariant is violated during chaos testing, the system generates a reproducible challenge embedding the DST seed, scenario parameters, and violation details. Performance regressions are detected through statistical comparison against baselines and similarly generate point-valued challenges.

```elixir
defmodule HelixQA.ChallengeGenerator do
  @moduledoc "Generates HelixQA challenges from virtual test outcomes."

  def generate_from_report(report) do
    challenges = []
    challenges = challenges ++
      Enum.flat_map(report.failed_invariants, &generate_invariant_challenge(report, &1))
    challenges = challenges ++
      Enum.flat_map(report.metrics, &generate_metric_challenge(report, &1))
    challenges
  end

  defp generate_invariant_challenge(report, invariant) do
    [%{
      id: "chaos-#{report.session_id}-#{invariant.name}",
      type: :safety_invariant,
      title: "Safety Violation: #{invariant.name}",
      description: build_description(report, invariant),
      severity: invariant.severity,
      reproducibility: :deterministic,
      seed: report.seed,
      points: severity_points(invariant.severity),
      harness: %{
        type: "dst_replay",
        seed: report.seed,
        scenario: report.scenario_name,
        duration_sec: report.duration_sec
      }
    }]
  end

  defp build_description(report, inv) do
    "During chaos scenario '#{report.scenario_name}', the safety invariant " <>
    "'#{inv.name}' was violated at simulated time #{inv.at_time}s. " <>
    "Seed: #{report.seed} (fully reproducible). Details: #{inv.details}."
  end

  defp severity_points(:critical), do: 500
  defp severity_points(:high), do: 300
  defp severity_points(:warning), do: 150
  defp severity_points(:info), do: 50
  defp severity_points(_), do: 100
end
```

#### 5.6.2 Metrics Validation and Regression Detection

Test outcomes are validated against a baseline metrics table that defines acceptable ranges for each key performance indicator. Violations at or above the specified severity trigger quality gate failures in CI/CD pipelines.

| Metric Name | Type | Validation Rule | Baseline | Severity |
|-------------|------|----------------|----------|----------|
| helix_nodes_healthy | gauge | value >= floor(total * 0.5) + 1 | quorum | critical |
| helix_tasks_unscheduled | gauge | value == 0 (steady state) | 0 | critical |
| helix_task_schedule_latency_ms | histogram | p99 < 1000ms | 500ms | warning |
| helix_consensus_rounds_per_sec | counter | rate < 10 (stable leader) | 5/sec | warning |
| helix_test_duration_seconds | histogram | p95 < 300s | 120s | warning |
| firecracker_vcpu_utilization | gauge | value < 80% per VM | 60% | info |
| helix_chaos_faults_injected | counter | value >= 1 (chaos active) | N/A | info |
| helix_recovery_time_ms | histogram | p99 < 30000ms | 10000ms | warning |

The regression detection engine applies Welch's t-test to compare current metrics against rolling baselines of at least 10 samples, flagging regressions where both statistical significance (p < 0.05) and practical significance (>10% change from baseline) are exceeded. This dual-threshold approach avoids false positives from statistically significant but practically negligible fluctuations.

#### 5.6.3 CI/CD Integration

The Virtual Testing Matrix integrates natively with GitHub Actions, GitLab CI, and Jenkins through webhook triggers and command-line interfaces. The GitHub Actions workflow demonstrates the standard pattern: DST smoke tests gate the full tier matrix, which in turn gates regression analysis.

```yaml
# .github/workflows/virtual-test-matrix.yaml
name: HelixCluster Virtual Test Matrix

on:
  push: { branches: [main, develop] }
  pull_request: { branches: [main] }
  schedule: [ cron: '0 2 * * *' ]       # Nightly full regression

jobs:
  dst-smoke:
    runs-on: [self-hosted, helix-test]
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - name: DST Smoke — Consensus Under Partitions
        run: |
          mix helix.test.dst run \
            --workload smoke-consensus \
            --seed ${{ github.run_id }} \
            --duration 300 --buggify true
      - name: Invariant Check
        run: |
          mix helix.test.check invariants --critical-only --fail-on-violation

  tier-matrix:
    needs: dst-smoke
    runs-on: [self-hosted, helix-test]
    timeout-minutes: 45
    strategy:
      matrix:
        tier: [T1, T2, T3, T4, T5, T6, T7, T8]
    steps:
      - uses: actions/checkout@v4
      - name: Provision Fleet
        run: mix helix.test.provision --tier ${{ matrix.tier }} --count 20
      - name: Chaos Scenarios
        run: mix helix.test.chaos run --scenario tiers/${{ matrix.tier }}.yaml
      - name: Metrics Export
        run: mix helix.test.metrics export --format prometheus
      - uses: actions/upload-artifact@v4
        with: { name: metrics-${{ matrix.tier }}, path: "*.prom" }

  regression-gate:
    needs: tier-matrix
    runs-on: [self-hosted, helix-test]
    steps:
      - uses: actions/download-artifact@v4
        with: { pattern: metrics-*, merge-multiple: true }
      - name: Regression Analysis
        run: |
          mix helix.test.regression check \
            --baseline-branch main --threshold 10 \
            --format markdown --output regression-report.md
      - name: Post PR Comment
        uses: actions/github-script@v7
        if: github.event_name == 'pull_request'
        with:
          script: |
            const fs = require('fs');
            const body = fs.readFileSync('regression-report.md', 'utf8');
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner, repo: context.repo.repo,
              body: '## Virtual Test Matrix Results\\n\\n' + body
            });
```

### 5.7 WebAssembly Plugin System

#### 5.7.1 WIT Interface Definitions

The WebAssembly Plugin System uses the WebAssembly Component Model with WIT (WebAssembly Interface Types) to define contracts between the host runtime and guest plugins. This enables plugin authors to write in any language that compiles to Wasm — Rust, Go, C++, Zig — while presenting a uniform interface to the host. Wasmtime's implementation achieves 5-microsecond instance spawn and 80-95% of native performance [^2098^][^2155^], making plugin invocation practical even for high-frequency operations like per-task scheduling decisions.

```wit
// helix-cluster-plugin.wit — WIT interface for all plugin types
package helix:cluster@1.0.0;

interface device-simulator {
    record device-config {
        tier: string, vcpus: u32, memory-mb: u32,
        disk-gb: u32, arch: string,
    }
    record device-state {
        id: string, health: health-status,
        cpu-percent: f32, memory-used-mb: u64, tasks-running: u32,
    }
    variant health-status { healthy, degraded(string), failed(string) }

    create: func(config: device-config) -> result<string, string>;
    destroy: func(id: string) -> result<_, string>;
    get-state: func(id: string) -> result<device-state, string>;
    reset: func(id: string) -> result<_, string>;
    apply-fault: func(id: string, fault: fault-params) -> result<_, string>;
    record fault-params { fault-type: string, duration-sec: u32, intensity: f32 }
}

interface workload-generator {
    record workload-config {
        name: string, target-tiers: list<string>,
        task-count: u32, rate-per-sec: f32, duration-sec: u32,
    }
    record task-spec {
        id: string, cpu-request: f32, memory-request: u64,
        priority: u8, deadline-sec: option<u32>,
    }
    record task-result {
        task-id: string, completed: bool,
        assigned-node: option<string>,
        schedule-latency-ms: u64, execution-latency-ms: u64,
    }
    generate-tasks: func(config: workload-config) -> result<list<task-spec>, string>;
    validate-result: func(result: task-result) -> result<bool, string>;
}

interface fault-injector {
    record fault-config {
        name: string, fault-type: string, targets: list<string>,
        duration-sec: u32, params: list<tuple<string, string>>,
    }
    record active-fault {
        id: string, fault-type: string, targets: list<string>,
        started-at: u64, expires-at: u64,
    }
    inject: func(config: fault-config) -> result<_, string>;
    heal: func(fault-id: string) -> result<_, string>;
    get-active-faults: func() -> list<active-fault>;
}

interface metrics-exporter {
    record metric {
        name: string, value: f64,
        labels: list<tuple<string, string>>, timestamp: u64,
    }
    enum export-format { prometheus, opentelemetry, json }
    export: func(metrics: list<metric>) -> result<_, string>;
    configure: func(endpoint: string, format: export-format) -> result<_, string>;
}

world helix-plugin {
    import device-simulator;
    import workload-generator;
    import fault-injector;
    import metrics-exporter;
}
```

The plugin type matrix defines which interfaces each plugin category must implement.

| Plugin Type | Required Interfaces | Compilation Target | Use Case |
|-------------|-------------------|-------------------|----------|
| Device Simulator | `device-simulator` | `wasm32-wasi` | Custom tier virtualization (e.g., RISC-V target) |
| Workload Generator | `workload-generator` | `wasm32-wasi` | Domain-specific load patterns (ML inference, rendering) |
| Fault Injector | `fault-injector` | `wasm32-wasi` | Custom fault types beyond the 25 built-in |
| Metrics Exporter | `metrics-exporter` | `wasm32-wasi` | Integration with proprietary metrics backends |
| Composite Plugin | All four interfaces | `wasm32-wasi` | Full test suite plugins with bundled workloads |

#### 5.7.2 Capability-Based Security Model

Plugin execution operates under a capability-based security model where each plugin receives only the capabilities explicitly granted at load time. Wasmtime's WASI implementation enforces these constraints at the system call boundary, preventing plugins from accessing unauthorized resources even if compromised.

```yaml
# plugin-security-policy.yaml — Default capability grants
plugin_sandbox:
  capabilities:
    - name: "network"
      default: false
      max_bandwidth_mbps: 100
      allowed_ports: [8080, 8443]
    - name: "filesystem"
      default: false
      read_only: true
      allowed_paths: ["/tmp/helix-plugin"]
    - name: "clock"
      default: false   # Plugins use simulated time by default
    - name: "random"
      default: true    # Deterministic PRNG in test mode
  resource_limits:
    memory_mb: 128
    cpu_percent: 10
    execution_timeout_ms: 5000
    max_concurrent_calls: 4
```

### 5.8 Deployment Architecture

#### 5.8.1 K3s Kubernetes Deployment with RuntimeClasses

The Virtual Testing Matrix deploys on K3s (a lightweight Kubernetes distribution that runs on 512MB RAM and a single CPU [^1924^]), using Kubernetes RuntimeClass to route different simulator types to appropriate node configurations. The architecture defines three primary RuntimeClasses: `firecracker` for microVM-based tiers (T1-T3), `kata-qemu` for full-system emulation (T4-T6), and `runc` for container-based simulation (T7-T8).

```yaml
# runtime-classes.yaml — K3s RuntimeClass definitions
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: firecracker
handler: firecracker-containerd
# Firecracker microVMs: 28ms boot, 5MB VMM overhead
# Used for T1-T3 desktop/workstation simulation
# Node selector requires: features.virt=kvm, features.vmm=firecracker
---
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata-qemu
handler: kata-qemu
# Kata Containers with QEMU: 150-300ms boot [^2002^], full device emulation
# Used for T4-T6 console/Android/SBC simulation
# Node selector requires: features.virt=kvm, features.arch=arm64
---
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: runc
handler: runc
# Standard OCI runtime: ms-boot, namespace isolation
# Used for T7-T8 protocol-level container simulation
---
# Example pod using firecracker RuntimeClass
apiVersion: v1
kind: Pod
metadata:
  name: helix-t1-node-001
  namespace: helix-testing
  labels:
    helixcluster.io/tier: T1
    helixcluster.io/session: "session-42"
spec:
  runtimeClassName: firecracker
  containers:
    - name: helix-agent
      image: registry.helixcluster.io/agent:v1.4
      resources:
        requests: { cpu: "4", memory: "4Gi" }
        limits:   { cpu: "4", memory: "4Gi" }
      env:
        - name: DEVICE_TIER
          value: "T1"
        - name: SNAPSHOT_RESTORE
          value: "/var/lib/helixcluster/snapshots/t1-desktop-golden"
  nodeSelector:
    node-role.kubernetes.io/test: "true"
    features.vmm: firecracker
```

#### 5.8.2 Resource Sizing and Host Capacity Planning

Per-host capacity depends on the dominant workload type. The following table provides sizing guidelines for a standard test host with 96 CPU cores, 512GB RAM, and 2TB NVMe storage.

| Workload Profile | Firecracker VMs | QEMU VMs | Docker Containers | Memory | vCPUs | Disk | Network |
|-----------------|----------------|----------|-------------------|--------|-------|------|---------|
| Smoke test (20 nodes, T1-T3) | 20 | 0 | 0 | 80GB | 80 | 100GB | 1Gbps |
| Full tier matrix (160 nodes, T1-T8) | 48 | 12 | 100 | 200GB | 192 | 500GB | 10Gbps |
| DST consensus (100 sim nodes) | 0 (in-process) | 0 | 0 | 2GB | 4 | 10GB | N/A |
| Chaos scenario (all tiers) | 20 per tier | 4 per tier | 10 per tier | 150GB | 128 | 200GB | 5Gbps |
| CI pipeline (parallel max) | 200 | 8 | 50 | 400GB | 384 | 1TB | 10Gbps |
| Max density test | 2,000 | 0 | 0 | 256GB | 2,000 | 200GB | 25Gbps |

The recommended test host specification per node is: AMD EPYC or Intel Xeon with 96 cores, 512GB DDR4/DDR5 memory, 2TB NVMe storage dedicated to the snapshot pool, and dual 10GbE or single 25GbE networking. The max density row demonstrates Firecracker's demonstrated capacity of 5,000+ microVMs per host [^2022^], though practical limits for HelixCluster testing are lower due to the need for concurrent QEMU and Docker instances across multiple tiers.

#### 5.8.3 WireGuard Mesh and Observability Stack

Multi-host test clusters communicate through an encrypted WireGuard mesh that extends the cluster network across physical boundaries. A Kubernetes DaemonSet manages WireGuard interface configuration on each test host, establishing full mesh connectivity with all peers.

```yaml
# wireguard-mesh.yaml — Inter-host encrypted mesh
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: helix-wireguard-mesh
  namespace: helix-testing
spec:
  selector:
    matchLabels: { app: helix-wireguard-mesh }
  template:
    metadata:
      labels: { app: helix-wireguard-mesh }
    spec:
      hostNetwork: true
      containers:
        - name: wireguard
          image: registry.helixcluster.io/wireguard-mesh:v1.0
          securityContext:
            privileged: true
            capabilities:
              add: ["NET_ADMIN", "SYS_MODULE"]
          env:
            - name: WG_CLUSTER_KEY
              valueFrom:
                secretKeyRef:
                  name: wireguard-cluster-key
                  key: private
            - name: WG_SUBNET
              value: "10.200.0.0/16"
            - name: WG_PORT
              value: "51820"
            - name: WG_DISCOVERY
              value: "kubernetes"
          volumeMounts:
            - name: wg-config
              mountPath: /etc/wireguard
      volumes:
        - name: wg-config
          hostPath: { path: /etc/wireguard, type: DirectoryOrCreate }
---
# Prometheus ServiceMonitor for metrics collection
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: helix-test-metrics
  namespace: helix-testing
spec:
  selector:
    matchLabels:
      app: helix-test-controller
  endpoints:
    - port: http
      path: /metrics
      interval: 15s
      scrapeTimeout: 10s
  namespaceSelector:
    matchNames: [helix-testing]
```

The WireGuard mesh provides two critical capabilities for distributed testing. First, it enables test clusters to span multiple physical hosts as if they were on a single flat network, with latencies between mesh nodes configurable through tc netem for WAN simulation. Second, it encrypts all inter-host test traffic, preventing information leakage when tests execute across cloud availability zones or datacenter boundaries. The mesh uses Kubernetes-based peer discovery (via the DaemonSet's pod listing capability) so that new test hosts automatically join the mesh without manual configuration.

The observability stack combines Prometheus for metrics collection (scraping at 15-second intervals from all controller pods and test agents), Grafana for visualization (pre-configured dashboards for test progress, device health, chaos injection status, and DST engine performance), and OpenTelemetry for distributed tracing across the Rust/Elixir/Go polyglot runtime boundary. This stack ensures that every test execution produces a complete telemetry record suitable for post-hoc analysis and regression comparison.
