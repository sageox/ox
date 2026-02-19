package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initGitRepo creates a minimal git repo with an initial commit in the given directory.
// Unlike setupGitRepo (which sets up a remote), this creates a standalone repo
// suitable for testing checkout cleanliness.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()

	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "git init failed")

	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("# test\n"), 0644))

	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
}

// --------------------------------------------------------------------------
// RegisterTeamContextsFromAPI
// --------------------------------------------------------------------------

func TestRegisterTeamContextsFromAPI(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tests := []struct {
		name string
		// pre-populated workspaces before calling RegisterTeamContextsFromAPI
		existing map[string]*WorkspaceState
		// input to RegisterTeamContextsFromAPI
		apiTeamContexts []api.RepoDetailTeamContext
		// expected workspace IDs after the call
		wantIDs []string
		// expected workspace count delta (newly added)
		wantAdded int
	}{
		{
			name:     "registers new team contexts from API",
			existing: map[string]*WorkspaceState{},
			apiTeamContexts: []api.RepoDetailTeamContext{
				{TeamID: "team_001", Name: "alpha", RepoURL: "https://git.example.com/alpha.git", AccessLevel: "viewer"},
				{TeamID: "team_002", Name: "beta", RepoURL: "https://git.example.com/beta.git", AccessLevel: "member"},
			},
			wantIDs:   []string{"team_001", "team_002"},
			wantAdded: 2,
		},
		{
			name: "skips team contexts already tracked by TeamID",
			existing: map[string]*WorkspaceState{
				"team_001": {ID: "team_001", Type: WorkspaceTypeTeamContext, TeamID: "team_001", TeamName: "alpha"},
			},
			apiTeamContexts: []api.RepoDetailTeamContext{
				{TeamID: "team_001", Name: "alpha", RepoURL: "https://git.example.com/alpha.git"},
				{TeamID: "team_002", Name: "beta", RepoURL: "https://git.example.com/beta.git"},
			},
			wantIDs:   []string{"team_001", "team_002"},
			wantAdded: 1, // only team_002 is new
		},
		{
			name: "skips team contexts already tracked by Name",
			existing: map[string]*WorkspaceState{
				// workspace registered under Name as ID (credential-discovered pattern)
				"beta": {ID: "beta", Type: WorkspaceTypeTeamContext, TeamID: "beta", TeamName: "beta"},
			},
			apiTeamContexts: []api.RepoDetailTeamContext{
				{TeamID: "team_002", Name: "beta", RepoURL: "https://git.example.com/beta.git"},
			},
			wantIDs:   []string{"beta"}, // existing stays, no duplicate added
			wantAdded: 0,
		},
		{
			name:     "skips entries with empty TeamID",
			existing: map[string]*WorkspaceState{},
			apiTeamContexts: []api.RepoDetailTeamContext{
				{TeamID: "", Name: "alpha", RepoURL: "https://git.example.com/alpha.git"},
				{TeamID: "team_002", Name: "beta", RepoURL: "https://git.example.com/beta.git"},
			},
			wantIDs:   []string{"team_002"},
			wantAdded: 1,
		},
		{
			name:     "skips entries with empty RepoURL",
			existing: map[string]*WorkspaceState{},
			apiTeamContexts: []api.RepoDetailTeamContext{
				{TeamID: "team_001", Name: "alpha", RepoURL: ""},
				{TeamID: "team_002", Name: "beta", RepoURL: "https://git.example.com/beta.git"},
			},
			wantIDs:   []string{"team_002"},
			wantAdded: 1,
		},
		{
			name:            "empty input does nothing",
			existing:        map[string]*WorkspaceState{},
			apiTeamContexts: []api.RepoDetailTeamContext{},
			wantIDs:         nil,
			wantAdded:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
			reg.endpoint = "test.sageox.ai"

			// seed existing workspaces
			for id, ws := range tt.existing {
				reg.workspaces[id] = ws
			}
			initialCount := len(reg.workspaces)

			reg.RegisterTeamContextsFromAPI(tt.apiTeamContexts)

			// verify the expected workspace count delta
			assert.Equal(t, initialCount+tt.wantAdded, len(reg.workspaces),
				"workspace count mismatch: started with %d, expected %d added", initialCount, tt.wantAdded)

			// verify all expected IDs are present
			for _, wantID := range tt.wantIDs {
				ws := reg.workspaces[wantID]
				assert.NotNilf(t, ws, "expected workspace %q to exist", wantID)
			}
		})
	}
}

