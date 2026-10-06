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

func TestRunSessionConflictMarkers(t *testing.T) {
	fixCmd := "ox doctor --fix-slug=" + CheckSlugSessionConflictMarkers
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		fix         bool
		wantPassed  bool
		wantSkipped bool
		wantMessage string
		wantDetail  []string
	}{
		{"no upstream is skipped", func(t *testing.T) string {
			dir := t.TempDir()
			mustRunGit(t, dir, "init", "--initial-branch=main")
			return dir
		}, true, false, true, "no upstream", nil},
		{"clean ledger passes", func(t *testing.T) string { return newWedgedLedger(t, false) },
			false, true, false, "no conflict markers", nil},
		{"report only names the fix command", func(t *testing.T) string { return newMarkedLedger(t, false) },
			false, false, false, "2 session file(s)", []string{fixCmd}},
		{"unparseable file is named with its path and reason", func(t *testing.T) string { return newMarkedLedger(t, true) },
			true, false, false, "could not be resolved", []string{markedUnparseable, "resolved 2", fixCmd}},
		{"fully repairable ledger passes", func(t *testing.T) string { return newMarkedLedger(t, false) },
			true, true, false, "resolved conflict markers in 2", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runSessionConflictMarkers(tt.setup(t), tt.fix)

			assert.Equal(t, tt.wantPassed, result.passed, "%s / %s", result.message, result.detail)
			assert.Equal(t, tt.wantSkipped, result.skipped)
			assert.Contains(t, result.message, tt.wantMessage)
			for _, fragment := range tt.wantDetail {
				assert.Contains(t, result.detail, fragment)
			}
		})
	}
}

// an unreadable upstream must fail the check, never report a clean Ledger it did not look at
func TestRunSessionConflictMarkers_UnreadableUpstreamFails(t *testing.T) {
	ledger := newMarkedLedger(t, false)
	ref := filepath.Join(ledger, ".git", "refs", "remotes", "origin", "main")
	require.NoError(t, os.WriteFile(ref, []byte("0123456789012345678901234567890123456789\n"), 0o644))

	result := runSessionConflictMarkers(ledger, true)

	assert.False(t, result.passed, "%s", result.message)
	assert.False(t, result.skipped, "%s", result.message)
}

func TestCheckSessionConflictMarkers_NoLedgerIsSkipped(t *testing.T) {
	skipIntegration(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Chdir(dir)

	result := checkSessionConflictMarkers(true)

	assert.True(t, result.skipped, "%s", result.message)
}

func TestSessionConflictMarkersCheck_IsRegistered(t *testing.T) {
	check, ok := DoctorCheckRegistry[CheckSlugSessionConflictMarkers]

	require.True(t, ok, "--fix-slug needs the check registered")
	assert.Equal(t, FixLevelSuggested, check.FixLevel)
}

func TestResolveMarkedFile_RefusesOrphanedTail(t *testing.T) {
	ledger := newMarkedLedger(t, false)
	orphan := "sessions/2026-10-06T13-00-dee-OxDDDD/notes.md"
	writeLedgerFile(t, ledger, orphan, "notes\n=======\n>>>>>>> Stashed changes\n")
	mustRunGit(t, ledger, "add", "-A")
	mustRunGit(t, ledger, "commit", "-m", "orphaned tail")

	report, err := resolveCommittedConflictMarkers(context.Background(), ledger, true)

	require.NoError(t, err)
	require.Len(t, report.Unrepairable, 1)
	assert.Equal(t, orphan, report.Unrepairable[0].Path)
	assert.Contains(t, report.Unrepairable[0].Reason, "not a balanced")
	assert.ElementsMatch(t, []string{markedUpstreamMeta, markedLocalOnly}, report.Resolved)
}

func TestResolveMarkedFile_RefusesLocalEdit(t *testing.T) {
	ledger := newMarkedLedger(t, false)
	writeLedgerFile(t, ledger, markedLocalOnly, autostashConflict+"// newer local edit\n")

	report, err := resolveCommittedConflictMarkers(context.Background(), ledger, true)

	require.NoError(t, err)
	require.Len(t, report.Unrepairable, 1)
	assert.Equal(t, markedLocalOnly, report.Unrepairable[0].Path)
	assert.Contains(t, report.Unrepairable[0].Reason, "uncommitted local edit")
	assert.Equal(t, []string{markedUpstreamMeta}, report.Resolved)
}

// diff3/zdiff3 conflict style adds a base section that must be dropped with the stash side.
func TestKeepUpstreamSide(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"two-way", autostashConflict, "{\n\"title\":\"kept\"\n}\n", true},
		{"diff3 base dropped", "{\n<<<<<<< Updated upstream\n\"a\":1\n||||||| base\n\"a\":0\n=======\n\"a\":2\n>>>>>>> Stashed changes\n}\n", "{\n\"a\":1\n}\n", true},
		{"orphaned tail", "{}\n=======\n>>>>>>> Stashed changes\n", "", false},
		{"nested start", "<<<<<<< Updated upstream\n<<<<<<< again\n=======\n>>>>>>> Stashed changes\n", "", false},
		{"unterminated block", "<<<<<<< Updated upstream\n{}\n=======\n", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := keepUpstreamSide([]byte(tt.in))
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.want, string(got))
			}
		})
	}
}
