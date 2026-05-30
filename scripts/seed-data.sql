-- ============================================================
-- seed-data.sql
-- Helix Cluster OS — Realistic Test Data
-- ============================================================
-- Run after all migrations are applied.
-- Usage: psql -U helix -d helix_cluster -f scripts/seed-data.sql
-- ============================================================

BEGIN;

-- ----------------------------------------------------------
-- Nodes (4-node heterogeneous cluster)
-- ----------------------------------------------------------
INSERT INTO nodes (id, hostname, ip_addresses, wg_pubkey, spiffe_id, status, role,
    cpu_arch, cpu_cores, cpu_threads, memory_bytes, gpu_count, storage_bytes,
    labels, region, version)
VALUES
    ('a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'node-alpha', ARRAY['192.168.1.10'::INET, '10.0.0.10'::INET],
     'alpha_wg_pubkey_1234567890abcdef', 'spiffe://helix.cluster/nodes/alpha',
     'ACTIVE', 'HYBRID', 'x86_64', 16, 32, 68719476736, 1, 2199023255552,
     '{"rack": "A1", "zone": "us-east-1a", "gpu_vendor": "nvidia"}'::JSONB,
     'us-east-1', '1.0.0-alpha'),

    ('b2c3d4e5-f6a7-8901-bcde-f12345678901', 'node-beta', ARRAY['192.168.1.11'::INET, '10.0.0.11'::INET],
     'beta_wg_pubkey_abcdef1234567890', 'spiffe://helix.cluster/nodes/beta',
     'ACTIVE', 'WORKER', 'x86_64', 32, 64, 137438953472, 1, 4398046511104,
     '{"rack": "A2", "zone": "us-east-1b", "gpu_vendor": "amd"}'::JSONB,
     'us-east-1', '1.0.0-alpha'),

    ('c3d4e5f6-a7b8-9012-cdef-123456789012', 'node-gamma', ARRAY['192.168.1.12'::INET, '10.0.0.12'::INET],
     'gamma_wg_pubkey_fedcba0987654321', 'spiffe://helix.cluster/nodes/gamma',
     'ACTIVE', 'WORKER', 'arm64', 12, 12, 38654705664, 1, 1099511627776,
     '{"rack": "B1", "zone": "us-west-2a", "gpu_vendor": "apple"}'::JSONB,
     'us-west-2', '1.0.0-alpha'),

    ('d4e5f6a7-b8c9-0123-defa-234567890123', 'node-delta', ARRAY['192.168.1.13'::INET, '10.0.0.13'::INET],
     'delta_wg_pubkey_1122334455667788', 'spiffe://helix.cluster/nodes/delta',
     'JOINING', 'WORKER', 'x86_64', 16, 16, 68719476736, 1, 2199023255552,
     '{"rack": "A3", "zone": "us-east-1c", "gpu_vendor": "intel"}'::JSONB,
     'us-east-1', '1.0.0-alpha');

-- ----------------------------------------------------------
-- GPU Devices
-- ----------------------------------------------------------
INSERT INTO gpu_devices (id, node_id, vendor, model, driver_version, api, api_version,
    total_memory, compute_units, features, attributes, status)
VALUES
    ('gpu-1111-2222-3333-4444-555555555555', 'a1b2c3d4-e5f6-7890-abcd-ef1234567890',
     'NVIDIA', 'GeForce RTX 4080', '545.23.06', 'CUDA', '12.3',
     17179869184, 76, ARRAY['ray_tracing', 'dlss', 'tensor_cores'],
     '{"pcie": "gen4", "power_limit_w": 320}'::JSONB, 'AVAILABLE'),

    ('gpu-2222-3333-4444-5555-666666666666', 'b2c3d4e5-f6a7-8901-bcde-f12345678901',
     'AMD', 'Radeon RX 7900 XTX', '23.30.13', 'ROCm', '5.7',
     25769803776, 96, ARRAY['ray_tracing', 'fsr'],
     '{"pcie": "gen4", "power_limit_w": 355}'::JSONB, 'ALLOCATED'),

    ('gpu-3333-4444-5555-6666-777777777777', 'c3d4e5f6-a7b8-9012-cdef-123456789012',
     'Apple', 'M3 Pro 18-Core GPU', 'macOS 14.2', 'Metal', '3.1',
     12884901888, 18, ARRAY['metal_performance_shaders', 'neural_engine'],
     '{"unified_memory": true, "power_limit_w": 40}'::JSONB, 'AVAILABLE'),

    ('gpu-4444-5555-6666-7777-888888888888', 'd4e5f6a7-b8c9-0123-defa-234567890123',
     'Intel', 'Arc A770', '31.0.101.5122', 'oneAPI', '2024.0',
     17179869184, 32, ARRAY['ray_tracing', 'xess'],
     '{"pcie": "gen4", "power_limit_w": 225}'::JSONB, 'AVAILABLE');

