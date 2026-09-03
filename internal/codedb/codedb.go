package codedb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/codedb/store"
)

// renameDir indirects os.Rename so tests can inject a promotion failure and
// verify the previous cache survives (BuildCodeDBAtomic).
var renameDir = os.Rename

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

// ReadOnly reports whether the codedb was opened from media this process cannot
// write. Reads work; indexing is refused.
func (db *DB) ReadOnly() bool {
	return db.store.ReadOnly
}

// IndexRepo clones/fetches and indexes a git repository.
func (db *DB) IndexRepo(ctx context.Context, url string, opts index.IndexOptions) error {
	return index.IndexRepo(ctx, db.store, url, opts)
}

// IndexLocalRepo indexes a local git repository's committed content.
func (db *DB) IndexLocalRepo(ctx context.Context, localPath string, opts index.IndexOptions) error {
	return index.IndexLocalRepo(ctx, db.store, localPath, opts)
}

// OpenIndexWithHeal opens the codedb at dataDir and runs indexFn against it. If
// indexFn fails with an on-disk corruption error (see index.IsCorruptionError)
// — the failure class that otherwise makes every subsequent `ox index` fail
// identically against the same half-written cache, i.e. a permanent crash loop
// — it discards the codedb cache and retries indexFn ONCE from a clean
// directory. This is the indexing-pass analog of the git checkout's
// discard-and-reclone recovery.
//
// The returned *DB is left open so the caller can run follow-up pipeline stages
// (ParseSymbols/ParseComments) against the healed store; the caller owns Close.
// A non-corruption failure (context cancellation/deadline, alternates
// unsupported, disk error, …) is returned as-is WITHOUT discarding anything.
// One retry only: the discard clears the corrupt state, so a second identical
// failure is a genuine error, not a loop to keep spinning on.
func OpenIndexWithHeal(ctx context.Context, dataDir string, indexFn func(context.Context, *DB) error) (*DB, error) {
	db, err := Open(dataDir)
	if err != nil {
		return nil, err
	}

	// Every recovery below this point writes: the marker escalation and the
	// corruption retry both wipe dataDir, and indexFn itself only writes. On a
	// read-only store none of that is possible, so say so once here instead of
	// failing partway through a pass.
	if db.ReadOnly() {
		_ = db.Close()
		return nil, fmt.Errorf("cannot index into %s: %w", dataDir, store.ErrReadOnly)
	}

	// Open self-heals a structurally-corrupt bleve sub-index transparently: it
	// empties the sub-index and writes a .needs_reindex marker. Incremental
	// indexing would then skip the commits already recorded in SQLite and leave
	// the emptied sub-index permanently empty — a silent empty search, worse than
	// a hard error. When a marker is present, escalate to a from-scratch rebuild
	// (wiping dataDir clears the stale SQL rows and the marker so the build
	// re-walks every commit). A one-shot in-process `ox index` has no daemon
	// "next pass" to do this, so it must happen here.
	if len(store.NeedsReindexMarkers(dataDir)) > 0 {
		slog.Warn("codedb self-heal marker present after open; rebuilding from scratch", "data_dir", dataDir)
		db, err = reopenClean(db, dataDir)
		if err != nil {
			return nil, err
		}
	}

	runErr := indexFn(ctx, db)
	if runErr == nil {
		return db, nil
	}
	if !index.IsCorruptionError(runErr) {
		_ = db.Close()
		return nil, runErr
	}

	slog.Warn("codedb indexing failed on a corrupt cache; discarding cache and retrying once",
		"data_dir", dataDir, "error", runErr)
	db, err = reopenClean(db, dataDir)
	if err != nil {
		return nil, err
	}
	if err := indexFn(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("codedb reindex after cache discard still failed: %w", err)
	}
	return db, nil
}

// reopenClean closes db, discards the codedb cache at dataDir, and returns a
// freshly-opened empty DB. Shared by OpenIndexWithHeal's marker-escalation and
// corruption-retry recovery paths so they wipe-and-reopen identically.
func reopenClean(db *DB, dataDir string) (*DB, error) {
	if closeErr := db.Close(); closeErr != nil {
		slog.Warn("close codedb before discard failed", "error", closeErr)
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return nil, fmt.Errorf("discard codedb cache: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("recreate codedb dir after discard: %w", err)
	}
	fresh, err := Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("reopen codedb after discard: %w", err)
	}
	return fresh, nil
}

