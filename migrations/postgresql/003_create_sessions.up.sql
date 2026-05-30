-- ============================================================
-- 003_create_sessions.up.sql
-- Helix Cluster OS — Sessions
-- ============================================================

CREATE TABLE sessions (
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
    labels          JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    terminated_at   TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sessions_owner ON sessions(owner);
CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_node ON sessions(node_id);
CREATE INDEX idx_sessions_mode ON sessions(mode);
