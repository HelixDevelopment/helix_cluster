-- ============================================================
-- 003_create_sessions.up.sql
-- Helix Cluster OS — Sessions
-- ============================================================

CREATE TABLE IF NOT EXISTS sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    owner           TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'CREATING',
    mode            VARCHAR(20) NOT NULL DEFAULT 'INTERACTIVE',
    backend         VARCHAR(20) NOT NULL DEFAULT 'TMUX',
    backend_id      TEXT,
    node_id         UUID REFERENCES nodes(id),
    cpu_request     INT NOT NULL DEFAULT 1000,
    memory_request  BIGINT NOT NULL DEFAULT 1073741824,
    gpu_request     JSONB DEFAULT NULL,
    priority        INT NOT NULL DEFAULT 50,
    labels          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    terminated_at   TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sessions_status_valid   CHECK (status IN (
        'CREATING', 'RUNNING', 'PAUSED', 'TERMINATING', 'TERMINATED', 'FAILED'
    )),
    CONSTRAINT sessions_mode_valid     CHECK (mode IN (
        'INTERACTIVE', 'BATCH', 'DAEMON', 'NOTEBOOK'
    )),
    CONSTRAINT sessions_backend_valid  CHECK (backend IN (
        'TMUX', 'WASM', 'CONTAINER', 'NATIVE'
    )),
    CONSTRAINT sessions_priority_range CHECK (priority >= 0 AND priority <= 100),
    CONSTRAINT sessions_cpu_positive   CHECK (cpu_request > 0),
    CONSTRAINT sessions_mem_positive   CHECK (memory_request > 0)
);

CREATE INDEX IF NOT EXISTS idx_sessions_owner    ON sessions(owner);
CREATE INDEX IF NOT EXISTS idx_sessions_status   ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_node_id  ON sessions(node_id);
CREATE INDEX IF NOT EXISTS idx_sessions_mode     ON sessions(mode);
CREATE INDEX IF NOT EXISTS idx_sessions_priority ON sessions(priority);
CREATE INDEX IF NOT EXISTS idx_sessions_labels   ON sessions USING GIN(labels);
