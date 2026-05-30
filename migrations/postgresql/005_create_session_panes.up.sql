-- ============================================================
-- 005_create_session_panes.up.sql
-- Helix Cluster OS — Session Panes
-- ============================================================

CREATE TABLE session_panes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    window_id       UUID NOT NULL REFERENCES session_windows(id) ON DELETE CASCADE,
    node_id         UUID REFERENCES nodes(id),
    command         TEXT,
    working_dir     TEXT,
    environment     JSONB DEFAULT '{}',
    cpu_limit       INT,
    memory_limit    BIGINT,
    gpu_id          UUID REFERENCES gpu_devices(id),
    status          VARCHAR(20) NOT NULL DEFAULT 'CREATING',
    crdt_state      JSONB DEFAULT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_panes_window ON session_panes(window_id);
CREATE INDEX idx_panes_node ON session_panes(node_id);
CREATE INDEX idx_panes_gpu ON session_panes(gpu_id);
CREATE INDEX idx_panes_status ON session_panes(status);
