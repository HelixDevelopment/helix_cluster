# HelixCluster Phase 4 — Virtual Testing Matrix Architecture

**Version:** 1.0  
**Date:** 2026-01-14  
**Status:** Architecture Specification  
**Classification:** Technical Design Document — Implementation Ready  

---

## 1. Executive Summary

HelixCluster Phase 4 establishes a comprehensive **Virtual Testing Matrix** — a deterministic, scalable, and fully automated testing infrastructure that simulates all eight device tiers (T1–T8) without requiring physical hardware. The architecture integrates microVMs, full-system emulation, container-based simulation, deterministic simulation testing (DST), chaos engineering, and a WebAssembly plugin system into a unified testing platform orchestrated by an Elixir/BEAM-based controller.

### Key Metrics at a Glance

| Metric | Target | Technology |
|--------|--------|------------|
| Simulated device boot time | 28ms (snapshot) / 125ms (cold) | Firecracker microVMs |
| VMs per host | 5,000+ | Firecracker + KSM overcommit |
| Full-system emulation boot | <5 minutes | QEMU/KVM with virt machine |
| Test state reset | ~10ms | qcow2 overlay discard + recreate |
| DST time compression | 10:1 (10x faster than real time) | Single-threaded event loop |
| Chaos experiment types | 25+ fault injection modes | Chaos Mesh + custom injectors |
| CI pipeline integration | Native GitHub Actions / GitLab CI | Webhook-driven triggers |
| Plugin system latency | 5us spawn, 80–95% native perf | Wasmtime Component Model |

### Design Philosophy

Phase 4 is built on five foundational pillars derived from the most advanced distributed systems testing practices in the industry:

1. **Deterministic Reproducibility** — Every test failure can be replayed with the same seed, identical to FoundationDB's approach of 1 trillion CPU-hours of simulation testing.
2. **Hardware Abstraction** — All I/O (network, disk, time, randomness) is abstracted behind swappable interfaces, enabling the same code to run in production and simulation.
3. **Composable Fault Injection** — Faults are first-class, composable primitives: `Partition`, `Crash`, `Latency(ms)`, `ClockSkew(sec)`, `Byzantine(prob)`.
4. **Instant State Reset** — Using Firecracker snapshots and qcow2 copy-on-write overlays, any test state can be reset in under 50ms.
5. **Continuous Validation** — Every PR triggers hundreds of thousands of simulation tests; chaos experiments run continuously in CI pipelines.

### Architecture in One Paragraph

The Virtual Testing Matrix consists of (1) a **Device Simulation Layer** using Firecracker for T1–T3 (PC/Workstation-class), QEMU/KVM for T4–T6 (Console/Android), and Docker/binfmt_misc for T7–T8 (iOS-limited/HarmonyOS); (2) a **Deterministic Simulation Testing Engine** written in Rust using turmoil/shuttle that runs distributed cluster protocols in a single-threaded event loop with virtual time; (3) a **Chaos Engineering System** built on Elixir/OTP that orchestrates 25+ fault injection types via Chaos Mesh CRDs and custom fault injectors; (4) a **Virtual Testing Controller** providing test orchestration, snapshot/restore, and a Phoenix LiveView dashboard; (5) a **HelixQA Integration Layer** for automatic challenge generation and regression detection; and (6) a **WebAssembly Plugin System** via Wasmtime for language-agnostic extensibility. The entire infrastructure deploys on K3s Kubernetes, supports multi-host scaling, and integrates natively with GitHub Actions and GitLab CI.

---

## 2. Architecture Overview

### 2.1 Design Principles

| # | Principle | Description | Enforcement |
|---|-----------|-------------|-------------|
| 1 | **Determinism** | All test execution must be perfectly reproducible from a seed value. | Seeded PRNG; simulated network/time/disk; single-threaded event loop |
| 2 | **Isolation** | Each simulated device runs in a fully isolated execution context. | KVM hardware isolation; namespace isolation; process isolation |
| 3 | **Scalability** | The matrix must scale from 1 to 10,000+ simulated devices. | Horizontal pod scaling on K3s; Firecracker density 5,000/host |
| 4 | **Fidelity** | Simulation must accurately reflect real device behavior. | Full-system emulation; real Linux kernels; virtio devices |
| 5 | **Composability** | Testing primitives must compose arbitrarily. | YAML-based scenarios; plugin system; functional composition |
| 6 | **Observability** | Every aspect of test execution must be observable. | OpenTelemetry tracing; Prometheus metrics; LiveView dashboard |
| 7 | **Speed** | Test iteration cycles must be measured in seconds. | 28ms snapshot restore; 10:1 time compression; parallel execution |

### 2.2 System Context

HelixCluster Phase 4 sits between Phase 2 (Device Agents) and Phase 5 (HelixQA). It takes device agent binaries as inputs, subjects them to rigorous virtual testing across all tiers, and produces verified artifacts ready for production deployment. Test failures feed back into device agent development; chaos findings inform protocol design; CI metrics drive quality gates.

### 2.3 Component Overview

The system is organized into six major subsystems:

1. **Virtual Device Simulation Layer** — Simulates T1-T8 using the appropriate technology per tier
2. **DST Engine** — Deterministic simulation testing for core protocol validation
3. **Chaos Engineering System** — Fault injection and resilience testing
4. **Virtual Testing Controller** — Orchestration, state management, dashboard
5. **HelixQA Integration Layer** — Challenge generation, metrics validation, regression detection
6. **WebAssembly Plugin System** — Language-agnostic extensibility for custom test logic

---

## 3. Virtual Device Simulation Layer

The Device Simulation Layer creates accurate virtual representations of all eight HelixCluster device tiers. The guiding principle: **"use the lightest simulator that provides sufficient fidelity for each tier."**

### 3.1 MicroVM-Based Simulation (Firecracker)

**Scope:** T1 (Desktop PC — FULL), T2 (Laptop PC — FULL), T3 (Workstation PC — FULL)

#### Firecracker Architecture

Firecracker provides the fastest boot (~125ms cold, ~28ms from snapshot), highest density (5,000+/host), and lowest overhead (<5MB VMM) for PC-class devices. Each microVM runs a real Linux kernel with the HelixCluster agent.

#### Firecracker VM Configuration (JSON)

```json
{
  "boot-source": {
    "kernel_image_path": "/var/lib/helixcluster/vmlinux-5.15-helix",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off nomodules quiet"
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "/var/lib/helixcluster/devices/t1-desktop-rootfs.ext4",
      "is_root_device": true,
      "is_read_only": true
    },
    {
      "drive_id": "overlay",
      "path_on_host": "/var/lib/helixcluster/sessions/{SESSION_ID}/overlay-{VM_ID}.ext4",
      "is_root_device": false,
      "is_read_only": false
    }
  ],
  "machine-config": {
    "vcpu_count": 4,
    "mem_size_mib": 4096,
    "smt": true
  },
  "network-interfaces": [
    {
      "iface_id": "eth0",
      "host_dev_name": "tap-{VM_ID}",
      "guest_mac": "AA:FC:00:01:{VM_ID_HEX}:01"
    }
  ],
  "vsock": {
    "guest_cid": {VM_ID + 3},
    "uds_path": "/var/run/helixcluster/vsock-{VM_ID}.sock"
  }
}
```

#### Snapshot/Restore for Instant Test Reset

```bash
#!/bin/bash
# helix-firecracker-snapshot.sh

SNAPSHOT_DIR="/var/lib/helixcluster/snapshots"
SESSION_DIR="/var/lib/helixcluster/sessions"
FIRECRACKER_SOCK="/run/firecracker/{VM_ID}.sock"

create_golden_snapshot() {
    local vm_id=$1 tier=$2
    boot_vm "$vm_id" "$tier"
    wait_for_vsock_agent "$vm_id" 30
    curl --unix-socket "$FIRECRACKER_SOCK" -X PATCH         'http://localhost/vm' -d '{"state": "Paused"}'
    curl --unix-socket "$FIRECRACKER_SOCK" -X PUT         'http://localhost/snapshot/create'         -d "{"snapshot_type": "Full", "snapshot_path": "${SNAPSHOT_DIR}/${tier}-golden-${vm_id}.snap", "mem_file_path": "${SNAPSHOT_DIR}/${tier}-golden-${vm_id}.mem"}"
}

restore_from_snapshot() {
    local vm_id=$1 tier=$2 session_id=$3
    local snap="${SNAPSHOT_DIR}/${tier}-golden-base.snap"
    local mem="${SNAPSHOT_DIR}/${tier}-golden-base.mem"
    mkdir -p "${SESSION_DIR}/${session_id}"
    curl --unix-socket "$FIRECRACKER_SOCK" -X PUT         'http://localhost/snapshot/load'         -d "{"snapshot_path": "${snap}", "mem_file_path": "${mem}"}"
    curl --unix-socket "$FIRECRACKER_SOCK" -X PATCH         'http://localhost/vm' -d '{"state": "Resumed"}'
}
```

#### VM Density

| Configuration | Per-VM Resources | Max per Host |
|---|---|---|
| T1 Desktop (FULL trust) | 4 vCPU, 4GB RAM | ~48 VMs |
| T2 Laptop (FULL trust) | 2 vCPU, 2GB RAM | ~96 VMs |
| T3 Workstation (FULL trust) | 8 vCPU, 8GB RAM | ~24 VMs |
| Micro smoke test VM | 1 vCPU, 128MB RAM | ~2000 VMs (with KSM) |
| **Snapshot restore time** | **N/A** | **~28ms per VM** |

### 3.2 Full-System Emulation (QEMU/KVM)

**Scope:** T4 (Gaming Console — SEMI), T5 (Android Device — SEMI), T6 (SBC/ARM — STANDARD)

#### T6 SBC Simulation (RK3588-approximate)

```bash
qemu-system-aarch64 \
    -name "helix-t6-sbc-{VM_ID}" \
    -machine type=virt,virtualization=on,gic-version=3 \
    -cpu max,pauth-impdef=on,sve=on \
    -smp 8,sockets=1,clusters=2,cores=4,threads=1 \
    -m 16384 \
    -accel kvm \
    -device virtio-net-pci,netdev=net0 \
    -netdev tap,id=net0,ifname=tap-t6-{VM_ID} \
    -drive file=/var/lib/helixcluster/devices/t6-sbc-rootfs.qcow2,if=virtio \
    -bios /usr/share/AAVMF/AAVMF_CODE.fd
```

