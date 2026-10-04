package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The incident these tests pin down: a laptop slept for four minutes, two
// team-context pulls failed with "Could not resolve host", and background
// team-context sync stopped for five hours on a machine that was awake and
// online the whole time. Nothing in the daemon un-suspends; only
// `ox sync --all-teams` or a restart revived it.

func TestIsTransientSyncError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// Verbatim from the incident.
			name: "dns could not resolve host",
			err:  errors.New(`fetch failed: fatal: unable to access 'https://git.example.ai/t/team-context.git/': Could not resolve host: git.example.ai: exit status 128`),
			want: true,
		},
		{"dns resolving timed out", errors.New("curl: (28) Resolving timed out after 5000 milliseconds"), true},
		{"no such host", errors.New("dial tcp: lookup git.example.ai: no such host"), true},
		{"connection refused", errors.New("failed to connect to git.example.ai port 443: Connection refused"), true},
		{"connection timed out", errors.New("Connection timed out after 30001 milliseconds"), true},
		{"network unreachable", errors.New("connect: Network is unreachable"), true},
		{"i/o timeout", errors.New("read tcp 10.0.0.1:443: i/o timeout"), true},
		{"gateway", errors.New("unable to access: The requested URL returned error: 502 Bad Gateway"), true},
		{"case insensitive", errors.New("COULD NOT RESOLVE HOST: git.example.ai"), true},

		// Local failures MUST stay eligible for suspension — that is the
		// issue #767 case the fingerprint guard exists for.
		{
			name: "lfs checkout failure is local",
			err:  errors.New("error: external filter 'git-lfs filter-process' failed; encountered 3 files that should have been pointers"),
			want: false,
		},
		{"rebase conflict is local", errors.New("error: could not apply 4870f54... CONFLICT (content): Merge conflict"), false},
		{"unstaged changes is local", errors.New("error: cannot pull with rebase: You have unstaged changes"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isTransientSyncError(tc.err))
		})
	}
}

// A network failure must leave the workspace on ordinary exponential backoff,
// which recovers on its own, rather than the permanent suspension that has no
// unattended exit.
func TestTransientFailure_UsesBackoffNotPermanentSuspension(t *testing.T) {
	t.Parallel()
	reg := newTestWorkspaceRegistry(t)
	const id = "team-test"
	reg.workspaces[id] = &WorkspaceState{ID: id}

	dns := errors.New("fetch failed: Could not resolve host: git.example.ai: exit status 128")
	require.True(t, isTransientSyncError(dns))

	// Two consecutive identical network failures — exactly what four minutes
	// of sleep produced. This is the branch doTeamSync takes for them.
	for range 2 {
		reg.RecordSyncFailure(id)
	}

	assert.False(t, reg.IsSyncSuspended(id),
		"a network outage must never permanently suspend sync")
	failures, next := reg.GetSyncRetryInfo(id)
	assert.Equal(t, 2, failures)
	assert.False(t, next.IsZero(), "backoff must schedule a retry; suspension zeroes it")
}

// A CLEAN worktree yields no fingerprint, so two failures against it cannot
// be mistaken for a deterministic local fault. sha256 of empty porcelain
// output is a universal constant, identical on every clean repo, so
// "the fingerprint repeated" was previously true for every read-only mirror.
func TestWorktreeFingerprint_EmptyForCleanTree(t *testing.T) {
	t.Parallel()
	repo := newFingerprintTestRepo(t)

	fp, err := worktreeFingerprint(context.Background(), repo)
	require.NoError(t, err)
	assert.Empty(t, fp, "a clean tree must not produce a fingerprint")

	// A dirty tree still fingerprints — issue #767's unbounded-autostash case
	// needs local changes to stash, and those must remain suspendable.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "local.txt"), []byte("uncommitted\n"), 0o600))
	dirtyFP, err := worktreeFingerprint(context.Background(), repo)
	require.NoError(t, err)
	assert.NotEmpty(t, dirtyFP, "a dirty tree must still fingerprint")
}

