-- ============================================================
-- 014_create_migration_history.up.sql
-- Helix Cluster OS — Session Migration History
-- ============================================================

CREATE TABLE migration_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id),
    source_node     UUID NOT NULL REFERENCES nodes(id),
    target_node     UUID NOT NULL REFERENCES nodes(id),
    method          VARCHAR(20) NOT NULL,
    duration_ms     INT NOT NULL,
    data_size_bytes BIGINT,
    success         BOOLEAN NOT NULL,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_migrations_session ON migration_history(session_id);
CREATE INDEX idx_migrations_source ON migration_history(source_node);
CREATE INDEX idx_migrations_target ON migration_history(target_node);
