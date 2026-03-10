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

	"github.com/blevesearch/bleve/v2"
	_ "modernc.org/sqlite"
)

// ErrCorrupt indicates the index is corrupted and needs re-indexing.
var ErrCorrupt = fmt.Errorf("codedb index is corrupt")

// Store wraps a SQLite database and Bleve full-text search indexes.
// All SQL access goes through the convenience methods below.
type Store struct {
	db        *sql.DB
	CodeIndex bleve.Index
	DiffIndex bleve.Index
	Root      string
	closeOnce sync.Once
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

	for _, dir := range []string{root, reposDir, bleveDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	dbPath := filepath.Join(root, "metadata.db")
	// WAL: concurrent readers + one writer. busy_timeout: wait up to 5s for
	// write locks instead of failing immediately. This matters when multiple
	// daemons (one per worktree) share the same index. Long-term fix is
	// one-daemon-per-repo; until then busy_timeout provides best-effort safety.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// integrity check before schema creation
	if err := checkSQLiteIntegrity(db); err != nil {
		db.Close()
		slog.Warn("sqlite corruption detected, removing database", "path", dbPath, "err", err)
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

	return &Store{
		db:        db,
		CodeIndex: codeIndex,
		DiffIndex: diffIndex,
		Root:      root,
	}, nil
}

// ReposDir returns the path to the bare git repos directory.
func (s *Store) ReposDir() string {
	return filepath.Join(s.Root, "repos")
}

// Close closes all resources. It is safe to call multiple times.
func (s *Store) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		if err := s.CodeIndex.Close(); err != nil {
			firstErr = err
		}
		if err := s.DiffIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// CheckIntegrity validates that the SQLite database and both Bleve indexes
// are healthy. Returns nil if everything is fine, ErrCorrupt otherwise.
func (s *Store) CheckIntegrity() error {
	if err := checkSQLiteIntegrity(s.db); err != nil {
		return fmt.Errorf("sqlite: %w", ErrCorrupt)
	}

	// validate bleve indexes can serve a basic query
	for name, idx := range map[string]bleve.Index{"code": s.CodeIndex, "diff": s.DiffIndex} {
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
	idx, err := bleve.Open(path)
	if err == nil {
		return idx, nil
	}
	if errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
		mapping := bleve.NewIndexMapping()
		return bleve.New(path, mapping)
	}

	// any other error indicates corruption; nuke and recreate
	slog.Warn("bleve index corrupt, recreating", "path", path, "err", err)
	if removeErr := os.RemoveAll(path); removeErr != nil {
		return nil, fmt.Errorf("remove corrupt bleve index %s: %w", path, removeErr)
	}
	mapping := bleve.NewIndexMapping()
	return bleve.New(path, mapping)
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
