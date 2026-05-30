#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "=== Helix Cluster OS: CodeGraph Update ==="

GRAPH_DIR=".codegraph"
mkdir -p "$GRAPH_DIR"

# Update index with current module and package list
MODULES=$(go list -m 2>/dev/null | jq -R -s -c 'split("\n") | map(select(length > 0))' || echo '[]')
PACKAGES=$(go list ./... 2>/dev/null | jq -R -s -c 'split("\n") | map(select(length > 0))' || echo '[]')

cat > "$GRAPH_DIR/index.json" <<EOF
{
  "version": "1.0",
  "updated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "modules": $MODULES,
  "packages": $PACKAGES
}
EOF

echo "CodeGraph update complete: $GRAPH_DIR/index.json"
