#!/usr/bin/env bash
# =============================================================================
# unblock-live-testing.sh
# Privileged unblock + recording for the Helix live test campaign (2026-06-22/23).
#
# WHAT THIS UNBLOCKS (the only two things gated on privilege/credentials):
#   1. amber.local  — authorize mistborn's SSH key so amber can join as 4th node.
#   2. mistborn      — reset macOS Screen-Recording TCC state + open the pane so
#                      Phase-6 screen capture can be granted (the toggle itself is
#                      GUI — SIP blocks scripting the grant, even as root).
#   3. thinker/nezha — (idempotent) enable user-linger + optional capture tooling.
#
# Nothing is hardcoded beyond the embedded mistborn public key and the repo path:
# host IPs, ports, the screen-capture device index and node inventory are all
# detected at runtime or read from deploy/cluster.env.
#
# ---------------------------------------------------------------------------
# HOW TO RUN (per host):
#
#   # On amber.local, AS ROOT (or as milosvasic):     authorizes the SSH key
#   sudo ./scripts/unblock-live-testing.sh amber
#
#   # On thinker.local AND nezha.local, AS ROOT:       linger + capture tooling
#   sudo ./scripts/unblock-live-testing.sh unblock
#
#   # On mistborn (this host), AS ROOT:                resets TCC + opens pane
#   sudo ./scripts/unblock-live-testing.sh unblock
#        -> then do the ONE manual GUI toggle it prints, restart your terminal app.
#
#   # On mistborn, AS YOUR NORMAL USER (NOT root), AFTER the TCC toggle:
#   ./scripts/unblock-live-testing.sh record          # Phase-6 recording + validate + move to T7
#
#   # On mistborn, AS YOUR NORMAL USER, AFTER amber key is authorized:
#   ./scripts/unblock-live-testing.sh onboard-amber   # joins amber as 4th node
# =============================================================================
set -uo pipefail

REPO="/Volumes/T7/Projects/helix_cluster"
TARGET_USER="milosvasic"
MISTBORN_PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH9wn7lmJLnvED41oSZPoM0qAsafdfaKMsVyGKmX/sQI i@mvasic.ru"
RECORDINGS_DIR="/Volumes/T7/Downloads/Recordings"
HOSTN="$(hostname -s 2>/dev/null || hostname)"
OS="$(uname -s)"
MODE="${1:-unblock}"

log(){ echo "[unblock] $*"; }

# ---------------------------------------------------------------------------
# 1. amber: authorize mistborn's pubkey for milosvasic (run as root ON amber).
# ---------------------------------------------------------------------------
authorize_amber_key(){
  local grp home ak
  grp="$(id -gn "$TARGET_USER")"
  home="$(eval echo "~$TARGET_USER")"
  install -d -m 700 -o "$TARGET_USER" -g "$grp" "$home/.ssh"
  ak="$home/.ssh/authorized_keys"
  touch "$ak"
  if grep -qF "$MISTBORN_PUBKEY" "$ak"; then
    log "key already present in $ak"
  else
    printf '%s\n' "$MISTBORN_PUBKEY" >> "$ak"
    log "added mistborn key to $ak"
  fi
  chown "$TARGET_USER:$grp" "$ak"; chmod 600 "$ak"
  # SELinux contexts if applicable (no-op otherwise)
  command -v restorecon >/dev/null 2>&1 && restorecon -R "$home/.ssh" 2>/dev/null || true
  log "amber authorized_keys ready — mistborn can now SSH in by key."
}

