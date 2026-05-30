-- ============================================================
-- 004_create_session_windows.up.sql
-- Helix Cluster OS — Session Windows
-- ============================================================

CREATE TABLE session_windows (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name            VARCHAR(255) NOT NULL,
    layout          VARCHAR(50) NOT NULL DEFAULT 'tiled',
    active          BOOLEAN NOT NULL DEFAULT FALSE,
    crdt_state      JSONB DEFAULT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_windows_session ON session_windows(session_id);
CREATE INDEX idx_windows_active ON session_windows(active);
