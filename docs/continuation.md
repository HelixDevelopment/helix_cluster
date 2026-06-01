# Continuation Document

**Revision:** 87
**Last modified:** 2026-06-01T23:44:33Z
**Description:** Sacred invariant resumption document for Helix Cluster OS
**Authority:** Constitution §12.10
**Maintainer:** Operator + AI loop

---

## §0: How to use this document

Any CLI agent resuming work on this project MUST read this file first.

## §1: Recently Completed Work (last 10 commits)

| Commit | Message |
|--------|---------|
| `7c41c2f` | Foundation wave 26: 3 disjoint streams (all approved; A minor comment-lie fixed by me). HXC-1268 standalone cmd/helix-snapshot CLI (run() dispatch + create/restore/compare/list/delete wiring real snapshot.Manager via -dir; 11 tests w/ real temp-dir fs assertions + exit codes; verified by me running the REAL binary: create writes .golden, list shows it, compare rc=0, delete removes it). HXC-1265 capability sandbox + REAL resource-limit enforcement + audit (NEW pkg/sandbox: Capability deny-by-default grant model + Guard.Check (deny ungranted) + atomic.Int64 cumulative MaxOps/MaxBytes/MaxDuration enforcement -- the genuine gap vs pkg/wasm caps -- + MemAudit allow/deny log; in-process model, OS syscall isolation honestly out-of-scope not faked; mutation neuter-Has -> ungranted allowed FAILS). HXC-1236 HelixNetwork trait prod+sim (NEW pkg/helixnet: HelixNetwork interface + ProdNetwork real in-process channel fabric (bytes genuinely move A->B, deliver errors on unknown/closed dst) + SimNetwork deterministic via dst.Engine; zero pkg/swim edits -- that's 1237; mutation drop-delivery -> send/unknown-addr tests FAIL). ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green (only the known internal/advisory load-flake, passes isolated), dataplane ok, tmux/screen clean; 3 mutation spot-checks bite + helix-snapshot real-binary verified. |
| `02cd05a` | Foundation wave 25: mark 3 items Completed (HXC-1254/1255/1267) — parallel TestRunner / session test FSM / helix-test CLI (verified already complete). Mutation spot-checks confirm bites. Registry 188->191 Completed / 448 Queued. |
| `aba565b` | Foundation wave 25: 2 new test-orchestration pkgs + 1 verified-already-done. HXC-1254 parallel TestRunner (NEW pkg/testing/runner: Suite registration + bounded-worker-pool parallel execution + result collection/aggregate Report sorted deterministically; barrier-proof concurrency test (serial runner deadlocks under a ctx-deadline guard), atomic peak counter proves MaxConcurrency cap, result counts incl Err->Failed (defense-in-depth: both guards must be removed to break the want-3-got-4 bite — mutation-verified), Register dup/empty-name errors). HXC-1255 session test FSM (NEW pkg/testing/sessionfsm: Idle->Setup->Running->ChaosInject->Verify->Done + any-nonterminal->Failed; legal-transition table, IllegalTransitionError leaves state unchanged, per-state callbacks fire in order, History(); mutation guard-always-allow -> illegal transition test FAILS). HXC-1267 helix-test CLI VERIFIED already complete (cmd/helix-test dst/chaos/device/snapshot subcommands wired to real pkg/testing packages w/ 13 mutation-sensitive tests; device subcmd lists real T1-T8) — verified+marked, not rebuilt. ZERO new deps, ZERO edits to existing files. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok, tmux clean (W23 fix holds); mutation spot-checks: FSM guard bites, TestRunner Err->Failed bites (both guards), cap-peak<=2 + barrier verified by review. |
| `ace79d4` | Foundation wave 24: mark HXC-1162 Completed — E2E multi-node session CRDT convergence (5 replicas, arbitrary-order gossip, converge to identical Fingerprint + canonical truth; mutation-verified bite). Registry 187->188 Completed / 451 Queued. |
| `8e8cebb` | Foundation wave 24: HXC-1162 E2E multi-node session CRDT convergence (pkg/session/convergence_hxc1162_test.go, test-only, ZERO edits). Proves STRONG EVENTUAL CONSISTENCY beyond the existing 2-replica tests: 5 independent replicas each seeded with a DISJOINT partition of 100 ops, gossiped via seeded-random ARBITRARY-ORDER pairwise merges (dst = dst.Clone().Merge(src)) until 3-sweep quiescence; asserts (a) ALL 5 replicas reach an identical Fingerprint AND (b) that fingerprint equals a canonical truth replica that applied all 100 ops sequentially — over 20 seeds. Reuses existing genUpdates/replicaFrom/buildUpdate + CRDTSessionState Merge/Clone/Fingerprint. Builds on W22 pkg/checkpoint_merge: this is the convergence guarantee that makes coordinator-free session migration safe. Gate: build/vet/vet-integration clean, full -short -race green (zero failures this run), dataplane ok, tmux ls clean (W23 leak fix holds); INDEPENDENT MUTATION SPOT-CHECK: order-dependent equal-version pane tiebreak (paneWins 'return false') -> replicas DIVERGE -> 'CONVERGENCE FAILED replica 0 and 1 diverged' (the N-node convergence bite is genuinely real). ZERO new deps. |
| `06ca1a2` | Foundation wave 23: fix tmux-session test leak (CLAUDE-1/§7.1 test hygiene; PTY-exhaustion guard, W15/W22 lesson). Real-tmux-backed tests now leave ZERO leaked sessions. ROOT CAUSE was cmd/helix-session/main_test.go: TestRunServesRealRPC + TestRunGracefulShutdownStopsServing call the production run() (real tmux backend since W15) and CreateSession 2 real tmux sessions (server-assigned session-<nanos> id == tmux session name) with no cleanup -> leaked on every full-suite run. FIX: added killSession(t,id) helper t.Cleanup-ing 'tmux kill-session -t <id>' after each CreateSession. Also pkg/session/backends/package_test.go (W23 workflow): added t.Cleanup kills to TestTmuxBackend_CreateAndKill/Lifecycle (defended against mid-test-failure leaks). VERIFIED: full -short -race ./... then 'tmux ls' => no server running (zero leaks); real-tmux proofs intact (hxc1136 exit gate untouched). Test-only changes, ZERO production code modified. NOTE flakes: internal/advisory TestLockBlocksUntilAvailable + test/e2e TestClusterLifecycleSuite FAIL only under heavy concurrent full-suite -race load, PASS isolated (load-induced timing flakes, not regressions). |
| `639e479` | Foundation wave 22: mark 2 items Completed (HXC-1152/1160) — container-checkpoint migration CRDT merge / tmux full functional Attach (send-keys+capture-pane, real marker round-trip). Mutation spot-checks confirm bites. Registry 185->187 Completed / 452 Queued. |
| `1d79b5c` | Foundation wave 22: 2 disjoint streams (both approved no-fix; mutation spot-checks bite). HXC-1152 container-checkpoint migration CRDT merge (NEW pkg/checkpoint_merge: Apply(local,checkpoint) restores+merges incoming CRDT checkpoint into a clone of local via existing CRDTSessionState.Merge LWW, Migrate(source,local) models export-A->merge-B; NEW pkg/session/crdt_checkpoint.go v2 checkpoint helper avoids import cycle; convergence tests assert EXACT LWW winner values + commutativity-by-Fingerprint + idempotence + corrupt-checkpoint sentinel; mutation skip-merge -> LWW winner reverts to wrong value FAILS). HXC-1160 tmux full functional Attach (pkg/session/backends/tmux_attach.go + tmux.go Attach: replaced the dead orphaned-io.Pipe stream with a REAL stream — Write delivers keystrokes via tmux send-keys -l/Enter, Read returns REAL pane content via tmux capture-pane -p; marker round-trip test against real tmux with bounded 20x100ms poll, unique names + defer Kill, ZERO leaks; mutation neuter send-keys -> marker absent FAILS). ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; both mutation spot-checks bite; W22 tmux test verified zero-leak in isolation. NOTE pre-existing leak: pkg/session.Manager-based tests (manager.go:106 session-<nanos> naming, e.g. hxc1136) can leave tmux sessions — hygiene item, not introduced here. |
| `09d0044` | Foundation wave 21: mark 3 items Completed (HXC-1130/1249/1243) — screen+zellij session backends (real screen, zellij ErrUnsupported) / emergency-stop+auto-recovery / 8 network fault injectors (verified already complete). Mutation spot-check confirms emergency-stop bite. Registry 182->185 Completed / 454 Queued. |
| `15977c0` | Foundation wave 21: 2 impl streams + 1 verified-already-done. HXC-1130 screen+zellij session backends (pkg/session/backends/screen.go real exec of /usr/bin/screen: Create -dmS / List -ls parse / Kill -X quit / SendInput -X stuff / CaptureOutput -X hardcopy; REAL integration test creates+lists(sink-side proof)+kills a uniquely-named session w/ defer-Kill, ZERO leaked sessions verified; zellij.go returns typed ErrBackendUnavailable since zellij absent — no fake; orchestrator fixed the Attach stream to deliver Write as REAL keystrokes via SendInput instead of an orphaned io.Pipe). HXC-1249 emergency-stop + auto-recovery (pkg/testing/scenario/stop.go: StopController.EmergencyStop snapshots ActiveFaults + RestoreAll -> StopResult{FaultsRecovered,HaltLatencyMS,WithinBudget,RecoveredNames}; halt latency modelled as data (faults*perFaultCost), NO real sleep; AutoRecover asserts no faults remain; post-stop ActiveFaults()==0 + WithinBudget false-case are the bites). HXC-1243 8 network fault injectors VERIFIED already complete (NetworkPartition/PacketLoss/LatencyInjection + PacketReorder/PacketDuplication/BandwidthThrottle/AsymmetricPartition/DNSBlackhole, all with passing tests) — verified+marked, not rebuilt. ZERO new deps. Gate: build/vet/vet-integration clean, full -short -race green, dataplane ok; mutation spot-check: B skip-RestoreAll -> post-stop ActiveFaults!=0 FAILS (bite real); A real screen create/list/kill verified sink-side, zero leaks. |

## §2: Environment Snapshot

| Property | Value |
|----------|-------|
| **Branch** | `main` |
| **Commit** | `7c41c2f` |
| **Timestamp** | 2026-06-01T23:44:33Z |

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
