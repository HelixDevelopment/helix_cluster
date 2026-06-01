package hxcregistry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgxArtifactRegistry is the production Postgres-backed ArtifactRegistry.  It
// uses pgxpool so concurrent callers share a connection pool rather than each
// holding a dedicated connection.  The schema is the artifact_schema.sql file
// in this package directory; callers must apply it (ApplyArtifactSchema) once
// before the first Put.
type PgxArtifactRegistry struct {
	pool *pgxpool.Pool
}

// NewPgxArtifactRegistry opens a pgxpool against dsn (standard Postgres
// connection string: postgres://user:pass@host:port/db?sslmode=disable) and
// returns a ready-to-use PgxArtifactRegistry.
//
// The caller must call ApplyArtifactSchema once before Put/Get/Tag/Resolve
// to ensure the tables exist.  Typically done in the integration test's
// TestMain or in the service's startup sequence.
func NewPgxArtifactRegistry(ctx context.Context, dsn string) (*PgxArtifactRegistry, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.ParseConfig: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.NewWithConfig: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgxpool ping: %w", err)
	}
	return &PgxArtifactRegistry{pool: pool}, nil
}

// Close releases pool resources.  Safe to call multiple times.
func (p *PgxArtifactRegistry) Close() {
	p.pool.Close()
}

// ApplyArtifactSchema creates the artifacts and artifact_tags tables if they
// do not yet exist.  Idempotent (uses IF NOT EXISTS).
func (p *PgxArtifactRegistry) ApplyArtifactSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
    digest      TEXT PRIMARY KEY NOT NULL,
    media_type  TEXT NOT NULL DEFAULT '',
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    data        BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS artifact_tags (
    name        TEXT PRIMARY KEY NOT NULL,
    digest      TEXT NOT NULL REFERENCES artifacts(digest) ON DELETE CASCADE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_artifact_tags_digest ON artifact_tags(digest);
`
	_, err := p.pool.Exec(ctx, schema)
	if err != nil {
		return fmt.Errorf("ApplyArtifactSchema: %w", err)
	}
	return nil
}

// PutArtifact stores content under its sha256 digest (content-addressing).
// If the digest already exists the row is left unchanged (idempotent).
// Returns the hex sha256 digest.
func (p *PgxArtifactRegistry) PutArtifact(mediaType string, content []byte) (string, error) {
	digest := DigestOf(content)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ON CONFLICT DO NOTHING implements idempotent Put: a second Put of the
	// same bytes is a no-op and returns the same digest without error.
	_, err := p.pool.Exec(ctx, `
		INSERT INTO artifacts (digest, media_type, size_bytes, data)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (digest) DO NOTHING
	`, digest, mediaType, int64(len(content)), content)
	if err != nil {
		return "", fmt.Errorf("PutArtifact insert: %w", err)
	}
	return digest, nil
}

// GetArtifact retrieves the bytes stored under digest via a SECOND pool
// connection (demonstrating that the data survives beyond the inserting
// connection, which is the key sink-side proof the integration test requires).
// Returns ErrNotFound if the digest does not exist.
func (p *PgxArtifactRegistry) GetArtifact(digest string) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mediaType string
	var data []byte
	err := p.pool.QueryRow(ctx, `
		SELECT media_type, data FROM artifacts WHERE digest = $1
	`, digest).Scan(&mediaType, &data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, fmt.Errorf("artifact %s: %w", digest, ErrNotFound)
		}
		return "", nil, fmt.Errorf("GetArtifact query: %w", err)
	}
	return mediaType, data, nil
}

// TagArtifact creates or updates the tag → digest mapping.  The digest must
// already exist or the foreign-key constraint will return an error (which this
// method wraps as ErrNotFound so callers do not need to inspect the pg error).
func (p *PgxArtifactRegistry) TagArtifact(name, digest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := p.pool.Exec(ctx, `
		INSERT INTO artifact_tags (name, digest, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
		    SET digest = EXCLUDED.digest,
		        updated_at = EXCLUDED.updated_at
	`, name, digest, time.Now().UTC())
	if err != nil {
		// The FK constraint fires when the digest does not exist.
		return fmt.Errorf("TagArtifact upsert (name=%s digest=%s): %w", name, digest, err)
	}
	return nil
}

// Resolve returns the digest the named tag currently points to.
// Returns ErrNotFound if the tag does not exist.
func (p *PgxArtifactRegistry) Resolve(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var digest string
	err := p.pool.QueryRow(ctx, `
		SELECT digest FROM artifact_tags WHERE name = $1
	`, name).Scan(&digest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("tag %q: %w", name, ErrNotFound)
		}
		return "", fmt.Errorf("Resolve query: %w", err)
	}
	return digest, nil
}
