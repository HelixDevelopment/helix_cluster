#!/bin/bash
# Helix Constitution Inheritance Verification Gate
# Constitution §7 + §11.4.102 — verifies the constitution submodule is properly
# inherited by the parent project and all nested submodules.
#
# This gate MUST pass before any build, merge, or commit to main.
# Paired mutation test: scripts/testing/meta_test_inheritance.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

PASS=0
FAIL=0

log_pass() { echo "  ✓ PASS: $1"; ((PASS++)) || true; }
log_fail() { echo "  ✗ FAIL: $1"; ((FAIL++)) || true; }

echo "=== Helix Constitution Inheritance Verification ==="
echo "Project root: ${PROJECT_ROOT}"
echo ""

# ─── Invariant 1: constitution/ directory exists ───
echo "[1/7] Checking constitution/ directory..."
if [ -d "${PROJECT_ROOT}/HelixConstitution" ]; then
    log_pass "HelixConstitution/ directory exists"
else
    log_fail "HelixConstitution/ directory NOT FOUND"
fi

# ─── Invariant 2: Constitution.md exists with §11.4 anchor ───
echo "[2/7] Checking Constitution.md §11.4 anchor..."
CONSTITUTION_MD="${PROJECT_ROOT}/HelixConstitution/Constitution.md"
if [ -f "${CONSTITUTION_MD}" ]; then
    if grep -qF '§11.4 End-user quality guarantee — forensic anchor' "${CONSTITUTION_MD}"; then
        log_pass "Constitution.md contains §11.4 forensic anchor"
    else
        log_fail "Constitution.md MISSING §11.4 forensic anchor"
    fi
else
    log_fail "Constitution.md NOT FOUND"
fi

# ─── Invariant 3: CLAUDE.md exists with anti-bluff covenant ───
echo "[3/7] Checking CLAUDE.md anti-bluff covenant..."
CLAUDE_MD="${PROJECT_ROOT}/HelixConstitution/CLAUDE.md"
if [ -f "${CLAUDE_MD}" ]; then
    if grep -qF 'MANDATORY ANTI-BLUFF COVENANT' "${CLAUDE_MD}"; then
        log_pass "CLAUDE.md contains anti-bluff covenant"
    else
        log_fail "CLAUDE.md MISSING anti-bluff covenant"
    fi
else
    log_fail "CLAUDE.md NOT FOUND"
fi

# ─── Invariant 4: AGENTS.md exists with anti-bluff reference ───
echo "[4/7] Checking AGENTS.md anti-bluff reference..."
AGENTS_MD="${PROJECT_ROOT}/HelixConstitution/AGENTS.md"
if [ -f "${AGENTS_MD}" ]; then
    if grep -qFi 'anti-bluff' "${AGENTS_MD}"; then
        log_pass "AGENTS.md contains anti-bluff reference"
    else
        log_fail "AGENTS.md MISSING anti-bluff reference"
    fi
else
    log_fail "AGENTS.md NOT FOUND"
fi

# ─── Invariant 5: Parent project references constitution ───
echo "[5/7] Checking parent project inheritance pointers..."
PARENT_CLAUDE="${PROJECT_ROOT}/CLAUDE.md"
PARENT_AGENTS="${PROJECT_ROOT}/AGENTS.md"

if [ -f "${PARENT_CLAUDE}" ] && grep -qi 'constitution' "${PARENT_CLAUDE}"; then
    log_pass "Parent CLAUDE.md references constitution"
else
    log_fail "Parent CLAUDE.md does NOT reference constitution"
fi

if [ -f "${PARENT_AGENTS}" ] && grep -qi 'constitution' "${PARENT_AGENTS}"; then
    log_pass "Parent AGENTS.md references constitution"
else
    log_fail "Parent AGENTS.md does NOT reference constitution"
fi

# ─── Invariant 6: find_constitution.sh helper exists ───
echo "[6/7] Checking find_constitution.sh helper..."
FIND_SCRIPT="${PROJECT_ROOT}/HelixConstitution/find_constitution.sh"
if [ -f "${FIND_SCRIPT}" ]; then
    log_pass "find_constitution.sh helper exists"
else
    log_fail "find_constitution.sh helper NOT FOUND"
fi

# ─── Invariant 7: install_upstreams.sh exists ───
echo "[7/7] Checking install_upstreams.sh..."
UPSTREAMS_SCRIPT="${PROJECT_ROOT}/HelixConstitution/install_upstreams.sh"
if [ -f "${UPSTREAMS_SCRIPT}" ]; then
    log_pass "install_upstreams.sh exists"
else
    log_fail "install_upstreams.sh NOT FOUND"
fi

# ─── Summary ───
echo ""
echo "=== Results ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo ""

if [ "${FAIL}" -gt 0 ]; then
    echo "✗ VERIFICATION FAILED — ${FAIL} invariant(s) violated"
    echo "  Fix at root cause per Constitution §11.4.4 before proceeding."
    exit 1
fi

echo "✓ ALL INVARIANTS PASS — Constitution inheritance verified"
exit 0
