#!/bin/bash
# Paired Meta-Test Mutation: Constitution Inheritance Gate
# Constitution §1.1 — Every gate MUST have a paired mutation proving it
# catches regressions. This script is the paired mutation for
# tests/constitution/test_inheritance.sh
#
# Operation: Temporarily corrupt constitution/Constitution.md by stripping
# the §11.4 forensic anchor, run the gate, verify it FAILS, then restore.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
GATE="${PROJECT_ROOT}/tests/constitution/test_inheritance.sh"
CI_TARGET="${PROJECT_ROOT}/HelixConstitution/Constitution.md"
CI_ANCHOR='§11.4 End-user quality guarantee — forensic anchor'

MUTATION_TAG="CM-CONSTITUTION-INHERITANCE"
PASS=0
FAIL=0

log_pass() { echo "  ✓ PASS: $1"; ((PASS++)) || true; }
log_fail() { echo "  ✗ FAIL: $1"; ((FAIL++)) || true; }

echo "=== ${MUTATION_TAG} Paired Mutation Test ==="
echo "Gate: ${GATE}"
echo "Target: ${CI_TARGET}"
echo ""

# Verify gate exists and target exists
if [ ! -f "${GATE}" ]; then
    echo "FATAL: Gate script not found: ${GATE}"
    exit 2
fi

if [ ! -f "${CI_TARGET}" ]; then
    echo "FATAL: Target file not found: ${CI_TARGET}"
    exit 2
fi

# Verify anchor exists in target BEFORE mutation
if ! grep -qF "${CI_ANCHOR}" "${CI_TARGET}"; then
    echo "FATAL: Anchor string not found in target — cannot mutate"
    exit 2
fi

# ─── Step 1: Run gate on CLEAN target (should PASS) ───
echo "[1/4] Running gate on CLEAN target (expect PASS)..."
if bash "${GATE}" >/dev/null 2>&1; then
    log_pass "Gate passes on clean target"
else
    log_fail "Gate FAILED on clean target — gate is broken"
    exit 2
fi

# ─── Step 2: Apply mutation (strip anchor) ───
echo "[2/4] Applying mutation: stripping §11.4 anchor..."
cp -- "${CI_TARGET}" "${CI_TARGET}.mut.bak"
sed -i.bak "s|${CI_ANCHOR}|MUTATED_OUT_${MUTATION_TAG}|g" "${CI_TARGET}"

# Verify mutation took effect
if grep -qF "${CI_ANCHOR}" "${CI_TARGET}"; then
    log_fail "Mutation did NOT take effect — sed failed"
    mv -- "${CI_TARGET}.mut.bak" "${CI_TARGET}"
    exit 2
fi
log_pass "Mutation applied successfully"

# ─── Step 3: Run gate on MUTATED target (must FAIL) ───
echo "[3/4] Running gate on MUTATED target (expect FAIL)..."
if bash "${GATE}" >/dev/null 2>&1; then
    log_fail "Gate PASSED on mutated target — gate is a BLUFF (does not catch regression)"
    # Restore before exit
    mv -- "${CI_TARGET}.mut.bak" "${CI_TARGET}"
    exit 1
else
    log_pass "Gate correctly FAILS on mutated target"
fi

# ─── Step 4: Restore target and verify ───
echo "[4/4] Restoring target..."
mv -- "${CI_TARGET}.mut.bak" "${CI_TARGET}"

# Verify restoration
if grep -qF "${CI_ANCHOR}" "${CI_TARGET}"; then
    log_pass "Target restored successfully"
else
    log_fail "Target restoration FAILED — manual intervention required"
    exit 2
fi

# Final clean verification
if bash "${GATE}" >/dev/null 2>&1; then
    log_pass "Gate passes after restoration"
else
    log_fail "Gate fails after restoration — corruption detected"
    exit 2
fi

# ─── Summary ───
echo ""
echo "=== ${MUTATION_TAG} Results ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo ""

if [ "${FAIL}" -gt 0 ]; then
    echo "✗ MUTATION TEST FAILED"
    exit 1
fi

echo "✓ MUTATION TEST PASSED — Gate is NOT a bluff"
echo "  (Gate correctly detects missing §11.4 anchor)"
exit 0
