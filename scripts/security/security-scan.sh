#!/usr/bin/env bash
# security-scan.sh — Token-free, 100% FOSS security scan for HelixCluster.
#
# The project forbids CI/CD pipelines (constitutional Hard Stop), so — exactly
# like scripts/deps-update.sh and scripts/gen-sbom.sh — this is the LOCAL,
# operator-run security gate. It is intentionally NOT wired into any automated
# CI job; run it manually or on a periodic reminder.
#
# Cost model: every scanner here is open-source AND token-free. NO paid Snyk /
# SonarCloud subscription and NO account token is required:
#   - govulncheck (golang.org/x/vuln) — Go vuln/SCA, reachability-aware
#   - gosec       (securego/gosec)     — Go SAST
#   - trivy       (aquasec/trivy)      — filesystem dependency + config CVE scan
#                                        (run via Podman; the host is Podman-only)
# Snyk is deliberately excluded: it needs a Snyk ACCOUNT token even on the free
# tier, so it is not token-free. The self-hosted SonarQube CE option lives in a
# separate target (make sonar-scan + deploy/compose/security_sonarqube.yml).
#
# Evidence (sink-side proof per CLAUDE-1) is captured under:
#   qa-results/security/<run-id>/
# Each sub-scanner that cannot run emits an HONEST SKIP-with-reason (a .skip
# file + log line) — never a fake PASS.
#
# Exit status: non-zero if any scanner that COULD run reported findings, so the
# operator notices. Honest SKIPs do not fail the run on their own; a summary
# line records them.
#
# Usage: make security-scan   (or: bash scripts/security/security-scan.sh)
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

# Ensure go-installed tools (~/go/bin) are reachable.
export PATH="$(go env GOPATH 2>/dev/null)/bin:$PATH"

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_DIR="${REPO_ROOT}/qa-results/security/${RUN_ID}"
mkdir -p "${OUT_DIR}"

# go.work modules to scan with the Go-native tools.
MODULES=". api/v1 security"

# Directories excluded from gosec (vendored / submodule / generated trees).
GOSEC_EXCLUDE_DIRS="vendor,docs_chain,.git,bin,build,dist,sbom,qa-results,node_modules"

# Overall status accumulator.
FINDINGS=0   # number of scanners that reported findings
SKIPS=0      # number of scanners honestly skipped
RAN=0        # number of scanners that actually executed

log() { echo "$@"; }
section() { echo; echo "==> $*"; }

# record_skip <scanner> <reason>
record_skip() {
  local scanner="$1"; shift
  local reason="$*"
  printf 'SKIP: %s — %s\n' "$scanner" "$reason" | tee "${OUT_DIR}/${scanner}.skip"
  SKIPS=$((SKIPS + 1))
}

############################################
# 1) govulncheck — Go vuln / SCA per module
############################################
scan_govulncheck() {
  section "govulncheck (Go vuln/SCA) — per go.work module"
  if ! command -v govulncheck >/dev/null 2>&1; then
    log "    govulncheck not on PATH; attempting: go install golang.org/x/vuln/cmd/govulncheck@latest"
    if ! go install golang.org/x/vuln/cmd/govulncheck@latest 2>>"${OUT_DIR}/govulncheck-install.log"; then
      record_skip "govulncheck" "not installed and 'go install' failed (see govulncheck-install.log)"
      return
    fi
  fi
  RAN=$((RAN + 1))
  local rc=0
  for m in $MODULES; do
    local safe; safe="$(echo "$m" | tr '/. ' '___')"
    local out="${OUT_DIR}/govulncheck-${safe}.txt"
    local jout="${OUT_DIR}/govulncheck-${safe}.json"
    log "    - govulncheck ${m}"
    ( cd "$m" && govulncheck ./... ) >"$out" 2>&1 || rc=$?
    # JSON variant for machine-readable evidence (best-effort).
    ( cd "$m" && govulncheck -json ./... ) >"$jout" 2>/dev/null || true
  done
  if [ "$rc" -ne 0 ]; then
    log "    govulncheck: reachable advisories reported (see govulncheck-*.txt)"
    FINDINGS=$((FINDINGS + 1))
  else
    log "    govulncheck: no reachable advisories."
  fi
}

