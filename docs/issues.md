# Issues

**Revision:** 74
**Last modified:** 2026-06-13T13:39:15Z
**Description:** Active workable-item registry for Helix Cluster OS
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth)
**Generated-by:** scripts/docs/db_to_md.py (DB is canonical; edit via cmd/hxc-registry, not by hand)

Total active items: **409**. Canonical source: `data/hxc_registry.db`. Full per-item detail (description, closure criteria, required test types, source refs) lives in the DB and `docs/research/_ledger/*.json`.

## Foundation (anti-bluff) (14 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-932 | Task | P0 | Queued | Wire-in vs prune decision + execution for ~167 orphaned pkg packages (STRATEGIC umbrella) |
| HXC-944 | Bug | P0 | Completed | Fix 3 CRITICAL CVEs from trivy security scan |
| HXC-016 | Task | P1 | Queued |  |
| HXC-017 | Task | P1 | Queued |  |
| HXC-018 | Task | P1 | Queued |  |
| HXC-019 | Task | P1 | Queued |  |
| HXC-020 | Task | P1 | Queued |  |
| HXC-942 | Bug | P1 | Completed | Add e2e/chaos -race integration tests for the 8 fixed concurrency hazards |
| HXC-945 | Bug | P1 | Completed | Triage and resolve gosec HIGH findings (99 HIGH) from security scan |
| HXC-938 | Task | P2 | Completed | Add fuzz to crypto and benchmarks to hot paths |
| HXC-939 | Docs | P2 | Completed | Refresh stale per-phase GAP_AUDIT.md files (CLAUDE-3) |
| HXC-940 | Task | P2 | Completed | Wire the orphaned gate packages (covgate/archlint/etcdlint/qualitygate/phasegate) |
| HXC-946 | Task | P2 | Completed | Triage trivy misconfig HIGH (50) + suppress QA-sentinel secret false-positives |
| HXC-949 | Feature | P2 | Queued | Chaos OTel polyglot trace (Rust/Elixir/Go) + Grafana dashboards (split from HXC-1258) |

## MVP (9 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1100 | Feature | P1 | Queued | Implement real Bazel RBE build execution (internal/build) |
| HXC-1140 | Task | P1 | Queued | Publish v1.0.0-dev-mvp release across all modules with packaging |
| HXC-1637 | Bug | P1 | Completed | make build is broken: builds non-existent ./cmd/helix-cluster |
| HXC-1113 | Bug | P2 | Obsolete | Remove dangling pkg/gpuattest placeholder and reuse security/pkg/gpuattest |
| HXC-1128 | Feature | P2 | Queued | Implement Health Monitor with eBPF probes and LSTM failure prediction |
| HXC-1129 | Feature | P2 | Queued | Implement CRIU/DMTCP-based session live migration orchestration |
| HXC-1143 | Feature | P2 | Queued | Implement distcc/icecream distributed C/C++ compilation and ccache layer |
| HXC-1638 | Bug | P2 | Completed | helixctl binary referenced in docs but absent from cmd/ |
| HXC-1639 | Bug | P2 | Completed | SQL schema drift: 0001_primary_schema.sql diverges from the golang-migrate chain (001-015) |

## Phase 2 (9 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1153 | Feature | P1 | Queued | Implement pkg/resources Linux GPU reader via /sys/class/drm (NVML-free) |
| HXC-1159 | Task | P1 | Queued | E2E: a Linux node joins the cluster mesh and becomes schedulable |
| HXC-906 | Docs | P1 | Queued | PHASE_2_ROADMAP.md |
| HXC-1154 | Feature | P2 | Queued | Integrate universal Vulkan compute backend (vendor-neutral, no device-specific code) |
| HXC-1155 | Feature | P2 | Queued | Implement console/node thermal sink reader via /sys/class/thermal |
| HXC-1161 | Feature | P2 | Queued | E2E: aggregate GPU pool runs llama.cpp inference across heterogeneous nodes |
| HXC-1163 | Task | P2 | Queued | Validate cross-NAT WireGuard mesh hole-punching across real hosts |
| HXC-1166 | Research | P3 | Queued | CRIU/DMTCP live process migration (research + spike) |
| HXC-931 | Task | P3 | Queued | Reconcile internal/console linux_boot.go (BootMachine) with boot_coordinator.go (BootCoordinator) |

## Phase 3 (34 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1168 | Feature | P0 | Queued | Implement SBC Linux node agent (Orange Pi 5 Max target, Tier T3) |
| HXC-1171 | Task | P0 | Queued | Validate Mali-G610 Vulkan/OpenCL compute on SBC |
| HXC-1172 | Feature | P0 | Queued | Scaffold Android Agent APK (Kotlin + NDK + Termux) |
| HXC-1173 | Feature | P0 | Queued | Implement Android foreground service framework with START_STICKY |
| HXC-1174 | Feature | P0 | Queued | Integrate Android BatteryManager monitoring |
| HXC-1184 | Feature | P0 | Completed | Implement edge node registration for all tiers (T3-T8) |
| HXC-1197 | Feature | P0 | Completed | Implement edge output verification (LLMsVerifier/redundant/checksum) |
| HXC-1205 | Feature | P0 | Queued | Build edge setup wizard for device onboarding |
| HXC-1208 | Task | P0 | Queued | Implement battery/thermal stress testing for edge devices |
| HXC-1169 | Feature | P1 | Queued | Implement SBCAdapter for SBC-specific hardware monitoring |
| HXC-1170 | Feature | P1 | Queued | Integrate RK3588 NPU via RKNN Toolkit2 C API |
| HXC-1176 | Feature | P1 | Queued | Implement Android Vulkan/NNAPI compute backend |
| HXC-1177 | Feature | P1 | Queued | Implement QUIC transport client for Android (Tier T6) |
| HXC-1179 | Feature | P1 | Queued | Scaffold iOS Agent app (Swift, Tier T7 EDGE_DONOR) |
| HXC-1180 | Feature | P1 | Queued | Implement iOS Metal compute integration |
| HXC-1182 | Feature | P1 | Queued | Implement iOS background execution scheduling (BGAppRefresh/BGProcessing) |
| HXC-1192 | Feature | P1 | Queued | Implement unified NPU backend with ONNX model converter |
| HXC-1193 | Feature | P1 | Completed | Implement per-tier workload quantization selection |
| HXC-1194 | Feature | P1 | Queued | Integrate MLC LLM universal engine for mobile inference |
| HXC-1200 | Feature | P1 | Queued | Implement Jetson CUDA-on-ARM GPU backend |
| HXC-1203 | Task | P1 | Queued | Implement Armbian TV-box provisioning path (RK3588/Amlogic) |
| HXC-1204 | Task | P1 | Queued | Implement ADB-over-WiFi fleet provisioning for Android cluster |
| HXC-1206 | Feature | P1 | Queued | Build APK/IPA distribution system |
| HXC-1209 | Task | P1 | Queued | Validate ARM SBC per-watt performance >=80% of x86 |
| HXC-926 | Task | P1 | Queued | Wire pkg/powergater CanAcceptWork into edge/agent work-acceptance path |
| HXC-1181 | Feature | P2 | Queued | Implement iOS CoreML / Neural Engine inference engine |
| HXC-1183 | Feature | P2 | Queued | Implement HarmonyOS Agent (ArkTS) with Da Vinci NPU inference |
| HXC-1217 | Feature | P2 | Queued | Implement internal/gpu cgo vendor backends (CUDA/ROCm/oneAPI/MLX) |
| HXC-1218 | Feature | P2 | Queued | Implement session live migration (CRIU/DMTCP) |
| HXC-1219 | Feature | P2 | Queued | Implement WireGuard UPnP/NAT-PMP NAT traversal |
| HXC-1221 | Docs | P2 | Queued | Document Phase 3 edge multi-platform agent architecture diagrams |
| HXC-1622 | Feature | P2 | Queued | HXC-1167 follow-up: CGO-enabled aarch64 cross-build via zig cc / aarch64-linux-gnu-gcc |
| HXC-1623 | Task | P2 | Queued | HXC-1167 follow-up: validate cross-compiled helix-agent on real Orange Pi 5 Max (aarch64) execution |
| HXC-1624 | Feature | P2 | Queued | HXC-1158 follow-up: add positive non-console SoC-match labels (RK3588/RPi/x86 server) to internal/console detector |

