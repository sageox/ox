package daemon

import (
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

	s.recordError("fetch failed: Could not resolve host: git.example.ai")
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
