-- ============================================================
-- 001_create_nodes.up.sql
-- Helix Cluster OS — Node Registry
-- ============================================================

CREATE TABLE IF NOT EXISTS nodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        VARCHAR(255) NOT NULL,
    ip_addresses    INET[] NOT NULL,
    wg_pubkey       TEXT NOT NULL,
    spiffe_id       TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'JOINING',
    role            VARCHAR(20) NOT NULL DEFAULT 'WORKER',
    cpu_arch        VARCHAR(20) NOT NULL,
    cpu_cores       INT NOT NULL,
    cpu_threads     INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_count       INT NOT NULL DEFAULT 0,
    storage_bytes   BIGINT NOT NULL,
    labels          JSONB NOT NULL DEFAULT '{}',
    region          VARCHAR(100),
    version         VARCHAR(50) NOT NULL,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT nodes_wg_pubkey_unique    UNIQUE (wg_pubkey),
    CONSTRAINT nodes_spiffe_id_unique    UNIQUE (spiffe_id),
    CONSTRAINT nodes_cpu_cores_positive  CHECK (cpu_cores > 0),
    CONSTRAINT nodes_cpu_threads_ge_cores CHECK (cpu_threads >= cpu_cores),
    CONSTRAINT nodes_memory_positive     CHECK (memory_bytes > 0),
    CONSTRAINT nodes_gpu_count_nonneg    CHECK (gpu_count >= 0),
    CONSTRAINT nodes_storage_positive    CHECK (storage_bytes > 0),
    CONSTRAINT nodes_status_valid        CHECK (status IN (
        'JOINING', 'READY', 'DRAINING', 'OFFLINE', 'MAINTENANCE', 'EVICTED'
    )),
    CONSTRAINT nodes_role_valid          CHECK (role IN (
        'WORKER', 'CONTROL_PLANE', 'GPU_WORKER', 'EDGE', 'OBSERVER'
    ))
);

CREATE INDEX IF NOT EXISTS idx_nodes_status    ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_role      ON nodes(role);
CREATE INDEX IF NOT EXISTS idx_nodes_region    ON nodes(region);
CREATE INDEX IF NOT EXISTS idx_nodes_labels    ON nodes USING GIN(labels);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
CREATE INDEX IF NOT EXISTS idx_nodes_hostname  ON nodes(hostname);
