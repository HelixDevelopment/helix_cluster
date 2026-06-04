-- ============================================================
-- 014_create_migration_history.up.sql
-- Helix Cluster OS — Session Migration History
-- ============================================================

CREATE TABLE IF NOT EXISTS migration_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id),
    source_node     UUID NOT NULL REFERENCES nodes(id),
    target_node     UUID NOT NULL REFERENCES nodes(id),
    method          VARCHAR(20) NOT NULL,
    duration_ms     INT NOT NULL,
    data_size_bytes BIGINT,
    success         BOOLEAN NOT NULL,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT mh_method_valid      CHECK (method IN (
        'CHECKPOINT', 'LIVE', 'COLD', 'SNAPSHOT'
    )),
    CONSTRAINT mh_duration_nonneg   CHECK (duration_ms >= 0),
    CONSTRAINT mh_nodes_differ      CHECK (source_node <> target_node)
);

CREATE INDEX IF NOT EXISTS idx_migration_history_session_id   ON migration_history(session_id);
CREATE INDEX IF NOT EXISTS idx_migration_history_source_node  ON migration_history(source_node);
CREATE INDEX IF NOT EXISTS idx_migration_history_target_node  ON migration_history(target_node);
CREATE INDEX IF NOT EXISTS idx_migration_history_success      ON migration_history(success);
