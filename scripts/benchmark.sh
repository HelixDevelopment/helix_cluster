#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: Benchmark ==="

echo "Running Go benchmarks..."
go test -bench=. -benchmem ./pkg/...

echo "Benchmark complete."
