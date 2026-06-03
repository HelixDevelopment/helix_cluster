# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Wave 80 — Avro event schemas + schema-validated routing (HXC-1119; careful single-threaded TDD build, conductor-gated build/vet/-race + 2 load-bearing mutation bites):**
  - `pkg/events` — a stdlib-only Avro-subset codec: flat primitive records, spec-faithful binary encoding (zig-zag varint int/long, length-prefixed string/bytes, LE-IEEE-754 double), **single-object encoding** framing (`0xC3 0x01` + 8-byte CRC-64-AVRO fingerprint) so each payload is self-describing, a `SchemaRegistry` giving **schema-validated routing** (a payload whose writer fingerprint is unregistered is rejected, never mis-decoded), and **writer→reader schema resolution** for backward+forward-compatible evolution (reader-only fields fill from defaults; writer-only fields skip; a reader-only field without a default is an unresolvable-pair error). `NodeEventSchemaV1→V2` adds a defaulted `action` field; proven both directions — a V1 consumer reads a V2 payload (skips `action`) and a V2 consumer reads a V1 payload (synthesizes `action` from default) — plus concurrent round-trip under `-race`. Live NATS-backend wiring + the other event-type schemas tracked HXC-1626 (HXC-1119).
- **Wave 79 — 2 bounded host-closeable fixes (conductor-gated build/vet/-race + load-bearing mutation bite):**
  - `pkg/multiraft` — the durability persist-error is now observable through a PUBLIC seam: a registerable `OnPersistError` hook (+ `LastPersistError` accessor) fires with the shard id + error from the same Ready-loop path that parks the un-persisted `Ready` and skips `Advance` (HXC-917); previously the fault was only `log.Printf`'d. Nil hook = prior behavior (HXC-927).
  - `pkg/cloudspot` — the GCP and Azure preemption pollers now honor the injected `WithClock` when computing `PreemptionNotice.Deadline` (both used `time.Now()` directly, silently dropping the clock; only the AWS poller honored it), making all three providers deterministically testable with a fake clock (HXC-1616).
- **Wave 78 — 4 disjoint host-closeable streams + a cross-platform SELinux fix (all conductor-gated build/vet/-race + load-bearing mutation bite, not agent self-reports):**
  - `internal/console` — fixture-driven device-classification hardening: `testdata/` `/proc`+`device-tree` trees for 6 device classes (PS4/PS5/x86-server/RK3588/Raspberry-Pi/generic) with exact-label table assertions; mutating the PS5 zen-2 SoC-match row flips `ps5_zen2` and fails the test. Non-console classes still classify `Unknown` today (positive labels tracked HXC-1624) (HXC-1158).
  - `scripts/cross-compile-agent.sh` + `Makefile` `cross-agent` target — reproducible ARM64 cross-compilation of `cmd/helix-agent`: emits linux/arm64 (ELF aarch64), linux/armv7 (ELF 32-bit ARM EABI5) and android/arm64 artifacts with a build-UUID via `-ldflags` and a `--version` print path; `crosscompile_test.go` asserts `elf.EM_AARCH64` via `debug/elf`. CGO-enabled cross (no `zig cc`/`aarch64-gnu-gcc` on host) and on-real-SBC execution are honest SKIPs (HXC-1622/HXC-1623) (HXC-1167).
  - `docs/architecture/PHASE_8C_INTEGRATION.md` — code-grounded Phase-8C integration map (attestation→scheduler, e2ee→orchestrator, marketplace) citing real `pkg/resources` symbols; scheduler attestation-wiring labeled PLANNED (verified: no `SetAttested` callers) (HXC-1614).
  - `docs/PHASE_8C_EXIT_GATE_EVIDENCE.md` — CLAUDE-1 exit-gate evidence matrix (PROVEN 9 / PARTIAL 6 / NOT-YET 7); PROVEN rows cite real `pkg/metering`+`pkg/auditproof` tests, NOT-YET rows cite Queued tickets (HXC-1613).
  - **`containers` submodule (`pkg/crossbuild`)** — SELinux bind-mount relabel made conditional + cross-platform: `:Z` private / `:z` shared **only when SELinux is active**, empty on macOS / non-SELinux Linux (was hardcoded `:Z`, breaking podman-machine/Docker-Desktop & shared-volume reuse); injectable detection, panic-recovered fail-safe, stress+chaos coverage; CLAUDE-2 §11.4.81 (HXC-1621, submodule `1598f28`).