-- ----------------------------------------------------------
-- Users
-- ----------------------------------------------------------
INSERT INTO users (id, spiffe_id, email, name, role, quota_cpu, quota_memory, quota_gpu, labels)
VALUES
    ('user-1111-aaaa-bbbb-cccc-dddddddddddd', 'spiffe://helix.cluster/users/admin',
     'admin@helix.cluster', 'Cluster Admin', 'ADMIN', 32000, 68719476736, 4,
     '{"team": "platform", "department": "engineering"}'::JSONB),

    ('user-2222-aaaa-bbbb-cccc-dddddddddddd', 'spiffe://helix.cluster/users/alice',
     'alice@helix.cluster', 'Alice Chen', 'USER', 16000, 34359738368, 2,
     '{"team": "ml", "department": "research"}'::JSONB),

    ('user-3333-aaaa-bbbb-cccc-dddddddddddd', 'spiffe://helix.cluster/users/bob',
     'bob@helix.cluster', 'Bob Smith', 'USER', 8000, 17179869184, 0,
     '{"team": "build", "department": "engineering"}'::JSONB);

-- ----------------------------------------------------------
-- Sessions
-- ----------------------------------------------------------
INSERT INTO sessions (id, name, owner, status, mode, backend, node_id,
    cpu_request, memory_request, gpu_request, priority, labels,
    started_at, updated_at)
VALUES
    ('sess-1111-aaaa-bbbb-cccc-111111111111', 'dev-shell', 'spiffe://helix.cluster/users/alice',
     'RUNNING', 'INTERACTIVE', 'TMUX', 'a1b2c3d4-e5f6-7890-abcd-ef1234567890',
     2000, 4294967296, NULL, 80,
     '{"project": "helix-api", "env": "development"}'::JSONB,
     NOW() - INTERVAL '2 hours', NOW()),

    ('sess-2222-aaaa-bbbb-cccc-222222222222', 'ml-training', 'spiffe://helix.cluster/users/alice',
     'RUNNING', 'BATCH', 'ZELLIJ', 'b2c3d4e5-f6a7-8901-bcde-f12345678901',
     16000, 68719476736, '{"gpu_count": 1, "gpu_memory_min": 17179869184}'::JSONB, 95,
     '{"project": "lora-finetune", "framework": "pytorch"}'::JSONB,
     NOW() - INTERVAL '6 hours', NOW()),

    ('sess-3333-aaaa-bbbb-cccc-333333333333', 'aosp-build', 'spiffe://helix.cluster/users/bob',
     'RUNNING', 'BATCH', 'TMUX', 'a1b2c3d4-e5f6-7890-abcd-ef1234567890',
     8000, 34359738368, NULL, 70,
     '{"project": "android-15", "target": "aosp_cf_x86_64_phone"}'::JSONB,
     NOW() - INTERVAL '30 minutes', NOW()),

    ('sess-4444-aaaa-bbbb-cccc-444444444444', 'ios-dev', 'spiffe://helix.cluster/users/alice',
     'CREATING', 'INTERACTIVE', 'TMUX', 'c3d4e5f6-a7b8-9012-cdef-123456789012',
     4000, 8589934592, '{"gpu_count": 1, "gpu_vendor": "apple"}'::JSONB, 60,
     '{"project": "swift-app", "env": "development"}'::JSONB,
     NULL, NOW());

