# Helix Cluster OS — Development Guide

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## Prerequisites

- Go 1.24+
- Zig 0.14+
- C/C++ compiler (GCC/Clang/MSVC)
- Docker / Podman (for containerized services)
- PostgreSQL 16+, Redis Cluster 7+, etcd, NATS, Kafka

## Build

```bash
# Full build
make all

# Per-layer builds
make l0   # Hardware abstraction
make l1   # Kernel extensions
make l2   # Container runtime
make l3   # Resource scheduler
make l4   # Service mesh
make l5   # Distributed storage
make l6   # Application platform
make l7   # User interface
```

## Test

```bash
# Run all tests
make test

# Run with mutation tests (anti-bluff)
make test-mutation

# Per-layer tests
make test-l3

# GPU backend tests (requires real hardware)
make test-gpu

# Cross-platform parity tests
make test-cross-platform
```

## Run

```bash
# Local development cluster
make dev-cluster-up

# Single-node mode
make run-single

# With GPU passthrough
make run-gpu
```

## CodeGraph

Ensure CodeGraph is indexed before starting development:

```bash
codegraph init && codegraph index
codegraph status
```

## Commit

Use the project's commit wrapper (do not `git commit` directly):

```bash
scripts/commit_all.sh
```

## Documentation

- `Constitution.md` — Project constitution (inherits HelixConstitution)
- `CLAUDE.md` — Claude Code agent rules
- `AGENTS.md` — Generic AI agent rules
- `QWEN.md` — Qwen Code agent rules
- `CODING_STANDARDS_GO.md` — Go style guide
- `CODING_STANDARDS_ZIG.md` — Zig style guide
- `CODING_STANDARDS_C.md` — C/C++ style guide
