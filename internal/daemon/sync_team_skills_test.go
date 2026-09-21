package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamconverge"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamrules"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// TestTeamSkillsTouched is the whole trigger for the daemon's team-skill
// refresh, so it is worth pinning against the shapes `git diff --name-only`
// actually emits.
//
// The legacy coworkers/skills root is in here deliberately: discovery walks both
// roots, and a predicate that knew only the canonical one would leave a skill
// authored in the legacy location discovered-but-never-refreshed — present in the
// team repo, absent from every repository, and silent in both directions.
func TestTeamSkillsTouched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changed []string
		want    bool
	}{
		{name: "nothing changed", changed: nil},
		{
			name:    "canonical root",
			changed: []string{"agents/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "legacy root",
			changed: []string{"coworkers/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "a non-manifest file inside a skill still counts",
			changed: []string{"agents/skills/deploy/references/runbook.md"},
			want:    true,
		},
		{
			name:    "mixed diff finds the skill among unrelated files",
			changed: []string{"MEMORY.md", "docs/principles.md", "agents/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "team rules are not team skills",
			changed: []string{"agents/rules/postgres.md"},
		},
		{
			// A prefix match without the separator would fire on this, and on any
			// future sibling directory whose name merely starts the same way.
			name:    "a sibling directory sharing the prefix does not count",
			changed: []string{"agents/skills-archive/deploy/SKILL.md"},
		},
		{
			// The roots are relative to the checkout; a path that merely contains
			// one deeper down belongs to some other tree.
			name:    "a nested lookalike path does not count",
			changed: []string{"documents/agents/skills/deploy/SKILL.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := teamSkillsTouched(tt.changed); got != tt.want {
				t.Fatalf("teamSkillsTouched(%v) = %v, want %v", tt.changed, got, tt.want)
			}
		})
	}
}

func TestTeamArtifactsTouched(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{path: "agents/skills/deploy/SKILL.md", want: true},
		{path: "agents/rules/security.md", want: true},
		{path: "coworkers/rules/legacy.md", want: true},
		{path: "docs/architecture.md", want: true},
		{path: "agents/tools/github.json", want: true},
		{path: "agents/profiles/reviewer.md", want: true},
		{path: "agents/commands/release.md", want: true},
		{path: "docs-archive/architecture.md", want: false},
		{path: "MEMORY.md", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := teamArtifactsTouched([]string{tt.path}); got != tt.want {
				t.Fatalf("teamArtifactsTouched(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestTeamConvergence_RetriesPendingLockContentionWithoutNewCommit(t *testing.T) {
	project := t.TempDir()
	runTeamGit(t, project, "init", "-q")
	runTeamGit(t, project, "remote", "add", "origin", "https://github.com/acme/api.git")
	targets, err := skillmanager.CanonicalizeTargets(project, []adapterprotocol.SkillTarget{{
		Key: "shared", Root: ".agents/skills", Format: adapterprotocol.SkillFormatAgentSkillsV1,
		Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})
	require.NoError(t, err)
	_, err = skillmanager.Reconcile(project, version.Version, skillmanager.DefaultDesired(targets), targets)
	require.NoError(t, err)

	team := t.TempDir()
	runTeamGit(t, team, "init", "-q")
	runTeamGit(t, team, "config", "user.email", "test@sageox.ai")
	runTeamGit(t, team, "config", "user.name", "test")
	runTeamGit(t, team, "config", "commit.gpgsign", "false")
	manifest := filepath.Join(team, "agents", "skills", "deploy", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(manifest), 0o755))
	require.NoError(t, os.WriteFile(manifest,
		[]byte("---\nname: deploy\ndescription: deploy safely\n---\n\nDeploy safely.\n"), 0o644))
	runTeamGit(t, team, "add", "-A")
	runTeamGit(t, team, "commit", "-q", "-m", "team skill")

	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{
		ConfigVersion: config.CurrentConfigVersion, RepoID: "repo_test", TeamID: "team_test", TeamName: "Test",
	}))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{TeamContexts: []config.TeamContext{{
		TeamID: "team_test", TeamName: "Test", Path: team,
	}}}))

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(context.Background(), skillmanager.LockPath(project), func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	scheduler := newTestScheduler(project)
	scheduler.reconcileTeamSkills([]string{"agents/skills/deploy/SKILL.md"})
	pending, err := teamconverge.LoadPending(project)
	require.NoError(t, err)
	require.NotNil(t, pending, "lock contention was forgotten after the changed commit was consumed")
	require.Equal(t, teamconverge.PendingRetry, pending.Status)
	require.Equal(t, 1, pending.Attempts)
	require.NoFileExists(t, filepath.Join(project, ".agents", "skills", "sageox-team-deploy", "SKILL.md"))

	close(release)
	require.NoError(t, <-done)
	// No changed paths and no new Team Context commit: only the durable marker can
	// cause this cycle to retry. Recreate the scheduler to prove a daemon restart
	// does not lose the work.
	scheduler = newTestScheduler(project)
	scheduler.reconcileTeamSkills(nil)
	pending, err = teamconverge.LoadPending(project)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(project, ".agents", "skills", "sageox-team-deploy", "SKILL.md"))
	require.Nil(t, pending, "verified convergence did not clear the pending marker")
}

