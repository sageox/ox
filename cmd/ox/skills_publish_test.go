package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestSkillsPublish_PromotesLocalSkillIntoTeamContext(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	local := installedSkillDirPath(repo, "release-check")
	require.NoError(t, os.MkdirAll(filepath.Join(local, "references"), 0o755))
	manifest := "---\nname: release-check\ndescription: Verify a release.\nrepos: [\"acme/install-test\"]\n---\n\nRun the checklist.\n"
	require.NoError(t, os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(manifest), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(local, "references", "checklist.md"), []byte("# Checklist\n"), 0o644))

	out, err := runSkillsChange(t, skillsPublishCmd, "--json", "release-check")
	require.NoError(t, err, "output: %s", out)

	var got skillsChangeOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output was not JSON: %q", out)
	require.NotEmpty(t, got.Guidance, "AI coworkers need the next action in JSON")
	require.Equal(t, team, got.TeamContext)
	require.Equal(t, []skillChangeRow{{
		Name: "release-check", State: skillChangePublished, Detail: "agents/skills/release-check",
	}}, got.Skills)
	require.ElementsMatch(t, []string{
		"agents/skills/release-check/SKILL.md",
		"agents/skills/release-check/references/checklist.md",
	}, got.Written)

	published := filepath.Join(team, "agents", "skills", "release-check")
	require.Equal(t, manifest, mustReadString(t, filepath.Join(published, "SKILL.md")))
	require.FileExists(t, filepath.Join(published, "references", "checklist.md"))
	require.Empty(t, gitOutput(t, team, "status", "--porcelain"), "publish left the Team Context dirty")
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), "release-check")

	publishedSkills, err := teamdocs.PublishedSkills(team)
	require.NoError(t, err)
	require.Len(t, publishedSkills, 1)
	require.Equal(t, []string{"acme/install-test"}, publishedSkills[0].Repos,
		"publish silently widened the skill to every team repository")

	// Publishing is a copy, not a move. The repository remains the authoring
	// source until the human chooses to remove it.
	require.Equal(t, manifest, mustReadString(t, filepath.Join(local, "SKILL.md")))
}

func TestSkillsPublish_RefusesAnythingItCannotSafelyOwnOrCopy(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, team := stageTeamPublishRepo(t)

		_, err := runSkillsChange(t, skillsPublishCmd, "not-here")
		require.ErrorContains(t, err, "not installed")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "not-here"))
	})

	t.Run("unsafe name", func(t *testing.T) {
		_, team := stageTeamPublishRepo(t)

		_, err := runSkillsChange(t, skillsPublishCmd, "../../escape")
		require.ErrorContains(t, err, "safe team skill name")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "escape"))
	})

	t.Run("frontmatter name mismatch", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		local := installedSkillDirPath(repo, "safe-name")
		require.NoError(t, os.MkdirAll(local, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(local, "SKILL.md"),
			[]byte("---\nname: ../../escape\ndescription: unsafe\n---\n"), 0o644))

		_, err := runSkillsChange(t, skillsPublishCmd, "safe-name")
		require.ErrorContains(t, err, "frontmatter name")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "safe-name"))
	})

	for _, tc := range []struct {
		name        string
		frontmatter string
		want        string
	}{
		{"block repos would widen scope", "repos:\n  - acme/install-test\n", "inline list"},
		{"hidden skill would not publish", "visibility: hidden\n", "prevents this skill"},
		{"human skill would not reach coworkers", "audience: human\n", "prevents AI coworkers"},
		{"draft skill would not publish", "status: draft\n", "prevents this skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, team := stageTeamPublishRepo(t)
			local := installedSkillDirPath(repo, "scoped")
			require.NoError(t, os.MkdirAll(local, 0o755))
			manifest := "---\nname: scoped\ndescription: scoped\n" + tc.frontmatter + "---\n"
			require.NoError(t, os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(manifest), 0o644))

			_, err := runSkillsChange(t, skillsPublishCmd, "scoped")
			require.ErrorContains(t, err, tc.want)
			require.NoDirExists(t, filepath.Join(team, "agents", "skills", "scoped"))
		})
	}

	t.Run("ox managed", func(t *testing.T) {
		_, team := stageTeamPublishRepo(t)

		_, err := runSkillsChange(t, skillsPublishCmd, "ox-cli-plan")
		require.ErrorContains(t, err, "managed by ox")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "ox-cli-plan"))
	})

	t.Run("selected unprefixed catalog skill", func(t *testing.T) {
		_, team := stageTeamPublishRepo(t)
		_, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill)
		require.NoError(t, err)

		_, err = runSkillsChange(t, skillsPublishCmd, catalogOptInSkill)
		require.ErrorContains(t, err, "managed by ox")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", catalogOptInSkill))
	})

	t.Run("symlink source", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "SKILL.md"),
			[]byte("---\nname: linked\ndescription: outside\n---\n"), 0o644))
		require.NoError(t, os.Symlink(outside, installedSkillDirPath(repo, "linked")))

		_, err := runSkillsChange(t, skillsPublishCmd, "linked")
		require.Error(t, err)
		require.Contains(t, strings.ToLower(err.Error()), "symlink")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "linked"))
	})
}

func TestSkillsPublish_IsAllOrNothingBeforeTeamMutation(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	local := installedSkillDirPath(repo, "valid")
	require.NoError(t, os.MkdirAll(local, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(local, "SKILL.md"),
		[]byte("---\nname: valid\ndescription: valid\n---\n"), 0o644))

	_, err := runSkillsChange(t, skillsPublishCmd, "valid", "missing")
	require.ErrorContains(t, err, "nothing was changed")
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", "valid"),
		"the earlier valid name was published before the later name failed")
}

