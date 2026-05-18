package codedb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/codedb/store"
)

// DB is the top-level CodeDB facade.
type DB struct {
	store *store.Store
}

// Open opens (or creates) a CodeDB at the given root directory.
func Open(root string) (*DB, error) {
	s, err := store.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open codedb store: %w", err)
	}
	return &DB{store: s}, nil
}

// OpenSQLOnly opens a CodeDB without touching its bleve sub-indexes.
// Use for read paths that only query SQL data (insights, status counters) so
// they keep working when bleve is mid-rebuild, locked by an active writer, or
// being self-healed after corruption.
//
// IMPORTANT: callers MUST NOT use Search, IndexRepo, IndexLocalRepo,
// BuildDirtyIndex, or any dirty-overlay API on a SQL-only DB — those depend
// on bleve and will dereference nil. SQL convenience methods (Query, QueryRow,
// Exec, RawSQL) are safe.
func OpenSQLOnly(root string) (*DB, error) {
	s, err := store.OpenSQLOnly(root)
	if err != nil {
		return nil, fmt.Errorf("open codedb store: %w", err)
	}
	return &DB{store: s}, nil
}

// Close releases all resources.
func (db *DB) Close() error {
	return db.store.Close()
}

// Store returns the underlying store for direct access.
func (db *DB) Store() *store.Store {
	return db.store
}

// IndexRepo clones/fetches and indexes a git repository.
func (db *DB) IndexRepo(ctx context.Context, url string, opts index.IndexOptions) error {
	return index.IndexRepo(ctx, db.store, url, opts)
}

// IndexLocalRepo indexes a local git repository's committed content.
func (db *DB) IndexLocalRepo(ctx context.Context, localPath string, opts index.IndexOptions) error {
	return index.IndexLocalRepo(ctx, db.store, localPath, opts)
}

// BuildDirtyIndex builds an on-disk Bleve index of dirty (uncommitted) files.
// Called by the daemon after committed content indexing.
func (db *DB) BuildDirtyIndex(ctx context.Context, localPath string, opts index.IndexOptions) (int, error) {
	dirtyPath := index.DirtyIndexPath(db.store.Root, localPath)
	return index.BuildDirtyIndex(ctx, localPath, dirtyPath, opts)
}

// AttachDirtyIndex opens the daemon-built on-disk dirty overlay and aliases it
// with the shared CodeIndex for transparent search.
// Uses a default key; for multi-worktree support use AttachDirtyIndexByID.
func (db *DB) AttachDirtyIndex(worktreePath string) error {
	dirtyPath := index.DirtyIndexPath(db.store.Root, worktreePath)
	return db.store.AttachDirtyIndex(dirtyPath)
}

// AttachDirtyIndexByID opens an on-disk dirty overlay by worktree ID and path.
// Multiple overlays can be attached simultaneously; all are merged at query time.
func (db *DB) AttachDirtyIndexByID(id, dirtyBlevePath string) error {
	return db.store.AttachDirtyIndexByID(id, dirtyBlevePath)
}

// DetachDirtyIndexByID removes a specific dirty overlay by ID.
func (db *DB) DetachDirtyIndexByID(id string) {
	db.store.DetachDirtyIndexByID(id)
}

// DirtyOverlayCount returns the number of currently attached dirty overlays.
func (db *DB) DirtyOverlayCount() int {
	return db.store.DirtyOverlayCount()
}

// AttachDirtyOverlay creates an in-memory Bleve overlay for dirty worktree files.
// Primarily used in tests; production uses AttachDirtyIndex for on-disk overlays.
func (db *DB) AttachDirtyOverlay() error {
	return db.store.AttachDirtyOverlay()
}

// DetachDirtyOverlay closes all attached dirty overlays.
func (db *DB) DetachDirtyOverlay() {
	db.store.DetachDirtyOverlay()
}

// AttachAllDirtyIndexes scans the dirty index directory for manifest files and
// attaches all valid dirty overlays by worktree ID. This gives CLI searches
// access to all active worktree overlays simultaneously.
func (db *DB) AttachAllDirtyIndexes() int {
	return index.AttachAllDirtyIndexes(db.store)
}

// GCDirtyIndexes removes stale dirty overlay directories for worktrees that no
// longer exist on disk. Returns the number of overlays removed.
func (db *DB) GCDirtyIndexes() (int, error) {
	return index.GCDirtyIndexes(db.store.Root)
}

// ParseSymbols extracts symbols from all unparsed blobs with supported languages.
func (db *DB) ParseSymbols(ctx context.Context, progress func(string)) (index.ParseStats, error) {
	return index.ParseSymbols(ctx, db.store, index.ProgressFunc(progress))
}

// ParseComments extracts comments from all unparsed blobs with supported languages.
func (db *DB) ParseComments(ctx context.Context, progress func(string)) (index.CommentStats, error) {
	return index.ParseComments(ctx, db.store, index.ProgressFunc(progress))
}

// IndexGitHubData reads PR/issue JSON files from the ledger and indexes them into CodeDB.
func (db *DB) IndexGitHubData(ctx context.Context, ledgerPath string, progress func(string)) (*index.GitHubIndexStats, error) {
	return index.IndexGitHubData(ctx, db.store, ledgerPath, index.ProgressFunc(progress))
}

// Search parses and executes a query.
func (db *DB) Search(ctx context.Context, input string) ([]search.Result, error) {
	query, err := search.ParseQuery(input)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	return search.Execute(ctx, db.store, query)
}

// TranslateQuery parses a query and returns the generated SQL without executing.
func (db *DB) TranslateQuery(input string) (*search.TranslatedQuery, error) {
	query, err := search.ParseQuery(input)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	return search.Translate(query)
}

// RawSQL executes a raw SQL query and returns results as column-value pairs.
func (db *DB) RawSQL(query string) ([]string, [][]string, error) {
	rows, err := db.store.Query(query)
	if err != nil {
		return nil, nil, fmt.Errorf("execute sql: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("get columns: %w", err)
	}

	var results [][]string
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			slog.Warn("raw sql scan error, skipping row", "err", err)
			continue
		}
		row := make([]string, len(cols))
		for i, v := range values {
			if v.Valid {
				row[i] = v.String
			} else {
				row[i] = "NULL"
			}
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return cols, results, fmt.Errorf("iterate rows: %w", err)
	}

	return cols, results, nil
}
