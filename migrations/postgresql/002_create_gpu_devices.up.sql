-- ============================================================
-- 002_create_gpu_devices.up.sql
-- Helix Cluster OS — GPU Devices
-- ============================================================

CREATE TABLE gpu_devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id         UUID NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    vendor          VARCHAR(20) NOT NULL,
    model           VARCHAR(100) NOT NULL,
    driver_version  VARCHAR(50) NOT NULL,
    api             VARCHAR(20) NOT NULL,
    api_version     VARCHAR(20) NOT NULL,
    total_memory    BIGINT NOT NULL,
    compute_units   INT NOT NULL,
    features        TEXT[] DEFAULT '{}',
    attributes      JSONB DEFAULT '{}',
    status          VARCHAR(20) NOT NULL DEFAULT 'AVAILABLE',
    allocated_to    UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_gpu_node ON gpu_devices(node_id);
CREATE INDEX idx_gpu_status ON gpu_devices(status);
CREATE INDEX idx_gpu_vendor ON gpu_devices(vendor);
