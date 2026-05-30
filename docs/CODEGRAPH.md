# CodeGraph Integration — Helix Cluster OS

| Field | Value |
|---|---|
| Revision | 1 |
| Created | 2026-05-30 |
| Last modified | 2026-05-30T20:43:00Z |
| Status | active |

## Overview

This project uses [CodeGraph](https://github.com/colbymchenry/codegraph) (`@colbymchenry/codegraph`) as its local semantic code-knowledge graph. CodeGraph is mandated by the Helix Constitution §11.4.78 and §11.4.79 for all AI-agent-driven projects.

## Installation

```bash
npm install -g @colbymchenry/codegraph
```

## Initialization & Indexing

```bash
cd /Users/milosvasic/Projects/HelixCluster
codegraph init
codegraph index
```

## Configuration

`.codegraph/config.json` controls inclusion/exclusion:

- **Included**: all project source files.
- **Excluded**: third-party dependencies, credential/secret paths, build artifacts, cache, temp files.
- **Submodule policy**: own-org submodules (`vasic-digital/*`, `HelixDevelopment/*`) are indexed; third-party submodules are excluded.

## Status

```bash
codegraph status
```

Current index (as of 2026-05-30):
- Files: 348
- Nodes: 5,914
- Edges: 15,756
- Languages: Go (328), YAML (16), Python (3), C (1)

## MCP Wiring

```bash
codegraph serve --mcp
```

For Qwen Code, add to `.qwen/settings.json`:
```json
{
  "mcpServers": {
    "codegraph": {
      "command": "codegraph",
      "args": ["serve", "--mcp", "--scope", "project", "--transport", "stdio"]
    }
  }
}
```

## Regeneration Mechanism

The CodeGraph database is gitignored (`.codegraph/.gitignore`). To regenerate on a fresh clone:

```bash
codegraph init && codegraph index
```

This satisfies Constitution §11.4.77 (regeneration-mechanism-required mandate).

## Anti-Bluff Verification

- `codegraph status` MUST report >0 files, >0 nodes, >0 edges.
- Symbol resolution MUST succeed for at least one symbol from an own-org submodule.
- Paired mutation test: temporarily exclude an own-org submodule path → validate FAILs → restore.

## References

- Constitution §11.4.78 — CodeGraph code-intelligence mandate
- Constitution §11.4.79 — Own-org submodules MUST be included in the index
- Constitution §11.4.80 — Regular update + sync automation mandate
