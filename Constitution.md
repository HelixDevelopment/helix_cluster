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
