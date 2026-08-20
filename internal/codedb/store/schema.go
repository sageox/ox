package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// isDuplicateColumnError reports whether err is SQLite's "duplicate column
// name: <column>" error for the exact column just attempted — the signature a
// losing `ALTER TABLE ADD COLUMN` gets when a concurrent caller already added
// that column. SQLite has no `ADD COLUMN IF NOT EXISTS`, so every migration
// checks column existence and then ALTERs in two separate statements; on a
// brand-new database two openers (e.g. `ox index code` racing another
// `Open`/`OpenSQLOnly` of the same freshly created codedb — see #758) can both
// observe the column missing and both attempt the ALTER. The loser's failure
// here means the column exists now, which is exactly what was wanted, so it must
// not fail the whole CreateSchema call.
//
// The match is deliberately narrow: a typed *sqlite.Error carrying the generic
// SQLITE_ERROR code whose message names the column we tried to add. Requiring
// the column name stops a duplicate on some *other* column (a real schema bug)
// from being silently swallowed; the type+code check stops an unrelated error
// whose text merely contains the phrase from slipping through.
func isDuplicateColumnError(err error, column string) bool {
	var sqErr *sqlite.Error
	if !errors.As(err, &sqErr) || sqErr.Code() != sqlite3.SQLITE_ERROR {
		return false
	}
	// SQLite formats this as "duplicate column name: <col>" (modernc appends a
	// trailing " (code)"). Compare the column exactly — a substring check would
	// let "sign" match "signature".
	const marker = "duplicate column name: "
	msg := sqErr.Error()
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return false
	}
	reported := msg[idx+len(marker):]
	if end := strings.IndexAny(reported, " \t\r\n"); end >= 0 {
		reported = reported[:end]
	}
	return reported == column
}

// columnDDL pairs a column name with the ALTER statement that adds it.
type columnDDL struct {
	name string
	ddl  string
}

// addColumnsIfMissing adds each of cols to table that is not already present.
// It reads the current column set once, then issues ALTERs only for the columns
// still missing. This makes a multi-column migration resumable: a process that
// crashed (or an older ox) after adding only some of its columns adds the rest
// on the next run, instead of a single guard column short-circuiting the whole
// migration and leaving the schema permanently partial.
//
// The single up-front read is deliberate. Checking each column immediately
// before its own ALTER interleaves schema reads with the concurrent ALTERs of
// other openers on a fresh codedb, and that contention corrupted a 16-way
// fresh-open race in testing. Reading once keeps the hot path as cheap as the
// original single guarded check while still recovering a partial schema.
//
// The check-then-ALTER is not atomic, so two openers can both observe a column
// missing and both ALTER; the loser's "duplicate column name" is success, not
// failure (see isDuplicateColumnError and #758).
// addColumnsIfMissing reports whether it added any column (added), which callers
// use to gate one-shot backfills that must run only when the migration did real
// work — not on every reopen of an already-current schema.
func addColumnsIfMissing(db *sql.DB, table string, cols []columnDDL) (added bool, err error) {
	existing, err := existingColumns(db, table)
	if err != nil {
		return false, err
	}
	for _, c := range cols {
		if existing[c.name] {
			continue
		}
		// A column observed missing here is being applied by this call, whether
		// our own ALTER wins or a concurrent caller's does; either way the
		// migration is doing real work.
		added = true
		addColumnRaceHook(c.name)
		if _, execErr := db.Exec(c.ddl); execErr != nil && !isDuplicateColumnError(execErr, c.name) {
			return added, fmt.Errorf("add column %s.%s: %w", table, c.name, execErr)
		}
	}
	return added, nil
}

// existingColumns returns the set of column names currently defined on table.
func existingColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan column of %s: %w", table, err)
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// testAddColumnRaceHook, when non-nil, is invoked by addColumnsIfMissing
// immediately before it attempts the ALTER for a still-missing column, passed
// that column's name. Production always leaves this nil
// (a lock-free read of a package global, effectively zero cost). Tests use it to
// force the #758 race window deterministically — pausing N goroutines right
// after they have all observed "missing" and releasing them together — rather
// than betting on goroutine scheduling to hit a microsecond-wide window.
//
// It MUST only ever be set from a test that does NOT call t.Parallel():
// production reads this global lock-free from concurrent migration goroutines,
// so a parallel test that writes it while another test's migrations read it is a
// data race on this variable.
var testAddColumnRaceHook func(column string)

