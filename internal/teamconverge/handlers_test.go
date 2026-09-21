package teamconverge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/teamrules"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestConverge_ConvergesSkillsAndReportsRuleContextDelivery(t *testing.T) {
	project := t.TempDir()
	gitTeam(t, project, "init", "-q")
	gitTeam(t, project, "remote", "add", "origin", "https://github.com/acme/api.git")
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
		[]byte(`{"config_version":"2","repo_id":"repo_test","team_id":"team_test","team_name":"Test"}`+"\n"), 0o644))
	local := fmt.Sprintf("[[team_contexts]]\nteam_id = %q\nteam_name = %q\npath = %q\n", "team_test", "Test", team)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.local.toml"), []byte(local), 0o600))

	report, err := Converge(context.Background(), Request{
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

func TestConverge_DefersSkillChangesUntilSessionBoundary(t *testing.T) {
	project := t.TempDir()
	gitTeam(t, project, "init", "-q")
	gitTeam(t, project, "remote", "add", "origin", "https://github.com/acme/api.git")
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

	report, err := Converge(context.Background(), Request{
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
	report, err = Converge(context.Background(), Request{
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
	report, err = Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "api", Mode: ModeAutomatic,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.Equal(t, report.Snapshot.Commit, report.Outcomes[0].SourceCommit)
	after, err := os.ReadFile(installed)
	require.NoError(t, err)
	require.Contains(t, string(after), "Version two")
}

func TestConverge_ProjectsTeamRuleExactlyOnceAndConvergesFilteringAndRemoval(t *testing.T) {
	project := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	gitTeam(t, project, "init", "-q")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", ".gitignore"),
		[]byte("rules/sageox-team-*\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".factory", "rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".factory", ".gitignore"),
		[]byte("rules/sageox-team-*\n"), 0o644))

	team := t.TempDir()
	gitTeam(t, team, "init", "-q")
	gitTeam(t, team, "config", "user.email", "test@sageox.ai")
	gitTeam(t, team, "config", "user.name", "test")
	gitTeam(t, team, "config", "commit.gpgsign", "false")
	writeTeamFile(t, team, "agents/rules/go-style.md", "---\nname: go-style\ndescription: Go conventions\nglobs: [\"**/*.go\"]\nvisibility: always\n---\n\nUse gofmt.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "add rule")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_test","team_id":"team_test","team_name":"Test"}`+"\n"), 0o644))
	local := fmt.Sprintf("[[team_contexts]]\nteam_id = %q\nteam_name = %q\npath = %q\n", "team_test", "Test", team)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.local.toml"), []byte(local), 0o600))

	report, err := Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StateApplied, report.Outcomes[0].State)
	require.Contains(t, report.Outcomes[0].Delivery, "claude")
	require.Contains(t, report.Outcomes[0].Detail, "droid",
		"status must explain why Droid uses the indexed fallback")

	rules, err := teamdocs.DiscoverRules(team, "acme/api")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	native, ok := teamrules.NativePath(project, "claude", rules[0])
	require.True(t, ok)
	require.FileExists(t, native)
	require.Empty(t, teamrules.ForPrime(project, "claude", rules),
		"Claude received the same rule through both native projection and prime")
	require.Len(t, teamrules.ForPrime(project, "codex", rules), 1,
		"Codex has no native rule target, so prime must remain its delivery path")
	require.Len(t, teamrules.ForPrime(project, "droid", rules), 1,
		"Droid cannot preserve globs natively, so prime must retain an indexed fallback")
	droidNative, ok := teamrules.NativePath(project, "droid", rules[0])
	require.True(t, ok)
	require.NoFileExists(t, droidNative, "a scoped rule was flattened into Droid's unscoped native format")

	writeTeamFile(t, team, "agents/rules/go-style.md", "---\nname: go-style\ndescription: Go conventions\nrepos: [\"acme/other\"]\nglobs: [\"**/*.go\"]\nvisibility: always\n---\n\nUse gofmt.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "filter rule")
	recording, err := session.StartRecording(project, session.StartRecordingOptions{
		AgentID: "OxLiveRuleBoundary", WorkspacePath: project,
		ParentPID: os.Getpid(), AgentType: "claude",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.ClearRecordingStateForAgent(project, recording.AgentID) })
	_, err = Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeAutomatic,
	})
	require.ErrorContains(t, err, "current rule snapshot")
	require.FileExists(t, native, "background convergence retired a rule during an active session")
	require.NoError(t, session.ClearRecordingStateForAgent(project, recording.AgentID))

	report, err = Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.NoFileExists(t, native, "a filtered native Team Rule survived convergence")
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, StateFiltered, report.Outcomes[0].State)

	writeTeamFile(t, team, "agents/rules/go-style.md", "---\nname: go-style\ndescription: Go conventions\nglobs: [\"**/*.go\"]\nvisibility: always\n---\n\nUse gofmt.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "restore rule")
	_, err = Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.FileExists(t, native)

	require.NoError(t, os.Remove(filepath.Join(team, "agents", "rules", "go-style.md")))
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "retire rule")
	report, err = Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.NoFileExists(t, native, "a retired native Team Rule survived an empty discovery result")
}

func TestConvergeRules_RetainsProjectionWhenSparseRulesAreBlind(t *testing.T) {
	project := t.TempDir()
	rulesRoot := filepath.Join(project, ".claude", "rules")
	require.NoError(t, os.MkdirAll(rulesRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".gitignore"),
		[]byte(".claude/rules/sageox-team-*\n"), 0o644))
	gitTeam(t, project, "init", "-q")

	team := t.TempDir()
	writeTeamFile(t, team, "agents/rules/security.md", "---\nname: security\ndescription: Security policy\nvisibility: always\n---\n\nNever log secrets.\n")
	rules, err := teamdocs.PublishedRules(team)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	artifact := Artifact{
		Kind: KindRule, Name: rules[0].Name, Applicable: true,
		Visibility: rules[0].Visibility, Description: rules[0].Description,
		rule: &rules[0],
	}
	_, err = convergeRules(context.Background(), Request{
		ProjectRoot: project, Mode: ModeExplicit,
	}, Snapshot{Path: team}, []Artifact{artifact})
	require.NoError(t, err)
	native, ok := teamrules.NativePath(project, "claude", rules[0])
	require.True(t, ok)
	require.FileExists(t, native)

	require.NoError(t, os.RemoveAll(filepath.Join(team, "agents")))
	_, err = convergeRules(context.Background(), Request{
		ProjectRoot: project, Mode: ModeExplicit,
	}, Snapshot{Path: team}, nil)
	require.ErrorContains(t, err, "not materialized")
	require.FileExists(t, native, "a blind sparse checkout was mistaken for authoritative retirement")
}

func TestIndexForPrime_DistinguishesInlineRulesFromIndexedArtifacts(t *testing.T) {
	outcomes, err := indexForPrime(context.Background(), Request{}, Snapshot{Commit: "abc"}, []Artifact{
		{Kind: KindRule, Name: "always", Visibility: teamdocs.VisibilityAlways},
		{Kind: KindRule, Name: "indexed", Visibility: teamdocs.VisibilityIndexed},
	})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	require.Equal(t, "prime-inline", outcomes[0].Delivery)
	require.Contains(t, outcomes[0].Detail, "next session boundary")
	require.Equal(t, "prime-index", outcomes[1].Delivery)
}

func TestConvergeSkills_ConfigurationAndModeBoundaries(t *testing.T) {
	artifact := Artifact{Kind: KindSkill, Name: "deploy", Applicable: true}

	t.Run("repository without team context", func(t *testing.T) {
		_, err := convergeSkills(context.Background(), Request{
			ProjectRoot: t.TempDir(), Mode: ModeExplicit,
		}, Snapshot{Path: t.TempDir()}, []Artifact{artifact})
		require.ErrorContains(t, err, "no Team Context")
	})

	t.Run("snapshot must match configured team context", func(t *testing.T) {
		project, configured := t.TempDir(), t.TempDir()
		wireHandlerTeamContext(t, project, configured)
		_, err := convergeSkills(context.Background(), Request{
			ProjectRoot: project, Mode: ModeExplicit,
		}, Snapshot{Path: t.TempDir()}, []Artifact{artifact})
		require.ErrorContains(t, err, "does not match snapshot")
	})

	t.Run("repository without native target is reported unsupported", func(t *testing.T) {
		project, team := t.TempDir(), t.TempDir()
		wireHandlerTeamContext(t, project, team)
		outcomes, err := convergeSkills(context.Background(), Request{
			ProjectRoot: project, Mode: ModeExplicit,
		}, Snapshot{Path: team}, []Artifact{artifact})
		require.NoError(t, err)
		require.Len(t, outcomes, 1)
		require.Equal(t, StateUnsupported, outcomes[0].State)
		require.Contains(t, outcomes[0].Detail, "ox init")
	})
}

// TestConvergeSkills_UsesDiscoveredArtifactsInsteadOfReDeriving is the
// red-first proof for ox-jr82.
//
// convergeSkills used to hand skillmanager an IDENTITY transform, which let
// skillmanager re-walk and re-filter the team checkout on its own — a SECOND,
// independent derivation of "which team skills apply here" beside the one
// FilesystemDiscovery already computed for this artifact set. The two can
// disagree: this fixture publishes a skill whose `repos:` filter excludes the
// real repository slug, so a FRESH internal walk excludes it too, while the
// artifact handed to convergeSkills claims (as discovery already decided,
// however it got there) that it applies here.
//
// Before the fix, skillmanager's own re-derivation found nothing for this
// name, so the decisions map produced by the fresh walk had no entry for it —
// triggering the (now-deleted) "skill reconcile did not report this artifact"
// fabrication. After the fix, convergeSkills hands skillmanager the SAME
// resolved set it was given, so the skill installs.
func TestConvergeSkills_UsesDiscoveredArtifactsInsteadOfReDeriving(t *testing.T) {
	project, team := t.TempDir(), t.TempDir()
	wireHandlerTeamContext(t, project, team)
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

	gitTeam(t, team, "init", "-q")
	gitTeam(t, team, "config", "user.email", "test@sageox.ai")
	gitTeam(t, team, "config", "user.name", "test")
	gitTeam(t, team, "config", "commit.gpgsign", "false")
	writeTeamFile(t, team, "agents/skills/targeted/SKILL.md",
		"---\nname: targeted\ndescription: only for another repo\nrepos: [\"acme/other\"]\n---\n\nOnly for another repo.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "team context")

	published, err := teamdocs.PublishedSkills(team)
	require.NoError(t, err)
	require.Len(t, published, 1)
	require.False(t, teamdocs.SkillAppliesToRepo(published[0], "acme/api"),
		"fixture invariant: a fresh internal walk with the real repo slug must exclude this skill")

	// Discovery already decided this artifact applies here. convergeSkills must
	// install exactly what it was handed, not silently re-derive applicability
	// and disagree with its own report.
	artifact := Artifact{
		Kind: KindSkill, Name: published[0].Name, Applicable: true,
		skill: &published[0],
	}

	outcomes, err := convergeSkills(context.Background(), Request{
		ProjectRoot: project, RepoSlug: "acme/api", Mode: ModeExplicit,
	}, Snapshot{Path: team}, []Artifact{artifact})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.NotEqual(t, StateError, outcomes[0].State,
		"convergeSkills re-derived team skills internally and disagreed with what discovery already resolved: %+v", outcomes[0])
	require.Equal(t, StateApplied, outcomes[0].State)
	require.Equal(t, "sageox-team-targeted", outcomes[0].InstalledAs)
	require.FileExists(t, filepath.Join(project, ".agents", "skills", "sageox-team-targeted", "SKILL.md"))
}

// TestConverge_FiltersTeamSkillWhoseReposExcludesThisRepository is the
// end-to-end proof that sharing the derivation (ox-jr82) did not delete the
// filtered diagnostic settled decision #2 requires: a team skill whose
// `repos:` excludes this repository must still surface as StateFiltered, not
// vanish as if the team published nothing.
func TestConverge_FiltersTeamSkillWhoseReposExcludesThisRepository(t *testing.T) {
	project := t.TempDir()
	gitTeam(t, project, "init", "-q")
	gitTeam(t, project, "remote", "add", "origin", "https://github.com/acme/api.git")
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
	writeTeamFile(t, team, "agents/skills/other-repo-only/SKILL.md",
		"---\nname: other-repo-only\ndescription: not for this repo\nrepos: [\"acme/other\"]\n---\n\nNot for this repo.\n")
	gitTeam(t, team, "add", "-A")
	gitTeam(t, team, "commit", "-q", "-m", "team context")

	require.NoError(t, os.MkdirAll(filepath.Join(project, ".sageox"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_test","team_id":"team_test","team_name":"Test"}`+"\n"), 0o644))
	local := fmt.Sprintf("[[team_contexts]]\nteam_id = %q\nteam_name = %q\npath = %q\n", "team_test", "Test", team)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".sageox", "config.local.toml"), []byte(local), 0o600))

	report, err := Converge(context.Background(), Request{
		ProjectRoot: project, TeamPath: team, RepoSlug: "acme/api", Mode: ModeExplicit,
	})
	require.NoError(t, err)
	require.True(t, report.Converged(), "%+v", report.Outcomes)
	require.Len(t, report.Outcomes, 2)

	byName := map[string]Outcome{}
	for _, o := range report.Outcomes {
		byName[o.Name] = o
	}
	require.Equal(t, StateApplied, byName["deploy"].State)
	require.Equal(t, "sageox-team-deploy", byName["deploy"].InstalledAs)
	require.Equal(t, StateFiltered, byName["other-repo-only"].State,
		"a repos:-filtered team skill vanished instead of surfacing as filtered")
	require.Contains(t, byName["other-repo-only"].Detail, "repos filter")
}