// The empty fingerprint has to actually reach the registry guard, not just be
// returned: RecordSyncFailureFingerprint("") must fall through to backoff.
func TestRecordSyncFailureFingerprint_EmptyNeverSuspends(t *testing.T) {
	t.Parallel()
	reg := newTestWorkspaceRegistry(t)
	const id = "team-test"
	reg.workspaces[id] = &WorkspaceState{ID: id}

	assert.False(t, reg.RecordSyncFailureFingerprint(id, ""))
	assert.False(t, reg.RecordSyncFailureFingerprint(id, ""),
		"a clean tree carries no evidence the checkout caused the failure")
	assert.False(t, reg.IsSyncSuspended(id))

	// Backoff, not suspension: a retry is scheduled and will fire on its own.
	// Suspension zeroes NextSyncAttempt, which is what made it permanent.
	_, next := reg.GetSyncRetryInfo(id)
	assert.False(t, next.IsZero(), "a retry must remain scheduled")
	reg.workspaces[id].NextSyncAttempt = time.Now().Add(-time.Minute)
	assert.True(t, reg.ShouldSync(id), "background sync must resume once backoff expires")
}

func newFingerprintTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "Test")
	run("config", "user.email", "fingerprint-test@test.sageox.ai")
	run("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o600))
	run("add", "base.txt")
	run("commit", "-m", "base")
	return repo
}

// TestDoTeamSync_NetworkOutageDoesNotSuspend drives the real decision path in
// doTeamSync — not the helpers it calls — because a helper-level test would
// pass even if the classification were never wired into the failure branch.
//
// Failure prevented: the reported incident. Two consecutive unresolvable-host
// pulls against a CLEAN team-context checkout permanently suspended background
// team sync, and no unattended path un-suspends it. Reverting either half of
// the fix turns the final assertion red.
func TestDoTeamSync_NetworkOutageDoesNotSuspend(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// Isolate from the developer's real SageOx state. Without this,
	// discoverTeams() reads live credentials and doTeamSync pulls the
	// machine's ACTUAL team contexts during the test run.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))

	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team-context")
	require.NoError(t, os.MkdirAll(teamPath, 0o755))

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(teamPath, "init", "--initial-branch=main")
	run(teamPath, "config", "user.name", "Test")
	run(teamPath, "config", "user.email", "team-sync-test@test.sageox.ai")
	run(teamPath, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "AGENTS.md"), []byte("team\n"), 0o600))
	run(teamPath, "add", "AGENTS.md")
	run(teamPath, "commit", "-m", "seed")
	// A closed loopback port refuses immediately and needs no DNS, so the
	// failure mode and latency don't depend on the CI resolver. The incident's
	// literal "Could not resolve host" text is covered without any network by
	// the TestIsTransientSyncError table.
	run(teamPath, "remote", "add", "origin", "https://127.0.0.1:1/team-context.git")

	const teamID = "team_transient"
	cfgTOML := "[[team_contexts]]\n" +
		"team_id = '" + teamID + "'\n" +
		"team_name = 'Transient Test'\n" +
		"path = '" + teamPath + "'\n" +
		"last_sync = 1970-01-01T00:00:00Z\n" +
		"last_gc = 1970-01-01T00:00:00Z\n"
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.local.toml"), []byte(cfgTOML), 0o600))

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectRoot
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	// The checkout is clean — the state every read-only team-context mirror is
	// in, and the state whose fingerprint is a constant. Asserted with git
	// directly, NOT via worktreeFingerprint: a precondition that calls the
	// function under test fails first when the fix is reverted, hiding which
	// assertion actually protects the claim.
	porcelain := exec.Command("git", "status", "--porcelain=v1", "--untracked-files=all")
	porcelain.Dir = teamPath
	dirty, err := porcelain.Output()
	require.NoError(t, err)
	require.Empty(t, string(dirty), "fixture must reproduce the clean-mirror case")

	// Retry the outage several times, as the ticker did across five hours.
	// More than two attempts are needed for a reason worth stating: the daemon
	// writes into the checkout on early passes (EnsureCheckoutGitignore), so
	// the worktree is not byte-identical until it settles. Once it does, every
	// further failure presents the same fingerprint — and that is the state
	// that used to suspend sync permanently.
	for attempt := 1; attempt <= 4; attempt++ {
		results, err := scheduler.doTeamSync(ctx, nil, false)
		require.NoError(t, err, "per-team failures are carried in results, not returned")

		// Assert the fixture is exercising the branch under test. Without this
		// the test would pass vacuously if the fixture's error ever came back
		// classified as LOCAL — it would then be taking the suspension path and
		// proving nothing about network failures.
		var observed string
		for _, r := range results {
			if r.TeamID == teamID {
				observed = r.Error
			}
		}
		require.NotEmpty(t, observed, "attempt %d must report a per-team error", attempt)
		require.True(t, isTransientSyncError(errors.New(observed)),
			"attempt %d: fixture must produce a NETWORK-shaped failure, got %q", attempt, observed)

		require.False(t, scheduler.workspaceRegistry.IsSyncSuspended(teamID),
			"attempt %d: a network outage must never permanently suspend team-context sync", attempt)

		failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo(teamID)
		require.Equal(t, attempt, failures,
			"attempt %d must still be attempted; a suspended workspace is skipped and never retried", attempt)

		// Expire the backoff so the next attempt runs without a real wait.
		// Nothing else is needed: the empty FETCH_HEAD a FAILED fetch leaves
		// behind no longer counts as a recent fetch (gitutil.FetchHeadAge), so
		// the failure cannot suppress its own retry.
		scheduler.workspaceRegistry.mu.Lock()
		scheduler.workspaceRegistry.workspaces[teamID].NextSyncAttempt = time.Now().Add(-time.Minute)
		scheduler.workspaceRegistry.mu.Unlock()
	}

	_, next := scheduler.workspaceRegistry.GetSyncRetryInfo(teamID)
	assert.False(t, next.IsZero(),
		"a retry must stay scheduled; suspension zeroes NextSyncAttempt and nothing unattended restores it")
}

