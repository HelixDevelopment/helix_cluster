-- ============================================================
-- 001_create_nodes.down.sql
-- Drop Node Registry
-- ============================================================

DROP INDEX IF EXISTS idx_nodes_labels;
DROP INDEX IF EXISTS idx_nodes_region;
DROP INDEX IF EXISTS idx_nodes_role;
DROP INDEX IF EXISTS idx_nodes_status;
DROP TABLE IF EXISTS nodes;