**Note:** Mali-G610 GPU, 6 TOPS NPU, and 8K VPU are NOT emulated. GPU/NPU workloads require actual hardware.

#### T5 Android Simulation (Cuttlefish)

```bash
export HOME=/var/lib/helixcluster/cuttlefish/{VM_ID}
./bin/launch_cvd \
    -daemon -cpus=4 -memory_mb=4096 \
    -blank_data_image_mb=8192 \
    -instance_nums={INSTANCE_NUM} \
    -enable_minimal_mode=true
```

#### T4 Console Simulation

QEMU does NOT support PlayStation 4 emulation. T4 is simulated at the **protocol level** — the HelixCluster agent binary runs in a constrained x86_64 VM matching console-class hardware specifications.

### 3.3 Container-Based Simulation (Docker + binfmt_misc)

**Scope:** T7 (iOS — EDGE_DONOR), T8 (HarmonyOS — SEMI)

```bash
# Enable QEMU binfmt_misc for cross-architecture execution
docker run --rm --privileged tonistiigi/binfmt --install all

# T7 iOS-limited: Protocol-level container simulation
docker run -d --name "helix-t7-{i}" \
    --network helix-cluster-net \
    --memory="128m" --cpus="0.5" \
    --env DEVICE_ID="t7-ios-{i}" \
    helix/t7-ios-sim:latest

# T8 HarmonyOS: ARM64 container simulation
docker run -d --name "helix-t8-{i}" \
    --network helix-cluster-net \
    --platform linux/arm64 \
    --memory="256m" --cpus="1.0" \
    --env DEVICE_ID="t8-harmonyos-{i}" \
    helix/t8-harmonyos-sim:latest
```

### 3.4 Platform-Specific Simulators

| Platform | Simulator | Architecture | Status |
|----------|-----------|--------------|--------|
| Android AOSP (T5) | Cuttlefish / CrosVM | arm64/x86_64 | Full — official Google reference |
| iOS (T7) | iOS Simulator (macOS only) | x86_64/arm64 | Limited — protocol-level only |
| iOS true virtualization | Corellium | arm64 | Enterprise ($9,995+) |
| macOS CI (T7 builds) | Tart (Virtualization.framework) | arm64 | Full for CI builds |
| HarmonyOS (T8) | OpenHarmony container | arm64 | Protocol-level |

### 3.5 Device Profile Registry

All device tiers are defined in a centralized YAML registry specifying simulator, resources, network constraints, and trust model. The registry is consumed by the DevicePool manager to provision the correct simulation environment for each tier.


---

## 4. Deterministic Simulation Testing (DST) Engine

The DST Engine runs real production code in a fully deterministic, single-threaded simulation. Inspired by FoundationDB (1 trillion CPU-hours, zero production bugs), it replaces all non-deterministic inputs with controllable implementations.

### 4.1 DST Architecture

Three key abstractions enable deterministic testing:

1. **Single-threaded pseudo-concurrency** — The entire cluster runs in one process, one thread. Cooperative multitasking via async/await means no scheduler non-determinism.
2. **Interface swapping** — The same code runs in production and simulation. A global `g_network` pointer holds either a real network impl (production) or a simulated one (testing).
3. **Deterministic randomness** — A seeded PRNG replaces all randomness. Same seed = same execution.

#### Interface Swapping Pattern (Production ↔ Simulation)

```rust
// helix-cluster-sim/src/network.rs
#[cfg(not(feature = "simulation"))]
pub use tokio::net::{TcpListener, TcpStream, UdpSocket};

#[cfg(feature = "simulation")]
pub use turmoil::net::{TcpListener, TcpStream, UdpSocket};
```

#### Deterministic Event Loop Core

```rust
// helix-cluster-sim/src/sim_loop.rs
use std::collections::BinaryHeap;

pub struct SimLoop {
    events: BinaryHeap<SimEvent>,
    now: SimInstant,
    rng: SeededRng,
    network: SimulatedNetwork,
    disk: SimulatedDisk,
    nodes: Vec<NodeHandle>,
    invariants: Vec<Box<dyn Invariant>>,
    running: bool,
}

impl SimLoop {
    pub fn new(seed: u64) -> Self {
        Self {
            events: BinaryHeap::new(),
            now: SimInstant::from_secs(0),
            rng: SeededRng::seed_from_u64(seed),
            network: SimulatedNetwork::new(),
            disk: SimulatedDisk::new(),
            nodes: Vec::new(),
            invariants: Vec::new(),
            running: true,
        }
    }

    pub fn spawn_node(&mut self, config: NodeConfig) -> NodeId {
        let node_id = NodeId(self.nodes.len());
        let handle = SimNode::spawn(node_id, config, self.network.clone());
        self.nodes.push(handle);
        self.events.push(SimEvent {
            time: self.now,
            kind: EventKind::NodeStart(node_id),
        });
        node_id
    }

    pub fn run(&mut self, max_duration: Duration) -> SimResult {
        let end_time = self.now + max_duration;
        while self.running && self.now < end_time {
            while let Some(event) = self.events.peek() {
                if event.time > self.now { break; }
                let event = self.events.pop().unwrap();
                self.handle_event(event);
            }
            for node in &mut self.nodes {
                if node.is_ready() { node.run_until_yield(&mut self.now); }
            }
            if let Some(next_event) = self.events.peek() {
                self.now = next_event.time;
            } else { self.running = false; }
            for invariant in &self.invariants {
                if let Err(violation) = invariant.check(&self.nodes, self.now) {
                    return SimResult::InvariantViolated {
                        invariant: invariant.name(),
                        violation, at_time: self.now, seed: self.rng.seed(),
                    };
                }
            }
        }
        SimResult::Success { duration: self.now, seed: self.rng.seed() }
    }
}
```

### 4.2 Network Simulation Layer

#### turmoil Integration

```rust
// helix-cluster-sim/tests/dst_consensus.rs
use turmoil::{Builder, Result};

#[test]
fn consensus_survives_partition() -> Result {
    let mut sim = Builder::new()
        .simulation_duration(Duration::from_secs(3600))
        .build();

    for i in 0..5 {
        sim.host(format!("node-{}", i), || async move {
            let config = NodeConfig::builder()
                .node_id(i)
                .peers((0..5).filter(|&p| p != i).collect())
                .build();
            helix_cluster::run_node(config).await
        });
    }

    sim.client("workload", async move {
        let client = helix_cluster::Client::new("node-0");
        for i in 0..100 {
            client.submit_task(TaskSpec {
                id: format!("task-{}", i),
                cpu_request: 1.0, memory_request: 512,
                priority: TaskPriority::Normal,
            }).await?;
            tokio::time::sleep(Duration::from_secs(36)).await;
        }
        Ok(())
    });

    sim.partition("node-0", "node-1");
    sim.partition("node-0", "node-2");
    sim.heal("node-0", "node-1");
    sim.heal("node-0", "node-2");

    sim.client("checker", async move {
        tokio::time::sleep(Duration::from_secs(3600)).await;
        let client = helix_cluster::Client::new("node-0");
        let status = client.get_cluster_status().await?;
        assert_eq!(status.unscheduled_tasks, 0);
        assert!(status.healthy_nodes >= 3);
        Ok(())
    });

    sim.run()
}
```

#### Mininet for Network Topology Testing

```python
#!/usr/bin/env python3
from mininet.topo import Topo
from mininet.net import Mininet
from mininet.link import TCLink

class HelixClusterWANTopo(Topo):
    def build(self, n_regions=3, nodes_per_region=5):
        region_switches = []
        for r in range(n_regions):
            sw = self.addSwitch(f'region-{r}-sw')
            region_switches.append(sw)
        wan_links = [
            (0, 1, {'delay': '70ms', 'bw': 100, 'loss': 0.1}),
            (0, 2, {'delay': '150ms', 'bw': 50, 'loss': 0.5}),
            (1, 2, {'delay': '220ms', 'bw': 30, 'loss': 1.0}),
        ]
        for src, dst, params in wan_links:
            self.addLink(region_switches[src], region_switches[dst],
                        cls=TCLink, **params)
        for r in range(n_regions):
            for n in range(nodes_per_region):
                node = self.addHost(f'node-r{r}-n{n}', ip=f'10.{r}.{n}.10/24')
                self.addLink(node, region_switches[r], cls=TCLink, delay='1ms', bw=1000)
```

### 4.3 Time Compression

The DST Engine achieves **10:1 time compression** by advancing simulated time only when all actors are blocked.

| Simulated Duration | Wall Clock Time | Ratio |
|---|---|---|
| 1 hour | 6 minutes | 10:1 |
| 24 hours | 2.4 hours | 10:1 |
| 7 days | 16.8 hours | 10:1 |
| 30 days | 3 days | 10:1 |
| 1 year | 36.5 days | 10:1 |

### 4.4 BUGGIFY Integration

BUGGIFY macros inject chaos at a ~25% fire rate throughout the codebase:

```rust
macro_rules! buggify {
    ($body:expr) => {
        if helix_cluster_sim::is_buggify_enabled() 
            && helix_cluster_sim::random::<u8>() % 4 == 0 { $body }
    };
}

impl RaftNode {
    pub async fn append_entries(&mut self, req: AppendEntriesReq) -> Result<AppendEntriesResp> {
        buggify! { sim::sleep(Duration::from_secs(600)).await; }
        buggify! { if sim::random::<bool>() { return Err(RaftError::CorruptedLog); } }
        let match_index = self.log.append(req.entries)?;
        Ok(AppendEntriesResp { term: self.current_term, success: true, conflict_index: match_index })
    }
}
```

Production timeouts (60s) become BUGGIFY timeouts (0.1s) — a 600x reduction that forces timeout paths to execute.

### 4.5 Workload Design Pattern

All DST workloads follow the FoundationDB 4-phase pattern: **SETUP → EXECUTION → CHECK → METRICS**.

