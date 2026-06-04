-- ============================================================
-- 005_create_session_panes.up.sql
-- Helix Cluster OS — Session Panes
-- ============================================================

CREATE TABLE IF NOT EXISTS session_panes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    window_id   UUID NOT NULL REFERENCES session_windows(id) ON DELETE CASCADE,
    node_id     UUID REFERENCES nodes(id),
    command     TEXT,
    working_dir TEXT,
    environment JSONB NOT NULL DEFAULT '{}',
    cpu_limit   INT,
    memory_limit BIGINT,
    gpu_id      UUID REFERENCES gpu_devices(id),
    status      VARCHAR(20) NOT NULL DEFAULT 'CREATING',
    crdt_state  JSONB DEFAULT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sp_status_valid   CHECK (status IN (
        'CREATING', 'RUNNING', 'STOPPED', 'FAILED', 'MIGRATING'
    )),
    CONSTRAINT sp_cpu_limit_pos  CHECK (cpu_limit IS NULL OR cpu_limit > 0),
    CONSTRAINT sp_mem_limit_pos  CHECK (memory_limit IS NULL OR memory_limit > 0)
);

CREATE INDEX IF NOT EXISTS idx_session_panes_window_id ON session_panes(window_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_node_id   ON session_panes(node_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_gpu_id    ON session_panes(gpu_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_status    ON session_panes(status);
