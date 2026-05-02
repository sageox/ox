package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/blevesearch/bleve/v2"
	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	_ "modernc.org/sqlite"
)

// ErrCorrupt indicates the index is corrupted and needs re-indexing.
var ErrCorrupt = fmt.Errorf("codedb index is corrupt")

// MetadataDBFile is the filename of the SQLite database inside a CodeDB directory.
const MetadataDBFile = "metadata.db"

// Store wraps a SQLite database and Bleve full-text search indexes.
// All SQL access goes through the convenience methods below.
//
// The store supports a two-tier architecture:
//   - Shared indexes (on-disk): committed content, shared across worktrees
//   - Dirty overlay (on-disk or in-memory): uncommitted worktree files, per-worktree
//
// When a dirty overlay is attached, CombinedCodeIndex transparently merges
// results from both tiers via Bleve IndexAlias.
type Store struct {
	db           *sql.DB
	queries      *codedbsqlc.Queries
	CodeIndex    bleve.Index
	DiffIndex    bleve.Index
	CommentIndex bleve.Index
	Root         string
	closeOnce    sync.Once

	// dirty overlays for uncommitted worktree files (keyed by worktree ID)
	dirtyCodeIndexes  map[string]bleve.Index
	CombinedCodeIndex bleve.Index // alias of CodeIndex + all dirty indexes, or just CodeIndex
}

// Open opens (or creates) a Store at the given root directory.
// It creates the directory structure, initializes SQLite and Bleve indexes.
// If SQLite corruption is detected, the database is removed and ErrCorrupt is returned
// so the caller can trigger a full re-index.
func Open(root string) (*Store, error) {
	reposDir := filepath.Join(root, "repos")
	bleveDir := filepath.Join(root, "bleve")
	bleveCodeDir := filepath.Join(bleveDir, "code")
	bleveDiffDir := filepath.Join(bleveDir, "diff")
	bleveCommentDir := filepath.Join(bleveDir, "comment")

	for _, dir := range []string{root, reposDir, bleveDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	dbPath := filepath.Join(root, MetadataDBFile)
	// WAL: concurrent readers + one writer. busy_timeout: wait up to 5s for
	// write locks instead of failing immediately. This matters when multiple
	// daemons (one per worktree) share the same index. Long-term fix is
	// one-daemon-per-repo; until then busy_timeout provides best-effort safety.
	// cache_size(-65536): 64 MB page cache. mmap_size(268435456): map up to
	// 256 MB of the DB into memory for faster reads; SQLite silently falls back
	// to normal I/O for pages beyond the mmap region or on unsupported systems.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// integrity check before schema creation
	if err := checkSQLiteIntegrity(db); err != nil {
		db.Close()
		slog.Error("sqlite corruption detected, removing database", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		return nil, fmt.Errorf("sqlite integrity check failed: %w", ErrCorrupt)
	}

	if err := CreateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	codeIndex, err := openOrCreateBleveIndex(bleveCodeDir)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open code index: %w", err)
	}

	diffIndex, err := openOrCreateBleveIndex(bleveDiffDir)
	if err != nil {
		db.Close()
		codeIndex.Close()
		return nil, fmt.Errorf("open diff index: %w", err)
	}

	commentIndex, err := openOrCreateBleveIndex(bleveCommentDir)
	if err != nil {
		db.Close()
		codeIndex.Close()
		diffIndex.Close()
		return nil, fmt.Errorf("open comment index: %w", err)
	}

	s := &Store{
		db:               db,
		queries:          codedbsqlc.New(db),
		CodeIndex:        codeIndex,
		DiffIndex:        diffIndex,
		CommentIndex:     commentIndex,
		Root:             root,
		dirtyCodeIndexes: make(map[string]bleve.Index),
	}
	s.CombinedCodeIndex = s.CodeIndex // default: no overlay
	return s, nil
}

// ReposDir returns the path to the bare git repos directory.
func (s *Store) ReposDir() string {
	return filepath.Join(s.Root, "repos")
}

// Close closes all resources. It is safe to call multiple times.
func (s *Store) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		s.DetachDirtyOverlay()
		if err := s.CodeIndex.Close(); err != nil {
			firstErr = err
		}
		if err := s.DiffIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.CommentIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// AttachDirtyOverlay creates an in-memory Bleve index for dirty worktree files
// and combines it with the shared CodeIndex via IndexAlias. Search code using
// CombinedCodeIndex will transparently search both.
// Primarily used in tests; production uses AttachDirtyIndex for on-disk overlays.
func (s *Store) AttachDirtyOverlay() error {
	s.DetachDirtyOverlay() // close any existing overlays first
	mapping := bleve.NewIndexMapping()
	dirtyIdx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		return fmt.Errorf("create in-memory dirty index: %w", err)
	}
	s.dirtyCodeIndexes["__test__"] = dirtyIdx
	s.rebuildCombinedIndex()
	return nil
}

