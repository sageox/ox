package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"sync"
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
// Pragmas match openSQLite's production DSN (store.go) — in particular
// busy_timeout, without which a concurrent-writer test would see raw
// SQLITE_BUSY errors that real ox connections never surface, since
// busy_timeout makes SQLite retry internally instead of failing instantly.
func createOldSchemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(baseSchemaV1); err != nil {
		_ = db.Close()
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	// V1 schema has no signature/return_type/params on symbols
	if columnExists(t, db, "symbols", "signature") {
		t.Fatal("base schema should NOT have signature column")
	}

	// seed data to verify parsed reset
	_, _ = db.Exec(`INSERT INTO blobs (content_hash, language, parsed) VALUES ('abc', 'go', 1)`)

	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("migrateAddTypeInfo: %v", err)
	}

	for _, col := range []string{"signature", "return_type", "params"} {
		if !columnExists(t, db, "symbols", col) {
			t.Errorf("symbols.%s should exist after migration", col)
		}
	}

	// parsed blobs should be reset to 0 so they get re-parsed with type info
	var parsed int
	_ = db.QueryRow(`SELECT parsed FROM blobs WHERE content_hash='abc'`).Scan(&parsed)
	if parsed != 0 {
		t.Errorf("expected parsed=0 after migration, got %d", parsed)
	}
}

func TestMigrateAddTypeInfo_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
}

func TestMigrateAddComments_FromOlderSchema(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if tableExists(t, db, "comments") {
		t.Fatal("base schema should NOT have comments table")
	}
	if columnExists(t, db, "blobs", "comments_parsed") {
		t.Fatal("base schema should NOT have comments_parsed column")
	}

	if err := migrateAddComments(t.Context(), db); err != nil {
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddComments(t.Context(), db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// insert data so we can verify it survives
	_, _ = db.Exec(`INSERT INTO blobs (content_hash, language, parsed, comments_parsed) VALUES ('x', 'go', 1, 1)`)

	if err := migrateAddComments(t.Context(), db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}

	var cp int
	_ = db.QueryRow(`SELECT comments_parsed FROM blobs WHERE content_hash='x'`).Scan(&cp)
	if cp != 1 {
		t.Error("idempotent migration should not reset existing data")
	}
}

func TestMigrateAddGitHubTables_FromOlderSchema(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	for _, tbl := range []string{"pull_requests", "pr_comments", "issues", "issue_comments", "github_file_mtimes"} {
		if tableExists(t, db, tbl) {
			t.Fatalf("base schema should NOT have %s table", tbl)
		}
	}

	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	_, _ = db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test', 'open')`)

	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pull_requests`).Scan(&count)
	if count != 1 {
		t.Error("idempotent migration should not affect existing data")
	}
}

func TestMigrateAddPRCommits_FromOlderSchema(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	// run prerequisite migration first
	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	if tableExists(t, db, "pr_commits") {
		t.Fatal("pr_commits should NOT exist before migration")
	}

	if err := migrateAddPRCommits(t.Context(), db); err != nil {
		t.Fatalf("migrateAddPRCommits: %v", err)
	}

	if !tableExists(t, db, "pr_commits") {
		t.Error("pr_commits table should exist after migration")
	}

	// verify columns
	for _, col := range []string{"id", "pr_id", "sha"} {
		if !columnExists(t, db, "pr_commits", col) {
			t.Errorf("pr_commits.%s should exist", col)
		}
	}

	// verify we can insert and query with FK
	_, err := db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (42, 'test', 'merged')`)
	if err != nil {
		t.Fatalf("insert PR: %v", err)
	}
	_, err = db.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (1, 'abc123')`)
	if err != nil {
		t.Errorf("insert pr_commits: %v", err)
	}

	var sha string
	_ = db.QueryRow(`SELECT sha FROM pr_commits WHERE pr_id = 1`).Scan(&sha)
	if sha != "abc123" {
		t.Errorf("expected sha 'abc123', got %q", sha)
	}
}

func TestMigrateAddPRCommits_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	if err := migrateAddPRCommits(t.Context(), db); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// insert data
	_, _ = db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test', 'merged')`)
	_, _ = db.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (1, 'sha1')`)

	if err := migrateAddPRCommits(t.Context(), db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM pr_commits`).Scan(&count)
	if count != 1 {
		t.Error("idempotent migration should not affect existing data")
	}
}