-- Update GPU allocated_to references
UPDATE gpu_devices SET allocated_to = 'sess-2222-aaaa-bbbb-cccc-222222222222'
WHERE id = 'gpu-2222-3333-4444-5555-666666666666';

-- ----------------------------------------------------------
-- Session Windows
-- ----------------------------------------------------------
INSERT INTO session_windows (id, session_id, name, layout, active, crdt_state)
VALUES
    ('win-1111-aaaa-bbbb-cccc-111111111111', 'sess-1111-aaaa-bbbb-cccc-111111111111',
     'editor', 'tiled', TRUE, '{"layout": "main-vertical", "splits": [0.6, 0.4]}'::JSONB),

    ('win-2222-aaaa-bbbb-cccc-222222222222', 'sess-1111-aaaa-bbbb-cccc-111111111111',
     'logs', 'tiled', FALSE, '{"layout": "even-horizontal", "splits": [0.5, 0.5]}'::JSONB),

    ('win-3333-aaaa-bbbb-cccc-333333333333', 'sess-2222-aaaa-bbbb-cccc-222222222222',
     'training', 'fullscreen', TRUE, NULL),

    ('win-4444-aaaa-bbbb-cccc-444444444444', 'sess-3333-aaaa-bbbb-cccc-333333333333',
     'build', 'tiled', TRUE, '{"layout": "main-horizontal", "splits": [0.7, 0.3]}'::JSONB);

-- ----------------------------------------------------------
-- Session Panes
-- ----------------------------------------------------------
INSERT INTO session_panes (id, window_id, node_id, command, working_dir, environment,
    cpu_limit, memory_limit, gpu_id, status, crdt_state)
VALUES
    ('pane-1111-aaaa-bbbb-cccc-111111111111', 'win-1111-aaaa-bbbb-cccc-111111111111',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'nvim', '/home/alice/helix-api',
     '{"EDITOR": "nvim", "SHELL": "/bin/zsh"}'::JSONB,
     1000, 2147483648, NULL, 'RUNNING', NULL),

    ('pane-2222-aaaa-bbbb-cccc-222222222222', 'win-1111-aaaa-bbbb-cccc-111111111111',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'cargo watch -x run', '/home/alice/helix-api',
     '{"RUST_LOG": "debug", "SHELL": "/bin/zsh"}'::JSONB,
     1000, 2147483648, NULL, 'RUNNING', NULL),

    ('pane-3333-aaaa-bbbb-cccc-333333333333', 'win-2222-aaaa-bbbb-cccc-222222222222',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'tail -f /var/log/helix/*.log', '/var/log/helix',
     '{"SHELL": "/bin/zsh"}'::JSONB,
     500, 1073741824, NULL, 'RUNNING', NULL),

    ('pane-4444-aaaa-bbbb-cccc-444444444444', 'win-3333-aaaa-bbbb-cccc-333333333333',
     'b2c3d4e5-f6a7-8901-bcde-f12345678901', 'python train.py --config config.yaml',
     '/home/alice/lora-finetune',
     '{"CUDA_VISIBLE_DEVICES": "0", "PYTHONPATH": "/home/alice/lora-finetune"}'::JSONB,
     8000, 34359738368, 'gpu-2222-3333-4444-5555-666666666666', 'RUNNING', NULL),

    ('pane-5555-aaaa-bbbb-cccc-555555555555', 'win-4444-aaaa-bbbb-cccc-444444444444',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'm', '/home/bob/aosp',
     '{"ANDROID_HOME": "/opt/android-sdk", "SHELL": "/bin/bash"}'::JSONB,
     4000, 17179869184, NULL, 'RUNNING', NULL);

-- ----------------------------------------------------------
-- Resource Allocations
-- ----------------------------------------------------------
INSERT INTO resource_allocations (id, session_id, node_id, cpu_millicores, memory_bytes,
    gpu_ids, status, expires_at)
