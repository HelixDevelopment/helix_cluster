-- ============================================================
-- 015_triggers_and_functions.up.sql
-- Helix Cluster OS — Network Policies, Cluster Config, Triggers and Functions
-- ============================================================

CREATE TABLE IF NOT EXISTS network_policies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    direction       VARCHAR(10) NOT NULL,
    action          VARCHAR(10) NOT NULL,
    protocol        VARCHAR(10),
    src_cidr        CIDR,
    dst_cidr        CIDR,
    src_port_min    INT,
    src_port_max    INT,
    dst_port_min    INT,
    dst_port_max    INT,
    priority        INT NOT NULL DEFAULT 100,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    labels          JSONB NOT NULL DEFAULT '{}',
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT np_name_unique        UNIQUE (name),
    CONSTRAINT np_direction_valid    CHECK (direction IN ('INGRESS', 'EGRESS', 'BOTH')),
    CONSTRAINT np_action_valid       CHECK (action IN ('ALLOW', 'DENY', 'LOG', 'RATE_LIMIT')),
    CONSTRAINT np_protocol_valid     CHECK (protocol IS NULL OR protocol IN (
        'TCP', 'UDP', 'ICMP', 'ESP', 'AH', 'ANY'
    )),
    CONSTRAINT np_priority_range     CHECK (priority >= 0 AND priority <= 65535),
    CONSTRAINT np_src_port_range     CHECK (
        src_port_min IS NULL OR (src_port_min >= 0 AND src_port_min <= 65535)
    ),
    CONSTRAINT np_dst_port_range     CHECK (
        dst_port_min IS NULL OR (dst_port_min >= 0 AND dst_port_min <= 65535)
    ),
    CONSTRAINT np_src_port_order     CHECK (
        src_port_min IS NULL OR src_port_max IS NULL OR src_port_min <= src_port_max
    ),
    CONSTRAINT np_dst_port_order     CHECK (
        dst_port_min IS NULL OR dst_port_max IS NULL OR dst_port_min <= dst_port_max
    )
);

CREATE INDEX IF NOT EXISTS idx_network_policies_direction ON network_policies(direction);
CREATE INDEX IF NOT EXISTS idx_network_policies_action    ON network_policies(action);
CREATE INDEX IF NOT EXISTS idx_network_policies_enabled   ON network_policies(enabled);
CREATE INDEX IF NOT EXISTS idx_network_policies_priority  ON network_policies(priority);

CREATE TABLE IF NOT EXISTS cluster_config (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key             VARCHAR(255) NOT NULL,
    value           JSONB NOT NULL,
    description     TEXT,
    category        VARCHAR(50) NOT NULL DEFAULT 'general',
    readonly        BOOLEAN NOT NULL DEFAULT FALSE,
    version         INT NOT NULL DEFAULT 1,
    updated_by      TEXT NOT NULL DEFAULT 'system',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT cc_key_unique         UNIQUE (key),
    CONSTRAINT cc_version_positive   CHECK (version > 0),
    CONSTRAINT cc_category_valid     CHECK (category IN (
        'general', 'scheduler', 'network', 'security',
        'storage', 'monitoring', 'llm', 'build'
    ))
);

CREATE INDEX IF NOT EXISTS idx_cluster_config_key      ON cluster_config(key);
CREATE INDEX IF NOT EXISTS idx_cluster_config_category ON cluster_config(category);

-- Auto-bump updated_at on every UPDATE
CREATE OR REPLACE FUNCTION helix_update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Immutable audit trigger: writes a row to audit_log on tracked mutations.
-- Security note: this function is SECURITY DEFINER to ensure it always
-- runs with sufficient privilege to INSERT into audit_log.
CREATE OR REPLACE FUNCTION helix_audit_trigger()
RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'DELETE') THEN
        INSERT INTO audit_log (
            event_type, severity, actor,
            resource_type, resource_id, action, details
        ) VALUES (
            TG_OP || '_' || TG_TABLE_NAME,
            'WARNING',
            COALESCE(current_setting('app.current_user', TRUE), 'system'),
            TG_TABLE_NAME,
            OLD.id::TEXT,
            TG_OP,
            to_jsonb(OLD)
        );
        RETURN OLD;
    ELSE
        INSERT INTO audit_log (
            event_type, severity, actor,
            resource_type, resource_id, action, details
        ) VALUES (
            TG_OP || '_' || TG_TABLE_NAME,
            'INFO',
            COALESCE(current_setting('app.current_user', TRUE), 'system'),
            TG_TABLE_NAME,
            NEW.id::TEXT,
            TG_OP,
            to_jsonb(NEW)
        );
        RETURN NEW;
    END IF;
END;
$$ LANGUAGE plpgsql;

-- updated_at triggers
CREATE OR REPLACE TRIGGER helix_nodes_updated_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_gpu_devices_updated_at
    BEFORE UPDATE ON gpu_devices
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_sessions_updated_at
    BEFORE UPDATE ON sessions
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_reservations_updated_at
    BEFORE UPDATE ON reservations
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_scheduling_queue_updated_at
    BEFORE UPDATE ON scheduling_queue
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_llm_advisories_updated_at
    BEFORE UPDATE ON llm_advisories
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_build_jobs_updated_at
    BEFORE UPDATE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_network_policies_updated_at
    BEFORE UPDATE ON network_policies
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE OR REPLACE TRIGGER helix_cluster_config_updated_at
    BEFORE UPDATE ON cluster_config
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();

-- audit triggers (AFTER, so committed state is captured)
CREATE OR REPLACE TRIGGER helix_nodes_audit
    AFTER INSERT OR UPDATE OR DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE OR REPLACE TRIGGER helix_sessions_audit
    AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE OR REPLACE TRIGGER helix_build_jobs_audit
    AFTER INSERT OR UPDATE OR DELETE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE OR REPLACE TRIGGER helix_users_audit
    AFTER INSERT OR UPDATE OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();

-- Seed default cluster config entries
INSERT INTO cluster_config (key, value, description, category) VALUES
    ('scheduler.max_queue_depth',   '1000',     'Maximum scheduler queue depth',        'scheduler'),
    ('scheduler.default_priority',  '50',        'Default session scheduling priority',  'scheduler'),
    ('network.mtu',                 '1420',      'WireGuard MTU (bytes)',                'network'),
    ('security.session_ttl_hours',  '8',         'Default session TTL in hours',         'security'),
    ('monitoring.health_interval_s','30',        'Health snapshot interval (seconds)',   'monitoring')
ON CONFLICT (key) DO NOTHING;
