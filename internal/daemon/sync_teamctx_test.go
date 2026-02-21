package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateCredentials sets up credential isolation for tests by redirecting
// credential storage to a temp directory and forcing file-based storage.
// Shared across daemon test files (referenced by sync_integration_test.go).
func isolateCredentials(t *testing.T) {
	t.Helper()
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})
}

// isolateCredentialsWithDir sets up credential isolation and returns the
// temp directory where credentials are stored, so tests can write custom
// credential files to it.
func isolateCredentialsWithDir(t *testing.T) string {
	t.Helper()
	credDir := t.TempDir()
	prevConfigDir := gitserver.TestSetConfigDirOverride(credDir)
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})
	return credDir
}

// setupProjectWithConfig creates a temp project directory with .sageox/config.local.toml
// and a project config.json pointing to a fake endpoint.
func setupProjectWithConfig(t *testing.T, localConfigContent string) string {
	t.Helper()
	projectDir := t.TempDir()
	sageoxDir := filepath.Join(projectDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.local.toml"),
		[]byte(localConfigContent),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`),
		0644,
	))
	return projectDir
}

// writeCredentialsFile writes a GitCredentials JSON file using the proper
// SaveCredentialsForEndpoint path. The endpoint must match what the project
// config uses, so the workspace registry's rebuildFromConfigLocked finds them.
func writeCredentialsFile(t *testing.T, _ string, creds gitserver.GitCredentials) {
	t.Helper()
	// use the same endpoint as our test project config ("https://fake.test.invalid")
	// SaveCredentialsForEndpoint uses the test config dir override
	require.NoError(t, gitserver.SaveCredentialsForEndpoint("https://fake.test.invalid", creds))
}

// newTestScheduler creates a SyncScheduler configured for testing.
func newTestScheduler(projectDir string) *SyncScheduler {
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = time.Minute
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger)
}

// --- Test 1: New team context appears in credentials ---

func TestTeamContextDiscovery_NewTeamAppears(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// set up project with empty local config (no team contexts configured)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// create a real git repo that acts as the team context
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	scheduler := newTestScheduler(projectDir)

	// first sync: no credentials, no team contexts
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus(), "no team contexts before credentials are written")

	// write credentials with a new team context repo
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_new123": {
				Name:   "new-team",
				Type:   "team-context",
				URL:    "file://" + teamDir + ".bare", // bare repo created by setupGitRepo
				TeamID: "team_new123",
			},
		},
	})

	// invalidate config cache so registry re-discovers from credentials
	scheduler.WorkspaceRegistry().InvalidateConfigCache()

	// reload workspace state - registry should discover the new team context
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.NotEmpty(t, teamContexts, "should discover new team context from credentials")

	found := false
	for _, tc := range teamContexts {
		if tc.TeamID == "team_new123" {
			found = true
			assert.Equal(t, "new-team", tc.TeamName)
			assert.NotEmpty(t, tc.CloneURL, "clone URL should be populated from credentials")
			break
		}
	}
	assert.True(t, found, "team_new123 should be discovered from credentials")
}

// --- Test 2: Team context removed from credentials ---

func TestTeamContextDiscovery_RemovedFromCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// set up project with empty local config
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// create a team context git repo
	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// write credentials with one team context
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_removeme": {
				Name:   "removable-team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/removable-team.git",
				TeamID: "team_removeme",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)

	// first load: team context should be discovered
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.Len(t, teamContexts, 1, "should have one team context initially")
	assert.Equal(t, "team_removeme", teamContexts[0].TeamID)

	// remove the team from credentials
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{}, // empty
	})

	// invalidate cache and reload
	scheduler.WorkspaceRegistry().InvalidateConfigCache()
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// team context should be removed from registry (rebuildFromConfigLocked
	// removes workspaces no longer in config or credentials)
	teamContexts = scheduler.WorkspaceRegistry().GetTeamContexts()
	assert.Empty(t, teamContexts, "removed team context should no longer appear in registry")

	// the local clone directory should NOT be deleted by the registry rebuild;
	// only CleanupRevokedTeamContexts deletes from disk (and that requires
	// the repo detail API to explicitly revoke). Verify the directory check:
	// since we never cloned to disk, there's nothing to verify on disk,
	// but the key invariant is that rebuildFromConfigLocked doesn't touch the filesystem.
}

// --- Test 3: Credentials file corrupted or missing ---

func TestTeamContextDiscovery_CredentialsCorrupted(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write garbage to the credentials file
	dir := filepath.Join(credDir, "sageox")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "git-credentials.json"),
		[]byte("this is not valid json {{{"),
		0600,
	))

	scheduler := newTestScheduler(projectDir)

	// loading config should not panic or crash; credentials load error
	// is logged and team contexts from credentials are simply not discovered
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	assert.Empty(t, scheduler.TeamContextStatus(), "corrupted credentials should not produce team contexts")
}

func TestTeamContextDiscovery_CredentialsMissing(t *testing.T) {
	isolateCredentials(t) // points to empty temp dir, no credentials file
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	scheduler := newTestScheduler(projectDir)

	// no credentials file at all
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())
	assert.Empty(t, scheduler.TeamContextStatus(), "missing credentials should not produce team contexts")

	// pullTeamContexts should also handle this gracefully
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus())
}

// --- Test 4: Empty credentials (repos list is empty) ---

func TestTeamContextDiscovery_EmptyRepos(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write valid credentials but with no repos
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// should handle gracefully with no team contexts
	assert.Empty(t, scheduler.TeamContextStatus())

	// pullTeamContexts should also work without error
	scheduler.pullTeamContexts(context.Background())
	assert.Empty(t, scheduler.TeamContextStatus())
}

// --- Test 5: Multiple team contexts, one fails ---

func TestTeamContextDiscovery_MultipleTeamsOneFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	// create three team context repos; two are real, one will have a bad path
	teamDir1 := t.TempDir()
	setupGitRepo(t, teamDir1)

	teamDir2 := t.TempDir()
	setupGitRepo(t, teamDir2)

	// team3 path exists but is NOT a git repo (simulates failed clone)
	teamDir3 := t.TempDir()

	projectDir := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = "team_good1"
team_name = "Good Team 1"
path = %q

[[team_contexts]]
team_id = "team_bad"
team_name = "Bad Team"
path = %q

[[team_contexts]]
team_id = "team_good2"
team_name = "Good Team 2"
path = %q
`, teamDir1, teamDir3, teamDir2))

	// write credentials so clone URLs don't matter for the two good teams
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos:     map[string]gitserver.RepoEntry{},
	})

	scheduler := newTestScheduler(projectDir)

	// make FETCH_HEAD old for the two good repos so sync isn't skipped
	for _, dir := range []string{teamDir1, teamDir2} {
		fetchHead := filepath.Join(dir, ".git", "FETCH_HEAD")
		oldTime := time.Now().Add(-1 * time.Hour)
		_ = os.Chtimes(fetchHead, oldTime, oldTime)
	}

	// run team context sync
	scheduler.pullTeamContexts(context.Background())

	status := scheduler.TeamContextStatus()
	require.Len(t, status, 3, "should report status for all three team contexts")

	// build lookup for assertions
	statusByID := make(map[string]*TeamContextSyncStatus)
	for i := range status {
		statusByID[status[i].TeamID] = &status[i]
	}

	// good teams should sync successfully
	good1 := statusByID["team_good1"]
	require.NotNil(t, good1)
	assert.True(t, good1.Exists, "good team 1 should exist")
	assert.Empty(t, good1.LastErr, "good team 1 should have no error")
	assert.False(t, good1.LastSync.IsZero(), "good team 1 should have last sync set")

	good2 := statusByID["team_good2"]
	require.NotNil(t, good2)
	assert.True(t, good2.Exists, "good team 2 should exist")
	assert.Empty(t, good2.LastErr, "good team 2 should have no error")
	assert.False(t, good2.LastSync.IsZero(), "good team 2 should have last sync set")

	// bad team should report an error but not prevent the other two from syncing
	bad := statusByID["team_bad"]
	require.NotNil(t, bad)
	assert.False(t, bad.Exists, "bad team should not exist as git repo")
	assert.NotEmpty(t, bad.LastErr, "bad team should have an error")

	// verify metrics reflect partial success
	snap := scheduler.Metrics().Snapshot()
	assert.GreaterOrEqual(t, snap.TeamSyncCount, int64(2), "at least two team syncs should succeed")
}

