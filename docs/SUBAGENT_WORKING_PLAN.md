# Subagent-Driven Working Plan — Path to a Zero-Issue Finish

| Field | Value |
|---|---|
| Created | 2026-06-14 |
| Owner | Autonomous overnight loop (coordinator + 3–4 parallel adversarial subagents per wave) |
| Status | ACTIVE |
| HEAD (last update) | `dba7ceb` on `main` (pushed to all 5 remotes) |
| Bugs fixed | **41 real production bugs** + a test-health fix (inference). Open-issue Bugs: **0**. |
| Coverage | **★ pkg/ COMPLETE 196/196 ★** + **internal/ in progress (12 swept, 6 real bugs found there)**. Whole-tree `go build`+`go vet`+`go test` GREEN. internal/ done: gpu/gateway/security/node/scheduler/llm/policy/messaging/console/build/federation/health. |
| Goal | Every `pkg/**` package adversarially proven; every known/latent issue resolved or consciously accepted with written justification; whole tree `build`+`vet`+`-race` green; docs in sync; main + submodules pushed to all upstreams. **Nothing left unfinished, zero open issues.** |

This plan is the single source of truth for what remains. It is written so any
coordinator can pick it up and drive it to completion with the same discipline
used for the 19 real bugs already fixed tonight.

---

## 1. Where we are (physical evidence, not claims)

- **19 REAL production bugs fixed tonight**, each mutation-proven load-bearing (inject fault → adversarial test FAILS → revert byte-identical), independently re-verified by the coordinator, landed verified-green to all 5 remotes. See `docs/fixed.md` (718 completed) and the HXC registry (`data/hxc_registry.db`).
  - hlc(1753), antientropy(1759), offlinesync×2(1762), helixnet(1771), classads(1782), costtracker(1785), dataplane(1788), budgetcap(1791), ewmarank(1794), admissioncontrol(1795), forecast(1798), carbonsched(1802), rescorer(1810), edge(1814), gitops(1818), hxcregistry(1819), jobadmit(1822), smartrouter(1823). Plus computeproto(1765) additive DoS-hardening.
- **Adversarial-sweep coverage: 149 / 196 pkg packages (76%).** 47 packages remain unswept (Section 3).
- **Build state ROCK-SOLID:** `go build ./...` ✓, `go vet ./...` ✓, `-race` green across all swept packages (dataplane runs WITHOUT `-race` per its libzmq note).

### The dominant bug signature (use it to triage every remaining package)
**A `NaN`/`±Inf`/overflow value slipping a `x < 0`-style guard** — then either:
- poisoning an accumulator/total → **cap/budget bypass** (budgetcap, admissioncontrol, jobadmit), or
- breaking a float comparator's strict-weak-ordering → **mis-routing / nondeterministic selection** (ewmarank, carbonsched, rescorer, forecast, smartrouter).
**Remediation idiom:** widen `x < 0` guards to `math.IsNaN(x) || math.IsInf(x,0)`; for int sums use overflow-safe `limit - used` instead of `used + req`; for comparators sink non-finite to the worst value.

The four recurring bug classes (the triage checklist for every package):
- **(a)** value crossing a trust/replica boundary at a numeric/identity edge lacks a **total order** (equal-version/equal-score tie with no deterministic tiebreak).
- **(b)** **unguarded shared mutable state** read-by-many/written-by-few (map/accumulator without a mutex).
- **(c)** **deserialize/recursion DoS** on hostile external input (unbounded nesting/allocation/decompression).
- **(d)** **NaN/Inf/overflow numeric poison** bypassing a cap/threshold/min-select.
Plus **(e) fail-open** security/admission verdicts (edge, jwt-HMAC-checked, anticheat) — a verdict that defaults to ALLOW/VALID on malformed/empty/unknown input.

---

## 2. KNOWN ISSUES — open + latent (the "issues we may have")

### 2A. OPEN bugs (tracked, not yet fixed) — MUST close before "done"
- **NONE.** All discovered bugs are fixed and landed. (HXC-1825 jwt RS256 was the last open one — **CLOSED** at HEAD `b190232`: verifyRSA now uses `crypto.SHA256/384/512`, the 3 self-signing test vectors updated, reproducer un-gated as a regression guard, mutation-proven.)

