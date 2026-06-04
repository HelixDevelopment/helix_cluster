#!/usr/bin/env bash
# sonar-scan.sh — Run a SonarQube Community Edition (CE) analysis against the
# locally self-hosted, free SonarQube server (see deploy/compose/security_sonarqube.yml).
#
# IMPORTANT — this is NOT a paid subscription:
#   SonarQube *Community Edition* is open-source (LGPL) and self-hostable for free.
#   The scanner authenticates to the local server with a token, but that token is
#   LOCALLY MINTED on your own server (My Account > Security > Generate Token).
#   It is free auth for your own instance — there is NO SonarCloud account and NO
#   paid tier involved. We therefore gate this scan on SONAR_TOKEN being set,
#   with a clear message explaining how to mint the free token.
#
# Like every other scan in this repo, this is a LOCAL, operator-run step — never
# wired into CI (constitutional Hard Stop). Evidence is captured under
# qa-results/security/sonar-<run-id>/.
#
# Usage:
#   make sonar-up                       # start the local CE server (Podman)
#   # open http://localhost:9000  (admin/admin on first login, then change it)
#   # mint a token: My Account > Security > Generate Tokens
#   export SONAR_TOKEN=<your-local-token>
#   make sonar-scan
#   make sonar-down                     # stop the server (no container left up)
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

SONAR_HOST_URL="${SONAR_HOST_URL:-http://localhost:9000}"
SONAR_PROJECT_KEY="${SONAR_PROJECT_KEY:-helixcluster}"
SONAR_SCANNER_IMAGE="${SONAR_SCANNER_IMAGE:-docker.io/sonarsource/sonar-scanner-cli:latest}"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_DIR="${REPO_ROOT}/qa-results/security/sonar-${RUN_ID}"

if [ -z "${SONAR_TOKEN:-}" ]; then
  cat >&2 <<'EOF'
SKIP: sonar-scan — SONAR_TOKEN is not set.

SonarQube Community Edition is FREE and self-hosted (no paid subscription). The
token is minted on YOUR OWN local server — it is free local auth, not an account:

  1) make sonar-up
  2) open http://localhost:9000  (first login admin/admin, then set a new password)
  3) My Account > Security > Generate Tokens > copy the value
  4) export SONAR_TOKEN=<that-token>
  5) make sonar-scan

This is intentionally gated so we never fake a PASS without a real analysis.
EOF
  exit 3
fi

mkdir -p "${OUT_DIR}"

if ! command -v podman >/dev/null 2>&1; then
  echo "SKIP: sonar-scan — podman not available (host is Podman-only; install/start Podman)." | tee "${OUT_DIR}/sonar.skip" >&2
  exit 3
fi
if ! podman info >/dev/null 2>&1; then
  echo "SKIP: sonar-scan — podman engine not reachable (is 'podman machine' running?)." | tee "${OUT_DIR}/sonar.skip" >&2
  exit 3
fi

# On macOS/podman-machine the server is reached from the scanner container via
# host.containers.internal; on a native Linux engine that name also resolves
# with --add-host below. Allow override via SONAR_HOST_URL.
SCANNER_HOST_URL="${SONAR_HOST_URL}"
case "${SCANNER_HOST_URL}" in
  *localhost*|*127.0.0.1*)
    SCANNER_HOST_URL="$(echo "${SONAR_HOST_URL}" | sed -e 's#localhost#host.containers.internal#' -e 's#127.0.0.1#host.containers.internal#')"
    ;;
esac

echo "==> sonar-scan: project=${SONAR_PROJECT_KEY} host(server)=${SONAR_HOST_URL} host(scanner)=${SCANNER_HOST_URL}"
LOG="${OUT_DIR}/sonar-scanner.log"

podman run --rm \
  --add-host host.containers.internal:host-gateway \
  -e SONAR_HOST_URL="${SCANNER_HOST_URL}" \
  -e SONAR_TOKEN="${SONAR_TOKEN}" \
  -v "${REPO_ROOT}:/usr/src:z" \
  "${SONAR_SCANNER_IMAGE}" \
  -Dsonar.projectKey="${SONAR_PROJECT_KEY}" \
  -Dsonar.sources=. \
  -Dsonar.exclusions='**/vendor/**,**/docs_chain/**,**/bin/**,**/build/**,**/dist/**,**/sbom/**,**/qa-results/**,**/*_test.go' \
  -Dsonar.go.tests.reportPaths= \
  2>&1 | tee "${LOG}"
rc="${PIPESTATUS[0]}"

if [ "${rc}" -ne 0 ]; then
  echo "sonar-scan: scanner exited non-zero (${rc}); see ${LOG}" >&2
  exit "${rc}"
fi
echo "==> sonar-scan complete. Open ${SONAR_HOST_URL}/dashboard?id=${SONAR_PROJECT_KEY} ; log at ${LOG}"
exit 0
