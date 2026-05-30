-- ============================================================
-- 013_create_users.up.sql
-- Helix Cluster OS — Users (shadow of OIDC provider)
-- ============================================================

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    spiffe_id       TEXT NOT NULL UNIQUE,
    email           TEXT,
    name            VARCHAR(255),
    role            VARCHAR(20) NOT NULL DEFAULT 'USER',
    quota_cpu       INT NOT NULL DEFAULT 8000,
    quota_memory    BIGINT NOT NULL DEFAULT 17179869184,
    quota_gpu       INT NOT NULL DEFAULT 0,
    labels          JSONB DEFAULT '{}',
    last_login      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_users_spiffe ON users(spiffe_id);
