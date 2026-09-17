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
		require.False(t, out.TeamContext.SkillsMaterialized)
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
		require.True(t, out.TeamContext.SkillsMaterialized)
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

// writeTeamSkillFixture stages a published skill in a team checkout.
func writeTeamSkillFixture(t *testing.T, teamPath, name, frontmatterExtra string) {
	t.Helper()
	dir := filepath.Join(teamPath, "agents", "skills", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "---\nname: " + name + "\ndescription: A published skill.\n" + frontmatterExtra + "---\n\nSteps.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644))
}

// TestCollectSkillsStatus_NeverClaimsInstalledFromDiscoveryAlone is the
// correctness core of the command.
//
// Discovery only establishes that the team PUBLISHED a skill for this repo. The
// first version defaulted every discovered skill to "installed" unless it was
// withheld, which meant the command reported success — and "Team skills are
// current" — precisely when reconcile had a pending create and the file was not
// on disk. That is a false green at the one moment the user is asking.
func TestCollectSkillsStatus_NeverClaimsInstalledFromDiscoveryAlone(t *testing.T) {
	repo := t.TempDir()
	teamPath := t.TempDir()
	writeTeamSkillFixture(t, teamPath, "deploy", "")
	stageStatusTeam(t, repo, teamPath)

	out := collectSkillsStatus(repo)

	require.Len(t, out.TeamSkills, 1)
	require.NotEqual(t, skillInstalled, out.TeamSkills[0].State,
		"a skill that is not on disk was reported installed on the strength of discovery alone")
	require.NotContains(t, out.Guidance, "Nothing to do",
		"guidance claimed everything was current while a skill had not reached the repository")
}

// TestCollectSkillsStatus_RepoFilteredSkillIsVisible: a skill published for
// other repos is removed by discovery before this command sees it, so without an
// explicit row "the team published nothing" and "the team published it for other
// repos" look identical. They need opposite actions — author one, or widen a
// repos: list — so the command has to say which it is.
func TestCollectSkillsStatus_RepoFilteredSkillIsVisible(t *testing.T) {
	repo := t.TempDir()
	teamPath := t.TempDir()
	writeTeamSkillFixture(t, teamPath, "frontend-lint", "repos: [\"acme/web\"]\n")
	stageStatusTeam(t, repo, teamPath)

	out := collectSkillsStatus(repo)

	require.Len(t, out.TeamSkills, 1, "a repos:-filtered skill was invisible, so it looked like the team published nothing")
	require.False(t, out.TeamSkills[0].AppliesHere)
	require.Equal(t, skillNotApplicable, out.TeamSkills[0].State)
	require.Contains(t, out.TeamSkills[0].Detail, "acme/web", "the row does not say which repos it does target")
}

// TestCollectSkillsStatus_MalformedCheckoutIsReported: a present-but-broken
// checkout — agents/ as a regular file — must not render as "none found". The
// reader would be told nothing exists when the truth is that the checkout needs
// repair, which is a different action entirely.
func TestCollectSkillsStatus_MalformedCheckoutIsReported(t *testing.T) {
	repo := t.TempDir()
	teamPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(teamPath, "agents"), []byte("not a directory\n"), 0o644))
	stageStatusTeam(t, repo, teamPath)

	out := collectSkillsStatus(repo)

	require.False(t, out.TeamContext.SkillsMaterialized,
		"a regular file named agents/ was accepted as a materialized skill root")
	require.Contains(t, strings.Join(out.Problems, "\n"), "no skills directory was materialized")
	require.NotContains(t, out.Guidance, "Nothing to do")
}

func stageStatusTeam(t *testing.T, repo, teamPath string) {
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
