#!/bin/bash
# Updates docs/CONTINUATION.md with current project state
# Reads git log, test status, and active work

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DOCS_DIR="${PROJECT_ROOT}/docs"
CONTINUATION_FILE="${DOCS_DIR}/CONTINUATION.md"
TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "=== Updating CONTINUATION.md ==="

# Gather git state
GIT_BRANCH="$(git -C "${PROJECT_ROOT}" rev-parse --abbrev-ref HEAD 2>/dev/null || echo 'unknown')"
GIT_COMMIT="$(git -C "${PROJECT_ROOT}" rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
GIT_LOG="$(git -C "${PROJECT_ROOT}" log --oneline -5 2>/dev/null || echo 'No recent commits')"

# Gather dirty / untracked files
DIRTY_FILES="$(git -C "${PROJECT_ROOT}" status --short 2>/dev/null || echo '')"

# Gather submodule status
SUBMODULE_STATUS="$(git -C "${PROJECT_ROOT}" submodule status --recursive 2>/dev/null || echo 'No submodules')"

# Test status (best effort)
TEST_STATUS="not run"
if [ -f "${PROJECT_ROOT}/Makefile" ]; then
    if make -C "${PROJECT_ROOT}" test-unit >/dev/null 2>&1; then
        TEST_STATUS="passing"
    else
        TEST_STATUS="failing"
    fi
fi

# Active work — list recently modified files (last 24h)
RECENT_FILES="$(find "${PROJECT_ROOT}" -type f -mtime -1 \
    ! -path '*/.git/*' \
    ! -path '*/node_modules/*' \
    ! -path '*/vendor/*' \
    ! -path '*/bin/*' \
    ! -path '*/build/*' 2>/dev/null | head -20 || true)"

# Build the CONTINUATION.md content
cat > "${CONTINUATION_FILE}" <<EOF
# CONTINUATION

> Auto-generated project state snapshot.
> Last updated: **${TIMESTAMP}**

## Current Branch & Commit

- **Branch:** \`${GIT_BRANCH}\`
- **Commit:** \`${GIT_COMMIT}\`

## Recent Commits

\`\`\`
${GIT_LOG}
\`\`\`

## Working Tree Status

\`\`\`
${DIRTY_FILES:-(clean)}
\`\`\`

## Submodules

\`\`\`
${SUBMODULE_STATUS}
\`\`\`

## Test Status

- **Unit tests:** ${TEST_STATUS}

## Recently Modified Files (last 24h)

EOF

if [ -n "${RECENT_FILES}" ]; then
    while IFS= read -r f; do
        rel="${f#${PROJECT_ROOT}/}"
        echo "- \`${rel}\`" >> "${CONTINUATION_FILE}"
    done <<< "${RECENT_FILES}"
else
    echo "_No files modified in the last 24 hours._" >> "${CONTINUATION_FILE}"
fi

cat >> "${CONTINUATION_FILE}" <<EOF

## Active Work Areas

<!-- Add manual notes about current focus below -->

- 

## Next Steps

<!-- Add planned next steps below -->

- 

---

*This file is maintained by \`scripts/docs/update_continuation.sh\`.*
*Do not edit the auto-generated sections manually — they will be overwritten.*
EOF

echo "[docs] CONTINUATION.md updated at ${CONTINUATION_FILE}"
echo "=== Done ==="
