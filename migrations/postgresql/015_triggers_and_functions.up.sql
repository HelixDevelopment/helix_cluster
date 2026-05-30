-- ============================================================
-- 015_triggers_and_functions.up.sql
-- Helix Cluster OS — Triggers and Functions
-- ============================================================

-- Auto-update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Apply to all tables with updated_at
CREATE TRIGGER nodes_updated_at BEFORE UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER gpu_devices_updated_at BEFORE UPDATE ON gpu_devices
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER sessions_updated_at BEFORE UPDATE ON sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER resource_allocations_updated_at BEFORE UPDATE ON resource_allocations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER scheduling_queue_updated_at BEFORE UPDATE ON scheduling_queue
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER build_jobs_updated_at BEFORE UPDATE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Audit log trigger (INSERT and UPDATE only; DELETE handled separately)
CREATE OR REPLACE FUNCTION audit_trigger()
RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'DELETE') THEN
        INSERT INTO audit_log (event_type, severity, actor, resource_type, resource_id, action, details)
        VALUES (
            TG_OP || '_' || TG_TABLE_NAME,
            'WARNING',
            COALESCE(current_setting('app.current_user', true), 'system'),
            TG_TABLE_NAME,
            OLD.id::text,
            TG_OP,
            to_jsonb(OLD)
        );
        RETURN OLD;
    ELSE
        INSERT INTO audit_log (event_type, severity, actor, resource_type, resource_id, action, details)
        VALUES (
            TG_OP || '_' || TG_TABLE_NAME,
            CASE WHEN TG_OP = 'DELETE' THEN 'WARNING' ELSE 'INFO' END,
            COALESCE(current_setting('app.current_user', true), 'system'),
            TG_TABLE_NAME,
            NEW.id::text,
            TG_OP,
            to_jsonb(NEW)
        );
        RETURN NEW;
    END IF;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER nodes_audit AFTER INSERT OR UPDATE OR DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION audit_trigger();
CREATE TRIGGER sessions_audit AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION audit_trigger();
CREATE TRIGGER build_jobs_audit AFTER INSERT OR UPDATE OR DELETE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION audit_trigger();
CREATE TRIGGER users_audit AFTER INSERT OR UPDATE OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION audit_trigger();