## Phase 4 (46 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1222 | Feature | P0 | Queued | Build K3s orchestration substrate with RuntimeClasses for firecracker/kata/runc |
| HXC-1223 | Feature | P0 | Queued | Implement Firecracker microVM provisioner for T1-T3 with vsock and virtio-net |
| HXC-1224 | Feature | P0 | Queued | Implement Firecracker golden snapshot create/restore pipeline (<=28ms restore) |
| HXC-1232 | Feature | P0 | Queued | Implement unified golden snapshot + COW instant-reset across all simulator backends |
| HXC-1234 | Feature | P0 | Queued | Build Rust DST engine: single-threaded SimLoop with virtual clock and seeded PRNG |
| HXC-1235 | Feature | P0 | Queued | Integrate Rust turmoil for deterministic simulated TCP/UDP networking |
| HXC-1242 | Feature | P0 | Queued | Build Elixir/OTP Chaos Controller GenServer with supervision-tree isolation |
| HXC-1251 | Feature | P0 | Queued | Build Virtual Testing Controller OTP supervision tree (one_for_all) |
| HXC-1252 | Feature | P0 | Queued | Implement SessionManager with 50-session cap, 2h TTL, resource-quota enforcement |
| HXC-1253 | Feature | P0 | Queued | Implement DevicePool GenServer with tier-to-simulator dispatch and health checks |
| HXC-1256 | Feature | P0 | Queued | Expose REST API for session CRUD, device provisioning and snapshot restore |
| HXC-1225 | Feature | P1 | Queued | Implement QEMU/KVM ARM64 virt provisioner for T6 RK3588-approximate SBC |
| HXC-1226 | Feature | P1 | Queued | Integrate Cuttlefish/CrosVM provisioner for T5 Android AOSP devices |
| HXC-1228 | Feature | P1 | Queued | Implement Docker + binfmt_misc protocol stubs for T7 iOS and T8 HarmonyOS |
| HXC-1245 | Feature | P1 | Completed | Implement 3 time fault injectors (clock skew, clock freeze, monotonic drift) |
| HXC-1250 | Feature | P1 | Queued | Integrate Chaos Mesh CRDs (NetworkChaos, TimeChaos, StressChaos, DNSChaos) |
| HXC-1257 | Feature | P1 | Queued | Build Phoenix LiveView real-time test dashboard |
| HXC-1259 | Feature | P1 | Queued | Implement HelixQA automatic challenge generation from test outcomes |
| HXC-1262 | Feature | P1 | Queued | Integrate CI/CD quality gates for GitHub Actions / GitLab CI / Jenkins |
| HXC-1263 | Feature | P1 | In progress | Build WebAssembly plugin host on Wasmtime Component Model with WIT bindings |
| HXC-1269 | Feature | P1 | Queued | Build cmd/helix-testd daemon as OTP controller node entrypoint |
| HXC-1273 | Feature | P1 | Queued | Implement vsock host-guest control channel for chaos-resilient telemetry |
| HXC-1284 | Task | P1 | Queued | Benchmark Firecracker 5,000+ microVMs per host with KSM and memory overcommit |
| HXC-1285 | Task | P1 | Queued | Run 72-hour continuous chaos soak proving no resource leaks or degradation |
| HXC-1286 | Docs | P1 | Queued | Execute Production Readiness Review with 80-item checklist (>=95% complete) |
| HXC-1287 | Task | P1 | Queued | Run full 8-tier matrix (160 nodes) end-to-end within 45-minute CI budget |
| HXC-907 | Docs | P1 | Queued | PHASE_4_ROADMAP.md |
| HXC-1227 | Feature | P2 | Queued | Implement T4 gaming-console protocol-level constrained x86_64 VM |
| HXC-1246 | Feature | P2 | Queued | Implement 6 hardware fault injectors (NMI, mem CE/UE, PCIe AER, CPU bit-flip, thermal throttle) |
| HXC-1266 | Docs | P2 | Queued | Provide reference plugins (Rust device-sim, Zig device-sim) compiled to wasm32-wasi |
| HXC-1270 | Feature | P2 | Queued | Implement Firecracker MicroVM Kubernetes operator and CRD |
| HXC-1271 | Feature | P2 | Queued | Deploy WireGuard encrypted inter-host mesh DaemonSet for multi-host clusters |
| HXC-1272 | Feature | P2 | Queued | Implement distributed snapshot pool backend (MinIO) and multi-host scheduling |
| HXC-1274 | Feature | P2 | Queued | Implement Mininet WAN topology harness for multi-region network-stack testing |
| HXC-1275 | Research | P2 | Queued | Author TLA+/PlusCal formal specs for leader election and scheduler allocation |
| HXC-1276 | Research | P2 | Queued | Author Jepsen black-box linearizability test for task scheduling under partitions |
| HXC-1279 | Feature | P2 | Queued | Add eBPF/XDP line-rate programmable network fault injection (cilium/ebpf) |
| HXC-1283 | Docs | P2 | Queued | Define Corellium / physical-hardware-in-the-loop path for iOS, PS4, RK3588 GPU/NPU |
| HXC-1288 | Task | P2 | Queued | Define polyglot interop boundaries (Rust↔Go↔Elixir↔Wasm) |
| HXC-1289 | Feature | P2 | Queued | Integrate libcluster automatic BEAM cluster formation via Kubernetes DNS |
| HXC-1625 | Bug | P2 | Queued | Replicate SELinux relabel fix (HXC-1621) to Herald/containers/pkg/crossbuild duplicate copy |
| HXC-921 | Feature | P2 | Queued | Bazel Remote Execution (REAPI) interop for build service (optional) |
| HXC-1277 | Research | P3 | Queued | Integrate Shadow/Phantom for unmodified-binary deterministic network simulation |
| HXC-1280 | Research | P3 | Queued | Validate gem5 big.LITTLE (O3+Minor) scheduler fidelity for RK3588 |
| HXC-1281 | Research | P3 | Queued | Evaluate VirGL/Venus virtual GPU for OpenGL/Vulkan workload functional testing |
| HXC-1282 | Research | P3 | Queued | Integrate Tart OCI-native macOS/Linux VMs for iOS/macOS CI build-and-test |