// TestDoTeamSync_UnreadableIndexDoesNotSuspend drives doTeamSync's real failure
// classification for the #962 fix's own mirror image.
//
// Failure prevented: classifyAutostashFailure calls an index it cannot read
// "durable" and returns an error carrying gitutil.ErrConflictProbeFailed. But a
// probe can fail for reasons that are not the index at all — a git killed
// mid-run, a fork under resource pressure, a stalled filesystem — and the error
// is identical either way. That text matches no isTransientSyncError substring,
// so two cycles against the same dirty worktree set SyncSuspended and zeroed
// NextSyncAttempt: team-context sync stopped permanently on a healthy machine,
// the same ending as the #906 network blip, reached through a different door.
//
// The failure MUST still be visible while it retries — that is what
// IssueTypeRepoIntegrity is for, and TestPullManagedRepo_* pin it. This test
// pins only the other half: visible is not the same as suspended.
//
// Not parallel: the fixture edits the process-wide PATH and HOME.
func TestDoTeamSync_UnreadableIndexDoesNotSuspend(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// Isolate from the developer's real SageOx state, as
	// TestDoTeamSync_NetworkOutageDoesNotSuspend does and for the same reason:
	// discoverTeams() would otherwise read live credentials and sync the
	// machine's actual team contexts.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))

	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team-context")
	require.NoError(t, os.MkdirAll(teamPath, 0o755))

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(teamPath, "init", "--initial-branch=main")
	run(teamPath, "config", "user.name", "Test")
	run(teamPath, "config", "user.email", "probe-suspend-test@test.sageox.ai")
	run(teamPath, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "AGENTS.md"), []byte("team\n"), 0o600))
	run(teamPath, "add", "AGENTS.md")
	run(teamPath, "commit", "-m", "seed")
	run(teamPath, "remote", "add", "origin", "https://127.0.0.1:1/team-context.git")

	// A DIRTY checkout, unlike the network-outage test's clean one: a clean
	// tree fingerprints to "" and can never suspend, so a clean fixture would
	// pass vacuously. This is the state issue #767's guard was built for, and
	// the one this failure class must nonetheless stay out of.
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "local.txt"), []byte("uncommitted\n"), 0o600))

	// Every index probe fails, for a reason that is not the index. The pull
	// cycle's FIRST act is ResolveAutostashConflicts, so this aborts each cycle
	// before any fetch — no network is involved, and `git status` (the
	// fingerprint) keeps working, which is exactly what makes the suspension
	// branch reachable.
	gitShimFailingUnmergedProbe(t, 1_000_000)

	const teamID = "team_probe_failure"
	cfgTOML := "[[team_contexts]]\n" +
		"team_id = '" + teamID + "'\n" +
		"team_name = 'Probe Failure Test'\n" +
		"path = '" + teamPath + "'\n" +
		"last_sync = 1970-01-01T00:00:00Z\n" +
		"last_gc = 1970-01-01T00:00:00Z\n"
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.local.toml"), []byte(cfgTOML), 0o600))

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectRoot
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	// Four passes for the reason the network test gives: the daemon writes into
	// the checkout on early passes, so the worktree is not byte-identical until
	// it settles — and only once it does can two fingerprints match and suspend.
	for attempt := 1; attempt <= 4; attempt++ {
		results, err := scheduler.doTeamSync(ctx, nil, false)
		require.NoError(t, err, "per-team failures are carried in results, not returned")

		// Guard against a vacuous pass: if the fixture ever stopped producing a
		// probe failure, every assertion below would hold for the wrong reason.
		var observed string
		for _, r := range results {
			if r.TeamID == teamID {
				observed = r.Error
			}
		}
		require.NotEmpty(t, observed, "attempt %d must report a per-team error", attempt)
		require.Contains(t, observed, "read index for",
			"attempt %d: fixture must produce a PROBE failure, got %q", attempt, observed)
		require.False(t, isTransientSyncError(errors.New(observed)),
			"attempt %d: the point of this test is a failure the transient allowlist does NOT match", attempt)

		require.False(t, scheduler.workspaceRegistry.IsSyncSuspended(teamID),
			"attempt %d: an unreadable index must retry with backoff, never suspend permanently", attempt)

		failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo(teamID)
		require.Equal(t, attempt, failures,
			"attempt %d must still be attempted; a suspended workspace is skipped and never retried", attempt)

		// Expire the backoff so the next attempt runs without a real wait.
		scheduler.workspaceRegistry.mu.Lock()
		scheduler.workspaceRegistry.workspaces[teamID].NextSyncAttempt = time.Now().Add(-time.Minute)
		scheduler.workspaceRegistry.mu.Unlock()
	}

	_, next := scheduler.workspaceRegistry.GetSyncRetryInfo(teamID)
	assert.False(t, next.IsZero(),
		"a retry must stay scheduled; suspension zeroes NextSyncAttempt and nothing unattended restores it")
}