- **Waves 76+77 — two concurrent subagent waves, 8 disjoint host-closeable streams (all independently gated build/vet/-race + load-bearing mutation bite per stream):**
  - `internal/llm` — advisory engine (MIGRATION/SCALING/CONFIG/ALERT/OPTIMIZATION) carrying rationale + confidence + risk, `PENDING` persistence, a non-LOW-risk approval gate (never auto-applied without explicit approval), and a mandatory `LLMsVerifier` that rejects hallucinated/unsafe actions; the LLM is an injectable `Advisor` so the gate is deterministic and host-closeable (HXC-1126).
  - `pkg/benchmark` — micro-benchmarker producing a real, repeatable normalized on-host CPU score (+ typed GPU TFLOPS / NPU TOPS seams), repeatability asserted within a coefficient-of-variation tolerance band (HXC-1308).
  - `pkg/security/e2ee` (**security submodule**) — gzip-before-encrypt payload compression matching the Chutes envelope framing: measurable wire-size reduction, byte-exact decrypt+gunzip round-trip, only applied when it actually shrinks (HXC-1564).
  - `cmd/dst-sim` — 1000-seed deterministic-simulation gate composing `pkg/porcupine`; exits non-zero naming the offending seed on a linearizability violation (ran 1000 seeds exit 0, deterministic across reruns) (HXC-915).
  - `docs/MVP_ARCHITECTURE.md` — living seven-layer architecture + service-communication Mermaid grounded in real packages, with a Go drift-validator that fails the build if the doc names a path that no longer exists; docs_chain-tracked (HXC-1145).
  - `pkg/gpu` — thread-safe `ProviderAdapter` registration hooks (`Register`/`List`/`Get`/`Lookup`) exposing a pool-facing listing, duplicate-name guarded, race-free under `-race` (HXC-1533).
  - `pkg/cloudspot` — real AWS IMDSv2 (token `PUT` → authenticated `GET` spot instance-action), Azure scheduled-events, and GCP preemption pollers behind `SignalSource`, with `drain→checkpoint→upload` via an injectable sink; proven against `httptest` fixtures replaying the genuine wire protocol (live cloud deferred, HXC-1617) (HXC-1328).
  - `pkg/tierdetect` — real cross-platform host-capability detection (darwin `sysctl`/arch + linux `/dev/kvm`,`/proc`,`binfmt` behind one build-tagged interface) gating tier requests pre-provision with a typed `MissingCapabilityError` (HXC-1230).
- **Wave 75 — provider hardening, security & boot readiness (5 items, all independently gated):**
  - `pkg/provider/runpod` — `Provision` no longer holds the mutex across the `ColdProvision` network RPC (concurrent cold provisions overlap; `reserved` counter preserves capacity), and `EndpointOf(id)` surfaces the reachable worker endpoint (HXC-919).
  - `pkg/marketplaceadapter` (**security**) — `buildSDL` YAML-encodes caller-controlled fields to contain manifest injection, and the Akash reputation gate is no longer bypassable by a zero-value adapter (HXC-918).
  - `pkg/provider/chutes` — `parseRetryAfter` HTTP-date branch + past-date clamp now covered by tests (HXC-920).
  - `internal/console` — `BootCoordinator` wired into the real `Registrar` via a `ReadyGate` so node registration blocks on boot readiness (HXC-1147).
  - `docs/guides/phase_02_architecture.md` — Phase-02 architecture guide + Mermaid diagram, docs_chain-tracked (HXC-1164).