## Phase 5 (28 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1314 | Feature | P0 | Queued | Implement pkg/handheld Steam Deck agent (Vulkan compute, battery, thermal) (DEFERRED hw) |
| HXC-1309 | Feature | P1 | Completed | Implement capability manifest generation + control-plane negotiation |
| HXC-1312 | Feature | P1 | Queued | Build multi-arch universal agent container (amd64/arm64/riscv64) via buildx + distroless |
| HXC-1313 | Feature | P1 | Queued | Implement riscv64 cross-compilation pipeline + RVV extension detection (DEFERRED CI) |
| HXC-1315 | Feature | P1 | Queued | Package Steam Deck Flatpak agent with systemd integration (DEFERRED hw) |
| HXC-1316 | Feature | P1 | Queued | Implement internal/sbc: Jetson TensorRT backend (>=60 TOPS) (DEFERRED hw) |
| HXC-1317 | Feature | P1 | Queued | Implement internal/sbc RK3588 NPU + Turing RK1 cluster support |
| HXC-1320 | Feature | P1 | Queued | Implement internal/enterprise EPYC/Ampere auto-provisioning + Coreboot trust detection (DEFERRED hw) |
| HXC-1321 | Feature | P1 | Queued | Implement internal/iot OpenWrt router gateway agent (GL-MT6000, Docker, <5% overhead) |
| HXC-1322 | Feature | P1 | Queued | Implement internal/iot NAS storage-node agent (Synology/QNAP Docker, dual-role) |
| HXC-1327 | Feature | P1 | Queued | Implement gVisor/Kata per-tier sandbox hardening for UNTRUSTED/edge nodes (DEFERRED) |
| HXC-1329 | Feature | P1 | Queued | Implement hybrid cloud-on-prem WireGuard mesh joining spot workers to on-prem cluster |
| HXC-1332 | Task | P1 | Queued | Phase 5a integration test: 10-node mixed handheld+SBC cluster |
| HXC-1318 | Feature | P2 | Queued | Implement pkg/fpga + internal/fpga hard-SoC/soft-core/DPU backends (DEFERRED hw) |
| HXC-1319 | Feature | P2 | Queued | Implement bitstream verification + management for FPGA tiers (T11 required) |
| HXC-1324 | Feature | P2 | Queued | Implement internal/exotic Groq LPU inference adapter (<100ms TTFT) (DEFERRED cloud) |
| HXC-1325 | Feature | P2 | Queued | Implement internal/exotic Cerebras CS-3 large-model inference adapter (DEFERRED cloud) |
| HXC-1331 | Docs | P2 | Completed | Author 64-device master taxonomy table as machine-readable device catalog |
| HXC-1333 | Task | P2 | Queued | Phase 5b integration test: 8-node arm64+riscv64+fpga heterogeneous cluster |
| HXC-1617 | Task | P2 | Queued | Live-cloud IMDS interruption integration on real AWS/Azure/GCP spot accounts |
| HXC-1618 | Task | P2 | Queued | Validate pkg/benchmark normalized scores across two physically different nodes |
| HXC-928 | Task | P2 | Queued | Execute linux fixtures on real linux CI for wave-74 packages (CLAUDE-2) |
| HXC-929 | Task | P2 | Queued | Real macOS thermal source for powergater/edgeheartbeat CPU temp |
| HXC-930 | Task | P2 | Queued | Live discrete-GPU/NPU hardware probe for pkg/resources accelerators |
| HXC-1323 | Feature | P3 | Queued | Implement webOS smart-TV JS agent over WebSocket (DEFERRED device) |
| HXC-1326 | Research | P3 | Queued | Implement pkg/quantum Qiskit Runtime plugin for async circuit submission (DEFERRED) |
| HXC-1330 | Docs | P3 | Queued | Define and validate 5 reference cluster build recipes ($250/$500/$1k/$2k/$5k) |
| HXC-1620 | Bug | P3 | Queued | Harden tierdetect linux cpuinfoIsARM64 multi-digit CPU-architecture parsing |

