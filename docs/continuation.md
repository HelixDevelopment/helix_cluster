# Continuation Document

**Revision:** 92
**Last modified:** 2026-06-02T00:31:12Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `634aa09` | Foundation wave 28: mark 3 items Completed (HXC-1311/1301/1303) — T1-T15 tier definitions / cloudspot PreemptionWatcher (verified) / inference Backend+Router (verified, dual-layer mutation). Registry 196->199 Completed / 440 Queued. |
| `d4829d1` | Foundation wave 28: HXC-1311 implemented + HXC-1301/1303 verified-already-done. HXC-1311 T1-T15 tier definitions (NEW pkg/tierdef: //go:embed tierdef.yaml with all 15 ascending tiers + min_requirements; LoadTiers parse+validate (rejects bad schema_version, missing/dup/out-of-order/wrong-count), Default(); TierDef.Meets(DeviceCaps) inclusive >= per-dimension (cpu/mem/gpu/vram/net); 73 test cases assert exact parsed T1/T15 values + per-dimension below-boundary false + exact-boundary true; mutation < -> <= fails TestMeets_ExactBoundary_True). HXC-1301 pkg/cloudspot PreemptionWatcher VERIFIED already complete (injectable PreemptionSource + 3-step graceful drain StopAdmission/Checkpoint/Deregister + deadline ForceStop + FakeSource + 9 mutation-killing tests). HXC-1303 pkg/inference provider-agnostic Backend interface + Router registry VERIFIED already complete (capability-aware Candidates/Route fallback + ReferenceBackend labeled output + ErrUnsupported; DUAL-LAYER capability correctness confirmed by me via mutation: neutering BOTH the Candidates filter AND the backend model gate fails TestRouterPicksCapabilityMatch/UnsupportedErrors -- defense-in-depth, not a bluff). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; 1311 boundary mutation + 1303 dual-layer mutation bite. |
| `5745f9f` | Foundation wave 27: mark 2 items Completed (HXC-1237/1278) — swim prod+sim transport parity (real protocol over deterministic SimNetwork) / property-based fuzzing (4 Go fuzz targets). Mutation spot-checks confirm bites. Registry 194->196 Completed / 443 Queued. |
| `656bec1` | Foundation wave 27: 2 disjoint streams (both approved no-fix; mutation spot-checks bite). HXC-1237 swim prod+sim transport parity (pkg/swim: minimal additive Config.Transport injection seam in NewProtocol -- nil => real UDP NewTransport as before, non-nil => injected MessageTransport; prod behavior unchanged. NEW simparity_test.go runs the REAL swim membership protocol over the deterministic SimNetwork: 3-node full-mesh converges (all see StateAlive, 12 msgs), 4-node determinism (same seed identical), seam-load-bearing test asserts sim addr 127.0.0.1:18200. mutation: ignore cfg.Transport -> node sees only itself, convergence FAILS). HXC-1278 property-based + coverage-guided fuzzing (4 native Go FuzzXxx targets, NEW files only): FuzzParseCoverProfile (no-panic + Covered<=Total + Aggregate in [0,100]), FuzzParseTraceParent (no-panic + String() round-trip stable + valid hex ids), FuzzApply (checkpoint_merge no-panic on arbitrary checkpoint bytes via real checksum/gob path), FuzzCRDTCheckpointRoundTrip (NewCRDTCheckpoint->RestoreCRDT Fingerprint-stable + corrupt-bytes no-panic). All assert falsifiable invariants on non-empty seed corpora, run under plain go test (no -fuzz needed); mutation: break TraceParent.String() round-trip FAILS. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean; 2 mutation spot-checks bite. |
| `5707d6b` | Foundation wave 26: mark 3 items Completed (HXC-1268/1265/1236) — helix-snapshot CLI / capability sandbox+resource-limits+audit / HelixNetwork trait (prod+sim). Mutation spot-checks + real-binary verified. Registry 191->194 Completed / 445 Queued. |
| `7c41c2f` | Foundation wave 26: 3 disjoint streams (all approved; A minor comment-lie fixed by me). HXC-1268 standalone cmd/helix-snapshot CLI (run() dispatch + create/restore/compare/list/delete wiring real snapshot.Manager via -dir; 11 tests w/ real temp-dir fs assertions + exit codes; verified by me running the REAL binary: create writes .golden, list shows it, compare rc=0, delete removes it). HXC-1265 capability sandbox + REAL resource-limit enforcement + audit (NEW pkg/sandbox: Capability deny-by-default grant model + Guard.Check (deny ungranted) + atomic.Int64 cumulative MaxOps/MaxBytes/MaxDuration enforcement -- the genuine gap vs pkg/wasm caps -- + MemAudit allow/deny log; in-process model, OS syscall isolation honestly out-of-scope not faked; mutation neuter-Has -> ungranted allowed FAILS). HXC-1236 HelixNetwork trait prod+sim (NEW pkg/helixnet: HelixNetwork interface + ProdNetwork real in-process channel fabric (bytes genuinely move A->B, deliver errors on unknown/closed dst) + SimNetwork deterministic via dst.Engine; zero pkg/swim edits -- that's 1237; mutation drop-delivery -> send/unknown-addr tests FAIL). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green (only the known internal/advisory load-flake, passes isolated), dataplane ok, tmux/screen clean; 3 mutation spot-checks bite + helix-snapshot real-binary verified. |
| `02cd05a` | Foundation wave 25: mark 3 items Completed (HXC-1254/1255/1267) — parallel TestRunner / session test FSM / helix-test CLI (verified already complete). Mutation spot-checks confirm bites. Registry 188->191 Completed / 448 Queued. |
| `aba565b` | Foundation wave 25: 2 new test-orchestration pkgs + 1 verified-already-done. HXC-1254 parallel TestRunner (NEW pkg/testing/runner: Suite registration + bounded-worker-pool parallel execution + result collection/aggregate Report sorted deterministically; barrier-proof concurrency test (serial runner deadlocks under a ctx-deadline guard), atomic peak counter proves MaxConcurrency cap, result counts incl Err->Failed (defense-in-depth: both guards must be removed to break the want-3-got-4 bite — mutation-verified), Register dup/empty-name errors). HXC-1255 session test FSM (NEW pkg/testing/sessionfsm: Idle->Setup->Running->ChaosInject->Verify->Done + any-nonterminal->Failed; legal-transition table, IllegalTransitionError leaves state unchanged, per-state callbacks fire in order, History(); mutation guard-always-allow -> illegal transition test FAILS). HXC-1267 helix-test CLI VERIFIED already complete (cmd/helix-test dst/chaos/device/snapshot subcommands wired to real pkg/testing packages w/ 13 mutation-sensitive tests; device subcmd lists real T1-T8) — verified+marked, not rebuilt. ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean (W23 fix holds); mutation spot-checks: FSM guard bites, TestRunner Err->Failed bites (both guards), cap-peak<=2 + barrier verified by review. |
| `ace79d4` | Foundation wave 24: mark HXC-1162 Completed — E2E multi-node session CRDT convergence (5 replicas, arbitrary-order gossip, converge to identical Fingerprint + canonical truth; mutation-verified bite). Registry 187->188 Completed / 451 Queued. |
| `8e8cebb` | Foundation wave 24: HXC-1162 E2E multi-node session CRDT convergence (pkg/session/convergence_hxc1162_test.go, test-only, ZERO edits). Proves STRONG EVENTUAL CONSISTENCY beyond the existing 2-replica tests: 5 independent replicas each seeded with a DISJOINT partition of 100 ops, gossiped via seeded-random ARBITRARY-ORDER pairwise merges (dst = dst.Clone().Merge(src)) until 3-sweep quiescence; asserts (a) ALL 5 replicas reach an identical Fingerprint AND (b) that fingerprint equals a canonical truth replica that applied all 100 ops sequentially — over 20 seeds. Reuses existing genUpdates/replicaFrom/buildUpdate + CRDTSessionState Merge/Clone/Fingerprint. Builds on W22 pkg/checkpoint_merge: this is the convergence guarantee that makes coordinator-free session migration safe. Gate: build/vet/vet-integration clean, full -short -race green (zero failures this run), dataplane ok, tmux ls clean (W23 leak fix holds); INDEPENDENT MUTATION SPOT-CHECK: order-dependent equal-version pane tiebreak (paneWins 'return false') -> replicas DIVERGE -> 'CONVERGENCE FAILED replica 0 and 1 diverged' (the N-node convergence bite is genuinely real). ZERO new deps. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `634aa09` |
| **Timestamp** | 2026-06-02T00:31:12Z |

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
