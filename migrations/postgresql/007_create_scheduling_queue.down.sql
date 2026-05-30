-- ============================================================
-- 007_create_scheduling_queue.down.sql
-- Drop Scheduling Queue
-- ============================================================

DROP INDEX IF EXISTS idx_scheduling_created;
DROP INDEX IF EXISTS idx_scheduling_priority;
DROP INDEX IF EXISTS idx_scheduling_status;
DROP TABLE IF EXISTS scheduling_queue;
