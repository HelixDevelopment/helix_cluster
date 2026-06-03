#!/usr/bin/env bash
# gen-sbom.sh — Generate CycloneDX SBOMs for HelixCluster modules (HXC-1635).
#
# Emits one CycloneDX 1.6 JSON SBOM per tracked Go module into sbom/:
#   sbom/helixcluster.cdx.json  (main module github.com/HelixDevelopment/helix_cluster)
#   sbom/api-v1.cdx.json        (github.com/HelixDevelopment/helix_cluster/api/v1)
#   sbom/security.cdx.json      (digital.vasic.security)
#
# Why GOWORK=off + per-module: under the repo's go.work, every `use` module is
# reported as Main:true, so cyclonedx-gomod cannot tell which module is the SBOM
# subject and always picks the alphabetically-first one. Disabling the workspace
# and generating per-module yields a correct, distinct root component for each.
#
# The generated *.cdx.json files are build derivatives (CONST-053) and are
# gitignored; this script + the Makefile `sbom` target are the tracked deliverable.
#
# Requires: cyclonedx-gomod on PATH
#   go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${REPO_ROOT}/sbom"

if ! command -v cyclonedx-gomod >/dev/null 2>&1; then
  echo "error: cyclonedx-gomod not found on PATH." >&2
  echo "       install with: go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@latest" >&2
  echo "       and ensure \$(go env GOPATH)/bin (usually ~/go/bin) is on PATH." >&2
  exit 1
fi

mkdir -p "${OUT_DIR}"

# gen_sbom <module-dir-relative-to-repo> <output-file-name>
# Runs in a fully workspace-disabled, restore-safe manner: go.mod/go.sum are
# snapshotted and restored so the working tree is never left dirty, and any
# root-level .DS_Store (which trips cyclonedx-gomod's module hashing) is moved
# aside for the duration of the run.
gen_sbom() {
  local mod_dir="$1"
  local out_file="$2"
  local abs_dir="${REPO_ROOT}/${mod_dir}"
  local out_path="${OUT_DIR}/${out_file}"

  echo ">> SBOM: ${mod_dir} -> sbom/${out_file}"

  # Snapshot module files so we can guarantee a pristine tree afterwards.
  cp "${abs_dir}/go.mod" "${abs_dir}/.go.mod.sbombak"
  local had_gosum=0
  if [ -f "${abs_dir}/go.sum" ]; then
    cp "${abs_dir}/go.sum" "${abs_dir}/.go.sum.sbombak"
    had_gosum=1
  fi
  local moved_dsstore=0
  if [ -f "${REPO_ROOT}/.DS_Store" ]; then
    mv "${REPO_ROOT}/.DS_Store" "${REPO_ROOT}/.DS_Store.sbombak"
    moved_dsstore=1
  fi

  restore() {
    mv "${abs_dir}/.go.mod.sbombak" "${abs_dir}/go.mod"
    if [ "${had_gosum}" -eq 1 ]; then
      mv "${abs_dir}/.go.sum.sbombak" "${abs_dir}/go.sum"
    else
      rm -f "${abs_dir}/go.sum"
    fi
    if [ "${moved_dsstore}" -eq 1 ]; then
      mv "${REPO_ROOT}/.DS_Store.sbombak" "${REPO_ROOT}/.DS_Store"
    fi
  }
  trap restore RETURN

  # Warm the module cache and complete go.sum entries (workspace-off, module-aware).
  # This may mutate go.mod/go.sum; we restore them after generation.
  ( cd "${abs_dir}" && GOWORK=off GOFLAGS=-mod=mod go mod download all >/dev/null 2>&1 ) || true

  # Generate the SBOM with the workspace disabled and a clean -mod=readonly so the
  # subject module is unambiguous. GOFLAGS is intentionally NOT set here.
  ( cd "${abs_dir}" && env -u GOFLAGS GOWORK=off cyclonedx-gomod mod \
      -json -licenses -output "${out_path}" . )
}

gen_sbom "."        "helixcluster.cdx.json"
gen_sbom "api/v1"   "api-v1.cdx.json"
gen_sbom "security" "security.cdx.json"

echo
echo "Generated SBOMs in sbom/:"
for f in helixcluster.cdx.json api-v1.cdx.json security.cdx.json; do
  p="${OUT_DIR}/${f}"
  name="$(jq -r '.metadata.component.name' "${p}")"
  comps="$(jq '.components | length' "${p}")"
  echo "  ${f}: ${name} (${comps} components)"
done
