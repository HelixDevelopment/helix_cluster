#!/usr/bin/env python3
"""
.gen_schema.py — single-source generator for the Helix Cluster OS Postgres schema.

HXC-1639 reconciliation: the golang-migrate chain (001..015) is the ONE canonical
source. The consolidated 0001_primary_schema.sql is a DERIVED artifact: it is the
in-order concatenation of the chain bodies. This generator emits BOTH from the same
in-memory bodies so they can never diverge. The drift-guard test
(internal/schema/drift_guard_test.go) re-asserts the equivalence on every `go test`.

Run:  python3 migrations/postgresql/.gen_schema.py
"""
import os

HERE = os.path.dirname(os.path.abspath(__file__))

# Each entry: (version, slug, header_title, body_sql)
# body_sql is the canonical, rich DDL for that migration. The concatenation of all
# bodies (in version order) IS 0001_primary_schema.sql. Every up-migration file is
# its per-file header comment + the same body. Down files drop what each created.

NODES = """\
CREATE TABLE IF NOT EXISTS nodes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname        VARCHAR(255) NOT NULL,
    ip_addresses    INET[] NOT NULL,
    wg_pubkey       TEXT NOT NULL,
    spiffe_id       TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'JOINING',
    role            VARCHAR(20) NOT NULL DEFAULT 'WORKER',
    cpu_arch        VARCHAR(20) NOT NULL,
    cpu_cores       INT NOT NULL,
    cpu_threads     INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_count       INT NOT NULL DEFAULT 0,
    storage_bytes   BIGINT NOT NULL,
    labels          JSONB NOT NULL DEFAULT '{}',
    region          VARCHAR(100),
    version         VARCHAR(50) NOT NULL,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    left_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT nodes_wg_pubkey_unique    UNIQUE (wg_pubkey),
    CONSTRAINT nodes_spiffe_id_unique    UNIQUE (spiffe_id),
    CONSTRAINT nodes_cpu_cores_positive  CHECK (cpu_cores > 0),
    CONSTRAINT nodes_cpu_threads_ge_cores CHECK (cpu_threads >= cpu_cores),
    CONSTRAINT nodes_memory_positive     CHECK (memory_bytes > 0),
    CONSTRAINT nodes_gpu_count_nonneg    CHECK (gpu_count >= 0),
    CONSTRAINT nodes_storage_positive    CHECK (storage_bytes > 0),
    CONSTRAINT nodes_status_valid        CHECK (status IN (
        'JOINING', 'READY', 'DRAINING', 'OFFLINE', 'MAINTENANCE', 'EVICTED'
    )),
    CONSTRAINT nodes_role_valid          CHECK (role IN (
        'WORKER', 'CONTROL_PLANE', 'GPU_WORKER', 'EDGE', 'OBSERVER'
    ))
);

CREATE INDEX IF NOT EXISTS idx_nodes_status    ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_role      ON nodes(role);
CREATE INDEX IF NOT EXISTS idx_nodes_region    ON nodes(region);
CREATE INDEX IF NOT EXISTS idx_nodes_labels    ON nodes USING GIN(labels);
CREATE INDEX IF NOT EXISTS idx_nodes_last_seen ON nodes(last_seen);
CREATE INDEX IF NOT EXISTS idx_nodes_hostname  ON nodes(hostname);
"""

GPU = """\
CREATE TABLE IF NOT EXISTS gpu_devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    vendor          VARCHAR(20) NOT NULL,
    model           VARCHAR(100) NOT NULL,
    driver_version  VARCHAR(50) NOT NULL,
    api             VARCHAR(20) NOT NULL,
    api_version     VARCHAR(20) NOT NULL,
    total_memory    BIGINT NOT NULL,
    compute_units   INT NOT NULL,
    features        TEXT[] NOT NULL DEFAULT '{}',
    attributes      JSONB NOT NULL DEFAULT '{}',
    status          VARCHAR(20) NOT NULL DEFAULT 'AVAILABLE',
    allocated_to    UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT gpu_vendor_valid  CHECK (vendor IN ('NVIDIA', 'AMD', 'INTEL', 'APPLE', 'QUALCOMM', 'OTHER')),
    CONSTRAINT gpu_api_valid     CHECK (api IN ('CUDA', 'ROCm', 'Metal', 'Vulkan', 'OpenCL', 'OTHER')),
    CONSTRAINT gpu_status_valid  CHECK (status IN ('AVAILABLE', 'IN_USE', 'RESERVED', 'OFFLINE', 'FAULT')),
    CONSTRAINT gpu_mem_positive  CHECK (total_memory > 0),
    CONSTRAINT gpu_cu_positive   CHECK (compute_units > 0)
);

CREATE INDEX IF NOT EXISTS idx_gpu_devices_node_id  ON gpu_devices(node_id);
CREATE INDEX IF NOT EXISTS idx_gpu_devices_status   ON gpu_devices(status);
CREATE INDEX IF NOT EXISTS idx_gpu_devices_vendor   ON gpu_devices(vendor);
CREATE INDEX IF NOT EXISTS idx_gpu_devices_model    ON gpu_devices(model);
"""

