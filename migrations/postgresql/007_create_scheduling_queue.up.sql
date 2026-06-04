-- ============================================================
-- 007_create_scheduling_queue.up.sql
-- Helix Cluster OS — Scheduling Queue
-- ============================================================

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
