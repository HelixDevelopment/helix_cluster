#!/usr/bin/env bash
# helix-setup.sh — single-command Helix Cluster node onboarding entrypoint.
#
# Usage:
#   scripts/helix-setup.sh [--cluster-name NAME] [--node-id ID] [--data-dir DIR] [extra flags...]
#
# This script:
#   1. Detects the OS.
#   2. Prints any required platform-level MANUAL steps (driver install etc.)
#      — these are privileged host operations that must be run separately;
#        they are NEVER executed or faked by this script.
#   3. Locates or builds the helix-setup binary.
#   4. Invokes the binary, forwarding all arguments.
#
# The binary (cmd/helix-setup) does the real work: hardware detection,
# config generation, config file write, and mesh join.
#
# Exit codes mirror cmd/helix-setup:
#   0 — success
#   2 — usage / flag error
#   3 — prerequisite check failed
#   4 — write / context error
set -euo pipefail

# ─── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "[helix-setup] $*"; }
warn() { echo "[helix-setup] WARN: $*" >&2; }
die()  { echo "[helix-setup] ERROR: $*" >&2; exit 1; }

# ─── OS Detection ─────────────────────────────────────────────────────────────

OS="$(uname -s)"
log "Detected OS: ${OS}"

case "${OS}" in
  Linux)
    PLATFORM="linux"
    ;;
  Darwin)
    PLATFORM="darwin"
    ;;
  *)
    warn "Unsupported platform: ${OS}. Proceeding without platform steps."
    PLATFORM="other"
    ;;
esac

# ─── Platform-Level Manual Steps ──────────────────────────────────────────────
#
# These are HOST-level privileged operations that CANNOT be automated from a
# non-root user script and MUST NOT be faked. They are printed as clear
# instructions for the operator to run manually before or after this script.

if [[ "${PLATFORM}" == "linux" ]]; then
  log "-------------------------------------------------------------------"
  log "PLATFORM MANUAL STEPS (Linux) — run these if not already done:"
  log ""
  log "  # Install WireGuard kernel module:"
  log "  sudo apt-get install -y wireguard-tools   # Debian/Ubuntu"
  log "  sudo dnf install -y wireguard-tools        # Fedora/RHEL"
  log ""
  log "  # Install GPU drivers (if applicable):"
  log "  # NVIDIA:  sudo apt-get install -y nvidia-driver-XXX"
  log "  # AMD ROCm: see https://rocmdocs.amd.com/en/latest/deploy/linux/installer/install.html"
  log ""
  log "  # Ensure kernel WireGuard is loaded:"
  log "  sudo modprobe wireguard"
  log "-------------------------------------------------------------------"
fi

if [[ "${PLATFORM}" == "darwin" ]]; then
  log "-------------------------------------------------------------------"
  log "PLATFORM MANUAL STEPS (macOS) — run these if not already done:"
  log ""
  log "  # Install wireguard-go (userspace WireGuard for macOS):"
  log "  brew install wireguard-tools wireguard-go"
  log ""
  log "  # Install Metal / GPU tools (if applicable):"
  log "  xcode-select --install"
  log ""
  log "  # Ensure utun kernel extension is available (macOS 10.15+):"
  log "  # (built into macOS — no extra action required)"
  log "-------------------------------------------------------------------"
fi

# ─── Locate / Build Binary ────────────────────────────────────────────────────

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BINARY_PATH="${REPO_ROOT}/bin/helix-setup"

if [[ ! -x "${BINARY_PATH}" ]]; then
  log "Binary not found at ${BINARY_PATH}. Building..."
  if ! command -v go &>/dev/null; then
    die "Go is not installed. Install Go 1.24+ and retry, or pre-build helix-setup."
  fi
  (
    cd "${REPO_ROOT}"
    go build -o "${BINARY_PATH}" ./cmd/helix-setup/
  )
  log "Built: ${BINARY_PATH}"
else
  log "Using existing binary: ${BINARY_PATH}"
fi

# ─── Invoke Binary ────────────────────────────────────────────────────────────

log "Starting helix-setup ..."
exec "${BINARY_PATH}" "$@"