SESSIONS = """\
CREATE TABLE IF NOT EXISTS sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    owner           TEXT NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'CREATING',
    mode            VARCHAR(20) NOT NULL DEFAULT 'INTERACTIVE',
    backend         VARCHAR(20) NOT NULL DEFAULT 'TMUX',
    backend_id      TEXT,
    node_id         UUID REFERENCES nodes(id),
    cpu_request     INT NOT NULL DEFAULT 1000,
    memory_request  BIGINT NOT NULL DEFAULT 1073741824,
    gpu_request     JSONB DEFAULT NULL,
    priority        INT NOT NULL DEFAULT 50,
    labels          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    terminated_at   TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sessions_status_valid   CHECK (status IN (
        'CREATING', 'RUNNING', 'PAUSED', 'TERMINATING', 'TERMINATED', 'FAILED'
    )),
    CONSTRAINT sessions_mode_valid     CHECK (mode IN (
        'INTERACTIVE', 'BATCH', 'DAEMON', 'NOTEBOOK'
    )),
    CONSTRAINT sessions_backend_valid  CHECK (backend IN (
        'TMUX', 'WASM', 'CONTAINER', 'NATIVE'
    )),
    CONSTRAINT sessions_priority_range CHECK (priority >= 0 AND priority <= 100),
    CONSTRAINT sessions_cpu_positive   CHECK (cpu_request > 0),
    CONSTRAINT sessions_mem_positive   CHECK (memory_request > 0)
);

CREATE INDEX IF NOT EXISTS idx_sessions_owner    ON sessions(owner);
CREATE INDEX IF NOT EXISTS idx_sessions_status   ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_node_id  ON sessions(node_id);
CREATE INDEX IF NOT EXISTS idx_sessions_mode     ON sessions(mode);
CREATE INDEX IF NOT EXISTS idx_sessions_priority ON sessions(priority);
CREATE INDEX IF NOT EXISTS idx_sessions_labels   ON sessions USING GIN(labels);
"""

WINDOWS = """\
CREATE TABLE IF NOT EXISTS session_windows (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name        VARCHAR(255) NOT NULL,
    layout      VARCHAR(50) NOT NULL DEFAULT 'tiled',
    active      BOOLEAN NOT NULL DEFAULT FALSE,
    crdt_state  JSONB DEFAULT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sw_layout_valid CHECK (layout IN (
        'tiled', 'even-horizontal', 'even-vertical', 'main-horizontal', 'main-vertical'
    ))
);

CREATE INDEX IF NOT EXISTS idx_session_windows_session_id ON session_windows(session_id);
CREATE INDEX IF NOT EXISTS idx_session_windows_active     ON session_windows(active);
"""

PANES = """\
CREATE TABLE IF NOT EXISTS session_panes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    window_id   UUID NOT NULL REFERENCES session_windows(id) ON DELETE CASCADE,
    node_id     UUID REFERENCES nodes(id),
    command     TEXT,
    working_dir TEXT,
    environment JSONB NOT NULL DEFAULT '{}',
    cpu_limit   INT,
    memory_limit BIGINT,
    gpu_id      UUID REFERENCES gpu_devices(id),
    status      VARCHAR(20) NOT NULL DEFAULT 'CREATING',
    crdt_state  JSONB DEFAULT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT sp_status_valid   CHECK (status IN (
        'CREATING', 'RUNNING', 'STOPPED', 'FAILED', 'MIGRATING'
    )),
    CONSTRAINT sp_cpu_limit_pos  CHECK (cpu_limit IS NULL OR cpu_limit > 0),
    CONSTRAINT sp_mem_limit_pos  CHECK (memory_limit IS NULL OR memory_limit > 0)
);

CREATE INDEX IF NOT EXISTS idx_session_panes_window_id ON session_panes(window_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_node_id   ON session_panes(node_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_gpu_id    ON session_panes(gpu_id);
CREATE INDEX IF NOT EXISTS idx_session_panes_status    ON session_panes(status);
"""