func addColumnRaceHook(column string) {
	if testAddColumnRaceHook != nil {
		testAddColumnRaceHook(column)
	}
}

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

-- ADR-019: resolved symbol edges. Populated alongside symbol_refs at index
-- time by per-language resolvers. src_symbol_id is the *containing* symbol
-- (caller); dst_symbol_id is the *resolved target* (callee) when known.
-- dst_blob_id/dst_symbol_id are NULL for unresolved/external targets (the
-- dst_name column always carries the referenced name so name-fallback queries
-- still work). confidence: extracted (direct binding), inferred (heuristic
-- match), ambiguous (one row per candidate, capped).
CREATE TABLE IF NOT EXISTS symbol_edges (
    id            INTEGER PRIMARY KEY,
    src_blob_id   INTEGER NOT NULL REFERENCES blobs(id),
    src_symbol_id INTEGER NOT NULL REFERENCES symbols(id),
    dst_blob_id   INTEGER          REFERENCES blobs(id),
    dst_symbol_id INTEGER          REFERENCES symbols(id),
    dst_name      TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    confidence    TEXT    NOT NULL,
    line          INTEGER NOT NULL,
    col           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_symbol_edges_src      ON symbol_edges(src_symbol_id, kind);
CREATE INDEX IF NOT EXISTS idx_symbol_edges_dst      ON symbol_edges(dst_symbol_id, kind);
CREATE INDEX IF NOT EXISTS idx_symbol_edges_dst_name ON symbol_edges(dst_name, kind);

CREATE TABLE IF NOT EXISTS pull_requests (
    id          INTEGER PRIMARY KEY,
    number      INTEGER NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    body        TEXT,
    author      TEXT,
    state       TEXT NOT NULL,
    labels      TEXT,
    created_at  INTEGER,
    merged_at   INTEGER,
    closed_at   INTEGER,
    updated_at  INTEGER,
    merge_commit TEXT,
    url         TEXT,
    source_path TEXT
);

CREATE TABLE IF NOT EXISTS pr_comments (
    id    INTEGER PRIMARY KEY,
    pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
    author TEXT,
    body   TEXT,
    path   TEXT,
    line   INTEGER,
    created_at INTEGER
);

CREATE TABLE IF NOT EXISTS issues (
    id          INTEGER PRIMARY KEY,
    number      INTEGER NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    body        TEXT,
    author      TEXT,
    state       TEXT NOT NULL,
    labels      TEXT,
    created_at  INTEGER,
    closed_at   INTEGER,
    updated_at  INTEGER,
    url         TEXT,
    source_path TEXT
);

CREATE TABLE IF NOT EXISTS issue_comments (
    id       INTEGER PRIMARY KEY,
    issue_id INTEGER NOT NULL REFERENCES issues(id),
    author   TEXT,
    body     TEXT,
    created_at INTEGER
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
CREATE INDEX IF NOT EXISTS idx_pull_requests_number ON pull_requests(number);
CREATE INDEX IF NOT EXISTS idx_pull_requests_state ON pull_requests(state);
CREATE INDEX IF NOT EXISTS idx_pull_requests_author ON pull_requests(author);
CREATE INDEX IF NOT EXISTS idx_pr_comments_pr ON pr_comments(pr_id);
CREATE INDEX IF NOT EXISTS idx_issues_number ON issues(number);
CREATE INDEX IF NOT EXISTS idx_issues_state ON issues(state);
CREATE INDEX IF NOT EXISTS idx_issues_author ON issues(author);
CREATE INDEX IF NOT EXISTS idx_issue_comments_issue ON issue_comments(issue_id);
CREATE INDEX IF NOT EXISTS idx_blobs_parsed_lang ON blobs(parsed, language);

CREATE TABLE IF NOT EXISTS pr_commits (
    id    INTEGER PRIMARY KEY,
    pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
    sha   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_pr_commits_pr ON pr_commits(pr_id);
CREATE INDEX IF NOT EXISTS idx_pr_commits_sha ON pr_commits(sha);

CREATE TABLE IF NOT EXISTS github_file_mtimes (
    source_path TEXT NOT NULL PRIMARY KEY,
    mtime_unix  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_pull_requests_updated_at ON pull_requests(updated_at);
CREATE INDEX IF NOT EXISTS idx_pull_requests_merged_at ON pull_requests(merged_at);
CREATE INDEX IF NOT EXISTS idx_pull_requests_created_at ON pull_requests(created_at);
CREATE INDEX IF NOT EXISTS idx_issues_updated_at ON issues(updated_at);
CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at);
CREATE INDEX IF NOT EXISTS idx_commits_timestamp ON commits(timestamp);
`

// CreateSchema initializes the SQLite tables and indexes, and runs all
// pending migrations. "Migrations" here include both schema migrations
// (idempotent ALTER/CREATE) and one-shot data repairs that recover from
// historical bugs (see migrateInvalidateGitHubMtimesForIssue474). New
// migrations should be appended at the end and gated by their own
// idempotency check (column existence, sentinel row, etc.).
func CreateSchema(db *sql.DB) error {
	_, err := db.Exec(schemaDDL)
	if err != nil {
		return err
	}
	if err := migrateAddTypeInfo(db); err != nil {
		return err
	}
	if err := migrateAddComments(db); err != nil {
		return err
	}
	if err := migrateAddGitHubTables(db); err != nil {
		return err
	}
	if err := migrateAddPRCommits(db); err != nil {
		return err
	}
	if err := migrateAddEdgeVersion(db); err != nil {
		return err
	}
	return migrateInvalidateGitHubMtimesForIssue474(db)
}

// migrateAddEdgeVersion adds the blobs.edge_version column used by the ADR-019
// resolver. Existing rows default to 0; the backfill routine bumps them to the
// current resolver version after edges are populated, and ParseSymbols stamps
// new blobs at the current version inline.
func migrateAddEdgeVersion(db *sql.DB) error {
	if _, err := addColumnsIfMissing(db, "blobs", []columnDDL{
		{"edge_version", `ALTER TABLE blobs ADD COLUMN edge_version INTEGER NOT NULL DEFAULT 0`},
	}); err != nil {
		return err
	}
	// Index supports the backfill scan: find blobs needing edges fast.
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_blobs_edge_version ON blobs(edge_version)`)
	return err
}

// migrateAddTypeInfo adds signature, return_type, and params columns to the
// symbols table for databases created before those columns existed.
func migrateAddTypeInfo(db *sql.DB) error {
	// Each column is added independently (see addColumnsIfMissing) so the
	// migration is resumable: the previous single-guard-on-signature form left
	// return_type/params permanently absent if a process died (or was an older
	// ox) after only the first ALTER landed.
	added, err := addColumnsIfMissing(db, "symbols", []columnDDL{
		{"signature", `ALTER TABLE symbols ADD COLUMN signature TEXT`},
		{"return_type", `ALTER TABLE symbols ADD COLUMN return_type TEXT`},
		{"params", `ALTER TABLE symbols ADD COLUMN params TEXT`},
	})
	if err != nil {
		return err
	}
	// Invalidate parsed state only when a type-info column was actually added
	// (first migration, or a resumed partial one) so existing blobs get reparsed
	// to populate it. CreateSchema runs on every Open/OpenSQLOnly, so doing this
	// unconditionally would reset parsed=0 for every language blob on each
	// reopen and force a needless full reparse.
	if !added {
		return nil
	}
	if _, err := db.Exec(`UPDATE blobs SET parsed = 0 WHERE language IS NOT NULL`); err != nil {
		return fmt.Errorf("invalidate parsed blobs after type-info migration: %w", err)
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
	if _, err := addColumnsIfMissing(db, "blobs", []columnDDL{
		{"comments_parsed", `ALTER TABLE blobs ADD COLUMN comments_parsed INTEGER NOT NULL DEFAULT 0`},
	}); err != nil {
		return err
	}

	// index for fast unparsed-comments lookups
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_blobs_comments_parsed ON blobs(comments_parsed)`); err != nil {
		return err
	}

	return nil
}

// migrateAddGitHubTables adds pull_requests, pr_comments, issues, and
// issue_comments tables for databases created before GitHub data indexing.
func migrateAddGitHubTables(db *sql.DB) error {
	var exists bool
	err := db.QueryRow(`SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='pull_requests'`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pull_requests (
			id          INTEGER PRIMARY KEY,
			number      INTEGER NOT NULL UNIQUE,
			title       TEXT NOT NULL,
			body        TEXT,
			author      TEXT,
			state       TEXT NOT NULL,
			labels      TEXT,
			created_at  INTEGER,
			merged_at   INTEGER,
			closed_at   INTEGER,
			updated_at  INTEGER,
			merge_commit TEXT,
			url         TEXT,
			source_path TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS pr_comments (
			id    INTEGER PRIMARY KEY,
			pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
			author TEXT,
			body   TEXT,
			path   TEXT,
			line   INTEGER,
			created_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS issues (
			id          INTEGER PRIMARY KEY,
			number      INTEGER NOT NULL UNIQUE,
			title       TEXT NOT NULL,
			body        TEXT,
			author      TEXT,
			state       TEXT NOT NULL,
			labels      TEXT,
			created_at  INTEGER,
			closed_at   INTEGER,
			updated_at  INTEGER,
			url         TEXT,
			source_path TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS issue_comments (
			id       INTEGER PRIMARY KEY,
			issue_id INTEGER NOT NULL REFERENCES issues(id),
			author   TEXT,
			body     TEXT,
			created_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_number ON pull_requests(number)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_state ON pull_requests(state)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_author ON pull_requests(author)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_comments_pr ON pr_comments(pr_id)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_number ON issues(number)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_state ON issues(state)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_author ON issues(author)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_comments_issue ON issue_comments(issue_id)`,
		`CREATE TABLE IF NOT EXISTS github_file_mtimes (
			source_path TEXT NOT NULL PRIMARY KEY,
			mtime_unix  INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_updated_at ON pull_requests(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_merged_at ON pull_requests(merged_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pull_requests_created_at ON pull_requests(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_updated_at ON issues(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_commits_timestamp ON commits(timestamp)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// migrateInvalidateGitHubMtimesForIssue474 forces a one-time full reindex of
// the GitHub data tables after the indexer's snapshot-selection logic was fixed
// in issue #474. The previous indexer iterated content-hashed PR/issue snapshots
// in lex order and let the last upsert win, which silently picked stale
// snapshots when their hash lex-sorted after the chronologically-latest one.
//
// The new indexer groups snapshots by object number and picks the latest by
// updated_at — but it skips groups whose file mtimes haven't changed. Existing
// caches have all mtimes recorded, so without this migration the corrupted
// rows would persist until a new snapshot lands. Clearing github_file_mtimes
// once forces the next IndexGitHubData call to re-pick every group.
//
// The migration marks itself done with a sentinel row so it runs exactly once
// per database. The sentinel uses an invalid filesystem path so it can't
// collide with a real source_path.
//
// The DELETE and sentinel INSERT run inside a transaction so a crash between
// them can't strand the cache in a "deleted but not marked done" state, which
// would otherwise wipe the freshly-rebuilt cache on every subsequent Open().
func migrateInvalidateGitHubMtimesForIssue474(db *sql.DB) error {
	const sentinelPath = "__migration_474_indexer_lex_order__"

	// idempotent: skip if sentinel already present
	var done bool
	err := db.QueryRow(
		`SELECT COUNT(*) > 0 FROM github_file_mtimes WHERE source_path = ?`,
		sentinelPath,
	).Scan(&done)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after successful commit is a no-op

	if _, err := tx.Exec(`DELETE FROM github_file_mtimes`); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO github_file_mtimes (source_path, mtime_unix) VALUES (?, 0)`,
		sentinelPath,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateAddPRCommits creates the pr_commits table for databases created
// before PR commit indexing existed.
func migrateAddPRCommits(db *sql.DB) error {
	var exists bool
	err := db.QueryRow(`SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='pr_commits'`).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS pr_commits (
			id    INTEGER PRIMARY KEY,
			pr_id INTEGER NOT NULL REFERENCES pull_requests(id),
			sha   TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_commits_pr ON pr_commits(pr_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_commits_sha ON pr_commits(sha)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
