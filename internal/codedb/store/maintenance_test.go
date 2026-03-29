package store

import (
	"context"
	"testing"
	"time"
)

func TestMaintainEmptyDB(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	result := s.Maintain(context.Background())

	if !result.IntegrityOK {
		t.Error("expected integrity OK on empty DB")
	}
	if result.OrphanBlobsPruned != 0 {
		t.Errorf("expected 0 orphan blobs pruned, got %d", result.OrphanBlobsPruned)
	}
	if result.OldDiffsPruned != 0 {
		t.Errorf("expected 0 old diffs pruned, got %d", result.OldDiffsPruned)
	}
	if result.StaleSymbolsCount != 0 {
		t.Errorf("expected 0 stale symbols, got %d", result.StaleSymbolsCount)
	}
	if result.Vacuumed {
		t.Error("should not vacuum empty DB")
	}
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestMaintainPrunesOrphanBlobs(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	// insert a repo + commit + blob + file_rev (referenced blob)
	s.Exec("INSERT INTO repos (id, name, path) VALUES (1, 'test', '/tmp/test')")
	s.Exec("INSERT INTO commits (id, repo_id, hash, timestamp) VALUES (1, 1, 'abc123', ?)", time.Now().Unix())
	s.Exec("INSERT INTO blobs (id, content_hash, language) VALUES (100, 'hash-referenced', 'go')")
	s.Exec("INSERT INTO file_revs (commit_id, path, blob_id) VALUES (1, 'main.go', 100)")

	// insert orphan blobs (not referenced by any file_rev)
	s.Exec("INSERT INTO blobs (id, content_hash, language) VALUES (200, 'hash-orphan-1', 'go')")
	s.Exec("INSERT INTO blobs (id, content_hash, language) VALUES (300, 'hash-orphan-2', 'py')")

	// insert symbols for the orphan blob
	s.Exec("INSERT INTO symbols (id, blob_id, name, kind, line, col) VALUES (1, 200, 'OrphanFunc', 'function', 1, 0)")
	s.Exec("INSERT INTO symbol_refs (id, blob_id, symbol_id, ref_name, kind, line, col) VALUES (1, 200, 1, 'OrphanFunc', 'call', 10, 0)")

	// also add a symbol for the referenced blob (should survive)
	s.Exec("INSERT INTO symbols (id, blob_id, name, kind, line, col) VALUES (2, 100, 'KeepFunc', 'function', 5, 0)")

	result := s.Maintain(context.Background())

	if !result.IntegrityOK {
		t.Error("expected integrity OK")
	}
	if result.OrphanBlobsPruned != 2 {
		t.Errorf("expected 2 orphan blobs pruned, got %d", result.OrphanBlobsPruned)
	}
	if result.StaleSymbolsCount != 1 {
		t.Errorf("expected 1 stale symbol pruned, got %d", result.StaleSymbolsCount)
	}

	// verify referenced blob still exists
	var count int
	s.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 blob remaining, got %d", count)
	}

	// verify only referenced symbol survives
	s.QueryRow("SELECT COUNT(*) FROM symbols").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 symbol remaining, got %d", count)
	}
	s.QueryRow("SELECT COUNT(*) FROM symbol_refs").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 orphan symbol_refs remaining, got %d", count)
	}
}