RESERVATIONS = """\
CREATE TABLE IF NOT EXISTS reservations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    node_id         UUID NOT NULL REFERENCES nodes(id),
    cpu_millicores  INT NOT NULL,
    memory_bytes    BIGINT NOT NULL,
    gpu_ids         UUID[] NOT NULL DEFAULT '{}',
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT reservations_status_valid   CHECK (status IN (
        'PENDING', 'ACTIVE', 'RELEASED', 'EXPIRED', 'FAILED'
    )),
    CONSTRAINT reservations_cpu_positive   CHECK (cpu_millicores > 0),
    CONSTRAINT reservations_mem_positive   CHECK (memory_bytes > 0),
    CONSTRAINT reservations_expires_future CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS idx_reservations_session_id ON reservations(session_id);
CREATE INDEX IF NOT EXISTS idx_reservations_node_id    ON reservations(node_id);
CREATE INDEX IF NOT EXISTS idx_reservations_status     ON reservations(status);
CREATE INDEX IF NOT EXISTS idx_reservations_expires_at ON reservations(expires_at);
"""

SCHEDULING = """\
CREATE TABLE IF NOT EXISTS scheduling_queue (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request         JSONB NOT NULL,
    priority        INT NOT NULL DEFAULT 50,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    scheduled_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT scheduling_status_valid   CHECK (status IN (
        'PENDING', 'SCHEDULING', 'SCHEDULED', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED'
    )),
    CONSTRAINT scheduling_priority_range CHECK (priority >= 0 AND priority <= 100)
);

CREATE INDEX IF NOT EXISTS idx_scheduling_queue_status   ON scheduling_queue(status);
CREATE INDEX IF NOT EXISTS idx_scheduling_queue_priority ON scheduling_queue(priority);
CREATE INDEX IF NOT EXISTS idx_scheduling_queue_created  ON scheduling_queue(created_at);
"""

HEALTH = """\
CREATE TABLE IF NOT EXISTS health_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    node_id             UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    overall_score       INT NOT NULL,
    cpu_score           INT NOT NULL,
    memory_score        INT NOT NULL,
    disk_score          INT NOT NULL,
    network_score       INT NOT NULL,
    gpu_score           INT NOT NULL,
    temperature_score   INT NOT NULL,
    services_score      INT NOT NULL,
    predictions         JSONB NOT NULL DEFAULT '[]',
    metrics             JSONB NOT NULL DEFAULT '{}',
    recorded_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT hs_overall_range     CHECK (overall_score BETWEEN 0 AND 100),
    CONSTRAINT hs_cpu_range         CHECK (cpu_score BETWEEN 0 AND 100),
    CONSTRAINT hs_memory_range      CHECK (memory_score BETWEEN 0 AND 100),
    CONSTRAINT hs_disk_range        CHECK (disk_score BETWEEN 0 AND 100),
    CONSTRAINT hs_network_range     CHECK (network_score BETWEEN 0 AND 100),
    CONSTRAINT hs_gpu_range         CHECK (gpu_score BETWEEN 0 AND 100),
    CONSTRAINT hs_temperature_range CHECK (temperature_score BETWEEN 0 AND 100),
    CONSTRAINT hs_services_range    CHECK (services_score BETWEEN 0 AND 100)
);

CREATE INDEX IF NOT EXISTS idx_health_snapshots_node_id     ON health_snapshots(node_id);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_recorded_at ON health_snapshots(recorded_at);
CREATE INDEX IF NOT EXISTS idx_health_snapshots_overall     ON health_snapshots(overall_score);
"""

