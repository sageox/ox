package codedb_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
)

// seedDamagedRepos creates a codedb under dataDir with one repos row, closes it,
// then overwrites the b-tree header of the repos table's root page. The file
// header and schema stay intact, so Open succeeds; the first statement that
// reads repos fails with SQLITE_CORRUPT — exactly how damage presents now that
// Open no longer scans the whole database.
func seedDamagedRepos(t *testing.T, dataDir string) {
	t.Helper()
	db, err := codedb.Open(dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Store().Exec(`INSERT INTO repos (name, path) VALUES ('demo', '/tmp/demo')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dbPath := filepath.Join(dataDir, store.MetadataDBFile)
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	var rootPage, pageSize int
	if err := raw.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'repos'`).Scan(&rootPage); err != nil {
		t.Fatalf("rootpage: %v", err)
	}
	if err := raw.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open db file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, int64(rootPage-1)*int64(pageSize)); err != nil {
		t.Fatalf("damage page: %v", err)
	}
}

// Open no longer scans for damage, so a damaged page is first met as
// SQLITE_CORRUPT from the statement that reads it. That must reach the same
// heal as the old open-time verdict did: discard the cache, retry once.
//
// Failure prevented: a damaged index fails the same way on every pass forever,
// because nothing at open time notices it any more.
func TestOpenIndexWithHeal_HealsDamageMetByARealStatement(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	dataDir := filepath.Join(t.TempDir(), "codedb")
	seedDamagedRepos(t, dataDir)

	calls := 0
	indexFn := func(ctx context.Context, db *codedb.DB) error {
		calls++
		// path is not covered by repos' UNIQUE(name) index, so this walks the
		// damaged table itself; COUNT(*) would be answered from that index
		rows, err := db.Store().Query(`SELECT path FROM repos`)
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			n++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("index: %w", err)
		}
		if n != 0 {
			t.Errorf("retry saw %d repos rows, want a cache rebuilt from scratch", n)
		}
		return nil
	}

	db, err := codedb.OpenIndexWithHeal(context.Background(), dataDir, indexFn)
	if err != nil {
		t.Fatalf("OpenIndexWithHeal: %v", err)
	}
	defer db.Close()
	if calls != 2 {
		t.Fatalf("index attempts = %d, want 2 (the damaged pass, then one clean retry)", calls)
	}
}

// The counterpart, and the #875 rule: a SQLite failure that is NOT damage — here
// a constraint violation, a real *sqlite.Error — must neither discard the cache
// nor retry.
func TestOpenIndexWithHeal_KeepsCacheOnNonDamageSQLiteError(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	dataDir := filepath.Join(t.TempDir(), "codedb")
	marker := filepath.Join(dataDir, "KEEP_ME")

	calls := 0
	indexFn := func(ctx context.Context, db *codedb.DB) error {
		calls++
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		for range 2 { // the second insert violates repos.name UNIQUE
			if _, err := db.Store().Exec(`INSERT INTO repos (name, path) VALUES ('dup', '/tmp/dup')`); err != nil {
				return fmt.Errorf("index: %w", err)
			}
		}
		return nil
	}

	db, err := codedb.OpenIndexWithHeal(context.Background(), dataDir, indexFn)
	if db != nil {
		db.Close()
		t.Fatal("expected the pass to fail")
	}
	if err == nil || errors.Is(err, store.ErrCorrupt) {
		t.Fatalf("error = %v, want the constraint failure, not a corruption verdict", err)
	}
	if calls != 1 {
		t.Errorf("index attempts = %d, want 1: a non-damage error must not trigger a discard-and-retry", calls)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("cache was discarded over a failure that is not damage: %v", statErr)
	}
}
