# responses-proxy
- **Repo:** https://github.com/chutesai/responses-proxy
- **Language:** Rust
- **License:** UNKNOWN (no LICENSE file in repo; GitHub API reports no detected license)
- **Maturity:** active
- **Distributed-Computing Relevance:** low
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (API-surface adapter / inference-routing front door)
- **Effort:** S

## Purpose
A small, stateless Rust (axum + tokio) HTTP service that translates the OpenAI **Responses API** (`POST /v1/responses`, SSE streaming) into a backend **Chat Completions** request (default `https://llm.chutes.ai/v1/chat/completions`). It exists so OpenAI-Codex-style clients speaking the `wire_api = "responses"` protocol can drive Chutes.ai inference backends unchanged.

## Capabilities
- Bidirectional API shape translation: Responses payloads → Chat Completions request, and Chat Completions stream → Responses SSE events (`src/services/converter.rs`, `src/services/streaming.rs`, `src/handlers/responses.rs` — the ~2000-line core).
- Streaming with **fragmentation-safe tool calling**: buffers tool-call argument deltas that arrive before the function name/header to preserve correct event ordering.
- Dual event emission for client migration: modern `output_tool_call.*` plus legacy `function_call_arguments.*` events.
- XML-style tool-call salvage: converts stray XML tool calls emitted by some models into native function-call events (`src/utils/xml_tool_parser.rs`).
- Reasoning-model support: captures `reasoning_content`, emits `<think>`-compatible events, surfaces reasoning items (`docs/REASONING_SUPPORT.md`).
- MCP tool-result ingestion: accepts `role:"tool"` messages with `content:[{type:"output",...}]` per MCP spec, plus legacy `function_call_output`.
- Operational hardening: request validation/size limits, attachment rejection, in-memory model cache refreshed every 60s with case-normalization, **circuit breaker** (5 failures → 30s cool-down), graceful SIGINT shutdown, connection pooling (1024 idle/host), 10MB body limit, gzip compression.
- Stateless auth pass-through: forwards client `Authorization`/`x-api-key` bearer to backend; keeps no session state (`src/services/auth.rs`).

## Distributed-Computing Notes
Effectively none of the primary-interest areas are present. This is a single-process, horizontally-scalable, **stateless protocol shim**:
- No GPU validation/attestation (no GraVal), no TEE/sek8s, no E2EE transport — auth is a plain bearer pass-through over rustls TLS.
- No p2p/gossip (no fiber), no Bittensor subnet consensus/weights, no miner/validator logic, no scheduling/placement.
- The only "routing" is L7 request forwarding to one configured `BACKEND_URL`; there is no multi-backend selection, load balancing, or placement decision. Fault tolerance is limited to a local circuit breaker and timeouts.
- Relevance to distributed compute is indirect: it is the **client-facing inference ingress** that sits in front of a (separately implemented) decentralized GPU backend.

## HelixCluster Gaps Addressed
- **LLMOrchestrator** only: a clean, production-shaped reference for an OpenAI-compatible *ingress/translation* layer (Responses↔Chat-Completions, SSE streaming, tool-call/MCP event normalization, reasoning passthrough). Useful if Helix wants to expose an OpenAI-Responses-compatible endpoint in front of its inference serving.
- Does **not** address scheduler/Omega, resources, GPU (planned), federation (Phase 6), security/E2EE, Messaging/EventBus, discovery, leader/consensus, or miner/marketplace (Phase 8/8B). None of those concerns appear in the code.

## Dependencies
axum 0.7, tokio 1, reqwest 0.12 (rustls-tls, http2, stream), serde/serde_json 1, tokio-stream, futures, tower-http (gzip), chrono, dotenvy, env_logger/log. Deployment via Docker + Caddy (TLS). Pure Rust; no CUDA/Python runtime deps.

## Rationale
REFERENCE, not PORT/WRAP. The logic is valuable but **narrow and OpenAI-protocol-specific**, and it is Rust while Helix is Go — wrapping a separate Rust sidecar for pure API-shape translation adds operational weight with no distributed-systems payoff. If Helix needs Responses-API compatibility, the right move is to reimplement the converter/streaming state machine in Go inside LLMOrchestrator, using this repo's event-ordering and tool-call-buffering invariants (documented in `docs/IMPLEMENTATION_NOTES.md`, `docs/TOOL_CALLING.md`) as the spec. WRAP is plausible only if Helix wants the exact byte-for-byte Codex behavior fast.

## Risks
- **License UNKNOWN** — no LICENSE file; all-rights-reserved by default. This is a hard blocker for porting/copying code until clarified. Treat as read-only inspiration; do not vendor source.
- **Language mismatch**: Rust vs. Helix Go — porting means a reimplementation, not reuse.
- **Protocol coupling/drift**: tightly bound to OpenAI Responses + Chutes Chat Completions wire details (and the Codex client); upstream API churn would require ongoing maintenance.
- **Narrow scope**: zero coverage of GPU/consensus/p2p/TEE, so it cannot anchor any Phase 6/8/8B distributed-compute work.
- No CUDA/Python risk (clean), and stateless design is the one genuinely portable architectural idea.
