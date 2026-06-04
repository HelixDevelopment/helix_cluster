# Phase 5 Gap Audit — Advanced & Exotic Device Ecosystem

| Field | Value |
|---|---|
| Auditor | Engineering auditor (code-grounded, anti-bluff) |
| Date | 2026-06-01 |
| Method | Direct inspection of `pkg/`, `internal/`, `cmd/`, `api/` against PHASE_5_ROADMAP.md deliverables |
| **Honest completion** | **~3% complete** |

## 3-Axis Package Status (Refreshed 2026-06-04, HXC-939)

> **Why this section exists:** "exists" ≠ "used". `wired` is **measured** via `go list -deps ./cmd/... | grep -Fx <module-path>/<pkg>` (module = `github.com/HelixDevelopment/helix_cluster`), not assumed. **"Completed (registry) ≠ wired"** — Completed only means source+tests exist; it does NOT prove a shipped binary reaches it. Phase 5 is almost entirely **unimplemented**, so most rows are `implemented=NO`; the table makes that mechanical rather than narrative.

| Package | implemented | wired (reachable from `cmd/`) | tested |
|---|:---:|:---:|:---:|
| `pkg/device` | **NO** | n/a | n/a |
| `pkg/handheld` | **NO** | n/a | n/a |
| `pkg/fpga` | **NO** | n/a | n/a |
| `pkg/riscv` | **NO** | n/a | n/a |
| `pkg/cloudspot` | **NO** | n/a | n/a |
| `pkg/inference` | **NO** | n/a | n/a |
| `pkg/quantum` | **NO** | n/a | n/a |
| `internal/handheld` / `sbc` / `fpga` / `enterprise` / `iot` / `exotic` | **NO** | n/a | n/a |
| `pkg/scheduler` (would be *extended* for tier/power) | yes (base only) | yes | yes |
| `pkg/resources` (would be *extended* for NPU/FPGA/TFLOPS) | yes (base only) | yes | yes |
| `pkg/discovery` (would be *extended* for 15-tier/trust) | yes (base only) | yes | yes |
| `pkg/wireguard` (would be *extended* for spot teardown) | yes (base only) | yes | yes |

**Note:** the four "extended" base packages are implemented+wired+tested for their *Phase 2–4* scope but carry **none** of the Phase 5 tier/trust/power/NPU behaviour, so they are not Phase 5 deliverables. No Phase 5 package is orphaned because no Phase 5 package exists yet.

**One-line summary:** Phase 5 is effectively unstarted in code — none of the 7 planned `pkg/` packages, none of the 6 planned `internal/` packages, and none of the planned proto fields (`tier`, `trust_level`, `compute_class`, `arch`, `npu_tops`, `power_budget`) exist; the only adjacent assets are generic Phase 2–4/8C foundations (scheduler, resources, wireguard, llm manager) that were NOT extended for device-tier/trust/power-aware behaviour. Recent "Wave1–5 / Phase 8C" hardening (RBAC scopes, `pkg/fiber`, cost-aware GPU placement, PQ-crypto/`gpuattest`/TEE attestation in the `security/` submodule) is real but belongs to other tracks and does not satisfy any Phase 5 deliverable.

---

## Deliverable Status Table

The roadmap's concrete deliverables are the new `pkg/` (§4.1), new `internal/` (§4.2), extended packages (§4.3), and proto extensions (§5.3).