// --- Test: Credentials with non-team-context repos are ignored ---

func TestTeamContextDiscovery_IgnoresNonTeamContextRepos(t *testing.T) {
	credDir := isolateCredentialsWithDir(t)
	projectDir := setupProjectWithConfig(t, "# empty config\n")

	// write credentials with a repo that is NOT a team-context type
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"some_ledger": {
				Name:   "ledger-repo",
				Type:   "ledger",
				URL:    "https://git.fake.test.invalid/ledger.git",
				TeamID: "some_ledger",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	// ledger-type repos in credentials should NOT be discovered as team contexts
	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	assert.Empty(t, teamContexts, "non-team-context repo types should be ignored")
}

// --- Test: Config-based team contexts enriched with clone URLs from credentials ---

func TestTeamContextDiscovery_ConfigEnrichedByCredentials(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	credDir := isolateCredentialsWithDir(t)

	teamDir := t.TempDir()
	setupGitRepo(t, teamDir)

	// team context defined in config.local.toml (no clone URL there)
	projectDir := setupProjectWithConfig(t, fmt.Sprintf(`
[[team_contexts]]
team_id = "team_enrich"
team_name = "Enriched Team"
path = %q
`, teamDir))

	// credentials provide the clone URL for the same team
	writeCredentialsFile(t, credDir, gitserver.GitCredentials{
		Token:     "test-token",
		ServerURL: "https://git.fake.test.invalid",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"team_enrich": {
				Name:   "Enriched Team",
				Type:   "team-context",
				URL:    "https://git.fake.test.invalid/enriched.git",
				TeamID: "team_enrich",
			},
		},
	})

	scheduler := newTestScheduler(projectDir)
	require.NoError(t, scheduler.WorkspaceRegistry().LoadFromConfig())

	teamContexts := scheduler.WorkspaceRegistry().GetTeamContexts()
	require.Len(t, teamContexts, 1)
	assert.Equal(t, "team_enrich", teamContexts[0].TeamID)
	assert.Equal(t, "https://git.fake.test.invalid/enriched.git", teamContexts[0].CloneURL,
		"clone URL should be populated from credentials even when team is defined in config")
	assert.True(t, teamContexts[0].Exists)
}
