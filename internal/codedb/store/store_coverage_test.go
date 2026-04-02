package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

func TestAttachDirtyOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// initially, CombinedCodeIndex is the same as CodeIndex
	if s.CombinedCodeIndex != s.CodeIndex {
		t.Error("CombinedCodeIndex should equal CodeIndex before overlay")
	}

	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("AttachDirtyOverlay: %v", err)
	}

	// after attach, CombinedCodeIndex should differ from CodeIndex (it's an alias)
	if s.CombinedCodeIndex == s.CodeIndex {
		t.Error("CombinedCodeIndex should differ from CodeIndex after overlay attach")
	}

	// dirty index should be usable -- index a document
	if err := s.DirtyCodeIndex().Index("test-doc", map[string]interface{}{"content": "hello world"}); err != nil {
		t.Fatalf("index into dirty overlay: %v", err)
	}

	// search combined should find it
	q := bleve.NewMatchQuery("hello")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	if err != nil {
		t.Fatalf("search combined: %v", err)
	}
	if result.Total != 1 {
		t.Errorf("search result count = %d, want 1", result.Total)
	}
}

func TestAttachDirtyOverlay_ReplacesPrevious(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// attach first overlay and add data
	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("first AttachDirtyOverlay: %v", err)
	}
	if err := s.DirtyCodeIndex().Index("doc1", map[string]interface{}{"content": "first overlay data"}); err != nil {
		t.Fatalf("index first overlay: %v", err)
	}

	// attach second overlay -- should close the first and create new
	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("second AttachDirtyOverlay: %v", err)
	}

	// second overlay should be empty (first was closed)
	q := bleve.NewMatchQuery("first")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	if err != nil {
		t.Fatalf("search combined after re-attach: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("search found %d results from old overlay, want 0", result.Total)
	}
}

func TestAttachDirtyIndex_NonexistentPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	err := s.AttachDirtyIndex("/nonexistent/path/to/dirty/index")
	if err == nil {
		t.Fatal("expected error attaching nonexistent dirty index, got nil")
	}

	// CombinedCodeIndex should still be usable (pointing at CodeIndex)
	if s.CombinedCodeIndex != s.CodeIndex {
		t.Error("CombinedCodeIndex should remain CodeIndex after failed attach")
	}
}

func TestAttachDirtyIndex_ValidPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// create a real bleve index on disk to attach
	dirtyDir := filepath.Join(t.TempDir(), "dirty-bleve")
	mapping := bleve.NewIndexMapping()
	dirtyIdx, err := bleve.New(dirtyDir, mapping)
	if err != nil {
		t.Fatalf("create dirty bleve index: %v", err)
	}
	// index a doc then close so we can reopen via AttachDirtyIndex
	if err := dirtyIdx.Index("dirty-doc", map[string]interface{}{"content": "dirty data"}); err != nil {
		t.Fatalf("index into dirty: %v", err)
	}
	dirtyIdx.Close()

	if err := s.AttachDirtyIndex(dirtyDir); err != nil {
		t.Fatalf("AttachDirtyIndex: %v", err)
	}

	// verify combined search finds the dirty data
	q := bleve.NewMatchQuery("dirty")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	if err != nil {
		t.Fatalf("search combined with dirty: %v", err)
	}
	if result.Total != 1 {
		t.Errorf("search result count = %d, want 1", result.Total)
	}
}

func TestDetachDirtyOverlay_NoOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// detach without attach should be safe
	s.DetachDirtyOverlay()

	if s.CombinedCodeIndex != s.CodeIndex {
		t.Error("CombinedCodeIndex should equal CodeIndex after detach with no overlay")
	}
}

func TestDetachDirtyOverlay_WithOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("AttachDirtyOverlay: %v", err)
	}

	s.DetachDirtyOverlay()

	if s.CombinedCodeIndex != s.CodeIndex {
		t.Error("CombinedCodeIndex should equal CodeIndex after detach")
	}
	if s.DirtyOverlayCount() != 0 {
		t.Error("dirtyCodeIndex should be nil after detach")
	}
}

func TestQueryContext(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// insert test data
	if _, err := s.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "ctx-repo", "/ctx"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx := context.Background()
	rows, err := s.QueryContext(ctx, "SELECT name FROM repos WHERE name = ?", "ctx-repo")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("QueryContext returned no rows")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if name != "ctx-repo" {
		t.Errorf("name = %q, want %q", name, "ctx-repo")
	}
}