```toml
[configuration]
name = "ConsensusUnderChaos"
seed = 42
buggify = true
simulated_duration_sec = 3600

[cluster]
node_count = 5
task_count = 100

[[workload.setup]]
name = "CreateCluster"
action = "spawn_nodes"
params = { count = 5 }

[[workload.setup]]
name = "WaitForConvergence"
action = "wait_for_quorum"
params = { timeout_sec = 60 }

[[workload.execution.chaos]]
name = "RandomPartition"
action = "network_partition"
schedule = "random"
params = { probability = 0.05, duration_min = 5, duration_max = 60 }

[[workload.execution.chaos]]
name = "NodeCrash"
action = "node_crash"
schedule = "random"
params = { probability = 0.02, auto_recover = true, recover_delay_sec = 30 }

[[workload.execution.workload]]
name = "TaskStorm"
action = "submit_tasks"
params = { rate = 10, total = 100 }

[[workload.check.invariant]]
name = "NoLostTasks"
assertion = "all_tasks_scheduled"
severity = "critical"

[[workload.check.invariant]]
name = "NoDoubleAssignment"
assertion = "no_task_assigned_to_multiple_nodes"
severity = "critical"

[[workload.metrics]]
name = "throughput"
type = "tasks_per_second"

[[workload.metrics]]
name = "latency_p99"
type = "task_schedule_latency_ms"
percentile = 99
```

---

## 5. Chaos Engineering & Fault Injection System

The Chaos Engineering System provides **25+ fault injection types** organized into four categories: Network, Node, Time, and Hardware.

### 5.1 Chaos Controller (Elixir/BEAM)

```elixir
defmodule HelixChaos.Controller do
  use GenServer
  require Logger

  @states [:idle, :setup, :running, :paused, :recovering, :completed, :failed]

  defstruct [
    :state, :active_scenario, :start_time,
    :target_devices, :injected_faults, :metrics,
    :abort_signal, :blast_radius
  ]

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  def load_scenario(yaml_path) do
    GenServer.call(__MODULE__, {:load_scenario, yaml_path})
  end

  def start_experiment do
    GenServer.call(__MODULE__, :start_experiment, 60_000)
  end

  def emergency_stop do
    GenServer.cast(__MODULE__, :emergency_stop)
  end

  @impl true
  def init(_opts) do
    {:ok, %__MODULE__{
      state: :idle, active_scenario: nil,
      start_time: nil, target_devices: [], injected_faults: [],
      metrics: %{}, abort_signal: false, blast_radius: 0.0
    }}
  end

  @impl true
  def handle_call({:load_scenario, yaml_path}, _from, state) do
    case HelixChaos.ScenarioEngine.load(yaml_path) do
      {:ok, scenario} ->
        Logger.info("Chaos scenario loaded: #{scenario.name}")
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
        blast_radius: scenario.blast_radius
      }
      Logger.warning(
        "CHAOS EXPERIMENT STARTED: #{scenario.name} | " <>
        "Targets: #{length(targets)}/#{length(devices)}")
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

### 5.2 Fault Injection Types

#### Network Faults (8 types)

| Fault Type | Tool | Parameters |
|---|---|---|
| Latency | tc netem | delay, jitter, distribution |
| Packet Loss | tc netem | percent, correlation |
| Packet Corruption | tc netem | percent |
| Packet Reordering | tc netem | percent, gap |
| Bandwidth Limit | tc tbf | rate, burst |
| Network Partition | iptables | direction, duration |
| DNS Failure | Chaos Mesh DNSChaos | patterns, duration |
| TCP Reset | tcpkill | port, duration |

#### Node Faults (8 types)

| Fault Type | Tool | Parameters |
|---|---|---|
| VM Crash | QMP system_powerdown | delay |
| VM Restart | QMP system_reset | delay, repeat |
| VM Pause | QMP stop/cont | duration |
| CPU Pressure | stress-ng | workers, timeout |
| Memory Pressure | stress-ng --vm | bytes, workers |
| Disk Pressure | fio + loopback | fill_percent |
| OOM Kill | cgroups memory.limit | limit_bytes |
| Graceful Shutdown | SSH shutdown | delay |

#### Time Faults (3 types)

| Fault Type | Tool | Parameters |
|---|---|---|
| Clock Skew | TimeChaos | offset_sec, clock_ids |
| Clock Freeze | TimeChaos | duration |
| Monotonic Drift | libfaketime | speed_factor |

#### Hardware Faults (6 types)

| Fault Type | Tool | Parameters |
|---|---|---|
| NMI Injection | QMP inject-nmi | target |
| Memory CE | EDAC sysfs | address |
| Memory UE | mce-inject | address |
| PCIe AER | QMP pcie_aer_inject_error | error_status |
| CPU Bit-flip | Custom QEMU mod | register, bit |
| Thermal Throttle | cpufreq governor | max_freq |

### 5.3 Scenario Engine

```yaml
apiVersion: helixcluster.io/v1
kind: ChaosScenario
metadata:
  name: network-partition-cascade
spec:
  blast_radius: 0.30
  abort_on_slo_breach: true
  target_selector:
    match_tiers: [T1, T2, T3, T6]
    exclude_labels: ["chaos.immune"]
  phases:
    - name: baseline
      duration: 60
      action: none
    - name: latency-injection
      duration: 300
      action: inject_faults
      faults:
        - type: network_latency
          params: { delay_ms: 200, jitter_ms: 50 }
          target_percent: 50
    - name: partial-partition
      duration: 300
      action: inject_faults
      faults:
        - type: network_partition
          params:
            groups: [["node-0","node-1","node-2"],["node-3","node-4","node-5"]]
    - name: severe-partition
      duration: 180
      action: inject_faults
      faults:
        - type: network_partition
          params:
            groups: [["node-0","node-1"],["node-2","node-3"],["node-4","node-5"]]
        - type: packet_loss
          params: { percent: 30 }
          target_percent: 25
    - name: recovery
      duration: 300
      action: heal_all
  success_criteria:
    - name: no_lost_tasks
      assertion: "cluster.unscheduled_tasks == 0"
      severity: critical
    - name: quorum_maintained
      assertion: "cluster.healthy_nodes >= ceil(cluster.total_nodes * 0.5) + 1"
      severity: critical
```

### 5.4 Integration with Chaos Mesh

```yaml
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: helix-partition-test
spec:
  action: partition
  mode: all
  selector:
    namespaces: [helix-testing]
    labelSelectors:
      "app.kubernetes.io/name": "helix-agent"
  direction: both
  target:
    mode: all
    selector:
      namespaces: [helix-testing]
      labelSelectors:
        "app.kubernetes.io/name": "helix-agent"
        "helixcluster.io/region": "secondary"
  duration: "5m"

---
apiVersion: chaos-mesh.org/v1alpha1
kind: TimeChaos
metadata:
  name: helix-clock-skew
spec:
  mode: random-max-percent
  value: "25"
  selector:
    namespaces: [helix-testing]
    labelSelectors:
      "app.kubernetes.io/component": "consensus"
  timeOffset: "-300s"
  clockIds: [CLOCK_REALTIME, CLOCK_MONOTONIC]
  duration: "10m"

---
apiVersion: chaos-mesh.org/v1alpha1
kind: StressChaos
metadata:
  name: helix-cpu-stress
spec:
  mode: random-max-percent
  value: "20"
  selector:
    namespaces: [helix-testing]
    labelSelectors:
      "app.kubernetes.io/name": "helix-agent"
  stressors:
    cpu:
      workers: 4
      load: 100
  duration: "5m"
```


---

## 6. Virtual Testing Controller

The Virtual Testing Controller is the central orchestrator of Phase 4. Written in Elixir/OTP, it manages test sessions, device pools, test execution, state machines, and provides a real-time dashboard.

### 6.1 Controller Architecture (Elixir/OTP)

The controller is organized as an OTP supervision tree:

```
helix_test_sup (one_for_all strategy)
  ├── SessionManager     — Session lifecycle and resource quotas
  ├── DevicePool         — Device provisioning and health monitoring
  ├── TestRunner         — Test suite execution engine
  ├── StateMachine       — IDLE→SETUP→RUNNING→VERIFY→REPORT
  ├── SnapshotManager    — Firecracker snapshots + qcow2 overlays
  ├── MetricsCollector   — Prometheus-compatible metrics
  ├── Web.Endpoint       — REST API + Phoenix LiveView
  └── Cluster (libcluster) — Distributed test execution
```

#### Session Manager

```elixir
defmodule HelixTest.SessionManager do
  use GenServer
  require Logger

  @max_sessions 50
  @default_session_ttl :timer.hours(2)

  defstruct [:sessions, :session_counter, :resource_pool]

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  def create_session(name, profile \\ "default") do
    GenServer.call(__MODULE__, {:create, name, profile})
  end

  def destroy_session(session_id) do
    GenServer.call(__MODULE__, {:destroy, session_id})
  end

  @impl true
  def init(_opts) do
    {:ok, %__MODULE__{
      sessions: %{},
      session_counter: 0,
      resource_pool: %{
        firecracker_vms: 500,
        qemu_vms: 48,
        docker_containers: 200,
        total_memory_mb: 256_000,
        total_vcpus: 192
      }
    }}
  end

  @impl true
  def handle_call({:create, name, profile}, _from, state) do
    if map_size(state.sessions) >= @max_sessions do
      {:reply, {:error, :max_sessions_reached}, state}
    else
      session_id = state.session_counter + 1
      session = %{
        id: session_id,
        name: name,
        profile: profile,
        state: :idle,
        created_at: DateTime.utc_now(),
        expires_at: DateTime.add(DateTime.utc_now(), @default_session_ttl, :millisecond),
        devices: %{},
        tests: [],
        resources: %{memory_mb: 0, vcpus: 0, vms: 0}
      }
      new_state = %{state |
        sessions: Map.put(state.sessions, session_id, session),
        session_counter: session_id
      }
      Logger.info("Test session created: #{name} (id=#{session_id})")
      {:reply, {:ok, session_id}, new_state}
    end
  end

  @impl true
  def handle_call({:destroy, session_id}, _from, state) do
    case Map.get(state.sessions, session_id) do
      nil -> {:reply, {:error, :not_found}, state}
      session ->
        Enum.each(session.devices, fn {device_id, _} ->
          HelixTest.DevicePool.destroy_device(device_id)
        end)
        new_state = %{state | sessions: Map.delete(state.sessions, session_id)}
        Logger.info("Test session destroyed: #{session.name} (id=#{session_id})")
        {:reply, :ok, new_state}
    end
  end
