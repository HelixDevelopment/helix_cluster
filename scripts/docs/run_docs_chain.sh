#!/bin/bash
# -----------------------------------------------------------------------------
# Purpose:       Stable entrypoint for the Docs Chain engine (Constitution
#                §11.4.106). Builds the engine from the pinned `docs_chain`
#                submodule (consumed BY REFERENCE, never copied) and execs it.
# Usage:         scripts/docs/run_docs_chain.sh <doctor|sync|verify|graph> [args...]
#                e.g. scripts/docs/run_docs_chain.sh verify --all
# Inputs:        $@ forwarded verbatim to the docs_chain CLI; contexts read from
#                <repo-root>/.docs_chain/contexts/*.yaml
# Outputs:       Regenerated exports (sync) / drift report (verify); per-run
#                evidence under qa-results/docs_chain/<run-id>/ (gitignored).
# Side-effects:  Builds bin/docs_chain (gitignored) when missing/stale.
# Dependencies:  go (build), pandoc + weasyprint (transforms), the docs_chain
#                submodule at <repo-root>/docs_chain.
# Cross-refs:    docs/scripts/run_docs_chain.md ; Constitution §11.4.106 / §11.4.12.
# Anti-bluff:    A failed engine build or a transform tool absence surfaces a
#                real non-zero exit (never a silent skip) per §11.4.3 / §7.1.
# -----------------------------------------------------------------------------
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENGINE_SRC="${ROOT}/docs_chain"
BIN_DIR="${ROOT}/bin"
BIN="${BIN_DIR}/docs_chain"
STAMP="${BIN_DIR}/.docs_chain.sha"

if [ ! -d "${ENGINE_SRC}/cmd/docs_chain" ]; then
    echo "ERROR: docs_chain submodule missing at ${ENGINE_SRC} (run: git submodule update --init docs_chain)" >&2
    exit 1
fi

HEAD_SHA="$(git -C "${ENGINE_SRC}" rev-parse HEAD 2>/dev/null || echo unknown)"
mkdir -p "${BIN_DIR}"
if [ ! -x "${BIN}" ] || [ "$(cat "${STAMP}" 2>/dev/null || echo none)" != "${HEAD_SHA}" ]; then
    echo "[docs_chain] building engine from submodule @ ${HEAD_SHA}..." >&2
    # GOWORK=off: docs_chain is a decoupled module (digital.vasic.docs_chain),
    # NOT part of the helix_cluster go.work — consumed as a CLI binary by reference.
    ( cd "${ENGINE_SRC}" && GOWORK=off go build -o "${BIN}" ./cmd/docs_chain )
    echo "${HEAD_SHA}" > "${STAMP}"
fi

cd "${ROOT}"
exec "${BIN}" "$@"
