# run_docs_chain.sh

**Revision:** 1
**Last modified:** 2026-06-01T00:00:00Z
**Authority:** Constitution §11.4.106 (Docs Chain) + §11.4.18 (script documentation)

## Purpose

Stable, reproducible entrypoint for the **Docs Chain** engine — the universal
bidirectional document-and-database dependency-propagation engine
(`vasic-digital/docs_chain`) that Constitution §11.4.106 mandates as the
mechanical enforcer of the documentation-sync anchors. It builds the engine
from the pinned `docs_chain` submodule (consumed **by reference**, never copied)
and execs the CLI against this repository's registered contexts.

## Usage

```bash
scripts/docs/run_docs_chain.sh <doctor|sync|verify|graph> [args...]
scripts/docs/run_docs_chain.sh doctor --all     # validate all contexts (no writes)
scripts/docs/run_docs_chain.sh sync   --all     # regenerate every export atomically
scripts/docs/run_docs_chain.sh verify --all     # read-only drift gate (CI / pre-commit)
scripts/docs/run_docs_chain.sh graph  tracked_docs
```

## Inputs

- Subcommand + arguments forwarded verbatim to the `docs_chain` CLI.
- Contexts: `<repo-root>/.docs_chain/contexts/*.yaml` (one chain per file).

## Outputs

- `sync`: regenerated `html` / `pdf` / `docx` exports (and DB↔markdown sync).
- `verify`: drift report; exit 0 in-sync, 2 conflict, 3 transform-fail, 4 config.
- Per-run captured evidence at `qa-results/docs_chain/<run-id>/` (gitignored).

## Side-effects

- Builds `bin/docs_chain` (gitignored) when missing or when the submodule HEAD
  changed (tracked via `bin/.docs_chain.sha`).

## Dependencies

- `go` (build), `pandoc` + `weasyprint` (builtin transforms).
- The `docs_chain` submodule at `<repo-root>/docs_chain`.

## Anti-bluff (§7.1 / §11.4.3)

A failed engine build, a missing transform tool, or a detected drift surfaces a
real non-zero exit — never a silent skip or fake PASS. The pre-commit wrapper
(`scripts/commit_all.sh`) gates on `verify --all`.

## Cross-references

- Constitution §11.4.106 (Docs Chain), §11.4.12 (Issues_Summary always-sync),
  §11.4.44 (revision header), §11.4.65 (universal markdown export).
- `.docs_chain/contexts/tracked_docs.yaml` — the registered context.
- `docs_chain/docs/CONFIG_SCHEMA.md` — the YAML contract.
