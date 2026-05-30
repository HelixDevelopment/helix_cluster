-- ============================================================
-- 015_triggers_and_functions.down.sql
-- Drop Triggers and Functions
-- ============================================================

DROP TRIGGER IF EXISTS users_audit ON users;
DROP TRIGGER IF EXISTS build_jobs_audit ON build_jobs;
DROP TRIGGER IF EXISTS sessions_audit ON sessions;
DROP TRIGGER IF EXISTS nodes_audit ON nodes;

DROP TRIGGER IF EXISTS users_updated_at ON users;
DROP TRIGGER IF EXISTS build_jobs_updated_at ON build_jobs;
DROP TRIGGER IF EXISTS scheduling_queue_updated_at ON scheduling_queue;
DROP TRIGGER IF EXISTS resource_allocations_updated_at ON resource_allocations;
DROP TRIGGER IF EXISTS sessions_updated_at ON sessions;
DROP TRIGGER IF EXISTS gpu_devices_updated_at ON gpu_devices;
DROP TRIGGER IF EXISTS nodes_updated_at ON nodes;

DROP FUNCTION IF EXISTS audit_trigger();
DROP FUNCTION IF EXISTS update_updated_at_column();