## Phase 6 (39 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1344 | Feature | P0 | Completed | Implement pkg/swim/hierarchical two-tier LAN+WAN delegate gossip |
| HXC-1348 | Feature | P0 | Completed | Implement pkg/nattraversal STUN client and NAT-type classification |
| HXC-1349 | Feature | P0 | Completed | Implement ICE candidate gathering, prioritization and connectivity checks |
| HXC-1352 | Feature | P0 | Completed | Implement internal/cell WireGuard inter-cell mesh manager |
| HXC-1369 | Feature | P0 | Queued | Implement cmd/helix-federation binary and helixctl federation CLI |
| HXC-1384 | Task | P0 | Queued | Establish multi-cell integration testbed for CLAUDE-1 usability gate |
| HXC-933 | Feature | P0 | Queued | Build cmd/helix-federation and wire Phase 6 packages (federation/crdt/hlc/spiffefed/gitops) |
| HXC-1345 | Feature | P1 | Completed | Implement gateway-relayed cross-cell suspicion propagation |
| HXC-1346 | Feature | P1 | Completed | Implement bootstrap/rendezvous strategies (static, DNS-SRV, DHT, cloud, mDNS) |
| HXC-1357 | Feature | P1 | Completed | Implement pkg/spiffe/federation trust-bundle exchange across cells |
| HXC-1361 | Feature | P1 | Completed | Implement double-encryption (WireGuard L3 + mTLS L7) data path |
| HXC-1362 | Feature | P1 | Queued | Implement pkg/cilium Cluster Mesh client and global-service propagation |
| HXC-1363 | Feature | P1 | Completed | Implement federated service discovery (cell-local + global registry) |
| HXC-1370 | Feature | P1 | Queued | Extend internal/node cell agent: gossip-tier selection + federation identity attestation |
| HXC-1371 | Task | P1 | Completed | Enforce etcd-stays-within-cell static-analysis lint gate |
| HXC-1380 | Feature | P1 | Completed | Implement split-brain detection PromQL alerts with runbook links |
| HXC-1385 | Task | P1 | Queued | Validate Phase 6 exit-gate KPIs against the live federation |
| HXC-1386 | Task | P1 | Completed | Verify sub-phase dependency gates 6a->6b->6c->6d soak conditions |
| HXC-937 | Task | P1 | Completed | Add e2e + chaos test types for consensus/federation/marketplace |
| HXC-1354 | Feature | P2 | Queued | Implement SSH tunnel bridge fallback for control-plane bootstrap |
| HXC-1355 | Feature | P2 | Queued | Implement cloud VPN bridge with provider-specific endpoint discovery |
| HXC-1358 | Feature | P2 | Completed | Implement OPA/Gatekeeper cross-cluster federated-trust admission |
| HXC-1359 | Feature | P2 | Completed | Implement OPA data-residency / data-sovereignty admission policy |
| HXC-1360 | Feature | P2 | Queued | Integrate External Secrets Operator + Vault for federation secrets |
| HXC-1366 | Feature | P2 | Queued | Implement cloud bursting auto-scaling burst pool with spot termination handling |
| HXC-1368 | Feature | P2 | Completed | Implement CRDT-based config sync for cell-local overrides |
| HXC-1372 | Feature | P2 | Completed | Implement per-cell etcd Raft WAN-safe tuning profiles |
| HXC-1374 | Feature | P2 | Completed | Implement internal/chaos CE-01..CE-12 chaos experiment suite |
| HXC-1375 | Feature | P2 | Completed | Implement Turmoil-style deterministic multi-cluster simulation harness |
| HXC-1377 | Docs | P2 | Completed | Author Phase 6 FMEA catalog with detection and recovery runbooks |
| HXC-1378 | Feature | P2 | Queued | Implement hierarchical Prometheus federation across cells |
| HXC-1379 | Feature | P2 | Queued | Implement OpenTelemetry cross-cell distributed tracing |
| HXC-1382 | Feature | P2 | Queued | Implement Velero tiered disaster recovery backup and restore |
| HXC-1383 | Feature | P2 | Completed | Implement five federation topology patterns and block-binding modes |
| HXC-922 | Task | P2 | Queued | Live ArgoCD 3-cell ApplicationSet rollout usability capture (HXC-1367 live gate) |
| HXC-924 | Task | P2 | Queued | Live Karmada multi-cluster <60s failover usability capture (HXC-1364 live gate) |
| HXC-925 | Task | P2 | Queued | Two-node LAN mDNS discovery usability capture (HXC-1347 live gate) |
| HXC-1376 | Docs | P3 | Queued | Establish quarterly Game Day federation chaos drill protocol |
| HXC-1381 | Feature | P3 | Completed | Build Grafana global-health cell-status-grid dashboard |

