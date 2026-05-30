#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: Format ==="

echo "Formatting Go code..."
go fmt ./...

if command -v goimports &>/dev/null; then
    echo "Running goimports..."
    goimports -w .
fi

if [ -d web ]; then
    echo "Formatting web UI..."
    cd web
    if [ -f package.json ]; then
        if command -v npm &>/dev/null; then
            npm run format 2>/dev/null || echo "No format script in package.json"
        fi
    fi
    cd ..
fi

echo "Format complete."