| Deliverable | Status | Evidence (file:line) | Notes |
|---|---|---|---|
| `pkg/device` — capability descriptor + auto-probe (P0 #1) | MISSING | dir absent (`ls pkg/` — no `device`) | Foundation for all tier/trust assignment; nothing exists. |
| `pkg/handheld` — Steam Deck/x86 handheld agent, Vulkan/battery/thermal (P0 #2) | MISSING | dir absent | No `vulkan|battery|thermal` in `pkg/`. Only `internal/console/thermal.go:13` exists (PS4/PS5 console thermal zones, Phase 2 — not handheld). |
| `pkg/fpga` — hard-SoC/soft-core/DPU backends (P1 #6) | MISSING | dir absent | No FPGA code anywhere in `pkg/`/`internal/`. |
| `pkg/riscv` — riscv64 cross-compile helpers, RVV probe (P0 #5) | MISSING | dir absent | No riscv build pipeline. (Note: roadmap claims riscv64 Go/Docker parity from Phase 3, unverified here.) |
| `pkg/cloudspot` — WireGuard join/leave + preemption handler (P1 #8) | MISSING | dir absent; `pkg/wireguard/mesh.go` has generic join/leave only | No `spot`/`preempt`/checkpoint logic; `grep -ril 'spot\|preempt' pkg/wireguard` → none. |
| `pkg/inference` — Groq/Cerebras/Jetson/llama.cpp provider-agnostic backend (P1 #10) | MISSING | `internal/llm/manager.go:142` (`Inference`) | Existing LLM layer is a local model-registry stub: no provider abstraction, no Groq/Cerebras/TensorRT/HTTP client. |
| `pkg/quantum` — Qiskit Runtime plugin (P2 #12) | MISSING / DEFERRED | dir absent | Research-only T14; external infra. |
| `internal/handheld` | MISSING | dir absent | — |
| `internal/sbc` — RK3588 NPU / Jetson TensorRT / Turing RK1 | MISSING | dir absent | — |
| `internal/fpga` | MISSING | dir absent | — |
| `internal/enterprise` — EPYC/Ampere auto-provision, Coreboot trust | MISSING | dir absent | — |
| `internal/iot` — OpenWrt/NAS/webOS agents | MISSING | dir absent | — |
| `internal/exotic` — Groq/Cerebras clients, quantum, neuromorphic | MISSING | dir absent | — |
| `pkg/scheduler` extension — tier-aware matchmaking + power-aware T9 policy (P0 #4) | MISSING | `pkg/scheduler/types.go` (no `Tier`/`Trust`/`PowerBudget`/`Arch`) | `cost_gpu.go` is Phase 8C cost-aware GPU placement (label-based `$/gpu-hr`), NOT tier/power-aware device routing. No power budget, no T9 handheld policy. |
| `pkg/resources` extension — NPU TOPS / FPGA LE / GPU TFLOPS / QPU probing | MISSING | `pkg/resources/types.go:24` (`GPUInfo` has no TFLOPS); no `npu/tops/fpga/tflops` fields | Only CPU/Mem/GPU(count)/Disk/Net cgroup-v2 probing exists. |
| `pkg/discovery` extension — 15-tier registry + trust-aware routing | MISSING | `pkg/discovery/selection.go`, `etcd_backend.go` (no `tier`/`trust`) | etcd-backed service discovery exists but is tier-agnostic. |
| `pkg/wireguard` extension — spot dynamic join/leave + preemption-safe teardown | PARTIAL | `pkg/wireguard/mesh.go`, `manager.go` | Generic mesh join/leave + key rotation exist; no preemption-aware teardown or spot-drain hook. |
| proto: `node.proto` += `tier`,`trust_level`,`compute_class`,`arch` | MISSING | `api/v1/node.proto:20-45` (only `node_id`,`region`,`status`,`reason`) | None of the 4 fields present. |
| proto: `scheduler.proto` += tier constraints, `power_budget` | MISSING | `api/v1/scheduler.proto` (grep tier/power → none) | — |
| proto: `resources.proto` += `npu_tops`,`fpga_logic_elements`,`tflops_gpu` | MISSING | `api/v1/resources.proto` file does not exist | The proto file itself is absent. |
| Steam Deck Flatpak agent (5a) | MISSING | no `cmd/` or packaging artifact | DEFERRED — needs real Steam Deck hardware for exit-gate evidence. |
| Jetson TensorRT backend (5a) | MISSING / DEFERRED | — | Needs L4T/Jetson hardware + TensorRT. |
| riscv64 CI build artifact (5b) | MISSING | no riscv pipeline in `scripts/`/CI verified | DEFERRED-ish — CI-implementable but needs toolchain. |
| FPGA DPU / Groq / Cerebras / Cloud-spot exit gates | MISSING | — | DEFERRED — all require external hardware/cloud (see below). |

**Tally:** 0 DONE, 1 PARTIAL (`pkg/wireguard` generic mesh), ~22 MISSING. Hence **~3%** (sole partial credit = reusable wireguard/scheduler/resources/discovery scaffolding that a Phase 5 build would extend).

---

## TOP IMPLEMENTABLE GAPS (Go, no new infra)

Prioritized, concrete, mutation-pairable. These need no hardware and unblock the rest of Phase 5.

### 1. `pkg/device` — universal capability descriptor + arch auto-probe  (P0, unblocks everything)
**Target:** `pkg/device/`
**Spec:** Define `Descriptor{ Tier, TrustLevel, ComputeClass, Arch, CPUCores, MemMB, GPUs, NPUTops, FPGALogicElements, GPUTFLOPS, PowerBudgetW }` with enums for the 15 tiers (T1–T15) and trust levels. Implement `Probe(ctx) (Descriptor, error)` that derives `Arch` from `runtime.GOARCH`, CPU/mem from existing `pkg/resources` readers, and reads optional override labels (env/config) for fields not auto-detectable (NPU/FPGA/TFLOPS). Provide `AssignTier(Descriptor) Tier` implementing the §50 taxonomy decision tree (e.g. `riscv64`→T10, x86+open-firmware→T1, GPU+battery present→T9). Pure Go, deterministic.
**Acceptance test (mutation-pairable):** table-driven fixtures mapping synthetic `Descriptor`s → expected tier. *Mutation:* swap the `riscv64→T10` branch to `→T2`; the riscv fixture assertion must fail. Verifies tier logic, not just non-panic.

### 2. proto + codegen: device/tier fields  (P0, blocks scheduler+discovery wiring)
**Target:** `api/v1/node.proto`, new `api/v1/resources.proto`, `api/v1/scheduler.proto`
**Spec:** Add `tier`, `trust_level`, `compute_class`, `arch` to `Node`; create `resources.proto` with `npu_tops`, `fpga_logic_elements`, `tflops_gpu`; add `power_budget` + repeated tier constraints to scheduler request. Regenerate via existing buf pipeline. Add round-trip mapping helpers `device.Descriptor <-> pb.Node`.
**Acceptance test:** marshal a Descriptor→proto→Descriptor, assert field equality incl. tier enum. *Mutation:* drop the `arch` field copy in the mapper → round-trip assertion fails.

### 3. `pkg/scheduler` tier-aware + power-aware plugin  (P0 #4)
**Target:** `pkg/scheduler/tier_power.go`
**Spec:** New `Plugin` (same `Name/Filter/Score/Bind` contract as `cost_gpu.go`) that (a) Filters out nodes whose `Tier`/`TrustLevel` violate a job's `RequiredTier`/`MinTrust`, and (b) for T9 HANDHELD nodes, Filters when `power_budget` remaining < job estimate or node reports gaming-active/on-battery label. Reuse the existing plugin registration in `plugins.go`.
**Acceptance test:** feed a job requiring T1 against a mixed node set; assert only T1 nodes survive Filter, and a charging-T9 node is admitted while a battery-only T9 is rejected. *Mutation:* invert the trust comparison (`>=`→`<`) → an untrusted node leaks through and the assertion fails.

### 4. `pkg/discovery` 15-tier registry + trust-aware selection  (P0)
**Target:** `pkg/discovery/tier_registry.go`
**Spec:** Extend the etcd-backed registry to index nodes by tier/trust and add `SelectByTier(tier, minTrust)` returning only matching entries. Backward-compatible with existing `selection.go`.
**Acceptance test:** register synthetic nodes across T1/T9/T10, query `SelectByTier(T10, …)`, assert exact set. *Mutation:* make `SelectByTier` ignore the trust filter → a below-trust node appears and assertion fails. (Run against the existing etcd integration harness for real-behavior credit.)

### 5. `pkg/cloudspot` preemption handler (logic only, no AWS)  (P1 #8)
**Target:** `pkg/cloudspot/`
**Spec:** `PreemptionWatcher` interface + a pluggable `SignalSource` (file/HTTP-poller injectable for test). On signal, fire ordered hooks: `Drain → Checkpoint → Leave(mesh)`. Provide a `FakeSignalSource` for tests; real AWS/GCP metadata poller is a thin adapter (deferred to infra). Wire `Leave` to `pkg/wireguard` mesh teardown.
**Acceptance test:** inject a fake preemption signal; assert hooks run in order and `Leave` is called exactly once within the 2-min budget (simulated clock). *Mutation:* reorder so `Leave` precedes `Checkpoint` → ordering assertion fails.

### 6. `pkg/inference` provider-agnostic interface + local backend  (P1 #10, partial)
**Target:** `pkg/inference/`
**Spec:** Define `Backend{ Name(); Generate(ctx, Req) (Resp, error) }` and a `Router` that selects a backend by capability/tier. Implement a `LocalBackend` wrapping the existing `internal/llm` manager so the interface is exercised end-to-end today. Groq/Cerebras/Jetson are adapter structs (HTTP/SDK) marked DEFERRED until credentials/hardware exist, but the interface + routing + local path are fully testable now.
**Acceptance test:** register a fake + local backend; route a request and assert the chosen backend matches the tier policy and response propagates. *Mutation:* make Router always pick index 0 → the tier-routing assertion fails.

---

## DEFERRED (external infra / hardware / Zig / GPU-kernel — cannot be honestly completed in-repo)

| Deliverable | Reason deferred |
|---|---|
| Steam Deck Flatpak agent gaming-interference gate | Requires real Steam Deck; exit gate demands real-device FPS/battery benchmark (CLAUDE-1 forbids mock-only). |
| Jetson TensorRT ≥60 TOPS backend | Requires Jetson Orin L4T hardware + TensorRT runtime. |
| FPGA hard-core (DE10-Nano/Zynq/KV260 DPU) + soft-core (Colorlight/VexRiscv) | Requires physical FPGA boards + Vitis AI / Yosys-LiteX toolchains. |
| riscv64 native CI build artifact | Implementable but needs riscv64 toolchain/runner in CI infra. |
| EPYC / Ampere Altra auto-provisioning gate | Requires real bare-metal server hardware for <30-min onboarding evidence. |
| Cloud spot hybrid (5 on-prem + 5 spot) integration gate | Requires live AWS/GCP spot accounts. |
| Groq LPU <100ms TTFT / Cerebras CS-3 | Requires GroqCloud / Cerebras cloud API credentials. |
| `pkg/quantum` Qiskit Runtime | Requires IBM Quantum account; research-only T14. |
| webOS smart TV agent | Requires webOS device + JS bridge runtime. |
| gVisor/Kata per-tier sandbox hardening | Requires gVisor/Kata runtime on real cluster nodes. |

**Deferred count: 10** — all blocked on external hardware, cloud accounts, or device-specific toolchains; none are pure-Go in-repo work. The 6 implementable gaps above (`pkg/device`, proto fields, tier/power scheduler plugin, tier-aware discovery, `pkg/cloudspot` logic, `pkg/inference` interface+local backend) constitute the honest, no-new-infra path to move Phase 5 off ~3%.
