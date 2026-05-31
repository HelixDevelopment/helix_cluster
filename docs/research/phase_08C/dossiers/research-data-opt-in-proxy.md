# research-data-opt-in-proxy
- **Repo:** https://github.com/chutesai/research-data-opt-in-proxy
- **Language:** Python
- **License:** UNKNOWN (no LICENSE file in repo; GitHub license API returns 404; no SPDX declared in pyproject.toml — must be treated as all-rights-reserved / proprietary)
- **Maturity:** active (production-deployed on Vercel at research-data-opt-in-proxy.chutes.ai; not a fork; single-org internal tool; last push 2026-03-31)
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (inference routing/recording adjunct); secondarily a NEW `pkg/inferenceproxy` observability submodule + security (header sanitization, deterministic anonymization)
- **Effort:** M

## Purpose
A standalone FastAPI reverse proxy that transparently forwards OpenAI-compatible LLM traffic to `https://llm.chutes.ai` while opt-in recording per-request research traces for a Harvard prefix-caching study. It is an observability/data-capture sidecar in front of the Chutes inference plane — NOT a scheduler, miner, validator, or compute node. The one-liner is accurate but undersells how much Bittensor/Chutes routing metadata it parses out of upstream trace envelopes.

## Capabilities
- Transparent OpenAI-compatible passthrough proxy (`/v1/chat/completions`, `/v1/models`, arbitrary paths) preserving SSE streaming semantics for `stream=true`.
- Hop-by-hop header stripping (RFC 7230) on both request and response legs; `content-encoding` dropped because httpx auto-decompresses.
- Injects proxy-managed headers downstream→upstream: `X-Chutes-Research-OptIn` (discount routing signal/secret), `X-Chutes-Trace: true` (enables upstream trace envelopes), `X-Chutes-Correlation-Id` (per-request UUID), `X-Chutes-RealIP` (forwarded client IP). Caller-supplied versions of managed headers are stripped first so they can never be spoofed.
- Per-request correlation ID minted once and threaded through upstream header, stored DB row, and exported JSONL.
- Trace-envelope unwrapping: upstream wraps payloads in `{"trace": ...}` / `{"result": ...}` SSE events; the proxy unwraps them streamingly (`TraceSSEUnwrapper`) so downstream clients still see clean OpenAI payloads, while a parallel `StreamingTraceMetadataBuilder` harvests routing metadata from the same byte stream without buffering twice.
- Dual recording formats, independently toggled: (1) raw HTTP request/response capture into Postgres `raw_http_records`; (2) anonymized Qwen Bailian-style usage trace (`anon_usage_traces` + session/clock/hash-domain tables).
- Deterministic prompt anonymization pipeline: tiktoken `cl100k_base` tokenization → 16-token blocks → salted SipHash-2-4 (key derived via Blake2b from `ANONYMIZATION_HASH_SALT`) → domain-remapped to sequential integer IDs via bulk upsert. Yields prefix-cache-analysis-friendly block hashes without storing raw text.
- Object-storage archival (S3 or Vercel Blob) of full request/response bodies with SHA-256 checksums + size metadata; compacts JSON into dedicated columns and can omit the large `BYTEA` payload. `ARCHIVE_ON_INGEST` uploads immediately to keep Postgres lean.
- Secret-gated internal endpoints: `/internal/export/raw-http.jsonl` (streaming JSONL export), `/internal/archive/run` (cron-drainable batch archival + retention cleanup), `/internal/storage/compact-json` (bounded legacy migration). Auth via dedicated header secrets, with `hmac.compare_digest` constant-time comparison and `Authorization: Bearer` fallback for cron.
- Operational hardening: per-IP token-bucket rate limiter with bounded client tracking, request-body size cap (413), SSE record-buffer cap (forwards full stream, caps only the stored copy), configurable httpx timeouts, retention-based auto-deletion, async fire-and-forget recording tasks tracked on `app.state.container.pending_tasks`.

## Distributed-Computing Notes
- **Bittensor subnet metadata harvesting (the genuinely DC-relevant part):** `app/chutes_trace.py` parses Chutes trace SSE messages with regexes that extract `target=<instance_id> uid=<int> hotkey=<...> coldkey=<...>` for every routing attempt, plus a parallel error regex. It reconstructs the full list of `attempts`, marks which had errors, and selects the winning target (last successful attempt). This is effectively a **passive observer of subnet inference placement decisions** — it learns which miner UID/hotkey/coldkey/instance actually served each request, and which attempts failed. `UsageTraceCandidate` persists `target_uid`, `target_hotkey`, `target_coldkey`, `target_instance_id`, `target_child_id`, `chute_id`, `invocation_id`. This is a clean reference for how Chutes surfaces miner-selection telemetry over the wire.
- **Inference routing / serving:** It does NOT route or schedule. It is downstream of the real Chutes router (`llm.chutes.ai`), which performs the miner selection. The proxy only injects a discount/opt-in signal header that the upstream router may honor — i.e., a thin policy hint, not a placement engine.
- **No GPU validation/attestation (no GraVal), no p2p/gossip (no fiber), no consensus/weight-setting, no TEE (no sek8s), no E2EE transport.** Traffic to upstream is ordinary TLS; the only confidentiality measures are header redaction and salted hashing of stored prompts. There is no scheduler, no fault-tolerance-of-compute logic beyond retry-attempt observation in the upstream trace.
- **Prefix-caching research angle:** The 16-token-block SipHash scheme is purpose-built so a downstream analyst can detect shared prefixes across conversations without seeing plaintext — relevant to KV-cache reuse / prefix-cache hit-rate analysis, which is a real distributed-inference-efficiency concern.
- **Fault-tolerance posture is proxy-local only:** records are skipped for `status >= 400`, partial/incomplete streaming exchanges are dropped rather than persisted, recording failures are swallowed and logged (never block the client response), and the proxy returns 502 on upstream `RequestError`.

