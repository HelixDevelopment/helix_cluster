# squad-api
- **Repo:** https://github.com/chutesai/squad-api
- **Language:** Python
- **License:** MIT
- **Maturity:** active
- **Distributed-Computing Relevance:** medium
- **Portability Verdict:** REFERENCE
- **Target Helix Module:** LLMOrchestrator (agent invocation/sandboxing) + pkg/scheduler (Kueue-style job admission pattern)

## Purpose
squad-api ("Agents, on chutes.ai", by Rayon Labs) is a FastAPI control plane for defining and running sandboxed AI agents on top of the Chutes decentralized-GPU platform. Each agent invocation is packaged as code + inputs and executed as an isolated Kubernetes Job; the agent itself is a `smolagents` `CodeAgent` whose LLM/VLM/image/TTS/embedding calls and web/X searches are served by remote Chutes inference endpoints and Bittensor subnets. It is a *consumer* of decentralized GPU compute, not a provider of it.

## Capabilities
- Agent definition + config store (Postgres via async SQLAlchemy/asyncpg) with per-agent tools, system prompts, max-steps, max-execution-time.
- Invocation engine: on `Invocation` row insert, a SQLAlchemy `after_insert` event listener builds a presigned-URL execution tarball (code + input files in S3/MinIO) and creates a namespaced Kubernetes `batch/v1` Job to run it.
- **Kueue-gated job admission**: Jobs are created `suspend=True` with label `kueue.x-k8s.io/queue-name: <queue>`; Kueue (full CRDs vendored in `charts/manifests.yaml`) decides admission. Free vs. paid queues map to different CPU/memory requests (`500m`/`2Gi` vs `2`/`4Gi`).
- Sandboxed worker: separate `parachutes/squad-worker:latest` image; worker downloads package, runs agent subprocess under an egress HTTP/HTTPS proxy with a strict `NO_PROXY` allowlist (only `*.chutes.ai` + internal API), streams logs back, uploads outputs, marks complete/fail with backoff retries.
- Built-in tool suite calling Chutes: `llm`, `vlm`, `image` (FLUX.1-schnell), `tts` (kokoro), `transcribe`, `apex_search` (Bittensor SN1 web search with `miners` fan-out param), `web` (Playwright), `memory`, `x` (Twitter), `agent_caller` (agent-to-agent), `byok`, `dangerzone`.
- Model discovery against `https://llm.chutes.ai/v1/models` and `https://api.chutes.ai/chutes/...` to resolve agent model + code chute IDs.
- OpenSearch-backed memory/X storage, Redis cache, memcached, BGE reranker (`bge-reranker-large` weights vendored) for result reranking.
- Secrets/BYOK: AES-encrypted secret store with dedicated migrations; JWT (RS256) auth scoped per-invocation with limited scopes.
- Ansible playbooks + Helm chart for cluster join and full deploy (API, OpenSearch, Redis, memcached, X streamer/searcher, execution RBAC).

## Distributed-Computing Notes
- **GPU compute model: consumer, not provider.** There is NO GraVal attestation, NO fiber p2p/gossip, NO Bittensor subtensor/weight-setting, NO TEE/confidential-compute, NO E2EE transport, NO miner/validator logic in this repo. All GPU work is offloaded to remote Chutes endpoints over plain OpenAI-compatible HTTP (`base_url="https://llm.chutes.ai/v1"`).
- **Scheduling/placement** is delegated entirely to Kubernetes + **Kueue** (gang/quota-based job queueing). squad-api only sets the queue label and resource requests; it implements no placement scoring itself. This is the most directly reusable distributed-systems idea: queue-name-routed, suspend-then-admit batch jobs with per-tier resource shapes.
- **Fault tolerance** is per-invocation and shallow: `backoff` retries on log-ship/download/upload, `backoff_limit=0` + `active_deadline_seconds` + `ttl_seconds_after_finished` on the Job, restart_policy Never. No cross-node failover, no consensus, no leader election.
- **"miners" references** (`apex_search.py`) are a client-side fan-out count passed to a remote Bittensor SN1 (Apex) search API — querying N miners for diversity/recall — not local miner orchestration.
- **Security isolation**: egress proxy + NO_PROXY allowlist confines agent network access to Chutes; short-lived scoped JWTs per invocation. A reasonable reference pattern for sandboxing untrusted agent code.

## HelixCluster Gaps Addressed
- **LLMOrchestrator**: end-to-end reference for an agent-invocation lifecycle (define agent → package code+inputs → isolated execution → log streaming → output capture → completion callback) and a clean OpenAI-compatible inference-routing client pattern that Helix could point at its own serving layer.
- **scheduler/Omega (partial)**: the Kueue `suspend=True` + queue-label admission + per-tier (free/paid) resource-request pattern is a concrete, battle-tested model for tiered job admission that Helix's Omega scheduler can borrow conceptually (not code — it's k8s-native).
- **security/sandboxing**: egress-proxy + allowlist + per-job scoped JWT is a useful template for Helix's untrusted-workload isolation.
- Does NOT address: GPU validation/attestation, federation (Phase 6), p2p/gossip, leader/consensus, miner/marketplace (Phase 8/8B) — those live in chutes-miner/gepetto/graval/fiber, not here.

## Dependencies
fastapi, uvicorn, SQLAlchemy 2 + asyncpg (Postgres), redis, aiomcache, opensearch-py, **kubernetes** (client; Job/Batch API), **smolagents[openai]** (CodeAgent), openai, transformers + tiktoken + huggingface-hub (BGE reranker), aioboto3 + google-cloud-storage (S3/GCS blob), playwright + beautifulsoup4 + markdownify (web tool), tweepy (X), cryptography + pyjwt (secrets/auth), Kueue (vendored CRDs in Helm chart), dbmate (SQL migrations), Poetry. Python ^3.12.

## Rationale
REFERENCE, not PORT/WRAP. The repo's genuinely valuable distributed-systems content is two *patterns*: (1) Kueue-gated, queue-routed, suspend-then-admit batch-job execution with tiered resource shapes, and (2) a sandboxed agent-invocation lifecycle with proxy-confined egress and scoped per-job JWTs. Both are architectural lessons Helix (Go) should study and re-implement natively in its scheduler/Omega and LLMOrchestrator, not lift. The bulk of the code is FastAPI CRUD, Chutes-specific tool wrappers, and X/Twitter plumbing with no Helix value. There is no decentralized-compute primitive here to extract — squad-api is a thin client of Chutes' GPU/inference layer.

## Risks
- **Language mismatch**: Python/FastAPI/asyncio vs Helix Go — no code reuse; port = full rewrite.
- **Kubernetes + Kueue coupling**: scheduling is entirely delegated to k8s CRDs; Helix's own scheduler (Omega) is not k8s-native, so only the *pattern* transfers, not the mechanism.
- **Tight Chutes coupling**: every inference/search path hardcodes `*.chutes.ai` endpoints and Chutes auth; not provider-agnostic without rework.
- **Heavy deps** (transformers, playwright, opensearch, smolagents) and a separate worker image — large surface, little of it relevant.
- **Security**: agent runs arbitrary generated Python in a subprocess inside the worker; isolation relies on the worker pod + egress proxy, not a hard sandbox — adopt the pattern only with stronger isolation in Helix.
- Low maturity signals: `version = "0.0.1"`, no README, `origins = ["*"]  # XXX delete this when ready`, commented-out CORS — early-stage/internal code.
