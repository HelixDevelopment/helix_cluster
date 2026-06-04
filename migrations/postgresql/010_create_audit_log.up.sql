-- ============================================================
-- 010_create_audit_log.up.sql
-- Helix Cluster OS — Audit Log (Partitioned)
-- ============================================================

CREATE TABLE IF NOT EXISTS audit_log (
    id            BIGSERIAL,
    timestamp     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type    VARCHAR(50) NOT NULL,
    severity      VARCHAR(10) NOT NULL DEFAULT 'INFO',
    actor         TEXT NOT NULL,
    resource_type VARCHAR(50) NOT NULL,
    resource_id   TEXT,
    action        VARCHAR(50) NOT NULL,
    details       JSONB NOT NULL DEFAULT '{}',
    source_ip     INET,
    session_id    UUID,
    PRIMARY KEY (id, timestamp),

    CONSTRAINT audit_log_severity_valid CHECK (severity IN (
        'DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'
    ))
) PARTITION BY RANGE (timestamp);

-- Default partition catches anything not covered by month partitions
CREATE TABLE IF NOT EXISTS audit_log_default
    PARTITION OF audit_log DEFAULT;

-- Monthly partitions for 2026
CREATE TABLE IF NOT EXISTS audit_log_2026_06
    PARTITION OF audit_log FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_07
    PARTITION OF audit_log FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_08
    PARTITION OF audit_log FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_09
    PARTITION OF audit_log FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_10
    PARTITION OF audit_log FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_11
    PARTITION OF audit_log FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE IF NOT EXISTS audit_log_2026_12
    PARTITION OF audit_log FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');

-- Monthly partitions for 2027
CREATE TABLE IF NOT EXISTS audit_log_2027_01
    PARTITION OF audit_log FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE IF NOT EXISTS audit_log_2027_02
    PARTITION OF audit_log FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
CREATE TABLE IF NOT EXISTS audit_log_2027_03
    PARTITION OF audit_log FOR VALUES FROM ('2027-03-01') TO ('2027-04-01');
CREATE TABLE IF NOT EXISTS audit_log_2027_04
    PARTITION OF audit_log FOR VALUES FROM ('2027-04-01') TO ('2027-05-01');
CREATE TABLE IF NOT EXISTS audit_log_2027_05
    PARTITION OF audit_log FOR VALUES FROM ('2027-05-01') TO ('2027-06-01');
CREATE TABLE IF NOT EXISTS audit_log_2027_06
    PARTITION OF audit_log FOR VALUES FROM ('2027-06-01') TO ('2027-07-01');

CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp     ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_event_type    ON audit_log(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor         ON audit_log(actor);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource      ON audit_log(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_severity      ON audit_log(severity);
CREATE INDEX IF NOT EXISTS idx_audit_log_session_id    ON audit_log(session_id);
