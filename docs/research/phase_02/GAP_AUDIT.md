# Phase 2 Gap Audit — Console Nodes & Distributed Foundations

| Field | Value |
|---|---|
| Auditor | Engineering auditor (code-grounded) |
| Date | 2026-06-01 |
| Method | Direct inspection of `pkg/`, `internal/`, `cmd/`, `api/` + `go test` of all Phase 2 packages |

## Verdict: ~80% complete

**Summary:** All P0/P1 distributed-foundation packages (`swim`, `wireguard`, `discovery`, `leader`, `resources`, `scheduler`, `session`, node agent) are genuinely implemented with real-behavior tests that all pass; the remaining ~20% gap is concentrated in console boot coordination (no `linux_boot.go`), real NAT traversal (STUN/UPnP/hole-punching is a stub), and live-migration strategies (CRIU/DMTCP/container correctly deferred). No PASS-bluffs found in the implemented core — tests assert sink-side behavior, not just non-panic.

All 14 Phase 2 packages compiled and passed `go test` at audit time (swim 3.2s, wireguard 0.7s, discovery 1.6s, leader 1.1s, resources 1.3s, scheduler 1.0s, session 1.7s, session/backends 3.7s, internal/node 2.3s, internal/console 2.0s, internal/gpu 3.1s, internal/scheduler 3.0s, internal/wireguard 2.6s, internal/session 2.6s).

## Deliverable Status

