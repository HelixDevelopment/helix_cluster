# chutes-autopilot
- **Repo:** https://github.com/chutesai/chutes-autopilot
- **Language:** Rust
- **License:** UNKNOWN (no LICENSE file, no `license` field in Cargo.toml, no SPDX header; GitHub API reports `license: null` → all-rights-reserved / proprietary)
- **Maturity:** active (not a fork; pushed 2026-02-19; v0.1.0; the README itself notes the routing problem has "since been solved natively in core Chutes via PR #103", positioning this repo as standalone/experimental tooling)
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (inference routing/failover patterns); secondary reference for pkg/scheduler ranking heuristics
- **Effort:** S

## Purpose
A single-binary, high-performance, OpenAI-compatible HTTP router/reverse-proxy in front of the Chutes LLM fleet (`llm.chutes.ai`). It picks one chute (model instance) per request via live-utilization ranking, an explicit comma-separated preference list, or direct passthrough, rewrites the `model` field, and streams the upstream response back unbuffered with retry-based failover. It deliberately does NOT transform prompts or implement product logic — it is purely a selection + proxy layer.

## Capabilities
- OpenAI-compatible `POST /v1/chat/completions` reverse proxy with true streaming passthrough (no buffering; `reqwest` stream → `axum` body).
- Three routing modes resolved per request from the `model` field: `chutesai/AutoPilot` alias (global ranked list), comma-separated preference list (ordered failover, dedup, `MAX_MODEL_LIST_ITEMS` cap), and direct single-model passthrough.
- Background control-plane refresh loop (~5s) pulling `GET api.chutes.ai/chutes/utilization`; builds an in-memory ranked candidate snapshot.
- Model-catalog allowlist refresh (~5min) from `GET llm.chutes.ai/v1/models`; last-known-good retention on fetch failure; fail-fast rejection of unknown/typo model ids when allowlist is fresh.
- Deterministic ranking function with explicit tie-breakers (score → instance count → utilization → rate-limit ratio → name).
- Sticky per-client selection (keyed by `Authorization: Bearer` token, else peer IP) with TTL and bounded entry count; rotates on failure.
- Safe streaming failover: retry next candidate only on pre-byte failures (connect fail, header timeout, first-body-byte timeout, 503-before-stream); never retry after any byte sent; pass 429 straight through (treated as user-caused).
- Readiness gating: `/readyz` stays 503 until both a fresh non-empty allowlist and a fresh non-empty candidate snapshot exist; `/healthz` liveness; `/metrics` Prometheus text format (request totals/active, snapshot/allowlist freshness, selections, failover reasons).
- Hardened deployment: non-root uid 10001, `debian:bookworm-slim`, pinned Rust toolchain, Caddy TLS front, configurable proxy-trust CIDRs for `x-forwarded-for`.
- Offline end-to-end smoke harness (`src/bin/smoke.rs`) spinning up a stub upstream + the router, validating alias streaming, preference failover, first-byte-timeout failover, and direct passthrough with no real credentials.

## Distributed-Computing Notes
- **Scheduling/placement:** This is the most relevant facet. The ranking engine (`rank_candidates` / `sort_ranked_candidates`) is a lightweight, deterministic, utilization-aware placement heuristic: `free_capacity = active_instance_count * (1 - util)`, plus a `scale_bonus` for scalable chutes, minus a rate-limit penalty `active_instance_count * rl * 2.0`, with EWMA-style smoothing over 5m/15m/1h utilization and rate-limit windows. This is a clean reference for capacity-/load-aware candidate ranking with hysteresis (stickiness) and tie-break determinism — conceptually adjacent to Helix's Omega scheduler scoring, though far simpler (no bidding, no optimistic-concurrency placement, single resource dimension).
- **Fault tolerance:** Pragmatic streaming-safe failover model — the "retry only before first byte, never after" rule is a correct and reusable pattern for any streaming proxy, including Helix inference transport.
- **GPU validation / attestation (GraVal):** NOT present as real functionality. The code treats a `-TEE` name suffix purely as an eligibility heuristic (`is_model_catalog_eligible` falls back to `name.ends_with("-TEE")` when the allowlist is empty). The `tests/testdata/chutes_live/evidence_*` fixtures and `evidence_probe_snapshot.rs` are documentation-only: they record a *known blocker* (every live TEE evidence probe returns HTTP 400 requiring `chutes_version >= 0.6.0`), proving the router does not itself perform GraVal/TEE attestation or nonce verification. No cryptographic attestation, no confidential-compute logic.
- **p2p/gossip, consensus/weights, Bittensor:** None. No SWIM, no Raft, no fiber, no subnet/weight logic. Control-plane state is a single in-memory snapshot per replica; horizontal scale is stateless replicas behind Caddy (sticky map is per-replica, not shared).
- **E2EE inference transport:** None — TLS only (rustls via reqwest, Caddy for ingress). No end-to-end encryption of inference payloads.
- **Serving:** Pure routing/proxy; does not host or run models.

