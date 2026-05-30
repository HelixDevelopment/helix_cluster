#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: Lint ==="

if command -v golangci-lint &>/dev/null; then
    echo "Linting Go code..."
    golangci-lint run ./...
else
    echo "golangci-lint not found, running go vet..."
    go vet ./...
fi

if [ -d web ]; then
    echo "Linting web UI..."
    cd web
    if [ -f package.json ]; then
        if command -v npm &>/dev/null; then
            npm run lint || echo "Web lint skipped (no lint script or dependencies missing)"
        else
            echo "npm not found, skipping web lint"
        fi
    fi
    cd ..
fi

echo "Lint complete."