// --- The two-input retry decision (PR #974 review) ---
//
// doTeamSync has exactly two ways to record a failed cycle: ordinary bounded
// backoff, which recovers unattended, and permanent worktree-fingerprint
// suspension, which does not. Routing on the error alone cannot get this right,
// because classifyAutostashFailure JOINS gitutil.ErrConflictProbeFailed onto an
// error a pull already produced — so the identical sentinel appears in the case
// that must back off and in the case that must suspend.
func TestTeamFailureTakesBackoff(t *testing.T) {
	t.Parallel()

	network := errors.New("fetch failed: Could not resolve host: git.example.ai: exit status 128")
	// What classifyAutostashFailure produces when NOTHING ran: the pre-pull
	// probe died, result was zero-valued, and this is the whole error.
	prePullProbe := fmt.Errorf("read index for team-context: %w: git ls-files --unmerged: signal: killed",
		gitutil.ErrConflictProbeFailed)
	// What it produces when a pull DID run: errors.Join of the pull's own
	// deterministic failure and the follow-up probe failure. errors.Is matches
	// the sentinel through the join, which is exactly the trap.
	dirtyWorktree := errors.New("pull failed: error: cannot pull with rebase: You have unstaged changes: exit status 1")
	postPullJoined := errors.Join(dirtyWorktree, prePullProbe)

	tests := []struct {
		name    string
		err     error
		pullRan bool
		want    bool
		why     string
	}{
		{
			name: "network outage backs off", err: network, pullRan: false, want: true,
			why: "#906: suspending a laptop-sleep DNS failure stopped team sync for five hours",
		},
		{
			name: "network outage backs off even after a pull ran", err: network, pullRan: true, want: true,
			why: "a fetch that failed on DNS still resolves on its own",
		},
		{
			name: "pre-pull probe failure backs off", err: prePullProbe, pullRan: false, want: true,
			why: "nothing fetched, so no autostash entry accumulated; the durable case is loud as IssueTypeRepoIntegrity",
		},
		{
			name: "joined post-pull probe failure SUSPENDS", err: postPullJoined, pullRan: true, want: false,
			why: "the pull ran and repeats its local side effects — exactly #767's unbounded autostash pile",
		},
		{
			name: "deterministic local failure suspends", err: dirtyWorktree, pullRan: true, want: false,
			why: "the fingerprint guard's original case: a checkout no pull can settle",
		},
		{
			name: "lock-acquire failure before any pull still suspends", err: errors.New("acquire repo lock for team-context: permission denied"), pullRan: false, want: false,
			why: "pullRan alone must not widen the backoff branch; only the probe sentinel opens it",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, teamFailureTakesBackoff(tc.err, tc.pullRan), tc.why)
		})
	}
}

