package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/doctor/autofix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isPartialClone gates checkAndRunGC's "full clone upgrade" trigger, which
// bypasses the GC interval entirely. When it always answers false, every team
// context is re-cloned on each hourly GC cycle, in every running daemon,
// forever — and each reclone discards the workspace's .sageox/cache/.
//
// Failure prevented: detecting partial clones by `extensions.partialClone`
// alone. Git 2.50 records `clone --filter` as remote.<name>.promisor and never
// writes that extension key, so the check could not return true for any clone
// ox creates.
func TestIsPartialClone_DetectsFilterClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	source := filepath.Join(root, "source")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.MkdirAll(source, 0o755))
	run(root, "init", "--bare", "--initial-branch=main", origin)
	run(root, "-C", origin, "config", "uploadpack.allowFilter", "true")
	run(source, "init", "--initial-branch=main")
	run(source, "config", "user.name", "Test")
	run(source, "config", "user.email", "partial-clone-test@test.sageox.ai")
	run(source, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte("team\n"), 0o600))
	run(source, "add", "AGENTS.md")
	run(source, "commit", "-m", "seed")
	run(source, "remote", "add", "origin", origin)
	run(source, "push", "origin", "main")

	partial := filepath.Join(root, "partial")
	run(root, "clone", "--filter=blob:none", origin, partial)

	// Precondition, asserted through git rather than the function under test:
	// this really is a partial clone, however git chose to record it.
	promisor := exec.Command("git", "-C", partial, "config", "--get", "remote.origin.promisor")
	out, err := promisor.Output()
	require.NoError(t, err, "fixture must be a partial clone")
	require.Equal(t, "true", string(out[:len(out)-1]))

	assert.True(t, isPartialClone(partial),
		"a --filter clone must be recognized as partial, or GC re-clones it every cycle")

	full := filepath.Join(root, "full")
	run(root, "clone", origin, full)
	assert.False(t, isPartialClone(full), "a full clone must still be reported as full")
}

// LastError feeds the "Last error: ✗ ..." line. recentErrors is bounded by
// count, not age, so without a window a single transient failure stayed on
// screen indefinitely — directly beside the "0 errors" that RecentErrorCount
// reports from the very same slice.
func TestLastError_WindowedToMatchRecentErrorCount(t *testing.T) {
	t.Parallel()
	s := &SyncScheduler{maxRecentErrs: 10}

	s.recordError("ledger", "fetch failed: Could not resolve host: git.example.ai")
	msg, when := s.LastError()
	assert.NotEmpty(t, msg, "a fresh error must be reported")
	assert.False(t, when.IsZero())
	assert.Equal(t, 1, s.RecentErrorCount())

	// Age it past the window, as a laptop-sleep DNS blip ages overnight.
	s.mu.Lock()
	s.recentErrors[0].Time = time.Now().Add(-recentErrorWindow - time.Minute)
	s.mu.Unlock()

	msg, when = s.LastError()
	assert.Empty(t, msg, "a stale error must not be displayed as the current state")
	assert.True(t, when.IsZero())
	assert.Equal(t, 0, s.RecentErrorCount(),
		"LastError and RecentErrorCount must never disagree about what is recent")
}

