-- ============================================================
-- 009_create_llm_advisories.up.sql
-- Helix Cluster OS — LLM Brain Advisories
-- ============================================================

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
