-- ============================================================
-- 010_create_audit_log.down.sql
-- Drop Audit Log (Partitioned)
-- ============================================================

DROP TABLE IF EXISTS audit_log_default CASCADE;
DROP TABLE IF EXISTS audit_log_2026_06 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_07 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_08 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_09 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_10 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_11 CASCADE;
DROP TABLE IF EXISTS audit_log_2026_12 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_01 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_02 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_03 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_04 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_05 CASCADE;
DROP TABLE IF EXISTS audit_log_2027_06 CASCADE;
DROP TABLE IF EXISTS audit_log CASCADE;
