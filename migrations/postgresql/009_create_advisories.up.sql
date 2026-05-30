-- ============================================================
-- 009_create_advisories.up.sql
-- Helix Cluster OS — LLM Brain Advisories
-- ============================================================

CREATE TABLE advisories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type            VARCHAR(20) NOT NULL,
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
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_advisories_status ON advisories(status);
CREATE INDEX idx_advisories_type ON advisories(type);
CREATE INDEX idx_advisories_risk ON advisories(risk_level);
