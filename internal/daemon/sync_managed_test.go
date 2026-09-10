package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. sessions/*/meta.json conflict classification ---
//
// sessions/ is hard-denied from auto-resolve by design (see
// internal/manifest/auto_resolve.go), so a genuine content conflict there
// can never be tier-1/2/3 resolved — pullManagedRepo must classify it as
// IssueTypeSessionConflictWedge, not the generic IssueTypeDiverged ordinary
// lag also produces. Without this discrimination, sync.go has no way to
// escalate severity for a truly stuck conflict versus a repo that's simply
// behind and will resolve on its own next cycle.

// makeSessionMetaConflictClone builds a bare remote plus a local clone where
// both sides modified the same sessions/<id>/meta.json content differently:
// the local clone has an unpushed commit, and the remote has since advanced
// past it on the same file. Running `git pull --rebase` against this clone
// reproduces the exact conflict shape the bug report described. Returns the
// local clone path.
func makeSessionMetaConflictClone(t *testing.T) string {
	t.Helper()
	isolateCredentials(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	out, err := runGitOut(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)
	require.NoError(t, err, out)

	writeMeta := func(dir, content string) {
		metaPath := filepath.Join(dir, "sessions", "2026-07-19T00-00-test-Ox0001", "meta.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(metaPath), 0o755))
		require.NoError(t, os.WriteFile(metaPath, []byte(content), 0o644))
	}
	commitAndPush := func(dir, msg string) {
		out, err := runGitOut(t, dir, "add", "-A")
		require.NoError(t, err, out)
		out, err = runGitOut(t, dir, "commit", "-m", msg)
		require.NoError(t, err, out)
		out, err = runGitOut(t, dir, "push", "origin", "HEAD:main")
		require.NoError(t, err, out)
	}

	// seed the remote with an initial version of the session's meta.json
	seedDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, seedDir)
	require.NoError(t, err, out)
	writeMeta(seedDir, `{"stop_reason":"initial"}`)
	commitAndPush(seedDir, "seed session")

	// local clone under test: modifies meta.json, commits, but never pushes
	localDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, localDir)
	require.NoError(t, err, out)
	writeMeta(localDir, `{"stop_reason":"local-in-flight"}`)
	out, err = runGitOut(t, localDir, "add", "-A")
	require.NoError(t, err, out)
	out, err = runGitOut(t, localDir, "commit", "-m", "local: finalize session")
	require.NoError(t, err, out)

	// a second writer advances the remote on the same file, so localDir is
	// simultaneously ahead (its own unpushed commit) and behind (remote
	// moved) on sessions/2026-07-19T00-00-test-Ox0001/meta.json.
	remoteWriterDir := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, remoteWriterDir)
	require.NoError(t, err, out)
	writeMeta(remoteWriterDir, `{"stop_reason":"remote-writer"}`)
	commitAndPush(remoteWriterDir, "remote: finalize session")

	return localDir
}

func TestPullManagedRepo_SessionMetaConflict_ClassifiesAsSessionConflictWedge(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	localDir := makeSessionMetaConflictClone(t)

	s := newTestScheduler(t.TempDir())
	result := s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath: localDir,
		RepoName: "ledger",
		// Deliberately NOT ledger.DefaultResolveRules: production now
		// auto-resolves sessions/, so this fixture would reconcile cleanly and
		// never produce an Issue to escalate. The subject under test is the
		// escalation MATH surviving a daemon restart, which applies to every
		// wedge type; drive it with rules that leave the conflict unresolved so
		// there is something to escalate. Coverage that production rules now
		// resolve this fixture lives in
		// TestSessionMetaConflict_AutoResolvesUnderProductionRules.
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	require.NotNil(t, result.Err, "the conflicting rebase must surface as a pull error, not a silent success")
	require.NotNil(t, result.Issue, "a sessions/ content conflict must produce a classified issue")
	assert.Equal(t, IssueTypeSessionConflictWedge, result.Issue.Type,
		"must be classified distinctly from generic IssueTypeDiverged so sync.go can escalate it by elapsed time")
	assert.Equal(t, "ledger", result.Issue.Repo)
	assert.False(t, result.AutoResolved, "sessions/ must never auto-resolve — it's hard-denied by design")

	// the rebase must actually be cleared (AuditAndAbort ran), not left wedged
	// mid-rebase — that's a separate, already-covered failure mode
	// (sync_rebase_recovery_test.go); this test only proves classification.
	assert.NoDirExists(t, filepath.Join(localDir, ".git", "rebase-merge"),
		"AuditAndAbort should have cleared the rebase-merge dir")
}

// --- B. Severity escalation by elapsed time ---
//
// escalateSessionConflictSeverity must escalate purely as a function of how
// long the (Type, Repo) issue has existed, using IssueTracker.SetIssue's
// existing Since-preservation — not a separate timer that resets on daemon
// restart (internal/daemon/workspace_registry.go's in-memory SyncFailures
// counter has exactly that fragility, which is why this doesn't reuse it).
// Failure prevented: a genuinely stuck conflict retrying forever at
// SeverityWarning, indistinguishable from a conflict that just started.

func TestEscalateSessionConflictSeverity_EscalatesByElapsedTime(t *testing.T) {
	cases := []struct {
		name         string
		age          time.Duration
		wantSeverity string
		wantConfirm  bool
	}{
		{"fresh_no_prior_issue", 0, SeverityWarning, false},
		{"under_6h", 5 * time.Hour, SeverityWarning, false},
		{"exactly_6h", 6 * time.Hour, SeverityError, true},
		{"under_24h", 23 * time.Hour, SeverityError, true},
		{"exactly_24h", 24 * time.Hour, SeverityCritical, true},
		{"well_past_24h", 72 * time.Hour, SeverityCritical, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewIssueTracker()
			if tc.age > 0 {
				tracker.SetIssue(DaemonIssue{
					Type:     IssueTypeSessionConflictWedge,
					Repo:     "ledger",
					Severity: SeverityWarning,
					Summary:  "seed",
					Since:    time.Now().Add(-tc.age),
				})
			}

			issue := DaemonIssue{
				Type:    IssueTypeSessionConflictWedge,
				Repo:    "ledger",
				Summary: "1 session(s) have unresolvable meta.json conflicts",
			}
			// no git-plumbing signal here — this test is exercising the
			// pure tracker-based path in isolation (see the restart test
			// below for the combined-signal path with a real git fixture).
			escalateSessionConflictSeverity(tracker, &issue, 0)

			assert.Equal(t, tc.wantSeverity, issue.Severity)
			assert.Equal(t, tc.wantConfirm, issue.RequiresConfirm)
		})
	}
}

// TestEscalateSessionConflictSeverity_PreservesSinceAcrossCycles proves the
// full loop a real sync cycle drives: classify, escalate, SetIssue, repeat —
// Since must anchor to the FIRST detection, not reset on every retry (which
// would keep a permanently-stuck conflict stuck at SeverityWarning forever).
func TestEscalateSessionConflictSeverity_PreservesSinceAcrossCycles(t *testing.T) {
	tracker := NewIssueTracker()

	firstIssue := DaemonIssue{Type: IssueTypeSessionConflictWedge, Repo: "ledger", Summary: "1 session(s) have unresolvable meta.json conflicts"}
	escalateSessionConflictSeverity(tracker, &firstIssue, 0)
	tracker.SetIssue(firstIssue)

	firstSeen, ok := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	require.True(t, ok)
	assert.Equal(t, SeverityWarning, firstSeen.Severity)

	// simulate the issue having existed for 7 hours by backdating Since directly
	tracker.SetIssue(DaemonIssue{
		Type:     IssueTypeSessionConflictWedge,
		Repo:     "ledger",
		Severity: firstSeen.Severity,
		Summary:  firstSeen.Summary,
		Since:    firstSeen.Since.Add(-7 * time.Hour),
	})

	// next sync cycle re-detects the identical, still-unresolved conflict
	secondIssue := DaemonIssue{Type: IssueTypeSessionConflictWedge, Repo: "ledger", Summary: "1 session(s) have unresolvable meta.json conflicts"}
	escalateSessionConflictSeverity(tracker, &secondIssue, 0)
	tracker.SetIssue(secondIssue)

	assert.Equal(t, SeverityError, secondIssue.Severity, "7h-old conflict must have escalated past the 6h threshold")
	assert.True(t, secondIssue.RequiresConfirm)

	final, ok := tracker.GetIssue(IssueTypeSessionConflictWedge, "ledger")
	require.True(t, ok)
	assert.Equal(t, SeverityError, final.Severity, "escalated severity must actually persist in the tracker, not just the local variable")
}

// --- C. Severity escalation survives a daemon restart ---
//
// internal/daemon/issues.go's IssueTracker is pure in-memory — daemon.go
// constructs `d.issues = NewIssueTracker()` on every daemon startup, with no
// persistence and no reload. A restart mid-incident (crash, `ox upgrade`,
// reboot, manual `ox daemon restart` — all realistic over a multi-hour
// wedge) therefore replaces a long-lived tracker with a brand-new empty
// one. Before this fix, the next detection of the SAME still-unresolved
// session conflict called GetIssue on the fresh tracker, got (zero-value,
// false), computed elapsed=0, and silently reset severity to Warning —
// indistinguishable from a conflict that just started. Failure prevented: a
// multi-hour incident going quiet (and un-escalated) purely because the
// daemon happened to restart.

// TestEscalateSessionConflictSeverity_SurvivesDaemonRestart proves severity
// is anchored to the restart-durable git-commit-timestamp signal
// (oldestUnpushedCommitAge / ManagedRepoPullResult.SessionConflictAge), not
// solely to the in-memory tracker. It runs the real sessions/*/meta.json
// conflict fixture through pullManagedRepo with a backdated local commit,
// then evaluates escalation against a completely fresh IssueTracker — the
// exact shape of a daemon restart — and asserts severity is still Critical.
func TestEscalateSessionConflictSeverity_SurvivesDaemonRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	localDir := makeSessionMetaConflictClone(t)

	// Backdate the unpushed local commit well past the 24h Critical
	// threshold. This is the one fact a restarted daemon can still
	// observe — the tracker's Since cannot, since it no longer exists.
	const backdateAge = 30 * time.Hour
	backdateCommitTimestamp(t, localDir, -backdateAge)

	s := newTestScheduler(t.TempDir())
	result := s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath: localDir,
		RepoName: "ledger",
		// Deliberately NOT ledger.DefaultResolveRules: production now
		// auto-resolves sessions/, so this fixture would reconcile cleanly and
		// never produce an Issue to escalate. The subject under test is the
		// escalation MATH surviving a daemon restart, which applies to every
		// wedge type; drive it with rules that leave the conflict unresolved so
		// there is something to escalate. Coverage that production rules now
		// resolve this fixture lives in
		// TestSessionMetaConflict_AutoResolvesUnderProductionRules.
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})
	require.NotNil(t, result.Issue)
	require.Equal(t, IssueTypeSessionConflictWedge, result.Issue.Type)
	require.GreaterOrEqual(t, result.SessionConflictAge, 24*time.Hour,
		"pullManagedRepo must surface the backdated commit's real age on the result")

	// Sanity check: a tracker that has known about this issue the whole
	// time (no restart) already reports Critical — proves the fixture and
	// thresholds are set up correctly before testing the restart case.
	longLivedTracker := NewIssueTracker()
	longLivedTracker.SetIssue(DaemonIssue{
		Type:     IssueTypeSessionConflictWedge,
		Repo:     "ledger",
		Severity: SeverityWarning,
		Summary:  "seed",
		Since:    time.Now().Add(-backdateAge),
	})
	beforeRestart := *result.Issue
	escalateSessionConflictSeverity(longLivedTracker, &beforeRestart, result.SessionConflictAge)
	require.Equal(t, SeverityCritical, beforeRestart.Severity, "sanity check: an uninterrupted tracker already reports Critical")

	// The actual regression: a brand-new tracker — exactly what
	// daemon.go's NewIssueTracker() constructs on every restart — sees
	// this identical, still-unresolved conflict for what looks like the
	// first time. Without the git-plumbing floor, GetIssue would return
	// (zero-value, false), elapsed would compute as 0, and severity would
	// silently reset to Warning.
	freshTracker := NewIssueTracker()
	afterRestart := *result.Issue
	escalateSessionConflictSeverity(freshTracker, &afterRestart, result.SessionConflictAge)

	assert.Equal(t, SeverityCritical, afterRestart.Severity,
		"severity must not silently drop after a daemon restart while the underlying conflict remains unresolved")
	assert.True(t, afterRestart.RequiresConfirm)
}

