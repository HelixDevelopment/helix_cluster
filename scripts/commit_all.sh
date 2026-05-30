#!/bin/bash
# Helix Cluster OS — Official Commit Wrapper
# Constitution §2: Single entrypoint, locked, multi-upstream push

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
LOCKFILE="/tmp/helix_cluster_commit.lock"

# Advisory lock — macOS-compatible (no flock)
if [ -e "${LOCKFILE}" ]; then
    PID=$(cat "${LOCKFILE}" 2>/dev/null || echo "")
    if [ -n "${PID}" ] && kill -0 "${PID}" 2>/dev/null; then
        echo "ERROR: Another commit/push wrapper is already running (PID ${PID}). Wait for it to complete."
        echo "If the owning process has crashed, run: rm -f ${LOCKFILE}"
        exit 1
    fi
fi
echo $$ > "${LOCKFILE}"
trap 'rm -f "${LOCKFILE}"' EXIT

cd "${PROJECT_ROOT}"

echo "=== Helix Cluster OS Commit Wrapper ==="
echo "Project root: ${PROJECT_ROOT}"
echo ""

# 1. Pre-commit validation (skip if tools not available)
echo "[1/6] Running pre-commit validations..."
if command -v golangci-lint &>/dev/null; then
    make lint || { echo "Lint failed. Fix before committing."; exit 1; }
else
    echo "  golangci-lint not available — skipping lint"
fi

# 2. Stage all changes
echo "[2/6] Staging changes..."
git add -A

# 3. Check for submodule changes and commit them first (Constitution §3)
echo "[3/6] Checking submodule changes..."
git submodule foreach '
    if [ -n "$(git status --porcelain)" ]; then
        echo "Submodule $name has changes — committing inside submodule first..."
        git add -A
        git commit -m "[$name] Auto-commit submodule changes" || true
        for remote in $(git remote); do
            git push "${remote}" $(git rev-parse --abbrev-ref HEAD) || true
        done
    fi
'

# 4. Commit main project
echo "[4/6] Committing main project..."
if [ -z "$(git status --porcelain)" ]; then
    echo "Nothing to commit."
    exit 0
fi

COMMIT_MSG="${1:-"Auto-commit: $(date -u +%Y-%m-%dT%H:%M:%SZ)"}"
git commit -m "${COMMIT_MSG}"

# 5. Push to all upstreams (Constitution §2.1)
echo "[5/6] Pushing to all upstreams..."
for remote in $(git remote); do
    echo "  Pushing to ${remote}..."
    git push "${remote}" "$(git rev-parse --abbrev-ref HEAD)" || echo "  WARNING: Push to ${remote} failed"
done

# 6. Mirror tags on submodules (Constitution §4)
echo "[6/6] Checking for tag mirroring..."
CURRENT_TAG=$(git describe --tags --exact-match 2>/dev/null || true)
if [ -n "${CURRENT_TAG}" ]; then
    echo "Tag ${CURRENT_TAG} detected — mirroring on owned submodules..."
    git submodule foreach '
        if git remote get-url origin 2>/dev/null | grep -qE "(vasic-digital|HelixDevelopment)"; then
            git tag -a "'"${CURRENT_TAG}"'" -m "Mirror tag '"${CURRENT_TAG}"' from parent" 2>/dev/null || true
            for remote in $(git remote); do
                git push "${remote}" --tags 2>/dev/null || true
            done
        fi
    '
fi

echo ""
echo "=== Commit complete ==="
