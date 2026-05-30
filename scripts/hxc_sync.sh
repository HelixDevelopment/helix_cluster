#!/bin/bash
# Bidirectional sync between SQLite HXC registry and markdown docs
# Usage: bash scripts/hxc_sync.sh [--to-db|--to-docs|--verify]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCS_DIR="${PROJECT_ROOT}/docs"
DB_PATH="${PROJECT_ROOT}/data/hxc_registry.db"

MODE="${1:---verify}"

if [ ! -f "${DB_PATH}" ]; then
    echo "[hxc_sync] ERROR: Database not found at ${DB_PATH}"
    echo "Run: ./hxc-registry init"
    exit 1
fi

echo "=== HXC Sync ==="
echo "Database: ${DB_PATH}"
echo "Mode: ${MODE}"
echo ""

# Parse issues.md for HXC items (simple grep-based extraction)
parse_docs() {
    local file="$1"
    local location="$2"
    grep -oE 'HXC-[0-9]+' "${file}" 2>/dev/null | sort -u || true
}

ISSUES_IDS=$(parse_docs "${DOCS_DIR}/issues.md" "Issues")
FIXED_IDS=$(parse_docs "${DOCS_DIR}/fixed.md" "Fixed")

# Get IDs from database
DB_IDS=$(sqlite3 "${DB_PATH}" "SELECT hxc_id FROM items ORDER BY hxc_id;" 2>/dev/null || true)

case "${MODE}" in
    --to-db)
        echo "[hxc_sync] Syncing from docs to database..."
        for id in ${ISSUES_IDS}; do
            if ! echo "${DB_IDS}" | grep -qx "${id}"; then
                echo "  [ADD] ${id} -> Issues (from docs)"
                # Extract title from issues.md
                title=$(grep -m1 "${id}" "${DOCS_DIR}/issues.md" | sed 's/.*|//;s/|.*//' | xargs || echo "Unknown")
                sqlite3 "${DB_PATH}" "INSERT INTO items (hxc_id, type, status, priority, phase, title, description, current_location, heading_hash) VALUES ('${id}', 'Task', 'Queued', 'P1', 0, '${title}', '${title}', 'Issues', lower(hex(randomblob(8))));" 2>/dev/null || echo "  [SKIP] ${id} already exists"
            fi
        done
        for id in ${FIXED_IDS}; do
            if ! echo "${DB_IDS}" | grep -qx "${id}"; then
                echo "  [ADD] ${id} -> Fixed (from docs)"
                title=$(grep -m1 "${id}" "${DOCS_DIR}/fixed.md" | sed 's/.*|//;s/|.*//' | xargs || echo "Unknown")
                sqlite3 "${DB_PATH}" "INSERT INTO items (hxc_id, type, status, priority, phase, title, description, current_location, heading_hash) VALUES ('${id}', 'Task', 'Completed', 'P1', 0, '${title}', '${title}', 'Fixed', lower(hex(randomblob(8))));" 2>/dev/null || echo "  [SKIP] ${id} already exists"
            fi
        done
        echo "[hxc_sync] Sync to DB complete"
        ;;

    --to-docs)
        echo "[hxc_sync] Syncing from database to docs..."
        echo "  (Not yet implemented — manual update recommended)"
        ;;

    --verify)
        echo "[hxc_sync] Verification report:"
        echo ""
        echo "Docs Issues:  $(echo "${ISSUES_IDS}" | wc -w | tr -d ' ') items"
        echo "Docs Fixed:   $(echo "${FIXED_IDS}" | wc -w | tr -d ' ') items"
        echo "DB Total:     $(echo "${DB_IDS}" | wc -w | tr -d ' ') items"
        echo ""

        # Check for items in docs but not in DB
        MISSING_IN_DB=0
        for id in ${ISSUES_IDS} ${FIXED_IDS}; do
            if ! echo "${DB_IDS}" | grep -qx "${id}"; then
                echo "  [WARN] ${id} in docs but NOT in database"
                MISSING_IN_DB=$((MISSING_IN_DB + 1))
            fi
        done

        # Check for items in DB but not in docs
        MISSING_IN_DOCS=0
        for id in ${DB_IDS}; do
            if ! echo "${ISSUES_IDS}" | grep -qx "${id}" && ! echo "${FIXED_IDS}" | grep -qx "${id}"; then
                echo "  [WARN] ${id} in database but NOT in docs"
                MISSING_IN_DOCS=$((MISSING_IN_DOCS + 1))
            fi
        done

        if [ ${MISSING_IN_DB} -eq 0 ] && [ ${MISSING_IN_DOCS} -eq 0 ]; then
            echo "  [PASS] All items synchronized"
        else
            echo ""
            echo "  ${MISSING_IN_DB} items missing in DB"
            echo "  ${MISSING_IN_DOCS} items missing in docs"
            echo "  Run: bash scripts/hxc_sync.sh --to-db"
        fi
        ;;

    *)
        echo "Usage: bash scripts/hxc_sync.sh [--to-db|--to-docs|--verify]"
        exit 1
        ;;
esac

echo ""
echo "=== HXC Sync complete ==="
