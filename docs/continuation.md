# Continuation Document

**Revision:** 97
**Last modified:** 2026-06-02T01:00:08Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `9baae11` | Foundation wave 31: HXC-1290+1292 combined + HXC-1297 verified-done. HXC-1290 (device universal Descriptor w/ 15 tiers) + HXC-1292 (device.AssignTier decision tree) — ONE deliverable: extended pkg/device.Tier enum additively to T1-T15 (+String cases; existing TierUnknown/T1-T8/ClassifyTier/EffectiveCompute byte-for-byte UNCHANGED, verified) + NEW pkg/device/assign_tier.go AssignTier(d,reg *tierdef.Registry) Tier mapping Descriptor->tierdef.DeviceCaps and iterating HIGH->LOW returning the highest tier whose tierdef.Meets passes (networkUnknown=MaxInt sentinel so missing net field never disqualifies); 46 tests incl exact-per-tier T1/T8/T12/T15, exhaustive boundary sweep, monotonic property; mutation low->high iteration -> T15-spec wrongly returns T1 FAILS. Import device->tierdef (no cycle). HXC-1297 pkg/scheduler tier-aware+power-aware Plugin VERIFIED already complete (tier_power.go min-tier filter + watts/perf-per-watt/overshoot score + edgeaware.go + handheld.go, 67+ mutation-guarded tests; mutation tier-min < -> > fails TestTierPowerFilterRejectsBelowMinTier). ZERO new deps. Gate: build/vet/vet-integration ./... clean (tier.go edit no regression), full -short -race green, dataplane ok, tmux clean; iteration-order + tier-min mutations bite. |
| `9327930` | Foundation wave 30: mark 3 items Completed (HXC-1302/1299/1291) — wireguard spot-drain teardown / discovery 15-tier registry / device.Probe real host detection. Mutation spot-checks confirm bites. Registry 202->205 Completed / 434 Queued. |
| `e4f0b03` | Foundation wave 30: 3 disjoint streams (all approved no-fix; mutation spot-checks bite). HXC-1302 wireguard preemption-safe teardown (pkg/wireguard/teardown.go: MeshCoordinator.TeardownPeers Stop+RemovePeer-all idempotent + SpotDrainHook(mc) cloudspot.Hooks.StopAdmission; tests w/ real curve25519 keys: add N -> teardown -> ListPeers==0, idempotent, FakeSource+Handler.Run drain removes peers; mutation skip-RemovePeer fails). HXC-1299 discovery 15-tier registry (pkg/discovery/tier_registry.go: TierRegistry.Register+SelectByTier(minTier) rank>=min via tierdef.Default() T1-T15 ordering, ordered exact-set; mutation >=->< fails TestSelectByTier_T8 got T1/T5 want T8/T12/T15). HXC-1291 device.Probe (pkg/device/probe.go + probe_{darwin,linux,other}.go: real Arch(GOARCH)/CPUCores(NumCPU)/MemoryBytes(sysctl hw.memsize / syscall.Sysinfo / MemStats fallback) + REAL os.Hostname-based ID, honest 0 for GPU/NPU/power; tests assert vs runtime oracle + real sysctl). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; B mutation bite + A/C reviewer-verified. |
| `1e88aec` | Foundation wave 29: mark 3 items Completed (HXC-1298/1296/1310) — handheld scheduling policy / device<->pb mapping (honest lossy) / per-tier security model. Mutation spot-checks confirm bites. Registry 199->202 Completed (crossed 200) / 437 Queued. |
| `441c7fe` | Foundation wave 29: 3 disjoint streams (all approved; A minor dead-helper + comment-lie fixed by me). HXC-1298 power/battery/thermal-aware handheld scheduling policy (pkg/scheduler/handheld.go HandheldPolicy Plugin reusing edgeaware battery/thermal/charging helpers: filter low-battery/over-thermal/unavailable/discharging-below-floor, score by battery/thermal/charging; absent-label assume-safe; 22 tests; mutation pct< -> pct> fails LowBatteryRejected). HXC-1296 device.Descriptor<->pb.Node round-trip mapping (NEW pkg/devicemap: DescriptorToNode/NodeToDescriptor over MAPPABLE fields; HONEST lossy -- round-trip identity over mapped subset + explicit test that device-only fields NPU/power/battery/trust are LOST (no false-recovery bluff); no import cycle; 7 tests). HXC-1310 per-tier security model enforcement (NEW pkg/tiersec reusing sandbox.Capability+edge+tierdef: ProfileForTier + Enforce(tier,op)->Decision over capability/data-class(inclusive)/network-egress CIDR allowlist via stdlib net; low-tier denied / high-tier allowed both directions; 24 tests; mutation data-class > -> >= fails inclusive-boundary). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; A+C mutations bite, B honest-loss reviewer-verified. |
| `634aa09` | Foundation wave 28: mark 3 items Completed (HXC-1311/1301/1303) — T1-T15 tier definitions / cloudspot PreemptionWatcher (verified) / inference Backend+Router (verified, dual-layer mutation). Registry 196->199 Completed / 440 Queued. |
| `d4829d1` | Foundation wave 28: HXC-1311 implemented + HXC-1301/1303 verified-already-done. HXC-1311 T1-T15 tier definitions (NEW pkg/tierdef: //go:embed tierdef.yaml with all 15 ascending tiers + min_requirements; LoadTiers parse+validate (rejects bad schema_version, missing/dup/out-of-order/wrong-count), Default(); TierDef.Meets(DeviceCaps) inclusive >= per-dimension (cpu/mem/gpu/vram/net); 73 test cases assert exact parsed T1/T15 values + per-dimension below-boundary false + exact-boundary true; mutation < -> <= fails TestMeets_ExactBoundary_True). HXC-1301 pkg/cloudspot PreemptionWatcher VERIFIED already complete (injectable PreemptionSource + 3-step graceful drain StopAdmission/Checkpoint/Deregister + deadline ForceStop + FakeSource + 9 mutation-killing tests). HXC-1303 pkg/inference provider-agnostic Backend interface + Router registry VERIFIED already complete (capability-aware Candidates/Route fallback + ReferenceBackend labeled output + ErrUnsupported; DUAL-LAYER capability correctness confirmed by me via mutation: neutering BOTH the Candidates filter AND the backend model gate fails TestRouterPicksCapabilityMatch/UnsupportedErrors -- defense-in-depth, not a bluff). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; 1311 boundary mutation + 1303 dual-layer mutation bite. |
| `5745f9f` | Foundation wave 27: mark 2 items Completed (HXC-1237/1278) — swim prod+sim transport parity (real protocol over deterministic SimNetwork) / property-based fuzzing (4 Go fuzz targets). Mutation spot-checks confirm bites. Registry 194->196 Completed / 443 Queued. |
| `656bec1` | Foundation wave 27: 2 disjoint streams (both approved no-fix; mutation spot-checks bite). HXC-1237 swim prod+sim transport parity (pkg/swim: minimal additive Config.Transport injection seam in NewProtocol -- nil => real UDP NewTransport as before, non-nil => injected MessageTransport; prod behavior unchanged. NEW simparity_test.go runs the REAL swim membership protocol over the deterministic SimNetwork: 3-node full-mesh converges (all see StateAlive, 12 msgs), 4-node determinism (same seed identical), seam-load-bearing test asserts sim addr 127.0.0.1:18200. mutation: ignore cfg.Transport -> node sees only itself, convergence FAILS). HXC-1278 property-based + coverage-guided fuzzing (4 native Go FuzzXxx targets, NEW files only): FuzzParseCoverProfile (no-panic + Covered<=Total + Aggregate in [0,100]), FuzzParseTraceParent (no-panic + String() round-trip stable + valid hex ids), FuzzApply (checkpoint_merge no-panic on arbitrary checkpoint bytes via real checksum/gob path), FuzzCRDTCheckpointRoundTrip (NewCRDTCheckpoint->RestoreCRDT Fingerprint-stable + corrupt-bytes no-panic). All assert falsifiable invariants on non-empty seed corpora, run under plain go test (no -fuzz needed); mutation: break TraceParent.String() round-trip FAILS. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; 2 mutation spot-checks bite. |
| `5707d6b` | Foundation wave 26: mark 3 items Completed (HXC-1268/1265/1236) — helix-snapshot CLI / capability sandbox+resource-limits+audit / HelixNetwork trait (prod+sim). Mutation spot-checks + real-binary verified. Registry 191->194 Completed / 445 Queued. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `9baae11` |
| **Timestamp** | 2026-06-02T01:00:08Z |

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