func TestTeamConvergence_DoesNotRetrySettledOrExhaustedWorkWithoutAChange(t *testing.T) {
	tests := []struct {
		name     string
		status   teamconverge.PendingStatus
		attempts int
	}{
		{name: "settled failure", status: teamconverge.PendingFailed, attempts: 1},
		{name: "automatic budget exhausted", status: teamconverge.PendingRetry, attempts: teamconverge.MaxAutomaticConvergenceAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := t.TempDir()
			runTeamGit(t, project, "init", "-q")
			team := t.TempDir()
			runTeamGit(t, team, "init", "-q")
			runTeamGit(t, team, "config", "user.email", "test@sageox.ai")
			runTeamGit(t, team, "config", "user.name", "test")
			runTeamGit(t, team, "config", "commit.gpgsign", "false")
			require.NoError(t, os.WriteFile(filepath.Join(team, "README.md"), []byte("team\n"), 0o644))
			runTeamGit(t, team, "add", "README.md")
			runTeamGit(t, team, "commit", "-q", "-m", "team")

			require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{
				ConfigVersion: config.CurrentConfigVersion, RepoID: "repo_test", TeamID: "team_test", TeamName: "Test",
			}))
			require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{TeamContexts: []config.TeamContext{{
				TeamID: "team_test", TeamName: "Test", Path: team,
			}}}))

			report := teamconverge.Report{Snapshot: teamconverge.Snapshot{Path: team, Commit: "same"}}
			for range tt.attempts {
				_, err := teamconverge.SavePending(project, tt.status, report, "fixture failure")
				require.NoError(t, err)
			}

			newTestScheduler(project).reconcileTeamSkills(nil)
			pending, err := teamconverge.LoadPending(project)
			require.NoError(t, err)
			require.NotNil(t, pending)
			require.Equal(t, tt.status, pending.Status)
			require.Equal(t, tt.attempts, pending.Attempts,
				"an unchanged daemon pass spent a retry outside the automatic budget")
		})
	}
}

// TestReconcileTeamSkills_RuleRepoFilterUsesCanonicalOriginNotDirectoryName
// covers the class of bug where convergence and prime disagree about "this
// repository": teamdocs.RuleAppliesToRepo/SkillAppliesToRepo (internal/teamdocs)
// fail closed on an empty repo slug, so only a canonical origin-derived
// identity may gate a repos: filter. repotools.RepoSlug's directory-name
// fallback has no relationship to the team's repos: naming, so if it reached
// teamconverge.Request.RepoSlug here, a clone with no origin remote could
// natively project a Team Rule that prime (cmd/ox/agent_prime.go) would
// exclude — two halves of the same "exactly once" contract disagreeing about
// who "this repository" is.
func TestReconcileTeamSkills_RuleRepoFilterUsesCanonicalOriginNotDirectoryName(t *testing.T) {
	project := t.TempDir()
	runTeamGit(t, project, "init", "-q")
	// Deliberately no origin remote: repotools.RepoSlug's directory-name
	// fallback is the failure mode under test. A native rule root makes a
	// wrongly-applicable rule observable as a file on disk.
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", ".gitignore"),
		[]byte("rules/sageox-team-*\n"), 0o644))
	dirName := filepath.Base(project)

	team := t.TempDir()
	runTeamGit(t, team, "init", "-q")
	runTeamGit(t, team, "config", "user.email", "test@sageox.ai")
	runTeamGit(t, team, "config", "user.name", "test")
	runTeamGit(t, team, "config", "commit.gpgsign", "false")
	rule := filepath.Join(team, "agents", "rules", "scoped.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(rule), 0o755))
	require.NoError(t, os.WriteFile(rule,
		[]byte("---\nname: scoped\ndescription: repo-scoped rule\nrepos: [\""+dirName+"\"]\nglobs: [\"**/*.go\"]\nvisibility: always\n---\n\nBody.\n"),
		0o644))
	runTeamGit(t, team, "add", "-A")
	runTeamGit(t, team, "commit", "-q", "-m", "add scoped rule")

	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{
		ConfigVersion: config.CurrentConfigVersion, RepoID: "repo_test", TeamID: "team_test", TeamName: "Test",
	}))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{TeamContexts: []config.TeamContext{{
		TeamID: "team_test", TeamName: "Test", Path: team,
	}}}))

	newTestScheduler(project).reconcileTeamSkills([]string{"agents/rules/scoped.md"})

	rules, err := teamdocs.PublishedRules(team)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	native, ok := teamrules.NativePath(project, "claude", rules[0])
	require.True(t, ok)
	require.NoFileExists(t, native,
		"a repos:-scoped rule matched the working directory's basename instead of the canonical origin identity")
}

func runTeamGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
