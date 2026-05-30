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

### QWEN-1: End-User Usability Guarantee (§7.1 + §11.4.39 Reinforcement)
**Forensic anchor — verbatim user mandate:**
> "We had been in position that all tests do execute with success and all Challenges as well, but in reality the most of the features does not work and can't be used! This MUST NOT be the case and execution of tests and Challenges MUST guarantee the quality, the completition and full usability by end users of the product!"

**Operative rules for QA agents:**
1. **Tests are necessary but NOT sufficient.** Every feature under QA MUST be validated with:
   - Unit tests (with paired mutation per §1.1)
   - Integration tests against REAL services (not mocks)
   - End-to-end tests that exercise the feature as an end user would
   - Challenge validation via HelixQA framework
2. **A test that passes on a non-functional feature is a PASS-bluff** — severity equivalent to §7.1 violation. You MUST file a defect and block release.
3. **Every test MUST prove the feature works for end users**, not just that code executes without panic. Validate actual user journeys and observable outcomes.
4. **Challenges (HelixQA) are bound equally** — a Challenge PASS on a broken feature is the same class of defect as a unit test PASS on broken code. Treat both with equal severity.
5. **No mock-only validation** for features that claim real-world operation. Mocks are permitted ONLY in unit tests per §11.4.27. Reject any feature validated solely with mocks.
6. **Sink-side evidence required:** Every feature closure MUST include captured evidence (screenshot, log, metrics) proving end-user-visible operation. Your sign-off is contingent on this evidence.
