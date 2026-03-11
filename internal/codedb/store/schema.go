package store

import "database/sql"

const schemaDDL = `
CREATE TABLE IF NOT EXISTS repos (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS commits (
    id        INTEGER PRIMARY KEY,
    repo_id   INTEGER NOT NULL REFERENCES repos(id),
    hash      TEXT NOT NULL UNIQUE,
    author    TEXT,
    message   TEXT,
    timestamp INTEGER
);

CREATE TABLE IF NOT EXISTS commit_parents (
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    parent_id INTEGER NOT NULL REFERENCES commits(id),
    PRIMARY KEY (commit_id, parent_id)
);

CREATE TABLE IF NOT EXISTS refs (
    id        INTEGER PRIMARY KEY,
    repo_id   INTEGER NOT NULL REFERENCES repos(id),
    name      TEXT NOT NULL,
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    UNIQUE(repo_id, name)
);

CREATE TABLE IF NOT EXISTS blobs (
    id           INTEGER PRIMARY KEY,
    content_hash TEXT NOT NULL UNIQUE,
    language     TEXT,
    parsed       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS file_revs (
    id        INTEGER PRIMARY KEY,
    commit_id INTEGER NOT NULL REFERENCES commits(id),
    path      TEXT NOT NULL,
    blob_id   INTEGER NOT NULL REFERENCES blobs(id),
    UNIQUE(commit_id, path)
);

CREATE TABLE IF NOT EXISTS diffs (
    id          INTEGER PRIMARY KEY,
    commit_id   INTEGER NOT NULL REFERENCES commits(id),
    path        TEXT NOT NULL,
    old_blob_id INTEGER REFERENCES blobs(id),
    new_blob_id INTEGER REFERENCES blobs(id),
    UNIQUE(commit_id, path)
);

CREATE TABLE IF NOT EXISTS symbols (
    id          INTEGER PRIMARY KEY,
    blob_id     INTEGER NOT NULL REFERENCES blobs(id),
    parent_id   INTEGER REFERENCES symbols(id),
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL,
    line        INTEGER NOT NULL,
    col         INTEGER NOT NULL,
    end_line    INTEGER,
    end_col     INTEGER,
    signature   TEXT,
    return_type TEXT,
    params      TEXT
);

CREATE TABLE IF NOT EXISTS symbol_refs (
    id        INTEGER PRIMARY KEY,
    blob_id   INTEGER NOT NULL REFERENCES blobs(id),
    symbol_id INTEGER REFERENCES symbols(id),
    ref_name  TEXT NOT NULL,
    kind      TEXT NOT NULL,
    line      INTEGER NOT NULL,
    col       INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_commits_repo ON commits(repo_id);
CREATE INDEX IF NOT EXISTS idx_refs_repo ON refs(repo_id);
CREATE INDEX IF NOT EXISTS idx_file_revs_commit ON file_revs(commit_id);
CREATE INDEX IF NOT EXISTS idx_file_revs_blob ON file_revs(blob_id);
CREATE INDEX IF NOT EXISTS idx_diffs_commit ON diffs(commit_id);
CREATE INDEX IF NOT EXISTS idx_symbols_blob ON symbols(blob_id);
CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_symbol_refs_blob ON symbol_refs(blob_id);
CREATE INDEX IF NOT EXISTS idx_symbol_refs_name ON symbol_refs(ref_name);
CREATE INDEX IF NOT EXISTS idx_symbol_refs_symbol ON symbol_refs(symbol_id);
`

// CreateSchema initializes the SQLite tables and indexes.
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	if err != nil {
		return err
	}
	if err := migrateAddTypeInfo(db); err != nil {
		return err
	}
	return migrateAddComments(db)
}

// migrateAddTypeInfo adds signature, return_type, and params columns to the
// symbols table for databases created before those columns existed.
func migrateAddTypeInfo(db *sql.DB) error {
	var exists bool
	err := db.QueryRow(`SELECT COUNT(*) > 0 FROM pragma_table_info('symbols') WHERE name='signature'`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	stmts := []string{
		`ALTER TABLE symbols ADD COLUMN signature TEXT`,
		`ALTER TABLE symbols ADD COLUMN return_type TEXT`,
		`ALTER TABLE symbols ADD COLUMN params TEXT`,
		`UPDATE blobs SET parsed = 0 WHERE language IS NOT NULL`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// migrateAddComments creates the comments table and adds the comments_parsed
// column to blobs for databases created before comment indexing existed.
func migrateAddComments(db *sql.DB) error {
	var exists bool
	err := db.QueryRow(`SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='comments'`).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS comments (
				id       INTEGER PRIMARY KEY,
				blob_id  INTEGER NOT NULL REFERENCES blobs(id),
				text     TEXT NOT NULL,
				kind     TEXT NOT NULL,
				line     INTEGER NOT NULL,
				end_line INTEGER NOT NULL,
				col      INTEGER NOT NULL,
				end_col  INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_blob ON comments(blob_id)`,
			`CREATE INDEX IF NOT EXISTS idx_comments_kind ON comments(kind)`,
		}
		for _, s := range stmts {
			if _, err := db.Exec(s); err != nil {
				return err
			}
		}
	}

	// add comments_parsed column to blobs if missing
	var colExists bool
	err = db.QueryRow(`SELECT COUNT(*) > 0 FROM pragma_table_info('blobs') WHERE name='comments_parsed'`).Scan(&colExists)
	if err != nil {
		return err
	}
	if !colExists {
		if _, err := db.Exec(`ALTER TABLE blobs ADD COLUMN comments_parsed INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	return nil
}
