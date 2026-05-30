-- ============================================================
-- 012_create_build_artifacts.up.sql
-- Helix Cluster OS — Build Artifacts
-- ============================================================

CREATE TABLE build_artifacts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL REFERENCES build_jobs(id) ON DELETE CASCADE,
    artifact_hash   TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    storage_path    TEXT NOT NULL,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_artifacts_job ON build_artifacts(job_id);
CREATE INDEX idx_artifacts_hash ON build_artifacts(artifact_hash);
CREATE INDEX idx_artifacts_created ON build_artifacts(created_at);
