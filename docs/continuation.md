# Continuation Document

**Revision:** 81
**Last modified:** 2026-06-01T23:02:10Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `639e479` | Foundation wave 22: mark 2 items Completed (HXC-1152/1160) — container-checkpoint migration CRDT merge / tmux full functional Attach (send-keys+capture-pane, real marker round-trip). Mutation spot-checks confirm bites. Registry 185->187 Completed / 452 Queued. |
| `1d79b5c` | Foundation wave 22: 2 disjoint streams (both approved no-fix; mutation spot-checks bite). HXC-1152 container-checkpoint migration CRDT merge (NEW pkg/checkpoint_merge: Apply(local,checkpoint) restores+merges incoming CRDT checkpoint into a clone of local via existing CRDTSessionState.Merge LWW, Migrate(source,local) models export-A->merge-B; NEW pkg/session/crdt_checkpoint.go v2 checkpoint helper avoids import cycle; convergence tests assert EXACT LWW winner values + commutativity-by-Fingerprint + idempotence + corrupt-checkpoint sentinel; mutation skip-merge -> LWW winner reverts to wrong value FAILS). HXC-1160 tmux full functional Attach (pkg/session/backends/tmux_attach.go + tmux.go Attach: replaced the dead orphaned-io.Pipe stream with a REAL stream — Write delivers keystrokes via tmux send-keys -l/Enter, Read returns REAL pane content via tmux capture-pane -p; marker round-trip test against real tmux with bounded 20x100ms poll, unique names + defer Kill, ZERO leaks; mutation neuter send-keys -> marker absent FAILS). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; both mutation spot-checks bite; W22 tmux test verified zero-leak in isolation. NOTE pre-existing leak: pkg/session.Manager-based tests (manager.go:106 session-<nanos> naming, e.g. hxc1136) can leave tmux sessions — hygiene item, not introduced here. |
| `09d0044` | Foundation wave 21: mark 3 items Completed (HXC-1130/1249/1243) — screen+zellij session backends (real screen, zellij ErrUnsupported) / emergency-stop+auto-recovery / 8 network fault injectors (verified already complete). Mutation spot-check confirms emergency-stop bite. Registry 182->185 Completed / 454 Queued. |
| `15977c0` | Foundation wave 21: 2 impl streams + 1 verified-already-done. HXC-1130 screen+zellij session backends (pkg/session/backends/screen.go real exec of /usr/bin/screen: Create -dmS / List -ls parse / Kill -X quit / SendInput -X stuff / CaptureOutput -X hardcopy; REAL integration test creates+lists(sink-side proof)+kills a uniquely-named session w/ defer-Kill, ZERO leaked sessions verified; zellij.go returns typed ErrBackendUnavailable since zellij absent — no fake; orchestrator fixed the Attach stream to deliver Write as REAL keystrokes via SendInput instead of an orphaned io.Pipe). HXC-1249 emergency-stop + auto-recovery (pkg/testing/scenario/stop.go: StopController.EmergencyStop snapshots ActiveFaults + RestoreAll -> StopResult{FaultsRecovered,HaltLatencyMS,WithinBudget,RecoveredNames}; halt latency modelled as data (faults*perFaultCost), NO real sleep; AutoRecover asserts no faults remain; post-stop ActiveFaults()==0 + WithinBudget false-case are the bites). HXC-1243 8 network fault injectors VERIFIED already complete (NetworkPartition/PacketLoss/LatencyInjection + PacketReorder/PacketDuplication/BandwidthThrottle/AsymmetricPartition/DNSBlackhole, all with passing tests) — verified+marked, not rebuilt. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; mutation spot-check: B skip-RestoreAll -> post-stop ActiveFaults!=0 FAILS (bite real); A real screen create/list/kill verified sink-side, zero leaks. |
| `bc99718` | Foundation wave 20: mark 2 items Completed (HXC-1248/1244) — YAML chaos scenario engine (blast-radius+abort) / simulated node fault injectors. Plus HXC-1110 /metrics coverage completed for the 6 gRPC-only services. Mutation spot-checks confirm bites. Registry 180->182 Completed / 457 Queued. |
| `e93fad6` | Foundation wave 20: 3 disjoint streams (all approved; A minor silent-error-discard fixed by orchestrator). /metrics sidecar completing HXC-1110 'every binary' intent (pkg/metrics/sidecar.go StartSidecar/StartSidecarFromEnv opt-in HTTP listener HELIX_METRICS_ADDR-gated; in-process scrape asserts exact 'helix_sidecar_hits_total 3'; wired into the 6 gRPC-only mains advisory/build/node/scheduler/security/session; orchestrator added log-on-bind-error to all 6 to avoid silent failure). HXC-1248 YAML chaos Scenario Engine (NEW pkg/testing/scenario: Scenario{phases,faults,MaxFaults,MaxAffected,AbortOn} parsed via yaml.v3; Engine.Run->ExecutionLog with per-phase + global blast-radius clamp + abort-on-condition early-stop; deterministic, no wall-clock; reuses chaos catalog read-only). HXC-1244 simulated node fault injectors (NEW pkg/testing/chaos/node_faults.go: NodeRestart w/ recovery window, DiskSpaceExhaustion write-block, ProcessTerminate — each a real chaos.Fault with state-flipping Apply/Restore; distinct from existing NodeCrash/SlowDisk/OOMKill). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; mutation spot-checks B blast-radius + C node-restart bite; A sidecar exact-value scrape verified. |
| `b7f988f` | Foundation wave 19: mark 3 items Completed (HXC-1142/1096/1125) — batch/interactive mode switching+preemption / GPU /proc parser (fixture-tested) / Setup Wizard orchestrator (review caught+fixed a critical not-wired-into-main CLAUDE-1 bluff; node-config now real, verified sink-side). Registry 177->180 Completed / 459 Queued. |
| `9c58146` | Foundation wave 19: 3 disjoint streams (A/B approved no-fix; C had a CRITICAL CLAUDE-1 bluff CAUGHT+FIXED by review). HXC-1142 batch/interactive mode switching + preemption (pkg/agentprovision/modeswitch.go: ModeManager with REAL fixed-capacity resource pool — Interactive Submit preempts the most-recent Batch occupant when full, marks Preempted=true in an eviction ledger, frees+reuses the slot; SwitchMode mutates live record changing the preempt outcome; mutation: Preempted=true->false fails the bite). HXC-1096 GPU /proc parser (internal/gpu/nvidia_parser.go: PURE ParseNvidiaInformation/ParseNvidiaProcTree fixture-tested on M3 — exact Model/UUID/MemoryMB, MiB+MB units, no-Model->error, malformed-mem->0, contiguous Index + Model-less skip; nvidia_reader_linux.go //go:build linux real /proc reader; mutation: corrupt Model prefix fails parse). HXC-1125 Setup Wizard orchestrator (cmd/helix-setup: Orchestrator detect->GenerateConfig->mesh-join(MeshJoiner seam)->Lifecycle SM, real per-OS memory via mem_darwin/linux/other.go, scripts/helix-setup.sh entrypoint with driver-install as printed host-step). REVIEW CAUGHT: orchestrator was implemented+tested but NEVER wired into main() (main wrote a hardcoded static config -> 21 passing tests on UNREACHABLE code = CLAUDE-1 PASS-bluff). FIXED: wired NewOrchestrator(...).Run() into run(); binary now writes node-config.yaml with REAL hardware-detected tier (T2 on M3, cpu_cores 11), flag node_id/cluster_name, wg_endpoint — VERIFIED sink-side by me. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, mutation spot-checks A+B bite, C verified end-to-end via real binary run. |
| `9a42d7c` | Foundation wave 18: mark 3 items Completed (HXC-1098/1110/1141) — GPU sharing modes (honest ErrUnsupported hardware seam) / Prometheus /metrics wired into 5 HTTP services / interactive-agent admission+context registry. Mutation spot-checks confirm bites. Registry 174->177 Completed / 462 Queued. |
| `1a67f0a` | Foundation wave 18: 3 disjoint streams (all approved no-fix; mutation spot-checks confirm bites). HXC-1098 GPU sharing modes (internal/gpu/backend_sharing.go: SharingMode Exclusive/MPS/TimeSlice/MIG + REAL DeviceSharingState.Admit decisions — exclusive rejects 2nd job, time-slice rejects over-quantum, MIG rejects over-partition-limit, Release frees; ConfigureSharing hardware control returns typed ErrUnsupported on M3 via real AppleBackend.EnableMPS — NEVER fake hardware success; 25 tests). HXC-1110 Prometheus /metrics wiring (pkg/metrics/mount.go Mount+NewServiceRegistry reusing existing Registry/PrometheusHandler; in-process httptest scrape asserts exact 'helix_test_jobs_total 2' rejecting 0/1; WIRED into 5 HTTP-serving mains gateway/policy/helixd/health/llm; gRPC-only bins correctly skipped). HXC-1141 interactive-agent provisioning (NEW pkg/agentprovision: AgentAdmission per-node cap+queue + per-user rate-limit reusing pkg/ratelimit.PerKeyLimiter; ContextRegistry copy-isolated shared-context map; workload type defined locally — ZERO scheduler edits; 9 deterministic bite tests). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green under load, dataplane+security ok; mutation spot-checks: A ConfigureSharing fake-success->ErrUnsupported test FAILS, A cap-unlimited->queue test FAILS (C), confirming decisions bite. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `639e479` |
| **Timestamp** | 2026-06-01T23:02:10Z |

## §3: Active Work

| HXC | Title | Status |
|-----|-------|--------|
| HXC-922 | Node service, advisory locks, chaos tests, docs | ✅ Done |
| HXC-921 | Web UI, K8s/Helm, integration, E2E, benchmarks | ✅ Done |
| HXC-920 | Health, security, build, wireguard services | ✅ Done |
| HXC-919 | htmux, messaging, GPU, WASM, storage, build manifest | ✅ Done |
| HXC-918 | Wire scheduler/session gRPC to real backends, etcd discovery | ✅ Done |
| HXC-916 | Stub expansion (config, retry, ratelimit, validator, build cache) | ✅ Done |
| HXC-914 | LLM, policy, setup, distributed lock | ✅ Done |
| HXC-913 | Health, security, ClassAds parser | ✅ Done |
| HXC-912 | Gateway, helixd, helix-agent | ✅ Done |
| HXC-911 | Scheduler, Session gRPC services | ✅ Done |
| HXC-910 | Console, security, testing infra | ✅ Done |

## §4: Next Planned Work

All MVP, Phase 2, Phase 3, and Phase 4 are **COMPLETE**. Potential future enhancements:

1. **Performance optimization** — Profile and optimize hot paths
2. **Multi-region support** — Cross-region cluster federation
3. **GPU scheduling v2** — Multi-GPU, fractional GPU allocation
4. **Web UI real-time** — WebSocket integration for live updates
5. **Operator pattern** — Kubernetes operator for cluster management

## §5: Known Issues / Blockers

- None

## §6: Quick Commands

```bash
# Run all tests (requires GOWORK due to workspace modules)
GOWORK=$(pwd)/go.work go test ./... -race -count=1

# Build all binaries
GOWORK=$(pwd)/go.work go build ./cmd/...

# Build web UI
cd web && npm run build

# Render Helm chart
helm template helix-cluster deploy/helm/

# Run benchmarks
go test -bench=. ./test/benchmark

# Run chaos tests
go test -race -count=1 ./test/chaos
```
