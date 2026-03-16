package store

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// expectedTables lists every table the schema should create.
var expectedTables = []string{
	"repos",
	"commits",
	"commit_parents",
	"refs",
	"blobs",
	"file_revs",
	"diffs",
	"symbols",
	"symbol_refs",
}

// openStore is a test helper that opens a store in a temp dir and registers cleanup.
func openStore(t *testing.T) *Store {
	t.Helper()
	tmp := t.TempDir()
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open(%s): %v", tmp, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesStructure(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// verify filesystem structure
	for _, rel := range []string{
		"metadata.db",
		"repos",
		"bleve/code",
		"bleve/diff",
	} {
		path := filepath.Join(tmp, rel)
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("expected %s to exist: %v", rel, statErr)
		}
	}

	// verify SQL works
	_, err = s.Exec("INSERT INTO repos (name, path) VALUES ('test', '/tmp/test')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 repo, got %d", count)
	}
}

func TestOpenIdempotent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// insert data before closing to verify it persists
	if _, err := s1.Exec("INSERT INTO repos (name, path) VALUES ('persist', '/tmp/p')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s1.Close()

	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var name string
	if err := s2.QueryRow("SELECT name FROM repos WHERE name='persist'").Scan(&name); err != nil {
		t.Fatalf("data did not survive reopen: %v", err)
	}
	if name != "persist" {
		t.Errorf("expected 'persist', got %q", name)
	}
}

func TestOpenCorruptSQLite(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	// write garbage to metadata.db before first Open
	dbPath := filepath.Join(tmp, "metadata.db")
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	_, err := Open(tmp)
	if err == nil {
		t.Fatal("expected error from corrupt database, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}

	// corrupt file should be removed so a subsequent Open succeeds
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("corrupt metadata.db should have been removed")
	}
}

func TestOpenCorruptBleve(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	// first open to create structure
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// corrupt the bleve code index by replacing its directory with a file
	bleveCodeDir := filepath.Join(tmp, "bleve", "code")
	if err := os.RemoveAll(bleveCodeDir); err != nil {
		t.Fatalf("remove bleve/code: %v", err)
	}
	if err := os.WriteFile(bleveCodeDir, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	// openOrCreateBleveIndex should recover by recreating
	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open after bleve corruption should recover, got: %v", err)
	}
	defer s2.Close()

	// verify the recreated index is functional
	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after bleve recovery: %v", err)
	}
}

func TestOpenMissingBleveDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// delete bleve code directory entirely
	if err := os.RemoveAll(filepath.Join(tmp, "bleve", "code")); err != nil {
		t.Fatalf("remove bleve/code: %v", err)
	}

	// reopen should recreate the missing index
	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open with missing bleve dir should recreate, got: %v", err)
	}
	defer s2.Close()

	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after bleve recreation: %v", err)
	}
}

func TestOpenPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	t.Parallel()

	tmp := t.TempDir()
	unwritable := filepath.Join(tmp, "readonly")
	if err := os.MkdirAll(unwritable, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// ensure cleanup can remove the directory
	t.Cleanup(func() { os.Chmod(unwritable, 0o700) })

	nested := filepath.Join(unwritable, "store")
	_, err := Open(nested)
	if err == nil {
		t.Fatal("expected error opening store in unwritable directory, got nil")
	}
}

func TestCheckIntegrity_Healthy(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	if err := s.CheckIntegrity(); err != nil {
		t.Errorf("fresh store should pass integrity check: %v", err)
	}
}

func TestCheckIntegrity_CorruptDB(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	// write garbage to metadata.db, then open a *new* connection against it.
	// SQLite caches pages in memory, so corrupting the file under an open
	// connection won't always be detected. Instead we corrupt before opening
	// and verify that Open itself returns ErrCorrupt.
	dbPath := filepath.Join(tmp, "metadata.db")

	// first create a valid store so the directory structure exists
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	s1.Close()

	// now corrupt the database on disk
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	// Open should detect corruption via PRAGMA integrity_check and return ErrCorrupt
	_, err = Open(tmp)
	if err == nil {
		t.Fatal("expected error from corrupt database, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Errorf("first Close returned unexpected error: %v", err)
	}

	// second close should be safe (no panic, no error)
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned unexpected error: %v", err)
	}
}

func TestReposDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	expected := filepath.Join(tmp, "repos")
	got := s.ReposDir()
	if got != expected {
		t.Errorf("ReposDir() = %q, want %q", got, expected)
	}

	// verify the directory actually exists
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("ReposDir path does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("ReposDir path is not a directory")
	}
}

func TestConcurrentOpen(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	// pre-create the store so concurrent opens don't race on schema creation
	s0, err := Open(tmp)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	s0.Close()

	const goroutines = 8
	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s, openErr := Open(tmp)
			if openErr != nil {
				errsMu.Lock()
				errs = append(errs, openErr)
				errsMu.Unlock()
				return
			}
			// do a small read to exercise WAL concurrency
			var count int
			s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count)
			s.Close()
		}()
	}

	wg.Wait()

	if len(errs) > 0 {
		t.Errorf("concurrent Open produced %d errors; first: %v", len(errs), errs[0])
	}
}

func TestSchemaCreation(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	for _, table := range expectedTables {
		t.Run(table, func(t *testing.T) {
			// sqlite_master query to verify table exists
			var name string
			err := s.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
				table,
			).Scan(&name)
			if err != nil {
				t.Fatalf("table %q not found in schema: %v", table, err)
			}
		})
	}
}

func TestSchemaIndexes(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	expectedIndexes := []string{
		"idx_commits_repo",
		"idx_refs_repo",
		"idx_file_revs_commit",
		"idx_file_revs_blob",
		"idx_diffs_commit",
		"idx_symbols_blob",
		"idx_symbols_name",
		"idx_symbol_refs_blob",
		"idx_symbol_refs_name",
		"idx_symbol_refs_symbol",
	}

	for _, idx := range expectedIndexes {
		t.Run(idx, func(t *testing.T) {
			var name string
			err := s.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='index' AND name=?",
				idx,
			).Scan(&name)
			if err != nil {
				t.Fatalf("index %q not found in schema: %v", idx, err)
			}
		})
	}
}

func TestSQLConvenienceMethods(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	// Exec + QueryRow
	res, err := s.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "r1", "/r1")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if id < 1 {
		t.Errorf("expected positive insert ID, got %d", id)
	}

	// Query
	rows, err := s.Query("SELECT name FROM repos WHERE path = ?", "/r1")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("Query returned no rows")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if name != "r1" {
		t.Errorf("expected 'r1', got %q", name)
	}

	// Begin / transaction
	tx, err := s.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = tx.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "r2", "/r2")
	if err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 repos after transaction, got %d", count)
	}
}

func TestOpenCorruptBleveDiffIndex(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// corrupt the diff index specifically
	bleveDiffDir := filepath.Join(tmp, "bleve", "diff")
	if err := os.RemoveAll(bleveDiffDir); err != nil {
		t.Fatalf("remove bleve/diff: %v", err)
	}
	if err := os.WriteFile(bleveDiffDir, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open after diff index corruption should recover, got: %v", err)
	}
	defer s2.Close()

	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after diff index recovery: %v", err)
	}
}

func TestOpenNonexistentRoot(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "a", "b", "c")

	// Open should create intermediate directories
	s, err := Open(nested)
	if err != nil {
		t.Fatalf("Open with nested nonexistent path should succeed: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(filepath.Join(nested, "metadata.db")); err != nil {
		t.Errorf("metadata.db not created in nested path: %v", err)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	// inserting a commit with a nonexistent repo_id should fail if foreign keys are on
	_, err := s.Exec(
		"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (999, 'abc', 'a', 'm', 0)",
	)
	if err == nil {
		t.Error("expected foreign key violation, got nil")
	}
}
