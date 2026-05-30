-- ============================================================
-- 011_create_build_jobs.up.sql
-- Helix Cluster OS — Build Jobs
-- ============================================================

CREATE TABLE build_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    mode            VARCHAR(20) NOT NULL DEFAULT 'BATCH',
    cpu_request     INT NOT NULL DEFAULT 1000,
    memory_request  BIGINT NOT NULL DEFAULT 1073741824,
    gpu_request     JSONB DEFAULT NULL,
    node_id         UUID REFERENCES nodes(id),
    priority        INT NOT NULL DEFAULT 50,
    labels          JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_build_jobs_status ON build_jobs(status);
CREATE INDEX idx_build_jobs_node ON build_jobs(node_id);
CREATE INDEX idx_build_jobs_mode ON build_jobs(mode);
CREATE INDEX idx_build_jobs_created ON build_jobs(created_at);