// --- D. oldestUnpushedCommitAge (git-plumbing age primitive) ---
//
// Direct coverage of the primitive escalateSessionConflictSeverity's
// restart-durable signal is built on, independent of the full
// pullManagedRepo conflict pipeline exercised above.

func TestOldestUnpushedCommitAge_NoUnpushedCommits_ReturnsNotOK(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	_, cloneDir := gcInitBareAndClone(t, t.TempDir())

	age, ok := oldestUnpushedCommitAge(context.Background(), cloneDir)
	assert.False(t, ok, "a clone with nothing unpushed must report no signal")
	assert.Zero(t, age)
}

func TestOldestUnpushedCommitAge_PicksOldestAcrossMultipleUnpushedCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	isolateCredentials(t)
	_, cloneDir := gcInitBareAndClone(t, t.TempDir())

	// two unpushed commits: an older one (backdated), then a newer one on
	// top. The function must report the OLDER commit's age, not the
	// newer one's — a naive implementation (e.g. reading only the first
	// or last `git log` line without regard to order) would get this
	// backwards.
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "older.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "older").Run())
	backdateCommitTimestamp(t, cloneDir, -10*time.Hour)

	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "newer.txt"), []byte("x"), 0o644))
	require.NoError(t, exec.Command("git", "-C", cloneDir, "add", "-A").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "-m", "newer").Run())

	age, ok := oldestUnpushedCommitAge(context.Background(), cloneDir)
	require.True(t, ok)
	assert.GreaterOrEqual(t, age, 10*time.Hour, "must report the OLDEST unpushed commit's age, not the newest")
}

