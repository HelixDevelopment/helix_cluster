-- ============================================================
-- 012_create_build_artifacts.down.sql
-- Drop Build Artifacts
-- ============================================================

DROP INDEX IF EXISTS idx_artifacts_created;
DROP INDEX IF EXISTS idx_artifacts_hash;
DROP INDEX IF EXISTS idx_artifacts_job;
DROP TABLE IF EXISTS build_artifacts;
