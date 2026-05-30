#!/bin/bash
# Documentation Verification Gate
# Constitution §11.4.44 + §12.10 — verifies docs are in sync

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DOCS_DIR="${PROJECT_ROOT}/docs"

ERRORS=0
WARNINGS=0

error() { echo "[VERIFY:FAIL] $1"; ERRORS=$((ERRORS + 1)); }
warn() { echo "[VERIFY:WARN] $1"; WARNINGS=$((WARNINGS + 1)); }
pass() { echo "[VERIFY:PASS] $1"; }

echo "=== Documentation Verification Gate ==="
echo "Project: ${PROJECT_ROOT}"
echo ""

# 1. Required docs exist
echo "[1/5] Checking required docs..."
REQUIRED=(
    "docs/continuation.md"
    "docs/issues.md"
    "docs/issues_summary.md"
    "docs/fixed.md"
    "docs/fixed_summary.md"
    "docs/HXC_REGISTRY.md"
)
for doc in "${REQUIRED[@]}"; do
    if [ -f "${PROJECT_ROOT}/${doc}" ]; then
        pass "Required doc exists: ${doc}"
    else
        error "Missing required doc: ${doc}"
    fi
done

# 2. Revision headers per §11.4.44
echo ""
echo "[2/5] Checking revision headers..."
for doc in "${REQUIRED[@]}"; do
    f="${PROJECT_ROOT}/${doc}"
    if [ ! -f "$f" ]; then continue; fi
    if grep -q '^\*\*Revision:\*\*' "$f" 2>/dev/null && \
       grep -q '^\*\*Last modified:\*\*' "$f" 2>/dev/null; then
        pass "Revision header OK: ${doc}"
    else
        error "Missing revision header: ${doc}"
    fi
done

# 3. Export parity (only for tracked docs in docs/, not submodules or research)
echo ""
echo "[3/5] Checking export parity..."
MISSING_EXPORTS=0
EXPORT_DIR="${DOCS_DIR}/export"

# Only check tracked docs that require exports per §11.4.44
TRACKED_DOCS=(
    "continuation.md"
    "issues.md"
    "issues_summary.md"
    "fixed.md"
    "fixed_summary.md"
    "HXC_REGISTRY.md"
)

for doc in "${TRACKED_DOCS[@]}"; do
    src="${DOCS_DIR}/${doc}"
    [ -f "$src" ] || continue
    basename_f=$(basename "$src")
    html="${EXPORT_DIR}/${doc%.md}.html"
    pdf="${EXPORT_DIR}/${doc%.md}.pdf"
    if [ ! -f "$html" ]; then
        MISSING_EXPORTS=$((MISSING_EXPORTS + 1))
        warn "Missing HTML export for ${basename_f}"
    fi
    if [ ! -f "$pdf" ]; then
        MISSING_EXPORTS=$((MISSING_EXPORTS + 1))
        warn "Missing PDF export for ${basename_f}"
    fi
done

if [ ${MISSING_EXPORTS} -eq 0 ]; then
    pass "All tracked docs have HTML and PDF exports"
else
    error "${MISSING_EXPORTS} missing exports (run: bash scripts/docs/generate.sh)"
fi

# 4. CONTINUATION.md freshness
echo ""
echo "[4/5] Checking CONTINUATION.md freshness..."
CONT="${DOCS_DIR}/continuation.md"
if [ -f "$CONT" ]; then
    if [ "$(uname)" = "Darwin" ]; then
        MTIME=$(stat -f %m "$CONT")
    else
        MTIME=$(stat -c %Y "$CONT")
    fi
    NOW=$(date +%s)
    AGE_HOURS=$(((NOW - MTIME) / 3600))
    if [ ${AGE_HOURS} -lt 24 ]; then
        pass "CONTINUATION.md is fresh (${AGE_HOURS}h old)"
    else
        error "CONTINUATION.md is stale (${AGE_HOURS}h old)"
    fi
else
    error "CONTINUATION.md not found"
fi

# 5. Issues.md / Fixed.md consistency
echo ""
echo "[5/5] Checking Issues/Fixed consistency..."
ISSUES="${DOCS_DIR}/issues.md"
FIXED="${DOCS_DIR}/fixed.md"
if [ -f "$ISSUES" ] && [ -f "$FIXED" ]; then
    # Check for HXC IDs in Fixed that are still in Issues
    FIXED_IDS=$(grep -oE 'HXC-[0-9]+' "$FIXED" 2>/dev/null | sort -u || true)
    ISSUE_IDS=$(grep -oE 'HXC-[0-9]+' "$ISSUES" 2>/dev/null | sort -u || true)
    DUPES=0
    for fid in ${FIXED_IDS}; do
        if echo "${ISSUE_IDS}" | grep -qx "${fid}"; then
            DUPES=$((DUPES + 1))
            warn "HXC ID ${fid} appears in both Issues.md and Fixed.md"
        fi
    done
    if [ ${DUPES} -eq 0 ]; then
        pass "Issues.md and Fixed.md are consistent (no duplicate HXC IDs)"
    fi
else
    warn "Issues.md or Fixed.md not found"
fi

# Summary
echo ""
echo "=== Verification Summary ==="
if [ ${ERRORS} -eq 0 ]; then
    echo "✓ ALL CHECKS PASSED (${WARNINGS} warnings)"
    exit 0
else
    echo "✗ VERIFICATION FAILED — ${ERRORS} error(s), ${WARNINGS} warning(s)"
    exit 1
fi