## HelixCluster Gaps Addressed
- **LLMOrchestrator (primary):** Reference design for an OpenAI-compatible front door that does utilization-aware model selection, ordered preference-list failover, sticky client→model affinity, and streaming-safe retry semantics. These patterns map directly onto Helix's inference-routing layer.
- **scheduler/Omega (secondary, conceptual only):** The deterministic capacity-minus-penalty scoring with multi-window EWMA smoothing and explicit tie-breakers is a useful, well-commented reference for placement scoring — but it is single-dimension and lacks Helix's bidding/optimistic-concurrency model.
- **resources / GPU (planned):** Weakly relevant — shows how to consume an external utilization feed to drive placement, but contributes no GPU validation/attestation despite the GraVal interest area (TEE handling is stubbed/heuristic only).
- **observability:** Clean example of Prometheus metrics + structured `req_id` logging with sensitive-header redaction.
- Does NOT address: federation (Phase 6), security/E2EE, miner/marketplace (Phase 8/8B), discovery, leader/consensus — none of these exist here.

## Dependencies
- Runtime: `axum` 0.7, `tokio` 1, `reqwest` 0.12 (rustls-tls, stream, json), `serde`/`serde_json`, `http`, `ipnet`, `uuid`, `prometheus` 0.13, `tracing`/`tracing-subscriber`, `anyhow`, `futures-util`.
- Dev: `proptest`, `tower`, `http-body-util`, `flate2`.
- External services (hard-coded defaults, overridable): `api.chutes.ai/chutes/utilization`, `llm.chutes.ai/v1/models`, `llm.chutes.ai` backend. Deployment uses Caddy + Docker Compose. No CUDA/Python/native deps — pure Rust, easy to build.

## Rationale
REFERENCE, not PORT/WRAP, for three converging reasons: (1) **License is UNKNOWN/proprietary** — there is no grant of any kind, so copying code into HelixCluster is legally impermissible; only the *ideas/patterns* are usable. (2) **Language mismatch** — it is Rust and Helix is Go, so even a permissive license would require reimplementation, not reuse. (3) **Scope mismatch** — its value to a distributed-cluster OS is the routing/failover/ranking *design*, which is small, self-contained, and easily reimplemented in Go (Effort S) inside LLMOrchestrator. The repo's own README concedes the feature is superseded by native Chutes work, reinforcing "study the patterns, don't depend on the artifact." The headline distributed-compute interest areas (GraVal attestation, p2p/gossip, Bittensor consensus, TEE/E2EE) are absent or stubbed here.

## Risks
- **License (blocking for any code reuse):** no license = all rights reserved; treat as look-but-don't-copy. Reimplement clean-room from the documented algorithm only.
- **Language mismatch:** Rust→Go reimplementation required; no binary/library integration path.
- **Superseded upstream:** maintainers state the native Chutes API (PR #103) is the long-term path, so this repo may stale or diverge; do not couple to it.
- **Hard-coded Chutes endpoints:** logic assumes the Chutes utilization-feed schema; not directly reusable against a generic fleet without adapting the data source.
- **Per-replica state:** sticky affinity and snapshot are in-memory and not shared across replicas — naive multi-replica deployment yields inconsistent stickiness; Helix would need a shared store.
- **TEE is illusory here:** relying on the `-TEE` suffix as an attestation signal would be a CLAUDE-1 PASS-bluff risk — it proves nothing about confidential compute; real GraVal/TEE verification must come from elsewhere.