############################################
# 2) gosec — Go SAST across the workspace
############################################
scan_gosec() {
  section "gosec (Go SAST) — workspace"
  if ! command -v gosec >/dev/null 2>&1; then
    log "    gosec not on PATH; attempting: go install github.com/securego/gosec/v2/cmd/gosec@latest"
    if ! go install github.com/securego/gosec/v2/cmd/gosec@latest 2>>"${OUT_DIR}/gosec-install.log"; then
      record_skip "gosec" "not installed and 'go install' failed (see gosec-install.log)"
      return
    fi
  fi
  RAN=$((RAN + 1))
  local json_out="${OUT_DIR}/gosec.json"
  local txt_out="${OUT_DIR}/gosec.txt"
  local rc=0
  # gosec walks the tree itself; exclude vendored/submodule/generated dirs.
  # -no-fail keeps gosec's own exit code at 0 for the JSON pass so we can parse
  # the report regardless; we derive findings from the report content below.
  gosec -quiet -fmt=json -out="${json_out}" -exclude-dir="${GOSEC_EXCLUDE_DIRS}" -no-fail ./... 2>"${OUT_DIR}/gosec-stderr.log" || true
  gosec -quiet -fmt=text -out="${txt_out}" -exclude-dir="${GOSEC_EXCLUDE_DIRS}" -no-fail ./... 2>>"${OUT_DIR}/gosec-stderr.log" || true
  if [ -f "${json_out}" ] && command -v jq >/dev/null 2>&1; then
    local nissues
    nissues="$(jq '.Issues | length' "${json_out}" 2>/dev/null || echo 0)"
    if [ "${nissues:-0}" -gt 0 ]; then
      log "    gosec: ${nissues} issue(s) found (see gosec.json / gosec.txt)"
      FINDINGS=$((FINDINGS + 1))
      rc=1
    else
      log "    gosec: no issues found."
    fi
  else
    log "    gosec: report produced (jq unavailable for count; see gosec.txt)"
  fi
  return $rc
}