ADVISORIES = """\
CREATE TABLE IF NOT EXISTS llm_advisories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type            VARCHAR(30) NOT NULL,
    description     TEXT NOT NULL,
    rationale       TEXT NOT NULL,
    proposed_action JSONB NOT NULL,
    confidence      FLOAT NOT NULL,
    risk_level      VARCHAR(10) NOT NULL,
    auto_approve    BOOLEAN NOT NULL DEFAULT FALSE,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    applied_by      TEXT,
    applied_at      TIMESTAMPTZ,
    result          JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT la_confidence_range CHECK (confidence >= 0.0 AND confidence <= 1.0),
    CONSTRAINT la_risk_valid        CHECK (risk_level IN (
        'LOW', 'MEDIUM', 'HIGH', 'CRITICAL'
    )),
    CONSTRAINT la_status_valid      CHECK (status IN (
        'PENDING', 'APPROVED', 'REJECTED', 'APPLIED', 'FAILED', 'EXPIRED'
    )),
    CONSTRAINT la_type_valid        CHECK (type IN (
        'SCALE_UP', 'SCALE_DOWN', 'MIGRATE', 'REBALANCE',
        'DRAIN', 'EVICT', 'ALERT', 'OPTIMIZE', 'SCHEDULE'
    ))
);

CREATE INDEX IF NOT EXISTS idx_llm_advisories_status     ON llm_advisories(status);
CREATE INDEX IF NOT EXISTS idx_llm_advisories_type       ON llm_advisories(type);
CREATE INDEX IF NOT EXISTS idx_llm_advisories_risk_level ON llm_advisories(risk_level);
CREATE INDEX IF NOT EXISTS idx_llm_advisories_created_at ON llm_advisories(created_at);
"""

AUDIT = """\
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
"""

BUILD_JOBS = """\
CREATE TABLE IF NOT EXISTS build_jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(255) NOT NULL,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    mode            VARCHAR(20) NOT NULL DEFAULT 'BATCH',
    cpu_request     INT NOT NULL DEFAULT 1000,
    memory_request  BIGINT NOT NULL DEFAULT 1073741824,
    gpu_request     JSONB DEFAULT NULL,
    node_id         UUID REFERENCES nodes(id),
    priority        INT NOT NULL DEFAULT 50,
    labels          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT bj_status_valid    CHECK (status IN (
        'PENDING', 'QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED'
    )),
    CONSTRAINT bj_mode_valid      CHECK (mode IN (
        'BATCH', 'STREAMING', 'INCREMENTAL', 'PARALLEL'
    )),
    CONSTRAINT bj_priority_range  CHECK (priority >= 0 AND priority <= 100),
    CONSTRAINT bj_cpu_positive    CHECK (cpu_request > 0),
    CONSTRAINT bj_mem_positive    CHECK (memory_request > 0)
);

CREATE INDEX IF NOT EXISTS idx_build_jobs_status     ON build_jobs(status);
CREATE INDEX IF NOT EXISTS idx_build_jobs_node_id    ON build_jobs(node_id);
CREATE INDEX IF NOT EXISTS idx_build_jobs_mode       ON build_jobs(mode);
CREATE INDEX IF NOT EXISTS idx_build_jobs_created_at ON build_jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_build_jobs_priority   ON build_jobs(priority);
"""

BUILD_ARTIFACTS = """\
CREATE TABLE IF NOT EXISTS build_artifacts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL REFERENCES build_jobs(id) ON DELETE CASCADE,
    artifact_hash   TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    storage_path    TEXT NOT NULL,
    artifact_type   VARCHAR(30) NOT NULL DEFAULT 'BINARY',
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT ba_size_positive    CHECK (size_bytes > 0),
    CONSTRAINT ba_type_valid       CHECK (artifact_type IN (
        'BINARY', 'CONTAINER_IMAGE', 'WASM', 'LIBRARY', 'CONFIG', 'REPORT'
    ))
);

CREATE INDEX IF NOT EXISTS idx_build_artifacts_job_id       ON build_artifacts(job_id);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_hash         ON build_artifacts(artifact_hash);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_created_at   ON build_artifacts(created_at);
CREATE INDEX IF NOT EXISTS idx_build_artifacts_type         ON build_artifacts(artifact_type);
"""

