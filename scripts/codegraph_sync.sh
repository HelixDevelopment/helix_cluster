#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: CodeGraph Sync ==="

GRAPH_DIR=".codegraph"
mkdir -p "$GRAPH_DIR"

# Gather Go module info
MODULES=$(go list -m -json all 2>/dev/null | jq -s '.' 2>/dev/null || echo '[]')

# Gather package info
PACKAGES=$(go list -json ./... 2>/dev/null | jq -s '.' 2>/dev/null || echo '[]')

cat > "$GRAPH_DIR/sync.json" <<EOF
{
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "modules": $MODULES,
  "packages": $PACKAGES
}
EOF

echo "CodeGraph sync complete: $GRAPH_DIR/sync.json"
