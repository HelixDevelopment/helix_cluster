#!/bin/bash
# Helix Cluster OS — Test Runner
# Constitution §1: Test coverage is mandatory for every change

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MODE="${1:-all}"
COVERAGE_THRESHOLD="${COVERAGE_THRESHOLD:-80}"

cd "${PROJECT_ROOT}"

case "${MODE}" in
    unit)
        echo "=== Running unit tests ==="
        # Constitution inheritance gate (§7 + §11.4.102)
        if [ -f tests/constitution/test_inheritance.sh ]; then
            echo "[pre-test] Constitution inheritance verification..."
            bash tests/constitution/test_inheritance.sh
        fi
        go test -short -race -coverprofile=coverage.out ./...
        # Orphan-prevention gate suite (HXC-940): the gate packages must actually
        # enforce. Run them against the real tree, feeding the freshly produced
        # coverage profile to covgate's coverage-threshold check.
        echo "[post-test] helix-gate enforcement suite (HXC-940)..."
        HELIX_REPO_ROOT="${PROJECT_ROOT}" COVERAGE_OUT="${PROJECT_ROOT}/coverage.out" \
            go run ./cmd/helix-gate all
        ;;
    gates)
        echo "=== Running helix-gate enforcement suite (HXC-940) ==="
        HELIX_REPO_ROOT="${PROJECT_ROOT}" go run ./cmd/helix-gate all
        ;;
    integration)
        echo "=== Running integration tests ==="
        go test -tags=integration -race ./...
        ;;
    mutation)
        echo "=== Running mutation tests ==="
        # Constitution inheritance paired mutation (§1.1)
        if [ -f scripts/testing/meta_test_inheritance.sh ]; then
            echo "[mutation] Constitution inheritance paired mutation..."
            bash scripts/testing/meta_test_inheritance.sh
        fi
        # §1.1 source-level paired mutation: each case removes an atomic/lock/guard
        # and asserts its paired test flips PASS->FAIL (a survivor is a PASS-bluff).
        echo "[mutation] §1.1 source paired-mutation runner..."
        bash test/mutation/run_mutations.sh
        ;;
    chaos)
        echo "=== Running chaos engineering tests ==="
        go test -tags=chaos ./test/chaos/...
        ;;
    benchmark)
        echo "=== Running benchmarks ==="
        go test -bench=. -benchmem ./...
        ;;
    all)
        echo "=== Running all tests ==="
        make test-unit
        make test-integration
        ;;
    *)
        echo "Usage: $0 {unit|integration|mutation|chaos|benchmark|gates|all}"
        exit 1
        ;;
esac

echo ""
echo "=== Test run complete ==="