### 2B. LATENT RISKS (not bugs today; each needs a conscious resolve-or-accept decision)
Recorded in `memory/` and/or per-package adversarial tests. For "zero issues," each must end as either (i) hardened with a mutation-proven fix, or (ii) explicitly accepted with a one-line written justification in the registry. Priority = blast radius (WIRED first).

| # | Package | Latent risk | Wired? | Suggested disposition |
|---|---|---|---|---|
| 1 | health | `CompositeChecker` folds **unknown status → Healthy** (HTTP 200). Fail-open footgun. | **YES (7 cmd importers)** | HARDEN: `default:` arm → Degraded; update doc + pin. `memory/health-compositechecker-unknown-status-fails-open.md` |
| 2 | grpcutil | demo Unary/Stream interceptors fail-open by design | latent if wired | ACCEPT (doc) — real gate is `AuthUnaryInterceptor`. `memory/grpcutil-demo-interceptors-fail-open.md` |
| 3 | netutil | `IsPrivateIP` excludes loopback/link-local/CGNAT/metadata → not a complete SSRF gate | callers must compose | ACCEPT + doc; audit callers for `169.254.169.254`. `memory/netutil-isprivateip-not-complete-ssrf-gate.md` |
| 4 | computeproto | raw `ReadComputeTask`/`GetRootAsTensor` panic on hostile FlatBuffers | mitigated | ACCEPT — decode untrusted bytes via `ReadComputeTaskSafe`. `memory/computeproto-use-readcomputetasksafe-for-untrusted-bytes.md` |
| 5 | chutes | attest HMAC `nodeID‖nonce` no domain separation (canonicalization collision) | not exploitable via Verify | HARDEN on a coordinated rollout (domain separator). `memory/chutes-attest-hmac-no-domain-separation.md` |
| 6 | constraints | near-`MaxInt` score + `colocationBonus` int overflow can split a Together pair | zero importers | ACCEPT (clean `ErrUnsatisfiable`) OR saturating-add; pinned. |
| 7 | federation | `relay.Forward` after unlock can reorder forwards | zero importers | ACCEPT — receiver is incarnation-monotonic (benign); chaos suite green. |
| 8 | backfill | `AssignmentFor` first-match on duplicate JobID; `EndTime=start+WallTime` no overflow guard | zero importers | ACCEPT (doc precondition) OR add guards; pinned. |
| 9 | cloudspot | Azure poller picks first-array event, not earliest-deadline | 1 importer (wireguard) | ACCEPT (real Azure doesn't emit competing events) OR min-deadline select; pinned. |
| 10 | auctionplace | empty-id rejection is a raw error, not an `errors.Is` sentinel | zero importers | ACCEPT or add `ErrEmptyBountyID`; pinned. |
| 11 | flowcontrol | duplicate live request-ID leaks a seat | zero importers (doc-forbidden) | ACCEPT (documented precondition); pinned. |
| 12 | fmea | int RPN overflow inverts ranking IF caller skips `Validate`/`ValidateCatalog` | zero importers | ACCEPT (contract requires Validate first) OR saturate in `HighRisk`; pinned. |
| 13 | balancemonitor | caller-supplied **NaN floor** silences the low-balance alarm | zero importers | ACCEPT (caller bug, documented) OR sanitize floor in `NewMonitor`; pinned. |
| 14 | fedtrust | zero-value `TrustConfig{}` is **allow-all** (operator footgun) | zero importers | ACCEPT (documented) OR fail-closed default; pinned. |
| 15 | etcd | flat-key prefix containment (`n1`⊂`n10`) if a caller uses a bare point-key with `GetPrefix` | 6 importers (all verified safe) | ACCEPT — all current callers scan `prefix+"/"`; pin guards new callers. |
| 16 | edge | `BatteryAbove(NaN)` degrades to `BatteryAbove(0)` (policy-config, not metric input) | edge fixed; this part latent | ACCEPT (config not untrusted input); pinned. |

| 17 | metrics | `Histogram.Observe(NaN/±Inf)` poisons the `_sum` exposition forever (buckets immune; count advances) | **WIRED (11 cmd importers)** | HARDEN: `if math.IsNaN/IsInf{return}` in Observe — but a WIRED behavior change, defer to review per the rule below. |
| 18 | metrics | `Counter.Add` accepts negative deltas (non-monotonic); int64 overflow wraps | WIRED | ACCEPT (type doesn't document monotonicity); pinned. |
| 19 | session | `Terminate` never reaps the map entry; no Delete/reaper → unbounded map growth for a long-lived Manager | **WIRED (internal/session gRPC)** | HARDEN: add bounded-retention/reap API before high-churn prod; no removal currently promised. Pinned. |
| 20 | security | `IsValidTrustDomain` also accepts `:@?#` and non-ASCII (port/userinfo/query in a trust domain) | WIRED | HARDEN in same pass as the `/` fix already landed (reject `:@?#%` + non-ASCII); pinned by `TestValidate_LatentRiskTrustDomains`. |
| 21 | storage | `FileStore` flat key `"a"` and nested `"a/b"` cannot coexist (file-vs-dir collision); `MemoryStore` allows both | zero importers | ACCEPT (opaque-key footgun) OR flat-encode keys; pinned. |

**Coordinator rule:** none of these may be silently dropped. Each ends the program as a Completed hardening HXC **or** an explicitly-accepted note (one line, "accepted because …") in the registry. **For any latent whose fix changes WIRED runtime behavior (rows 1, 17, 19, 20), the hardening is DEFERRED to human review per the overnight zero-risk mandate** — the precise one-line fix is recorded and ready, but not applied unilaterally overnight.

### 2C. OPERATIONAL / INFRA known-issues (outside the code, must be resolved for "done")
- **Pre-existing docs gap — 5 stale PDFs.** `docs_chain verify tracked_docs` reports STALE `[arch_pdf archdia_pdf mvp_pdf npbound_pdf ph02arch_pdf]` (architecture/mvp docs untouched tonight). `sync` reports in-sync yet `verify` disagrees = a surfaced both-dirty conflict docs_chain will not silently merge. **Resolve:** a human inspects each source/export pair and re-renders or accepts. NOT force-overwritten overnight (zero-risk).
- **3 dirty submodules NOT authored by this loop — need human review before commit/push.** (a) `EventBus` — `go.mod`/`go.sum` (dep tidy churn; CVE bump already committed locally). (b) `containers` — `go.mod`/`go.sum` (tidy churn). (c) `helixqa` — substantive uncommitted work this loop did NOT write: `pkg/bridge/sidecarutil/framing.go(+_test)`, untracked `pkg/challengegen/`, `.codegraph/` index artifacts, and many `tools/opensource/*` nested-submodule pointer drifts. **Resolve:** the author/human reviews and commits these intentionally; the loop must not bundle unknown code into a commit and push it to upstreams. (The main repo — all of this loop's work — IS committed and pushed to all 5 remotes every wave.)
- **Pre-existing test FLAKE — `pkg/build` `TestInFlightCancellation` (build_test.go:535).** Fails ~1/5 under `-race -count=30`; reproduces on the pristine tree (no tonight's changes) — a timing race in the test's own `blockingBuilder`/cancel logic, NOT production code. **Resolve:** make the test deterministic (inject the cancel/clock rendezvous). Not weakened/touched tonight (out of scope, anti-bluff: don't edit an existing test to hide a flake without understanding it). The default `-count=1` run is green.
- **Subagent shared session/rate-limit** (resets ~per-window, e.g. 8:30pm Europe/Moscow). When hit, subagents return 0 tokens mid-work and may leave an incomplete/hanging test file. **Rule:** never land a rate-limited subagent's output unverified — independently compile+run+mutation-check, and if it hangs/can't be verified, REMOVE it and re-queue the package. (Done for raft + resources — re-queued, see Section 3 Tier 1.)

---

## 3. UNFINISHED WORK — the 47 unswept packages (the work-list)

Every package below has production code and an existing test but **no adversarial sweep yet**. Drive them in waves of 3–4 parallel subagents using the brief in Section 4. Ordered by priority = (wired × size × bug-class surface). **Do the WIRED + large ones first** — they have real importers, so a bug there has live blast radius.

### Tier 1 — large + wired control-plane core (highest priority; may need >1 wave each)
SWEPT this session (clean unless noted): ~~session~~ ✓, ~~metrics~~ ✓, ~~storage~~ ✓ (HXC-1827 fixed), ~~security~~ ✓ (HXC-1826 fixed), ~~stonith~~ ✓.
**RE-QUEUED (subagents rate-limited mid-work; files removed unverified):** `raft` (3294 — consensus; total-order + concurrency + persistence — its test compiled+passed but had NO mutation-proof, so removed; re-sweep fully), `resources` (4931 — CLAUDE-2 `/proc`/sysfs parity + numeric — its test HUNG on `sysctl`/`vm_stat`, removed; re-sweep).
**STILL UNSWEPT:** `scheduler` (10433 loc — the omega scheduler; total-order + concurrency + numeric), `swim` (8027 — gossip; total-order + concurrency + hostile-input), `wireguard` (6914 — CLAUDE-2 macOS parity + crypto + teardown race), `infra` (3055), `tracing` (2900).

### Tier 2 — mid-size services (1–2k loc; one wave each, 3–4 at a time)
`build` (2104), `pool` (1994), `device` (1847), `chaosexp` (1835), `grafanadash` (1619), `fedtopology` (1610), `tieredcache` (1428 — eviction boundary + concurrency), `jwt`-already-swept (close HXC-1825), `quantization` (1367 — numeric), `agentprovision` (1036).

### Tier 3 — leaf libraries (≤1k loc; batch 4 per wave)
`dst` (974), `inference` (886), `timefault` (838), `qualitygate` (812), `tiersec` (799), `covgate` (752), `retry` (729 — backoff numeric/overflow), `bootstrap` (706), `redundantexec` (667), `chutesaccount` (645), `thermalwarm` (633), `provider` (635), `errors` (620), `phase7matrix` (609), `log` (607), `doublecrypt` (592 — crypto), `crypto` (572 — **crypto, high care**), `validator` (561 — **fail-open validation surface**), `tofu` (472), `local` (441), `benchmark` (409), `helixtask` (346), `compliancedoc` (314), `workerpool` (311 — concurrency/over-commit), `e2eebench` (281), `context` (176).

> Note: `pkg/testing` has no prod `.go` (scaffolding) — skip. Count remaining = **47**.

### Beyond pkg/: the next frontiers (after pkg/** is 100%)
- **`internal/**` and `cmd/**`** — wired entrypoints; same four-class sweep, prioritized by those with real request/auth/admission paths (`internal/gateway`, `internal/scheduler`, `internal/node`, `internal/messaging`).
- **CLAUDE-2 parity audit** — enumerate every `_linux.go`/`_other.go`/`_mock.go` pair for a real-operation feature and confirm a real macOS equivalent exists (hotspots: `pkg/resources`, `pkg/wireguard`, `pkg/gpu`→`internal/gpu`). A `!linux` stub for a real-operation feature is a CLAUDE-1 PASS-bluff and a defect.

---

## 4. HOW each subagent works (the proven, mandatory brief)

Per wave: coordinator dispatches **3–4 subagents in parallel**, each owning ONE package, conflict-free. Each subagent:
1. Reads ALL non-test `.go` in its package; notes the **documented contract** (concurrency claims, ordering, numeric handling, validation, fail-open/closed intent).
2. Writes `pkg/X/X_adversarial_test.go`; probes the four classes (a)/(b)/(c)/(d) + (e) fail-open, **SINK-SIDE** (assert the actual winner/total/verdict/order, never "no panic").
3. Runs `go test -race -count=2` for the package.
4. **Three-way discrimination (mandatory, honest):**
   - **REAL BUG** = code promises X but violates X → propose a minimal load-bearing fix.
   - **DOCUMENTED CONTRACT** = code disclaims the guarantee (e.g. "not safe for concurrent use") → that is honest; PIN it with a test, no fix. (metering/gpupool/constraints were these.)
   - **LATENT RISK** = fragile but not currently reachable/violating → note it, no fix.
5. If REAL bug: apply the fix TEMPORARILY → full `-race` suite green → **mutation-prove** (fix in → test PASSES; remove only the fix line → test FAILS, with pasted evidence) → **revert prod byte-identical via Edit (NEVER `git checkout`)** → confirm `git diff` empty.
6. **Never** `git add`/`git commit`. Leave only the untracked `*_adversarial_test.go`. Report blast radius (non-test importers).

### Coordinator landing protocol (per wave, after subagents return)
1. **Independently verify** every claim via live `git diff` / `go test` — never trust the report. Confirm prod is HEAD-clean for every package (`git status --porcelain`).
2. For each REAL bug: re-read the bug site, apply the fix in the main stream, mutation-prove it AGAIN independently (revert→FAIL, restore→green), confirm whole-tree `go build ./...` + `go vet ./...`.
3. Register HXC ids (`/tmp/hxc-registry`, `HXC_DB=data/hxc_registry.db`; types Bug/Feature/Task/Research/Docs; priorities P0–P3). Bugs → Bug; no-bug evidence → Task.
4. `rm -f .git/index.lock` (auto-commit daemon contention) → `git add` ONLY the fix files + test files + `data/hxc_registry.db` → commit with a forensic message → mark HXC Completed with the commit sha.
5. Render docs: `python3 scripts/docs/db_to_md.py` → commit `docs/fixed.md` `docs/issues.md`.
6. Push all 5 remotes: `for r in origin github gitlab gitflic gitverse; do git push $r main; done`.
7. Update `.remember/remember.md` (HEAD, fixed.md count, bug list, next HXC id) and `memory/` for any new latent risk.

### Anti-bluff invariants (non-negotiable, from CLAUDE-1)
- A test that passes on a non-functional feature is a PASS-bluff = §7.1-class defect. Every test must prove **sink-side** behavior.
- Verified-green only reaches `main`. `git diff --stat` on prod files must be empty before committing a test-only item.
- A failing reproducer that ships before its fix must be `t.Skip`-gated with its HXC id (so the default suite stays green) — never left red, never faked green.
- No `t.Skip` to hide an unimplemented port (CLAUDE-2). A genuine no-equivalent skip must be justified in writing.

---

## 5. DEFINITION OF DONE (the zero-issue finish line)

The program is finished only when ALL of the following hold and are evidenced:
1. **Coverage:** every `pkg/**` package (then `internal/**`, `cmd/**`) has a mutation-proven adversarial test. Target: 196/196 pkg, then internal/cmd.
2. **Open issues closed:** HXC-1825 (jwt RS256) fixed + landed; `docs/issues.md` contains **no open Bug** that represents a real defect (only Features/Tasks/Research deliberately deferred, each justified).
3. **Latent risks resolved:** every Section-2B row ends as a Completed hardening OR an explicitly-accepted registry note. None silently dropped.
4. **CLAUDE-2 parity:** no `!linux` mock/stub backs a real-operation feature without a real per-OS equivalent; macOS host sweep green.
5. **CLAUDE-3 docs-sync:** `docs_chain verify` green (or the documented honest SKIP-with-reason); README/CLAUDE/AGENTS/QWEN/FOUNDATION_PACKAGES/CHANGELOG + diagrams/SQL in sync; this plan kept current.
6. **Build gate:** whole-tree `go build ./...` ✓, `go vet ./...` ✓, `-race -count=2` green for every swept package (dataplane without `-race`), on the host OS.
7. **All 5 remotes** at the same green HEAD.

### Suggested execution order to the finish
1. Close **HXC-1825** (jwt) — small, well-specified, removes the one open Bug.
2. Resolve **WIRED latent risks** first: health (#1), then chutes (#5), then audit netutil/grpcutil callers.
3. Sweep **Tier 1** (scheduler, swim, raft, security, storage, resources, wireguard, session, …) — 3–4 subagents/wave, big ones may need multiple waves.
4. Sweep **Tier 2**, then **Tier 3** to 196/196.
5. Resolve remaining latent risks (accept-or-harden) and CLAUDE-2 parity audit.
6. Extend sweep to `internal/**` and `cmd/**`.
7. Final: docs_chain verify, whole-tree gate, single green HEAD on all remotes; flip this plan's Status to DONE.

---

## 6. Living log (update every wave)
- 2026-06-14: Plan authored at HEAD `c147da2`. 19 bugs fixed, 149/196 pkg swept, 1 open issue (HXC-1825), 16 latent risks catalogued. Next free HXC id: **HXC-1826**.
- 2026-06-14: **HXC-1825 (jwt RS256) FIXED & landed** at HEAD `b190232` (20th real bug) — verifyRSA now uses crypto.SHA256/384/512; 3 test vectors updated; reproducer un-gated as a regression guard; mutation-proven. **Open known-issue Bugs: 0.** fixed.md=719.
- 2026-06-14: **DECISION — health (latent #1) hardening DEFERRED to human review.** Per the overnight "safest/most-stable/zero-risk" mandate, `CompositeChecker` unknown→Healthy fold will NOT be changed unilaterally because it alters documented health-aggregation semantics across 7 wired cmd binaries (a readiness-probe behavior change). Stays tracked (memory note + plan row 1); the one-line fix (`default:` → Degraded + doc + pin flip) is ready for a human-approved change. Same posture applies to any latent whose fix changes WIRED runtime behavior.
- 2026-06-14: **Wave A16 — 3 more real bugs (21st–23rd) at HEAD `7fabdc2`.** HXC-1826 security (SPIFFE `/` fail-open, WIRED), HXC-1827 storage (SetSessionRouting non-atomic orphan route), HXC-1828 validator (float NaN range bypass). stonith swept clean. **Total: 23 real bugs fixed, 0 open issue Bugs.** Coverage ≈153/196 pkg. fixed.md=723. New latent: security IsValidTrustDomain also accepts `:@?#`/non-ASCII (pinned); storage FileStore flat-vs-nested key collision (pinned).
- 2026-06-14: **Wave A17 — partial (subagent rate-limit hit).** metrics + session swept CLEAN (HXC-1830/1831, mutation-proven, NO bug). raft + resources subagents rate-limited mid-work → unverified files REMOVED, packages RE-QUEUED (raft passed but no mutation-proof; resources HUNG on sysctl). New latents: metrics histogram NaN→`_sum` poison + Counter.Add negatives (WIRED); session unbounded growth (Terminate no reap, WIRED). fixed.md=725.
- 2026-06-14: **Waves A18–A20 — 3 more real bugs (24th–26th) + a test-health fix.** HXC-1832 retry (backoff overflow hot-loop), HXC-1835 tiersec (Sscanf fail-open clearance), HXC-1839 qualitygate (NaN fail-open, WIRED CI gate). HXC-1840: fixed 2 PRE-EXISTING stale tests in pkg/inference (encoded a retired internal/llm stub) — the only red package on main. Swept clean: jobadmit/voting/jwt-closed/crypto/workerpool/tieredcache/doublecrypt/covgate/pool/quantization/inference/metrics/session/security/storage/validator/stonith.
- 2026-06-14: **★ WHOLE-TREE TEST HEALTH GREEN ★ (DEFINITION OF DONE #6, test level).** `go test ./pkg/... ./internal/... ./cmd/...` ALL exit 0 — 214 pkg packages (0 without tests), all internal, 24 cmd (2 no-test). After HXC-1840 there is **no known red test package on main**. (Note: this is the non-`-race` whole-tree run; per-package `-race -count=2` is run on every swept package as it lands.) **26 real bugs, 0 open-issue Bugs, coverage ~166/196 pkg.** Next free HXC id: **HXC-1843**.
- 2026-06-14: **Waves A21–A22 — 1 more real bug (27th) + 7 clean sweeps.** HXC-1847 timefault (abs64 MinInt64 overflow → split-brain lease). Swept clean: raft (hashicorp wrapper), device (CLAUDE-2 sysctl-verified), dst (deterministic harness), provider, devicemap (pure converter), fedtopology (deterministic topology), thermalwarm. **27 real bugs, 0 open issues, coverage ~174/196. Next free HXC id: HXC-1851.** STILL UNSWEPT: scheduler, swim, wireguard (CLAUDE-2), tracing, infra, resources (re-queue — hangs on sysctl, needs a non-shelling test), grafanadash, chaosexp, agentprovision, build, bootstrap, benchmark, chutesaccount, compliancedoc, context, errors, helixtask, local, log, phase7matrix, e2eebench.
- 2026-06-14: **Waves A23–A24 — 4 more real bugs (28th–31st).** HXC-1851 tracing (finished-span duration grows w/ wall-clock), HXC-1852 agentprovision (dup-ID double-provision + silent eviction), HXC-1855 swim (handleDead self-dead, no self-refute — WIRED 6 importers, safety), HXC-1856 build (clone aliases BuildArgs vs documented deep-copy — WIRED). Swept clean: chaosexp, errors, tracing-pins, log, bootstrap. **31 real bugs, 0 open issues, coverage ~182/196. Next free HXC id: HXC-1859.** New latents: swim uint32 incarnation wrap (hlc class); errors typed-nil interface trap; chaosexp non-monotonic-nowFn metric; build pre-existing test flake (above). STILL UNSWEPT: scheduler, wireguard (CLAUDE-2), infra, resources (re-queue, hangs on sysctl), grafanadash, benchmark, chutesaccount, compliancedoc, context, helixtask, local, phase7matrix, e2eebench.
- 2026-06-14: **Waves A25–A27 — 4 more real bugs (32nd–35th).** HXC-1863 scheduler (map-iteration nondeterministic placement on score ties — WIRED gRPC), HXC-1864 infra (Scale negative-replicas panic — WIRED), HXC-1865 local (NaN slips validate → TCO poison stuck-local), HXC-1867 context (Detach leaks parent Deadline despite 'never cancelled'). Swept clean: wireguard (CLAUDE-2 darwin parity VERIFIED REAL wireguard-go — not a stub), grafanadash, helixtask, chutesaccount, raft, device (CLAUDE-2 sysctl-verified), dst, provider, devicemap, fedtopology, thermalwarm, phase7matrix, benchmark, compliancedoc, e2eebench. **35 real bugs, 0 open issues, coverage ~194/196 pkg. Next free HXC id: HXC-1871.** ONLY `resources` left (its sweep hung shelling to sysctl — re-dispatched with anti-hang guidance: exec.CommandContext+timeout, no blocking). New latents: benchmark CV NaN→silent gate-pass; compliancedoc no-verdict PASS-bluff risk; e2eebench empty-key degenerate-accept.
- 2026-06-14: **★ MILESTONE — pkg/ adversarial coverage COMPLETE 196/196 ★** at HEAD `afee6c6`. HXC-1871 resources (CLAUDE-2 darwin parity VERIFIED REAL, last package). Final gate GREEN: `go build ./...` ✓, `go vet ./...` ✓, `go test ./pkg/...` exit 0 (214 pkg, 0 fail, 0 no-test). **35 real bugs fixed, 0 open-issue Bugs, 190 adversarial test files.** Next free HXC id: HXC-1872. **REMAINING for full DONE:** (1) internal/** + cmd/** adversarial sweep (next frontier); (2) resolve/accept the ~24 catalogued LATENT RISKS (§2B) — several WIRED ones deferred to human review per zero-risk mandate; (3) fix the build test-flake (§2C); (4) human review of the 3 dirty submodules + 5 stale arch PDFs (§2C); (5) the WIRED-behavior-change latents (health/metrics/session/swim-uint32) await human sign-off.
- 2026-06-16: **internal/** sweep — 6 real bugs (36th–41st).** HXC-1872 internal/node (gRPC shared-pointer data race), HXC-1876 internal/policy (rego non-numeric health remote fail-open), HXC-1877 internal/messaging (Enqueue send-on-closed teardown race), HXC-1880 internal/build (SubmitBuild BuildArgs alias race), HXC-1881 internal/federation (scoreCell NaN breaks selection total-order), HXC-1882 internal/health (aggregate no-default folds unknown→Healthy, fail-open /readyz+/livez). Swept clean: internal/gpu (CLAUDE-2 REAL), gateway, security, scheduler, llm, console. **41 real bugs, 0 open issues. Next free HXC id: HXC-1884.** STILL UNSWEPT internal/: schema, chaos, wireguard, advisory, backup, session, trust, costbroker, verifier. Then cmd/**.
- 2026-06-14: **CLAUDE-3 docs sync + infra at HEAD `dba7ceb`.** `docs_chain sync --all` regenerated 62 export files (README/CHANGELOG/foundation/db-schema/user-manual/fixed/issues/consensus/gap_audits → docx/html/pdf), committed. Surfaced: 5 pre-existing stale arch/mvp PDFs (both-dirty conflict, human-resolve) + 3 dirty submodules NOT authored by this loop (EventBus/containers go.mod-sum tidy; helixqa substantive framing.go/challengegen — human review before commit/push). Main repo pushed to all 5 upstreams each wave. Next free HXC id: **HXC-1832**.