end
```

#### Device Pool Manager

```elixir
defmodule HelixTest.DevicePool do
  use GenServer
  require Logger

  defstruct [:available_devices, :active_devices, :health_checks]

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  def provision_devices(session_id, tier_spec) do
    GenServer.call(__MODULE__, {:provision, session_id, tier_spec})
  end

  def destroy_device(device_id) do
    GenServer.cast(__MODULE__, {:destroy, device_id})
  end

  @impl true
  def init(_opts) do
    {:ok, %__MODULE__{
      available_devices: initialize_pools(),
      active_devices: %{},
      health_checks: %{}
    }}
  end

  defp initialize_pools do
    %{
      firecracker: %{t1: 100, t2: 100, t3: 50},
      qemu: %{t4: 10, t5: 12, t6: 20},
      docker: %{t7: 50, t8: 50}
    }
  end

  defp provision_tier(session_id, tier) do
    case tier.simulator do
      "firecracker" ->
        HelixTest.FirecrackerManager.create_vm(session_id, tier.tier, tier.resources)
      "qemu_kvm" ->
        HelixTest.QemuManager.create_vm(session_id, tier.tier, tier.resources)
      "docker_protocol" ->
        HelixTest.DockerManager.create_container(session_id, tier.tier, tier.resources)
      "cuttlefish" ->
        HelixTest.CuttlefishManager.create_instance(session_id, tier.tier, tier.resources)
      _ ->
        {:error, :unknown_simulator}
    end
  end
end
```

### 6.2 Test Orchestration

```elixir
defmodule HelixTest.TestRunner do
  use GenServer
  require Logger

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  def run_suite(session_id, suite_def) do
    GenServer.call(__MODULE__, {:run_suite, session_id, suite_def}, :infinity)
  end

  @impl true
  def handle_call({:run_suite, session_id, suite}, _from, state) do
    Logger.info("Starting test suite '#{suite.name}' for session #{session_id}")
    :ok = run_phase(session_id, suite.setup)
    results = suite.tests
    |> Task.async_stream(&run_test(session_id, &1),
        max_concurrency: suite.parallelism || 4,
        timeout: suite.timeout || :timer.minutes(5))
    |> Enum.to_list()
    {passed, failed, _skipped} = collect_results(results)
    report = %{
      suite: suite.name,
      session_id: session_id,
      total: length(suite.tests),
      passed: passed,
      failed: failed,
      timestamp: DateTime.utc_now()
    }
    {:reply, {:ok, report}, state}
  end

  defp run_phase(_session_id, nil), do: :ok
  defp run_phase(session_id, steps) do
    Enum.each(steps, fn step ->
      Logger.debug("Setup step: #{step.name}")
      case step.type do
        "provision_devices" ->
          HelixTest.DevicePool.provision_devices(session_id, step.params.tiers)
        "wait_for_quorum" ->
          HelixTest.Cluster.wait_for_quorum(session_id, step.params.min_nodes)
        "load_snapshot" ->
          HelixTest.SnapshotManager.load(session_id, step.params.snapshot_name)
        _ -> :ok
      end
    end)
    :ok
  end

  defp run_test(session_id, test_def) do
    start_time = System.monotonic_time(:millisecond)
    result = case test_def.type do
      "dst_workload" ->
        HelixTest.DSTEngine.run_workload(session_id, test_def.params)
      "chaos_scenario" ->
        HelixChaos.Controller.load_scenario(test_def.params.scenario_file)
        HelixChaos.Controller.start_experiment()
      "network_test" ->
        HelixTest.NetworkTester.run(session_id, test_def.params)
      "consistency_check" ->
        HelixTest.ConsistencyChecker.run(session_id, test_def.params)
      _ ->
        {:error, :unknown_test_type}
    end
    duration = System.monotonic_time(:millisecond) - start_time
    %{name: test_def.name, result: result, duration_ms: duration}
  end

  defp collect_results(results) do
    passed = Enum.count(results, fn {:ok, r} -> match?({:ok, _}, r.result) end)
    failed = Enum.count(results, fn {:ok, r} -> match?({:error, _}, r.result) end)
    {passed, failed, 0}
  end
end
```

### 6.3 State Management

| State | Description | Transitions |
|-------|-------------|-------------|
| **IDLE** | Session created, waiting | SETUP |
| **SETUP** | Devices provisioning | RUNNING, FAILED |
| **RUNNING** | Tests executing | VERIFY, CHAOS_INJECT |
| **CHAOS_INJECT** | Chaos in progress | RUNNING |
| **VERIFY** | Invariant checking | REPORT, RECOVERY |
| **RECOVERY** | Attempting recovery | RUNNING, REPORT |
| **REPORT** | Report generated | IDLE |
| **FAILED** | Unrecoverable failure | IDLE |

### 6.4 Snapshot/Restore System

```elixir
defmodule HelixTest.SnapshotManager do
  use GenServer
  require Logger

  @snapshot_dir "/var/lib/helixcluster/snapshots"
  @overlay_dir "/var/lib/helixcluster/sessions"

  def create_golden(tier, base_image) do
    GenServer.call(__MODULE__, {:create_golden, tier, base_image})
  end

  def instant_reset(session_id, device_id, tier) do
    GenServer.call(__MODULE__, {:instant_reset, session_id, device_id, tier})
  end

  @impl true
  def handle_call({:create_golden, tier, base_image}, _from, state) do
    snapshot_path = Path.join(@snapshot_dir, "#{tier}-golden.snap")
    mem_path = Path.join(@snapshot_dir, "#{tier}-golden.mem")
    result = case tier do
      tier when tier in ["T1", "T2", "T3"] ->
        HelixTest.FirecrackerManager.create_snapshot(base_image, snapshot_path, mem_path)
      tier when tier in ["T4", "T5", "T6"] ->
        HelixTest.QemuManager.create_external_snapshot(base_image, snapshot_path)
      tier when tier in ["T7", "T8"] ->
        HelixTest.DockerManager.commit_image(base_image, "#{tier}-golden:latest")
      _ ->
        {:error, :unknown_tier}
    end
    {:reply, result, state}
  end

  @impl true
  def handle_call({:instant_reset, session_id, device_id, tier}, _from, state) do
    overlay_path = Path.join(@overlay_dir, "#{session_id}/#{device_id}.qcow2")
    golden_path = Path.join(@snapshot_dir, "#{tier}-golden")
    result = case tier do
      tier when tier in ["T1", "T2", "T3"] ->
        HelixTest.FirecrackerManager.restore_from_snapshot(
          device_id, golden_path <> ".snap", golden_path <> ".mem")
      tier when tier in ["T4", "T5", "T6"] ->
        File.rm!(overlay_path)
        HelixTest.QemuManager.create_overlay(golden_path <> ".qcow2", overlay_path)
        HelixTest.QemuManager.start_vm(device_id)
      tier when tier in ["T7", "T8"] ->
        HelixTest.DockerManager.reset_container(device_id)
    end
    {:reply, result, state}
  end
end
```


---

## 7. HelixQA Integration Layer

The HelixQA Integration Layer connects the Virtual Testing Matrix to the HelixQA challenge system for automatic challenge generation, metrics validation, regression detection, and CI/CD integration.

### 7.1 Challenge Generation

```elixir
defmodule HelixQA.ChallengeGenerator do
  @moduledoc "Automatically generates HelixQA challenges from virtual test outcomes."

  def generate_from_report(report) do
    challenges = []
    challenges = challenges ++ 
      Enum.flat_map(report.failed_invariants, fn inv ->
        generate_invariant_challenge(report, inv)
      end)
    challenges = challenges ++
      Enum.flat_map(report.metrics, fn metric ->
        if metric.regression_detected do
          generate_performance_challenge(report, metric)
        else
          []
        end
      end)
    challenges
  end

  defp generate_invariant_challenge(report, invariant) do
    [%{
      id: "chaos-#{report.session_id}-#{invariant.name}",
      type: :safety_invariant,
      title: "Safety Invariant Violated: #{invariant.name}",
      description: "During chaos scenario '#{report.scenario_name}', the safety invariant '#{invariant.name}' was violated at simulated time #{invariant.at_time}s. Violation details: #{invariant.details}. Seed: #{report.seed} (reproducible)",
      severity: invariant.severity,
      reproducibility: :deterministic,
      seed: report.seed,
      tags: ["chaos", invariant.severity, report.scenario_name],
      points: calculate_points(invariant),
      test_harness: %{
        type: "dst_replay",
        seed: report.seed,
        scenario: report.scenario_name
      }
    }]
  end

  defp generate_performance_challenge(report, metric) do
    [%{
      id: "perf-#{report.session_id}-#{metric.name}",
      type: :performance_regression,
      title: "Performance Regression: #{metric.name}",
      description: "Metric '#{metric.name}' showed regression: Baseline: #{metric.baseline} #{metric.unit}, Observed: #{metric.observed} #{metric.unit}, Regression: #{metric.percent_change}%",
      severity: :warning,
      tags: ["performance", "regression"],
      points: trunc(metric.percent_change * 10)
    }]
  end

  defp calculate_points(invariant) do
    case invariant.severity do
      :critical -> 500
      :high -> 300
      :warning -> 150
      :info -> 50
      _ -> 100
    end
  end
