-- ============================================================
-- 009_create_advisories.down.sql
-- Drop Advisories
-- ============================================================

DROP INDEX IF EXISTS idx_advisories_risk;
DROP INDEX IF EXISTS idx_advisories_type;
DROP INDEX IF EXISTS idx_advisories_status;
DROP TABLE IF EXISTS advisories;
