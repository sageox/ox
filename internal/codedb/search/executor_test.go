package search

import (
	"context"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
)

// openTestStore creates a temporary Store for testing.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedTestData inserts minimal data for SQL-path executor tests.
func seedTestData(t *testing.T, s *store.Store) {
	t.Helper()
	stmts := []string{
		`INSERT INTO repos (id, name, path) VALUES (1, 'github.com/test/repo', '/tmp/repo')`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (1, 1, 'abc1234567', 'alice', 'initial commit', 1700000000)`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (2, 1, 'def7890123', 'bob', 'fix bug in parser', 1700100000)`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (3, 1, 'ghi4567890', 'alice', 'refactor search module', 1700200000)`,
		`INSERT INTO refs (id, repo_id, name, commit_id) VALUES (1, 1, 'refs/heads/main', 3)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (1, 'hash1', 'go', 1)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (2, 'hash2', 'rust', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (1, 3, 'main.go', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (2, 3, 'lib.rs', 2)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col, end_line, end_col) VALUES (1, 1, 'main', 'function', 1, 1, 5, 1)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col, end_line, end_col) VALUES (2, 1, 'helper', 'function', 7, 1, 10, 1)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col, end_line, end_col, return_type) VALUES (3, 2, 'process', 'function', 1, 1, 20, 1, 'Result')`,
		`INSERT INTO symbol_refs (id, blob_id, symbol_id, ref_name, kind, line, col) VALUES (1, 1, 1, 'helper', 'call', 3, 5)`,
		`INSERT INTO symbol_refs (id, blob_id, symbol_id, ref_name, kind, line, col) VALUES (2, 2, 3, 'parse', 'call', 10, 5)`,
	}
	for _, stmt := range stmts {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed: %s: %v", stmt, err)
		}
	}
}

func TestExecuteCommitSearch(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:commit author:alice")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for commit search by alice")
	}
	for _, r := range results {
		if r.Author != "alice" {
			t.Errorf("expected author alice, got %q", r.Author)
		}
	}
}

func TestExecuteCommitSearchByMessage(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:commit refactor")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Message != "refactor search module" {
		t.Errorf("message = %q", results[0].Message)
	}
}

func TestExecuteSymbolSearch(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:symbol lang:go main")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for symbol search")
	}
	found := false
	for _, r := range results {
		if r.SymbolName == "main" && r.SymbolKind == "function" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find 'main' function symbol")
	}
}

func TestExecuteSymbolSearchWithKindFilter(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:symbol select:symbol.function lang:go helper")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	for _, r := range results {
		if r.SymbolKind != "function" {
			t.Errorf("expected kind=function, got %q", r.SymbolKind)
		}
	}
}

func TestExecuteCallsSearch(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("calls:helper")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for calls:helper")
	}
	// The caller of helper should be main (symbol_id=1 -> name=main)
	found := false
	for _, r := range results {
		if r.SymbolName == "main" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find 'main' as caller of 'helper'")
	}
}

func TestExecuteReturnsSearch(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("returns:Result")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for returns:Result")
	}
	found := false
	for _, r := range results {
		if r.SymbolName == "process" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find 'process' function returning Result")
	}
}

func TestExecuteCommitSearchNoResults(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:commit author:nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestExecuteWithCount(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("type:commit count:1 author:alice")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 1 {
		t.Errorf("expected at most 1 result, got %d", len(results))
	}
}

func TestExecuteContextCancelled(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	q, err := ParseQuery("type:commit author:alice")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Execute(ctx, s, q)
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestExecuteCalledBySearch(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	q, err := ParseQuery("calledby:process")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for calledby:process")
	}
	found := false
	for _, r := range results {
		if r.SymbolName == "parse" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find 'parse' as callee of 'process'")
	}
}

func TestExecuteSymbolSearchLangFilter(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	// Search for symbols in rust — should find 'process' but not 'main'
	q, err := ParseQuery("type:symbol lang:rust process")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.SymbolName == "main" {
			t.Error("should not find Go symbol 'main' when filtering lang:rust")
		}
	}
}

func TestExecuteCommitDateFilter(t *testing.T) {
	s := openTestStore(t)
	seedTestData(t, s)

	// Only commits after timestamp 1700050000 (should exclude 'initial commit')
	q, err := ParseQuery("type:commit after:2023-11-15 author:alice")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Message == "initial commit" {
			t.Error("should not include 'initial commit' with after filter")
		}
	}
}

func TestExecuteSymbolSearchMasterBranch(t *testing.T) {
	s := openTestStore(t)
	// seed data with master branch instead of main — regression test for repos
	// that use "master" as their default branch returning zero results
	stmts := []string{
		`INSERT INTO repos (id, name, path) VALUES (1, 'github.com/test/repo', '/tmp/repo')`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (1, 1, 'abc1234567', 'alice', 'initial commit', 1700000000)`,
		`INSERT INTO refs (id, repo_id, name, commit_id) VALUES (1, 1, 'refs/heads/master', 1)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (1, 'hash1', 'go', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (1, 1, 'main.go', 1)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col, end_line, end_col) VALUES (1, 1, 'handler', 'function', 1, 1, 5, 1)`,
	}
	for _, stmt := range stmts {
		if _, err := s.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	q, err := ParseQuery("type:symbol handler")
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(context.Background(), s, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for symbol search on master branch repo")
	}
}