## Phase 7 (46 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1387 | Feature | P0 | Completed | Implement Multi-Raft per-shard consensus manager (G-01, P0) |
| HXC-1388 | Feature | P0 | Completed | Implement heartbeat coalescer for Multi-Raft network overhead control |
| HXC-1390 | Feature | P0 | Completed | Implement MVCC revision store with B-tree index and time-travel (G-08, P0) |
| HXC-1392 | Feature | P0 | Completed | Implement SLURM-style backfill scheduler over availability timeline (G-02, P0) |
| HXC-1394 | Feature | P0 | Completed | Implement CRC16 16,384 hash-slot session router (G-03, P0) |
| HXC-1395 | Feature | P0 | Completed | Implement MOVED/ASK redirection and ASKING for in-flight slot migration (G-03) |
| HXC-1400 | Feature | P0 | Completed | Implement largest-subcluster-wins voting quorum (G-19, P0) |
| HXC-1419 | Feature | P0 | Completed | Build deterministic simulation testing (DST) framework on Turmoil (G-14, P0) |
| HXC-1420 | Feature | P0 | Completed | Implement BUGGIFY chaos macros at 25% deterministic fire rate (G-15, P0) |
| HXC-1389 | Feature | P1 | Completed | Implement leaseholder fast-path local reads in Multi-Raft |
| HXC-1391 | Feature | P1 | Completed | Implement persistent Watch Manager with synced/unsynced groups (G-08 support) |
| HXC-1393 | Feature | P1 | Completed | Implement multifactor priority queue for scheduler (age+fairshare+size+QoS) |
| HXC-1398 | Feature | P1 | Completed | Add Startup health-probe tier with GPU grace period (G-04, P1) |
| HXC-1399 | Feature | P1 | Completed | Implement SWIM majority PFAIL->FAIL two-phase failure confirmation (G-23, P1) |
| HXC-1401 | Feature | P1 | Completed | Implement STONITH fencing agents (IPMI/cloud/shared-disk) (G-19, P1) |
| HXC-1404 | Feature | P1 | Completed | Implement N+K failover capacity admission control (G-22, P1) |
| HXC-1405 | Feature | P1 | Completed | Implement gang scheduling all-or-nothing GPU reservation (G-18, P1) |
| HXC-1407 | Feature | P1 | Completed | Implement device-plugin / GRES fingerprinting framework (G-11/G-13/G-17, P1) |
| HXC-1408 | Feature | P1 | Completed | Implement BOINC-style redundant-execution trust scorer (G-09, P1) |
| HXC-1410 | Feature | P1 | Completed | Implement Cassandra 3-layer repair: hinted handoff + read repair + Merkle anti-entropy (G-01 part, P1) |
| HXC-1421 | Feature | P1 | Completed | Integrate Porcupine linearizability checking in CI under fault injection (G-16, P1) |
| HXC-1425 | Docs | P1 | Completed | Produce Phase 7 phase-by-phase 23-gap matrix as tracked deliverable ledger |
| HXC-908 | Bug | P1 | Completed | Harden pkg/stonith IPMI credential handling (no -P on argv, redact in errors) |
| HXC-1396 | Feature | P2 | Completed | Implement Atomic Slot Migration controller for live session migration (G-03, P2) |
| HXC-1397 | Feature | P2 | Completed | Add config-epoch conflict resolution for simultaneous failovers |
| HXC-1402 | Feature | P2 | Completed | Implement Pacemaker-style four-type constraint engine (G-20, P2) |
| HXC-1403 | Feature | P2 | Completed | Implement SCAN stable virtual-IP/DNS client endpoint (G-21, P2) |
| HXC-1406 | Feature | P2 | Completed | Implement topology-aware NUMA/NVLink GPU placement scoring (G-10/G-18, P2) |
| HXC-1409 | Feature | P2 | Completed | Implement standalone delta-state CRDT package (G-counter/PN-counter/OR-set/LWW-map) (G-01 part, P2) |
| HXC-1411 | Feature | P2 | Completed | Implement Informer cache helixcache.Watcher (list-watch local cache) (G-05, P2) |
| HXC-1412 | Feature | P2 | Completed | Implement rate-limited work queue with exponential backoff (G-06, P2) |
| HXC-1413 | Feature | P2 | Completed | Implement API Priority & Fairness FlowSchema->PriorityLevel->Queue (G-07, P2) |
| HXC-1414 | Feature | P2 | Queued | Implement NATS leaf-node edge-to-core store-and-forward topology (G-12, P2) |
| HXC-1415 | Feature | P2 | Completed | Implement idempotent producer (PID + sequence) for exactly-once messaging |
| HXC-1416 | Feature | P2 | Completed | Implement cooperative incremental rebalancing for consumers/membership |
| HXC-1418 | Feature | P2 | Completed | Implement tiered cache hot(memory)/warm(NVMe)/cold(SSD) data tiers |
| HXC-1422 | Feature | P2 | Completed | Build nightly chaos pipeline (pod kill, partition, disk stall, clock skew) (G-15 part) |
| HXC-1424 | Docs | P2 | Completed | Produce hardened HelixCluster architecture diagram and component map |
| HXC-1619 | Task | P2 | Queued | Enable cmd/dst-sim 1000-seed gate as a live CI job |
| HXC-909 | Task | P2 | Completed | pkg/multiraft: make RaftTransport async-delivery safe (Step under shard lock) |
| HXC-910 | Task | P2 | Completed | pkg/deviceplugin: add concurrency test exercising Registry mutex under -race |
| HXC-912 | Bug | P2 | Completed | pkg/kraft: CreateTopic should return ErrTopicExists on conflicting re-create |
| HXC-1417 | Feature | P3 | Completed | Implement embedded KRaft-style Raft quorum to remove external ZooKeeper |
| HXC-1423 | Docs | P3 | Queued | Author TLA+ formal specifications for consensus/coordination protocols (P2) |
| HXC-913 | Task | P3 | Completed | pkg/porcupine: implement state-dedup memoization via Model.Equal for large histories |
| HXC-914 | Task | P3 | Queued | internal/chaos: author live-cluster nightly pipeline + GitHub Actions workflow |

