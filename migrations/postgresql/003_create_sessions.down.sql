-- ============================================================
-- 003_create_sessions.down.sql
-- Drop Sessions
-- ============================================================

DROP INDEX IF EXISTS idx_sessions_mode;
DROP INDEX IF EXISTS idx_sessions_node;
DROP INDEX IF EXISTS idx_sessions_status;
DROP INDEX IF EXISTS idx_sessions_owner;
DROP TABLE IF EXISTS sessions;
