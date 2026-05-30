# ADR 0001 — Use Monorepo for Helix Cluster OS

| Field | Value |
|---|---|
| ADR Number | 0001 |
| Title | Use Monorepo |
| Status | accepted |
| Date | 2026-05-30 |
| Deciders | Helix Cluster OS core team |

## Context

Helix Cluster OS spans seven layers (L0-L7), 14 control plane microservices, GPU kernel code, and multiple language runtimes. Splitting into many repositories would fragment the dependency graph, complicate atomic changes, and make cross-layer refactoring prohibitively expensive.

## Decision

Adopt a monorepo structure at `HelixDevelopment/HelixCluster` with:
- Submodules for own-org reusable libraries (`HelixConstitution`, `containers`, etc.).
- In-repo source for layer-specific code (`cmd/`, `pkg/`, `src/`).
- Unified build orchestration via `Makefile` at repo root.
- CodeGraph indexed across the entire tree including own-org submodules.

## Consequences

- **Positive**: Atomic commits across layers; single CodeGraph index; unified CI/CD.
- **Positive**: Constitution inheritance works naturally via `HelixConstitution` submodule.
- **Negative**: Repo size grows with history; requires `git lfs` for large assets.
- **Negative**: Clone time increases; mitigated by shallow clones and sparse checkouts.

## Compliance

- Cross-platform parity checked: build system dispatches per-OS (PCS-1).
- GPU backend coverage: kernel code lives in-repo, tested on real hardware (PCS-2).
- Mutation test: build-system gate has paired mutation per Constitution §1.1.
