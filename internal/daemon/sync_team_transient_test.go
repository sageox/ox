package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
	// RFC 2606 reserved TLD: never resolves, so this reproduces the incident's
	// "Could not resolve host" without depending on the network.
	run(teamPath, "remote", "add", "origin", "https://nonexistent-host-for-ox-tests.invalid/team-context.git")

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
		_, err := scheduler.doTeamSync(ctx, nil, false)
		require.NoError(t, err, "per-team failures are carried in results, not returned")

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