// AttachDirtyIndex opens an existing on-disk dirty overlay index (built by the
// daemon) and aliases it with the shared CodeIndex for transparent search.
// Uses a default key; for multi-worktree support use AttachDirtyIndexByID.
func (s *Store) AttachDirtyIndex(dirtyBlevePath string) error {
	return s.AttachDirtyIndexByID("__default__", dirtyBlevePath)
}

// AttachDirtyIndexByID opens an on-disk dirty overlay and adds it to the overlay map
// under the given ID. If the ID is already attached, the old overlay is detached first.
// Rebuilds the combined alias to include all active overlays.
func (s *Store) AttachDirtyIndexByID(id, dirtyBlevePath string) error {
	// detach existing overlay for this ID if present
	if existing, ok := s.dirtyCodeIndexes[id]; ok {
		existing.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	dirtyIdx, err := bleve.Open(dirtyBlevePath)
	if err != nil {
		s.rebuildCombinedIndex() // keep alias consistent
		return fmt.Errorf("open dirty index %s: %w", id, err)
	}
	s.dirtyCodeIndexes[id] = dirtyIdx
	s.rebuildCombinedIndex()
	return nil
}

// DetachDirtyIndexByID closes and removes a specific dirty overlay by ID.
// Rebuilds the combined alias with remaining overlays.
func (s *Store) DetachDirtyIndexByID(id string) {
	if idx, ok := s.dirtyCodeIndexes[id]; ok {
		idx.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	s.rebuildCombinedIndex()
}

// DetachDirtyOverlay closes all attached dirty overlays and resets CombinedCodeIndex.
func (s *Store) DetachDirtyOverlay() {
	for id, idx := range s.dirtyCodeIndexes {
		idx.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	s.CombinedCodeIndex = s.CodeIndex
}

// DirtyOverlayCount returns the number of currently attached dirty overlays.
func (s *Store) DirtyOverlayCount() int {
	return len(s.dirtyCodeIndexes)
}

// DirtyCodeIndex returns the first dirty overlay index found, or nil.
// Used by callers that need direct access to a dirty index (e.g., for indexing docs).
func (s *Store) DirtyCodeIndex() bleve.Index {
	for _, idx := range s.dirtyCodeIndexes {
		return idx
	}
	return nil
}

// rebuildCombinedIndex reconstructs CombinedCodeIndex from CodeIndex + all dirty overlays.
func (s *Store) rebuildCombinedIndex() {
	if len(s.dirtyCodeIndexes) == 0 {
		s.CombinedCodeIndex = s.CodeIndex
		return
	}
	indexes := make([]bleve.Index, 0, 1+len(s.dirtyCodeIndexes))
	indexes = append(indexes, s.CodeIndex)
	for _, idx := range s.dirtyCodeIndexes {
		indexes = append(indexes, idx)
	}
	s.CombinedCodeIndex = bleve.NewIndexAlias(indexes...)
}

// CheckIntegrity validates that the SQLite database and all Bleve indexes
// are healthy. Returns nil if everything is fine, ErrCorrupt otherwise.
func (s *Store) CheckIntegrity() error {
	if err := checkSQLiteIntegrity(s.db); err != nil {
		return fmt.Errorf("sqlite: %w", ErrCorrupt)
	}

	// validate bleve indexes can serve a basic query
	for name, idx := range map[string]bleve.Index{"code": s.CodeIndex, "diff": s.DiffIndex, "comment": s.CommentIndex} {
		q := bleve.NewMatchNoneQuery()
		req := bleve.NewSearchRequest(q)
		req.Size = 0
		if _, err := idx.Search(req); err != nil {
			return fmt.Errorf("bleve %s index: %w", name, ErrCorrupt)
		}
	}

	return nil
}

// checkSQLiteIntegrity runs PRAGMA integrity_check and returns an error if the database is corrupt.
func checkSQLiteIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check query failed: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned: %s", result)
	}
	return nil
}

// removeSQLiteFiles removes the database file and its WAL/SHM sidecars.
func removeSQLiteFiles(dbPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove sqlite file", "path", p, "err", err)
		}
	}
}

