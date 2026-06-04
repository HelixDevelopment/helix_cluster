-- ============================================================
-- 006_create_reservations.up.sql
-- Helix Cluster OS — Reservations (resource reservations)
-- ============================================================

CREATE TABLE IF NOT EXISTS reservations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id         UUID NOT NULL REFERENCES nodes(id),
    cpu_millicores  INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_ids         UUID[] NOT NULL DEFAULT '{}',
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT reservations_status_valid   CHECK (status IN (
        'PENDING', 'ACTIVE', 'RELEASED', 'EXPIRED', 'FAILED'
    )),
    CONSTRAINT reservations_cpu_positive   CHECK (cpu_millicores > 0),
    CONSTRAINT reservations_mem_positive   CHECK (memory_bytes > 0),
    CONSTRAINT reservations_expires_future CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_reservations_session_id ON reservations(session_id);
CREATE INDEX IF NOT EXISTS idx_reservations_node_id    ON reservations(node_id);
CREATE INDEX IF NOT EXISTS idx_reservations_status     ON reservations(status);
CREATE INDEX IF NOT EXISTS idx_reservations_expires_at ON reservations(expires_at);