end
```

### 7.2 Metrics Validation

| Metric Name | Type | Validation Rule | Severity |
|---|---|---|---|
| helix_nodes_healthy | gauge | value >= floor(total_nodes * 0.5) + 1 | critical |
| helix_tasks_unscheduled | gauge | value == 0 | critical |
| helix_tasks_unscheduled | gauge | rate(value)[5m] < 1 | warning |
| helix_task_schedule_latency_ms | histogram | p99 < 1000 | warning |
| helix_consensus_rounds_total | counter | rate(value)[5m] < 10 | warning |
| helix_test_duration_seconds | histogram | p95 < 300 | warning |
| firecracker_vcpu_time_seconds | counter | rate(value)[5m] < 80% | warning |
| helix_chaos_faults_injected_total | counter | value >= 1 | info |

### 7.3 Regression Detection

```elixir
defmodule HelixQA.RegressionDetector do
  @moduledoc "Compares test runs to detect performance and correctness regressions."

  @min_samples 10
  @significance_threshold 0.05
  @regression_threshold 0.10

  def detect_regression(current_run, baseline_runs)
      when length(baseline_runs) >= @min_samples do
    Enum.flat_map(current_run.metrics, fn {metric_name, current_value} ->
      baseline_values = Enum.map(baseline_runs, &get_in(&1, [:metrics, metric_name]))
      |> Enum.reject(&is_nil/1)

      if length(baseline_values) >= @min_samples do
        baseline_mean = Statistics.mean(baseline_values)
        baseline_std = Statistics.std_dev(baseline_values)

        if baseline_std > 0 do
          z_score = abs(current_value - baseline_mean) / baseline_std
          p_value = 2 * (1 - Statistics.norm_cdf(z_score))
          percent_change = (current_value - baseline_mean) / baseline_mean

          if p_value < @significance_threshold and
             abs(percent_change) > @regression_threshold do
            [%{
              metric: metric_name,
              baseline_mean: baseline_mean,
              current_value: current_value,
              percent_change: percent_change * 100,
              p_value: p_value,
              severity: if(abs(percent_change) > 0.5, do: :critical, else: :warning)
            }]
          else
            []
          end
        else
          []
        end
      else
        []
      end
    end)
  end

  def store_baseline(run) do
    HelixQA.BaselineStore.append(run)
  end
end
```

### 7.4 CI/CD Pipeline Integration

#### GitHub Actions Workflow

```yaml
name: HelixCluster Virtual Test Matrix

on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main]
  schedule:
    - cron: '0 2 * * *'

env:
  HELIX_TEST_HOSTS: "test-runner-1,test-runner-2,test-runner-3"

jobs:
  smoke-test:
    runs-on: [self-hosted, helix-test]
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - name: Run DST Smoke Tests
        run: |
          mix helix.test.dst run --workload smoke-consensus \
            --seed ${{ github.run_id }} --duration 300 --buggify true
      - name: Validate Invariants
        run: |
          mix helix.test.check invariants --critical-only --fail-on-violation

  full-tier-test:
    needs: smoke-test
    runs-on: [self-hosted, helix-test]
    timeout-minutes: 45
    strategy:
      matrix:
        tier: [T1, T2, T3, T4, T5, T6, T7, T8]
    steps:
      - uses: actions/checkout@v4
      - name: Provision Tier ${{ matrix.tier }} Fleet
        run: |
          mix helix.test.provision --tier ${{ matrix.tier }} --count 20
      - name: Run Chaos Scenarios
        run: |
          mix helix.test.chaos run --scenario scenarios/tier-${{ matrix.tier }}-chaos.yaml
      - name: Collect Metrics
        run: |
          mix helix.test.metrics export --format prometheus \
            --output metrics-${{ matrix.tier }}.prom
      - uses: actions/upload-artifact@v4
        with:
          name: metrics-${{ matrix.tier }}
          path: metrics-${{ matrix.tier }}.prom

  regression-check:
    needs: full-tier-test
    runs-on: [self-hosted, helix-test]
    steps:
      - uses: actions/download-artifact@v4
        with:
          pattern: metrics-*
          merge-multiple: true
      - name: Detect Regressions
        run: |
          mix helix.test.regression check --baseline-branch main \
            --threshold 10 --format markdown --output regression-report.md
      - name: Post Regression Report
        uses: actions/github-script@v7
        if: github.event_name == 'pull_request'
        with:
          script: |
            const fs = require('fs');
            const report = fs.readFileSync('regression-report.md', 'utf8');
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo: context.repo.repo,
              body: '## Virtual Test Matrix Results\n\n' + report
            });
```

#### GitLab CI Configuration

```yaml
stages:
  - test_smoke
  - test_full
  - check_regression

variables:
  HELIX_TEST_IMAGE: "registry.helixcluster.io/test-matrix:v1.4"

smoke_test:
  stage: test_smoke
  image: $HELIX_TEST_IMAGE
  script:
    - helix-test dst run --workload smoke --seed $CI_PIPELINE_ID
  tags: [helix-test-runner]
  timeout: 10 minutes

test_matrix:
  stage: test_full
  image: $HELIX_TEST_IMAGE
  parallel:
    matrix:
      - TIER: [T1, T2, T3, T4, T5, T6, T7, T8]
  script:
    - helix-test provision --tier $TIER --count 20
    - helix-test chaos run --scenario scenarios/${TIER}.yaml
    - helix-test metrics export --output metrics-${TIER}.json
  artifacts:
    reports:
      metrics: metrics-*.json
    expire_in: 30 days
  tags: [helix-test-runner]
  timeout: 45 minutes

regression_gate:
  stage: check_regression
  image: $HELIX_TEST_IMAGE
  script:
    - helix-test regression check --baseline main --threshold 10
  allow_failure: false
  tags: [helix-test-runner]
```


---

## 8. WebAssembly Plugin System

The WebAssembly Plugin System enables language-agnostic extensibility for custom device simulators, workload generators, fault injectors, and metric exporters. Built on Wasmtime with the WebAssembly Component Model, it provides capability-based sandboxing and near-native performance (80-95%).

### 8.1 Plugin Architecture

```
+-------------------+     +--------------------+     +-------------------+
|   Host Runtime    |     |   WIT Interface    |     |  Guest Plugin     |
|   (Elixir/Rust)   |     |   (Component Model)|     |  (Any language)   |
|                   |     |                    |     |                   |
| - Wasmtime embed  |<--->| - DeviceSimulator  |<--->| - Zig/C plugin    |
| - Resource limits |     | - FaultInjector    |     | - Rust plugin     |
| - Capability auth |     | - WorkloadGen      |     | - Go plugin       |
| - Plugin registry |     | - MetricsExporter  |     | - Python plugin   |
+-------------------+     +--------------------+     +-------------------+
```

#### WIT Interface Definition

```wit
// helix-cluster-plugin.wit
package helix:cluster@1.0.0;

interface device-simulator {
    record device-config {
        tier: string,
        vcpus: u32,
        memory-mb: u32,
        disk-gb: u32,
        arch: string,
    }

    record device-state {
        id: string,
        health: health-status,
        cpu-percent: f32,
        memory-used-mb: u64,
        tasks-running: u32,
    }

    variant health-status {
        healthy,
        degraded(string),
        failed(string),
    }

    create: func(config: device-config) -> result<string, string>;
    destroy: func(id: string) -> result<_, string>;
    get-state: func(id: string) -> result<device-state, string>;
    reset: func(id: string) -> result<_, string>;
    apply-fault: func(id: string, fault: fault-params) -> result<_, string>;

    record fault-params {
        fault-type: string,
        duration-sec: u32,
        intensity: f32,
    }
}

interface workload-generator {
    record workload-config {
        name: string,
        target-tiers: list<string>,
        task-count: u32,
        rate-per-sec: f32,
        duration-sec: u32,
    }

    record task-spec {
        id: string,
        cpu-request: f32,
        memory-request: u64,
        priority: u8,
        deadline-sec: option<u32>,
    }

    generate-tasks: func(config: workload-config) -> result<list<task-spec>, string>;
    validate-result: func(result: task-result) -> result<bool, string>;

    record task-result {
        task-id: string,
        completed: bool,
        assigned-node: option<string>,
        schedule-latency-ms: u64,
        execution-latency-ms: u64,
    }
}

interface fault-injector {
    record fault-config {
        name: string,
        fault-type: string,
        targets: list<string>,
        duration-sec: u32,
        params: list<tuple<string, string>>,
    }

    inject: func(config: fault-config) -> result<_, string>;
    heal: func(fault-id: string) -> result<_, string>;
    get-active-faults: func() -> list<active-fault>;

    record active-fault {
        id: string,
        fault-type: string,
        targets: list<string>,
        started-at: u64,
        expires-at: u64,
    }
}

interface metrics-exporter {
    record metric {
        name: string,
        value: f64,
        labels: list<tuple<string, string>>,
        timestamp: u64,
    }

    export: func(metrics: list<metric>) -> result<_, string>;
    configure: func(endpoint: string, format: export-format) -> result<_, string>;

    enum export-format {
        prometheus,
        opentelemetry,
        json,
    }
}

world helix-plugin {
    import device-simulator;
    import workload-generator;
    import fault-injector;
    import metrics-exporter;
}
```

#### Rust Plugin Example (compiled to WASM)

```rust
// plugins/custom-device-simulator/src/lib.rs
// Compiled with: cargo build --target wasm32-wasi

use helix::cluster::plugin::*;

struct CustomDeviceSimulator;

impl DeviceSimulator for CustomDeviceSimulator {
    fn create(config: DeviceConfig) -> Result<String, String> {
        log::info!("Creating custom device: tier={}, vcpus={}, mem={}MB",
            config.tier, config.vcpus, config.memory_mb);
        let device_id = format!("custom-{}-{}", config.tier, generate_id());
        Ok(device_id)
    }

    fn destroy(id: String) -> Result<(), String> {
        log::info!("Destroying device: {}", id);
        Ok(())
    }

    fn get_state(id: String) -> Result<DeviceState, String> {
        Ok(DeviceState {
            id: id.clone(),
            health: HealthStatus::Healthy,
            cpu_percent: 15.5,
            memory_used_mb: 512,
            tasks_running: 2,
        })
    }

    fn reset(id: String) -> Result<(), String> {
        log::info!("Resetting device: {}", id);
        Ok(())
    }

    fn apply_fault(id: String, fault: FaultParams) -> Result<(), String> {
        log::warn!("Applying fault to {}: type={}, duration={}s, intensity={}",
            id, fault.fault_type, fault.duration_sec, fault.intensity);
        Ok(())
    }
}