func TestMaintainPrunesOldDiffs(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	oldTimestamp := time.Now().Add(-120 * 24 * time.Hour).Unix() // 120 days ago
	recentTimestamp := time.Now().Add(-30 * 24 * time.Hour).Unix()

	s.Exec("INSERT INTO repos (id, name, path) VALUES (1, 'test', '/tmp/test')")

	// old commit NOT referenced by any ref
	s.Exec("INSERT INTO commits (id, repo_id, hash, timestamp) VALUES (1, 1, 'old-hash', ?)", oldTimestamp)
	s.Exec("INSERT INTO blobs (id, content_hash) VALUES (1, 'blob-old')")
	s.Exec("INSERT INTO file_revs (commit_id, path, blob_id) VALUES (1, 'old.go', 1)")
	s.Exec("INSERT INTO diffs (commit_id, path, new_blob_id) VALUES (1, 'old.go', 1)")

	// recent commit NOT referenced by ref
	s.Exec("INSERT INTO commits (id, repo_id, hash, timestamp) VALUES (2, 1, 'recent-hash', ?)", recentTimestamp)
	s.Exec("INSERT INTO blobs (id, content_hash) VALUES (2, 'blob-recent')")
	s.Exec("INSERT INTO file_revs (commit_id, path, blob_id) VALUES (2, 'recent.go', 2)")
	s.Exec("INSERT INTO diffs (commit_id, path, new_blob_id) VALUES (2, 'recent.go', 2)")

	// old commit referenced by a ref (should NOT be pruned)
	s.Exec("INSERT INTO commits (id, repo_id, hash, timestamp) VALUES (3, 1, 'old-ref-hash', ?)", oldTimestamp)
	s.Exec("INSERT INTO blobs (id, content_hash) VALUES (3, 'blob-old-ref')")
	s.Exec("INSERT INTO file_revs (commit_id, path, blob_id) VALUES (3, 'ref.go', 3)")
	s.Exec("INSERT INTO diffs (commit_id, path, new_blob_id) VALUES (3, 'ref.go', 3)")
	s.Exec("INSERT INTO refs (repo_id, name, commit_id) VALUES (1, 'refs/heads/main', 3)")

	result := s.Maintain(context.Background())

	if result.OldDiffsPruned != 1 {
		t.Errorf("expected 1 old diff pruned (only unreferenced old commit), got %d", result.OldDiffsPruned)
	}

	// verify: recent diff and ref'd diff remain
	var count int
	s.QueryRow("SELECT COUNT(*) FROM diffs").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 diffs remaining, got %d", count)
	}
}

func TestMaintainVacuumsAfterLargePrune(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	// insert >100 orphan blobs to trigger vacuum (no file_revs reference them)
	for i := 1; i <= 150; i++ {
		s.Exec("INSERT INTO blobs (content_hash, language) VALUES (?, 'go')", randomHash(i))
	}

	result := s.Maintain(context.Background())

	if result.OrphanBlobsPruned != 150 {
		t.Errorf("expected 150 orphan blobs pruned, got %d", result.OrphanBlobsPruned)
	}
	if !result.Vacuumed {
		t.Error("expected vacuum after pruning >100 rows")
	}
}

func TestMaintainNoVacuumOnSmallPrune(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	// insert a few orphan blobs (below vacuum threshold of 100)
	for i := 1; i <= 5; i++ {
		s.Exec("INSERT INTO blobs (content_hash, language) VALUES (?, 'go')", randomHash(i))
	}

	result := s.Maintain(context.Background())

	if result.OrphanBlobsPruned != 5 {
		t.Errorf("expected 5 orphan blobs pruned, got %d", result.OrphanBlobsPruned)
	}
	if result.Vacuumed {
		t.Error("should not vacuum when only 5 rows pruned")
	}
}

func TestMaintainCancellation(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result := s.Maintain(ctx)

	// should return early without error
	if result.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestMaintainSizeTracking(t *testing.T) {
	t.Parallel()
	s := openStore(t)

	result := s.Maintain(context.Background())

	if result.SizeBefore < 0 {
		t.Error("expected non-negative SizeBefore")
	}
	if result.SizeAfter < 0 {
		t.Error("expected non-negative SizeAfter")
	}
}

func TestMaintainTotalPruned(t *testing.T) {
	r := MaintenanceResult{
		OrphanBlobsPruned: 10,
		OldDiffsPruned:    5,
		StaleSymbolsCount: 3,
	}
	if r.TotalPruned() != 18 {
		t.Errorf("expected TotalPruned=18, got %d", r.TotalPruned())
	}
}

func TestDBPath(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	path := s.DBPath()
	if path == "" {
		t.Error("expected non-empty DBPath")
	}
}

func randomHash(i int) string {
	return "orphan-hash-" + string(rune('A'+i%26)) + "-" + time.Now().Format("150405.000") + "-" + string(rune('0'+i%10))
}
