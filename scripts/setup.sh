#!/bin/bash
# Helix Cluster OS — Post-Clone Bootstrap Script
# Constitution §11.4.36: Mandatory install_upstreams on clone

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "=== Helix Cluster OS Bootstrap ==="
echo "Project root: ${PROJECT_ROOT}"
echo ""

# 1. Check required tools
echo "[1/8] Checking required tools..."
MISSING=""
for tool in go git docker docker-compose npm; do
    if ! command -v "$tool" &>/dev/null; then
        MISSING="${MISSING} ${tool}"
    fi
done
if [ -n "${MISSING}" ]; then
    echo "ERROR: Missing required tools:${MISSING}"
    exit 1
fi
echo "  All required tools found."

# 2. Initialize submodules
echo "[2/8] Initializing submodules..."
cd "${PROJECT_ROOT}"
git submodule update --init --recursive

# 3. Install upstream remotes
echo "[3/8] Installing upstream remotes..."
if [ -f "${PROJECT_ROOT}/HelixConstitution/install_upstreams.sh" ]; then
    bash "${PROJECT_ROOT}/HelixConstitution/install_upstreams.sh"
fi
for upstream_script in "${PROJECT_ROOT}/upstreams/"*.sh; do
    if [ -f "$upstream_script" ]; then
        bash "$upstream_script"
    fi
done

# 4. Set up Go workspace
echo "[4/8] Setting up Go workspace..."
cd "${PROJECT_ROOT}"

# Check containers submodule
echo "Checking containers module..."
if [ ! -f "containers/go.mod" ]; then
    echo "ERROR: containers submodule not found. Run: git submodule update --init"
    exit 1
fi
cd containers && go build ./... || { echo "ERROR: containers module failed to build"; exit 1; }
cd "${PROJECT_ROOT}"

go work sync || true
for mod_dir in auth cache challenges concurrency config containers database discovery EventBus Filesystem helixqa Herald http3 LLMOrchestrator LLMProvider LLMsVerifier mdns Messaging middleware observability Panoptic ratelimiter recovery security Storage tmux VisionEngine; do
    if [ -d "${PROJECT_ROOT}/${mod_dir}" ] && [ -f "${PROJECT_ROOT}/${mod_dir}/go.mod" ]; then
        (cd "${PROJECT_ROOT}/${mod_dir}" && go mod tidy) || true
    fi
done

# 5. Install CodeGraph
echo "[5/8] Setting up CodeGraph..."
npm install -g @colbymchenry/codegraph 2>/dev/null || echo "  CodeGraph already installed or npm install failed"
if command -v codegraph &>/dev/null; then
    cd "${PROJECT_ROOT}"
    codegraph init 2>/dev/null || true
    codegraph index 2>/dev/null || true
fi

# 6. Build project
echo "[6/8] Building project..."
cd "${PROJECT_ROOT}"
make build || echo "  Build had issues — check logs"

# 7. Run tests
echo "[7/8] Running tests..."
make test-unit || echo "  Tests had issues — check logs"

# 8. Start dev environment
echo "[8/8] Starting development environment..."
echo "  Run 'make dev' to start Docker Compose services."

echo ""
echo "=== Bootstrap complete ==="
echo "Next steps:"
echo "  1. make dev          # Start development environment"
echo "  2. make test         # Run all tests"
echo "  3. make benchmark    # Run benchmarks"
