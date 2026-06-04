-- ============================================================
-- 011_create_build_jobs.up.sql
-- Helix Cluster OS — Build Jobs
-- ============================================================

CREATE TABLE IF NOT EXISTS build_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    mode            VARCHAR(20) NOT NULL DEFAULT 'BATCH',
    cpu_request     INT NOT NULL DEFAULT 1000,
    memory_request  BIGINT NOT NULL DEFAULT 1073741824,
    gpu_request     JSONB DEFAULT NULL,
    node_id         UUID REFERENCES nodes(id),
    priority        INT NOT NULL DEFAULT 50,
    labels          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT bj_status_valid    CHECK (status IN (
        'PENDING', 'QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED'
    )),
    CONSTRAINT bj_mode_valid      CHECK (mode IN (
        'BATCH', 'STREAMING', 'INCREMENTAL', 'PARALLEL'
    )),
    CONSTRAINT bj_priority_range  CHECK (priority >= 0 AND priority <= 100),
    CONSTRAINT bj_cpu_positive    CHECK (cpu_request > 0),
    CONSTRAINT bj_mem_positive    CHECK (memory_request > 0)
);

CREATE INDEX IF NOT EXISTS idx_build_jobs_status     ON build_jobs(status);
CREATE INDEX IF NOT EXISTS idx_build_jobs_node_id    ON build_jobs(node_id);
CREATE INDEX IF NOT EXISTS idx_build_jobs_mode       ON build_jobs(mode);
CREATE INDEX IF NOT EXISTS idx_build_jobs_created_at ON build_jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_build_jobs_priority   ON build_jobs(priority);