func openOrCreateBleveIndex(path string) (bleve.Index, error) {
	idx, err := safeOpenBleve(path)
	if err == nil {
		return idx, nil
	}
	if errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
		mapping := bleve.NewIndexMapping()
		return bleve.New(path, mapping)
	}

	// Before treating as corruption, check if the bbolt file exists.
	// If it does, the error is likely a lock-timeout (another goroutine or process
	// has the index open with bbolt's exclusive flock). Nuking in that case destroys
	// an index that is actively being written — the correct action is to return an
	// error and let the caller retry later.
	boltPath := filepath.Join(path, "store", "root.bolt")
	_, statErr := os.Stat(boltPath)
	if statErr == nil {
		return nil, fmt.Errorf("bleve index appears to be in use (lock contention): %w", err)
	}
	// only nuke when the bolt file is provably absent (ENOENT) or the path structure
	// is broken (ENOTDIR: a directory was replaced by a file). Permission and I/O
	// errors (EPERM, EIO, etc.) are transient — nuking would cause data loss.
	if !os.IsNotExist(statErr) && !errors.Is(statErr, syscall.ENOTDIR) {
		return nil, fmt.Errorf("stat bleve bolt file %s: %w", boltPath, statErr)
	}

	// bolt file absent or path structure broken — nuke and recreate
	slog.Error("bleve index corrupt, recreating", "path", path, "err", err)
	if removeErr := os.RemoveAll(path); removeErr != nil {
		return nil, fmt.Errorf("remove corrupt bleve index %s: %w", path, removeErr)
	}
	mapping := bleve.NewIndexMapping()
	return bleve.New(path, mapping)
}

// safeOpenBleve wraps bleve.Open with panic recovery.
// bbolt panics on certain corruption types (e.g., invalid page type)
// instead of returning an error.
func safeOpenBleve(path string) (idx bleve.Index, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("bleve.Open panicked (corrupt index)", "path", path, "panic", r)
			idx = nil
			err = fmt.Errorf("bleve.Open panic: %v", r)
		}
	}()
	return bleve.Open(path)
}

// --- SQL convenience methods ---

// Query executes a SQL query and returns the rows.
func (s *Store) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.Query(query, args...)
}

// QueryContext executes a SQL query with context and returns the rows.
func (s *Store) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a SQL query expected to return at most one row.
func (s *Store) QueryRow(query string, args ...interface{}) *sql.Row {
	return s.db.QueryRow(query, args...)
}

// Exec executes a SQL statement that doesn't return rows.
func (s *Store) Exec(query string, args ...interface{}) (sql.Result, error) {
	return s.db.Exec(query, args...)
}

// BeginTx starts a new transaction with the given context and options.
func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, opts)
}

// Begin starts a new transaction.
func (s *Store) Begin() (*sql.Tx, error) {
	return s.db.Begin()
}

// Queries returns the sqlc-generated typed queries bound to the store's DB.
func (s *Store) Queries() *codedbsqlc.Queries {
	return s.queries
}

// QueriesFromTx returns sqlc-generated typed queries bound to a transaction.
func QueriesFromTx(tx *sql.Tx) *codedbsqlc.Queries {
	return codedbsqlc.New(tx)
}
