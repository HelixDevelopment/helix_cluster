# Continuation Document

**Revision:** 103
**Last modified:** 2026-06-02T01:37:45Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `0c8b4ca` | Foundation wave 34: HXC-1365 built + HXC-1342/1353 verified. HXC-1365 latency-aware spot/preemptible scheduler scoring (NEW pkg/scheduler/latency_spot.go LatencySpotPolicy Plugin: Score = base - latency_ms*k - preemptible-penalty(latency-sensitive jobs) + spot-bonus(batch); optional MaxLatencyMs Filter; absent-label assume-safe; 20 tests; mutation flip penalty-sign -> higher-latency scores higher FAILS LowerLatencyScoresHigher, mutation remove preemptible-penalty -> sensitive-job ordering FAILS). HXC-1342 federation API aggregation w/ partial failure VERIFIED (pkg/federation/aggregate.go CellError/Aggregate.Errors/AllCells; mutation drop-error-recording fails TestAllCells_PartialFailure). HXC-1353 WireGuard zero-downtime key rotation w/ overlap window VERIFIED (pkg/wireguard/keyrotation.go KeyOverlap grace + ActiveKeys; mutation drop-retain fails TestActiveKeysGraceWindow). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; 1365 latency-sign + 1342 + 1353 mutations bite. |
| `6b9135e` | Foundation wave 33: mark 3 items Completed (HXC-1339/1373/1343) — Vector Clock causality / split-brain partition classifier / phi-accrual failure detector (verified). Mutation spot-checks confirm bites. Registry 216->219 Completed / 420 Queued. |
| `37c2a9a` | Foundation wave 33: 2 builds + 1 verified. HXC-1339 Vector Clock causality (pkg/crdt/vectorclock.go: VectorClock map[ReplicaID]uint64 + Increment/Merge(per-replica max)/Compare(Before/After/Equal/CONCURRENT)/HappensBefore; 23 tests; Concurrent case (disjoint advances both flags) is the bite — a single-flag Compare returns Before and fails). HXC-1373 split-brain prevention + partition classification (NEW pkg/splitbrain: Classify(reachable,total,witness)->(Partition,Action) via 2*reachable vs total — HasQuorum/Tie/Minority/TotalIsolation + Decide leader-step-down; 22 subtests; 2-of-5=Minority NOT Tie (integer-division trap caught), 2-of-4=Tie, witness flips tie action; mutation > -> >= fails 2-of-4 tie). HXC-1343 phi-accrual failure detector VERIFIED already complete (pkg/swim/phi_accrual.go sliding-window mean/variance + -log10 tail prob + Suspect; mutation Phi-return-0 fails monotonic-rise test). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; B quorum-tie + A Concurrent + 1343 phi mutations bite. |
| `752a2da` | Foundation wave 32: mark 8 items Completed (HXC-1334 HLC drift-clamp + verified HXC-1335/1336/1337/1338/1340/1341/1304 — CRDT/HLC/Merkle/Cell-federation/inference distributed primitives). Mutation spot-checks confirm bites. Registry 208->216 Completed / 423 Queued. |
| `0346e1c` | Foundation wave 32: HXC-1334 HLC drift clamp implemented + 7 phase-6/5 items verified-already-done. HXC-1334 (pkg/hlc): added DefaultMaxDrift const + NewWithMaxDrift + a drift-clamp block in Clock.Update — a remote physical timestamp exceeding local wall-clock by >maxDrift is clamped to local+maxDrift (prevents far-future-remote clock poisoning, HLC's classic failure mode); Now() untouched, existing Update in-drift behaviour + 14 existing tests intact, 6 new tests; mutation neuter-clamp -> far-future remote (1.5e9) > ceiling (1.05e9) FAILS. VERIFIED already-complete (build+test+mutation-confirmed suites, NOT rebuilt): HXC-1335 pkg/crdt GCounter, HXC-1336 LWWRegister (timestamp+replica LWW tiebreak), HXC-1337 ORSet (mutation ignore-tombstones fails AddRemove tests), HXC-1338 pkg/crdt/merkle anti-entropy Diff (mutation Diff-nil fails), HXC-1340 pkg/federation Cell model+FSM (mutation always-legal-transition fails RejectsIllegalJumps), HXC-1341 CellRegistry Register/Deregister/Lookup/List/Advance thread-safe, HXC-1304 pkg/inference LocalBackend wrapping internal/llm. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; drift-clamp + 3 batch mutations bite. |
| `caabf37` | Foundation wave 31: mark 3 items Completed (HXC-1290/1292/1297) — device Tier T1-T15 + AssignTier registry classifier / tier-aware+power-aware scheduler Plugin (verified). Mutation spot-checks confirm bites. Registry 205->208 Completed / 431 Queued. |
| `9baae11` | Foundation wave 31: HXC-1290+1292 combined + HXC-1297 verified-done. HXC-1290 (device universal Descriptor w/ 15 tiers) + HXC-1292 (device.AssignTier decision tree) — ONE deliverable: extended pkg/device.Tier enum additively to T1-T15 (+String cases; existing TierUnknown/T1-T8/ClassifyTier/EffectiveCompute byte-for-byte UNCHANGED, verified) + NEW pkg/device/assign_tier.go AssignTier(d,reg *tierdef.Registry) Tier mapping Descriptor->tierdef.DeviceCaps and iterating HIGH->LOW returning the highest tier whose tierdef.Meets passes (networkUnknown=MaxInt sentinel so missing net field never disqualifies); 46 tests incl exact-per-tier T1/T8/T12/T15, exhaustive boundary sweep, monotonic property; mutation low->high iteration -> T15-spec wrongly returns T1 FAILS. Import device->tierdef (no cycle). HXC-1297 pkg/scheduler tier-aware+power-aware Plugin VERIFIED already complete (tier_power.go min-tier filter + watts/perf-per-watt/overshoot score + edgeaware.go + handheld.go, 67+ mutation-guarded tests; mutation tier-min < -> > fails TestTierPowerFilterRejectsBelowMinTier). ZERO new deps. Gate: build/vet/vet-integration ./... clean (tier.go edit no regression), full -short -race green, dataplane ok, tmux clean; iteration-order + tier-min mutations bite. |
| `9327930` | Foundation wave 30: mark 3 items Completed (HXC-1302/1299/1291) — wireguard spot-drain teardown / discovery 15-tier registry / device.Probe real host detection. Mutation spot-checks confirm bites. Registry 202->205 Completed / 434 Queued. |
| `e4f0b03` | Foundation wave 30: 3 disjoint streams (all approved no-fix; mutation spot-checks bite). HXC-1302 wireguard preemption-safe teardown (pkg/wireguard/teardown.go: MeshCoordinator.TeardownPeers Stop+RemovePeer-all idempotent + SpotDrainHook(mc) cloudspot.Hooks.StopAdmission; tests w/ real curve25519 keys: add N -> teardown -> ListPeers==0, idempotent, FakeSource+Handler.Run drain removes peers; mutation skip-RemovePeer fails). HXC-1299 discovery 15-tier registry (pkg/discovery/tier_registry.go: TierRegistry.Register+SelectByTier(minTier) rank>=min via tierdef.Default() T1-T15 ordering, ordered exact-set; mutation >=->< fails TestSelectByTier_T8 got T1/T5 want T8/T12/T15). HXC-1291 device.Probe (pkg/device/probe.go + probe_{darwin,linux,other}.go: real Arch(GOARCH)/CPUCores(NumCPU)/MemoryBytes(sysctl hw.memsize / syscall.Sysinfo / MemStats fallback) + REAL os.Hostname-based ID, honest 0 for GPU/NPU/power; tests assert vs runtime oracle + real sysctl). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; B mutation bite + A/C reviewer-verified. |
| `1e88aec` | Foundation wave 29: mark 3 items Completed (HXC-1298/1296/1310) — handheld scheduling policy / device<->pb mapping (honest lossy) / per-tier security model. Mutation spot-checks confirm bites. Registry 199->202 Completed (crossed 200) / 437 Queued. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `0c8b4ca` |
| **Timestamp** | 2026-06-02T01:37:45Z |

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