// TestDoTeamSync_PostPullFailureStillSuspends drives the real decision path, not
// the predicate, because a predicate-only test would pass even if pullRan never
// reached doTeamSync.
//
// Failure prevented: routing on errors.Is(err, ErrConflictProbeFailed) alone.
// A pull RUNS, fails deterministically on a dirty worktree it can never settle,
// and the post-pull index read then fails too. classifyAutostashFailure joins
// the probe sentinel onto the pull's error, the sentinel test matches, and the
// whole cycle takes ordinary backoff — retrying forever a pull whose autostash
// entries pile up without bound, which is the one thing issue #767's suspension
// guard exists to stop.
//
// Not parallel: the fixture edits the process-wide PATH and HOME.
func TestDoTeamSync_PostPullFailureStillSuspends(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// Isolate from the developer's real SageOx state, for the reason the two
	// tests above give: discoverTeams() would otherwise read live credentials.
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "data"))

	projectRoot := t.TempDir()
	teamPath := filepath.Join(t.TempDir(), "team-context")
	require.NoError(t, os.MkdirAll(teamPath, 0o755))

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(teamPath, "init", "--initial-branch=main")
	run(teamPath, "config", "user.name", "Test")
	run(teamPath, "config", "user.email", "post-pull-suspend-test@test.sageox.ai")
	run(teamPath, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "AGENTS.md"), []byte("team\n"), 0o600))
	run(teamPath, "add", "AGENTS.md")
	run(teamPath, "commit", "-m", "seed")
	// origin exists but is never contacted: no upstream tracking branch is
	// configured, so remoteRefCheck bails to "fetch" without an ls-remote, and
	// the shim below answers the fetch itself.
	run(teamPath, "remote", "add", "origin", "https://127.0.0.1:1/team-context.git")

	// A DIRTY checkout: a clean tree fingerprints to "" and can never suspend,
	// so a clean fixture would pass vacuously. This is also the literal #767
	// state — local changes a pull keeps stashing and restashing.
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "local.txt"), []byte("uncommitted\n"), 0o600))

	probeCounter := gitShimPostPullProbeFailure(t)

	const teamID = "team_post_pull_failure"
	cfgTOML := "[[team_contexts]]\n" +
		"team_id = '" + teamID + "'\n" +
		"team_name = 'Post Pull Failure Test'\n" +
		"path = '" + teamPath + "'\n" +
		"last_sync = 1970-01-01T00:00:00Z\n" +
		"last_gc = 1970-01-01T00:00:00Z\n"
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".sageox", "config.local.toml"), []byte(cfgTOML), 0o600))

	cfg := DefaultConfig()
	cfg.ProjectRoot = projectRoot
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)
	ctx := context.Background()

	// The daemon writes into the checkout on early passes (EnsureCheckoutGitignore),
	// so the worktree is not byte-identical until it settles; only then can two
	// fingerprints match. Six passes is comfortably past that, and the loop stops
	// as soon as suspension lands so it never runs against a suspended workspace.
	var suspended bool
	for attempt := 1; attempt <= 6 && !suspended; attempt++ {
		// Each cycle starts from a readable index — the shim's counter is what
		// makes the FIRST read of a cycle succeed and every later one fail, and
		// it has to be per-cycle to model that. Left cumulative, cycle 2's
		// PRE-pull read would fail and the pull would never run.
		require.NoError(t, os.WriteFile(probeCounter, []byte("0"), 0o600))

		results, err := scheduler.doTeamSync(ctx, nil, false)
		require.NoError(t, err, "per-team failures are carried in results, not returned")

		var observed string
		for _, r := range results {
			if r.TeamID == teamID {
				observed = r.Error
			}
		}
		// Guard against a vacuous pass. Both halves must be present or the
		// fixture is not staging the joined error this test is about.
		require.NotEmpty(t, observed, "attempt %d must report a per-team error", attempt)
		require.Contains(t, observed, "pull failed",
			"attempt %d: the pull must actually RUN and fail, got %q", attempt, observed)
		require.Contains(t, observed, "read index for",
			"attempt %d: the post-pull index read must fail too, got %q", attempt, observed)
		require.False(t, isTransientSyncError(errors.New(observed)),
			"attempt %d: a transient error would take backoff for an unrelated reason", attempt)

		suspended = scheduler.workspaceRegistry.IsSyncSuspended(teamID)

		scheduler.workspaceRegistry.mu.Lock()
		scheduler.workspaceRegistry.workspaces[teamID].NextSyncAttempt = time.Now().Add(-time.Minute)
		scheduler.workspaceRegistry.mu.Unlock()
	}

	assert.True(t, suspended,
		"a pull that ran and failed deterministically must reach fingerprint suspension even when a failed index probe is joined onto its error")
}

