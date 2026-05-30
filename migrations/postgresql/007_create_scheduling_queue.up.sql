-- ============================================================
-- 007_create_scheduling_queue.up.sql
-- Helix Cluster OS — Scheduling Queue
-- ============================================================

CREATE TABLE scheduling_queue (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request         JSONB NOT NULL,
    priority        INT NOT NULL DEFAULT 50,
    status          VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    scheduled_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_scheduling_status ON scheduling_queue(status);
CREATE INDEX idx_scheduling_priority ON scheduling_queue(priority);
CREATE INDEX idx_scheduling_created ON scheduling_queue(created_at);
