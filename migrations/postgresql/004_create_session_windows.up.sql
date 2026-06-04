-- ============================================================
-- 004_create_session_windows.up.sql
-- Helix Cluster OS — Session Windows
-- ============================================================

CREATE TABLE IF NOT EXISTS session_windows (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    layout      VARCHAR(50) NOT NULL DEFAULT 'tiled',
    active      BOOLEAN NOT NULL DEFAULT FALSE,
    crdt_state  JSONB DEFAULT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sw_layout_valid CHECK (layout IN (
        'tiled', 'even-horizontal', 'even-vertical', 'main-horizontal', 'main-vertical'
    ))
);

CREATE INDEX IF NOT EXISTS idx_session_windows_session_id ON session_windows(session_id);
CREATE INDEX IF NOT EXISTS idx_session_windows_active     ON session_windows(active);