// TestMigrateInvalidateGitHubMtimesForIssue474 verifies the one-shot cache
// reset that recovers existing CodeDB rows from the indexer's lex-order bug.
// After the fix, mtimes recorded by the buggy indexer would normally make the
// new indexer skip groups (the cache says "nothing changed"). Clearing the
// table once forces a full re-pick on the next IndexGitHubData call.
func TestMigrateInvalidateGitHubMtimesForIssue474(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddGitHubTables(t.Context(), db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	// pretend the buggy indexer ran and populated mtimes
	rows := [][2]any{
		{"/some/path/461-aaaaaaaa.json", int64(1700000001)},
		{"/some/path/461-bbbbbbbb.json", int64(1700000002)},
		{"/some/path/472-cccccccc.json", int64(1700000003)},
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO github_file_mtimes (source_path, mtime_unix) VALUES (?, ?)`,
			r[0], r[1],
		); err != nil {
			t.Fatalf("seed mtime row: %v", err)
		}
	}

	// run migration
	if err := migrateInvalidateGitHubMtimesForIssue474(t.Context(), db); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// all real mtime rows should be gone, sentinel should remain
	var realCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM github_file_mtimes WHERE source_path NOT LIKE '__migration%'`,
	).Scan(&realCount); err != nil {
		t.Fatalf("count real rows: %v", err)
	}
	if realCount != 0 {
		t.Errorf("expected all real mtime rows cleared, got %d remaining", realCount)
	}

	var sentinelCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM github_file_mtimes WHERE source_path = '__migration_474_indexer_lex_order__'`,
	).Scan(&sentinelCount); err != nil {
		t.Fatalf("count sentinel: %v", err)
	}
	if sentinelCount != 1 {
		t.Errorf("expected sentinel row, got %d", sentinelCount)
	}

	// seed real rows again — these would be added by a subsequent indexer run
	if _, err := db.Exec(
		`INSERT INTO github_file_mtimes (source_path, mtime_unix) VALUES ('/new/path/100-aaa.json', 1700000010)`,
	); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	// second run must be a no-op (idempotent) — must NOT clear the new rows
	if err := migrateInvalidateGitHubMtimesForIssue474(t.Context(), db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	if err := db.QueryRow(
		`SELECT COUNT(*) FROM github_file_mtimes WHERE source_path NOT LIKE '__migration%'`,
	).Scan(&realCount); err != nil {
		t.Fatalf("count after second migration: %v", err)
	}
	if realCount != 1 {
		t.Errorf("idempotent migration must preserve post-migration rows, got %d", realCount)
	}
}

func TestCreateSchema_AllMigrations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

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
		{"pr_commits", "sha"},
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
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
	_, _ = db.Exec(`INSERT INTO repos (name, path) VALUES ('existing', '/tmp/existing')`)
	_ = db.Close()

	// Open() via store should run all migrations
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open existing V1 DB: %v", err)
	}
	defer func() { _ = s.Close() }()

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
	if !tableExists(t, s.db, "pr_commits") {
		t.Error("migrateAddPRCommits did not run")
	}
}

// TestCreateSchema_ConcurrentFreshOpen races 16 openers against a codedb
// directory that does not exist yet: a cold `ox index code` meeting another
// opener (e.g. a concurrent `OpenSQLOnly` read path, or two agents
// cold-building the same repo). Every open must succeed and the full schema
// must land. It runs in parallel with the package's other tests, whose SQLite
// traffic in the same process is the load under which the per-statement build
// used to outlast busy_timeout in CI. TestCreateSchema_OneTransaction and
// TestCreateSchema_InProcessOpenersTakeTurns are the deterministic proofs.
//
// Failure prevented: a cold index build failing under concurrent openers with
// "create schema: database is locked".
func TestCreateSchema_ConcurrentFreshOpen(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	const openers = 16
	root := t.TempDir()

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, openers)
	dbs := make([]*sql.DB, openers)

	for i := range openers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // released together to maximize overlap
			db, _, err := openSQLite(root)
			errs[i] = err
			dbs[i] = db
		}(i)
	}
	close(start)
	wg.Wait()

	defer func() {
		for _, db := range dbs {
			if db != nil {
				_ = db.Close()
			}
		}
	}()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: openSQLite on fresh codedb: %v", i, err)
		}
	}

	// Whichever opener created it, the schema must have landed fully and be
	// queryable from an independent connection.
	verifyDB, _, err := openSQLite(root)
	if err != nil {
		t.Fatalf("reopen after concurrent creation: %v", err)
	}
	defer func() { _ = verifyDB.Close() }()

	for _, c := range []struct{ table, column string }{
		{"blobs", "edge_version"},
		{"symbols", "signature"},
		{"blobs", "comments_parsed"},
	} {
		if !columnExists(t, verifyDB, c.table, c.column) {
			t.Errorf("%s.%s missing after concurrent CreateSchema", c.table, c.column)
		}
	}
}

// TestCreateSchema_OneTransaction holds the first opener of a new codedb inside
// its write transaction, after the whole schema is applied and before COMMIT,
// and checks what other connections see: nothing. A concurrent opener therefore
// waits for that one transaction instead of starting its own statement-by-
// statement build, and succeeds once the first commits.
//
// Failure prevented: openers interleaving dozens of schema write transactions
// until one waits past busy_timeout and the open fails with "create schema:
// database is locked".
//
// Not run with t.Parallel(): it sets testSchemaAppliedHook, which CreateSchema
// reads without a lock.
func TestCreateSchema_OneTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	dbPath := filepath.Join(t.TempDir(), MetadataDBFile)
	first, second, observer := openRawDB(t, dbPath), openRawDB(t, dbPath), openRawDB(t, dbPath)
	defer func() {
		for _, db := range []*sql.DB{first, second, observer} {
			_ = db.Close()
		}
	}()

	applied := make(chan struct{})
	release := make(chan struct{})
	var holdFirst sync.Once
	testSchemaAppliedHook = func() {
		holdFirst.Do(func() {
			close(applied)
			<-release
		})
	}
	t.Cleanup(func() { testSchemaAppliedHook = nil })

	firstErr := make(chan error, 1)
	go func() { firstErr <- CreateSchema(first) }()
	select {
	case <-applied:
	case err := <-firstErr:
		t.Fatalf("first opener returned without applying the schema in a write transaction: %v", err)
	}

	var objects int
	if err := observer.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&objects); err != nil {
		close(release)
		<-firstErr
		t.Fatalf("read schema while the first opener holds its transaction: %v", err)
	}
	if objects != 0 {
		t.Errorf("another connection saw %d schema objects before the first opener committed; the build must be one transaction", objects)
	}

	secondErr := make(chan error, 1)
	go func() { secondErr <- CreateSchema(second) }()
	close(release)
	// Collect both before failing, so no opener is still running when Cleanup
	// resets the hook.
	errFirst, errSecond := <-firstErr, <-secondErr
	if errFirst != nil {
		t.Fatalf("first opener: %v", errFirst)
	}
	if errSecond != nil {
		t.Fatalf("second opener, waiting for the first: %v", errSecond)
	}

	requireIntegrityOK(t, observer)
	if got, want := snapshotSchema(t, observer), goldenSchema(t); !reflect.DeepEqual(got, want) {
		t.Errorf("schema after both openers diverged from golden\n got=%+v\nwant=%+v", got, want)
	}
}

// TestCreateSchema_InProcessOpenersTakeTurns opens a new codedb from 16
// connections in this process at once, each with busy_timeout(0), so any wait on
// SQLite's write lock fails at once with SQLITE_BUSY. All 16 must succeed:
// openers in one process take turns on schemaMu and reach SQLite's lock one at a
// time.
//
// Failure prevented: openers in one process polling SQLite's write lock for each
// other. Every poll takes SQLite's process-wide VFS mutex, which the opener
// holding the lock needs to commit, so under load that commit can stall past the
// waiters' busy_timeout.
func TestCreateSchema_InProcessOpenersTakeTurns(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	dbPath := filepath.Join(t.TempDir(), MetadataDBFile)
	// Create the file in WAL mode, read once to build its WAL index, and keep the
	// connection open so the index stays built: an opener would otherwise convert
	// the file or recover the index, and busy_timeout(0) fails anyone waiting on
	// those, which is not what this test measures.
	keeper := openRawDB(t, dbPath)
	defer func() { _ = keeper.Close() }()
	var objects int
	if err := keeper.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&objects); err != nil {
		t.Fatalf("create %s: %v", dbPath, err)
	}

	const openers = 16
	var wg sync.WaitGroup
	errs := make([]error, openers)
	begin := make(chan struct{})
	for i := range openers {
		db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)")
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer func() { _ = db.Close() }()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			errs[i] = CreateSchema(db)
		}(i)
	}
	close(begin)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
	if got, want := snapshotSchema(t, keeper), goldenSchema(t); !reflect.DeepEqual(got, want) {
		t.Errorf("schema diverged from golden\n got=%+v\nwant=%+v", got, want)
	}
}

// TestMigrateAddTypeInfo_ResumesPartialMigration proves migrateAddTypeInfo is
// resumable. It adds three columns (signature, return_type, params); the older
// form guarded all three behind a single existence check on signature, so a
// database left with signature but not the other two — a process that crashed
// between ALTERs, or an older ox that only ever added signature — would
// short-circuit on the guard and never gain return_type/params.
//
// Failure prevented: symbols.return_type / symbols.params silently never exist,
// and every type-info query returns empty forever with no error to flag it.
func TestMigrateAddTypeInfo_ResumesPartialMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	// Simulate a migration that died after the first ALTER: signature landed,
	// return_type and params did not.
	if _, err := db.Exec(`ALTER TABLE symbols ADD COLUMN signature TEXT`); err != nil {
		t.Fatalf("seed partial migration: %v", err)
	}

	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("migrateAddTypeInfo on partially migrated schema: %v", err)
	}

	for _, col := range []string{"signature", "return_type", "params"} {
		if !columnExists(t, db, "symbols", col) {
			t.Errorf("symbols.%s missing after resuming a partial migration", col)
		}
	}
}

// TestMigrateAddTypeInfo_PreservesParsedOnReopen guards the parsed-state
// invalidation against firing on every open. migrateAddTypeInfo resets parsed=0
// to force a reparse when it first adds the type-info columns; it must NOT do so
// when the schema is already current, because CreateSchema runs on every
// Open/OpenSQLOnly — otherwise each reopen wipes parsed state and triggers a
// full, needless reparse.
//
// Failure prevented: simply reopening a codedb silently invalidates every parsed
// language blob.
func TestMigrateAddTypeInfo_PreservesParsedOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	// First migration adds the columns and (correctly) invalidates parsed.
	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Mark a language blob parsed, as a completed indexing pass would.
	if _, err := db.Exec(`INSERT INTO blobs (content_hash, language, parsed) VALUES ('h', 'go', 1)`); err != nil {
		t.Fatalf("seed parsed blob: %v", err)
	}

	// Reopen path: run the migration again against the now-current schema.
	if err := migrateAddTypeInfo(t.Context(), db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var parsed int
	if err := db.QueryRow(`SELECT parsed FROM blobs WHERE content_hash='h'`).Scan(&parsed); err != nil {
		t.Fatalf("read parsed: %v", err)
	}
	if parsed != 1 {
		t.Errorf("parsed reset to %d on a no-op reopen; must stay 1", parsed)
	}
}
