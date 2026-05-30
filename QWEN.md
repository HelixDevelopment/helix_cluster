# Helix Cluster OS — QWEN.md

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## INHERITED FROM HelixConstitution/QWEN.md

All rules in `HelixConstitution/QWEN.md` (and the `HelixConstitution/Constitution.md`, `HelixConstitution/CLAUDE.md`, and `HelixConstitution/AGENTS.md` it references) apply unconditionally. The project-specific rules below extend them.

## Project-Specific Extensions

### Agent Context
- This is the Helix Cluster OS — a distributed cluster operating system.
- Seven-layer stack (L0-L7), 14 control plane microservices, SWIM gossip + Raft consensus.
- Cross-platform parity mandatory: Linux, macOS, Windows/WSL2 per-feature.
- GPU backend coverage mandatory: NVIDIA CUDA, AMD ROCm, Intel oneAPI, Apple MLX on real hardware.

### CodeGraph Integration
- CodeGraph is installed, indexed, and wired via MCP.
- Qwen Code SHOULD use CodeGraph for symbol resolution and cross-reference queries.
- See `docs/CODEGRAPH.md` for setup and usage.

### Critical Base Rules Restated
- Anti-bluff covenant — END-USER QUALITY GUARANTEE (§11.4).
- Mutation-paired gates (§1.1).
- Credentials-handling mandate (§11.4.10) + pre-store leak audit (§11.4.10.A).
- Subagent-driven-by-default (§11.4.20).
- Fetch-before-edit (§11.4.37).
- CodeGraph code-intelligence mandate (§11.4.78) — installed and active.
