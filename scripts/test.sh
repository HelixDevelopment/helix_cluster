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
        go test -short -race -coverprofile=coverage.out ./...
        ;;
    integration)
        echo "=== Running integration tests ==="
        go test -tags=integration -race ./...
        ;;
    mutation)
        echo "=== Running mutation tests ==="
        # Mutation testing requires dedicated tooling
        echo "Mutation tests: Placeholder — integrate mutation testing framework"
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
        echo "Usage: $0 {unit|integration|mutation|chaos|benchmark|all}"
        exit 1
        ;;
esac

echo ""
echo "=== Test run complete ==="
