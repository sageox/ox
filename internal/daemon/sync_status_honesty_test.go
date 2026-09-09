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

// Team contexts discovered from the repo-detail API have no config entry, so
// the plain update looped over an empty slice and returned silently while its
// caller saved the file and reported success. last_sync was unrecordable for
// exactly those teams.
func TestUpsertTeamContextLastSync_AddsMissingTeam(t *testing.T) {
	t.Parallel()
	cfg := &config.LocalConfig{}

	cfg.UpdateTeamContextLastSync("team_api_discovered")
	assert.Empty(t, cfg.TeamContexts,
		"the plain update must stay a no-op for an unknown team (callers rely on it not inventing entries without identity)")

	cfg.UpsertTeamContextLastSync("team_api_discovered", "Discovered", "discovered", "/tmp/team")
	require.Len(t, cfg.TeamContexts, 1, "an API-discovered team must become recordable")
	assert.Equal(t, "team_api_discovered", cfg.TeamContexts[0].TeamID)
	assert.Equal(t, "Discovered", cfg.TeamContexts[0].TeamName)
	assert.Equal(t, "/tmp/team", cfg.TeamContexts[0].Path)
	require.True(t, cfg.TeamContexts[0].HasLastSync())
	first := cfg.TeamContexts[0].LastSync

	// A second sync updates in place rather than appending a duplicate.
	cfg.UpsertTeamContextLastSync("team_api_discovered", "Ignored", "ignored", "/tmp/other")
	require.Len(t, cfg.TeamContexts, 1, "upsert must not duplicate an existing team")
	assert.Equal(t, "Discovered", cfg.TeamContexts[0].TeamName, "identity fields seed only on create")
	assert.False(t, cfg.TeamContexts[0].LastSync.Before(first))

	// An empty team ID is not an identity and must never be persisted.
	cfg.UpsertTeamContextLastSync("", "", "", "")
	assert.Len(t, cfg.TeamContexts, 1)
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
