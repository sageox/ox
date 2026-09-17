package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/require"
)

// TestCollectSkillsStatus_DistinguishesTheFailureModes is the whole point of the
// command.
//
// Every way a team skill can fail to arrive produces the SAME observation from
// the repository — nothing there. A checkout that has not cloned, a sparse set
// that never materialized the skills root, a slug that matches no `repos:`
// filter, and a team that published nothing are indistinguishable without this.
// So the assertion is not "it prints state"; it is that each case yields a
// DIFFERENT, actionable problem line.
func TestCollectSkillsStatus_DistinguishesTheFailureModes(t *testing.T) {
	stageTeam := func(t *testing.T, repo, teamPath string) {
		t.Helper()
		require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
			ProjectID: "proj_status", WorkspaceID: "ws_status",
			TeamID: "team_status", TeamName: "Status Team",
		}))
		require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
			TeamContexts: []config.TeamContext{{
				TeamID: "team_status", TeamName: "Status Team", Slug: "status-team", Path: teamPath,
			}},
		}))
	}

	t.Run("no team context configured", func(t *testing.T) {
		out := collectSkillsStatus(t.TempDir())
		require.Contains(t, strings.Join(out.Problems, "\n"), "no Team Context is configured")
		require.NotEmpty(t, out.Guidance, "an AI coworker reading the JSON gets no next action")
	})

	t.Run("team context configured but not cloned", func(t *testing.T) {
		repo := t.TempDir()
		stageTeam(t, repo, filepath.Join(t.TempDir(), "never-cloned"))
		out := collectSkillsStatus(repo)
		require.False(t, out.TeamContext.Present)
		require.Contains(t, strings.Join(out.Problems, "\n"), "has not finished cloning")
	})

	t.Run("cloned but no skills root materialized", func(t *testing.T) {
		repo := t.TempDir()
		teamPath := t.TempDir() // exists, but nothing inside
		stageTeam(t, repo, teamPath)
		out := collectSkillsStatus(repo)
		require.True(t, out.TeamContext.Present)
		require.False(t, out.TeamContext.AgentsMaterialized)
		require.Contains(t, strings.Join(out.Problems, "\n"), "no skills directory was materialized")
	})

	// The legacy root is healthy, not broken. Reporting it as a problem sends
	// every pre-migration team to `ox doctor` for a non-condition.
	t.Run("legacy coworkers root is healthy", func(t *testing.T) {
		repo := t.TempDir()
		teamPath := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(teamPath, "coworkers", "skills"), 0o755))
		stageTeam(t, repo, teamPath)
		out := collectSkillsStatus(repo)
		require.True(t, out.TeamContext.AgentsMaterialized)
		require.NotContains(t, strings.Join(out.Problems, "\n"), "no skills directory was materialized")
	})

	// A slug that fell back to the directory name matches no `repos:` filter, so
	// every targeted skill vanishes silently. Naming it is the difference between
	// a five-minute fix and an afternoon.
	t.Run("slug fell back to the directory name", func(t *testing.T) {
		out := collectSkillsStatus(t.TempDir())
		require.False(t, out.Repo.SlugFromRemote)
		require.Contains(t, strings.Join(out.Problems, "\n"), "cannot match here")
	})
}
