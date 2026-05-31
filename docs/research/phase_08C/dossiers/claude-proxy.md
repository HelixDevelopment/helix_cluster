# claude-proxy
- **Repo:** https://github.com/chutesai/claude-proxy
- **Language:** Rust
- **License:** UNKNOWN
- **Maturity:** active
- **Distributed-Computing Relevance:** low
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (inference routing / API translation shim)
- **Effort:** S

## Purpose
A stateless HTTP proxy (crate `claude_openai_proxy`, v0.1.14) that exposes the Anthropic Claude Messages API (`POST /v1/messages`, `/v1/messages/count_tokens`, `GET /health`) and translates requests/responses to/from an OpenAI-compatible `/v1/chat/completions` backend. It lets Claude Code and other Claude clients talk to OpenAI-shaped inference backends (Chutes' hosted `llm.chutes.ai`) while preserving Claude-style SSE streaming, tool calls, token counting, and model-friendly errors.

## Capabilities
- Bidirectional schema translation: Claude Messages format ↔ OpenAI chat-completions format (text, image, system, tool_use, tool_result content blocks).
- Claude-style SSE streaming and non-streaming (`stream: false`) response synthesis.
- Token counting via `tiktoken-rs` (`/v1/messages/count_tokens`).
- Model discovery + case correction by polling backend `/v1/models` (cached in `RwLock`, periodic refresh).
- Bearer token pass-through to the backend (forwards client `cpk_*` keys; rejects Anthropic `sk-ant-*` OAuth tokens); token masking for logs.
- Optional in-process **circuit breaker** (consecutive-failure threshold → open → half-open timeout) for backend fault protection (`ENABLE_CIRCUIT_BREAKER`).
- Configurable backend URL, timeout, host header override, gzip compression (`tower-http`).
- Two deployment shapes: native Axum/Tokio binary (Docker + Caddy TLS front), and a separate `worker/` crate targeting **Cloudflare Workers** (`worker = 0.0.11`, `wrangler`) as a WASM edge variant.
- Synthetic error formatting and "thinking"/reasoning block translation when the backend exposes reasoning output.

## Distributed-Computing Notes
Effectively none of the primary-interest primitives are present. This is a single-process, stateless L7 API gateway:
- **No GPU validation/attestation (GraVal), no miner/validator logic, no Bittensor consensus/weights, no TEE/sek8s, no E2EE transport, no p2p/gossip (fiber), no scheduler/placement.**
- The only distribution-adjacent features are: (1) the per-instance **circuit breaker** (local fault tolerance, not cluster-coordinated), and (2) horizontal statelessness — many replicas can sit behind Caddy/any L4 LB with no shared state, since model cache is per-instance and requests carry their own auth. It is a **leaf serving/routing shim** that would sit in front of, not inside, a decentralized inference fabric. It does not pick GPUs, miners, or routes itself — it forwards to one configured `BACKEND_URL`.

## HelixCluster Gaps Addressed
- **LLMOrchestrator (narrow):** provides a proven, compact reference for Anthropic-Messages ↔ OpenAI-chat-completions translation, SSE re-framing, and tiktoken-based token counting — useful if Helix ever needs to present a Claude-compatible front door to its inference layer.
- **Fault tolerance (minor):** the circuit-breaker state machine is a clean, testable pattern Helix could mirror in Go for backend health gating.
- Does **not** address Helix's core distributed gaps: scheduler/Omega, resources, GPU(planned) validation, federation (Phase 6), security/E2EE, Messaging/EventBus, discovery, leader/consensus, or miner/marketplace (Phase 8/8B). It is orthogonal to all of them.

## Dependencies
- `axum` 0.7, `tokio` 1 (multi-thread), `reqwest` 0.12 (rustls-tls, http2, stream), `serde`/`serde_json`, `tokio-stream`, `futures`, `tiktoken-rs` 0.6, `tower-http` (gzip), `dotenvy`, `env_logger`.
- Worker variant: `worker` 0.0.11 (Cloudflare WASM), `wrangler`, `@cloudflare/workers-types`.
- Runtime/infra: Caddy (TLS front), Docker / docker-compose, Node + `@anthropic-ai/claude-code` (client side, via `install_claude_code.sh`).

## Rationale
**REFERENCE**, not PORT/WRAP. The code is pure-Rust and small (~7k LOC) but solves a problem tangential to HelixCluster's distributed-computing mission: it is an API-shape adapter, not a compute, scheduling, or consensus component. Helix is Go; porting the whole proxy adds a Rust service for a feature Helix does not currently need. The valuable, extractable ideas are the translation/SSE/token-count logic and the circuit-breaker pattern, which can be re-implemented in Go inside LLMOrchestrator if/when a Claude-compatible endpoint is required. Wrapping the binary as-is is possible but introduces a second-language runtime for low strategic value.

## Risks
- **License: UNKNOWN** — no `LICENSE`/`COPYING` file and no `license` field in `Cargo.toml`; absent an explicit license, the default is "all rights reserved," which **blocks copying or porting** until clarified with Chutes. This is the dominant gating risk.
- **Language mismatch:** Rust vs Helix's Go core — direct code reuse not possible; only conceptual/algorithmic porting.
- **Scope mismatch:** zero overlap with decentralized GPU/miner/validator/consensus/TEE/E2EE goals; low strategic ROI.
- **Pre-1.0 / drift:** crate is v0.1.14 and the Cloudflare `worker` dep is 0.0.11 (very early); APIs churn.
- **Incomplete feature surface:** prompt caching, citations, server tools, audio, and URL/file-backed documents are explicitly not implemented — degraded fidelity for some Claude features.