helix::cluster::plugin::export!(CustomDeviceSimulator);
```

### 8.2 Test Plugins

Plugins can implement any combination of interfaces:

| Plugin Type | Required Interfaces | Use Case |
|---|---|---|
| Device Simulator | `device-simulator` | Custom device tier simulation |
| Workload Generator | `workload-generator` | Custom workload patterns |
| Fault Injector | `fault-injector` | Custom chaos scenarios |
| Metrics Exporter | `metrics-exporter` | Custom metrics destinations |
| Composite Plugin | All of the above | Full test suite plugins |

### 8.3 Security Model

```yaml
# plugin-security-policy.yaml
plugin_sandbox:
  # Capability-based access control
  capabilities:
    - name: "network"
      description: "Network access"
      default: false
      max_bandwidth_mbps: 100
      allowed_ports: [8080, 8443]

    - name: "filesystem"
      description: "File system access"
      default: false
      read_only: true
      allowed_paths: ["/tmp/helix-plugin"]

    - name: "clock"
      description: "Wall clock access"
      default: false
      # Plugins should use simulated time by default

    - name: "random"
      description: "Random number generation"
      default: true
      # Uses deterministic PRNG in test mode

  # Resource limits
  resource_limits:
    memory_mb: 128
    cpu_percent: 10
    execution_timeout_ms: 5000
    max_concurrent_calls: 4

  # Plugin isolation
  isolation:
    memory_isolation: true
    namespace_isolation: true
    seccomp_filter: true

  # Audit logging
  audit:
    log_all_calls: true
    log_fault_injections: true
    retention_days: 30
```

---

## 9. Deployment Architecture

### 9.1 K3s/Kubernetes Orchestration

```yaml
# k3s-cluster-config.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: helix-testing

---
apiVersion: v1
kind: ResourceQuota
metadata:
  name: helix-test-quota
  namespace: helix-testing
spec:
  hard:
    requests.cpu: "96"
    requests.memory: "256Gi"
    limits.cpu: "192"
    limits.memory: "512Gi"
    pods: "200"
    firecracker.microvm.io/vms: "500"

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: helix-test-controller
  namespace: helix-testing
spec:
  replicas: 3
  selector:
    matchLabels:
      app: helix-test-controller
  template:
    metadata:
      labels:
        app: helix-test-controller
    spec:
      serviceAccountName: helix-test
      containers:
        - name: controller
          image: registry.helixcluster.io/test-controller:v1.4
          ports:
            - containerPort: 4000
              name: http
            - containerPort: 4001
              name: rpc
          env:
            - name: KUBERNETES_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            - name: POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
            - name: LIBCLUSTER_KUBERNETES_SELECTOR
              value: "app=helix-test-controller"
            - name: LIBCLUSTER_KUBERNETES_NAMESPACE
              value: "helix-testing"
            - name: FIRECRACKER_SNAPSHOT_POOL
              value: "/var/lib/helixcluster/snapshots"
            - name: MAX_CONCURRENT_SESSIONS
              value: "50"
          resources:
            requests:
              cpu: "2"
              memory: "4Gi"
            limits:
              cpu: "4"
              memory: "8Gi"
          volumeMounts:
            - name: snapshot-pool
              mountPath: /var/lib/helixcluster/snapshots
            - name: kvm-device
              mountPath: /dev/kvm
            - name: vsock-device
              mountPath: /dev/vsock
      volumes:
        - name: snapshot-pool
          hostPath:
            path: /var/lib/helixcluster/snapshots
            type: Directory
        - name: kvm-device
          hostPath:
            path: /dev/kvm
            type: CharDevice
        - name: vsock-device
          hostPath:
            path: /dev/vsock
            type: CharDevice

---
apiVersion: v1
kind: Service
metadata:
  name: helix-test-controller
  namespace: helix-testing
spec:
  selector:
    app: helix-test-controller
  ports:
    - port: 4000
      targetPort: 4000
      name: http
    - port: 4001
      targetPort: 4001
      name: rpc

---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: helix-test-controller-hpa
  namespace: helix-testing
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: helix-test-controller
  minReplicas: 3
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: 80
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 60
      policies:
        - type: Percent
          value: 100
          periodSeconds: 60
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
        - type: Percent
          value: 10
          periodSeconds: 60

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: helix-test
  namespace: helix-testing

---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: helix-test-role
  namespace: helix-testing
rules:
  - apiGroups: [""]
    resources: ["pods", "services", "configmaps"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["chaos-mesh.org"]
    resources: ["*"]
    verbs: ["get", "list", "watch", "create", "delete"]
```

### 9.2 Resource Requirements

| Workload | VMs/Containers | Memory | vCPUs | Disk | Network |
|---|---|---|---|---|---|
| Smoke test (T1-T3, 20 nodes) | 20 Firecracker | 40GB | 80 | 100GB | 1Gbps |
| Full tier test (all T1-T8, 160 nodes) | 48 FC + 12 QEMU + 100 Docker | 200GB | 192 | 500GB | 10Gbps |
| DST consensus (100 nodes sim) | In-process (no VMs) | 2GB | 4 | 10GB | N/A |
| Chaos scenario (8 tiers) | 20 per tier | 150GB | 128 | 200GB | 5Gbps |
| CI pipeline (parallel) | Max 200 FC | 400GB | 384 | 1TB | 10Gbps |

**Recommended host sizing per test node:**
- CPU: 96 cores (AMD EPYC or Intel Xeon)
- Memory: 512GB DDR4/DDR5
- Storage: 2TB NVMe (for snapshot pool)
- Network: Dual 10GbE or 25GbE
- GPU: Optional (for T4 console GPU passthrough testing)

### 9.3 Multi-Host Scaling

```yaml
# multi-host-deployment.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: helix-test-cluster-config
  namespace: helix-testing
data:
  cluster.conf: |
    cluster.name = helix-test-matrix
    discovery.type = kubernetes
    kubernetes.namespace = helix-testing
    kubernetes.selector = app=helix-test-controller
    kubernetes.node_selector = node-role.kubernetes.io/test=true

    # etcd cluster for distributed state
    etcd.endpoints = http://etcd-0.etcd:2379,http://etcd-1.etcd:2379,http://etcd-2.etcd:2379

    # WireGuard mesh between test hosts
    wireguard.enabled = true
    wireguard.port = 51820
    wireguard.subnet = 10.200.0.0/16

    # Distributed snapshot pool
    snapshot.backend = minio
    snapshot.minio.endpoint = http://minio.helix-testing.svc:9000
    snapshot.minio.bucket = helix-snapshots
    snapshot.minio.access_key = ${MINIO_ACCESS_KEY}
    snapshot.minio.secret_key = ${MINIO_SECRET_KEY}

    # Test scheduling
    scheduler.policy = spread
    scheduler.max_sessions_per_host = 10
    scheduler.host_capacity.memory_mb = 512000
    scheduler.host_capacity.vcpus = 96
    scheduler.host_capacity.firecracker_vms = 500
    scheduler.host_capacity.qemu_vms = 12

---
# WireGuard mesh DaemonSet
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: helix-wireguard-mesh
  namespace: helix-testing
spec:
  selector:
    matchLabels:
      app: helix-wireguard-mesh
  template:
    metadata:
      labels:
        app: helix-wireguard-mesh
    spec:
      hostNetwork: true
      containers:
        - name: wireguard
          image: registry.helixcluster.io/wireguard-mesh:v1.0
          securityContext:
            privileged: true
            capabilities:
              add: ["NET_ADMIN"]
          env:
            - name: WG_CLUSTER_KEY
              valueFrom:
                secretKeyRef:
                  name: wireguard-cluster-key
                  key: private
```


---

## 11. Source Code Examples

### 11.1 Go: Device Agent Test Stub

```go
// cmd/test-stub/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/helixcluster/helixcluster/pkg/agent"
	"github.com/helixcluster/helixcluster/pkg/consensus"
)

type TestConfig struct {
	NodeID      int
	PeerAddrs   []string
	ListenAddr  string
	Tier        string
	TrustModel  string
	CPUQuota    float64
	MemoryQuota int64
}

func main() {
	var config TestConfig
	flag.IntVar(&config.NodeID, "node-id", 0, "Unique node identifier")
	flag.StringVar(&config.ListenAddr, "listen", ":8080", "Listen address")
	flag.StringVar(&config.Tier, "tier", "T1", "Device tier (T1-T8)")
	flag.StringVar(&config.TrustModel, "trust", "FULL", "Trust model")
	flag.Float64Var(&config.CPUQuota, "cpu-quota", 4.0, "CPU quota")
	flag.Int64Var(&config.MemoryQuota, "mem-quota", 4096, "Memory quota in MB")
	flag.Parse()
	config.PeerAddrs = flag.Args()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutdown signal received...")
		cancel()
	}()

	node, err := agent.NewNode(agent.NodeConfig{
		ID:          config.NodeID,
		ListenAddr:  config.ListenAddr,
		PeerAddrs:   config.PeerAddrs,
		Tier:        config.Tier,
		TrustModel:  config.TrustModel,
		CPUQuota:    config.CPUQuota,
		MemoryQuota: config.MemoryQuota,
	})
	if err != nil {
		log.Fatalf("Failed to create node: %v", err)
	}

	if err := node.Start(ctx); err != nil {
		log.Fatalf("Failed to start node: %v", err)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down...")
			node.Stop()
			return
		case <-ticker.C:
			status := node.HealthStatus()
			log.Printf("Node %d health: %s (peers: %d, tasks: %d)",
				config.NodeID, status.State, status.PeerCount, status.TaskCount)
		}
	}
}
```

### 11.2 Rust: DST Consensus Test

```rust
// helix-cluster-sim/tests/dst_consensus.rs
use std::time::Duration;
use turmoil::{Builder, Result};

