-- ============================================================
-- 002_create_gpu_devices.up.sql
-- Helix Cluster OS — GPU Devices
-- ============================================================

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
