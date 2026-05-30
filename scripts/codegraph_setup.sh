#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: CodeGraph Setup ==="

GRAPH_DIR=".codegraph"
mkdir -p "$GRAPH_DIR"

# Initialize CodeGraph index if not present
if [ ! -f "$GRAPH_DIR/index.json" ]; then
    echo '{"version":"1.0","modules":[],"dependencies":[]}' > "$GRAPH_DIR/index.json"
    echo "Initialized CodeGraph index."
else
    echo "CodeGraph index already exists."
fi

echo "CodeGraph setup complete."
