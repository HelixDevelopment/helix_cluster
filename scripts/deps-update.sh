#!/usr/bin/env bash
# Local dependency-update maintenance (HXC-1636 / PRR item 80).
#
# The project forbids CI/CD pipelines (constitutional Hard Stop), so there is no
# dependabot/renovate bot. This is the LOCAL, operator-run equivalent: bump module
# deps, sync the workspace, re-run the vulnerability scan, and refresh the SBOMs.
# Run it manually or on a periodic reminder. It is intentionally not wired into any
# automated gate.
#
# Usage: make deps-update   (or: bash scripts/deps-update.sh)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export PATH="$(go env GOPATH)/bin:$PATH"

MODULES=". api/v1 security"

echo "==> bumping module dependencies (go get -u)"
for m in $MODULES; do
	echo "    - $m"
	( cd "$m" && go get -u ./... )
done

echo "==> syncing workspace (go work sync)"
go work sync

echo "==> vulnerability scan (govulncheck) — non-zero exit on reachable advisories"
rc=0
if command -v govulncheck >/dev/null 2>&1; then
	for m in $MODULES; do
		echo "    - govulncheck $m"
		( cd "$m" && govulncheck ./... ) || rc=$?
	done
else
	echo "    govulncheck not on PATH; install: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
	rc=1
fi

echo "==> refreshing SBOMs (make sbom)"
bash "$ROOT/scripts/gen-sbom.sh" || true

if [ "$rc" -ne 0 ]; then
	echo "deps-update: govulncheck reported reachable advisories (or was missing) — review above." >&2
fi
exit "$rc"