// A ledger wedged on a session conflict that auto-resolve refuses, then fixed
// by hand, must stop reporting the wedge on the next cycle that finds it
// healthy.
//
// Failure prevented: `ox daemon status` showing "Last error: field title
// differs ...", "has unresolved conflicts" and "Sync suspended after 3
// consecutive failures" beside a ledger it reported as synced seconds ago. The
// pull that wedged had already moved HEAD to the remote tip, so until the
// remote moves again every cycle skips as "remote unchanged", and only a
// completed pull cleared those issues; the error log was never cleared at all,
// only aged out after an hour.
func TestDoPull_FixedWedgeStopsReportingItsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git conflict and recovery")
	}
	for _, tc := range []struct {
		name string
		// remoteAdvances pushes an unrelated commit after the fix, so the
		// recovery cycle runs a real pull instead of the "remote unchanged" skip.
		remoteAdvances bool
	}{
		{name: "remote unchanged since the fix"},
		{name: "remote advanced since the fix", remoteAdvances: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledgerDir := filepath.Join(t.TempDir(), "ledger")
			require.NoError(t, os.MkdirAll(ledgerDir, 0o755))
			setupGitRepo(t, ledgerDir)
			remote := bareRepoPath(ledgerDir)
			s := newPullTestScheduler(t, ledgerDir)
			ctx := context.Background()

			const rel = "sessions/s1/meta.json"
			require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions", "s1"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, rel), []byte(`{"session_id":"s1","title":""}`+"\n"), 0o644))
			gitCmd(t, ledgerDir, "add", rel)
			gitCmd(t, ledgerDir, "commit", "-m", "seed session")
			gitCmd(t, ledgerDir, "push", "origin", "HEAD:main")

			// Two writers title the same session differently. Auto-resolve
			// refuses to choose, so the pull leaves an unmerged autostash.
			pushFromSeparateClone(t, remote, rel, `{"session_id":"s1","title":"Remote title"}`+"\n")
			local := []byte(`{"session_id":"s1","title":"Local title"}` + "\n")
			require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, rel), local, 0o644))

			err := s.doPull(ctx, nil, false, false)
			require.Error(t, err)
			require.Contains(t, err.Error(), "field title differs")
			_, conflicted := s.issues.GetIssue(IssueTypeMergeConflict, "ledger")
			require.True(t, conflicted, "the wedge must be reported while it lasts")
			lastErr, _ := s.LastError()
			require.Contains(t, lastErr, "field title differs")

			// The next cycle lands inside the failure backoff.
			require.NoError(t, s.doPull(ctx, nil, false, false))
			_, backedOff := s.issues.GetIssue(IssueTypeSyncBackoff, "ledger")
			require.True(t, backedOff, "the backoff must be reported while it lasts")

			// The backoff elapses before anyone fixes it: the retry must fail
			// and keep everything it reported.
			s.workspaceRegistry.workspaces["ledger"].NextSyncAttempt = time.Now().Add(-time.Minute)
			require.Error(t, s.doPull(ctx, nil, false, false))
			_, conflicted = s.issues.GetIssue(IssueTypeMergeConflict, "ledger")
			require.True(t, conflicted, "an unresolved wedge must stay reported")
			require.Equal(t, 2, s.RecentErrorCount(), "each failed cycle must stay recorded")

			// A coworker keeps the local title and drops the autostash.
			require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, rel), local, 0o644))
			gitCmd(t, ledgerDir, "add", rel)
			gitCmd(t, ledgerDir, "stash", "drop")
			if tc.remoteAdvances {
				pushFromSeparateClone(t, remote, "next.txt", "after the fix\n")
				// A scheduled retry runs outside the cross-daemon fetch dedup window.
				old := time.Now().Add(-time.Hour)
				require.NoError(t, os.Chtimes(filepath.Join(ledgerDir, ".git", "FETCH_HEAD"), old, old))
			}

			// The backoff elapses and the scheduled retry finds the ledger healthy.
			s.workspaceRegistry.workspaces["ledger"].NextSyncAttempt = time.Now().Add(-time.Minute)
			require.NoError(t, s.doPull(ctx, nil, false, false))

			assert.Empty(t, s.issues.GetIssues(), "a fixed ledger has no issue left to report")
			lastErr, _ = s.LastError()
			assert.Empty(t, lastErr, "the wedge's error must not outlive the fix")
			assert.Zero(t, s.RecentErrorCount(), "fixed errors must not keep the status at Warning")
		})
	}
}

