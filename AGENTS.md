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

### AGENT-1: End-User Usability Guarantee (§7.1 + §11.4.39 Reinforcement)
**Forensic anchor — verbatim user mandate:**
> "We had been in position that all tests do execute with success and all Challenges as well, but in reality the most of the features does not work and can't be used! This MUST NOT be the case and execution of tests and Challenges MUST guarantee the quality, the completition and full usability by end users of the product!"

**Operative rules for coding agents:**
1. **Tests are necessary but NOT sufficient.** Every feature you implement MUST have:
   - Unit tests (with paired mutation per §1.1)
   - Integration tests against REAL services (not mocks)
   - End-to-end tests that exercise the feature as an end user would
   - Challenge validation via HelixQA framework
2. **A test that passes on a non-functional feature is a PASS-bluff** — severity equivalent to §7.1 violation. If you encounter this, you MUST fix the test or the feature.
3. **Every test MUST prove the feature works for end users**, not just that code executes without panic. Verify actual behavior, not just absence of errors.
4. **Challenges (HelixQA) are bound equally** — a Challenge PASS on a broken feature is the same class of defect as a unit test PASS on broken code.
5. **No mock-only validation** for features that claim real-world operation. Mocks are permitted ONLY in unit tests per §11.4.27.
6. **Sink-side evidence required:** Every feature closure MUST include captured evidence (screenshot, log, metrics) proving end-user-visible operation. Do not mark tasks complete without it.
