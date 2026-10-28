package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"github.com/sageox/ox/internal/sqliteerr"
)

// columnDDL pairs a column name with the ALTER statement that adds it.
type columnDDL struct {
	name string
	ddl  string
}

// addColumnsIfMissing adds each of cols to table that is not already present.
// It reads the current column set once, then issues ALTERs only for the columns
// still missing. This makes a multi-column migration resumable: an older ox ran
// each ALTER as its own transaction, so one that crashed partway left some
// columns added, and the next run adds the rest instead of a single guard
// column short-circuiting the whole migration.
//
// addColumnsIfMissing reports whether it added any column (added), which callers
// use to gate one-shot backfills that must run only when the migration did real
// work — not on every reopen of an already-current schema.
func addColumnsIfMissing(ctx context.Context, q codedbsqlc.DBTX, table string, cols []columnDDL) (added bool, err error) {
	existing, err := existingColumns(ctx, q, table)
	if err != nil {
		return false, err
	}
	for _, c := range cols {
		if existing[c.name] {
			continue
		}
		if _, err := q.ExecContext(ctx, c.ddl); err != nil {
			return added, fmt.Errorf("add column %s.%s: %w", table, c.name, err)
		}
		added = true
	}
	return added, nil
}

// existingColumns returns the set of column names currently defined on table.
func existingColumns(ctx context.Context, q codedbsqlc.DBTX, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
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

// CreateSchema brings db's schema up to date: the tables and indexes in
// schemaDDL, then every migration in applySchema.
//
// It first runs applySchema on a query_only connection, which never takes the
// write lock. On a current schema every statement is a guarded no-op, so the pass
// succeeds and the open does not queue behind a writer such as the daemon's
// indexer. When a statement would write, SQLite refuses it with SQLITE_READONLY,
// and only then does CreateSchema take the write lock, once, and run applySchema
// again in a single BEGIN IMMEDIATE transaction. (A connection whose cached
// schema predates another opener's commit can be refused for a statement that is
// now a no-op; it takes the write path and commits nothing.)
//
// The single transaction replaces a convoy. While each statement was its own
// transaction, every opener took the write lock for each statement it found
// missing, so concurrent openers of a new or outdated codedb handed the lock back
// and forth dozens of times each, and under load one wait outlasted busy_timeout:
// "create schema: database is locked". Now an opener takes the lock at most once,
// and one that gets it after another opener committed finds every statement a
// no-op.
func CreateSchema(db *sql.DB) error {
	ctx := context.Background()
	needsWrite := false
	err := withConn(ctx, db, []pragmaOverride{queryOnly}, func(conn *sql.Conn) error {
		err := applySchema(ctx, conn)
		if sqliteerr.IsReadOnly(err) {
			needsWrite = true
			return nil
		}
		return err
	})
	if err != nil || !needsWrite {
		return err
	}
	schemaMu.Lock()
	defer schemaMu.Unlock()
	return withConn(ctx, db, nil, func(conn *sql.Conn) error {
		return inImmediateTx(ctx, conn, func() error {
			if err := applySchema(ctx, conn); err != nil {
				return err
			}
			if testSchemaAppliedHook != nil {
				testSchemaAppliedHook()
			}
			return nil
		})
	})
}

// schemaMu makes openers in this process take turns at the write lock for a
// schema change, of any codedb. Waiting for SQLite's write lock means polling
// it, each poll takes SQLite's process-wide VFS mutex, and the opener holding the
// write lock needs that same mutex to commit, so many connections in one process
// waiting on each other can stall that commit past their busy_timeout. Openers in
// other processes still meet at SQLite's lock, one per process.
var schemaMu sync.Mutex

// testSchemaAppliedHook, when non-nil, runs inside CreateSchema's write
// transaction after applySchema and before COMMIT, so a test can hold the
// transaction open and inspect what other connections see. CreateSchema reads
// it without a lock: set it only from tests that do not call t.Parallel().
var testSchemaAppliedHook func()

// applySchema runs schemaDDL and then every migration, oldest first.
// "Migrations" include both schema migrations (idempotent ALTER/CREATE) and
// one-shot data repairs that recover from historical bugs (see
// migrateInvalidateGitHubMtimesForIssue474).
//
// Every statement is guarded (CREATE ... IF NOT EXISTS, a table or column
// existence check, a sentinel row), so against a current schema none of them
// writes. CreateSchema's query_only pass and the read-only open
// (openSQLiteReadOnly) both rely on that. Append new migrations at the end with
// their own guard: one that writes unconditionally would make every open take
// the write lock.
func applySchema(ctx context.Context, q codedbsqlc.DBTX) error {
	if _, err := q.ExecContext(ctx, schemaDDL); err != nil {
		return err
	}
	if err := migrateAddTypeInfo(ctx, q); err != nil {
		return err
	}
	if err := migrateAddComments(ctx, q); err != nil {
		return err
	}
	if err := migrateAddGitHubTables(ctx, q); err != nil {
		return err
	}
	if err := migrateAddPRCommits(ctx, q); err != nil {
		return err
	}
	if err := migrateAddEdgeVersion(ctx, q); err != nil {
		return err
	}
	return migrateInvalidateGitHubMtimesForIssue474(ctx, q)
}

// migrateAddEdgeVersion adds the blobs.edge_version column used by the ADR-019
// resolver. Existing rows default to 0; the backfill routine bumps them to the
// current resolver version after edges are populated, and ParseSymbols stamps
// new blobs at the current version inline.
func migrateAddEdgeVersion(ctx context.Context, q codedbsqlc.DBTX) error {
	if _, err := addColumnsIfMissing(ctx, q, "blobs", []columnDDL{
		{"edge_version", `ALTER TABLE blobs ADD COLUMN edge_version INTEGER NOT NULL DEFAULT 0`},
	}); err != nil {
		return err
	}
	// Index supports the backfill scan: find blobs needing edges fast.
	_, err := q.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_blobs_edge_version ON blobs(edge_version)`)
	return err
}

// migrateAddTypeInfo adds signature, return_type, and params columns to the
// symbols table for databases created before those columns existed.
func migrateAddTypeInfo(ctx context.Context, q codedbsqlc.DBTX) error {
	// Each column is added independently (see addColumnsIfMissing) so the
	// migration is resumable: the previous single-guard-on-signature form left
	// return_type/params permanently absent if a process died (or was an older
	// ox) after only the first ALTER landed.
	added, err := addColumnsIfMissing(ctx, q, "symbols", []columnDDL{
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
	if _, err := q.ExecContext(ctx, `UPDATE blobs SET parsed = 0 WHERE language IS NOT NULL`); err != nil {
		return fmt.Errorf("invalidate parsed blobs after type-info migration: %w", err)
	}
	return nil
}

// migrateAddComments creates the comments table and adds the comments_parsed
// column to blobs for databases created before comment indexing existed.
func migrateAddComments(ctx context.Context, q codedbsqlc.DBTX) error {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='comments'`).Scan(&exists)
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
			if _, err := q.ExecContext(ctx, s); err != nil {
				return err
			}
		}
	}

	// add comments_parsed column to blobs if missing
	if _, err := addColumnsIfMissing(ctx, q, "blobs", []columnDDL{
		{"comments_parsed", `ALTER TABLE blobs ADD COLUMN comments_parsed INTEGER NOT NULL DEFAULT 0`},
	}); err != nil {
		return err
	}

	// index for fast unparsed-comments lookups
	if _, err := q.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_blobs_comments_parsed ON blobs(comments_parsed)`); err != nil {
		return err
	}

	return nil
}

// migrateAddGitHubTables adds pull_requests, pr_comments, issues, and
// issue_comments tables for databases created before GitHub data indexing.
func migrateAddGitHubTables(ctx context.Context, q codedbsqlc.DBTX) error {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='pull_requests'`).Scan(&exists)
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
		if _, err := q.ExecContext(ctx, s); err != nil {
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
// CreateSchema writes only inside its one transaction, so the DELETE and the
// sentinel INSERT commit together: a crash between them can't strand the cache
// in a "deleted but not marked done" state, which would otherwise wipe the
// freshly-rebuilt cache on every subsequent Open().
func migrateInvalidateGitHubMtimesForIssue474(ctx context.Context, q codedbsqlc.DBTX) error {
	const sentinelPath = "__migration_474_indexer_lex_order__"

	// idempotent: skip if sentinel already present
	var done bool
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) > 0 FROM github_file_mtimes WHERE source_path = ?`,
		sentinelPath,
	).Scan(&done)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	if _, err := q.ExecContext(ctx, `DELETE FROM github_file_mtimes`); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO github_file_mtimes (source_path, mtime_unix) VALUES (?, 0)`,
		sentinelPath,
	)
	return err
}

// migrateAddPRCommits creates the pr_commits table for databases created
// before PR commit indexing existed.
func migrateAddPRCommits(ctx context.Context, q codedbsqlc.DBTX) error {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='pr_commits'`).Scan(&exists)
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
		if _, err := q.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
