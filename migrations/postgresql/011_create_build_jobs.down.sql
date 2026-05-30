-- ============================================================
-- 011_create_build_jobs.down.sql
-- Drop Build Jobs
-- ============================================================

DROP INDEX IF EXISTS idx_build_jobs_created;
DROP INDEX IF EXISTS idx_build_jobs_mode;
DROP INDEX IF EXISTS idx_build_jobs_node;
DROP INDEX IF EXISTS idx_build_jobs_status;
DROP TABLE IF EXISTS build_jobs;
