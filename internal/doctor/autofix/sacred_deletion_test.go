package autofix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSacredTestRepo makes a real git repo with an identity and a base commit.
// Uses a SageOx-owned test domain per the test-email-domains hard rule.
func newSacredTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	afGit(t, repo, "init", "--initial-branch=main")
	afGit(t, repo, "config", "user.name", "Test")
	afGit(t, repo, "config", "user.email", "sacred-test@test.sageox.ai")
	afGit(t, repo, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644))
	afGit(t, repo, "add", "base.txt")
	afGit(t, repo, "commit", "-m", "base")
	return repo
}

func seedSacredPlansAF(t *testing.T, repo string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		dir := filepath.Join(repo, "data", "plans", fmt.Sprintf("2026-06-%02d-plan", i+1))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "plan.md"), []byte("x\n"), 0o644))
	}
	afGit(t, repo, "add", "data/plans")
	afGit(t, repo, "commit", "-m", "seed plans")
}

// TestScanLedgerSacredDeletions_FlagsHistoricalWipe: a commit that deleted every
// plan sits in history. The detector must surface it (StatusFound) even though
// the wipe already landed — this is the belt to the commit guard's suspenders,
// catching a wipe that reached history via an old binary or a force-push.
func TestScanLedgerSacredDeletions_FlagsHistoricalWipe(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, sacred.MassDeleteThreshold+5)
	afGit(t, repo, "rm", "-r", "data/plans")
	afGit(t, repo, "commit", "-m", "chore: add .sageox/.gitignore to exclude daemon cache files")

	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusFound, res.Status, "a historical sacred wipe must be surfaced")
	assert.Contains(t, res.Summary, "plan/session deletion history")
}

// seedSacredSessions writes n session directories with the full artifact set a
// real session carries. Six files per session is the point: the detector must
// key on the SESSION being gone, not on how many files it happened to hold.
func seedSacredSessions(t *testing.T, repo string, n int) []string {
	t.Helper()
	names := make([]string, 0, n)
	for i := range n {
		name := fmt.Sprintf("2026-06-%02dT10-00-devon-Ox%04d", i+1, i)
		dir := filepath.Join(repo, "sessions", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for _, f := range []string{"meta.json", "raw.jsonl", "summary.json", "summary.md", "session.md", "context-trace.jsonl"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("preserved in history\n"), 0o600))
		}
		names = append(names, name)
	}
	afGit(t, repo, "add", "sessions")
	afGit(t, repo, "commit", "-m", "record sessions")
	return names
}

// Deleting ONE session is routine churn — the same claim
// TestScanLedgerSacredDeletions_IgnoresSmallDeletion makes for one plan — and
// must not be flagged, no matter how many artifact files that session holds.
//
// Previously this asserted StatusFound, which looked like policy but was an
// artifact of counting files: a 6-file session crossed a 5-FILE threshold while
// the 1-file plan in IgnoresSmallDeletion did not, so the two tests demanded
// opposite outcomes for the identical operation. On a real ledger that made
// every `ox session delete` a permanent "deletion history" warning.
//
// The scan must still leave history, the deleted data, and unrelated dirty
// files completely untouched.
func TestScanLedgerSacredDeletions_SingleSessionDeleteIsRoutineChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history")
	}
	for _, message := range []string{"Delete session example", "github: sync PRs"} {
		t.Run(message, func(t *testing.T) {
			repo := newSacredTestRepo(t)
			names := seedSacredSessions(t, repo, 1)
			dir := filepath.Join(repo, "sessions", names[0])
			afGit(t, repo, "rm", "-r", "sessions/"+names[0])
			afGit(t, repo, "commit", "-m", message)
			head := afGit(t, repo, "rev-parse", "HEAD")
			// An unrelated uncommitted edit must also survive the scan.
			dirty := filepath.Join(repo, "base.txt")
			require.NoError(t, os.WriteFile(dirty, []byte("customer edit\n"), 0o600))
			status := afGit(t, repo, "status", "--porcelain")
			for range 2 {
				res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
				require.Equal(t, StatusClean, res.Status, "one session is churn, not a wipe")
			}
			assert.Equal(t, head, afGit(t, repo, "rev-parse", "HEAD"))
			assert.Equal(t, status, afGit(t, repo, "status", "--porcelain"))
			assert.NoDirExists(t, dir, "scanning must not restore intentionally deleted data")
			assert.Equal(t, "preserved in history", afGit(t, repo, "show", "HEAD^:sessions/"+names[0]+"/raw.jsonl"))
			content, err := os.ReadFile(dirty)
			require.NoError(t, err)
			assert.Equal(t, "customer edit\n", string(content))
		})
	}
}

// A wipe wearing an innocuous commit message is still a wipe: the detector
// keys on what the commit did, never on what it said. This is the surviving
// half of the old ReportsFactsWithoutChangingHistory intent.
func TestScanLedgerSacredDeletions_CommitMessageIsNotAuthorization(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history")
	}
	repo := newSacredTestRepo(t)
	seedSacredSessions(t, repo, sacred.MassDeleteThreshold+3)
	afGit(t, repo, "rm", "-r", "sessions")
	afGit(t, repo, "commit", "-m", "github: sync PRs")

	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	require.Equal(t, StatusFound, res.Status, "commit messages are not proof of authorization")
	assert.Contains(t, res.Summary, "whole plans/sessions")
	assert.Contains(t, res.Summary, "verify intent before recovery")
	assert.NotContains(t, res.Summary, "mass-deletion")
}

// Sweeping stale artifacts from INSIDE sessions loses no session, so it must
// not be reported — however many files it removes, and however many sessions
// it touches. This is the shape that put a permanent, purely false
// "221 plan/session files" warning on a real ledger: 15 `.rej` files across 6
// session directories, 0 sessions lost.
func TestScanLedgerSacredDeletions_ArtifactSweepRemovesNoSession(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git history")
	}
	repo := newSacredTestRepo(t)
	names := seedSacredSessions(t, repo, sacred.MassDeleteThreshold+4)
	for _, name := range names {
		rej := filepath.Join(repo, "sessions", name, "meta.json.rej")
		require.NoError(t, os.WriteFile(rej, []byte("stale patch\n"), 0o600))
	}
	afGit(t, repo, "add", "sessions")
	afGit(t, repo, "commit", "-m", "record rejected patches")
	for _, name := range names {
		afGit(t, repo, "rm", "sessions/"+name+"/meta.json.rej")
	}
	afGit(t, repo, "commit", "-m", "chore(ledger): remove stale .rej rejected-patch artifacts")

	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status, "removing artifacts inside sessions loses no session")
	for _, name := range names {
		assert.FileExists(t, filepath.Join(repo, "sessions", name, "meta.json"))
	}
}

// TestScanLedgerSacredDeletions_CleanWhenNoWipe: a ledger that only ever added
// plans has nothing to flag.
func TestScanLedgerSacredDeletions_CleanWhenNoWipe(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, 3)
	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status)
}

// TestScanLedgerSacredDeletions_IgnoresSmallDeletion: deleting a single plan is
// routine churn (below the threshold) and must not be flagged.
func TestScanLedgerSacredDeletions_IgnoresSmallDeletion(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, 3)
	afGit(t, repo, "rm", "-r", "data/plans/2026-06-01-plan")
	afGit(t, repo, "commit", "-m", "Delete plan 2026-06-01-plan")
	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status)
}
