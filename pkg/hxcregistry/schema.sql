-- HXC Registry Schema — adapted from proven tmux workable_items.db schema.
-- Source of truth for Helix Cluster OS HXC-XXX tickets.

PRAGMA foreign_keys = ON;

-- ============================================================
-- items: primary HXC ticket registry
-- ============================================================
CREATE TABLE IF NOT EXISTS items (
    hxc_id           TEXT PRIMARY KEY NOT NULL,

    type             TEXT NOT NULL CHECK (type IN ('Bug', 'Feature', 'Task', 'Research', 'Docs')),

    status           TEXT NOT NULL CHECK (status IN (
                         'Queued', 'In progress', 'Ready for testing',
                         'In testing', 'Completed', 'Obsolete'
                     )),

    priority         TEXT NOT NULL CHECK (priority IN ('P0', 'P1', 'P2', 'P3')),

    title            TEXT NOT NULL,

    description      TEXT NOT NULL DEFAULT '',

    phase            TEXT,

    commit_sha       TEXT,

    current_location TEXT NOT NULL CHECK (current_location IN ('Issues', 'Fixed')) DEFAULT 'Issues',

    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_items_status ON items(status);
CREATE INDEX IF NOT EXISTS idx_items_type ON items(type);
CREATE INDEX IF NOT EXISTS idx_items_priority ON items(priority);
CREATE INDEX IF NOT EXISTS idx_items_location ON items(current_location);
CREATE INDEX IF NOT EXISTS idx_items_phase ON items(phase);

-- ============================================================
-- item_history: append-only audit log for status changes
-- ============================================================
CREATE TABLE IF NOT EXISTS item_history (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    hxc_id           TEXT NOT NULL REFERENCES items(hxc_id),

    event_type       TEXT NOT NULL CHECK (event_type IN (
                         'Opened', 'Updated', 'StatusChanged', 'Closed'
                     )),

    by               TEXT,

    on_date          TEXT NOT NULL DEFAULT (datetime('now')),

    reason           TEXT,

    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_item_history_hxc_id ON item_history(hxc_id);
CREATE INDEX IF NOT EXISTS idx_item_history_event_type ON item_history(event_type);

-- ============================================================
-- meta: registry metadata (last_sync, version)
-- ============================================================
CREATE TABLE IF NOT EXISTS meta (
    key              TEXT PRIMARY KEY,
    value            TEXT NOT NULL,
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO meta(key, value) VALUES
    ('schema_version', '1'),
    ('last_sync_direction', 'none'),
    ('last_sync_timestamp', ''),
    ('integrity_hash', '');

-- ============================================================
-- document_sources: sha256 of issues.md and fixed.md for drift detection
-- ============================================================
CREATE TABLE IF NOT EXISTS document_sources (
    location         TEXT PRIMARY KEY NOT NULL CHECK (location IN ('Issues', 'Fixed')),
    raw_text         TEXT NOT NULL DEFAULT '',
    sha256           TEXT NOT NULL DEFAULT '',
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);
