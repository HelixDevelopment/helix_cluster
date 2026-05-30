-- ============================================================
-- 004_create_session_windows.down.sql
-- Drop Session Windows
-- ============================================================

DROP INDEX IF EXISTS idx_windows_active;
DROP INDEX IF EXISTS idx_windows_session;
DROP TABLE IF EXISTS session_windows;