## Phase 8 (69 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1434 | Feature | P0 | Queued | Implement GraVal-style miner attestation handshake controller in pkg/chutes |
| HXC-1435 | Feature | P0 | Completed | Implement GraValVerifier.BatchVerify concurrent attestation with pass-rate KPI |
| HXC-1436 | Feature | P0 | Completed | Implement GraVal VRAM-threshold (95%) gate in attestation |
| HXC-1438 | Feature | P0 | Queued | Implement pkg/chutes MinerController K3s deployment lifecycle |
| HXC-1439 | Feature | P0 | Completed | Define ChutesMinerConfig and ValidatorConfig data structures |
| HXC-1440 | Feature | P0 | Queued | Deploy PostgreSQL StatefulSet for miner inventory tracking |
| HXC-1441 | Feature | P0 | Queued | Deploy Redis pub/sub Deployment for miner event propagation |
| HXC-1442 | Feature | P0 | Queued | Deploy GraVal bootstrap privileged DaemonSet for GPU attestation |
| HXC-1443 | Feature | P0 | Queued | Deploy miner-api Deployment with NodePort 32000 |
| HXC-1444 | Feature | P0 | Queued | Deploy Gepetto strategy engine via ConfigMap-backed Deployment |
| HXC-1445 | Feature | P0 | Queued | Deploy registry-proxy host-network DaemonSet on port 30500 |
| HXC-1446 | Feature | P0 | Completed | Implement custom HelixGepetto dual-resource arbitration strategy |
| HXC-1447 | Feature | P0 | Completed | Implement dual-workload capacity reservation in internal/gpu |
| HXC-1450 | Feature | P0 | Queued | Implement pkg/bittensor wallet (coldkey/hotkey) and TAO balance |
| HXC-1451 | Feature | P0 | Queued | Implement Bittensor SN64 subnet registration flow |
| HXC-1462 | Feature | P0 | Queued | Author helm/helixcluster-chutes base chart (values.yaml) |
| HXC-1465 | Feature | P0 | Queued | Author scripts/chutes-node-prep.sh bare-metal node preparation |
| HXC-1466 | Feature | P0 | Queued | Author scripts/chutes-miner-deploy.sh miner deployment + registration |
| HXC-1491 | Task | P0 | Queued | Validate end-to-end full request flow integration (workload->client->E2EE->network->miner->response) |
| HXC-934 | Bug | P0 | Completed | Make cmd/e2ee-proxy consume security/pkg/e2ee instead of inline crypto (de-bluff E2EE) |
| HXC-1426 | Feature | P1 | Completed | Implement pkg/chutes Chutes OpenAI-compatible API Client (non-streaming) |
| HXC-1427 | Feature | P1 | Completed | Implement Chutes API client fallback-model chain on retriable failure |
| HXC-1428 | Feature | P1 | Completed | Implement SSE streaming decoder for Chutes chat completions |
| HXC-1429 | Feature | P1 | Completed | Implement Chutes API model-list and user-account/balance queries |
| HXC-1430 | Feature | P1 | Completed | Implement model-router default-model resolution by strategy (latency/throughput/quality/cost) |
| HXC-1448 | Feature | P1 | Completed | Implement GraVal attestation hook in internal/gpu Manager |
| HXC-1454 | Feature | P1 | Completed | Implement MarketplaceAdapter interface and Chutes adapter |
| HXC-1455 | Feature | P1 | Completed | Implement UnifiedManager.RouteWorkload concurrent pricing + composite scoring |
| HXC-1463 | Feature | P1 | Queued | Author chute-deployment.yaml Helm template for inference pods |
| HXC-1464 | Feature | P1 | Queued | Author values-models.yaml with 8 pre-configured model deployments |
| HXC-1467 | Feature | P1 | Queued | Author scripts/chutes-health-monitor.sh observability + alerts |
| HXC-1468 | Feature | P1 | Queued | Author scripts/chutes-verify.sh end-to-end verification |
| HXC-1469 | Feature | P1 | Completed | Wire internal/gateway to route internal AI requests through Chutes API client |
| HXC-1470 | Feature | P1 | Completed | Replace internal/llm stub Inference with real model router (latency/throughput/quality/cost) |
| HXC-1471 | Feature | P1 | Queued | Deploy vLLM inference engine with PagedAttention defaults |
| HXC-1472 | Feature | P1 | Queued | Deploy SGLang inference engine with RadixAttention |
| HXC-1483 | Feature | P1 | Completed | Extend pkg/metrics + Prometheus/Grafana with TAO/GraVal/throughput dashboards |
| HXC-1485 | Task | P1 | Queued | Author HelixQA Challenge: miner deployment and TAO earnings |
| HXC-1486 | Task | P1 | Queued | Author HelixQA Challenge: E2EE inference roundtrip |
| HXC-1487 | Task | P1 | Queued | Author HelixQA Challenge: multi-marketplace routing decision |
| HXC-1488 | Task | P1 | Queued | Author HelixQA Challenge: GraVal attestation pass/fail |
| HXC-1489 | Task | P1 | Queued | Validate Chutes API client P99 first-token streaming latency < 500ms |
| HXC-935 | Feature | P1 | Queued | Wire Phase 8 marketplace/GraVal/miner into a real control loop |
| HXC-936 | Bug | P1 | Queued | Resolve dual package trees: top-level pkg/gpuattest vs security/pkg/gpuattest (and similar) |
| HXC-1432 | Feature | P2 | Queued | Implement gzip compression pipeline in E2EE inference envelope |
| HXC-1433 | Task | P2 | Queued | Reconcile E2EE AEAD spec deviation (AES-256-GCM vs roadmap ChaCha20-Poly1305) |
| HXC-1437 | Research | P2 | Queued | Implement GraVal CUDA Proof-of-Consecutive-VRAM-Work kernel (DEFERRED, GPU) |
| HXC-1449 | Feature | P2 | Completed | Implement MIG profile management in internal/gpu |
| HXC-1452 | Feature | P2 | Queued | Implement Yuma Consensus / metagraph weight queries in pkg/bittensor |
| HXC-1456 | Feature | P2 | Completed | Implement RevenueOptimizer.OptimizeAllocation greedy GPU-to-marketplace assignment |
| HXC-1457 | Feature | P2 | Queued | Implement io.net marketplace adapter |
| HXC-1458 | Feature | P2 | Completed | Implement Akash marketplace adapter |
| HXC-1459 | Feature | P2 | Queued | Implement Salad marketplace adapter |
| HXC-1460 | Feature | P2 | Completed | Implement pkg/economics RewardDistributor multi-token distribution |
| HXC-1461 | Feature | P2 | Completed | Implement RewardDistributor.GetParticipantROI and break-even tracking |
| HXC-1473 | Feature | P2 | Completed | Enable AWQ 4-bit quantization as default model format |
| HXC-1475 | Feature | P2 | Queued | Deploy Intel TDX + NVIDIA CC TEE stack via sek8s |
| HXC-1476 | Feature | P2 | Completed | Implement hybrid PQC TLS (X25519 + ML-KEM-768) node-to-node transport |
| HXC-1477 | Feature | P2 | Queued | Implement Cosign/Sigstore image admission controller for K3s |
| HXC-1480 | Feature | P2 | Completed | Implement carbon-aware scheduler with per-job energy metering |
| HXC-1481 | Feature | P2 | Completed | Implement EU AI Act compliance documentation pipeline |
| HXC-1482 | Feature | P2 | Completed | Implement export-control tier verification (country-code KYC) at node onboarding |
| HXC-1490 | Task | P2 | Queued | Achieve >60% line coverage on pkg/chutes, pkg/marketplace, pkg/e2ee (Codecov gate) |
| HXC-911 | Bug | P2 | Completed | pkg/chutes StreamChannel: honor cancellation on blocked reads + close reader |
| HXC-941 | Task | P2 | Completed | Reconcile E2EE AEAD spec deviation (AES-256-GCM vs roadmap ChaCha20-Poly1305) |
| HXC-1453 | Feature | P3 | Queued | Implement child-hotkey delegation support in pkg/bittensor |
| HXC-1474 | Feature | P3 | Queued | Deploy TurboDiffusion video-generation engine |
| HXC-1478 | Feature | P3 | Queued | Implement LUKS-encrypted root for miner nodes |
| HXC-1479 | Feature | P3 | Queued | Implement Cilium egress control and Watchtower integrity challenges |

