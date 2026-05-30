-- ============================================================
-- 002_create_gpu_devices.down.sql
-- Drop GPU Devices
-- ============================================================

DROP INDEX IF EXISTS idx_gpu_vendor;
DROP INDEX IF EXISTS idx_gpu_status;
DROP INDEX IF EXISTS idx_gpu_node;
DROP TABLE IF EXISTS gpu_devices;
