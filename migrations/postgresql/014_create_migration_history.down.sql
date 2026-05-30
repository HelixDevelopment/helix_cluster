-- ============================================================
-- 014_create_migration_history.down.sql
-- Drop Migration History
-- ============================================================

DROP INDEX IF EXISTS idx_migrations_target;
DROP INDEX IF EXISTS idx_migrations_source;
DROP INDEX IF EXISTS idx_migrations_session;
DROP TABLE IF EXISTS migration_history;