## Phase 8B (67 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1492 | Feature | P0 | Queued | Define pkg/pool GPUProvider interface as single integration seam |
| HXC-1493 | Feature | P0 | Completed | Implement GPUTier type with priority ordering (Local..Decentralized) |
| HXC-1494 | Feature | P0 | Completed | Implement PoolManager registry with tier-ordered Allocate walk |
| HXC-1495 | Feature | P0 | Completed | Implement WorkloadSpec/GPUDevice/GPUAllocation/PoolStatus data structures |
| HXC-1496 | Feature | P0 | Completed | Implement candidate filtering (model/VRAM/cost/label selector) |
| HXC-1498 | Feature | P0 | Completed | Implement PriorityScheduler (tier then least-load then cost) |
| HXC-1502 | Feature | P0 | Completed | Implement internal/costbroker ComputeBroker weighted scorer |
| HXC-1504 | Feature | P0 | Completed | Implement pkg/burst BurstController MONITOR->SPILL->RECOVER hysteresis |
| HXC-1510 | Feature | P0 | Completed | Implement pkg/provider/chutes ChutesProvider OpenAI-compatible adapter |
| HXC-1519 | Feature | P0 | Completed | Implement pkg/local LocalGPURegistrar with TCO effective-cost |
| HXC-1521 | Task | P0 | Queued | Expose security ML-KEM-768 E2EE Session/Transport as pkg/e2ee |
| HXC-1522 | Feature | P0 | Queued | Implement pkg/e2ee E2EEProxy outbound remote-GPU traffic proxy |
| HXC-1530 | Feature | P0 | Completed | Implement cmd/gpu-pool-manager HTTP+gRPC front-end binary |
| HXC-1531 | Feature | P0 | Completed | Implement cmd/burst-controller utilization-driven binary |
| HXC-1532 | Feature | P0 | Completed | Implement cmd/e2ee-proxy transparent ML-KEM-768 proxy binary |
| HXC-1552 | Task | P0 | Queued | Implement HelixQA BurstChallenge saturate+failover exit gate |
| HXC-1553 | Task | P0 | Queued | Implement E2E helix submit -> saturate -> route-to-Chutes demo |
| HXC-1497 | Feature | P1 | Completed | Implement global MaxCostPerHour budget cap on allocations |
| HXC-1499 | Feature | P1 | Completed | Implement CostAwareScheduler selecting cheapest GPUs meeting SLA |
| HXC-1500 | Feature | P1 | Completed | Implement LatencyAwareScheduler for inference routing |
| HXC-1501 | Feature | P1 | Completed | Implement HealthMonitor with 30s checks and auto-failover trigger |
| HXC-1503 | Feature | P1 | Completed | Implement ComputeBroker 60s re-scoring loop with price-spike guard |
| HXC-1505 | Feature | P1 | Completed | Implement BurstController utilization RingBuffer moving average |
| HXC-1506 | Feature | P1 | Completed | Implement BurstController activateBurst capacity estimation + allocation |
| HXC-1507 | Feature | P1 | Completed | Implement pkg/burst CostRouter per-workload-type provider scoring |
| HXC-1508 | Feature | P1 | Completed | Implement 5-tier fallback chain Chutes->io.net->RunPod->AWS |
| HXC-1511 | Feature | P1 | Completed | Implement Chutes retryWithFallback across fallback models on 429 |
| HXC-1515 | Feature | P1 | Completed | Implement pkg/provider/runpod RunPodProvider serverless + warm pool |
| HXC-1520 | Feature | P1 | Queued | Implement LocalGPURegistrar nvidia-smi discovery + GPU classification |
| HXC-1523 | Feature | P1 | Completed | Implement pkg/e2ee GraValVerifier provider admission gate |
| HXC-1526 | Feature | P1 | Queued | Implement pkg/proxy CUDA API interceptor + memory staging |
| HXC-1528 | Feature | P1 | Queued | Implement virtual /dev/nvidia* device creation proxying to remote GPU |
| HXC-1529 | Feature | P1 | Completed | Implement workload-suitability classifier (HPC local vs inference remote) |
| HXC-1534 | Feature | P1 | Completed | Add GPUTier-aware filter predicate to pkg/scheduler |
| HXC-1535 | Feature | P1 | Completed | Add GPU-tier utilization/cost/provider-health metrics to pkg/metrics |
| HXC-1536 | Feature | P1 | Queued | Register virtual GPU devices into internal/node cluster resource model |
| HXC-1537 | Task | P1 | Queued | Integrate GraValVerifier into pkg/security provider admission |
| HXC-1540 | Feature | P1 | Queued | Implement local vLLM + SGLang serving stack deployment |
| HXC-1541 | Feature | P1 | Queued | Implement GPU-proxy DaemonSet advertising virtual nvidia.com/gpu |
| HXC-1548 | Feature | P1 | Completed | Implement CostTracker monthly cost report vs AWS on-demand |
| HXC-1549 | Task | P1 | Queued | Author Helm chart for Phase 8B GPU pool + burst deployment |
| HXC-1551 | Task | P1 | Completed | Build Prometheus/Grafana Phase 8B tier-utilization + cost dashboards |
| HXC-1554 | Task | P1 | Queued | Stand up real-provider CI integration (Chutes/io.net/RunPod) |
| HXC-1555 | Task | P1 | Queued | Capture E2EE usability evidence: tee=true encrypted-traffic gate |
| HXC-1556 | Task | P1 | Completed | Benchmark ML-KEM-768 E2EE handshake latency (<1ms target) |
| HXC-1557 | Task | P1 | Queued | Run inference p99 <500ms load test via Chutes (1000 requests) |
| HXC-1615 | Feature | P1 | Queued | Wire pkg/gpu ProviderAdapter registry into pkg/pool provider consumption |
| HXC-1509 | Feature | P2 | Completed | Implement QoS tiers (real-time, interactive, batch, best-effort) |
| HXC-1512 | Feature | P2 | Completed | Implement Chutes GetBalance USD balance monitor |
| HXC-1513 | Feature | P2 | Completed | Implement Chutes streaming (SSE) chat-completion variant |
| HXC-1516 | Feature | P2 | Completed | Implement pkg/provider/aws AWSProvider EC2 Spot adapter |
| HXC-1517 | Feature | P2 | Queued | Implement AWS Spot 2-minute interrupt -> CRIU/S3 checkpoint failover |
| HXC-1524 | Feature | P2 | Queued | Implement pkg/e2ee TDX AttestationVerifier for sensitive workloads |
| HXC-1525 | Feature | P2 | Queued | Implement key rotation CronJob + HSM integration for E2EE keys |
| HXC-1538 | Feature | P2 | Completed | Implement @helix.task Go decorator/SDK with lifecycle hooks |
| HXC-1539 | Feature | P2 | Completed | Implement intelligent ModelRouter (latency/throughput/cost/tee/balanced) |
| HXC-1543 | Feature | P2 | Queued | Implement KEDA queue-depth/custom-metric autoscaling |
| HXC-1545 | Feature | P2 | Queued | Implement CRIU GPU-context checkpoint/restore + graceful degradation |
| HXC-1547 | Feature | P2 | Completed | Implement TCO calculator for hybrid own+burst economic model |
| HXC-1550 | Task | P2 | Queued | Author Docker Compose development stack for Phase 8B |
| HXC-1558 | Task | P2 | Queued | Run 48-hour load test and 7-day soak (>99.9% success) |
| HXC-1559 | Task | P2 | Queued | Run chaos suite for provider failover and DDoS resilience |
| HXC-916 | Task | P2 | Completed | pkg/provider/chutes: ctx-interruptible backoff + honor Retry-After header |
| HXC-1518 | Docs | P3 | Queued | Document GCP and Azure provider adapter patterns |
| HXC-1542 | Feature | P3 | Queued | Implement bittencert blockchain-backed X.509 identity |
| HXC-1544 | Research | P3 | Completed | Implement predictive scaling forecast to pre-warm before peaks |
| HXC-1546 | Feature | P3 | Queued | Implement chutes-dropzone self-hosted gateway integration |