func TestConvergeRules_EmptyAndPrimeFallbackPaths(t *testing.T) {
	outcomes, err := convergeRules(context.Background(), Request{
		ProjectRoot: t.TempDir(), Mode: ModeExplicit,
	}, Snapshot{Path: t.TempDir()}, nil)
	require.NoError(t, err)
	require.Empty(t, outcomes)

	unmaterialized := Artifact{
		Kind: KindRule, Name: "security", Applicable: true,
		Visibility: teamdocs.VisibilityAlways,
	}
	outcomes, err = convergeRules(context.Background(), Request{
		ProjectRoot: t.TempDir(), Mode: ModeExplicit,
	}, Snapshot{Path: t.TempDir()}, []Artifact{unmaterialized})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, StatePending, outcomes[0].State)
	require.Contains(t, outcomes[0].Detail, "no rules directory")

	project, team := t.TempDir(), t.TempDir()
	writeTeamFile(t, team, "agents/rules/security.md", "---\nname: security\ndescription: Secure defaults\nvisibility: always\n---\n\nNever log secrets.\n")
	rules, err := teamdocs.PublishedRules(team)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	materialized := Artifact{
		Kind: KindRule, Name: "security", Applicable: true,
		Visibility: teamdocs.VisibilityAlways, rule: &rules[0],
	}
	outcomes, err = convergeRules(context.Background(), Request{
		ProjectRoot: project, Mode: ModeExplicit,
	}, Snapshot{Path: team}, []Artifact{materialized})
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, StateIndexed, outcomes[0].State)
	require.Equal(t, "prime-inline", outcomes[0].Delivery)
	require.Contains(t, outcomes[0].Detail, "next session boundary")
}

func wireHandlerTeamContext(t *testing.T, project, team string) {
	t.Helper()
	require.NoError(t, config.SaveProjectConfig(project, &config.ProjectConfig{
		ProjectID: "proj_handler", RepoID: "repo_handler", TeamID: "team_handler", TeamName: "Handler Team",
	}))
	require.NoError(t, config.SaveLocalConfig(project, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID: "team_handler", TeamName: "Handler Team", Slug: "handler-team", Path: team,
		}},
	}))
}