/// Run a deterministic simulation test for consensus protocol.
pub fn run_consensus_dst(
    seed: u64,
    node_count: usize,
    duration_sec: u64,
    faults: Vec<FaultSpec>,
) -> Result<SimReport> {
    let mut sim = Builder::new()
        .simulation_duration(Duration::from_secs(duration_sec))
        .tick_duration(Duration::from_millis(100))
        .enable_random_ordering(false)
        .build();

    // SETUP: Create cluster nodes
    for i in 0..node_count {
        let peers: Vec<usize> = (0..node_count)
            .filter(|&p| p != i).collect();
        sim.host(format!("helix-node-{}", i), move || {
            let peers = peers.clone();
            async move {
                let config = helix_cluster::NodeConfig::builder()
                    .node_id(i)
                    .listen_addr(format!("helix-node-{}", i))
                    .peers(peers.into_iter()
                        .map(|p| format!("helix-node-{}", p))
                        .collect())
                    .build();
                helix_cluster::run_node(config).await
            }
        });
    }

    // SETUP: Create workload client
    sim.client("workload", async move {
        let client = helix_cluster::Client::new("helix-node-0");
        for i in 0..100 {
            client.submit_task(helix_cluster::TaskSpec {
                id: format!("task-{}", i),
                cpu_request: 1.0,
                memory_request: 512 * 1024 * 1024,
                priority: helix_cluster::TaskPriority::Normal,
                deadline: None,
            }).await?;
            tokio::time::sleep(Duration::from_secs(36)).await;
        }
        Ok(())
    });

    // EXECUTION: Schedule fault injections
    for fault in faults {
        match fault.fault_type {
            FaultType::NetworkPartition { node_a, node_b, duration } => {
                let delay = fault.delay_sec;
                sim.client(format!("fault-{}", fault.name), async move {
                    tokio::time::sleep(Duration::from_secs(delay)).await;
                    sim.partition(
                        format!("helix-node-{}", node_a),
                        format!("helix-node-{}", node_b)
                    );
                    tokio::time::sleep(Duration::from_secs(duration)).await;
                    sim.heal(
                        format!("helix-node-{}", node_a),
                        format!("helix-node-{}", node_b)
                    );
                    Ok(())
                });
            }
            FaultType::NodeCrash { node, .. } => {
                let delay = fault.delay_sec;
                sim.client(format!("fault-{}", fault.name), async move {
                    tokio::time::sleep(Duration::from_secs(delay)).await;
                    sim.bounce(format!("helix-node-{}", node));
                    Ok(())
                });
            }
            _ => {}
        }
    }

    // CHECK: Verify invariants
    sim.client("invariant-checker", async move {
        tokio::time::sleep(Duration::from_secs(duration_sec)).await;
        let client = helix_cluster::Client::new("helix-node-0");
        let status = client.get_cluster_status().await?;

        assert_eq!(status.unscheduled_tasks, 0,
            "SAFETY VIOLATION: {} tasks unscheduled",
            status.unscheduled_tasks);

        for task in &status.tasks {
            assert!(task.assigned_nodes.len() <= 1,
                "SAFETY VIOLATION: Task {} assigned to {} nodes",
                task.id, task.assigned_nodes.len());
        }

        let quorum = (node_count / 2) + 1;
        assert!(status.healthy_nodes >= quorum,
            "LIVENESS VIOLATION: {} healthy (quorum: {})",
            status.healthy_nodes, quorum);

        Ok(())
    });

    sim.run()?;

    Ok(SimReport {
        seed, node_count, duration_sec,
        faults_injected: faults.len(), passed: true,
    })
}
```

### 11.3 Elixir: OTP Application Supervisor

```elixir
# lib/helix_test/application.ex
defmodule HelixTest.Application do
  use Application
  require Logger

  @impl true
  def start(_type, _args) do
    topologies = Application.get_env(:helix_test, :libcluster_topologies, [])

    children = [
      {Cluster.Supervisor, [topologies, [name: HelixTest.ClusterSupervisor]]},
      HelixTest.SessionManager,
      HelixTest.DevicePool,
      HelixTest.TestRunner,
      HelixTest.StateMachine,
      HelixTest.SnapshotManager,
      HelixTest.MetricsCollector,
      HelixChaos.Controller,
      HelixChaos.NetworkFault,
      HelixChaos.NodeFault,
      HelixChaos.TimeFault,
      HelixChaos.ScenarioEngine,
      HelixTest.Web.Endpoint,
      {Phoenix.PubSub, name: HelixTest.PubSub}
    ]

    opts = [strategy: :one_for_all, name: HelixTest.Supervisor]
    Logger.info("HelixCluster Virtual Testing Controller starting...")
    Supervisor.start_link(children, opts)
  end

  @impl true
  def config_change(changed, _new, removed) do
    HelixTest.Web.Endpoint.config_change(changed, removed)
    :ok
  end
end
```

### 11.4 Zig: Custom Device Simulator Plugin

```zig
// plugins/zig-device-simulator/src/main.zig
const std = @import("std");
const helix = @import("helix-plugin-sdk");

pub const DeviceSimulator = struct {
    allocator: std.mem.Allocator,
    devices: std.StringHashMap(DeviceState),

    const DeviceState = struct {
        id: []const u8,
        tier: []const u8,
        health: HealthStatus,
        cpu_percent: f32,
        memory_used_mb: u64,
        tasks_running: u32,
    };

    const HealthStatus = enum { healthy, degraded, failed };

    pub fn init(allocator: std.mem.Allocator) DeviceSimulator {
        return .{
            .allocator = allocator,
            .devices = std.StringHashMap(DeviceState).init(allocator),
        };
    }

    pub fn create(self: *DeviceSimulator, config: helix.DeviceConfig) ![]const u8 {
        const device_id = try std.fmt.allocPrint(
            self.allocator,
            "zig-sim-{s}-{d}",
            .{ config.tier, self.devices.count() }
        );
        const state = DeviceState{
            .id = device_id,
            .tier = config.tier,
            .health = .healthy,
            .cpu_percent = 0.0,
            .memory_used_mb = 0,
            .tasks_running = 0,
        };
        try self.devices.put(device_id, state);
        helix.log.info("Created device: {s} (tier={s})", .{ device_id, config.tier });
        return device_id;
    }

    pub fn destroy(self: *DeviceSimulator, id: []const u8) !void {
        _ = self.devices.remove(id);
        self.allocator.free(id);
        helix.log.info("Destroyed device: {s}", .{id});
    }

    pub fn reset(self: *DeviceSimulator, id: []const u8) !void {
        var device = self.devices.getPtr(id) orelse
            return error.DeviceNotFound;
        device.health = .healthy;
        device.cpu_percent = 0.0;
        device.memory_used_mb = 0;
        device.tasks_running = 0;
        helix.log.info("Reset device: {s}", .{id});
    }
};
```

### 11.5 Firecracker VM Operator CRD

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: microvms.firecracker.helixcluster.io
spec:
  group: firecracker.helixcluster.io
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                tier:
                  type: string
                  enum: [T1, T2, T3]
                vcpus:
                  type: integer
                  minimum: 1
                  maximum: 8
                memoryMb:
                  type: integer
                  minimum: 128
                  maximum: 16384
                kernelImage:
                  type: string
                rootfsImage:
                  type: string
                network:
                  type: object
                  properties:
                    bridge:
                      type: string
                    macAddress:
                      type: string
                snapshot:
                  type: object
                  properties:
                    enabled:
                      type: boolean
                    goldenSnapshot:
                      type: string
            status:
              type: object
              properties:
                phase:
                  type: string
                  enum: [Pending, Creating, Running, Paused, Snapshotting, Deleting, Failed]
                vmId:
                  type: string
                ipAddress:
                  type: string
      subresources:
        status: {}
  scope: Namespaced
  names:
    plural: microvms
    singular: microvm
    kind: MicroVM
    shortNames: [mvm]

---
apiVersion: firecracker.helixcluster.io/v1
kind: MicroVM
metadata:
  name: t1-desktop-test-001
  namespace: helix-testing
  labels:
    helixcluster.io/tier: T1
spec:
  tier: T1
  vcpus: 4
  memoryMb: 4096
  kernelImage: /var/lib/helixcluster/vmlinux-5.15-helix-x86_64
  rootfsImage: /var/lib/helixcluster/devices/t1-desktop-rootfs.ext4
  network:
    bridge: br-helix-cluster
    macAddress: "AA:FC:00:01:00:01"
  snapshot:
    enabled: true
    goldenSnapshot: /var/lib/helixcluster/snapshots/t1-desktop-golden
```


---

## 12. Appendices

### A. Device Tier Simulation Matrix

| Tier | Name | Trust Model | Simulator | Architecture | GPU Emulated | NPU Emulated | Limitations |
|------|------|-------------|-----------|--------------|--------------|--------------|-------------|
| **T1** | Desktop PC | FULL | Firecracker microVM | x86_64 | virtio-gpu (basic) | No | GPU compute limited to virtio |
| **T2** | Laptop PC | FULL | Firecracker microVM | x86_64 | virtio-gpu (basic) | No | Battery/power not simulated |
| **T3** | Workstation PC | FULL | Firecracker microVM | x86_64 | virtio-gpu (basic) | No | TEE not available in VM |
| **T4** | Gaming Console | SEMI | QEMU/KVM x86_64 | x86_64 | No | No | PS4 GPU not emulable; protocol-level only |
| **T5** | Android Device | SEMI | Cuttlefish/CrosVM | arm64/x86_64 | virtio-gpu | No | TEE/Keystore simulated, not real |
| **T6** | SBC (Orange Pi 5 Max) | STANDARD | QEMU/KVM ARM64 virt | arm64 | No | No | Mali-G610, NPU, VPU not emulated |
| **T7** | iOS Device | EDGE_DONOR | Docker protocol stub | arm64 (container) | No | No | No true iOS emulation; protocol-level only |
| **T8** | HarmonyOS Device | SEMI | Docker protocol stub | arm64 (container) | No | No | OpenHarmony AOSP fork; protocol-level |

**Key:**
- **FULL trust:** Device has complete access to cluster resources
- **SEMI trust:** Device has limited access, additional verification required
- **STANDARD trust:** Standard verification, may require attestation
- **EDGE_DONOR:** Minimal trust, primarily contributes compute resources
- **Protocol-level:** Agent protocol is simulated, not full OS/device stack

### B. Fault Injection Taxonomy

#### Network Faults (8 types)

