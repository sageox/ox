package store

import (
	"database/sql"
	"path/filepath"
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

	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateAddTypeInfo(db); err != nil {
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddComments(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	// insert data so we can verify it survives
	_, _ = db.Exec(`INSERT INTO blobs (content_hash, language, parsed, comments_parsed) VALUES ('x', 'go', 1, 1)`)

	if err := migrateAddComments(db); err != nil {
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
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}
	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	_, _ = db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test', 'open')`)

	if err := migrateAddGitHubTables(db); err != nil {
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
	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	if tableExists(t, db, "pr_commits") {
		t.Fatal("pr_commits should NOT exist before migration")
	}

	if err := migrateAddPRCommits(db); err != nil {
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

	if err := migrateAddGitHubTables(db); err != nil {
		t.Fatalf("migrateAddGitHubTables: %v", err)
	}

	if err := migrateAddPRCommits(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// insert data
	_, _ = db.Exec(`INSERT INTO pull_requests (number, title, state) VALUES (1, 'test', 'merged')`)
	_, _ = db.Exec(`INSERT INTO pr_commits (pr_id, sha) VALUES (1, 'sha1')`)

	if err := migrateAddPRCommits(db); err != nil {
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

	if err := migrateAddGitHubTables(db); err != nil {
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
	if err := migrateInvalidateGitHubMtimesForIssue474(db); err != nil {
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
	if err := migrateInvalidateGitHubMtimesForIssue474(db); err != nil {
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

// TestCreateSchema_ConcurrentFreshOpen reproduces #758 end-to-end: a cold
// `ox index code` racing another opener (e.g. a concurrent `OpenSQLOnly` read
// path, or two agents cold-building the same repo) against a codedb
// directory that does not exist yet. This is a real-world sanity check, not
// the regression proof — goroutine scheduling makes the exact race window
// unreliable to hit on its own (it did not reproduce #758 even once across
// 8 runs against the unfixed code during development of this test). The
// deterministic proof is TestAddColumnMigrations_ConcurrentRace below, which
// forces the window with a hook instead of hoping for it.
//
// Failure prevented (when it does land): a cold index build failing under
// concurrent openers, leaving an empty-but-schema-complete codedb that
// answers every future query with zero results instead of erroring loudly.
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
			<-start // maximize overlap on the check-then-ALTER race window
			db, err := openSQLite(root)
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

	// Regardless of which opener "won" each migration's race, the schema
	// must have landed fully and be queryable from an independent connection.
	verifyDB, err := openSQLite(root)
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

// TestAddColumnMigrations_ConcurrentRace is the deterministic regression
// proof for #758. It forces the exact race — N callers all observing a
// column as missing, then all attempting `ALTER TABLE ADD COLUMN`
// together — via testAddColumnRaceHook, rather than betting on goroutine
// scheduling to land the same microsecond-wide window on its own (see the
// non-deterministic TestCreateSchema_ConcurrentFreshOpen above, which did
// not catch this bug in 8/8 runs against the unfixed code).
//
// Covers the class of failure, not just the one reported column: every
// ALTER-based migration in schema.go shares the identical
// "SELECT exists, then ALTER" pattern, so all three are exercised here.
//
// Not run with t.Parallel(): it mutates the package-level race hook, which
// would race against other tests in this file that also touch the DB.
func TestAddColumnMigrations_ConcurrentRace(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	cases := []struct {
		name    string
		migrate func(*sql.DB) error
		table   string
		columns []string
		// syncColumn is the one column each racer parks on to force the race.
		// migrateAddTypeInfo adds three columns via addColumnIfMissing, so the
		// hook fires once per column; parking on only the first keeps the
		// bounded ready channel from overflowing while still guaranteeing all
		// racers hit the check-then-ALTER window together.
		syncColumn string
	}{
		{"edge_version", migrateAddEdgeVersion, "blobs", []string{"edge_version"}, "edge_version"},
		{"type_info", migrateAddTypeInfo, "symbols", []string{"signature", "return_type", "params"}, "signature"},
		{"comments_parsed", migrateAddComments, "blobs", []string{"comments_parsed"}, "comments_parsed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := createOldSchemaDB(t)
			defer func() { _ = db.Close() }()

			const racers = 8
			ready := make(chan struct{}, racers)
			release := make(chan struct{})

			testAddColumnRaceHook = func(column string) {
				if column != tc.syncColumn {
					return
				}
				ready <- struct{}{}
				<-release
			}
			t.Cleanup(func() { testAddColumnRaceHook = nil })

			var wg sync.WaitGroup
			errs := make([]error, racers)
			for i := range racers {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					errs[i] = tc.migrate(db)
				}(i)
			}

			// Block until every racer has observed the column missing and is
			// parked in the hook, then release them all into the ALTER at once —
			// guarantees the race window is hit, not just hoped for.
			for range racers {
				<-ready
			}
			close(release)
			wg.Wait()

			for i, err := range errs {
				if err != nil {
					t.Errorf("racer %d: %s: %v (a losing ALTER must be swallowed as success, not returned)", i, tc.name, err)
				}
			}
			for _, col := range tc.columns {
				if !columnExists(t, db, tc.table, col) {
					t.Errorf("%s.%s missing after concurrent migration", tc.table, col)
				}
			}
		})
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

	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("migrateAddTypeInfo on partially migrated schema: %v", err)
	}

	for _, col := range []string{"signature", "return_type", "params"} {
		if !columnExists(t, db, "symbols", col) {
			t.Errorf("symbols.%s missing after resuming a partial migration", col)
		}
	}
}

// TestIsDuplicateColumnError pins the precision of the duplicate-column guard:
// it must swallow a duplicate ONLY for the exact column being added, so a
// genuine schema bug that reports a duplicate on a different column, or any
// other error, still fails the migration loudly.
//
// Failure prevented: an over-broad match (e.g. plain substring on the message)
// silently eating a real error and leaving a half-built schema.
func TestIsDuplicateColumnError(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite migration")
	}

	db, _ := createOldSchemaDB(t)
	defer func() { _ = db.Close() }()

	// Provoke a real "duplicate column name: signature" from the driver.
	if _, err := db.Exec(`ALTER TABLE symbols ADD COLUMN signature TEXT`); err != nil {
		t.Fatalf("seed column: %v", err)
	}
	_, dupErr := db.Exec(`ALTER TABLE symbols ADD COLUMN signature TEXT`)
	if dupErr == nil {
		t.Fatal("expected a duplicate-column error on the second ALTER")
	}
	// A different sqlite error (same generic SQLITE_ERROR code, different message)
	// must not be mistaken for a duplicate-column error.
	_, missingTableErr := db.Exec(`ALTER TABLE definitely_no_such_table ADD COLUMN x TEXT`)
	if missingTableErr == nil {
		t.Fatal("expected an error for a missing table")
	}

	cases := []struct {
		name   string
		err    error
		column string
		want   bool
	}{
		{"matches the exact column", dupErr, "signature", true},
		{"rejects a different column", dupErr, "return_type", false},
		// "sign" is a prefix of "signature": a substring match would wrongly
		// accept it, silently swallowing a real duplicate on another column.
		{"rejects a column that is a prefix of the real one", dupErr, "sign", false},
		{"rejects nil", nil, "signature", false},
		{"rejects a non-duplicate sqlite error", missingTableErr, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDuplicateColumnError(tc.err, tc.column); got != tc.want {
				t.Errorf("isDuplicateColumnError(%v, %q) = %v, want %v", tc.err, tc.column, got, tc.want)
			}
		})
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
	if err := migrateAddTypeInfo(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Mark a language blob parsed, as a completed indexing pass would.
	if _, err := db.Exec(`INSERT INTO blobs (content_hash, language, parsed) VALUES ('h', 'go', 1)`); err != nil {
		t.Fatalf("seed parsed blob: %v", err)
	}

	// Reopen path: run the migration again against the now-current schema.
	if err := migrateAddTypeInfo(db); err != nil {
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
