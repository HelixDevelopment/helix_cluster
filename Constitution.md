# Helix Cluster OS — Project Constitution

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## INHERITED FROM HelixConstitution/Constitution.md

All rules in `HelixConstitution/Constitution.md` apply unconditionally.
This project Constitution extends them with Helix Cluster OS-specific rules.

## Project-Specific Rules

### PCS-1: Cross-Platform Parity
Every feature MUST have per-OS implementations for Linux, macOS, and Windows/WSL2.

### PCS-2: GPU Backend Coverage
All 4 GPU vendor backends (NVIDIA CUDA, AMD ROCm, Intel oneAPI, Apple MLX) MUST be tested on real hardware.

### PCS-3: Session Migration Validation
CRIU/DMTCP migration MUST be validated with real process state capture and restore.

### PCS-4: Build Distribution Verification
AOSP builds MUST be verified to distribute across real cluster nodes.

### PCS-5: Anti-Bluff Enforcement
Every test gate MUST have a paired mutation test per Constitution §1.1.

### PCS-6: End-User Usability Guarantee (§7.1 + §11.4.39 Reinforcement)
**Forensic anchor — verbatim user mandate:**
> "We had been in position that all tests do execute with success and all Challenges as well, but in reality the most of the features does not work and can't be used! This MUST NOT be the case and execution of tests and Challenges MUST guarantee the quality, the completition and full usability by end users of the product!"

**Operative rules:**
1. **Tests are necessary but NOT sufficient.** Every feature MUST have:
   - Unit tests (with paired mutation per §1.1)
   - Integration tests against REAL services (not mocks)
   - End-to-end tests that exercise the feature as an end user would
   - Challenge validation via HelixQA framework
2. **A test that passes on a non-functional feature is a PASS-bluff** — severity equivalent to §7.1 violation.
3. **Every test MUST prove the feature works for end users**, not just that code executes without panic.
4. **Challenges (HelixQA) are bound equally** — a Challenge PASS on a broken feature is the same class of defect as a unit test PASS on broken code.
5. **No mock-only validation** for features that claim real-world operation. Mocks are permitted ONLY in unit tests per §11.4.27.
6. **Sink-side evidence required:** Every feature closure MUST include captured evidence (screenshot, log, metrics) proving end-user-visible operation.
