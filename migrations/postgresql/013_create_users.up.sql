-- ============================================================
-- 013_create_users.up.sql
-- Helix Cluster OS — Users (shadow of OIDC provider)
-- ============================================================

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
