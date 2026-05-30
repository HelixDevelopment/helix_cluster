-- ============================================================
-- 005_create_session_panes.down.sql
-- Drop Session Panes
-- ============================================================

DROP INDEX IF EXISTS idx_panes_status;
DROP INDEX IF EXISTS idx_panes_gpu;
DROP INDEX IF EXISTS idx_panes_node;
DROP INDEX IF EXISTS idx_panes_window;
DROP TABLE IF EXISTS session_panes;