| Deliverable | Status | Evidence (file:line) | Notes |
|---|---|---|---|
| `pkg/swim` gossip: membership, suspicion, indirect ping | **DONE** | `pkg/swim/suspicion.go:99` (SuspectWithRelays), `pkg/swim/prober.go:55` (IndirectProbe), `pkg/swim/failure_detector.go:38`; 3136 LOC | Real incarnation/refute logic; suspicion_test + package_test assert Suspect/Confirm/Refute transitions. |
| `pkg/wireguard` mesh: tunnels, key exchange, config gen | **DONE** | `pkg/wireguard/config.go:37` (GenerateKeyPair via wgtypes/curve25519), `pkg/wireguard/mesh.go`, `pkg/wireguard/configgen.go`; 1807 LOC | Real key gen + config rendering; mesh+manager tested. |
| WireGuard key rotation | **DONE** | `pkg/wireguard/keyrotation.go:104` (RotateKeysTracked), `:17` (KeyFingerprint), `:44` (Supersedes) | Generation tracking + supersede ordering; keyrotation_config_test covers it. |
| WireGuard NAT traversal (STUN/UPnP/hole-punch) | **PARTIAL** | `pkg/wireguard/nat_traversal.go:18` ("STUN-like"), `:33` & `:41` return `"UPnP/NAT-PMP not implemented"` | DiscoverExternalAddress hits external HTTP echo only; no real STUN client, UPnP, or UDP hole-punching. Roadmap P2 risk item. |
| `pkg/discovery` registry + health-aware routing | **DONE** | `pkg/discovery/selection.go:15` (WeightedSelector), `:110-114` (healthy + TTL-expiry filter), `pkg/discovery/etcd_backend.go`; 1485 LOC | In-memory + etcd backends real; weighted health-aware selection tested. etcd_backend_test uses a mock client (real etcd proven in `pkg/etcd` integration tests per repo). |
| `pkg/leader` TTL election + fencing | **DONE** | `pkg/leader/fencing.go:117` (AcquireLease), `:45` (IsStale), `:73` (LeaseRegistry.acquire); 796 LOC | Monotonic fencing tokens + lease TTL; fencing_test asserts stale-token rejection. |
| `pkg/resources` aggregator (CPU/mem/GPU/cgroup) | **DONE** | `pkg/resources/proc_linux.go:40` (/proc/cpuinfo), `:93` (/proc/meminfo), `pkg/resources/cgroup_v2.go`; 1449 LOC | Real /proc + cgroup v2 parsing; proc_mock enables fixture-based tests. GPU is a struct field populated by console layer (see below). |
| `pkg/scheduler` plugin framework + optimistic concurrency | **DONE** | `pkg/scheduler/plugins.go:13` (Filter/Score/Bind), `pkg/scheduler/scheduler.go:154` (ScheduleOptimistic + version), `pkg/scheduler/cost_gpu.go`, `gang_preempt.go`; 2093 LOC | NodeResourcesFit/CapabilityMatch/LoadAware plugins + cost-aware GPU + gang/preempt; version CAS tested. |
| `pkg/session` CRDT state + manager | **DONE** | `pkg/session/crdt.go`, `pkg/session/convergence.go`, `pkg/session/lifecycle.go`; 3058 LOC | CRDT convergence + lifecycle tested with real merge assertions. |
| `pkg/session/backends` tmux + native PTY | **DONE** | `pkg/session/backends/native.go:15` (creack/pty), `:53` (pty.Start), `pkg/session/backends/tmux.go:20` (LookPath tmux) | Native PTY spawns real process; tmux shells out. tmux Attach is a documented partial (window-pipe, not full PTY wrap) — `tmux.go:67-73`. |
| Session migration (CRDT / CRIU / DMTCP / container) | **PARTIAL** | `pkg/session/migration.go:51` (crdtStrategy real), `:178/:196/:214` return `"... not implemented"` | Only CRDT (state-only) strategy is real; CRIU/DMTCP/container are explicit stubs — matches roadmap §6 P2 "CRIU/DMTCP research, Phase 3". |
| `internal/node` agent: heartbeat, probes, SWIM+WG wiring | **DONE** | `internal/node/node.go:84` (swim.NewProtocol), `:97` (wireguard.NewManager), `:109` (resources aggregator); node_behavior_test + server_behavior_test | Genuinely composes the three substrates; behavior tests present. |
| `internal/console` PS4/PS5 detection + thermal + trust | **DONE** | `internal/console/detector.go:46` (Detect, /proc/cpuinfo + device-tree), `thermal.go`, `trust.go`, `register.go` | Real /proc parsing with injectable paths; detector_test/thermal_test/trust_test/register_sink_test (sink-side) all present. |
| `internal/console` GPU compute wrapper (`gpu_wrapper.go`) | **DONE (renamed)** | `internal/console/gpu.go:21` (GPUQuerier, vulkanLoaderPath); gpu_test + gpu_helpers_test | Vulkan-loader probe; satisfies roadmap `gpu_wrapper.go` mapping under a different filename. |
| `internal/console` Linux boot coordination (`linux_boot.go`) | **MISSING** | No `linux_boot.go` / `LinuxBoot` symbol in `internal/console/` | Roadmap §4.3 maps `console_dim04` → `linux_boot.go`. No boot coordination code exists. |
| `internal/gpu` manager/monitor | **DONE** | `internal/gpu/manager.go`, `monitor.go`; manager_test + manager_extra_test | Generic GPU manager beyond console; tested. |
| Proto APIs (node/session/scheduler/health) | **DONE** | `api/v1/node.proto`+`node.pb.go`, `session`, `scheduler`, `health`, `advisory`, `security`, `build` (+ `_grpc.pb.go`) | Generated stubs present and consumed by internal/* servers. |
| `cmd/helix-node`, `helix-scheduler`, `helix-session` binaries | **DONE** | `cmd/helix-node`, `cmd/helix-scheduler`, `cmd/helix-session` present (all cmd/* hardened) | Binaries wired to internal servers. |
| PS4/PS5 bare-metal Linux agent (real hardware join) | **DEFERRED** | n/a | Requires owned jailbroken hardware + firmware; roadmap §7 high-risk. Detection logic exists but real-node E2E un-runnable here. |
| Console GPU as CUDA/ROCm compute donor | **DEFERRED** | n/a | Needs GPU kernels + drivers on console hardware. |

## Top Implementable Gaps (Go, no new infra)

1. **`internal/console/linux_boot.go` — boot coordination state machine.**
   Implement a `BootCoordinator` that reads kernel/firmware markers (e.g. `/proc/version`, `/sys/firmware/devicetree`, cmdline) via injectable paths and reports a `BootState` (Unknown/Booting/UserspaceReady/ClusterReady) plus a `WaitReady(ctx)` that gates node registration until probes pass. Pure file-reads + a deterministic state machine; no hardware needed for the logic.
   *Acceptance test (mutation-pairable):* feed fixture path-sets representing each boot phase and assert the returned `BootState`; mutant = flip the readiness threshold/ordering and confirm the test fails.

2. **`pkg/wireguard` real STUN client in `nat_traversal.go`.**
   Replace the HTTP-echo `DiscoverExternalAddress` with a minimal RFC 5389 STUN Binding request over UDP (parse XOR-MAPPED-ADDRESS) against a configurable STUN server; keep UPnP returning a typed `ErrUnsupported`. No new infra — uses a stock public STUN endpoint or a local test server.
   *Acceptance test:* spin an in-process fake STUN UDP responder returning a known XOR-mapped addr; assert the parsed reflexive `ip:port`. Mutant = corrupt the XOR-decode mask and confirm failure.

3. **`pkg/discovery` etcd-backed integration test (real server).**
   Add a build-tagged integration test that runs `EtcdBackend` against a real embedded/containerized etcd (repo already proves etcd in `pkg/etcd`), asserting register→lookup→TTL-expiry→deregister round-trips — closing the mock-only gap on the backend.
   *Acceptance test:* register an instance with short TTL, sleep past TTL, assert `Lookup` excludes it; mutant = drop the lease/TTL on Put and confirm the expired instance wrongly persists.

4. **`pkg/session/migration.go` container-checkpoint strategy (CRDT+manifest, no CRIU).**
   Implement `containerStrategy` to do a state-only checkpoint: serialize CRDT session state + a restart manifest (cmd, env, cwd) to a blob, "migrate" by transferring the blob and re-spawning via the native backend on target. This is real and infra-free (process restart, not live freeze), distinct from the deferred CRIU path.
   *Acceptance test:* checkpoint a session, restore on a second manager instance, assert CRDT state + manifest equality and a live re-spawned PTY. Mutant = omit env from the manifest and confirm the restored-state assertion fails.

5. **`pkg/resources` Linux GPU reader (`/sys` + NVML-free sysfs).**
   Add a `GPUReader` that enumerates `/dev/dri/card*` and `/sys/class/drm/*/device` (vendor/device IDs, VRAM where exposed) to populate `GPUInfo.Count`/vendor without vendor SDKs; wire into `NodeAggregator` (currently GPU is only copied through at `aggregator.go:122`).
   *Acceptance test:* point the reader at fixture sysfs trees (NVIDIA, AMD, none) and assert detected count/vendor. Mutant = swap the vendor-ID map and confirm misclassification fails the test.

6. **`pkg/wireguard` key-rotation grace/overlap window in mesh.**
   Extend `RotateKeysTracked` to keep the superseded key valid for a configurable overlap so in-flight peers don't drop; expose `ActiveKeys()` returning current+grace keys consumed by `mesh.go` peer config.
   *Acceptance test:* rotate, assert both keys are accepted within the window and the old key is rejected after it (driven by an injected clock). Mutant = expire the old key immediately and confirm the overlap assertion fails.

## Deferred (need external infra / hardware / Zig / GPU kernels)

- **PS4/PS5 bare-metal Linux agent join (real hardware):** needs owned, jailbroken consoles + tested firmware; detection logic is implemented but a real-node E2E cannot run in this environment. (Roadmap §7 high-risk.)
- **Console GPU CUDA/ROCm compute donor path:** requires GPU kernels (C/C++) and console GPU drivers; out of scope for Go-only work.
- **CRIU/DMTCP live process migration:** requires CRIU/DMTCP system binaries + privileged checkpoint/restore; roadmap §6 explicitly schedules this for Phase 3 research.
- **Real WireGuard kernel-module mesh over NAT across real networks:** STUN client (gap #2) is implementable, but cross-NAT hole-punching validation needs multi-host/relay infra.

**Deferred count: 4** (all blocked on owned console hardware, GPU kernels/drivers, privileged checkpoint binaries, or multi-host networks — none implementable as pure no-infra Go here).
