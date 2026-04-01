package daemon

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func codedbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// waitForIndexingDone polls until the indexing flag clears or times out.
func waitForIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.indexing
		mgr.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for indexing to complete")
}

// --- Two-tier baseline+dirty architecture tests ---
// These tests validate the baseline index lifecycle, its independence from the
// worktree index, and resilience to real-world failure modes.
// Each test documents what failure it prevents.

// waitForBaselineIndexingDone polls until the baselineIndexing flag clears or times out.
func waitForBaselineIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		done := !mgr.baselineIndexing
		mgr.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for baseline indexing to complete")
}
