package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	markedUpstreamMeta = "sessions/2026-09-17T10-00-tess-OxTTTT/meta.json" // remote has it clean
	markedLocalOnly    = "sessions/2026-10-06T11-00-ryan-OxRRRR/meta.json" // remote never saw it
	markedUnparseable  = "sessions/2026-10-06T12-00-cy-OxUUUU/meta.json"   // no side parses as JSON
)

const (
	cleanUpstreamMeta = "{\"title\":\"upstream\",\"attempts\":3}\n"
	autostashConflict = "{\n<<<<<<< Updated upstream\n\"title\":\"kept\"\n=======\n\"title\":\"stash side\"\n>>>>>>> Stashed changes\n}\n"
	brokenConflict    = "<<<<<<< Updated upstream\n{\"title\":\n=======\n\"x\"}\n>>>>>>> Stashed changes\n"
)

// newMarkedLedger builds a Ledger one unpushed commit ahead of a bare remote where the commit holds
// autostash-pop conflict blocks, the shape a swept `Update sessions` commit leaves behind.
func newMarkedLedger(t *testing.T, withUnparseable bool) string {
	t.Helper()
	skipIntegration(t)
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	ledger := filepath.Join(base, "ledger")
	require.NoError(t, os.MkdirAll(remote, 0o755))
	mustRunGit(t, remote, "init", "--bare", "--initial-branch=main")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	mustRunGit(t, seed, "init", "--initial-branch=main")
	mustRunGit(t, seed, "config", "commit.gpgsign", "false")
	writeLedgerFile(t, seed, markedUpstreamMeta, cleanUpstreamMeta)
	mustRunGit(t, seed, "add", "-A")
	mustRunGit(t, seed, "commit", "-m", "teammate session")
	mustRunGit(t, seed, "remote", "add", "origin", remote)
	mustRunGit(t, seed, "push", "origin", "main")
	mustRunGit(t, base, "clone", remote, ledger)
	mustRunGit(t, ledger, "config", "user.name", "Test")
	mustRunGit(t, ledger, "config", "user.email", "test@example.com")
	mustRunGit(t, ledger, "config", "commit.gpgsign", "false")

	writeLedgerFile(t, ledger, markedUpstreamMeta, autostashConflict)
	writeLedgerFile(t, ledger, markedLocalOnly, autostashConflict)
	if withUnparseable {
		writeLedgerFile(t, ledger, markedUnparseable, brokenConflict)
	}
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "Update sessions 2026-10-01")
	return ledger
}

func TestResolveCommittedConflictMarkers(t *testing.T) {
	ctx := context.Background()

	t.Run("detect only leaves the ledger untouched", func(t *testing.T) {
		ledger := newMarkedLedger(t, false)
		head, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")

		report, err := resolveCommittedConflictMarkers(ctx, ledger, false)

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{markedUpstreamMeta, markedLocalOnly}, report.Marked)
		after, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		assert.Equal(t, head, after)
	})

	t.Run("repairs from upstream or the upstream side, then pushes", func(t *testing.T) {
		ledger := newMarkedLedger(t, false)
		userCommit, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		require.ErrorContains(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"), "unresolved conflict")

		report, err := resolveCommittedConflictMarkers(ctx, ledger, true)

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{markedUpstreamMeta, markedLocalOnly}, report.Resolved)
		assert.Empty(t, report.Unrepairable)
		assert.True(t, report.Committed)
		assert.NoError(t, report.Remaining)

		subject, _ := runIsolatedGit(t, ledger, "log", "-1", "--format=%s")
		assert.Equal(t, "doctor: resolve committed conflict markers in 2 session files", subject)
		parent, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD~1")
		assert.Equal(t, userCommit, parent, "the existing commit must be left exactly as it was")

		assert.Equal(t, cleanUpstreamMeta, ledgerFile(t, ledger, "HEAD:"+markedUpstreamMeta), "upstream's version wins when it has the file")
		localOnly := ledgerFile(t, ledger, "HEAD:"+markedLocalOnly)
		assert.True(t, json.Valid([]byte(localOnly)))
		assert.Contains(t, localOnly, `"kept"`)
		assert.NotContains(t, localOnly, "stash side")

		require.NoError(t, lfs.ValidateUnpushedTip(ctx, ledger, "origin/main"))
		mustRunGit(t, ledger, "push", "origin", "main")
		head, _ := runIsolatedGit(t, ledger, "rev-parse", "HEAD")
		remoteHead, _ := runIsolatedGit(t, ledger, "ls-remote", "origin", "refs/heads/main")
		assert.Contains(t, remoteHead, head)
	})

	t.Run("a meta.json that will not parse is reported and left untouched", func(t *testing.T) {
		ledger := newMarkedLedger(t, true)

		report, err := resolveCommittedConflictMarkers(ctx, ledger, true)

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{markedUpstreamMeta, markedLocalOnly}, report.Resolved)
		require.Len(t, report.Unrepairable, 1)
		assert.Equal(t, markedUnparseable, report.Unrepairable[0].Path)
		assert.Equal(t, brokenConflict, ledgerFile(t, ledger, "HEAD:"+markedUnparseable))
		onDisk, err := os.ReadFile(filepath.Join(ledger, filepath.FromSlash(markedUnparseable)))
		require.NoError(t, err)
		assert.Equal(t, brokenConflict, string(onDisk))
		require.Error(t, report.Remaining)
		assert.Contains(t, report.Remaining.Error(), markedUnparseable)
	})

	t.Run("a clean ledger reports nothing", func(t *testing.T) {
		ledger := newWedgedLedger(t, false)
		report, err := resolveCommittedConflictMarkers(ctx, ledger, true)
		require.NoError(t, err)
		assert.Empty(t, report.Marked)
		assert.False(t, report.Committed)
	})
}