func TestRegisterTeamContextsFromAPI_FieldValues(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.endpoint = "test.sageox.ai"

	input := []api.RepoDetailTeamContext{
		{
			TeamID:      "team_abc",
			Name:        "my-team",
			RepoURL:     "https://git.example.com/my-team.git",
			AccessLevel: "viewer",
			Visibility:  "public",
		},
	}

	reg.RegisterTeamContextsFromAPI(input)

	ws := reg.workspaces["team_abc"]
	require.NotNil(t, ws, "workspace should be registered")

	assert.Equal(t, "team_abc", ws.ID, "ID should be the TeamID")
	assert.Equal(t, WorkspaceTypeTeamContext, ws.Type, "Type should be team_context")
	assert.Equal(t, "team_abc", ws.TeamID)
	assert.Equal(t, "my-team", ws.TeamName)
	assert.Equal(t, "https://git.example.com/my-team.git", ws.CloneURL)
	assert.Equal(t, "test.sageox.ai", ws.Endpoint)

	// path should use paths.TeamContextDir with the team ID and endpoint
	expectedPath := paths.TeamContextDir("team_abc", "test.sageox.ai")
	assert.Equal(t, expectedPath, ws.Path, "Path should match TeamContextDir(teamID, endpoint)")
	assert.NotEmpty(t, ws.Path, "Path must not be empty")

	// Exists should be false because the path doesn't point to a real git repo
	assert.False(t, ws.Exists, "Exists should be false for non-existent path")
}

// --------------------------------------------------------------------------
// CleanupRevokedTeamContexts
// --------------------------------------------------------------------------