func TestSkillsPublish_RefusesDivergentCopiesAcrossSelectedRoots(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	_, err := reconcileSelectedSkills(repo, []adapterprotocol.SkillTarget{{
		Key: "agents-project", Root: ".agents/skills", Format: "agent-skills/v1",
		Scope: "project", LinkPolicy: "reject",
	}})
	require.NoError(t, err)
	for root, body := range map[string]string{
		".claude/skills": "one",
		".agents/skills": "two",
	} {
		dir := filepath.Join(repo, filepath.FromSlash(root), "shared")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: shared\ndescription: shared\n---\n\n"+body+"\n"), 0o644))
	}

	_, err = runSkillsChange(t, skillsPublishCmd, "shared")
	require.ErrorContains(t, err, "different copies")
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", "shared"))
}

func TestSkillsPublishCommand_ResolvesThroughRoot(t *testing.T) {
	found, args, err := rootCmd.Find([]string{"skills", "publish"})
	require.NoError(t, err)
	require.Empty(t, args)
	require.Same(t, skillsPublishCmd, found)
	require.True(t, found.Runnable())
}

func TestSkillsPublish_RequiresRepositoryAndSelectedSkillRoot(t *testing.T) {
	chdirOutsideGit(t)
	_, err := runSkillsChange(t, skillsPublishCmd, "local-skill")
	require.ErrorContains(t, err, "not inside a git repository")

	repo := t.TempDir()
	gitInitRepo(t, repo)
	ruleTarget := adapterprotocol.SkillTarget{
		Key: "rules-only", Root: ".rules-only", Format: adapterprotocol.RuleFormatMarkdownV1,
		Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	targets, err := skillmanager.CanonicalizeTargets(repo, []adapterprotocol.SkillTarget{ruleTarget})
	require.NoError(t, err)
	_, err = skillmanager.Reconcile(repo, version.Version,
		skillmanager.AddTargets(skillmanager.DesiredSkills{}, targets...), targets)
	require.NoError(t, err)
	_, err = publishRepoSkillsToTeam(repo, []string{"local-skill"})
	require.ErrorContains(t, err, "no skills directory")
}

func TestPublishSourceHelpers_RejectEscapesNonDirectoriesAndLinkedFiles(t *testing.T) {
	repoPath := t.TempDir()
	repo, err := os.OpenRoot(repoPath)
	require.NoError(t, err)
	defer repo.Close()

	_, err = openPublishSourceDir(repo, "../escape")
	require.ErrorContains(t, err, "escapes repository")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "not-a-directory"), []byte("x"), 0o644))
	_, err = openPublishSourceDir(repo, "not-a-directory/skill")
	require.ErrorContains(t, err, "non-directory")

	skillDir := filepath.Join(repoPath, "skill")
	require.NoError(t, os.Mkdir(skillDir, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(repoPath, "not-a-directory"), filepath.Join(skillDir, "linked.md")))
	dir, err := os.OpenRoot(skillDir)
	require.NoError(t, err)
	defer dir.Close()
	_, err = readPublishSourceFiles(dir)
	require.ErrorContains(t, err, "symlink")
}

func TestPublishManifestValidation_CoversOwnershipAndShapeFailures(t *testing.T) {
	valid := []byte("---\nname: release-check\ndescription: Verify a release.\n---\n")
	managed := agentx.StampedContent(valid, version.Version, "ox")
	for _, tc := range []struct {
		name  string
		files []skills.File
		want  string
	}{
		{name: "missing manifest", files: []skills.File{{Path: "notes.md", Content: []byte("notes")}}, want: "no SKILL.md"},
		{name: "managed manifest", files: []skills.File{{Path: skills.SkillFileName, Content: managed}}, want: "managed by ox"},
		{name: "missing description", files: []skills.File{{Path: skills.SkillFileName, Content: []byte("---\nname: release-check\n---\n")}}, want: "no single-line description"},
		{name: "folded description", files: []skills.File{{Path: skills.SkillFileName, Content: []byte("---\nname: release-check\ndescription: >-\n  folded\n---\n")}}, want: "multi-line YAML block"},
		{name: "incomplete repos", files: []skills.File{{Path: skills.SkillFileName, Content: []byte("---\nname: release-check\ndescription: Verify.\nrepos: [\"acme/widget\"\n---\n")}}, want: "incomplete inline list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, validatePublishManifest("release-check", tc.files), tc.want)
		})
	}
	require.NoError(t, validatePublishManifest("release-check", []skills.File{{Path: skills.SkillFileName, Content: valid}}))
}

func TestPublishManifestFieldAndSkillEqualityEdges(t *testing.T) {
	value, present := publishManifestField([]byte("not frontmatter\nname: ignored\n"), "name")
	require.False(t, present)
	require.Empty(t, value)
	value, present = publishManifestField([]byte("---\nname: 'release-check' # inline comment\n---\n"), "name")
	require.True(t, present)
	require.Equal(t, "release-check", value)
	_, present = publishManifestField([]byte("---\ndescription: done\n---\nname: too-late\n"), "name")
	require.False(t, present)

	one := []skills.File{{Path: "SKILL.md", Content: []byte("same")}}
	require.True(t, sameSkillFiles(one, append([]skills.File(nil), one...)))
	require.False(t, sameSkillFiles(one, nil))
	require.False(t, sameSkillFiles(one, []skills.File{{Path: "other.md", Content: []byte("same")}}))
	require.False(t, sameSkillFiles(one, []skills.File{{Path: "SKILL.md", Content: []byte("different")}}))
}

func mustReadString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
