-- ============================================================
-- 006_create_resource_allocations.up.sql
-- Helix Cluster OS — Resource Allocations (Reservations)
-- ============================================================

CREATE TABLE resource_allocations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id),
    node_id         UUID NOT NULL REFERENCES nodes(id),
    cpu_millicores  INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_ids         UUID[],
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_allocations_session ON resource_allocations(session_id);
CREATE INDEX idx_allocations_node ON resource_allocations(node_id);
CREATE INDEX idx_allocations_status ON resource_allocations(status);
CREATE INDEX idx_allocations_expires ON resource_allocations(expires_at);
