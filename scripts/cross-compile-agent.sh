#!/usr/bin/env bash
#
# cross-compile-agent.sh — reproducible ARM64 cross-compilation toolchain for
# the Helix node agent (cmd/helix-agent), which is PURE GO (CGO not required).
#
# HXC-1167. Emits, into dist/, one binary per target triple, each stamped with a
# shared build UUID + version via `-ldflags -X`:
#
#   helix-agent-linux-arm64    GOOS=linux   GOARCH=arm64        CGO_ENABLED=0
#   helix-agent-linux-armv7    GOOS=linux   GOARCH=arm GOARM=7  CGO_ENABLED=0
#   helix-agent-android-arm64  GOOS=android GOARCH=arm64        CGO_ENABLED=0
#
# Reproducibility:
#   * -trimpath strips absolute build paths from the binary.
#   * The SAME BUILD_UUID is stamped into every artifact of one invocation, so
#     the three per-arch outputs of a single run are correlatable. Override it
#     for a byte-reproducible rebuild:  BUILD_UUID=<uuid> VERSION=<v> ./...
#   * SOURCE_DATE_EPOCH is honored if set (passed through to the Go toolchain).
#
# Honest deferrals (CLAUDE-1 / §11.4.3 — never faked as a PASS):
#   (1) CGO-ENABLED cross builds need a C cross toolchain (`zig cc -target
#       aarch64-linux-gnu` or aarch64-linux-gnu-gcc). When such a tool is
#       DETECTED on the host we additionally build a CGO-enabled variant; when
#       ABSENT we print an honest skip line and DO NOT fail (ToolAbsentError).
#   (2) On-real-Orange-Pi-5-Max execution is DEFERRED hardware: this script
#       produces the artifacts; running them on the SBC is out of host scope.
#
# This script is pure stdlib tooling: it adds no module dependency and never
# touches go.mod / go.sum.

set -euo pipefail

# --- locate repo root (this script lives in scripts/) ---------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# DIST_DIR is overridable so a test harness can point the build at a scratch
# directory instead of polluting the repo's dist/. Production callers leave it
# unset and get the canonical ${REPO_ROOT}/dist.
DIST_DIR="${DIST_DIR:-${REPO_ROOT}/dist}"
PKG="./cmd/helix-agent"

# --- build identity (overridable for reproducible rebuilds) ---------------
VERSION="${VERSION:-$(git -C "${REPO_ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)}"
# A single UUID shared across all artifacts of THIS invocation. Prefer a real
# UUID source; fall back to a timestamp+random if none is present.
if [[ -z "${BUILD_UUID:-}" ]]; then
  if command -v uuidgen >/dev/null 2>&1; then
    BUILD_UUID="$(uuidgen)"
  elif [[ -r /proc/sys/kernel/random/uuid ]]; then
    BUILD_UUID="$(cat /proc/sys/kernel/random/uuid)"
  else
    BUILD_UUID="build-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  fi
fi

LDFLAGS="-s -w -X main.Version=${VERSION} -X main.BuildUUID=${BUILD_UUID}"

mkdir -p "${DIST_DIR}"

echo "==> helix-agent reproducible cross-build"
echo "    version    : ${VERSION}"
echo "    build uuid : ${BUILD_UUID}"
echo "    output dir : ${DIST_DIR}"
echo

# build_target <label> <goos> <goarch> <goarm>
build_target() {
  local out="$1" goos="$2" goarch="$3" goarm="${4:-}"
  echo "--> ${out}  (GOOS=${goos} GOARCH=${goarch} GOARM=${goarm:-} CGO_ENABLED=0)"
  local -a env_vars=("GOOS=${goos}" "GOARCH=${goarch}" "CGO_ENABLED=0")
  if [[ -n "${goarm}" ]]; then
    env_vars+=("GOARM=${goarm}")
  fi
  env "${env_vars[@]}" \
    go build -trimpath -ldflags "${LDFLAGS}" -o "${DIST_DIR}/${out}" "${PKG}"
}

build_target "helix-agent-linux-arm64"   linux   arm64
build_target "helix-agent-linux-armv7"   linux   arm 7
build_target "helix-agent-android-arm64" android arm64

# --- optional CGO-enabled cross builds (only if a C cross toolchain exists) -
# Detect zig (`zig cc`) or a gnu aarch64 cross-gcc. Absence is an honest SKIP,
# not a failure — these variants are a deferral, not a host-closeable promise.
CC_AARCH64=""
if command -v zig >/dev/null 2>&1; then
  CC_AARCH64="zig cc -target aarch64-linux-gnu"
elif command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
  CC_AARCH64="aarch64-linux-gnu-gcc"
fi

echo
if [[ -n "${CC_AARCH64}" ]]; then
  echo "--> CGO toolchain detected (${CC_AARCH64%% *}); building CGO-enabled aarch64 variant"
  GOOS=linux GOARCH=arm64 CGO_ENABLED=1 CC="${CC_AARCH64}" \
    go build -trimpath -ldflags "${LDFLAGS}" \
      -o "${DIST_DIR}/helix-agent-linux-arm64-cgo" "${PKG}"
  echo "    wrote ${DIST_DIR}/helix-agent-linux-arm64-cgo"
else
  echo "SKIP: CGO-enabled aarch64 cross build — no 'zig cc' or 'aarch64-linux-gnu-gcc' on host."
  echo "      (ToolAbsentError / §11.4.3 SKIP-with-reason. helix-agent is pure Go, so the"
  echo "       CGO_ENABLED=0 artifacts above are the production deliverable; the CGO variant"
  echo "       is an optional extra requiring a C cross toolchain.)"
fi

echo
echo "SKIP: on-real-Orange-Pi-5-Max execution — DEFERRED hardware (no SBC / qemu on host)."
echo
echo "==> done. Artifacts:"
ls -l "${DIST_DIR}"/helix-agent-* 2>/dev/null || true