// BuildCodeDBAtomic performs a from-scratch codedb build at finalDir
// crash-atomically. It builds into a sibling "<finalDir>.building" directory and
// os.Rename()s it into place only after buildFn returns successfully and the DB
// is closed. A process killed mid-build therefore leaves finalDir untouched (or
// absent) rather than half-written — so the next run starts clean instead of
// crash-looping on a partially-written cache. Any "<finalDir>.building" left by a
// prior killed build is discarded before starting.
//
// buildFn receives a *DB rooted at the temp directory and MUST perform the
// COMPLETE build (git index + symbol/comment parse) — anything left for after
// the swap would not be covered by the atomicity guarantee. Use this for
// full / first-time builds; incremental refreshes of an existing healthy cache
// write in place (a whole-directory swap would force a full rebuild every pass
// and break concurrent readers).
func BuildCodeDBAtomic(ctx context.Context, finalDir string, buildFn func(context.Context, *DB) error) error {
	parent := filepath.Dir(finalDir)
	base := filepath.Base(finalDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create codedb parent dir: %w", err)
	}

	// Unique staging dir per build. A fixed "<dir>.building" path lets two
	// concurrent builds (e.g. one per worktree, sharing the ledger cache) delete
	// each other's in-progress files; MkdirTemp gives each build its own.
	tmpDir, err := os.MkdirTemp(parent, base+".building-*")
	if err != nil {
		return fmt.Errorf("create codedb build dir: %w", err)
	}
	// Always clean up the staging dir. On the success path it has already been
	// renamed to finalDir, so this is a no-op; on every failure path it removes
	// the abandoned build (no orphan accumulation in the shared ledger cache).
	defer func() { _ = os.RemoveAll(tmpDir) }()

	db, err := Open(tmpDir)
	if err != nil {
		return err
	}
	if err := buildFn(ctx, db); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close codedb build before swap: %w", err)
	}

	// Promote without ever leaving a window where no usable cache exists: move
	// any existing cache aside first, rename the freshly-built one into place,
	// then discard the moved-aside copy. If the promoting rename fails, restore
	// the previous cache — it stays available until the replacement is installed.
	// os.Rename within the same parent dir is atomic on POSIX.
	suffix := strings.TrimPrefix(filepath.Base(tmpDir), base+".building-")
	backupDir := filepath.Join(parent, base+".old-"+suffix)
	haveBackup := false
	if _, statErr := os.Stat(finalDir); statErr == nil {
		if err := renameDir(finalDir, backupDir); err != nil {
			return fmt.Errorf("move stale codedb aside before swap: %w", err)
		}
		haveBackup = true
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat codedb before swap: %w", statErr)
	}

	if err := renameDir(tmpDir, finalDir); err != nil {
		// Promotion failed: restore the previous healthy cache so it stays usable
		// (the build simply re-runs on the next pass). The staging dir is removed
		// by the deferred cleanup.
		if haveBackup {
			if restoreErr := renameDir(backupDir, finalDir); restoreErr != nil {
				// Restore also failed. If finalDir now holds a usable cache — e.g.
				// a concurrent builder promoted into it while ours was moved aside,
				// so both of our renames hit a non-empty target — our backup is
				// redundant; discard it so a full index copy does not leak into the
				// shared ledger cache. Otherwise the backup is the only surviving
				// copy: keep it and surface where to recover it from.
				if _, statErr := os.Stat(finalDir); statErr == nil {
					_ = os.RemoveAll(backupDir)
				} else {
					slog.Error("codedb cache stranded after failed swap and restore; recover from backup",
						"final_dir", finalDir, "backup_dir", backupDir, "restore_error", restoreErr)
					return fmt.Errorf("promote codedb build into place: %w (previous cache preserved at %s)", err, backupDir)
				}
			}
		}
		return fmt.Errorf("promote codedb build into place: %w", err)
	}

	if haveBackup {
		_ = os.RemoveAll(backupDir)
	}
	return nil
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

// BackfillSymbolEdges populates ADR-019 symbol_edges for blobs that were
// parsed before the resolver landed (or before a resolver version bump).
// Idempotent and cheap (pure SQL, no tree-sitter); daemons call it after
// every index pass so codedbs upgrade without operator action.
func (db *DB) BackfillSymbolEdges(ctx context.Context, progress func(string)) (index.BackfillStats, error) {
	return index.BackfillSymbolEdges(ctx, db.store, index.ProgressFunc(progress))
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

// SearchRestrictedFiles parses and executes a query with an additional
// internal file-path OR filter. The extra filter is applied inside the search
// plan, before result limits are enforced.
func (db *DB) SearchRestrictedFiles(ctx context.Context, input string, patterns []string) ([]search.Result, error) {
	query, err := search.ParseQuery(input)
	if err != nil {
		return nil, fmt.Errorf("parse query: %w", err)
	}
	query.RestrictFiles(patterns)
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
