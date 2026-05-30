-- ============================================================
-- 010_create_audit_log.down.sql
-- Drop Audit Log
-- ============================================================

DROP INDEX IF EXISTS idx_audit_resource;
DROP INDEX IF EXISTS idx_audit_actor;
DROP INDEX IF EXISTS idx_audit_event;
DROP INDEX IF EXISTS idx_audit_time;

DROP TABLE IF EXISTS audit_log_2026_06;
DROP TABLE IF EXISTS audit_log_2026_07;
DROP TABLE IF EXISTS audit_log_2026_08;
DROP TABLE IF EXISTS audit_log_2026_09;
DROP TABLE IF EXISTS audit_log_2026_10;
DROP TABLE IF EXISTS audit_log_2026_11;
DROP TABLE IF EXISTS audit_log_2026_12;
DROP TABLE IF EXISTS audit_log;
