-- ============================================================
-- 014_create_migration_history.down.sql
-- Drop Session Migration History
-- ============================================================

DROP TABLE IF EXISTS migration_history CASCADE;