func TestCleanupRevokedTeamContexts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("keeps team contexts still in currentTeamIDs by TeamID", func(t *testing.T) {
		reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
		reg.workspaces["team_keep"] = &WorkspaceState{
			ID:       "team_keep",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_keep",
			TeamName: "keepers",
			Exists:   false,
		}

		currentTeamIDs := map[string]bool{"team_keep": true}
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.NotNil(t, reg.workspaces["team_keep"], "workspace matched by TeamID should be kept")
	})

	t.Run("keeps team contexts still in currentTeamIDs by TeamName", func(t *testing.T) {
		reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
		reg.workspaces["team_xyz"] = &WorkspaceState{
			ID:       "team_xyz",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_xyz",
			TeamName: "my-team-name",
			Exists:   false,
		}

		// lookup uses TeamName
		currentTeamIDs := map[string]bool{"my-team-name": true}
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.NotNil(t, reg.workspaces["team_xyz"], "workspace matched by TeamName should be kept")
	})

	t.Run("removes clean checkout from disk and registry", func(t *testing.T) {
		tmpDir := t.TempDir()
		tcDir := filepath.Join(tmpDir, "team-clean")
		require.NoError(t, os.MkdirAll(tcDir, 0755))
		initGitRepo(t, tcDir)

		reg := NewWorkspaceRegistry(tmpDir, "test-repo")
		reg.workspaces["team_revoked"] = &WorkspaceState{
			ID:       "team_revoked",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_revoked",
			TeamName: "revoked-team",
			Path:     tcDir,
			Exists:   true,
		}

		currentTeamIDs := map[string]bool{} // empty = all revoked
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.Nil(t, reg.workspaces["team_revoked"], "revoked clean workspace should be removed from registry")
		_, err := os.Stat(tcDir)
		assert.True(t, os.IsNotExist(err), "clean checkout directory should be deleted from disk")
	})

	t.Run("keeps dirty checkout with error marker", func(t *testing.T) {
		tmpDir := t.TempDir()
		tcDir := filepath.Join(tmpDir, "team-dirty")
		require.NoError(t, os.MkdirAll(tcDir, 0755))
		initGitRepo(t, tcDir)

		// make the checkout dirty by adding an uncommitted file
		dirtyFile := filepath.Join(tcDir, "uncommitted.txt")
		require.NoError(t, os.WriteFile(dirtyFile, []byte("dirty\n"), 0644))

		reg := NewWorkspaceRegistry(tmpDir, "test-repo")
		reg.workspaces["team_dirty"] = &WorkspaceState{
			ID:       "team_dirty",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_dirty",
			TeamName: "dirty-team",
			Path:     tcDir,
			Exists:   true,
		}

		currentTeamIDs := map[string]bool{} // empty = all revoked
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		ws := reg.workspaces["team_dirty"]
		require.NotNil(t, ws, "dirty workspace should remain in registry")
		assert.Contains(t, ws.LastErr, "access revoked", "should have error marker about revocation")
		assert.Contains(t, ws.LastErr, "uncommitted changes", "error should mention uncommitted changes")

		// directory should still exist
		_, err := os.Stat(tcDir)
		assert.NoError(t, err, "dirty checkout directory should remain on disk")
	})

	t.Run("removes from registry when not on disk", func(t *testing.T) {
		reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
		reg.workspaces["team_gone"] = &WorkspaceState{
			ID:       "team_gone",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_gone",
			TeamName: "gone-team",
			Path:     "/nonexistent/path/to/team",
			Exists:   false,
		}

		currentTeamIDs := map[string]bool{}
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.Nil(t, reg.workspaces["team_gone"], "non-existent workspace should be removed from registry")
	})

	t.Run("does not touch ledger workspaces", func(t *testing.T) {
		reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
		reg.workspaces["ledger"] = &WorkspaceState{
			ID:   "ledger",
			Type: WorkspaceTypeLedger,
			Path: "/some/ledger/path",
		}
		reg.workspaces["team_revoked"] = &WorkspaceState{
			ID:       "team_revoked",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_revoked",
			TeamName: "revoked-team",
			Exists:   false,
		}

		currentTeamIDs := map[string]bool{} // empty = all revoked
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.NotNil(t, reg.workspaces["ledger"], "ledger workspace must not be touched")
		assert.Nil(t, reg.workspaces["team_revoked"], "revoked team context should be removed")
	})

	t.Run("mixed keep and remove", func(t *testing.T) {
		tmpDir := t.TempDir()
		cleanDir := filepath.Join(tmpDir, "team-clean-mix")
		require.NoError(t, os.MkdirAll(cleanDir, 0755))
		initGitRepo(t, cleanDir)

		reg := NewWorkspaceRegistry(tmpDir, "test-repo")
		// this one stays (in currentTeamIDs)
		reg.workspaces["team_active"] = &WorkspaceState{
			ID:       "team_active",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_active",
			TeamName: "active-team",
			Path:     "/some/active/path",
			Exists:   true,
		}
		// this one gets removed (clean checkout, not in currentTeamIDs)
		reg.workspaces["team_removed"] = &WorkspaceState{
			ID:       "team_removed",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_removed",
			TeamName: "removed-team",
			Path:     cleanDir,
			Exists:   true,
		}
		// this one is not on disk, gets removed
		reg.workspaces["team_not_on_disk"] = &WorkspaceState{
			ID:       "team_not_on_disk",
			Type:     WorkspaceTypeTeamContext,
			TeamID:   "team_not_on_disk",
			TeamName: "no-disk-team",
			Path:     "/nonexistent",
			Exists:   false,
		}

		currentTeamIDs := map[string]bool{"team_active": true}
		reg.CleanupRevokedTeamContexts(currentTeamIDs)

		assert.NotNil(t, reg.workspaces["team_active"], "active workspace should be kept")
		assert.Nil(t, reg.workspaces["team_removed"], "clean revoked workspace should be removed")
		assert.Nil(t, reg.workspaces["team_not_on_disk"], "non-existent revoked workspace should be removed")
	})
}