// TestSessionMetaConflict_AutoResolvesUnderProductionRules is the counterpart
// to the escalation test above: under the REAL production rules, the same
// sessions/*/meta.json fixture must reconcile cleanly instead of raising a
// wedge issue.
//
// Failure prevented: sessions/ silently dropping out of
// ledger.DefaultResolveRules would restore the 13-day production wedge, and
// every other test in this file would still pass because they pin the
// escalation math rather than the resolve outcome.
func TestSessionMetaConflict_AutoResolvesUnderProductionRules(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	localDir := makeSessionMetaConflictClone(t)

	s := newTestScheduler(t.TempDir())
	result := s.pullManagedRepo(context.Background(), ManagedRepoPullOpts{
		RepoPath:     localDir,
		RepoName:     "ledger",
		ResolveRules: ledger.DefaultResolveRules,
		Logger:       discardLogger(),
	})

	require.Nil(t, result.Issue,
		"sessions/ conflicts must auto-resolve under production rules, not wedge")
	require.False(t, gitutil.IsRebaseInProgress(localDir),
		"no rebase may be left in progress after a successful reconcile")
}

// A successful pull can leave autostash conflicts without a rebase in progress.
// Both the first pull and later cycles must recover agreeing metadata, while
// preserving genuinely different values for manual resolution.
func TestPullManagedRepo_AutostashConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git pull and autostash")
	}
	for _, tc := range []struct {
		name        string
		preexisting bool
		localTitle  string
		wantError   bool
	}{
		{"new agreeing conflict", false, "Recovered", false},
		{"existing agreeing conflict", true, "Recovered", false},
		{"new differing conflict", false, "Local title", true},
		{"existing differing conflict", true, "Local title", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCredentials(t)
			bare, writer := initBareRepo(t, "autostash")
			const rel = "sessions/test/meta.json"
			require.NoError(t, os.MkdirAll(filepath.Join(writer, "sessions/test"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(writer, rel), []byte(`{"session_id":"s","title":""}`+"\n"), 0o644))
			gitInDir(t, writer, "add", "--sparse", rel)
			gitInDir(t, writer, "commit", "-m", "seed")
			gitInDir(t, writer, "push", "origin", "main")
			local := filepath.Join(t.TempDir(), "local")
			gitInDir(t, writer, "clone", bare, local)
			gitConfig(t, local)
			require.NoError(t, os.WriteFile(filepath.Join(writer, rel), []byte(`{"session_id":"s","title":"Recovered"}`+"\n"), 0o644))
			gitInDir(t, writer, "commit", "-am", "remote title repair")
			gitInDir(t, writer, "push", "origin", "main")
			localMeta := `{"title":"` + tc.localTitle + `","session_id":"s","summary_attempts":0,"validation_error":""}` + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(local, rel), []byte(localMeta), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(local, "unrelated.txt"), []byte("keep me"), 0o644))
			if tc.preexisting {
				out, err := runGitOut(t, local, "pull", "--rebase", "--autostash", "--quiet")
				require.NoError(t, err, out)
				require.Contains(t, out, "Applying autostash resulted in conflicts")
			}
			s := newTestScheduler(t.TempDir())
			opts := ManagedRepoPullOpts{RepoPath: local, RepoName: "ledger", ResolveRules: ledger.DefaultResolveRules, Logger: discardLogger()}
			result := s.pullManagedRepo(context.Background(), opts)
			assert.False(t, gitutil.IsRebaseInProgress(local))
			conflicts, err := runGitOut(t, local, "ls-files", "--unmerged")
			require.NoError(t, err)
			if tc.wantError {
				require.Error(t, result.Err)
				require.NotNil(t, result.Issue)
				assert.Equal(t, IssueTypeMergeConflict, result.Issue.Type)
				assert.NotEmpty(t, conflicts)
			} else {
				require.NoError(t, result.Err)
				require.Nil(t, result.Issue)
				assert.True(t, result.AutoResolved)
				assert.Empty(t, conflicts)
				meta, err := os.ReadFile(filepath.Join(local, rel))
				require.NoError(t, err)
				assert.JSONEq(t, localMeta, string(meta))
				// Recovery must allow a later remote update to arrive too.
				require.NoError(t, os.WriteFile(filepath.Join(writer, "next.txt"), []byte("next pull"), 0o644))
				gitInDir(t, writer, "add", "next.txt")
				gitInDir(t, writer, "commit", "-m", "next remote update")
				gitInDir(t, writer, "push", "origin", "main")
				old := time.Now().Add(-time.Hour)
				require.NoError(t, os.Chtimes(filepath.Join(local, ".git/FETCH_HEAD"), old, old))
				result = newTestScheduler(t.TempDir()).pullManagedRepo(context.Background(), opts)
				require.NoError(t, result.Err)
				assert.FileExists(t, filepath.Join(local, "next.txt"))
			}
			stash, err := runGitOut(t, local, "stash", "list")
			require.NoError(t, err)
			assert.Contains(t, stash, "autostash", "retain the original local changes")
			unrelated, err := os.ReadFile(filepath.Join(local, "unrelated.txt"))
			require.NoError(t, err)
			assert.Equal(t, "keep me", string(unrelated))
		})
	}
}
