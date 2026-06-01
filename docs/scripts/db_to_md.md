# db_to_md.py

**Revision:** 1
**Last modified:** 2026-06-01T00:00:00Z
**Authority:** Constitution §11.4.93 (workable-items DB single source of truth) + §11.4.18

## Purpose

Regenerate the workable-item Markdown views from the canonical SQLite registry
`data/hxc_registry.db`, making the database the single source of truth. Produces
compact per-phase tables so every active item carries a Status while keeping the
docs_chain html/pdf/docx exports a reasonable size.

## Usage

```bash
python3 scripts/docs/db_to_md.py        # regenerate the four views from the DB
```

Run it after any change to the registry (via `cmd/hxc-registry` or bulk ingestion),
then let `scripts/docs/run_docs_chain.sh sync --all` regenerate the exports (the
`commit_all.sh` wrapper does both automatically).

## Inputs / Outputs

- Input: `HXC_DB` env (default `data/hxc_registry.db`).
- Output (overwritten, revision-bumped): `docs/issues.md`, `docs/issues_summary.md`,
  `docs/fixed.md`, `docs/fixed_summary.md`.

## Notes

- The Markdown is a DERIVED VIEW. Do not hand-edit it; edit the DB. Full per-item
  detail (description, closure criteria, required test types, source refs) lives in
  the DB `forensic_anchor` JSON and `docs/research/_ledger/*.json`.
- Future: wired as the docs_chain `db-to-md` exec transform of an `issues` context
  (paired with `md-to-db` via `cmd/hxc-registry`) for full bidirectional sync.

## Cross-references

- Constitution §11.4.93 / §11.4.95 / §11.4.15 / §11.4.16 / §11.4.44.
- `scripts/docs/run_docs_chain.sh`, `.docs_chain/contexts/`.
