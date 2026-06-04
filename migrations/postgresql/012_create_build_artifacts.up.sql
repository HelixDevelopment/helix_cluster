-- ============================================================
-- 012_create_build_artifacts.up.sql
-- Helix Cluster OS — Build Artifacts
-- ============================================================

CREATE TABLE IF NOT EXISTS build_artifacts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL REFERENCES build_jobs(id) ON DELETE CASCADE,
    artifact_hash   TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    storage_path    TEXT NOT NULL,
    artifact_type   VARCHAR(30) NOT NULL DEFAULT 'BINARY',
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT ba_size_positive    CHECK (size_bytes > 0),
    CONSTRAINT ba_type_valid       CHECK (artifact_type IN (
        'BINARY', 'CONTAINER_IMAGE', 'WASM', 'LIBRARY', 'CONFIG', 'REPORT'
    ))
);

CREATE INDEX IF NOT EXISTS idx_build_artifacts_job_id       ON build_artifacts(job_id);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_hash         ON build_artifacts(artifact_hash);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_created_at   ON build_artifacts(created_at);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_type         ON build_artifacts(artifact_type);