// gitShimPostPullProbeFailure stages the one sequence no real-git fixture can:
// the PRE-pull index read succeeds (so the cycle proceeds to the pull), the pull
// itself fails deterministically on a dirty worktree, and every index read after
// that fails for a reason that is not the index.
//
// `fetch` is answered as a successful no-op rather than pointed at a real bare
// remote: it keeps the test off the network, and because no FETCH_HEAD is
// written, the FETCH_HEAD-age dedup cannot turn later cycles into skips.
// `git status` deliberately passes through — doTeamSync fingerprints with it,
// and a fixture that broke it would never reach the suspension branch at all.
//
// Returns the path of its probe counter so the caller can reset it per cycle.
// Callers must NOT t.Parallel(): the PATH edit is process-wide.
func gitShimPostPullProbeFailure(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	dir := t.TempDir()
	counter := filepath.Join(dir, "probe-count")
	// The pull's failure text is verbatim git's, and is one of the rows
	// TestIsTransientSyncError pins as LOCAL — a settled failure the checkout
	// causes, which is precisely what must stay eligible for suspension.
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --unmerged)
      n=$(cat %[1]q 2>/dev/null || echo 0)
      n=$((n + 1))
      printf '%%s' "$n" > %[1]q
      if [ "$n" -gt 1 ]; then
        echo "fatal: unable to read the index: Resource temporarily unavailable" >&2
        exit 128
      fi
      break
      ;;
    fetch)
      exit 0
      ;;
    pull)
      echo "error: cannot pull with rebase: You have unstaged changes." >&2
      exit 1
      ;;
  esac
done
exec %[2]q "$@"
`, counter, realGit)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return counter
}
