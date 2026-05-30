#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: Build ==="

# Constitution inheritance verification gate (§7 + §11.4.102)
echo "[pre-build] Running constitution inheritance verification..."
if [ -f tests/constitution/test_inheritance.sh ]; then
    bash tests/constitution/test_inheritance.sh
else
    echo "WARNING: Constitution inheritance gate not found"
fi

echo "Building Go modules..."
go build ./...

if [ -d web ]; then
    echo "Building web UI..."
    cd web
    if [ -f package.json ]; then
        if command -v npm &>/dev/null; then
            npm install
            npm run build
        else
            echo "npm not found, skipping web build"
        fi
    fi
    cd ..
fi

echo "Build complete."
