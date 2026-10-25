package autofix

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
)

// captureAlerts swaps the default slog logger for one writing to a buffer and
// returns a func reporting how many ALERT lines have been logged so far.
func captureAlerts(t *testing.T) func() int {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() int { return bytes.Count(buf.Bytes(), []byte("ALERT: plan/session deletions")) }
}

// wipePlans seeds enough plans, then commits their removal.
func wipePlans(t *testing.T, repo string) {
	t.Helper()
	seedSacredPlansAF(t, repo, sacred.MassDeleteThreshold+5)
	afGit(t, repo, "rm", "-r", "data/plans")
	afGit(t, repo, "commit", "-m", "wipe")
}

// The daemon scans on every sync cycle. A deletion already on origin was an
// intentional cleanup a teammate pushed; only local, unpushed deletions are
// still preventable, and each must alert exactly once.
func TestScanLedgerSacredDeletions_AlertsOncePerLocalCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history")
	}
	ctx := context.Background()

	newRepoWithRemote := func(t *testing.T) string {
		repo := newSacredTestRepo(t)
		remote := filepath.Join(t.TempDir(), "origin.git")
		afGit(t, repo, "init", "--bare", "--initial-branch=main", remote)
		afGit(t, repo, "remote", "add", "origin", remote)
		return repo
	}

	t.Run("pushed deletion does not alert", func(t *testing.T) {
		alerts := captureAlerts(t)
		repo := newRepoWithRemote(t)
		wipePlans(t, repo)
		afGit(t, repo, "push", "-u", "origin", "main")

		res := scanLedgerSacredDeletions(ctx, repo, "/fake/repo")
		assert.Equal(t, StatusClean, res.Status)
		assert.Equal(t, 0, alerts())
	})

	t.Run("local deletion alerts once across cycles, new one alerts again", func(t *testing.T) {
		alerts := captureAlerts(t)
		repo := newRepoWithRemote(t)
		afGit(t, repo, "push", "-u", "origin", "main")
		wipePlans(t, repo)

		res := scanLedgerSacredDeletions(ctx, repo, "/fake/repo")
		assert.Equal(t, StatusFound, res.Status)
		assert.Equal(t, 1, alerts())

		res = scanLedgerSacredDeletions(ctx, repo, "/fake/repo")
		assert.Equal(t, StatusClean, res.Status, "already-alerted commit is not re-reported")
		assert.Equal(t, 1, alerts(), "second cycle must log nothing new")

		wipePlans(t, repo)
		res = scanLedgerSacredDeletions(ctx, repo, "/fake/repo")
		assert.Equal(t, StatusFound, res.Status)
		assert.Equal(t, 2, alerts(), "a new local deletion alerts once")
	})
}
