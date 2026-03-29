package codedb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndClose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Store should be accessible
	if db.Store() == nil {
		t.Fatal("Store() returned nil")
	}

	// verify directory structure was created
	for _, rel := range []string{"metadata.db", "repos", "bleve/code", "bleve/diff"} {
		p := filepath.Join(tmp, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", rel, err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenInvalidPath(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	t.Parallel()

	// create a read-only dir so nested creation fails
	tmp := t.TempDir()
	unwritable := filepath.Join(tmp, "ro")
	if err := os.MkdirAll(unwritable, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(unwritable, 0o700) })

	_, err := Open(filepath.Join(unwritable, "nested"))
	if err == nil {
		t.Fatal("expected error opening in unwritable directory")
	}
}

func TestRawSQL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// insert test data
	_, err = db.Store().Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "test-repo", "/tmp/test")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err = db.Store().Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "other-repo", "/tmp/other")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	cols, rows, err := db.RawSQL("SELECT name, path FROM repos ORDER BY name")
	if err != nil {
		t.Fatalf("RawSQL: %v", err)
	}

	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(cols))
	}
	if cols[0] != "name" || cols[1] != "path" {
		t.Errorf("columns = %v, want [name path]", cols)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "other-repo" {
		t.Errorf("first row name = %q, want 'other-repo'", rows[0][0])
	}
}

func TestRawSQL_NullValues(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// commits.author is nullable
	_, err = db.Store().Exec("INSERT INTO repos (name, path) VALUES ('r', '/r')")
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	_, err = db.Store().Exec(
		"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (1, 'abc', NULL, NULL, 0)",
	)
	if err != nil {
		t.Fatalf("insert commit: %v", err)
	}

	cols, rows, err := db.RawSQL("SELECT author, message FROM commits WHERE hash = 'abc'")
	if err != nil {
		t.Fatalf("RawSQL: %v", err)
	}

	if len(cols) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(cols))
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// NULL values should render as "NULL"
	if rows[0][0] != "NULL" {
		t.Errorf("null author = %q, want 'NULL'", rows[0][0])
	}
	if rows[0][1] != "NULL" {
		t.Errorf("null message = %q, want 'NULL'", rows[0][1])
	}
}

func TestRawSQL_EmptyResult(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cols, rows, err := db.RawSQL("SELECT name FROM repos WHERE 1=0")
	if err != nil {
		t.Fatalf("RawSQL: %v", err)
	}
	if len(cols) != 1 {
		t.Fatalf("expected 1 column, got %d", len(cols))
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestRawSQL_InvalidQuery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	_, _, err = db.RawSQL("SELECT * FROM nonexistent_table")
	if err == nil {
		t.Fatal("expected error for invalid query")
	}
}

func TestSearch_InvalidQuery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// empty query should still be parseable (or return parse error)
	// exercise the search path to verify it doesn't panic
	_, _ = db.Search(context.Background(), "")
}

func TestTranslateQuery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	db, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// simple keyword query
	result, err := db.TranslateQuery("func main")
	if err != nil {
		t.Fatalf("TranslateQuery: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestOpenReopen(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	tmp := t.TempDir()

	// open, write data, close
	db1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_, err = db1.Store().Exec("INSERT INTO repos (name, path) VALUES ('persist', '/p')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	db1.Close()

	// reopen and verify data persists
	db2, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	var name string
	if err := db2.Store().QueryRow("SELECT name FROM repos WHERE name='persist'").Scan(&name); err != nil {
		t.Fatalf("data did not persist: %v", err)
	}
	if name != "persist" {
		t.Errorf("expected 'persist', got %q", name)
	}
}
