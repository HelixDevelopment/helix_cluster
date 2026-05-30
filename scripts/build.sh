#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: Build ==="

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
