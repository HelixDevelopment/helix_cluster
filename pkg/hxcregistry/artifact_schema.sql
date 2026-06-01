-- Artifact registry schema for Helix Cluster OS content-addressed artifact store.
-- Apply once before the first PutArtifact call.  All statements are idempotent
-- (IF NOT EXISTS / ON CONFLICT), so re-applying is safe.

-- artifacts: primary content-addressed store.
-- digest is sha256(data) in hex; it is the primary key so a second Put of the
-- same bytes is a no-op (ON CONFLICT DO NOTHING in the Go layer).
CREATE TABLE IF NOT EXISTS artifacts (
    digest      TEXT PRIMARY KEY NOT NULL,
    media_type  TEXT NOT NULL DEFAULT '',
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    data        BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- artifact_tags: human-readable name → digest mapping (mutable pointer).
-- digest FK ensures a tag can only point at an artifact that exists.
CREATE TABLE IF NOT EXISTS artifact_tags (
    name        TEXT PRIMARY KEY NOT NULL,
    digest      TEXT NOT NULL REFERENCES artifacts(digest) ON DELETE CASCADE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_artifact_tags_digest ON artifact_tags(digest);
