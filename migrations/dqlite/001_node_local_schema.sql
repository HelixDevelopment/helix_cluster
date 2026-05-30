-- ============================================================
-- dqlite/001_node_local_schema.sql
-- Helix Cluster OS — Per-Node SQLite (dqlite) Schema
-- ============================================================
-- dqlite provides Raft-replicated SQLite for per-node state
-- that must survive node restarts and be consistent across
-- local replicas.
-- ============================================================

-- Local node identity and configuration
CREATE TABLE IF NOT EXISTS local_node (
    node_id         TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    wg_pubkey       TEXT NOT NULL UNIQUE,
    spiffe_id       TEXT NOT NULL UNIQUE,
    role            TEXT NOT NULL DEFAULT 'WORKER',
    region          TEXT,
    version         TEXT NOT NULL,
    config          BLOB,
    joined_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local resource inventory (snapshot, refreshed on change)
CREATE TABLE IF NOT EXISTS local_resources (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_type   TEXT NOT NULL, -- cpu, memory, gpu, storage, network
    name            TEXT NOT NULL,
    total           INTEGER NOT NULL,
    available       INTEGER NOT NULL,
    unit            TEXT NOT NULL, -- cores, bytes, bps
    attributes      BLOB,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local GPU devices (subset of PostgreSQL gpu_devices)
CREATE TABLE IF NOT EXISTS local_gpus (
    id              TEXT PRIMARY KEY,
    vendor          TEXT NOT NULL,
    model           TEXT NOT NULL,
    driver_version  TEXT NOT NULL,
    api             TEXT NOT NULL,
    total_memory    INTEGER NOT NULL,
    compute_units   INTEGER NOT NULL,
    status          TEXT NOT NULL DEFAULT 'AVAILABLE',
    allocated_to    TEXT,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Active local sessions and their backends
CREATE TABLE IF NOT EXISTS local_sessions (
    session_id      TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'CREATING',
    backend         TEXT NOT NULL DEFAULT 'TMUX',
    backend_pid     INTEGER,
    node_id         TEXT,
    resources       BLOB,
    started_at      DATETIME,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local session windows (CRDT state stored as blob)
CREATE TABLE IF NOT EXISTS local_windows (
    window_id       TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES local_sessions(session_id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    layout          TEXT NOT NULL DEFAULT 'tiled',
    active          INTEGER NOT NULL DEFAULT 0,
    crdt_state      BLOB,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local session panes
CREATE TABLE IF NOT EXISTS local_panes (
    pane_id         TEXT PRIMARY KEY,
    window_id       TEXT NOT NULL REFERENCES local_windows(window_id) ON DELETE CASCADE,
    command         TEXT,
    working_dir     TEXT,
    environment     BLOB,
    pty_path        TEXT,
    status          TEXT NOT NULL DEFAULT 'CREATING',
    crdt_state      BLOB,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Peer WireGuard endpoints (for mesh reconnection)
CREATE TABLE IF NOT EXISTS local_peers (
    node_id         TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    wg_pubkey       TEXT NOT NULL UNIQUE,
    endpoint_host   TEXT,
    endpoint_port   INTEGER,
    allowed_ips     TEXT,
    last_handshake  INTEGER,
    status          TEXT NOT NULL DEFAULT 'ACTIVE',
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local task queue (build jobs, scheduled tasks)
CREATE TABLE IF NOT EXISTS local_tasks (
    task_id         TEXT PRIMARY KEY,
    task_type       TEXT NOT NULL,
    payload         BLOB NOT NULL,
    priority        INTEGER NOT NULL DEFAULT 50,
    status          TEXT NOT NULL DEFAULT 'PENDING',
    retry_count     INTEGER NOT NULL DEFAULT 0,
    max_retries     INTEGER NOT NULL DEFAULT 3,
    scheduled_at    DATETIME,
    started_at      DATETIME,
    completed_at    DATETIME,
    error_message   TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Local health metrics cache (last N samples)
CREATE TABLE IF NOT EXISTS local_health (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    overall_score   INTEGER NOT NULL,
    cpu_score       INTEGER NOT NULL,
    memory_score    INTEGER NOT NULL,
    disk_score      INTEGER NOT NULL,
    network_score   INTEGER NOT NULL,
    gpu_score       INTEGER NOT NULL,
    temperature_score INTEGER NOT NULL,
    services_score  INTEGER NOT NULL,
    metrics         BLOB,
    recorded_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indices
CREATE INDEX IF NOT EXISTS idx_local_sessions_status ON local_sessions(status);
CREATE INDEX IF NOT EXISTS idx_local_tasks_status ON local_tasks(status);
CREATE INDEX IF NOT EXISTS idx_local_tasks_scheduled ON local_tasks(scheduled_at);
CREATE INDEX IF NOT EXISTS idx_local_health_recorded ON local_health(recorded_at);
CREATE INDEX IF NOT EXISTS idx_local_peers_status ON local_peers(status);
