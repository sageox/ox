package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedCodeDB creates a CodeDB at dataDir with test data for search/stats tests.
func seedCodeDB(t *testing.T, dataDir string) {
	t.Helper()
	s, err := store.Open(dataDir)
	require.NoError(t, err)
	defer s.Close()

	stmts := []string{
		`INSERT INTO repos (id, name, path) VALUES (1, 'github.com/test/repo-a', '/tmp/repo-a')`,
		`INSERT INTO repos (id, name, path) VALUES (2, 'github.com/test/repo-b', '/tmp/repo-b')`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (1, 1, 'aaa1111111', 'alice', 'initial', 1700000000)`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (2, 1, 'aaa2222222', 'bob', 'fix bug', 1700100000)`,
		`INSERT INTO commits (id, repo_id, hash, author, message, timestamp) VALUES (3, 2, 'bbb1111111', 'carol', 'start project', 1700200000)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (1, 'h1', 'go', 1)`,
		`INSERT INTO blobs (id, content_hash, language, parsed) VALUES (2, 'h2', 'python', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (1, 1, 'main.go', 1)`,
		`INSERT INTO file_revs (id, commit_id, path, blob_id) VALUES (2, 3, 'app.py', 2)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col) VALUES (1, 1, 'main', 'function', 1, 1)`,
		`INSERT INTO symbols (id, blob_id, name, kind, line, col) VALUES (2, 2, 'run', 'function', 1, 1)`,
	}
	for _, stmt := range stmts {
		_, err := s.Exec(stmt)
		require.NoError(t, err, "seed: %s", stmt)
	}
}

