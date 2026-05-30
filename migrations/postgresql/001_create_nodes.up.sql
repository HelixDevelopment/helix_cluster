-- ============================================================
-- 001_create_nodes.up.sql
-- Helix Cluster OS — Node Registry
-- ============================================================

CREATE TABLE nodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        VARCHAR(255) NOT NULL,
    ip_addresses    INET[] NOT NULL,
    wg_pubkey       TEXT NOT NULL UNIQUE,
    spiffe_id       TEXT NOT NULL UNIQUE,
    status          VARCHAR(20) NOT NULL DEFAULT 'JOINING',
    role            VARCHAR(20) NOT NULL DEFAULT 'WORKER',
    cpu_arch        VARCHAR(20) NOT NULL,
    cpu_cores       INT NOT NULL,
    cpu_threads     INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_count       INT NOT NULL DEFAULT 0,
    storage_bytes   BIGINT NOT NULL,
    labels          JSONB DEFAULT '{}',
    region          VARCHAR(100),
    version         VARCHAR(50) NOT NULL,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_nodes_status ON nodes(status);
CREATE INDEX idx_nodes_role ON nodes(role);
CREATE INDEX idx_nodes_region ON nodes(region);
CREATE INDEX idx_nodes_labels ON nodes USING GIN(labels);
