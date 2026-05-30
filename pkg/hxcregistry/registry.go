package hxcregistry

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Registry provides CRUD access to the HXC SQLite database.
type Registry struct {
	db *sql.DB
}

// Open opens or creates the HXC registry at the given path.
// If path is ":memory:", an in-memory database is used (for tests).
func Open(path string) (*Registry, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	r := &Registry{db: db}
	if err := r.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return r, nil
}

// Close closes the database connection.
func (r *Registry) Close() error {
	return r.db.Close()
}

// migrate ensures the schema is present.
func (r *Registry) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS items (
    hxc_id           TEXT PRIMARY KEY NOT NULL,
    type             TEXT NOT NULL CHECK (type IN ('Bug', 'Feature', 'Task', 'Research', 'Docs')),
    status           TEXT NOT NULL CHECK (status IN ('Queued', 'In progress', 'Ready for testing', 'In testing', 'Completed', 'Obsolete')),
    priority         TEXT NOT NULL CHECK (priority IN ('P0', 'P1', 'P2', 'P3')),
    phase            INTEGER NOT NULL DEFAULT 0,
    title            TEXT NOT NULL,
    description      TEXT NOT NULL,
    commit_sha       TEXT,
    forensic_anchor  TEXT,
    closure_criteria TEXT,
    composes_with    TEXT,
    current_location TEXT NOT NULL CHECK (current_location IN ('Issues', 'Fixed')) DEFAULT 'Issues',
    heading_hash     TEXT NOT NULL UNIQUE,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_items_status ON items(status);
CREATE INDEX IF NOT EXISTS idx_items_type ON items(type);
CREATE INDEX IF NOT EXISTS idx_items_priority ON items(priority);
CREATE INDEX IF NOT EXISTS idx_items_phase ON items(phase);
CREATE INDEX IF NOT EXISTS idx_items_location ON items(current_location);

CREATE TABLE IF NOT EXISTS item_history (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    hxc_id           TEXT NOT NULL REFERENCES items(hxc_id) ON DELETE CASCADE,
    event_type       TEXT NOT NULL CHECK (event_type IN ('Opened', 'Updated', 'StatusChanged', 'Completed', 'Obsolete')),
    by_entity        TEXT CHECK (by_entity IN ('AI', 'User', 'System', NULL)),
    on_date          TEXT NOT NULL DEFAULT (datetime('now')),
    reason           TEXT,
    evidence_path    TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_item_history_hxc_id ON item_history(hxc_id);
CREATE INDEX IF NOT EXISTS idx_item_history_event_type ON item_history(event_type);

CREATE TABLE IF NOT EXISTS meta (
    key              TEXT PRIMARY KEY,
    value            TEXT NOT NULL,
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS document_sources (
    location         TEXT PRIMARY KEY NOT NULL CHECK (location IN ('Issues', 'Fixed')),
    raw_text         TEXT NOT NULL DEFAULT '',
    sha256           TEXT NOT NULL DEFAULT '',
    last_modified    TEXT NOT NULL DEFAULT (datetime('now'))
);
`
	if _, err := r.db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}

// CreateItem inserts a new HXC item into the registry.
func (r *Registry) CreateItem(item *HXCItem) error {
	if err := item.Validate(); err != nil {
		return err
	}
	item.EnsureHash()
	now := time.Now().UTC()
	item.CreatedAt = now
	item.LastModified = now

	_, err := r.db.Exec(`
		INSERT INTO items (hxc_id, type, status, priority, phase, title, description,
			commit_sha, forensic_anchor, closure_criteria, composes_with,
			current_location, heading_hash, created_at, last_modified)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.HXCID, item.Type, item.Status, item.Priority, item.Phase,
		item.Title, item.Description, item.CommitSHA, item.ForensicAnchor,
		item.ClosureCriteria, item.ComposesWith, item.CurrentLocation,
		item.HeadingHash, item.CreatedAt.Format(time.RFC3339),
		item.LastModified.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert item: %w", err)
	}

	// Record history
	_, _ = r.db.Exec(`
		INSERT INTO item_history (hxc_id, event_type, by_entity, reason)
		VALUES (?, 'Opened', 'System', 'Created via registry')`, item.HXCID)

	return nil
}

// GetItem retrieves an item by HXC ID.
func (r *Registry) GetItem(id string) (*HXCItem, error) {
	row := r.db.QueryRow(`
		SELECT hxc_id, type, status, priority, phase, title, description,
			commit_sha, forensic_anchor, closure_criteria, composes_with,
			current_location, heading_hash, created_at, last_modified
		FROM items WHERE hxc_id = ?`, id)

	item := &HXCItem{}
	var created, modified string
	var commitSHA, forensic, closure, composes sql.NullString
	err := row.Scan(
		&item.HXCID, &item.Type, &item.Status, &item.Priority, &item.Phase,
		&item.Title, &item.Description, &commitSHA, &forensic,
		&closure, &composes, &item.CurrentLocation,
		&item.HeadingHash, &created, &modified,
	)
	if err == nil {
		item.CommitSHA = commitSHA.String
		item.ForensicAnchor = forensic.String
		item.ClosureCriteria = closure.String
		item.ComposesWith = composes.String
	}
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("item %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("scan item: %w", err)
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339, created)
	item.LastModified, _ = time.Parse(time.RFC3339, modified)
	return item, nil
}

// UpdateItem updates an existing item and records history.
func (r *Registry) UpdateItem(item *HXCItem) error {
	if err := item.Validate(); err != nil {
		return err
	}
	item.LastModified = time.Now().UTC()

	_, err := r.db.Exec(`
		UPDATE items SET
			type = ?, status = ?, priority = ?, phase = ?, title = ?,
			description = ?, commit_sha = ?, forensic_anchor = ?,
			closure_criteria = ?, composes_with = ?, current_location = ?,
			last_modified = ?
		WHERE hxc_id = ?`,
		item.Type, item.Status, item.Priority, item.Phase, item.Title,
		item.Description, item.CommitSHA, item.ForensicAnchor,
		item.ClosureCriteria, item.ComposesWith, item.CurrentLocation,
		item.LastModified.Format(time.RFC3339), item.HXCID,
	)
	if err != nil {
		return fmt.Errorf("update item: %w", err)
	}

	_, _ = r.db.Exec(`
		INSERT INTO item_history (hxc_id, event_type, by_entity, reason)
		VALUES (?, 'Updated', 'System', 'Updated via registry')`, item.HXCID)

	return nil
}

// ListItems returns items filtered by status (empty = all).
func (r *Registry) ListItems(status string) ([]*HXCItem, error) {
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = r.db.Query(`
			SELECT hxc_id, type, status, priority, phase, title, description,
				commit_sha, forensic_anchor, closure_criteria, composes_with,
				current_location, heading_hash, created_at, last_modified
			FROM items WHERE status = ? ORDER BY phase, priority, hxc_id`, status)
	} else {
		rows, err = r.db.Query(`
			SELECT hxc_id, type, status, priority, phase, title, description,
				commit_sha, forensic_anchor, closure_criteria, composes_with,
				current_location, heading_hash, created_at, last_modified
			FROM items ORDER BY phase, priority, hxc_id`)
	}
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()

	var items []*HXCItem
	for rows.Next() {
		item := &HXCItem{}
		var created, modified string
		var commitSHA, forensic, closure, composes sql.NullString
		if err := rows.Scan(
			&item.HXCID, &item.Type, &item.Status, &item.Priority, &item.Phase,
			&item.Title, &item.Description, &commitSHA, &forensic,
			&closure, &composes, &item.CurrentLocation,
			&item.HeadingHash, &created, &modified,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		item.CommitSHA = commitSHA.String
		item.ForensicAnchor = forensic.String
		item.ClosureCriteria = closure.String
		item.ComposesWith = composes.String
		item.CreatedAt, _ = time.Parse(time.RFC3339, created)
		item.LastModified, _ = time.Parse(time.RFC3339, modified)
		items = append(items, item)
	}
	return items, rows.Err()
}

// NextHXCID returns the next available HXC number (best effort).
func (r *Registry) NextHXCID() (string, error) {
	row := r.db.QueryRow(`SELECT hxc_id FROM items ORDER BY hxc_id DESC LIMIT 1`)
	var last string
	if err := row.Scan(&last); err != nil {
		if err == sql.ErrNoRows {
			return "HXC-001", nil
		}
		return "", fmt.Errorf("query last id: %w", err)
	}
	var num int
	if _, err := fmt.Sscanf(last, "HXC-%d", &num); err != nil {
		return "", fmt.Errorf("parse last id: %w", err)
	}
	return fmt.Sprintf("HXC-%03d", num+1), nil
}
