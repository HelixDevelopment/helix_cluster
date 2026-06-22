#!/usr/bin/env bash
# =============================================================================
# agent-launch.sh — launch helix-agent on a worker host (config-driven).
# =============================================================================
# Runs ON the worker. Derives every value from arguments/env — NOTHING is
# hardcoded:
#   - node id / region / wg-port / swim-port : passed in by the orchestrator
#     (deploy/deploy-workers.sh), which sources them from deploy/cluster.env
#   - etcd endpoints  : passed in (host:port list reachable from the worker;
#                       derived from cluster.env + the auto-detected host IP)
#   - bind-addr       : AUTO-DETECTED as THIS worker's own LAN IP (never a
#                       literal); loopback and VPN/CGNAT (10.x, e.g. Mullvad
#                       wg0) addresses are excluded.
#   - binary name     : env HELIX_AGENT_BINARY (default helix-agent)
#
# Usage (positional order matches deploy-workers.sh):
#   agent-launch.sh <node-id> <etcd-endpoints> [region] [swim-port] [wg-port]
#
# The process is fully detached (setsid + </dev/null) so closing the SSH session
# does NOT deliver SIGHUP and self-terminate the agent (a root cause of the
# transient-registration symptom observed in D14).
# =============================================================================
set -u

ID="${1:?node id required}"
ETCD="${2:?etcd endpoints required}"
REGION="${3:-lan}"
SWIM_PORT="${4:-7946}"
WG_PORT="${5:-51820}"
BIN_NAME="${HELIX_AGENT_BINARY:-helix-agent}"
BINARY="./$BIN_NAME"
LAN_PREFIX="${HELIX_LAN_PREFIX:-192.168.}"

# --- Auto-detect this worker's own LAN IP (config-driven prefix) --------------
# Prefer an address on the LAN prefix; fall back to the first global-scope v4
# that is NOT loopback and NOT a VPN/CGNAT 10.x address. nezha's `hostname -I`
# is unsupported, so we use `ip -4 -o addr show scope global`.
detect_ip() {
  ip=$(ip -4 -o addr show scope global 2>/dev/null \
        | awk '{print $4}' | cut -d/ -f1 \
        | grep -E "^${LAN_PREFIX//./\\.}" | head -1)
  if [ -z "$ip" ]; then
    ip=$(ip -4 -o addr show scope global 2>/dev/null \
          | awk '{print $4}' | cut -d/ -f1 \
          | grep -vE '^(127\.|10\.)' | head -1)
  fi
  printf '%s' "$ip"
}
IP="$(detect_ip)"
if [ -z "$IP" ]; then
  echo "FATAL: could not auto-detect a LAN IP on this worker" >&2
  exit 1
fi

# --- Generate an ephemeral WireGuard key (never hardcoded) --------------------
WGKEY=$(openssl rand -base64 32 2>/dev/null || head -c 32 /dev/urandom | base64)

# --- Stop any previous instance BEFORE we touch the binary --------------------
# (Avoids "text file busy" when the orchestrator scp's a fresh binary.)
pkill -f "$BIN_NAME" 2>/dev/null || true
sleep 1

LOG="${HOME}/helix-agent.log"
# Fully detach so neither the SSH session closing (SIGHUP) NOR sshd/logind
# reaping the login session (SIGTERM to the session scope) can stop the agent.
#
# Preferred: a transient systemd --user SERVICE unit. Unlike `--scope`, a
# `--user --unit` service is adopted by the user manager and is fully decoupled
# from the SSH login session, so it survives logout. Over a non-interactive SSH
# command XDG_RUNTIME_DIR/DBUS are not exported, so we set XDG_RUNTIME_DIR
# explicitly (the user bus lives at /run/user/<uid>/bus). Verified on thinker
# and nezha (both Linger=yes).
#
# Fallback (no usable user systemd): setsid + nohup + </dev/null detaches into a
# new session with SIGHUP ignored.
UID_NUM="$(id -u)"
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$UID_NUM}"

started=""
if command -v systemd-run >/dev/null 2>&1 \
   && systemctl --user show-environment >/dev/null 2>&1; then
  # Clear any prior unit of the same name so a redeploy succeeds.
  systemctl --user stop "helix-agent-$ID.service" >/dev/null 2>&1 || true
  systemctl --user reset-failed "helix-agent-$ID.service" >/dev/null 2>&1 || true
  if systemd-run --user --unit="helix-agent-$ID" \
       "$BINARY" -id "$ID" -region "$REGION" \
         -etcd-endpoints "$ETCD" \
         -bind-addr "$IP" -bind-port "$SWIM_PORT" \
         -wg-key "$WGKEY" -wg-port "$WG_PORT" >/dev/null 2>&1; then
    started="systemd:helix-agent-$ID.service"
  fi
fi

if [ -z "$started" ]; then
  setsid nohup "$BINARY" -id "$ID" -region "$REGION" \
    -etcd-endpoints "$ETCD" \
    -bind-addr "$IP" -bind-port "$SWIM_PORT" \
    -wg-key "$WGKEY" -wg-port "$WG_PORT" \
    < /dev/null > "$LOG" 2>&1 &
  started="setsid+nohup"
fi
echo "launch-method=$started"

sleep 8
echo "node=$ID ip=$IP etcd=$ETCD"
if pgrep -x "$BIN_NAME" >/dev/null 2>&1; then
  echo "PROC: running"
  pgrep -af "$BIN_NAME" | grep -- "-id $ID" || pgrep -af "$BIN_NAME" | grep -v agent-launch
else
  echo "PROC: NOT running"
fi
echo "--- log tail ---"
# systemd path logs to the journal; setsid path logs to $LOG. Show whichever has it.
case "$started" in
  systemd:*)
    journalctl --user -u "helix-agent-$ID" --no-pager -n 30 2>/dev/null \
      || tail -30 "$LOG" 2>/dev/null || echo "no log" ;;
  *)
    tail -30 "$LOG" 2>/dev/null || echo "no log" ;;
esac
