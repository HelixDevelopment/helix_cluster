-- ============================================================
-- 006_create_resource_allocations.down.sql
-- Drop Resource Allocations
-- ============================================================

DROP INDEX IF EXISTS idx_allocations_expires;
DROP INDEX IF EXISTS idx_allocations_status;
DROP INDEX IF EXISTS idx_allocations_node;
DROP INDEX IF EXISTS idx_allocations_session;
DROP TABLE IF EXISTS resource_allocations;