VALUES
    ('alloc-1111-aaaa-bbbb-cccc-111111111111', 'sess-1111-aaaa-bbbb-cccc-111111111111',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 2000, 4294967296,
     NULL, 'ACTIVE', NOW() + INTERVAL '24 hours'),

    ('alloc-2222-aaaa-bbbb-cccc-222222222222', 'sess-2222-aaaa-bbbb-cccc-222222222222',
     'b2c3d4e5-f6a7-8901-bcde-f12345678901', 16000, 68719476736,
     ARRAY['gpu-2222-3333-4444-5555-666666666666']::UUID[], 'ACTIVE', NOW() + INTERVAL '48 hours'),

    ('alloc-3333-aaaa-bbbb-cccc-333333333333', 'sess-3333-aaaa-bbbb-cccc-333333333333',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 8000, 34359738368,
     NULL, 'ACTIVE', NOW() + INTERVAL '12 hours'),

    ('alloc-4444-aaaa-bbbb-cccc-444444444444', 'sess-4444-aaaa-bbbb-cccc-444444444444',
     'c3d4e5f6-a7b8-9012-cdef-123456789012', 4000, 8589934592,
     ARRAY['gpu-3333-4444-5555-6666-777777777777']::UUID[], 'PENDING', NOW() + INTERVAL '24 hours');

-- ----------------------------------------------------------
-- Scheduling Queue
-- ----------------------------------------------------------
INSERT INTO scheduling_queue (id, request, priority, status, scheduled_at, started_at, completed_at)
VALUES
    ('sched-1111-aaaa-bbbb-cccc-111111111111',
     '{"type": "session", "session_id": "sess-4444-aaaa-bbbb-cccc-444444444444", "requirements": {"cpu": 4000, "memory": 8589934592, "gpu": 1}}'::JSONB,
     60, 'SCHEDULED', NOW() - INTERVAL '5 minutes', NOW() - INTERVAL '4 minutes', NULL),

    ('sched-2222-aaaa-bbbb-cccc-222222222222',
     '{"type": "build", "job_id": "build-5555-aaaa-bbbb-cccc-555555555555", "requirements": {"cpu": 16000, "memory": 68719476736, "gpu": 0}}'::JSONB,
     75, 'PENDING', NULL, NULL, NULL),

    ('sched-3333-aaaa-bbbb-cccc-333333333333',
     '{"type": "session", "session_id": "sess-5555-aaaa-bbbb-cccc-555555555555", "requirements": {"cpu": 2000, "memory": 4294967296}}'::JSONB,
     50, 'COMPLETED', NOW() - INTERVAL '1 hour', NOW() - INTERVAL '59 minutes', NOW() - INTERVAL '30 minutes');

-- ----------------------------------------------------------
-- Health Scores
-- ----------------------------------------------------------
INSERT INTO health_scores (node_id, overall_score, cpu_score, memory_score, disk_score,
    network_score, gpu_score, temperature_score, services_score, predictions, metrics)
VALUES
    ('a1b2c3d4-e5f6-7890-abcd-ef1234567890', 92, 88, 95, 90, 94, 96, 85, 98,
     '[{"metric": "cpu_temp", "forecast": 72, "horizon": "5m"}]'::JSONB,
     '{"cpu_usage": 0.45, "memory_usage": 0.62, "disk_io_mbps": 120}'::JSONB),

    ('b2c3d4e5-f6a7-8901-bcde-f12345678901', 87, 82, 78, 88, 90, 92, 75, 95,
     '[{"metric": "gpu_memory", "forecast": 0.95, "horizon": "10m", "alert": "high_utilization"}]'::JSONB,
     '{"cpu_usage": 0.78, "memory_usage": 0.85, "disk_io_mbps": 200}'::JSONB),

    ('c3d4e5f6-a7b8-9012-cdef-123456789012', 95, 96, 94, 92, 88, 97, 98, 99,
     '[]'::JSONB,
     '{"cpu_usage": 0.22, "memory_usage": 0.35, "disk_io_mbps": 45}'::JSONB),

    ('d4e5f6a7-b8c9-0123-defa-234567890123', 78, 80, 75, 70, 82, 76, 72, 88,
     '[{"metric": "disk_space", "forecast": 0.15, "horizon": "1h", "alert": "low_disk"}]'::JSONB,
     '{"cpu_usage": 0.55, "memory_usage": 0.70, "disk_io_mbps": 80}'::JSONB);