func TestQueryContext_CancelledContext(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := s.QueryContext(ctx, "SELECT name FROM repos")
	if err == nil {
		t.Error("expected error with canceled context, got nil")
	}
}

func TestBeginTx(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	ctx := context.Background()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	// insert within transaction
	if _, err := tx.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "tx-repo", "/tx"); err != nil {
		t.Fatalf("tx exec: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("tx commit: %v", err)
	}

	// verify committed data is visible
	var name string
	if err := s.QueryRow("SELECT name FROM repos WHERE name = ?", "tx-repo").Scan(&name); err != nil {
		t.Fatalf("data not visible after commit: %v", err)
	}
}

func TestBeginTx_Rollback(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	ctx := context.Background()
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	if _, err := tx.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "rollback-repo", "/rb"); err != nil {
		t.Fatalf("tx exec: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("tx rollback: %v", err)
	}

	// verify rolled-back data is not visible
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM repos WHERE name = ?", "rollback-repo").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Errorf("rolled-back data should not be visible, got count=%d", count)
	}
}

func TestOpenCorruptCommentIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	// first open to create structure
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// corrupt the comment index
	commentDir := filepath.Join(tmp, "bleve", "comment")
	if err := os.RemoveAll(commentDir); err != nil {
		t.Fatalf("remove bleve/comment: %v", err)
	}
	if err := os.WriteFile(commentDir, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	// reopen should recover
	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open after comment index corruption should recover, got: %v", err)
	}
	defer s2.Close()

	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after comment index recovery: %v", err)
	}
}

func TestCheckIntegrity_WithDirtyOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("AttachDirtyOverlay: %v", err)
	}

	// integrity check should pass even with overlay attached
	if err := s.CheckIntegrity(); err != nil {
		t.Errorf("integrity check should pass with overlay: %v", err)
	}
}

func TestCloseWithDirtyOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.AttachDirtyOverlay(); err != nil {
		t.Fatalf("AttachDirtyOverlay: %v", err)
	}

	// close should clean up both regular and dirty indexes
	if err := s.Close(); err != nil {
		t.Errorf("Close with overlay returned error: %v", err)
	}

	// second close should be safe
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

func TestSchemaCommentTable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// verify the comments table exists (added by migration)
	var name string
	err := s.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='comments'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("comments table not found in schema: %v", err)
	}
}

func TestSchemaGitHubTables(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	githubTables := []string{"pull_requests", "pr_comments", "issues", "issue_comments", "github_file_mtimes", "pr_commits"}
	for _, table := range githubTables {
		t.Run(table, func(t *testing.T) {
			var name string
			err := s.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
				table,
			).Scan(&name)
			if err != nil {
				t.Fatalf("table %q not found: %v", table, err)
			}
		})
	}
}

func TestCreateSchema_FreshDB(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "fresh.db")

	db, err := openTestDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// CreateSchema on empty DB should create all tables + run all migrations
	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}

	// verify all tables exist
	allTables := []string{
		"repos", "commits", "commit_parents", "refs", "blobs",
		"file_revs", "diffs", "symbols", "symbol_refs",
		"comments", "pull_requests", "pr_comments",
		"issues", "issue_comments", "github_file_mtimes", "pr_commits",
	}
	for _, tbl := range allTables {
		if !tableExists(t, db, tbl) {
			t.Errorf("table %q missing after CreateSchema", tbl)
		}
	}

	// verify migration columns exist
	for _, col := range []string{"signature", "return_type", "params"} {
		if !columnExists(t, db, "symbols", col) {
			t.Errorf("symbols.%s missing after CreateSchema", col)
		}
	}
	if !columnExists(t, db, "blobs", "comments_parsed") {
		t.Error("blobs.comments_parsed missing after CreateSchema")
	}
}

func TestCreateSchema_Idempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "idem.db")

	db, err := openTestDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("first CreateSchema: %v", err)
	}

	// insert data
	db.Exec("INSERT INTO repos (name, path) VALUES ('test', '/test')")

	// second call should not destroy data
	if err := CreateSchema(db); err != nil {
		t.Fatalf("second CreateSchema: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count)
	if count != 1 {
		t.Errorf("data lost after idempotent CreateSchema, count = %d", count)
	}
}

// openTestDB opens a SQLite database for testing.
func openTestDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
}