## HelixCluster Gaps Addressed
- **LLMOrchestrator (primary):** Helix's inference orchestration can borrow the transparent-proxy + per-request correlation-ID + trace-envelope-unwrap pattern to capture which backend (miner/instance) served each inference, with sink-side evidence (stored row + JSONL export) per the CLAUDE-1 usability mandate. The `target_uid/hotkey/coldkey` extraction is a direct template for a Helix "which compute node served this" audit trail.
- **security (secondary):** The managed-header sanitization model (strip caller-supplied trusted headers before re-injecting canonical values, constant-time secret comparison, X-Forwarded-For trust scoped to the proxy edge, automatic Authorization/cookie/API-key redaction before persistence) is a reusable pattern for Helix's E2EE/transport edge and for any Helix gateway that records traffic.
- **Deterministic anonymization primitive:** The Blake2b-keyed SipHash-2-4 block-hash + domain-remap-to-sequential-IDs technique fills a gap for any Helix telemetry/audit subsystem that must correlate repeated payloads (e.g., dedup, prefix-cache analytics, marketplace usage metering in Phase8/8B) without retaining sensitive content.
- **Does NOT address:** scheduler/Omega, pkg/resources, GPU validation (planned), federation (Phase6), discovery, leader/consensus, or miner/marketplace economics. It observes subnet routing rather than participating in it.

## Dependencies
- `fastapi>=0.115`, `uvicorn>=0.34` (ASGI app/server)
- `httpx>=0.27` + `h2>=4.2` (async upstream client, optional HTTP/2)
- `asyncpg>=0.30` (Postgres 16 — self-hosted on Hetzner via PgBouncer; Neon kept only for rollback)
- `orjson>=3.10` (fast JSON), `pydantic-settings>=2.6` (config)
- `tiktoken>=0.9` (cl100k_base tokenization for anonymized trace lengths/blocks) — pulls a Rust-backed wheel + downloads BPE vocab
- `boto3>=1.35` (S3 archival) and `vercel>=0.5` + Vercel Blob token (alternate archival/host)
- Pure-Python SipHash-2-4 implementation vendored in `app/siphash.py` (no native crypto dep)
- Deploy target: Vercel serverless (`api/index.py` entrypoint, `vercel.json`); cron-driven archival/retention.

## Rationale
REFERENCE, not PORT or WRAP, for three converging reasons:
1. **License is UNKNOWN/proprietary** — no LICENSE file, no SPDX, GitHub reports no license. Copying code into Helix (Apache-style Go project) would be a license violation; only the *patterns and protocol knowledge* are safely reusable.
2. **Language and runtime mismatch** — it is Python/FastAPI/Vercel-serverless built around `tiktoken`, `asyncpg`, Postgres, and S3/Vercel-Blob. Helix is Go-1.24 microservices. There is no shared binary or library surface; WRAP-ing it as a sidecar would import a Python+Postgres+tiktoken stack into a Go cluster for marginal gain.
3. **It is an observability/data-capture adjunct, not a distributed-compute primitive** — it neither schedules, attests GPUs, gossips, nor sets weights. Its high-value, Helix-relevant ideas (trace-envelope unwrapping to recover miner UID/hotkey/coldkey placement; deterministic salted-SipHash prefix-block anonymization; spoof-proof managed-header injection; cron-drainable archival with constant-time-authed internal endpoints) are best **re-implemented natively in Go** inside LLMOrchestrator and a new `pkg/inferenceproxy` observability submodule. Effort M reflects re-implementing the streaming SSE trace parser and the anonymization pipeline in Go.

## Risks
- **License risk (blocking for any copy):** UNKNOWN/all-rights-reserved. Do not vendor source; treat as read-only reference. Independently re-implement to avoid contamination.
- **Language mismatch:** Python→Go re-port required; no FFI path. tiktoken/cl100k_base has no first-class Go equivalent — a Go port must choose a compatible BPE tokenizer (e.g., a tiktoken-compatible Go lib) or the block-hash semantics will diverge from Chutes/Harvard's, breaking cross-comparability.
- **Coupling to Chutes-specific trace envelope:** The `{"trace": ...}/{"result": ...}` SSE format and the exact `target=... uid=... hotkey=... coldkey=...` log message grammar are private Chutes conventions parsed via brittle regexes; they can drift silently upstream and are undocumented outside this repo.
- **Privacy/compliance surface:** Captures full request/response bodies (with header redaction) and client IPs; any Helix adoption inherits GDPR/opt-in-consent obligations and a real-secret salt-management requirement (app refuses to start on placeholder salt — good, but a hard operational dependency).
- **Serverless/Postgres operational assumptions:** Fire-and-forget asyncio recording, Vercel cron archival, and PgBouncer endpoints are Vercel/Hetzner-specific; none map onto Helix's NATS/etcd/Raft control plane and would need wholesale replacement.
- **No mutation/E2E proof of the *distributed* claims:** tests are proxy/recorder/Postgres-focused (unit/integration/e2e against live llm.chutes.ai); they validate recording fidelity, not any compute-placement guarantee — consistent with it being an observer, not a scheduler.
