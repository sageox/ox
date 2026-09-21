package teamconverge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestDefaultCoordinator_ConvergesSkillsAndReportsRuleContextDelivery(t *testing.T) {
	project := t.TempDir()
	targets, err := skillmanager.CanonicalizeTargets(project, []adapterprotocol.SkillTarget{{
		Key:        "shared",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})
	require.NoError(t, err)
	_, err = skillmanager.Reconcile(project, version.Version, skillmanager.DefaultDesired(targets), targets)
	require.NoError(t, err)

	team := t.TempDir()
	gitTeam(t, team, "init", "-q")
	gitTeam(t, team, "config", "user.email", "test@sageox.ai")
	gitTeam(t, team, "config", "user.name", "test")
	gitTeam(t, team, "config", "commit.gpgsign", "false")
	writeTeamFile(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: deploy safely\n---\n\nDeploy safely.\n")
	writeTeamFile(t, team, "agents/rules/security.md", "---\nname: security\ndescription: secure defaults\nvisibility: always\n---\n\nNever log secrets.\n")
	writeTeamFile(t, team, "docs/architecture.md", "---\ntitle: Architecture\ndescription: System map\n---\n\n# Architecture\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "team context")

	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","team_id":"team_test","team_name":"Test"}`+"\n"), 0o644))
	local := fmt.Sprintf("[[team_contexts]]\nteam_id = %q\nteam_name = %q\npath = %q\n", "team_test", "Test", team)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.local.toml"), []byte(local), 0o600))

	coordinator, err := NewDefault()
	require.NoError(t, err)
	report, err := coordinator.Converge(context.Background(), Request{
		ProjectRoot: project,
		TeamPath:    team,
		RepoSlug:    "api",
		Mode:        ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.NotEmpty(t, report.Snapshot.Commit)
	require.Len(t, report.Outcomes, 3)

	byKey := map[string]Outcome{}
	for _, outcome := range report.Outcomes {
		byKey[string(outcome.Kind)+"/"+outcome.Name] = outcome
		require.Equal(t, report.Snapshot.Commit, outcome.SourceCommit)
	}
	require.Equal(t, StateApplied, byKey["skill/deploy"].State)
	require.Equal(t, "sageox-team-deploy", byKey["skill/deploy"].InstalledAs)
	require.Equal(t, StateIndexed, byKey["rule/security"].State)
	require.Equal(t, "prime-inline", byKey["rule/security"].Delivery)
	require.Contains(t, byKey["rule/security"].Detail, "next session boundary")
	require.Equal(t, StateIndexed, byKey["context/architecture.md"].State)
	require.FileExists(t, filepath.Join(project, ".agents", "skills", "sageox-team-deploy", "SKILL.md"))
	revision, _, selected := skillmanager.InstalledSource(project)
	require.True(t, selected)
	require.True(t, strings.Contains(revision, report.Snapshot.Commit),
		"projection state %q must name Team Context commit %s", revision, report.Snapshot.Commit)
}

func TestDefaultCoordinator_DefersSkillChangesUntilSessionBoundary(t *testing.T) {
	project := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	targets, err := skillmanager.CanonicalizeTargets(project, []adapterprotocol.SkillTarget{{
		Key:        "shared",
		Root:       ".agents/skills",
		Format:     adapterprotocol.SkillFormatAgentSkillsV1,
		Scope:      adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}})
	require.NoError(t, err)
	_, err = skillmanager.Reconcile(project, version.Version, skillmanager.DefaultDesired(targets), targets)
	require.NoError(t, err)

	team := t.TempDir()
	gitTeam(t, team, "init", "-q")
	gitTeam(t, team, "config", "user.email", "test@sageox.ai")
	gitTeam(t, team, "config", "user.name", "test")
	gitTeam(t, team, "config", "commit.gpgsign", "false")
	writeTeamFile(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: deploy safely\n---\n\nVersion one.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "team context")

	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_test","team_id":"team_test","team_name":"Test"}`+"\n"), 0o644))
	local := fmt.Sprintf("[[team_contexts]]\nteam_id = %q\nteam_name = %q\npath = %q\n", "team_test", "Test", team)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.local.toml"), []byte(local), 0o600))
	installed := filepath.Join(project, ".agents", "skills", "sageox-team-deploy", "SKILL.md")

	coordinator, err := NewDefault()
	require.NoError(t, err)
	report, err := coordinator.Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	before, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Contains(t, string(before), "Version one")

	recording, err := session.StartRecording(project, session.StartRecordingOptions{
		AgentID: "OxLiveBoundary", WorkspacePath: project,
		ParentPID: os.Getpid(), AgentType: "codex",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.ClearRecordingStateForAgent(project, recording.AgentID) })

	writeTeamFile(t, team, "agents/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: deploy safely\n---\n\nVersion two.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "update team context")
	report, err = coordinator.Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "api", Mode: ModeAutomatic,
	})
	require.NoError(t, err)
	require.False(t, report.Converged())
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StatePending, report.Outcomes[0].State)
	require.Contains(t, report.Outcomes[0].Detail, "active AI coworker session")
	during, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Contains(t, string(during), "Version one", "background convergence changed live session behavior")
	require.NotContains(t, string(during), "Version two")

	require.NoError(t, session.ClearRecordingStateForAgent(project, recording.AgentID))
	report, err = coordinator.Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "api", Mode: ModeAutomatic,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.Equal(t, report.Snapshot.Commit, report.Outcomes[0].SourceCommit)
	after, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Contains(t, string(after), "Version two")
}
