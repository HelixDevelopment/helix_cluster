# Helix Cluster OS — AGENTS.md

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## INHERITED FROM HelixConstitution/AGENTS.md

All rules in `HelixConstitution/AGENTS.md` (and the `HelixConstitution/Constitution.md` and `HelixConstitution/CLAUDE.md` it references) apply unconditionally. The project-specific rules below extend them.

## Project-Specific Extensions

### Agent Context
- This is a distributed cluster operating system project (Helix Cluster OS).
- Agents working on this project MUST understand the seven-layer architecture (L0-L7).
- Agents MUST respect the cross-platform parity rule (PCS-1): every feature needs Linux, macOS, and Windows/WSL2 implementations.
- Agents MUST respect the GPU backend coverage rule (PCS-2): all 4 vendor backends require real-hardware testing.

### CodeGraph Integration
- CodeGraph (`@colbymchenry/codegraph`) is installed and indexed.
- Agents SHOULD query CodeGraph via MCP for symbol resolution across the project and own-org submodules.
- See `docs/CODEGRAPH.md` for integration details.

### Critical Base Rules Restated
- Anti-bluff covenant binds every test (Constitution §11.4).
- Mutation-paired gates mandatory per §1.1.
- Credentials NEVER tracked; pre-store leak audit per §11.4.10.A.
- Subagent-driven-by-default per §11.4.20.
- Fetch-before-edit per §11.4.37.