// A ledger whose rebase still fails must keep reporting it through a cycle
// that skips the pull because the clone was fetched moments earlier, and stop
// only once HEAD is back at the remote tip.
//
// Failure prevented: a fresh FETCH_HEAD retiring a divergence that still
// exists. The fetch that wrote it belongs to the pull whose rebase just
// failed, so that skip proves nothing about the divergence or its error.
func TestDoPull_UnresolvedDivergenceOutlivesARecentFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git divergence")
	}
	ledgerDir := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0o755))
	setupGitRepo(t, ledgerDir)
	remote := bareRepoPath(ledgerDir)
	s := newPullTestScheduler(t, ledgerDir)
	ctx := context.Background()

	// Local and remote commits edit the same file outside every auto-resolve
	// path, so the rebase fails and is aborted.
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "src", "main.go"), []byte("package main // local\n"), 0o644))
	gitCmd(t, ledgerDir, "add", "src/main.go")
	gitCmd(t, ledgerDir, "commit", "-m", "local edit")
	other := filepath.Join(t.TempDir(), "other")
	gitCmd(t, t.TempDir(), "clone", remote, other)
	require.NoError(t, os.MkdirAll(filepath.Join(other, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "src", "main.go"), []byte("package main // remote\n"), 0o644))
	gitCmd(t, other, "add", "src/main.go")
	gitCmd(t, other, "commit", "-m", "remote edit")
	gitCmd(t, other, "push", "origin", "HEAD")

	require.Error(t, s.doPull(ctx, nil, false, false))
	_, diverged := s.issues.GetIssue(IssueTypeDiverged, "ledger")
	require.True(t, diverged, "the divergence must be reported while it lasts")

	// `ox sync` moments later. HEAD is not the remote tip, and a conflicting
	// pull cannot succeed, so a new last-sync time can only come from the
	// "recently fetched" skip.
	require.NotEqual(t, gitCmd(t, ledgerDir, "rev-parse", "HEAD"), gitCmd(t, ledgerDir, "rev-parse", "origin/main"))
	before := s.LastSync()
	require.NoError(t, s.doPull(ctx, nil, true, false))
	require.True(t, s.LastSync().After(before), "this cycle must be the recently-fetched skip")
	_, diverged = s.issues.GetIssue(IssueTypeDiverged, "ledger")
	assert.True(t, diverged, "a recent fetch says nothing about a divergence that still exists")
	assert.Equal(t, 1, s.RecentErrorCount(), "nor about the error it caused")

	// A coworker merges by hand, keeping the local edit, and pushes: HEAD is
	// the remote tip again.
	gitCmd(t, ledgerDir, "merge", "-X", "ours", "-m", "keep local edit", "origin/main")
	gitCmd(t, ledgerDir, "push", "origin", "HEAD:main")
	require.NoError(t, s.doPull(ctx, nil, false, false))
	assert.Empty(t, s.issues.GetIssues(), "a resolved divergence has no issue left to report")
	lastErr, _ := s.LastError()
	assert.Empty(t, lastErr, "the divergence's error must not outlive the fix")
}

// A team context whose clone failed, and that was cloned later, must stop
// reporting the clone failure once it syncs.
//
// Failure prevented: the clone error was recorded under the repo type, a key
// that nothing proving the team context healthy ever cleared, so "Last error:
// clone team-context failed" and the Warning it caused stayed for up to an
// hour beside a team context that was syncing.
func TestPullTeamContext_ClonedTeamStopsReportingItsCloneFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git clone and pull")
	}
	for _, tc := range []struct {
		name string
		// remoteAdvances pushes a commit after the clone, so the sync runs a
		// real pull instead of the "remote unchanged" skip.
		remoteAdvances bool
	}{
		{name: "remote unchanged since the clone"},
		{name: "remote advanced since the clone", remoteAdvances: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCredentials(t)
			s := newTestScheduler(t.TempDir())
			s.issues = NewIssueTracker()
			teamDir := filepath.Join(t.TempDir(), "team_recovers")

			// The first clone cannot reach the server.
			_, err := s.Checkout(CheckoutPayload{CloneURL: "http://127.0.0.1:1/team.git", RepoPath: teamDir, RepoType: "team-context"}, nil)
			require.Error(t, err)
			lastErr, _ := s.LastError()
			require.Contains(t, lastErr, "clone team-context failed", "the clone failure must be reported while it lasts")

			// A later clone succeeds.
			bare, writer := initBareRepo(t, "team")
			require.NoError(t, os.WriteFile(filepath.Join(writer, "TEAM.md"), []byte("team\n"), 0o644))
			gitInDir(t, writer, "add", "TEAM.md")
			gitInDir(t, writer, "commit", "-m", "seed")
			gitInDir(t, writer, "push", "origin", "main")
			require.NoError(t, os.RemoveAll(teamDir))
			gitInDir(t, filepath.Dir(teamDir), "clone", bare, teamDir)
			if tc.remoteAdvances {
				require.NoError(t, os.WriteFile(filepath.Join(writer, "NEXT.md"), []byte("next\n"), 0o644))
				gitInDir(t, writer, "add", "NEXT.md")
				gitInDir(t, writer, "commit", "-m", "next")
				gitInDir(t, writer, "push", "origin", "main")
			}

			_, err = s.pullTeamContext(context.Background(), teamDir)
			require.NoError(t, err)
			lastErr, _ = s.LastError()
			assert.Empty(t, lastErr, "the clone failure must not outlive a team context that syncs")
			assert.Zero(t, s.RecentErrorCount())
		})
	}
}

