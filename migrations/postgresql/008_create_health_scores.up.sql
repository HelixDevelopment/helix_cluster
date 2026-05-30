-- ============================================================
-- 008_create_health_scores.up.sql
-- Helix Cluster OS — Health Scores (Snapshots)
-- ============================================================

CREATE TABLE health_scores (
    id                  BIGSERIAL PRIMARY KEY,
    node_id             UUID NOT NULL REFERENCES nodes(id),
    overall_score       INT NOT NULL,
    cpu_score           INT NOT NULL,
    memory_score        INT NOT NULL,
    disk_score          INT NOT NULL,
    network_score       INT NOT NULL,
    gpu_score           INT NOT NULL,
    temperature_score   INT NOT NULL,
    services_score      INT NOT NULL,
    predictions         JSONB DEFAULT '[]',
    metrics             JSONB NOT NULL DEFAULT '{}',
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_health_node ON health_scores(node_id);
CREATE INDEX idx_health_time ON health_scores(recorded_at);
CREATE INDEX idx_health_score ON health_scores(overall_score);