## Phase 8C (48 active)

| HXC | Type | Pri | Status | Title |
|---|---|---|---|---|
| HXC-1560 | Feature | P0 | Queued | Implement pkg/security/e2ee PQ envelope (ML-KEM-768 + HKDF-SHA256) |
| HXC-1562 | Task | P0 | Queued | Add Chutes byte-interop test-vector corpus for pkg/security/e2ee |
| HXC-1568 | Feature | P0 | Completed | Implement pkg/gpuattest device-info challenge/response + fingerprint |
| HXC-1569 | Feature | P0 | Completed | Implement seeded matmul proof-of-GPU-work (PoVW) in pkg/gpuattest |
| HXC-1570 | Feature | P0 | Completed | Implement O(1) spot-check verification in pkg/gpuattest |
| HXC-1571 | Feature | P0 | Completed | Implement device-sealed encrypt/decrypt (SealForDevice/OpenFromDevice) in pkg/gpuattest |
| HXC-1574 | Feature | P0 | Queued | Implement pkg/security/attestation TEE quote construct/verify |
| HXC-1575 | Feature | P0 | Queued | Implement Intel TDX quote generation with nonce+cert binding (report_data layout) |
| HXC-1576 | Feature | P0 | Queued | Implement NVIDIA GPU attestation evidence verification (NRAS/nvTrust) |
| HXC-1566 | Feature | P1 | Queued | Implement E2EE instance-discovery + single-use nonce contract |
| HXC-1577 | Feature | P1 | Queued | Implement measured-boot / RTMR3 access-config measurement |
| HXC-1578 | Feature | P1 | Queued | Implement attestation-gated key release (LUKS/secret) handshake |
| HXC-1580 | Feature | P1 | Completed | Implement pkg/modelintegrity hf_cache_verify gate (SHA-256 + size) |
| HXC-1583 | Feature | P1 | Completed | Maintain pkg/scheduler cost-aware GPU placement plugin |
| HXC-1584 | Feature | P1 | Completed | Implement value-multiplier preemption in pkg/scheduler (Gepetto model) |
| HXC-1585 | Feature | P1 | Completed | Implement SKIP LOCKED-style optimistic work-claiming in pkg/scheduler |
| HXC-1586 | Feature | P1 | Completed | Implement attestation-gated scheduler admission predicate |
| HXC-1590 | Feature | P1 | Completed | Implement NodeSelector placement-constraint schema in pkg/resources |
| HXC-1591 | Feature | P1 | Queued | Extend pkg/resources GPUInfo with device fingerprint + attested/thermal state |
| HXC-1592 | Feature | P1 | Completed | Implement SUPPORTED_GPUS compute-multiplier catalog + LookupMultiplier |
| HXC-1594 | Feature | P1 | Queued | Implement LLMOrchestrator vLLM/SGLang backend WRAP (OpenAI HTTP) |
| HXC-1595 | Feature | P1 | Completed | Implement streaming-safe failover (retry only before first byte) in LLMOrchestrator |
| HXC-1604 | Feature | P1 | Completed | Maintain pkg/fiber length-prefixed framed miner<->validator transport |
| HXC-1605 | Feature | P1 | Completed | Implement fiber ed25519 signed-identity handshake + stake-gated admission |
| HXC-1572 | Feature | P2 | Completed | Implement filesystem residency challenge in pkg/gpuattest |
| HXC-1573 | Feature | P2 | Completed | Implement multi-GPU node enumeration in pkg/gpuattest |
| HXC-1579 | Feature | P2 | Completed | Implement pkg/security/admission OPA-style policy + signed-image (cosign) gate |
| HXC-1581 | Feature | P2 | Completed | Implement model/revision anti-cheat verification token (V2 HMAC-SHA256) |
| HXC-1587 | Feature | P2 | Completed | Implement bounty/auction economic-placement plugin for scheduler |
| HXC-1588 | Feature | P2 | Completed | Implement utilization-aware EWMA candidate ranking for scheduler/orchestrator |
| HXC-1589 | Feature | P2 | Completed | Implement Kueue-style suspend-then-admit tiered job admission |
| HXC-1593 | Feature | P2 | Queued | Implement VRAM-capacity (>=95%) usable-memory gate in resources/gpuattest |
| HXC-1596 | Feature | P2 | Completed | Implement per-task ordered fallback chains with dedup/cap + empty-response detection |
| HXC-1597 | Feature | P2 | Completed | Implement thermal pre-warm / scale-from-zero state machine (Therm) |
| HXC-1598 | Feature | P2 | Completed | Implement passthrough cords + disconnect-aware upstream abort in LLMOrchestrator |
| HXC-1601 | Feature | P2 | Completed | Implement pkg/inferenceproxy correlation-ID + backend audit trail |
| HXC-1602 | Feature | P2 | Completed | Implement deterministic keyed-hash anonymization in pkg/inferenceproxy |
| HXC-1603 | Feature | P2 | Completed | Implement spoof-proof managed-header sanitization at the recording edge |
| HXC-1606 | Feature | P2 | Completed | Implement node identity verify-then-pin (TOFU) keypair-rooted TLS |
| HXC-1607 | Feature | P2 | Completed | Implement pkg/marketplace registration -> bounty -> metering -> payout loop |
| HXC-1608 | Feature | P2 | Completed | Implement pkg/marketplace/audit commit-then-prove reproducible reconciliation |
| HXC-1582 | Feature | P3 | Completed | Implement X25519 ephemeral session-key handshake for verification tokens |
| HXC-1599 | Feature | P3 | Completed | Implement Claude/Responses API-shape adapters for LLMOrchestrator front door |
| HXC-1600 | Feature | P3 | Completed | Implement error-classified LLM failover taxonomy + sandbox scheduling-hint contract |
| HXC-1609 | Feature | P3 | Completed | Implement scale-to-zero metered hot/cold billing state machine |
| HXC-1610 | Feature | P3 | Completed | Implement watchtower-style liveness/integrity prober for served instances |
| HXC-1611 | Research | P3 | Queued | Implement Karmada-pattern multi-cluster aggregation over SWIM+Raft+etcd (Phase 6 hook) |
| HXC-1612 | Feature | P3 | Completed | Implement OpenAPI request-validation admission middleware for control-plane APIs |