// --------------------------------------------------------------------------
// isCheckoutClean
// --------------------------------------------------------------------------

func TestIsCheckoutClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("clean git repo returns true", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		assert.True(t, isCheckoutClean(dir), "repo with no uncommitted changes should be clean")
	})

	t.Run("dirty git repo with untracked file returns false", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// add untracked file
		require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0644))

		assert.False(t, isCheckoutClean(dir), "repo with untracked file should be dirty")
	})

	t.Run("dirty git repo with modified tracked file returns false", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// modify the committed file
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# modified\n"), 0644))

		assert.False(t, isCheckoutClean(dir), "repo with modified tracked file should be dirty")
	})

	t.Run("dirty git repo with staged change returns false", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		// stage a new file
		require.NoError(t, os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged\n"), 0644))
		cmd := exec.Command("git", "add", "staged.txt")
		cmd.Dir = dir
		require.NoError(t, cmd.Run())

		assert.False(t, isCheckoutClean(dir), "repo with staged changes should be dirty")
	})

	t.Run("non-git directory returns false", func(t *testing.T) {
		dir := t.TempDir()
		// just a plain directory, no .git

		assert.False(t, isCheckoutClean(dir), "non-git directory should return false")
	})

	t.Run("nonexistent path returns false", func(t *testing.T) {
		assert.False(t, isCheckoutClean("/nonexistent/path/xyz"), "nonexistent path should return false")
	})
}

// TestUpdateConfigLastSync_PreservesLedgerPath verifies that UpdateConfigLastSync
// does not clobber the ledger path when writing last_sync to disk.
// This was the root cause of the "daemon synced but ox status shows not cloned" bug:
// UpdateConfigLastSync used an in-memory cache with Path="" and overwrote the path
// that persistLedgerPath had just written to disk.
func TestUpdateConfigLastSync_PreservesLedgerPath(t *testing.T) {
	projectDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectDir, ".sageox"), 0755))

	// seed config.local.toml with an empty-path ledger (the broken state)
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ""},
	}
	require.NoError(t, config.SaveLocalConfig(projectDir, localCfg))

	// build registry and load the empty-path config into cache
	reg := NewWorkspaceRegistry(projectDir, "test-repo")
	reg.configCacheDuration = 1 * time.Hour // keep cache warm
	require.NoError(t, reg.LoadFromConfig())

	// simulate daemon discovering ledger via API and registering it
	ledgerPath := filepath.Join(t.TempDir(), "ledger")
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerPath, ".git"), 0755))
	reg.workspaces["ledger"] = &WorkspaceState{
		ID:   "ledger",
		Type: WorkspaceTypeLedger,
		Path: ledgerPath,
	}

	// persist the discovered path (writes through the cache)
	require.NoError(t, reg.PersistLedgerPath(ledgerPath))

	// UpdateConfigLastSync writes last_sync through the same cache
	require.NoError(t, reg.UpdateConfigLastSync("ledger"))

	// reload from disk and verify the path survived
	reloaded, err := config.LoadLocalConfig(projectDir)
	require.NoError(t, err)
	require.NotNil(t, reloaded.Ledger)
	assert.Equal(t, ledgerPath, reloaded.Ledger.Path,
		"UpdateConfigLastSync must not clobber ledger path set by PersistLedgerPath")
	assert.True(t, reloaded.Ledger.HasLastSync(),
		"last_sync should be set after UpdateConfigLastSync")
}
