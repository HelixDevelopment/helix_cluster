-- ============================================================
-- 013_create_users.down.sql
-- Drop Users
-- ============================================================

DROP INDEX IF EXISTS idx_users_spiffe;
DROP TABLE IF EXISTS users;