USERS = """\
CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    spiffe_id       TEXT NOT NULL,
    email           TEXT,
    name            VARCHAR(255),
    role            VARCHAR(20) NOT NULL DEFAULT 'USER',
    quota_cpu       INT NOT NULL DEFAULT 8000,
    quota_memory    BIGINT NOT NULL DEFAULT 17179869184,
    quota_gpu       INT NOT NULL DEFAULT 0,
    labels          JSONB NOT NULL DEFAULT '{}',
    last_login      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT users_spiffe_id_unique  UNIQUE (spiffe_id),
    CONSTRAINT users_role_valid        CHECK (role IN (
        'SUPERADMIN', 'ADMIN', 'OPERATOR', 'USER', 'READONLY', 'SERVICE'
    )),
    CONSTRAINT users_quota_cpu_nonneg  CHECK (quota_cpu >= 0),
    CONSTRAINT users_quota_mem_nonneg  CHECK (quota_memory >= 0),
    CONSTRAINT users_quota_gpu_nonneg  CHECK (quota_gpu >= 0)
);

CREATE INDEX IF NOT EXISTS idx_users_spiffe_id  ON users(spiffe_id);
CREATE INDEX IF NOT EXISTS idx_users_role       ON users(role);
CREATE INDEX IF NOT EXISTS idx_users_email      ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_labels     ON users USING GIN(labels);
"""

MIGRATION_HISTORY = """\
CREATE TABLE IF NOT EXISTS migration_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id),
    source_node     UUID NOT NULL REFERENCES nodes(id),
    target_node     UUID NOT NULL REFERENCES nodes(id),
    method          VARCHAR(20) NOT NULL,
    duration_ms     INT NOT NULL,
    data_size_bytes BIGINT,
    success         BOOLEAN NOT NULL,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT mh_method_valid      CHECK (method IN (
        'CHECKPOINT', 'LIVE', 'COLD', 'SNAPSHOT'
    )),
    CONSTRAINT mh_duration_nonneg   CHECK (duration_ms >= 0),
    CONSTRAINT mh_nodes_differ      CHECK (source_node <> target_node)
);

CREATE INDEX IF NOT EXISTS idx_migration_history_session_id   ON migration_history(session_id);
CREATE INDEX IF NOT EXISTS idx_migration_history_source_node  ON migration_history(source_node);
CREATE INDEX IF NOT EXISTS idx_migration_history_target_node  ON migration_history(target_node);
CREATE INDEX IF NOT EXISTS idx_migration_history_success      ON migration_history(success);
"""

# Migration 015 carries: network_policies + cluster_config (with seed) + the
# helix_-prefixed trigger functions and their per-table attachments.
NETWORK_POLICIES = """\
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
"""

CLUSTER_CONFIG = """\
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
"""

TRIGGERS = """\
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
CREATE TRIGGER helix_nodes_updated_at
    BEFORE UPDATE ON nodes
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_gpu_devices_updated_at
    BEFORE UPDATE ON gpu_devices
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_sessions_updated_at
    BEFORE UPDATE ON sessions
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_reservations_updated_at
    BEFORE UPDATE ON reservations
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_scheduling_queue_updated_at
    BEFORE UPDATE ON scheduling_queue
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_llm_advisories_updated_at
    BEFORE UPDATE ON llm_advisories
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_build_jobs_updated_at
    BEFORE UPDATE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_network_policies_updated_at
    BEFORE UPDATE ON network_policies
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();
CREATE TRIGGER helix_cluster_config_updated_at
    BEFORE UPDATE ON cluster_config
    FOR EACH ROW EXECUTE FUNCTION helix_update_updated_at_column();

-- audit triggers (AFTER, so committed state is captured)
CREATE TRIGGER helix_nodes_audit
    AFTER INSERT OR UPDATE OR DELETE ON nodes
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE TRIGGER helix_sessions_audit
    AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE TRIGGER helix_build_jobs_audit
    AFTER INSERT OR UPDATE OR DELETE ON build_jobs
    FOR EACH ROW EXECUTE FUNCTION helix_audit_trigger();
CREATE TRIGGER helix_users_audit
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
"""