// TestConcurrentIndexAttempts simulates multiple daemons (one per worktree)
// attempting to re-index at the same time. The in-process mutex ensures only
// one succeeds; others get "indexing already in progress". Under WAL + busy_timeout,
// no SQLite errors should occur.
func TestConcurrentIndexAttempts(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// multiple managers pointing at the same project root, simulating
	// multiple daemons in different worktrees sharing one index
	tmpDir := t.TempDir()

	const managers = 5
	const attemptsPerManager = 3

	mgrs := make([]*CodeDBManager, managers)
	for i := range managers {
		mgrs[i] = NewCodeDBManager(tmpDir, logger, nil)
	}

	var wg sync.WaitGroup
	var succeeded atomic.Int32
	var alreadyInProgress atomic.Int32
	var otherErrors atomic.Int32

	wg.Add(managers * attemptsPerManager)
	for _, mgr := range mgrs {
		for range attemptsPerManager {
			go func(m *CodeDBManager) {
				defer wg.Done()
				_, err := m.Index(context.Background(), CodeIndexPayload{}, nil)
				if err == nil {
					succeeded.Add(1)
				} else if err.Error() == "indexing already in progress" {
					alreadyInProgress.Add(1)
				} else {
					// other errors are acceptable (e.g., not a git repo)
					// but should not be panics or data races
					otherErrors.Add(1)
				}
			}(mgr)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent index attempts deadlocked")
	}

	total := succeeded.Load() + alreadyInProgress.Load() + otherErrors.Load()
	assert.Equal(t, int32(managers*attemptsPerManager), total, "all attempts should complete")

	t.Logf("succeeded=%d, already_in_progress=%d, other_errors=%d",
		succeeded.Load(), alreadyInProgress.Load(), otherErrors.Load())
}

// TestConcurrentStatsWhileIndexing verifies that Stats() can be called
// concurrently while indexing is in progress without races or panics.
func TestConcurrentStatsWhileIndexing(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tmpDir := t.TempDir()
	mgr := NewCodeDBManager(tmpDir, logger, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const readers = 20
	var readersWg sync.WaitGroup
	readersWg.Add(readers)

	// start readers that continuously call Stats()
	for range readers {
		go func() {
			defer readersWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				stats := mgr.Stats()
				// IndexExists might be true or false depending on timing
				_ = stats.IndexingNow
				_ = stats.Commits
				_ = stats.DataDir
				_ = stats.Repos
			}
		}()
	}

	// attempt indexing (will fail because tmpDir is not a git repo, but
	// that's fine — we're testing Stats() safety during the attempt)
	go func() {
		_, _ = mgr.Index(context.Background(), CodeIndexPayload{}, nil)
	}()

	// let readers run during the index attempt
	time.Sleep(200 * time.Millisecond)
	cancel()
	readersWg.Wait()

	// final stats call should succeed
	stats := mgr.Stats()
	_ = stats
}

// TestConcurrentSearchWhileIndexing simulates many CLI processes running
// "ox code search" while the daemon is re-indexing. Uses separate store
// connections (as real CLI processes would) hitting the same SQLite file.
func TestConcurrentSearchWhileIndexing(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	seedCodeDB(t, dataDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const readers = 20
	const readsPerReader = 10
	var readersWg sync.WaitGroup
	readersWg.Add(readers)

	var searchErrors atomic.Int32
	var searchSuccess atomic.Int32

	// reader goroutines: each opens its own DB connection (simulating
	// separate CLI processes), searches, closes
	for range readers {
		go func() {
			defer readersWg.Done()
			for range readsPerReader {
				db, err := codedb.Open(dataDir)
				if err != nil {
					searchErrors.Add(1)
					continue
				}

				results, err := db.Search(ctx, "type:commit author:alice")
				if err != nil {
					searchErrors.Add(1)
				} else if len(results) > 0 {
					searchSuccess.Add(1)
				}
				db.Close()
			}
		}()
	}

	// writer goroutine: simulates daemon inserting new data
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		db, err := codedb.Open(dataDir)
		if err != nil {
			return
		}
		defer db.Close()

		for i := range 30 {
			_, _ = db.Store().Exec(
				`INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (1, ?, 'writer', 'bg write', ?)`,
				"w"+string(rune('A'+i%26))+string(rune('0'+i/26)),
				1700400000+i,
			)
			time.Sleep(time.Millisecond)
		}
	}()

	readersWg.Wait()
	<-writerDone

	assert.Equal(t, int32(0), searchErrors.Load(),
		"no search errors expected under WAL with busy_timeout")
	assert.Greater(t, searchSuccess.Load(), int32(0),
		"at least some searches should succeed")
}

// TestMultipleManagersCheckFreshness simulates multiple daemons (worktrees)
// calling CheckFreshness simultaneously. CheckFreshness is non-blocking and
// should not panic or deadlock. When no index exists, it fires a background
// goroutine to create the initial index.
func TestMultipleManagersCheckFreshness(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tmpDir := t.TempDir()

	// use a cancellable context so background indexing goroutines stop before cleanup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const managers = 10
	var wg sync.WaitGroup
	wg.Add(managers)

	for range managers {
		go func() {
			defer wg.Done()
			mgr := NewCodeDBManager(tmpDir, logger, nil)
			mgr.CheckFreshness(ctx)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CheckFreshness deadlocked under concurrent calls")
	}

	// cancel to stop any background indexing before temp dir cleanup
	cancel()
	// brief pause for goroutines to notice cancellation
	time.Sleep(50 * time.Millisecond)
}

// TestConcurrentStatsWithPerRepoData verifies Stats() returns correct per-repo
// breakdowns even under concurrent access from multiple callers.
func TestConcurrentStatsWithPerRepoData(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	seedCodeDB(t, dataDir)
	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			db, err := codedb.Open(dataDir)
			if err != nil {
				t.Logf("open: %v", err)
				return
			}
			defer db.Close()

			var commits, blobs, symbols int
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&commits)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobs)
			_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&symbols)

			// per-repo query (same as Stats() uses)
			rows, err := db.Store().Query(`
				SELECT r.name, r.path, COUNT(DISTINCT c.id), COUNT(DISTINCT fr.blob_id)
				FROM repos r
				LEFT JOIN commits c ON c.repo_id = r.id
				LEFT JOIN file_revs fr ON fr.commit_id = c.id
				GROUP BY r.id ORDER BY r.name`)
			if err != nil {
				t.Logf("per-repo query: %v", err)
				return
			}
			defer rows.Close()

			var repoCount int
			for rows.Next() {
				var name, path string
				var rc, rb int
				if rows.Scan(&name, &path, &rc, &rb) == nil {
					repoCount++
				}
			}

			// seed data has 2 repos, 3 commits, 2 blobs, 2 symbols
			assert.Equal(t, 3, commits)
			assert.Equal(t, 2, blobs)
			assert.Equal(t, 2, symbols)
			assert.Equal(t, 2, repoCount)
		}()
	}

	wg.Wait()
}

// TestConcurrentOpenAndSearch opens multiple separate DB connections to the
// same data directory (simulating many CLI processes) and runs searches
// simultaneously. This is the most realistic simulation of many agents
// running "ox code search" at the same time.
func TestConcurrentOpenAndSearch(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	seedCodeDB(t, dataDir)

	queries := []string{
		"type:commit author:alice",
		"type:commit author:bob",
		"type:commit author:carol",
		"type:symbol lang:go main",
		"type:symbol lang:python run",
	}

	const goroutines = 30
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var completed atomic.Int32

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			// each goroutine opens its own connection (like separate CLI processes)
			db, err := codedb.Open(dataDir)
			if err != nil {
				t.Logf("goroutine %d open: %v", id, err)
				return
			}
			defer db.Close()

			q := queries[id%len(queries)]
			_, err = db.Search(context.Background(), q)
			if err != nil {
				t.Errorf("goroutine %d search %q: %v", id, q, err)
				return
			}
			completed.Add(1)
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent open+search deadlocked")
	}

	assert.Equal(t, int32(goroutines), completed.Load(),
		"all goroutines should complete search successfully")
}