############################################
# 3) trivy fs — dependency + config CVE scan (via Podman)
############################################
scan_trivy() {
  section "trivy fs (dependency + config CVE) — via Podman"
  local trivy_mode=""
  if command -v trivy >/dev/null 2>&1; then
    trivy_mode="native"
  elif command -v podman >/dev/null 2>&1; then
    # Confirm the podman machine / engine is actually reachable.
    if podman info >/dev/null 2>&1; then
      trivy_mode="podman"
    else
      record_skip "trivy" "podman present but engine not reachable (is 'podman machine' running?)"
      return
    fi
  else
    record_skip "trivy" "neither native trivy nor podman available (install trivy or start Podman)"
    return
  fi

  RAN=$((RAN + 1))
  local txt_out="${OUT_DIR}/trivy-fs.txt"
  local json_out="${OUT_DIR}/trivy-fs.json"
  # Skip scanning our own evidence + build derivatives to keep the report focused.
  local skipdirs="qa-results,sbom,bin,build,dist,docs_chain"
  local rc=0

  # Cache the trivy DB INSIDE the repo (on the path Podman already mounts) so the
  # DB is reused across runs. ~/.cache is not always shared into the podman
  # machine VM (statfs failures), whereas the repo root is. This dir lives under
  # the gitignored qa-results/ tree.
  local cache="${REPO_ROOT}/qa-results/.trivy-cache"
  mkdir -p "${cache}"

  # Apply the repo's NARROW, fully-commented suppressions (.trivyignore.yaml):
  # confirmed anti-leak QA sentinel "secrets" + submodule-owned / intentional
  # host-agent misconfigs (see that file for the per-path justifications).
  # trivy's working dir differs (native: repo root; podman: '/'), so we pass
  # --ignorefile explicitly instead of relying on auto-discovery, which would
  # silently NOT load the file under Podman. Honest SKIP-with-reason, not a
  # blanket severity downgrade.
  local ignorefile_native="${REPO_ROOT}/.trivyignore.yaml"
  local ignorefile_podman="/src/.trivyignore.yaml"

  if [ "$trivy_mode" = "native" ]; then
    log "    using native trivy: $(command -v trivy)"
    trivy fs --cache-dir "${cache}" --ignorefile "${ignorefile_native}" \
      --scanners vuln,misconfig,secret --skip-dirs "${skipdirs}" \
      --format table --output "${txt_out}" . 2>"${OUT_DIR}/trivy-stderr.log" || rc=$?
    trivy fs --cache-dir "${cache}" --ignorefile "${ignorefile_native}" \
      --scanners vuln,misconfig,secret --skip-dirs "${skipdirs}" \
      --format json --output "${json_out}" . 2>>"${OUT_DIR}/trivy-stderr.log" || true
  else
    log "    using Podman: docker.io/aquasec/trivy:latest"
    # ':z' relabels the bind mount for SELinux hosts; harmless on macOS. The DB
    # cache is mounted from inside the repo (a path the VM already shares).
    podman run --rm \
      -v "${REPO_ROOT}:/src:z" \
      docker.io/aquasec/trivy:latest fs \
      --cache-dir /src/qa-results/.trivy-cache \
      --ignorefile "${ignorefile_podman}" \
      --scanners vuln,misconfig,secret --skip-dirs "${skipdirs}" \
      --format table /src >"${txt_out}" 2>"${OUT_DIR}/trivy-stderr.log" || rc=$?
    podman run --rm \
      -v "${REPO_ROOT}:/src:z" \
      docker.io/aquasec/trivy:latest fs \
      --cache-dir /src/qa-results/.trivy-cache \
      --ignorefile "${ignorefile_podman}" \
      --scanners vuln,misconfig,secret --skip-dirs "${skipdirs}" \
      --format json /src >"${json_out}" 2>>"${OUT_DIR}/trivy-stderr.log" || true
  fi

  # Honest result interpretation. A valid trivy JSON report always has a
  # top-level .Results array (even when empty). If the JSON is missing, empty,
  # or unparseable, the scan did NOT actually complete — report that as a
  # failure/SKIP, NEVER as "no findings" (that would be a CLAUDE-1 PASS-bluff).
  if command -v jq >/dev/null 2>&1 \
     && [ -s "${json_out}" ] \
     && jq -e 'has("Results")' "${json_out}" >/dev/null 2>&1; then
    local nfind
    nfind="$(jq '[.Results[]? | ((.Vulnerabilities // []) + (.Misconfigurations // []) + (.Secrets // [])) | length] | add // 0' "${json_out}" 2>/dev/null || echo 0)"
    if [ "${nfind:-0}" -gt 0 ]; then
      log "    trivy: ${nfind} finding(s) (vuln/misconfig/secret) — see trivy-fs.txt / trivy-fs.json"
      FINDINGS=$((FINDINGS + 1))
    else
      log "    trivy: no findings."
    fi
  else
    # No usable report -> the scanner failed to run; do not claim success.
    RAN=$((RAN - 1))
    record_skip "trivy" "scan did not produce a valid report (rc=${rc}); see trivy-stderr.log"
  fi
}

#############################
# Run all scanners + summary
#############################
scan_govulncheck
scan_gosec
scan_trivy

SUMMARY="${OUT_DIR}/SUMMARY.txt"
{
  echo "HelixCluster token-free FOSS security scan"
  echo "run-id:        ${RUN_ID}"
  echo "evidence-dir:  qa-results/security/${RUN_ID}/"
  echo "scanners-ran:  ${RAN}"
  echo "with-findings: ${FINDINGS}"
  echo "skipped:       ${SKIPS}"
  echo
  echo "Artifacts:"
  ls -1 "${OUT_DIR}" | sed 's/^/  - /'
} | tee "${SUMMARY}"

section "security-scan complete — evidence in qa-results/security/${RUN_ID}/"
if [ "${FINDINGS}" -gt 0 ]; then
  echo "RESULT: ${FINDINGS} scanner(s) reported findings; ${SKIPS} skipped honestly. Review the evidence." >&2
  exit 1
fi
if [ "${RAN}" -eq 0 ]; then
  echo "RESULT: no scanner could run (all skipped). Install the FOSS tools / start Podman." >&2
  exit 2
fi
echo "RESULT: no findings from ${RAN} scanner(s); ${SKIPS} skipped honestly."
exit 0