# ---------------------------------------------------------------------------
# 2. thinker/nezha: enable linger (agents survive SSH logout) + optional tooling.
# ---------------------------------------------------------------------------
linux_worker_prep(){
  loginctl enable-linger "$TARGET_USER" 2>/dev/null \
    && log "linger enabled for $TARGET_USER (systemd user services survive logout)" \
    || log "WARN: could not enable linger (check loginctl)"
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get update -y >/dev/null 2>&1 || true
    if DEBIAN_FRONTEND=noninteractive apt-get install -y ffmpeg gstreamer1.0-tools >/dev/null 2>&1; then
      log "installed ffmpeg + gstreamer (optional Linux-side capture)"
    else
      log "WARN: ffmpeg/gstreamer install skipped/failed (non-fatal)"
    fi
  fi
  usermod -aG video,render "$TARGET_USER" 2>/dev/null \
    && log "added $TARGET_USER to video,render groups (DRM capture)" || true
  log "linux worker prep complete on $HOSTN."
}

# ---------------------------------------------------------------------------
# 3. mistborn: reset Screen-Recording TCC + open the pane (GUI toggle required).
# ---------------------------------------------------------------------------
mistborn_tcc_prep(){
  tccutil reset ScreenCapture >/dev/null 2>&1 && log "reset Screen-Recording TCC state" || true
  open "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture" 2>/dev/null || true
  cat <<EOF

  ========================= MANUAL STEP (root cannot script this) =========================
  macOS SIP protects the TCC database, so the grant itself is a GUI toggle:

    1. In the opened pane (Privacy & Security > Screen & System Audio Recording),
       ENABLE the toggle for the terminal/app that runs Claude Code
       (Terminal.app / iTerm2 / VS Code / etc.).
    2. FULLY QUIT and reopen that app (the grant only applies after a restart).

  Then, AS YOUR NORMAL USER (not root), on mistborn:
       $REPO/scripts/unblock-live-testing.sh record
  ========================================================================================

EOF
}

# ---------------------------------------------------------------------------
# Phase-6 driven flow shown on screen while recording.
# ---------------------------------------------------------------------------
driven_flow(){
  local E="helix-etcd-1"
  echo "################ HELIX CLUSTER — LIVE FLOW @ $(date) ################"
  echo; echo "## 1) Control-plane health (helixd /health)"
  curl -s http://localhost:8081/health; echo
  echo; echo "## 2) Worker node membership (etcd /clusteros/nodes)"
  podman exec "$E" etcdctl get /clusteros/nodes/ --prefix --keys-only | grep -E '/clusteros/nodes/[^/]+$'
  for n in $(podman exec "$E" etcdctl get /clusteros/nodes/ --prefix --keys-only | sed -nE 's#^/clusteros/nodes/([^/]+)$#\1#p'); do
    echo "   - $n:"; podman exec "$E" etcdctl get "/clusteros/nodes/$n" | tail -1; sleep 1
  done
  echo; echo "## 3) etcd quorum / membership"
  podman exec "$E" etcdctl member list -w table 2>&1 | head -8; sleep 1
  echo; echo "## 4) Infra containers"
  podman ps --format '{{.Names}}\t{{.Status}}' | grep '^helix-' | sort; sleep 1
  echo; echo "## 5) Control-plane challenges (live, anti-bluff)"
  for c in helixd_control_plane node_membership etcd_quorum; do
    local s="$REPO/challenges/challenges/scripts/${c}_challenge.sh"
    [ -f "$s" ] && { echo "   --- $c ---"; bash "$s" 2>&1 | tail -4; sleep 1; }
  done
  echo; echo "## 6) helixd metrics sample"
  curl -s http://localhost:8081/metrics 2>/dev/null | grep -iE '^helix|^go_goroutines' | head -5
  echo; echo "################ FLOW COMPLETE ################"
}

