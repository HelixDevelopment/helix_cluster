# Helix Cluster OS — CLAUDE.md

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## INHERITED FROM HelixConstitution/CLAUDE.md

All rules in `HelixConstitution/CLAUDE.md` (and the `HelixConstitution/Constitution.md` it references) apply unconditionally. The project-specific rules below extend them.

## Project-Specific Extensions

### Technology Stack
- Go 1.24+ for microservices
- Zig 0.14+ for system primitives
- C/C++ for GPU kernels
- PostgreSQL 16+, Redis Cluster 7+, etcd, NATS, Kafka

### Architecture
- Seven-layer stack (L0-L7)
- 14 control plane microservices
- SWIM gossip + Raft consensus
- Omega-model scheduler with optimistic concurrency

### CLAUDE-1: End-User Usability Guarantee (§7.1 + §11.4.39 Reinforcement)
**Forensic anchor — verbatim user mandate:**
> "We had been in position that all tests do execute with success and all Challenges as well, but in reality the most of the features does not work and can't be used! This MUST NOT be the case and execution of tests and Challenges MUST guarantee the quality, the completition and full usability by end users of the product!"

**Operative rules for AI agents:**
1. **Tests are necessary but NOT sufficient.** When implementing or reviewing a feature, you MUST verify:
   - Unit tests exist (with paired mutation per §1.1)
   - Integration tests against REAL services (not mocks)
   - End-to-end tests that exercise the feature as an end user would
   - Challenge validation via HelixQA framework
2. **A test that passes on a non-functional feature is a PASS-bluff** — severity equivalent to §7.1 violation. You MUST flag this during code review.
3. **Every test MUST prove the feature works for end users**, not just that code executes without panic. Verify sink-side behavior, not just function return values.
4. **Challenges (HelixQA) are bound equally** — a Challenge PASS on a broken feature is the same class of defect as a unit test PASS on broken code.
5. **No mock-only validation** for features that claim real-world operation. Mocks are permitted ONLY in unit tests per §11.4.27.
6. **Sink-side evidence required:** Before declaring a feature complete, you MUST require captured evidence (screenshot, log, metrics) proving end-user-visible operation.

### Applied Fixes Table
| Fix | Date | Commit | Release | Description |
|-----|------|--------|---------|-------------|
