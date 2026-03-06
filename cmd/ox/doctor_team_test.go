//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckTeamContextHealth_NoTeamContexts(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// should only return skipped checks (legacy/orphan) when no team contexts configured
	for _, check := range checks {
		assert.True(t, check.skipped, "all checks should be skipped when no team contexts: %s", check.name)
	}
}

func TestCheckTeamContextHealth_WithTeamContext(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context directory with real git init
	teamDir := filepath.Join(t.TempDir(), "team-test")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// configure team context in local config
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("test-team", "Test Team", teamDir)
	localCfg.UpdateTeamContextLastSync("test-team")
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Test Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	assert.True(t, teamCheck.passed)
}

func TestCheckTeamContextHealth_MissingDirectory(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// configure team context pointing to non-existent directory
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("test-team", "Test Team", "/non/existent/path")
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Test Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	assert.False(t, teamCheck.passed)
	assert.Contains(t, teamCheck.message, "missing")
}

func TestCheckTeamContextHealth_OrphanedBackpointers(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context directory with real git init
	teamDir := filepath.Join(t.TempDir(), "team-test")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// configure team context
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("test-team", "Test Team", teamDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	// add orphaned backpointers (pointing to non-existent project)
	backpointers := []config.WorkspaceBackpointer{
		{WorkspaceID: "ws-orphan", ProjectPath: "/deleted/project", LastActive: time.Now()},
	}
	require.NoError(t, config.SaveBackpointers(teamDir, backpointers))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Test Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	assert.True(t, teamCheck.warning)
	assert.Contains(t, teamCheck.message, "orphaned")
}

func TestCheckTeamContextHealth_FixOrphanedBackpointers(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context directory with real git init
	teamDir := filepath.Join(t.TempDir(), "team-test")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// configure team context
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("test-team", "Test Team", teamDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	// add orphaned backpointer
	backpointers := []config.WorkspaceBackpointer{
		{WorkspaceID: "ws-orphan", ProjectPath: "/deleted/project", LastActive: time.Now()},
	}
	require.NoError(t, config.SaveBackpointers(teamDir, backpointers))

	// run with fix=true
	opts := doctorOptions{fix: true}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Test Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	assert.True(t, teamCheck.passed)
	assert.Contains(t, teamCheck.message, "cleaned")

	// verify backpointers were cleaned
	remaining, _ := config.LoadBackpointers(teamDir)
	assert.Empty(t, remaining)
}

func TestCheckLegacyTeamContexts(t *testing.T) {
	skipIntegration(t)

	tempDir := t.TempDir()
	projectDir := filepath.Join(tempDir, "my-project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	// create legacy team context directory with real git init
	legacyDir := filepath.Join(tempDir, "sageox_team_abc123_context")
	require.NoError(t, os.MkdirAll(legacyDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = legacyDir
	require.NoError(t, cmd.Run())

	result := checkLegacyTeamContexts(projectDir)

	assert.True(t, result.warning)
	assert.Contains(t, result.message, "1 found")
	assert.Contains(t, result.detail, "migrate")
}

func TestCheckSingleTeamContext_StaleContext(t *testing.T) {
	skipIntegration(t)

	// create team context directory with real git init
	teamDir := filepath.Join(t.TempDir(), "team-stale")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// create a workspace that still exists but hasn't been active
	existingProject := t.TempDir()
	backpointers := []config.WorkspaceBackpointer{
		{WorkspaceID: "ws-old", ProjectPath: existingProject, LastActive: time.Now().Add(-60 * 24 * time.Hour)},
	}
	require.NoError(t, config.SaveBackpointers(teamDir, backpointers))

	tc := config.TeamContext{
		TeamID:   "stale-team",
		TeamName: "Stale Team",
		Path:     teamDir,
		LastSync: time.Now().Add(-60 * 24 * time.Hour),
	}

	opts := doctorOptions{fix: false}
	result := checkSingleTeamContext(tc, opts)

	assert.True(t, result.warning)
	assert.Contains(t, result.message, "no activity")
}

// Additional edge case tests for team context robustness

func TestCheckTeamContextHealth_DeletedDirectory(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context directory
	teamDir := filepath.Join(t.TempDir(), "team-to-delete")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// configure team context in local config
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("delete-team", "Delete Team", teamDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	// now delete the team directory to simulate manual deletion
	require.NoError(t, os.RemoveAll(teamDir))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Delete Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	assert.False(t, teamCheck.passed, "check should fail for deleted directory")
	assert.Contains(t, teamCheck.message, "directory missing")
}

func TestCheckTeamContextHealth_MovedToXDG(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create team context in a temp location (simulating old location)
	oldDir := filepath.Join(t.TempDir(), "old-location", "team-moved")
	require.NoError(t, os.MkdirAll(oldDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = oldDir
	require.NoError(t, cmd.Run())

	// configure team context pointing to old location
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("moved-team", "Moved Team", oldDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	// "move" the team context by deleting old and creating new location
	// (simulating XDG migration without config update)
	newDir := filepath.Join(t.TempDir(), "new-xdg-location", "team-moved")
	require.NoError(t, os.MkdirAll(newDir, 0755))
	cmd = exec.Command("git", "init")
	cmd.Dir = newDir
	require.NoError(t, cmd.Run())
	require.NoError(t, os.RemoveAll(oldDir))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Moved Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	// should fail because config still points to old location
	assert.False(t, teamCheck.passed, "check should fail for moved directory")
}

func TestCheckTeamContextHealth_CorruptedGit(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context with corrupted git
	teamDir := filepath.Join(t.TempDir(), "team-corrupted")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	// create a fake .git directory (corrupted - not a real git repo)
	require.NoError(t, os.MkdirAll(filepath.Join(teamDir, ".git"), 0755))

	// configure team context in local config
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("corrupted-team", "Corrupted Team", teamDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check - it should still find it (we check for .git dir, not validity)
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Corrupted Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	// behavior depends on implementation - corrupted git might pass existence check
	// but would fail on actual git operations
}

func TestCheckTeamContextHealth_PermissionDenied(t *testing.T) {
	skipIntegration(t)

	// skip if running as root
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a team context directory
	teamDir := filepath.Join(t.TempDir(), "team-readonly")
	require.NoError(t, os.MkdirAll(teamDir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = teamDir
	require.NoError(t, cmd.Run())

	// configure team context
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("readonly-team", "Readonly Team", teamDir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	// make directory read-only
	require.NoError(t, os.Chmod(teamDir, 0444))
	defer os.Chmod(teamDir, 0755) // restore for cleanup

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find the team check
	var teamCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: Readonly Team" {
			teamCheck = &checks[i]
			break
		}
	}

	require.NotNil(t, teamCheck, "should have team check")
	// directory exists and is accessible for reading, so check might pass
	// but operations requiring write would fail
}

func TestCheckTeamContextHealth_MultipleTeamsOneFailing(t *testing.T) {
	skipIntegration(t)

	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create two team context directories
	team1Dir := filepath.Join(t.TempDir(), "team-ok")
	require.NoError(t, os.MkdirAll(team1Dir, 0755))
	cmd := exec.Command("git", "init")
	cmd.Dir = team1Dir
	require.NoError(t, cmd.Run())

	team2Dir := filepath.Join(t.TempDir(), "team-missing")
	// team2 is not created - simulating a deleted team context

	// configure both team contexts
	localCfg := &config.LocalConfig{}
	localCfg.SetTeamContext("ok-team", "OK Team", team1Dir)
	localCfg.SetTeamContext("missing-team", "Missing Team", team2Dir)
	require.NoError(t, config.SaveLocalConfig(gitRoot, localCfg))

	opts := doctorOptions{fix: false}
	checks := checkTeamContextHealth(opts)

	// find both team checks
	var okCheck, missingCheck *checkResult
	for i := range checks {
		if checks[i].name == "Team: OK Team" {
			okCheck = &checks[i]
		}
		if checks[i].name == "Team: Missing Team" {
			missingCheck = &checks[i]
		}
	}

	require.NotNil(t, okCheck, "should have OK team check")
	require.NotNil(t, missingCheck, "should have missing team check")

	assert.True(t, okCheck.passed, "OK team should pass")
	assert.False(t, missingCheck.passed, "missing team should fail")
}