// An API-discovered team context must NOT be written into config.local.toml.
//
// Failure prevented: a revoked team coming back. CleanupRevokedTeamContexts
// removes the in-memory workspace and the on-disk checkout but never edits
// config.local.toml, and LoadFromConfig recreates a workspace from any entry
// it finds — so a config entry written on a successful sync would resurrect a
// team whose access was revoked, and it would resume cloning and syncing.
//
// Recording last-sync for these teams is handled by sync-state.json in the
// checkout, which survives a GC reclone (see the cache preservation in
// runBlueGreenGCOpts) and is what a non-owner daemon reads regardless.
func TestUpdateTeamContextLastSync_DoesNotPersistAPIDiscoveredTeam(t *testing.T) {
	t.Parallel()
	cfg := &config.LocalConfig{}

	cfg.UpdateTeamContextLastSync("team_api_discovered")
	assert.Empty(t, cfg.TeamContexts,
		"persisting an API-discovered team would let a revoked team survive cleanup")

	// A team the user is actually configured for still records normally.
	cfg.TeamContexts = []config.TeamContext{{TeamID: "team_configured", TeamName: "Configured"}}
	cfg.UpdateTeamContextLastSync("team_configured")
	require.Len(t, cfg.TeamContexts, 1)
	assert.True(t, cfg.TeamContexts[0].HasLastSync(), "a configured team must still record last_sync")

	// An empty team ID is not an identity: it must not match an entry that
	// happens to carry an empty TeamID and stamp it as freshly synced.
	cfg.TeamContexts = []config.TeamContext{{TeamID: "", TeamName: "Malformed"}}
	cfg.UpdateTeamContextLastSync("")
	assert.False(t, cfg.TeamContexts[0].HasLastSync(),
		"an empty team ID must not be treated as a match")
}

// routeAutofixResult is the only thing that writes or retires an "autofix_*"
// issue, so both directions are load-bearing.
//
// Failure prevented: a repair reporting itself as an outstanding problem, and
// an issue that no code path could ever clear. `ox doctor` — which the
// accompanying hint tells the user to run — cannot clear daemon issues at all,
// so before this the only cure was restarting the daemon.
func TestRouteAutofixResult_RetiresResolvedIssues(t *testing.T) {
	t.Parallel()
	d := &Daemon{issues: NewIssueTracker()}
	const repo = "/repo"

	// A check that needs a human opens an issue.
	d.routeAutofixResult(autofix.CheckResult{
		Slug: "ledger-sacred-deletion", Status: autofix.StatusFound,
		Repo: repo, Summary: "deletion history",
	})
	require.Equal(t, 1, d.issues.Count())
	got, ok := d.issues.GetIssue("autofix_ledger-sacred-deletion", repo)
	require.True(t, ok)
	assert.Equal(t, SeverityWarning, got.Severity)

	// The next pass finds nothing wrong — the issue must be retired.
	d.routeAutofixResult(autofix.CheckResult{
		Slug: "ledger-sacred-deletion", Status: autofix.StatusClean, Repo: repo,
	})
	assert.Equal(t, 0, d.issues.Count(), "a Clean pass must retire the issue it previously raised")

	// A successful repair is an event, not an open issue.
	d.routeAutofixResult(autofix.CheckResult{
		Slug: "session-meta-titles", Status: autofix.StatusFixed,
		Repo: repo, Summary: "session meta titles: recovered=1",
	})
	assert.Equal(t, 0, d.issues.Count(), "a completed repair must not be filed as a warning")

	// A broken check is still escalated.
	d.routeAutofixResult(autofix.CheckResult{
		Slug: "session-meta-titles", Status: autofix.StatusError,
		Repo: repo, Summary: "read sessions dir: permission denied",
	})
	require.Equal(t, 1, d.issues.Count())
	errIssue, ok := d.issues.GetIssue("autofix_session-meta-titles", repo)
	require.True(t, ok)
	assert.Equal(t, SeverityError, errIssue.Severity)
}
