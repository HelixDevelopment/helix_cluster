-- ============================================================
-- 013_create_users.down.sql
-- Drop Users (shadow of OIDC provider)
-- ============================================================

DROP TABLE IF EXISTS users CASCADE;
