package daemon

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func codedbTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// waitForIndexingDone polls until the indexing flag clears or times out.
func waitForIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.indexing
	}, 5*time.Second, 10*time.Millisecond, "timed out waiting for indexing to complete")
}

// --- Two-tier ledger+dirty architecture tests ---
// These tests validate the ledger index lifecycle, its independence from the
// worktree index, and resilience to real-world failure modes.
// Each test documents what failure it prevents.

// waitForLedgerIndexingDone polls until the ledgerIndexing flag clears or times out.
func waitForLedgerIndexingDone(t *testing.T, mgr *CodeDBManager) {
	t.Helper()
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return !mgr.ledgerIndexing
	}, 5*time.Second, 10*time.Millisecond, "timed out waiting for ledger indexing to complete")
}