- **Wave 74 — accelerator probing, power-aware edge & raft durability (5 items, all independently gated build/vet/-race + load-bearing mutation bite):**
  - `pkg/resources` accelerator reporting: `GPUInfo.TFLOPS` + `Accelerators{NPUTops,FPGALogicElements,QPUPresent}`, real per-OS probe (darwin Apple-GPU TFLOPS from `system_profiler`, linux sysfs PCI catalog) + operator override-label fallback (HXC-1300).
  - `pkg/powergater` — charging-gated `CanAcceptWork() (bool, reason)` work-acceptance guard with real darwin `pmset` / linux sysfs `PowerSource` (HXC-1175).
  - `pkg/edgeheartbeat` — battery/charging/thermal/network heartbeat + churn-timeout collector over a real loopback transport, real per-OS `TelemetrySource` (HXC-1185).
  - `pkg/multiraft` **durability fix**: a `ShardStorage` persistence error now **parks** the un-persisted `Ready` and skips `Advance` (no false "durably handled"), instead of swallowing the error (HXC-917).
  - `docs/NODE_PROVISIONING_BOUNDARY.md` — normative no-jailbreak/no-root/no-unlock provisioning boundary, registered in docs_chain (HXC-1146).
- **Foundation distributed-systems library (`pkg/`)** — a large, growing pure-Go, deterministic, mutation-tested package set. Recent additions include:
  - Consensus/coordination: `voting`, `failconfirm`, `heartbeatcoalescer`, `splitbrainalert`.
  - Replication/state: `mvcc` (B-tree time-travel store), `antientropy` (hinted-handoff + read-repair + Merkle diff), `watchmanager` (synced/unsynced/victim watch groups), `crdt`/`merkle`.
  - Scheduling/placement: `constraints` (Pacemaker 4-type), `preempt` (value-multiplier), `priorityqueue` (multifactor aging), `workclaim` (SKIP-LOCKED), `admissioncontrol` (N+K reserve), `budgetcap`, `qos`, `suitability`, `ewmarank`, `fallbackchain`.
  - GPU/resource & cost: `costsched`, `latencysched`, `healthmonitor`, `gpuattest` (attestation crypto: challenge/response, proof-of-GPU-work, O(1) spot-check, device-sealing), `attestadmit`, `quantization`, `local` (TCO), `pool`, `burst`.
  - Messaging/flow & edge: `flowcontrol` (K8s APF), `idempotent` (exactly-once), `rebalance` (cooperative-sticky), `informer` (list-watch cache), `redundantexec` (BOINC trust), `edgeregistry`, `edgeverify`, `slotmigration`, `hashslot`, `healthprobe`.
  - Testing/simulation: `timefault` (clock skew/freeze/monotonic injectors), BUGGIFY in `testing/dst`, `phase7matrix` (gap-matrix verifier).
  - This session (34 pure-Go units):
    - `bursthysteresis` — `BurstController` with a MONITOR→SPILL→RECOVER two-threshold hysteresis dead-band (HXC-1504).
    - `providerchain` — ordered multi-tier provider fallback cascade with retriable/terminal error classification and injected-clock failover SLA (HXC-1508).
    - `gpucatalog` — `SUPPORTED_GPUS` compute-multiplier catalog, `LookupMultiplier`, and attestation-aware `Score` (attested above un-attested) (HXC-1592).
    - `gravaladmit` — HMAC-SHA256 GraVal challenge-response provider admission gate with single-use nonce and `graval.verified` label (HXC-1523).
    - `modelrouter` — strategy-to-default-model resolution (latency/throughput/quality/cost) with a typed unknown-strategy error (HXC-1430).
    - `gravalverify` — GraVal GPU attestation with a VRAM-ratio `>= 0.95` gate and a concurrent, race-clean `BatchVerify` pass-rate KPI (HXC-1435, HXC-1436).
    - `gepetto` — HelixGepetto dual-resource local-vs-Chutes arbitration with a monotonic reserve schedule and a strict `>0.80` high-water anti-starvation zero-capacity cut-off (HXC-1446).
    - `chutesaccount` — Chutes API model-list (TEE/price fields) and `/users/me` account-balance client over HTTP with `Authorization: Bearer` (HXC-1429).
    - `cmd/burst-controller` — utilization-driven binary driving `bursthysteresis`; emits a "burst allocation request" on SPILL and a RECOVER line, runID-stamped (HXC-1531).
    - `tieredcache` — hot(memory)/warm(NVMe)/cold(SSD) tiered cache with injected backends + injected clock, idle-demotion + promotion, and per-tier hit stats (HXC-1418).
    - `smartrouter` — intelligent ModelRouter (latency/throughput/cost/TEE/balanced) that always excludes unhealthy models, with a typed no-eligible-model error (HXC-1539).
    - `epochresolve` — config-epoch failover slot-ownership conflict resolution (highest-epoch wins, deterministic lexicographic equal-epoch tiebreak, order-independent convergence) (HXC-1397).
    - `balancemonitor` — USD account-balance monitor with a strict low-balance floor warning, over HTTP with `Authorization: Bearer` (HXC-1512).
    - `scan` — Oracle-SCAN-style stable virtual client endpoint: least-loaded healthy-backend routing with a membership-stable name and injected clock (HXC-1403).
    - `llmfailover` — error-classified LLM failover taxonomy (deterministic per-class fallback paths) plus a sandbox scheduling-hint contract (HXC-1600).
    - `llmadapter` — Claude Messages / OpenAI Responses-API request and response shape adapters to OpenAI chat-completions, preserving tool-call order (HXC-1599).
    - `deltacrdt` — standalone delta-state CRDTs (G-counter / PN-counter / observed-remove OR-set / LWW-map) with delta-mutators and a convergent (commutative/associative/idempotent) merge (HXC-1409).
    - `gputopo` — topology-aware NUMA/NVLink GPU placement scoring that prefers NVLink-connected GPU pairs and falls back to PCIe (HXC-1406).
    - `fsresidency` — filesystem residency challenge: per-byte-range `ReadAt` + SHA-256 verification proving a model file is really present on disk; truncated/missing/mismatch all fail (HXC-1572).
    - `economics` — `RewardDistributor` multi-token distribution with treasury/reinvest splits and a conservation invariant (`sum(ledger)+treasury+reinvest == total`), plus `GetParticipantROI` (ROI% + break-even days, typed unknown-participant error) (HXC-1460, HXC-1461).
    - `revenueopt` — `RevenueOptimizer.OptimizeAllocation` greedy GPU→marketplace assignment maximising expected revenue, with a TEE→Chutes revenue bias; self-contained types (HXC-1456).
    - `exportcontrol` — export-control / country-code KYC gate: a controlled GPU (`H100`/`A100`) from a Tier-3 (embargoed) or unknown jurisdiction is rejected with `ErrExportDenied`; Tier-1 is allowed (HXC-1482).
    - `devicecatalog` — machine-readable 64-device taxonomy with case-insensitive substring `Lookup` resolving a discovered CPU model (e.g. `Rockchip RK3588`) to its tier / trust-level / compute-class (HXC-1331).
    - `compliancedoc` — EU AI Act compliance-documentation pipeline: generates a model card + technical-documentation artifact from attestation-log records, with a SHA-256 provenance hash bound to the actual record content (HXC-1481).
    - `hybridkex` — hybrid post-quantum key exchange combining X25519 (`crypto/ecdh`) and ML-KEM-768 (`crypto/mlkem`) shared secrets via HKDF-SHA256 (group `X25519MLKEM768`); a tampered ML-KEM ciphertext yields a non-matching key (HXC-1476).
    - `carbonsched` — carbon-aware job placement: chooses the lowest-carbon-intensity latency-eligible region (deterministic name tiebreak) with per-job kWh / gCO2 metering (HXC-1480).
    - `gpuattest` (additive `multigpu.go`) — multi-GPU node enumeration: an N-GPU node descriptor yields N distinct, independently-verifiable per-device attestation proofs keyed by GPU UUID; rejects duplicate/empty descriptors (HXC-1573).
    - `workloadrouter` — `UnifiedManager.RouteWorkload`: concurrent per-marketplace price probes + a weighted composite score (inverse price/latency, health) with a TEE multiplier that re-ranks confidential offers when the workload requires a TEE (HXC-1455).
    - `metrics` (additive `tiermetrics.go`) — `TierMetrics` exposes `gpu_tier_utilization`, `gpu_cost_per_hour`, and `provider_health` Prometheus-text series with deterministic ordering and concurrent-safe setters (HXC-1535).
    - `cmd/gpu-pool-manager` — GPU pool manager HTTP front-end: `POST /allocate` returns the tier-correct (lowest-tier healthy, name tiebreak) provider as JSON (`503` when none healthy) and `GET /providers` lists providers with health; httptest-gated, self-contained (HXC-1530).
    - `marketplaceadapter` — `MarketplaceAdapter` interface (`Name`/`GetCurrentPricing`/`SubmitWork`) with a `Name()`-keyed dispatch `Registry` and a `ChutesAdapter` over HTTP (httptest fixture, non-2xx rejected) (HXC-1454).
    - `cmd/e2ee-proxy` — transparent ML-KEM-768 (`crypto/mlkem`) + AES-256-GCM encrypting proxy: `EncryptingTransport` seals request bodies and `DecryptingHandler` opens them, so the on-wire payload between proxy and upstream is ciphertext-only while the client receives the correct plaintext; tampered ciphertext is rejected (HXC-1532).
    - `e2eebench` — `MeasureHandshakeLatency` benchmarks the `hybridkex` full handshake (median + P95), proving median < 1ms on host and enforcing per-iteration key agreement (`ErrKeyDisagreement`) (HXC-1556).
    - `archlint` + `docs/ARCHITECTURE.md` — the hardened L0–L7 architecture diagram and component map, mechanically enforced: `archlint` parses the component-map table and fails the build if any documented component maps to a package path that does not exist on disk (HXC-1424).
  - Wave 67 (5 units, subagent-driven, 5 parallel streams in disjoint packages):
    - `multiraft` — `MultiRaftManager` partitioning cluster state into independent per-shard `go.etcd.io/raft/v3` groups, each electing its own leader and committing independently via per-shard `Propose` routing over an in-process `RaftTransport`; write throughput scales with shard count and a shard re-elects on leader loss while others keep committing (HXC-1387).
    - `stonith` — STONITH fencing: a `FencingAgent` interface with IPMI / AWS-EC2 / Azure-ARM / SBD-shared-disk / NoOp drivers and a `MultiLevelFencer` multi-level fallback, with sink-side fence confirmation; the IPMI real-binary path is an honest SKIP-with-reason when `ipmitool` is absent (HXC-1401).
    - `deviceplugin` — Nomad/K8s-style gRPC device-plugin / GRES fingerprinting framework over real gRPC: plugins register full device fingerprints, the registry parses GRES descriptors, atomically applies fingerprints, allocates by request, and rejects oversubscription (HXC-1407).
    - `internal/gpu` (additive `manager_helixpow.go`) — dual-workload capacity reservation: `ReserveForHelixPoW(fraction)` + `ChutesCapacity()` reporting only the non-reserved remainder, with a strict `>0.80` Helix-load Gepetto starvation guard that zeroes Chutes capacity (HXC-1447).
    - `chutes` (additive `config.go`) — `ChutesMinerConfig` and `ValidatorConfig` deployment descriptors with `Validate()` invariants (non-empty IDs/hotkeys, `GPUCount>0`, non-negative cost, positive cache size, TEE consistency) (HXC-1439).
  - Wave 68 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 69):
    - `dst` — standalone FoundationDB-style deterministic simulation harness: single-threaded seeded-RNG event loop with injectable network/disk/logical-clock; same seed => byte-for-byte identical trace + exact failure replay; 1000+ seeded sims (HXC-1419).
    - `porcupine` — self-contained WGL linearizability checker + concurrent history recorder: PASS on linearizable, FAIL (with violating op) on a seeded non-linearizable history (HXC-1421).
    - `kraft` — KRaft-style self-managed Raft metadata quorum (`go.etcd.io/raft/v3`, no ZooKeeper): replicated `CreateTopic`/`ListTopics` + partition-leader assignment, controller election + controller-loss failover (HXC-1417).
    - `internal/chaos` — fault-injection library (PodKill/NetworkPartition/DiskStall/ClockSkew) + canary-guarded runner with automatic rollback on health<SLO; deterministic via injected clock + seeded selection (HXC-1422).
    - `multiraft` (additive `lease.go`) — `LeaseTracker` CockroachDB-style leaseholder local reads (no Raft round-trip), follower routing via `RaftTransport.SendRead`, lease-expiry re-route against an injected clock (HXC-1389).
  - Wave 69 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 68):
    - `stonith` — IPMI credential hardening: BMC password delivered via the `IPMI_PASSWORD` env + `-E` (never on the argv / process table) with redacted argv in error strings; tests assert the password appears in neither argv nor errors (HXC-908, closing a wave-67 follow-up).
    - `deviceplugin` — concurrency test driving `ApplyFingerprint`/`Allocate`/`Release` from 50+ goroutines under `-race`, proving the Registry never oversubscribes (mutex removal trips the race detector) (HXC-910, closing a wave-67 follow-up).
    - `internal/llm` — replaced the stub `Inference` with a real strategy-based router (latency/throughput/quality/cost) over the live model registry, excluding unhealthy/unloaded models with a typed no-eligible-model error and a real injectable `Backend` seam (HXC-1470).
    - `metrics` (additive `earningsmetrics.go`) — `EarningsMetrics` exposing `tao_earnings_total` / `graval_attestation_status` / `token_throughput` / `gpu_utilization` Prometheus-text series with deterministic (sorted) ordering + concurrent-safe setters (HXC-1483).
    - `internal/gpu` (additive `attesthook.go`) — GraVal attestation hook: `AllocateForChutes` refuses a GPU that has not passed attestation (`ErrAttestationRequired`), even on the idempotent re-allocation path; injectable `ChutesAttestor` seam (HXC-1448).
  - Wave 70 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 71):
    - `provider/chutes` — OpenAI-compatible `/v1/chat/completions` provider (`cpk_` Bearer) with HTTP-429 retry+backoff; httptest proves retry-then-success, Bearer header, and typed errors (HXC-1510).
    - `pool` (additive `model.go`) — `GPUDevice`/`WorkloadSpec`/`GPUAllocation`/`PoolStatus` data model with snake_case JSON tags + round-trip/concurrency tests (HXC-1495).
    - `quantization` (additive `awq.go`) — AWQ 4-bit default serving format + `EstimateVRAM` (~25% of fp16) + budget-aware `SelectFormat` preferring AWQ when fp16 won't fit (HXC-1473).
    - `internal/gateway` (additive `inference.go`) — inference route forwarding to an injectable Chutes client; `TEERequired` => `X-E2EE-Enabled` routing marker; client failure => 502 (HXC-1469).
    - `internal/gpu` (additive `mig.go`) — MIG profile management: standardized vGPU tiers (1g.10gb/2g.20gb/3g.40gb/7g.80gb), in-memory partitioning with oversubscription rejection + fixed-at-creation immutability (HXC-1449).
  - Wave 71 (5 units, subagent-driven, 5 parallel streams in disjoint packages, run concurrently with wave 70; closes 4 prior follow-ups):
    - `multiraft` — RaftTransport async-delivery safety: inbox handler Steps under the shard mutex, outbound messages deferred + flushed after unlock; a new async-transport `-race` test proves a goroutine-delivering transport is race-free and still commits (HXC-909).
    - `kraft` — `CreateTopic` returns `ErrTopicExists` on a conflicting re-create (different partition count); identical re-create stays idempotent (HXC-912).
    - `porcupine` — WGL state-dedup memoization via `Model.Equal`: verdict-preserving pruning (memo fires on backtracking histories; identical PASS/FAIL + witness vs `Equal==nil`) (HXC-913).
    - `chutes` — `StreamChannel` honors ctx cancellation on a blocked read (prompt channel close, no goroutine leak) and closes the owned reader (HXC-911).
    - `marketplaceadapter` (additive `akash.go`) — Akash (Cosmos/AKT) adapter over an injectable client: reverse-auction pricing, SDL submission, provider-reputation gate, registered in the `Name()`-dispatch Registry (HXC-1458).
  - Wave 72 (5 units, subagent-driven, 5 parallel streams in disjoint packages):
    - `provider/runpod` — RunPodProvider (implements `pool.GPUProvider`) with a warm-pool fast path over an injectable client (HXC-1515).
    - `provider/aws` — AWSProvider EC2 Spot adapter over an injectable EC2-client interface (no aws-sdk dep): GPU model→instance-type selection (p5/p4de/g5), Spot+tags, TOCTOU-safe capacity gate (HXC-1516).
    - `scheduler` (additive `tier_filter.go`) — GPUTier-aware filter predicate: hard-exclude disallowed tiers + preference scoring, composes with the existing count filter, fails closed on a nil predicate (HXC-1534).
    - `provider/chutes` — ctx-interruptible 429 backoff (select vs `ctx.Done()`) + honors `Retry-After` (delta-seconds/HTTP-date, `max(hint,backoff)`) (HXC-916, closing a wave-70 follow-up).
    - `grafanadash` (additive `tiercost_dash.go`) — Phase-8B tier-utilization + cost dashboard generator (panels target `gpu_tier_utilization`/`gpu_cost_per_hour`), valid + deterministic (HXC-1551).
  - Wave 73 (5 units, subagent-driven, 5 parallel streams in disjoint packages; impl→spec-review→quality-review + independent build/vet/-race/mutation gate):
    - `provider/ionet` — IONetProvider over the io.net REST API (implements `pool.GPUProvider`, injectable base URL + `*http.Client` + bearer): `DeployCluster` returns the backend-minted cluster run-UUID (parsed, never fabricated), `HealthCheck` derives healthy state, concurrency-safe reserve-under-lock capacity gate; closure proven against a `net/http/httptest` io.net backend (HXC-1514).
    - `gitops` — ArgoCD ApplicationSet federation client: matrix generator expands one Application per cell, `syncPolicy` prune+selfHeal, RollingSync canary→tier-2 (50%)→tier-1 (25%) ordering, drift/prune detection; logic proven vs an httptest ArgoCD, strictly-live UI capture honestly skipped (follow-up HXC-922) (HXC-1367).
    - `internal/federation` — Karmada PropagationPolicy + OverridePolicy engine (CRs modeled as Go structs → YAML, no client-go dep) with constraint-aware (data-locality/latency/cost/compliance) two-level cell selection and failover reselect; deterministic failover proven via in-process FakeHub, live Karmada <60s capture honestly skipped (follow-up HXC-924) (HXC-1364).
    - `health` (additive `miner.go`) — miner-api + GraVal bootstrap DaemonSet dependency checks wired into the health rollup, naming the specific failing check; healthy→unhealthy delta proven against an httptest miner-api (HXC-1484).
    - `discovery` (additive `mdns.go`) — mDNS/DNS-SD advertiser+browser on `_helix-cluster._tcp` (stdlib `net` + `x/net/dnsmessage`): TXT encode/decode + reject-invalid + in-process loopback discovery; real-multicast two-node LAN capture sandbox-skipped (follow-up HXC-925); discovery-only (trust via SPIFFE) (HXC-1347).
    - Reconciled stale P0 `HXC-904` Phase-4 Build Service: delivered podman-based multi-arch container build orchestration + artifact cache (`internal/build`/`pkg/build`/`cmd/helix-build`, green) marked Completed; Bazel REAPI interop split to follow-up HXC-921.
- **Security fix (HIGH):** closed a TOCTOU replay-bypass in `pkg/gpuattest.Verify` — the nonce check and consume are now atomic, with a concurrent-replay `-race` regression test.
- Every foundation package ships with tests that fail under mutation of the logic they cover (CLAUDE-1 enforcement) and are gated by whole-tree `build`/`vet` + `-race`.
- Initial project scaffold with Go workspace.
- 29 submodules integrated at project root.
- Directory structure: `cmd/`, `pkg/`, `internal/`, `api/`, `web/`, `scripts/`, `deploy/`, `test/`.

### Changed
- **Governance:** CLAUDE-3 / AGENT-2 / QWEN-2 documentation continuous-sync guarantee added (restates and cites Constitution §11.4.106 docs-chain); `.docs_chain/contexts/tracked_docs.yaml` extended to enforce README, CLAUDE/AGENTS/QWEN, CHANGELOG, FOUNDATION_PACKAGES, and CODEGRAPH export-sync.

## [0.1.0-dev] - 2026-05-30

### Added
- Phase 0 bootstrap: submodule cloning and workspace initialization.
