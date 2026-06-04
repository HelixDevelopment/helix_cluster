-- ============================================================
-- 008_create_health_snapshots.up.sql
-- Helix Cluster OS — Health Snapshots
-- ============================================================

CREATE TABLE IF NOT EXISTS health_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    node_id             UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    overall_score       INT NOT NULL,
    cpu_score           INT NOT NULL,
    memory_score        INT NOT NULL,
    disk_score          INT NOT NULL,
    network_score       INT NOT NULL,
    gpu_score           INT NOT NULL,
    temperature_score   INT NOT NULL,
    services_score      INT NOT NULL,
    predictions         JSONB NOT NULL DEFAULT '[]',
    metrics             JSONB NOT NULL DEFAULT '{}',
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT hs_overall_range     CHECK (overall_score BETWEEN 0 AND 100),
    CONSTRAINT hs_cpu_range         CHECK (cpu_score BETWEEN 0 AND 100),
    CONSTRAINT hs_memory_range      CHECK (memory_score BETWEEN 0 AND 100),
    CONSTRAINT hs_disk_range        CHECK (disk_score BETWEEN 0 AND 100),
    CONSTRAINT hs_network_range     CHECK (network_score BETWEEN 0 AND 100),
    CONSTRAINT hs_gpu_range         CHECK (gpu_score BETWEEN 0 AND 100),
    CONSTRAINT hs_temperature_range CHECK (temperature_score BETWEEN 0 AND 100),
    CONSTRAINT hs_services_range    CHECK (services_score BETWEEN 0 AND 100)
);

CREATE INDEX IF NOT EXISTS idx_health_snapshots_node_id     ON health_snapshots(node_id);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_recorded_at ON health_snapshots(recorded_at);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_overall     ON health_snapshots(overall_score);