# (version, slug, title, body, down_sql)
MIGRATIONS = [
    ("001", "create_nodes", "Node Registry", NODES,
     "DROP TABLE IF EXISTS nodes CASCADE;\n"),
    ("002", "create_gpu_devices", "GPU Devices", GPU,
     "DROP TABLE IF EXISTS gpu_devices CASCADE;\n"),
    ("003", "create_sessions", "Sessions", SESSIONS,
     "DROP TABLE IF EXISTS sessions CASCADE;\n"),
    ("004", "create_session_windows", "Session Windows", WINDOWS,
     "DROP TABLE IF EXISTS session_windows CASCADE;\n"),
    ("005", "create_session_panes", "Session Panes", PANES,
     "DROP TABLE IF EXISTS session_panes CASCADE;\n"),
    ("006", "create_reservations", "Reservations (resource reservations)", RESERVATIONS,
     "DROP TABLE IF EXISTS reservations CASCADE;\n"),
    ("007", "create_scheduling_queue", "Scheduling Queue", SCHEDULING,
     "DROP TABLE IF EXISTS scheduling_queue CASCADE;\n"),
    ("008", "create_health_snapshots", "Health Snapshots", HEALTH,
     "DROP TABLE IF EXISTS health_snapshots CASCADE;\n"),
    ("009", "create_llm_advisories", "LLM Brain Advisories", ADVISORIES,
     "DROP TABLE IF EXISTS llm_advisories CASCADE;\n"),
    ("010", "create_audit_log", "Audit Log (Partitioned)", AUDIT,
     ("DROP TABLE IF EXISTS audit_log_default CASCADE;\n"
      + "".join(
          f"DROP TABLE IF EXISTS audit_log_{y}_{m:02d} CASCADE;\n"
          for (y, months) in [(2026, range(6, 13)), (2027, range(1, 7))]
          for m in months
      )
      + "DROP TABLE IF EXISTS audit_log CASCADE;\n")),
    ("011", "create_build_jobs", "Build Jobs", BUILD_JOBS,
     "DROP TABLE IF EXISTS build_jobs CASCADE;\n"),
    ("012", "create_build_artifacts", "Build Artifacts", BUILD_ARTIFACTS,
     "DROP TABLE IF EXISTS build_artifacts CASCADE;\n"),
    ("013", "create_users", "Users (shadow of OIDC provider)", USERS,
     "DROP TABLE IF EXISTS users CASCADE;\n"),
    ("014", "create_migration_history", "Session Migration History", MIGRATION_HISTORY,
     "DROP TABLE IF EXISTS migration_history CASCADE;\n"),
    ("015", "triggers_and_functions",
     "Network Policies, Cluster Config, Triggers and Functions",
     NETWORK_POLICIES + "\n" + CLUSTER_CONFIG + "\n" + TRIGGERS,
     ("DROP FUNCTION IF EXISTS helix_audit_trigger() CASCADE;\n"
      "DROP FUNCTION IF EXISTS helix_update_updated_at_column() CASCADE;\n"
      "DROP TABLE IF EXISTS cluster_config CASCADE;\n"
      "DROP TABLE IF EXISTS network_policies CASCADE;\n")),
]

CONSOLIDATED_HEADER = """\
-- ============================================================
-- 0001_primary_schema.sql
-- Helix Cluster OS — Authoritative Postgres 16+ Primary Schema
--
-- GENERATED ARTIFACT — DO NOT EDIT BY HAND.
-- This file is the in-order concatenation of the golang-migrate chain bodies
-- (001_*.up.sql … 015_*.up.sql), produced by migrations/postgresql/.gen_schema.py.
-- The migrate chain is the SINGLE canonical source (HXC-1639). The drift-guard
-- test internal/schema/drift_guard_test.go fails the build if this file and the
-- chain ever diverge.
-- ============================================================
"""


def up_header(version, slug, title):
    return (
        "-- ============================================================\n"
        f"-- {version}_{slug}.up.sql\n"
        f"-- Helix Cluster OS — {title}\n"
        "-- ============================================================\n\n"
    )


def down_header(version, slug, title):
    return (
        "-- ============================================================\n"
        f"-- {version}_{slug}.down.sql\n"
        f"-- Drop {title}\n"
        "-- ============================================================\n\n"
    )


def main():
    consolidated_parts = [CONSOLIDATED_HEADER]
    for (version, slug, title, body, down) in MIGRATIONS:
        body = body.rstrip("\n") + "\n"
        up_path = os.path.join(HERE, f"{version}_{slug}.up.sql")
        down_path = os.path.join(HERE, f"{version}_{slug}.down.sql")
        with open(up_path, "w") as f:
            f.write(up_header(version, slug, title) + body)
        with open(down_path, "w") as f:
            f.write(down_header(version, slug, title) + down.rstrip("\n") + "\n")
        consolidated_parts.append("\n" + body)

    consolidated = "".join(consolidated_parts)
    with open(os.path.join(HERE, "0001_primary_schema.sql"), "w") as f:
        f.write(consolidated)

    print("generated %d migration pairs + 0001_primary_schema.sql" % len(MIGRATIONS))


if __name__ == "__main__":
    main()
