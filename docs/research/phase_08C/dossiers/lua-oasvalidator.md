# lua-oasvalidator
- **Repo:** https://github.com/chutesai/lua-oasvalidator
- **Language:** Lua (binding over C++; the validation engine itself is C++11)
- **License:** MIT
- **Maturity:** stale (fork-tracking; identical to upstream, no Chutes commits)
- **Distributed-Computing Relevance:** none
- **Portability Verdict:** SKIP
- **Target Helix Module:** N/A (closest conceptual neighbor would be an API-gateway/admission layer, not a current Helix module)
- **Effort:** S

## Purpose
A thin Lua C-API binding (`luaopen_oasvalidator`) over the C++11 `cpp-oasvalidator` library (vendored as a git submodule) that validates inbound HTTP requests against an OpenAPI 3.x specification. It is intended to be embedded inside Lua-scriptable API gateways such as Kong or OpenResty/NGINX to reject non-compliant requests before they reach backend services.

## Capabilities
- Loads an OpenAPI 3.x JSON spec and returns a validator object (`oasvalidator.GetValidators(spec_path, method_mappings)`).
- Sequential request validation with these methods: `ValidateRoute`, `ValidateBody`, `ValidatePathParam`, `ValidateQueryParam`, `ValidateHeaders`, `ValidateRequest`.
- Deserializes OpenAPI parameter styles (simple/label/matrix for path; form/spaceDelimited/pipeDelimited/deepObject for query; simple for headers) across primitives, strings, arrays, and objects.
- Returns a numeric error code (e.g. `INVALID_METHOD=-1`, `INVALID_BODY=-6`) plus a structured JSON error message with a JSON-pointer `specRef`/`instance`/`schema`.
- Lazy deserialization: only parses body/params once preceding cheaper checks (method, route) pass.
- Optional HTTP method aliasing (e.g. treat `HEAD` as `GET`).

## Distributed-Computing Notes
None. This is an OpenAPI schema-conformance validator for single HTTP requests. There is no GPU code, no attestation/GraVal, no scheduling or placement logic, no p2p/gossip (fiber), no Bittensor subnet/consensus/weights, no TEE/sek8s, no E2EE transport, no inference routing/serving, and no fault-tolerance/replication machinery. Its only relationship to a compute network is the generic one that any request-admission filter could sit in front of an inference HTTP endpoint.

## HelixCluster Gaps Addressed
None of the tracked Helix areas (scheduler/Omega, resources, planned GPU, Phase-6 federation, security/E2EE, LLMOrchestrator, Messaging/EventBus, discovery, leader/consensus, Phase-8/8B miner/marketplace) are addressed. The only adjacent — and currently non-existent in Helix — concern is request-level OpenAPI admission validation at an API-gateway edge, which Helix does not have as a module and which Go already covers natively via libraries like `kin-openapi`/`libopenapi-validator`.

## Dependencies
- `lua >= 5.1` (runtime; supports 5.1 through 5.2+ via `luaL_setfuncs`/`luaL_register` shims).
- `cpp-oasvalidator` (git submodule, `nawaz1991/cpp-oasvalidator`, MIT) — the actual validation engine, built on RapidJSON.
- Build toolchain: CMake >= 3.1, a C++11 compiler, LuaRocks (`oasvalidator-1.1.0-1.rockspec`, build type `cmake`).

## Rationale
SKIP. (1) Zero distributed-computing relevance — it is an HTTP/OpenAPI request validator. (2) Language mismatch: Helix is Go; this is Lua-over-C++ requiring a Lua VM and a CMake/C++ build, none of which exist in Helix's stack. (3) The Chutes fork is byte-for-byte identical to upstream `nawaz1991/lua-oasvalidator` — `HEAD == upstream/main`, no divergent commits, no diff. Chutes added nothing; it is a passive mirror, likely pulled in transitively because some Chutes Lua/Kong gateway config referenced it. (4) Any equivalent capability Helix might want (OpenAPI admission at an edge) is trivially and more idiomatically obtained from a native Go OpenAPI validator. There is nothing here to port, wrap, or even use as a meaningful reference for distributed-systems design.

## Risks
- **Language/runtime mismatch:** porting would mean introducing a Lua VM and C++/CMake build into a pure-Go cluster OS — unjustifiable for a feature Go libraries already provide.
- **Fork drift:** the fork carries no Chutes changes, so any future relevance depends entirely on upstream, which is itself effectively dormant (last commit 2024-07-08).
- **Maintenance/abandonment:** upstream is a single-author hobby project; no security review pipeline.
- **License:** MIT — permissive and not a blocker; this is the only low-risk dimension, but it is moot given the SKIP verdict.