-- ----------------------------------------------------------
-- Advisories (LLM Brain)
-- ----------------------------------------------------------
INSERT INTO advisories (id, type, description, rationale, proposed_action, confidence,
    risk_level, auto_approve, status, applied_by, applied_at, result)
VALUES
    ('adv-1111-aaaa-bbbb-cccc-111111111111', 'RESOURCE_REBALANCE',
     'Move session sess-2222 from node-beta to node-alpha to free GPU on beta for higher-priority job.',
     'Node-beta GPU memory at 95%% forecast. Node-alpha GPU is idle. Migration would reduce contention.',
     '{"action": "migrate_session", "session_id": "sess-2222-aaaa-bbbb-cccc-222222222222", "target_node": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"}'::JSONB,
     0.87, 'LOW', TRUE, 'APPLIED', 'system', NOW() - INTERVAL '10 minutes',
     '{"success": true, "duration_ms": 4200}'::JSONB),

    ('adv-2222-aaaa-bbbb-cccc-222222222222', 'SCALE_UP',
     'Cluster CPU utilization trending above 80%%. Recommend adding node-delta to active pool.',
     '7-day CPU trend shows 12%% weekly growth. Current pool will saturate in ~3 days.',
     '{"action": "promote_node", "node_id": "d4e5f6a7-b8c9-0123-defa-234567890123", "role": "WORKER"}'::JSONB,
     0.74, 'MEDIUM', FALSE, 'PENDING', NULL, NULL, NULL),

    ('adv-3333-aaaa-bbbb-cccc-333333333333', 'HEALTH_ALERT',
     'Node-delta disk space critically low (15%% remaining). Clean build artifacts or expand storage.',
     'Disk score dropped from 85 to 70 over 24h. Build artifacts consuming 1.2TB.',
     '{"action": "cleanup_artifacts", "node_id": "d4e5f6a7-b8c9-0123-defa-234567890123", "retention": "7d"}'::JSONB,
     0.91, 'HIGH', FALSE, 'PENDING', NULL, NULL, NULL);

-- ----------------------------------------------------------
-- Build Jobs
-- ----------------------------------------------------------
INSERT INTO build_jobs (id, name, status, mode, cpu_request, memory_request, gpu_request,
    node_id, priority, labels, started_at, completed_at)
VALUES
    ('build-1111-aaaa-bbbb-cccc-111111111111', 'aosp-main-userdebug',
     'COMPLETED', 'BATCH', 8000, 34359738368, NULL,
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 70,
     '{"target": "aosp_cf_x86_64_phone", "branch": "main"}'::JSONB,
     NOW() - INTERVAL '8 hours', NOW() - INTERVAL '6 hours'),

    ('build-2222-aaaa-bbbb-cccc-222222222222', 'kernel-gs201',
     'RUNNING', 'BATCH', 4000, 17179869184, NULL,
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 65,
     '{"target": "kernel", "device": "pixel7"}'::JSONB,
     NOW() - INTERVAL '1 hour', NULL),

    ('build-3333-aaaa-bbbb-cccc-333333333333', 'llama-cpp-cuda',
     'PENDING', 'BATCH', 16000, 68719476736,
     '{"gpu_count": 1, "gpu_memory_min": 17179869184}'::JSONB,
     NULL, 90,
     '{"framework": "llama.cpp", "variant": "cuda"}'::JSONB,
     NULL, NULL),

    ('build-4444-aaaa-bbbb-cccc-444444444444', 'helix-agent-darwin',
     'FAILED', 'BATCH', 4000, 8589934592, NULL,
     'c3d4e5f6-a7b8-9012-cdef-123456789012', 50,
     '{"target": "darwin-arm64", "branch": "feature/wireguard-mesh"}'::JSONB,
     NOW() - INTERVAL '3 hours', NOW() - INTERVAL '2 hours 55 minutes');

