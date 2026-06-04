-- ============================================================
-- 015_triggers_and_functions.down.sql
-- Drop Network Policies, Cluster Config, Triggers and Functions
-- ============================================================

DROP FUNCTION IF EXISTS helix_audit_trigger() CASCADE;
DROP FUNCTION IF EXISTS helix_update_updated_at_column() CASCADE;
DROP TABLE IF EXISTS cluster_config CASCADE;
DROP TABLE IF EXISTS network_policies CASCADE;