| ID | Fault Type | Tool | Parameters | Default Values | Effect |
|----|-----------|------|------------|----------------|--------|
| NF-01 | Latency injection | tc netem | delay, jitter, distribution | delay=100ms, jitter=20ms, dist=normal | Slows inter-node communication |
| NF-02 | Packet loss | tc netem | percent, correlation | percent=5%, corr=25% | Drops packets randomly |
| NF-03 | Packet corruption | tc netem | percent | percent=1% | Corrupts packet payloads |
| NF-04 | Packet reordering | tc netem | percent, gap | percent=5%, gap=5 | Reorders packet stream |
| NF-05 | Packet duplication | tc netem | percent | percent=1% | Duplicates packets |
| NF-06 | Bandwidth limit | tc tbf | rate, burst | rate=10Mbps, burst=32k | Caps available bandwidth |
| NF-07 | Network partition | iptables/nftables | direction, duration | direction=both, duration=60s | Complete connectivity loss |
| NF-08 | DNS failure | Chaos Mesh DNSChaos | patterns, duration | patterns=["*.helixcluster.io"], duration=300s | Fails DNS lookups |

#### Node Faults (8 types)

| ID | Fault Type | Tool | Parameters | Default Values | Effect |
|----|-----------|------|------------|----------------|--------|
| NF-09 | VM crash | QMP system_powerdown | delay | delay=0s | Abrupt power loss |
| NF-10 | VM restart | QMP system_reset | delay, repeat | delay=30s, repeat=1 | Hard reboot |
| NF-11 | VM pause | QMP stop/cont | duration | duration=30s | Freezes VM execution |
| NF-12 | CPU pressure | stress-ng | workers, timeout | workers=0 (all), timeout=300s | CPU exhaustion |
| NF-13 | Memory pressure | stress-ng --vm | bytes, workers, timeout | bytes=1G, workers=4, timeout=300s | OOM condition |
| NF-14 | Disk pressure | fio + loopback | fill_percent | fill_percent=95% | Disk full condition |
| NF-15 | OOM kill | cgroups memory.limit | limit_bytes | limit_bytes=50MB | Kernel OOM killer |
| NF-16 | Graceful shutdown | SSH shutdown | delay | delay=10s | Clean node shutdown |

#### Time Faults (3 types)

| ID | Fault Type | Tool | Parameters | Default Values | Effect |
|----|-----------|------|------------|----------------|--------|
| NF-17 | Clock skew | Chaos Mesh TimeChaos | offset_sec, clock_ids | offset=300s, clocks=[REALTIME] | Moves clock forward/backward |
| NF-18 | Clock freeze | Chaos Mesh TimeChaos | duration | duration=60s | Stops clock advance |
| NF-19 | Monotonic drift | libfaketime | speed_factor | speed_factor=2.0 | Speeds/slows monotonic clock |

#### Hardware Faults (6 types)

| ID | Fault Type | Tool | Parameters | Default Values | Effect |
|----|-----------|------|------------|----------------|--------|
| NF-20 | NMI injection | QMP inject-nmi | target | target=vm_id | Non-maskable interrupt |
| NF-21 | Memory CE | EDAC sysfs | address, count | count=100 | Correctable memory errors |
| NF-22 | Memory UE | mce-inject | address | address=0x1000 | Uncorrectable memory error |
| NF-23 | PCIe AER | QMP pcie_aer_inject_error | error_status | status=0x2000 | PCIe link errors |
| NF-24 | CPU bit-flip | Custom QEMU mod | register, bit | register=rax, bit=63 | Register corruption |
| NF-25 | Thermal throttle | cpufreq governor | max_freq | max_freq=1GHz | CPU frequency reduction |

### C. Performance Benchmarks

#### Firecracker MicroVM Performance

| Metric | Value | Notes |
|--------|-------|-------|
| Cold boot time | ~125ms | Full boot from kernel + rootfs |
| Snapshot restore | ~28ms | From golden snapshot |
| Memory overhead | ~5MB | Per-VM VMM overhead |
| Max VMs per host | 5,000+ | With KSM and memory overcommit |
| vCPU overhead | <1% | Near-native with KVM |
| Network latency (virtio-net) | <50us | To host bridge |
| Snapshot size | ~50MB | Compressed memory snapshot |
| VM density (smoke test) | 2,000 | 1 vCPU, 128MB each on 256GB host |
| VM density (T1 Desktop) | 48 | 4 vCPU, 4GB each on 96-core/256GB host |

#### QEMU/KVM Performance

| Metric | Value | Notes |
|--------|-------|-------|
| Cold boot time | 2-5 minutes | Full system boot with UEFI |
| Snapshot restore | 10-30 seconds | qcow2 external snapshot |
| Memory overhead | ~100MB | Per-VM QEMU process |
| Max VMs per host | 12-20 | Resource-intensive |
| vCPU overhead | <3% | Near-native with KVM |
| ARM64 emulation | ~5-10% overhead | virtio devices |
| Full snapshot | ~1-4GB | Full memory dump |

#### DST Engine Performance

| Metric | Value | Notes |
|--------|-------|-------|
| Time compression | 10:1 | Simulated:wall-clock |
| Max simulated nodes | 1,000+ | In single process |
| Events per second | 100,000+ | Event loop throughput |
| Memory per node | ~2MB | Simulated node state |
| Invariant check | <1ms | Per time-step |
| Seed reproducibility | 100% | Same seed = same result |

#### Container Simulation Performance

| Metric | Value | Notes |
|--------|-------|-------|
| Container start | 500ms-2s | Docker pull + start |
| Container reset | 500ms | Stop + recreate |
| Max containers per host | 200+ | With resource limits |
| binfmt_misc overhead | ~10-20% | QEMU user-mode emulation |
| Cross-arch overhead | 20-50% | ARM64 on x86_64 |

### D. Technology Comparison Matrix

| Technology | HelixCluster Use | Pros | Cons | Alternative |
|-----------|-----------------|------|------|-------------|
| **Firecracker** | T1-T3 microVMs | 28ms boot, 5MB VMM, 5K+/host | No GPU, limited devices | Cloud Hypervisor, QEMU microvm |
| **QEMU/KVM** | T4-T6 full emulation | Full device model, ARM64 support | Heavy (~100MB), slow boot | Cloud Hypervisor (limited devices) |
| **Cuttlefish** | T5 Android AOSP | Official Google target, CrosVM | Requires host packages, limited | Android Emulator (deprecated) |
| **Docker+binfmt** | T7-T8 containers | Lightweight, fast setup | Not true emulation, protocol-only | N/A |
| **turmoil** | Rust DST network | Deterministic, easy API | Rust only, no UDP multicast (yet) | Mininet, Shadow |
| **Mininet** | Network topology | Full Linux stack, OpenFlow | Python 2/3 issues, limited scale | Kathara, GNS3 |
| **Chaos Mesh** | K8s-native chaos | Rich CRDs, good UI | K8s only, limited VM support | LitmusChaos, Gremlin |
| **LitmusChaos** | Alternative chaos | Mature, good workflows | Fewer fault types | Chaos Mesh |
| **Elixir/OTP** | Test controller | Fault-tolerant, distributed | Learning curve, BEAM tuning | Go + etcd |
| **Rust** | DST engine | Zero-cost abstractions, safe | Compile times, async complexity | Go, C++ |
| **Wasmtime** | Plugin system | Fast, sandboxed, component model | WASI limitations, debugging | WasmEdge, WAMR |
| **K3s** | Orchestration | Lightweight K8s, single binary | Single-node default, less HA | K0s, kubeadm |
| **Prometheus** | Metrics | De facto standard, pull model | Cardinality concerns | InfluxDB, VictoriaMetrics |
| **Grafana** | Dashboards | Rich visualizations, alerts | Resource usage | Datadog, custom |
| **Phoenix LiveView** | Real-time UI | Low latency, no JS needed | Elixir required | React + WebSocket |

### E. Glossary

| Term | Definition |
|------|-----------|
| **BUGGIFY** | A testing technique where ~25% of code paths inject extreme behavior to force error handling |
| **CrosVM** | Google's Rust-based VMM used by ChromeOS and Cuttlefish |
| **Cuttlefish** | Google's official virtual Android device for AOSP development |
| **DST** | Deterministic Simulation Testing — running real code in a simulated, deterministic environment |
| **Firecracker** | AWS's microVM VMM — fast boot, high density, low overhead |
| **Golden Snapshot** | A pre-configured VM state used as a clean starting point for tests |
| **KSM** | Kernel Samepage Merging — deduplicates identical memory pages across VMs |
| **qcow2** | QEMU's copy-on-write disk image format enabling efficient snapshots |
| **SEV-SNP** | AMD Secure Encrypted Virtualization with Secure Nested Paging — hardware memory encryption |
| **TDX** | Intel Trust Domain Extensions — confidential computing technology |
| **turmoil** | A Rust library for deterministic network simulation testing |
| **virtio** | Virtual I/O standard for paravirtualized devices in VMs |
| **vsock** | Virtual socket — host-guest communication mechanism |
| **WIT** | WebAssembly Interface Types — language-agnostic interface definitions |
| **Wasmtime** | Bytecode Alliance's WebAssembly runtime |

### F. References

1. **FoundationDB Simulation** — "Testing Distributed Systems with Deterministic Simulation" — Renkan et al., 2021
2. **Firecracker** — Amazon Web Services. firecracker-microvm.github.io
3. **Chaos Mesh** — CNCF Sandbox Project. chaos-mesh.org
4. **turmoil** — tokio-rs/turmoil — Deterministic testing for async Rust
5. **WebAssembly Component Model** — W3C WebAssembly Working Group, 2024
6. **Shadow Simulator** — shadow.github.io — Discrete-event network simulator
7. **Cuttlefish** — Android Open Source Project. source.android.com
8. **Elixir/OTP Design Principles** — erlang.org/doc/design_principles
9. **Chaos Engineering** — Basiri et al., "Chaos Engineering", IEEE Software, 2016
10. **Mininet** — mininet.org — Rapid prototyping for Software Defined Networks

---

*End of Document — HelixCluster Phase 4 Virtual Testing Matrix Architecture*

**Document Control:**
- Version: 1.0
- Last Updated: 2026-01-14
- Author: HelixCluster Architecture Team
- Reviewers: Distributed Systems Engineering, QA, DevOps
- Classification: Technical Design Document — Implementation Ready