-- ----------------------------------------------------------
-- Build Artifacts
-- ----------------------------------------------------------
INSERT INTO build_artifacts (id, job_id, artifact_hash, size_bytes, storage_path, metadata)
VALUES
    ('art-1111-aaaa-bbbb-cccc-111111111111', 'build-1111-aaaa-bbbb-cccc-111111111111',
     'sha256:a1b2c3d4e5f67890abcdef1234567890abcdef1234567890abcdef1234567890', 2147483648,
     'ceph://builds/aosp/main/2026-05-30/aosp-main-userdebug.zip',
     '{"compression": "zip", "signed": true}'::JSONB),

    ('art-2222-aaaa-bbbb-cccc-222222222222', 'build-1111-aaaa-bbbb-cccc-111111111111',
     'sha256:b2c3d4e5f6a78901bcdef1234567890abcdef1234567890abcdef1234567890a', 1073741824,
     'ceph://builds/aosp/main/2026-05-30/boot.img',
     '{"type": "boot_image", "signed": true}'::JSONB),

    ('art-3333-aaaa-bbbb-cccc-333333333333', 'build-1111-aaaa-bbbb-cccc-111111111111',
     'sha256:c3d4e5f6a7b89012cdef1234567890abcdef1234567890abcdef1234567890ab', 536870912,
     'ceph://builds/aosp/main/2026-05-30/system.img',
     '{"type": "system_image", "sparse": true}'::JSONB);

-- ----------------------------------------------------------
-- Migration History
-- ----------------------------------------------------------
INSERT INTO migration_history (id, session_id, source_node, target_node, method,
    duration_ms, data_size_bytes, success, error_message)
VALUES
    ('mig-1111-aaaa-bbbb-cccc-111111111111', 'sess-2222-aaaa-bbbb-cccc-222222222222',
     'b2c3d4e5-f6a7-8901-bcde-f12345678901', 'a1b2c3d4-e5f6-7890-abcd-ef1234567890',
     'CRIU', 4200, 268435456, TRUE, NULL),

    ('mig-2222-aaaa-bbbb-cccc-222222222222', 'sess-1111-aaaa-bbbb-cccc-111111111111',
     'a1b2c3d4-e5f6-7890-abcd-ef1234567890', 'c3d4e5f6-a7b8-9012-cdef-123456789012',
     'RESTART', 850, 0, TRUE, NULL),

    ('mig-3333-aaaa-bbbb-cccc-333333333333', 'sess-3333-aaaa-bbbb-cccc-333333333333',
     'b2c3d4e5-f6a7-8901-bcde-f12345678901', 'd4e5f6a7-b8c9-0123-defa-234567890123',
     'DMTCP', 15600, 1073741824, FALSE,
     'DMTCP checkpoint failed: GPU context not serializable on Intel Arc');

COMMIT;

-- ============================================================
-- Verification Queries
-- ============================================================

-- Count all seeded records
SELECT 'nodes' AS table_name, COUNT(*) AS count FROM nodes
UNION ALL SELECT 'gpu_devices', COUNT(*) FROM gpu_devices
UNION ALL SELECT 'users', COUNT(*) FROM users
UNION ALL SELECT 'sessions', COUNT(*) FROM sessions
UNION ALL SELECT 'session_windows', COUNT(*) FROM session_windows
UNION ALL SELECT 'session_panes', COUNT(*) FROM session_panes
UNION ALL SELECT 'resource_allocations', COUNT(*) FROM resource_allocations
UNION ALL SELECT 'scheduling_queue', COUNT(*) FROM scheduling_queue
UNION ALL SELECT 'health_scores', COUNT(*) FROM health_scores
UNION ALL SELECT 'advisories', COUNT(*) FROM advisories
UNION ALL SELECT 'build_jobs', COUNT(*) FROM build_jobs
UNION ALL SELECT 'build_artifacts', COUNT(*) FROM build_artifacts
UNION ALL SELECT 'migration_history', COUNT(*) FROM migration_history
UNION ALL SELECT 'audit_log', COUNT(*) FROM audit_log;
