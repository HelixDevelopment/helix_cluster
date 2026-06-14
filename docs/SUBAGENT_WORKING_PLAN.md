# Subagent-Driven Working Plan — Path to a Zero-Issue Finish

| Field | Value |
|---|---|
| Created | 2026-06-14 |
| Owner | Autonomous overnight loop (coordinator + 3–4 parallel adversarial subagents per wave) |
| Status | ACTIVE |
| HEAD at authoring | `c147da2` on `main` (pushed to all 5 remotes) |
| Goal | Every `pkg/**` package adversarially proven; every known/latent issue resolved or consciously accepted with written justification; whole tree `build`+`vet`+`-race` green; docs in sync. **Nothing left unfinished, zero open issues.** |

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

### 2A. OPEN bug (tracked, not yet fixed) — MUST close before "done"
- **HXC-1825 — jwt RS256 fail-CLOSED interop defect.** `pkg/jwt` `verifyRSA` passes raw `crypto.Hash` `0/1/2` to `rsa.VerifyPKCS1v15` instead of `crypto.SHA256/384/512`, so it **rejects every standards-compliant RFC-7518 RS256 token** (only its own non-conformant self-signed tokens verify). Fail-CLOSED (rejects valid; cannot forge) → not a security hole. **Zero prod reachability today** (only the HMAC/HS256 path is wired, via `internal/gateway/auth.go`).
  - **Fix (coordinated — touches the package's own test vectors):** in `pkg/jwt/package.go` `verifyRSA`, use `crypto.SHA256/SHA384/SHA512` (add `import "crypto"`); THEN update the 3 self-signing test vectors that use `crypto.Hash(0)`: `package_test.go:102`, `package_test.go:278`, `temporal_keyset_test.go:294` → sign with `crypto.SHA256`. Then remove the `t.Skip("HXC-1825 …")` in `pkg/jwt/jwt_adversarial_test.go::TestAdversarial_RS256_RFC7518Compliant_MustVerify` and confirm it passes + the whole jwt suite is green. Mutation-prove, land, mark HXC-1825 Completed.
  - The HMAC path is fully sound (verified tonight: rejects `alg=none`, HS/RS confusion, tampered payload, expired/nbf, empty-sig) — do not touch it.

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

**Coordinator rule:** none of these may be silently dropped. Each ends the program as a Completed hardening HXC **or** an explicitly-accepted note (one line, "accepted because …") in the registry.

---

## 3. UNFINISHED WORK — the 47 unswept packages (the work-list)

Every package below has production code and an existing test but **no adversarial sweep yet**. Drive them in waves of 3–4 parallel subagents using the brief in Section 4. Ordered by priority = (wired × size × bug-class surface). **Do the WIRED + large ones first** — they have real importers, so a bug there has live blast radius.

### Tier 1 — large + wired control-plane core (highest priority; may need >1 wave each)
`scheduler` (10433 loc — the omega scheduler; total-order + concurrency + numeric), `swim` (8027 — gossip; total-order + concurrency + hostile-input), `wireguard` (6914 — CLAUDE-2 macOS parity + crypto + teardown race), `session` (5041 — state + concurrency), `resources` (4931 — CLAUDE-2 `/proc`/sysfs parity + numeric), `raft` (3294 — consensus; total-order + concurrency + persistence), `infra` (3055), `tracing` (2900), `metrics` (2839 — numeric/concurrency accumulators), `storage` (2688 — durability/atomicity, cf. hxcregistry), `security` (2706 — **fail-open surface**, highest care), `stonith` (2278 — fencing; fail-open/total-order).

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
- 2026-06-14: **Wave A16 — 3 more real bugs (21st–23rd) at HEAD `7fabdc2`.** HXC-1826 security (SPIFFE `/` fail-open, WIRED), HXC-1827 storage (SetSessionRouting non-atomic orphan route), HXC-1828 validator (float NaN range bypass). stonith swept clean. **Total: 23 real bugs fixed, 0 open issue Bugs.** Coverage ≈153/196 pkg. fixed.md=723. Next free HXC id: **HXC-1830**. New latent: security IsValidTrustDomain also accepts `:@?#`/non-ASCII (pinned); storage FileStore flat-vs-nested key collision (pinned).