# ---------------------------------------------------------------------------
# record: capture the driven flow, validate liveness, move to T7 (run AS USER).
# ---------------------------------------------------------------------------
do_record(){
  [ "$(id -u)" = "0" ] && { echo "ERROR: run 'record' as your normal user, NOT root (TCC grants are per-user)."; exit 2; }
  command -v ffmpeg >/dev/null || { echo "ERROR: ffmpeg not on PATH"; exit 1; }
  local stamp ev out dev dur rc
  stamp="$(date +%Y%m%dT%H%M%SZ)"
  ev="$REPO/qa-results/live-20260622/recording"; mkdir -p "$ev"
  out="$ev/helix_cluster_flow_${stamp}.mp4"
  dur=85
  # Detect the screen-capture device index dynamically (it changes between runs).
  dev="$(ffmpeg -hide_banner -f avfoundation -list_devices true -i "" 2>&1 | grep 'Capture screen 0' | grep -oE '\[[0-9]+\]' | tr -d '[]' | head -1)"
  [ -n "$dev" ] || { echo "ERROR: no screen-capture device — Screen Recording not granted, or no GUI session."; exit 1; }
  echo "[record] capturing screen device [$dev] for ${dur}s -> $out"
  ffmpeg -y -hide_banner -loglevel warning -f avfoundation -framerate 30 -i "${dev}:none" \
         -t "$dur" -pix_fmt yuv420p "$out" &
  local ffpid=$!
  sleep 2
  driven_flow
  wait "$ffpid" 2>/dev/null
  [ -s "$out" ] || { echo "ERROR: capture produced no file (TCC likely denied frames)."; exit 1; }
  echo "[record] validating liveness (recording-analyzer §11.4.107) — NO bluff:"
  "$REPO/bin/recording-analyzer" -post-analyze -recording "$out" \
       -findings-out "$ev/findings_${stamp}.jsonl" 2>&1 | tee "$ev/analyze_${stamp}.log"
  rc=${PIPESTATUS[0]}
  if [ "$rc" = "0" ]; then
    mkdir -p "$RECORDINGS_DIR"
    mv "$out" "$RECORDINGS_DIR/"
    echo "[record] ✅ VALIDATED (live, advancing) + MOVED -> $RECORDINGS_DIR/$(basename "$out")"
  else
    echo "[record] ❌ FAILED liveness validation (rc=$rc) — NOT moved (anti-bluff). See $ev/analyze_${stamp}.log"
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# onboard-amber: add amber to the config-driven inventory + deploy (run AS USER).
# ---------------------------------------------------------------------------
onboard_amber(){
  [ "$(id -u)" = "0" ] && { echo "ERROR: run 'onboard-amber' as your normal user, not root."; exit 2; }
  ssh -o BatchMode=yes -o ConnectTimeout=6 "${TARGET_USER}@amber.local" 'echo OK' >/dev/null 2>&1 \
    || { echo "ERROR: amber not reachable by key yet — run 'unblock-live-testing.sh amber' on amber first."; exit 1; }
  local env="$REPO/deploy/cluster.env"
  grep -q '^HELIX_NODE_amber=' "$env" || {
    printf 'HELIX_NODE_amber="amber|amber.local|lan|51820"\n' >> "$env"
    log "added HELIX_NODE_amber to cluster.env"
  }
  grep -E '^HELIX_NODES=.*\bamber\b' "$env" >/dev/null \
    || sed -i '' -E 's/^(HELIX_NODES="[^"]*)"/\1 amber"/' "$env"
  log "deploying amber via config-driven deploy-workers.sh ..."
  ( cd "$REPO" && bash deploy/deploy-workers.sh amber )
  sleep 8
  echo "[onboard-amber] verify:"
  podman exec helix-etcd-1 etcdctl get /clusteros/nodes/ --prefix --keys-only | grep -E '/clusteros/nodes/amber$' \
    && echo "[onboard-amber] ✅ amber registered as 4th node" \
    || echo "[onboard-amber] ⚠️ amber not yet registered — check ~/helix-agent.log on amber"
}

# ---------------------------------------------------------------------------
case "$MODE" in
  unblock)
    if [ "$OS" = "Darwin" ]; then
      mistborn_tcc_prep
    elif printf '%s' "$HOSTN" | grep -qi 'amber'; then
      authorize_amber_key
    else
      linux_worker_prep
    fi ;;
  amber)          authorize_amber_key ;;
  record)         do_record ;;
  onboard-amber)  onboard_amber ;;
  *) echo "usage: $0 [unblock|amber|record|onboard-amber]"; exit 2 ;;
esac
