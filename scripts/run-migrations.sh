#!/usr/bin/env bash
# ============================================================
# run-migrations.sh
# Helix Cluster OS — PostgreSQL Migration Runner
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
MIGRATIONS_DIR="${PROJECT_ROOT}/migrations/postgresql"

# Defaults
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_NAME="${DB_NAME:-helix_cluster}"
DB_USER="${DB_USER:-helix}"
DB_PASS="${DB_PASS:-helix}"
DB_SSLMODE="${DB_SSLMODE:-disable}"

# Detect migrate binary
MIGRATE_BIN=""
if command -v migrate &>/dev/null; then
    MIGRATE_BIN="migrate"
elif [ -x "${HOME}/go/bin/migrate" ]; then
    MIGRATE_BIN="${HOME}/go/bin/migrate"
else
    echo "ERROR: golang-migrate 'migrate' binary not found. Install with:"
    echo "  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"
    exit 1
fi

# Honor a pre-set DATABASE_URL (e.g. from the Makefile / CI / operator env).
# When unset, build it from the DB_* components above. No secret is hardcoded;
# the password comes from $DB_PASS (default 'helix' for local dev only).
DATABASE_URL="${DATABASE_URL:-postgres://${DB_USER}:${DB_PASS}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DB_SSLMODE}}"

usage() {
    cat <<EOF
Usage: $(basename "$0") <command> [args]

Commands:
  up [N]          Apply all (or N) pending migrations
  down [N]        Rollback all (or N) migrations
  version         Print current migration version
  force V         Force set version to V (use with caution)
  create NAME     Create a new migration pair (up/down)
  validate        Run up -> down -> up to validate all migrations
  status          Show migration status

Environment:
  DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASS, DB_SSLMODE

Examples:
  DB_PASS=secret ./scripts/run-migrations.sh up
  ./scripts/run-migrations.sh validate
EOF
}

cmd_up() {
    local n="${1:-}"
    if [ -n "$n" ]; then
        echo "==> Applying $n migration(s)..."
        "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" up "$n"
    else
        echo "==> Applying all pending migrations..."
        "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" up
    fi
}

cmd_down() {
    local n="${1:-}"
    if [ -n "$n" ]; then
        echo "==> Rolling back $n migration(s)..."
        "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" down "$n"
    else
        echo "==> Rolling back 1 migration..."
        "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" down 1
    fi
}

cmd_version() {
    "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" version
}

cmd_force() {
    local v="${1:-}"
    if [ -z "$v" ]; then
        echo "ERROR: version required"
        exit 1
    fi
    "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" force "$v"
}

cmd_create() {
    local name="${1:-}"
    if [ -z "$name" ]; then
        echo "ERROR: migration name required"
        exit 1
    fi
    "$MIGRATE_BIN" create -ext sql -dir "$MIGRATIONS_DIR" -seq "$name"
}

cmd_validate() {
    echo "==> Validation: migrate up"
    cmd_up
    echo "==> Validation: migrate down"
    "$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" down 15
    echo "==> Validation: migrate up again"
    cmd_up
    echo "==> Validation PASSED"
}

cmd_status() {
    echo "Migrations directory: $MIGRATIONS_DIR"
    echo "Database: $DATABASE_URL"
    echo ""
    local version_info
    version_info=$("$MIGRATE_BIN" -path "$MIGRATIONS_DIR" -database "$DATABASE_URL" version 2>/dev/null || true)
    echo "Current version: ${version_info:-unknown}"
    echo ""
    echo "Migration files:"
    ls -1 "$MIGRATIONS_DIR"/*.up.sql 2>/dev/null | sed 's|.*/||' | sort || true
}

# Main
case "${1:-}" in
    up)       shift; cmd_up "$@" ;;
    down)     shift; cmd_down "$@" ;;
    version)  cmd_version ;;
    force)    shift; cmd_force "$@" ;;
    create)   shift; cmd_create "$@" ;;
    validate) cmd_validate ;;
    status)   cmd_status ;;
    *)        usage; exit 1 ;;
esac
