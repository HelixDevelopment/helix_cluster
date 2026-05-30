#!/bin/bash
# HXC Ticket Management Helper
# Usage:
#   bash scripts/hxc.sh next          # Show next available HXC number
#   bash scripts/hxc.sh list          # List all tickets
#   bash scripts/hxc.sh list active   # List active tickets
#   bash scripts/hxc.sh show HXC-200  # Show ticket details
#   bash scripts/hxc.sh create "Title" "Phase" "Type"  # Create new ticket

set -euo pipefail

REGISTRY="docs/HXC_REGISTRY.md"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REGISTRY_PATH="${SCRIPT_DIR}/../${REGISTRY}"

if [[ ! -f "$REGISTRY_PATH" ]]; then
    echo "Error: Registry not found at $REGISTRY_PATH" >&2
    exit 1
fi

cmd="${1:-help}"

hxc_next() {
    local max_num
    max_num=$(grep -oE 'HXC-[0-9]+' "$REGISTRY_PATH" | sed 's/HXC-//' | sort -n | tail -1)
    if [[ -z "$max_num" ]]; then
        max_num=0
    fi
    local next_num=$((max_num + 1))
    printf "HXC-%03d\n" "$next_num"
}

hxc_list() {
    local filter="${1:-all}"
    echo "=== HXC Tickets ==="
    echo ""
    if [[ "$filter" == "active" ]]; then
        echo "--- Active Tickets ---"
        awk '/^## Active Tickets/{flag=1; next} /^## /{flag=0} flag && /^\| HXC-/' "$REGISTRY_PATH"
    elif [[ "$filter" == "completed" ]]; then
        echo "--- Completed Tickets ---"
        awk '/^## Completed Tickets/{flag=1; next} /^## /{flag=0} flag && /^\| HXC-/' "$REGISTRY_PATH"
    else
        echo "--- Active Tickets ---"
        awk '/^## Active Tickets/{flag=1; next} /^## /{flag=0} flag && /^\| HXC-/' "$REGISTRY_PATH"
        echo ""
        echo "--- Completed Tickets ---"
        awk '/^## Completed Tickets/{flag=1; next} /^## /{flag=0} flag && /^\| HXC-/' "$REGISTRY_PATH"
    fi
}

hxc_show() {
    local ticket_id="${1:-}"
    if [[ -z "$ticket_id" ]]; then
        echo "Usage: hxc.sh show HXC-XXX" >&2
        exit 1
    fi

    # Normalize to uppercase
    ticket_id="$(echo "$ticket_id" | tr '[:lower:]' '[:upper:]')"

    local line
    line=$(grep -E "^\| ${ticket_id} " "$REGISTRY_PATH" || true)
    if [[ -z "$line" ]]; then
        echo "Ticket $ticket_id not found in registry." >&2
        exit 1
    fi

    echo "=== $ticket_id ==="
    echo ""
    echo "Phase:   $(echo "$line" | awk -F'|' '{print $3}' | xargs)"
    echo "Type:    $(echo "$line" | awk -F'|' '{print $4}' | xargs)"
    echo "Status:  $(echo "$line" | awk -F'|' '{print $5}' | xargs)"
    echo "Title:   $(echo "$line" | awk -F'|' '{print $6}' | xargs)"
    echo "Commit:  $(echo "$line" | awk -F'|' '{print $7}' | xargs)"
}

hxc_create() {
    local title="${1:-}"
    local phase="${2:-}"
    local type="${3:-Task}"

    if [[ -z "$title" || -z "$phase" ]]; then
        echo "Usage: hxc.sh create \"Title\" \"Phase\" [Type]" >&2
        echo "  Phase: Phase 0, Phase 1, ..., Phase 8, Cross-cutting" >&2
        echo "  Type:  Feature, Bug, Chore, Test, Audit, Verification, Governance, Tracking, Task (default)" >&2
        exit 1
    fi

    local next_id
    next_id=$(hxc_next)

    # Determine phase label
    local phase_label="$phase"

    local new_line="| ${next_id} | ${phase_label} | ${type} | Queued | ${title} | — |"

    # Insert into Active Tickets section (before the first blank line after the header)
    awk -v line="$new_line" '
        /^## Active Tickets/ { print; getline; print; getline; print; found=1; next }
        found && /^\|[-]+\|/ { print; getline; print line; found=0; next }
        { print }
    ' "$REGISTRY_PATH" > "${REGISTRY_PATH}.tmp"

    mv "${REGISTRY_PATH}.tmp" "$REGISTRY_PATH"

    # Update last modified timestamp
    sed -i.bak "s/^\*\*Last modified:\*\* .*/\*\*Last modified:\*\* $(date -u +%Y-%m-%dT%H:%M:%SZ)/" "$REGISTRY_PATH"
    rm -f "${REGISTRY_PATH}.bak"

    echo "Created $next_id: $title"
    echo "Status: Queued"
    echo "Remember to commit with: Refs: $next_id"
}

hxc_help() {
    cat <<'EOF'
HXC Ticket Management Helper

Usage:
  bash scripts/hxc.sh next                    Show next available HXC number
  bash scripts/hxc.sh list                    List all tickets
  bash scripts/hxc.sh list active             List active tickets only
  bash scripts/hxc.sh list completed          List completed tickets only
  bash scripts/hxc.sh show HXC-200            Show ticket details
  bash scripts/hxc.sh create "Title" "Phase" [Type]
                                              Create a new ticket

Examples:
  bash scripts/hxc.sh create "Fix race in discovery" "Phase 2" "Bug"
  bash scripts/hxc.sh show HXC-001
EOF
}

case "$cmd" in
    next)
        hxc_next
        ;;
    list)
        hxc_list "${2:-all}"
        ;;
    show)
        hxc_show "${2:-}"
        ;;
    create)
        hxc_create "${2:-}" "${3:-}" "${4:-Task}"
        ;;
    help|--help|-h)
        hxc_help
        ;;
    *)
        echo "Unknown command: $cmd" >&2
        hxc_help
        exit 1
        ;;
esac
