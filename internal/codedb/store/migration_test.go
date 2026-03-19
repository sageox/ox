package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// baseSchemaV1 is the original schema before any migrations existed.
// It intentionally omits: signature/return_type/params on symbols,
// comments table, comments_parsed on blobs, and all GitHub tables.
const baseSchemaV1 = `
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
    end_col     INTEGER
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
`

// createOldSchemaDB opens a SQLite DB with the V1 schema (no migration columns).
func createOldSchemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(baseSchemaV1); err != nil {
		db.Close()
		t.Fatalf("create base schema: %v", err)
	}
	return db, dbPath
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	if err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return count > 0
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count)
	if err != nil {
		t.Fatalf("check column %s.%s: %v", table, column, err)
	}
	return count > 0
}

func TestMigrateAddTypeInfo_FromOlderSchema(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	// V1 schema has no signature/return_type/params on symbols
	if columnExists(t, db, "symbols", "signature") {
		t.Fatal("base schema should NOT have signature column")
	}

	// seed data to verify parsed reset
	db.Exec(`INSERT INTO blobs (content_hash, language, parsed) VALUES ('abc', 'go', 1)`)

	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("migrateAddTypeInfo: %v", err)
	}

	for _, col := range []string{"signature", "return_type", "params"} {
		if !columnExists(t, db, "symbols", col) {
			t.Errorf("symbols.%s should exist after migration", col)
		}
	}

	// parsed blobs should be reset to 0 so they get re-parsed with type info
	var parsed int
	db.QueryRow(`SELECT parsed FROM blobs WHERE content_hash='abc'`).Scan(&parsed)
	if parsed != 0 {
		t.Errorf("expected parsed=0 after migration, got %d", parsed)
	}
}

func TestMigrateAddTypeInfo_Idempotent(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
}

func TestMigrateAddComments_FromOlderSchema(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	if tableExists(t, db, "comments") {
		t.Fatal("base schema should NOT have comments table")
	}
	if columnExists(t, db, "blobs", "comments_parsed") {
		t.Fatal("base schema should NOT have comments_parsed column")
	}

	if err := migrateAddComments(db); err != nil {
		t.Fatalf("migrateAddComments: %v", err)
	}

	if !tableExists(t, db, "comments") {
		t.Error("comments table should exist after migration")
	}
	if !columnExists(t, db, "blobs", "comments_parsed") {
		t.Error("blobs.comments_parsed should exist after migration")
	}

	// verify comments table has expected columns
	for _, col := range []string{"id", "blob_id", "text", "kind", "line", "end_line", "col", "end_col"} {
		if !columnExists(t, db, "comments", col) {
			t.Errorf("comments.%s should exist", col)
		}
	}
}

func TestMigrateAddComments_Idempotent(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	if err := migrateAddComments(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// insert data so we can verify it survives
	db.Exec(`INSERT INTO blobs (content_hash, language, parsed, comments_parsed) VALUES ('x', 'go', 1, 1)`)

	if err := migrateAddComments(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}

	var cp int
	db.QueryRow(`SELECT comments_parsed FROM blobs WHERE content_hash='x'`).Scan(&cp)
	if cp != 1 {
		t.Error("idempotent migration should not reset existing data")
	}
}

func TestMigrateAddGitHubTables_FromOlderSchema(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	for _, tbl := range []string{"pull_requests", "pr_comments", "issues", "issue_comments", "github_file_mtimes"} {
		if tableExists(t, db, tbl) {
			t.Fatalf("base schema should NOT have %s table", tbl)
		}
	}

	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	for _, tbl := range []string{"pull_requests", "pr_comments", "issues", "issue_comments", "github_file_mtimes"} {
		if !tableExists(t, db, tbl) {
			t.Errorf("%s table should exist after migration", tbl)
		}
	}

	// verify we can insert and query
	_, err := db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test PR', 'open')`)
	if err != nil {
		t.Errorf("insert into pull_requests: %v", err)
	}
	_, err = db.Exec(`INSERT INTO issues (number, title, state) VALUES (1, 'test issue', 'open')`)
	if err != nil {
		t.Errorf("insert into issues: %v", err)
	}
}

func TestMigrateAddGitHubTables_Idempotent(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test', 'open')`)

	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM pull_requests`).Scan(&count)
	if count != 1 {
		t.Error("idempotent migration should not affect existing data")
	}
}

func TestCreateSchema_AllMigrations(t *testing.T) {
	t.Parallel()
	db, _ := createOldSchemaDB(t)
	defer db.Close()

	// CreateSchema runs all migrations in sequence
	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema on old DB: %v", err)
	}

	// verify all migration artifacts exist
	checks := []struct {
		table  string
		column string
	}{
		{"symbols", "signature"},
		{"symbols", "return_type"},
		{"symbols", "params"},
		{"comments", "blob_id"},
		{"blobs", "comments_parsed"},
		{"pull_requests", "number"},
		{"issues", "number"},
		{"github_file_mtimes", "source_path"},
	}
	for _, c := range checks {
		if c.column != "" {
			if !columnExists(t, db, c.table, c.column) {
				t.Errorf("after full migration: %s.%s should exist", c.table, c.column)
			}
		} else if !tableExists(t, db, c.table) {
			t.Errorf("after full migration: table %s should exist", c.table)
		}
	}
}

func TestOpenExistingDB_TriggersAllMigrations(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "metadata.db")

	// create a DB with V1 schema (simulating a user upgrading ox)
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(baseSchemaV1); err != nil {
		t.Fatalf("create V1 schema: %v", err)
	}
	// seed data that should survive
	db.Exec(`INSERT INTO repos (name, path) VALUES ('existing', '/tmp/existing')`)
	db.Close()

	// Open() via store should run all migrations
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open existing V1 DB: %v", err)
	}
	defer s.Close()

	// verify data survived
	var name string
	if err := s.QueryRow(`SELECT name FROM repos WHERE name='existing'`).Scan(&name); err != nil {
		t.Fatalf("existing data lost after migration: %v", err)
	}

	// verify migrations ran
	if !columnExists(t, s.db, "symbols", "signature") {
		t.Error("migrateAddTypeInfo did not run")
	}
	if !tableExists(t, s.db, "comments") {
		t.Error("migrateAddComments did not run")
	}
	if !tableExists(t, s.db, "pull_requests") {
		t.Error("migrateAddGitHubTables did not run")
	}
}
