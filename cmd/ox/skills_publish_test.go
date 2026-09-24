package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/teamdocs"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// unprefixedSkillName is an ordinary, unprefixed name — the shape ox's own
// catalog skills take, without asserting this exact name is currently one.
// Sibling suites use it to exercise the unprefixed-but-not-reserved boundary
// without depending on which names the embedded catalog happens to ship.
const unprefixedSkillName = "shared-notes"

// teamPublishSkill is the hand-authored skill the Team Context transaction
// tests below publish. It is deliberately NOT a catalog name: publishing is for
// content a human wrote in their repository, and ox refuses to publish a skill
// it manages itself.
const teamPublishSkill = "deploy-check"

// addDesiredSkillName records name in the repo's committed lockfile the way a
// selection ends up there, without resolving it against the embedded skills
// catalog. `selectedCatalogNames` treats anything in `desired.names` as
// ox-managed regardless of provenance, and that is the one fact this helper
// needs to be true — not that name is a real, currently-shipped catalog skill.
func addDesiredSkillName(t *testing.T, repo, name string) {
	t.Helper()
	path := skillmanager.LockPath(repo)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	desired, ok := doc["desired"].(map[string]any)
	require.True(t, ok, "lockfile has no desired object: %s", raw)
	names, _ := desired["names"].([]any)
	desired["names"] = append(names, name)
	updated, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, updated, 0o644))
}

// stageLocalPublishableSkill writes a hand-authored skill into the repository's
// selected skill root, with a bundled reference file so a publish that copies
// the manifest and drops the rest is visible.
func stageLocalPublishableSkill(t *testing.T, repo, name string) string {
	t.Helper()
	dir := installedSkillDirPath(repo, name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, skills.SkillFileName),
		[]byte("---\nname: "+name+"\ndescription: Check a deploy before it ships.\n---\n\nRun the checklist.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "references", "AUTHORING.md"),
		[]byte("# Authoring\n"), 0o644))
	return dir
}

func installedSkillDirPath(repo, name string) string {
	return filepath.Join(repo, filepath.FromSlash(approvalTargetRoot), name)
}

// stageInstallRepo builds a real git repo with a skill target already pinned in
// the committed lockfile, reconciles it to the default baseline, and chdirs in
// so the commands' own findGitRoot resolves.
//
// The pin is load-bearing: without it reconcile falls through to
// detectedSkillTargets, which shells out to whatever ox-adapter-* binaries are
// installed, so whether a skill reaches disk would become a property of the
// developer's machine rather than of this code.
func stageInstallRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitInitRepo(t, repo)
	gitOutput(t, repo, "remote", "add", "origin", "https://github.com/acme/install-test.git")
	stageSelectedTarget(t, repo)
	_, err := reconcileExactSelectedSkills(repo)
	require.NoError(t, err, "establish the installed baseline")
	t.Chdir(repo)
	return repo
}

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@test.sageox.ai"},
		{"config", "user.name", "t"},
		// Signing off, pinned in the fixture's own config. A developer whose global
		// config signs commits with a passphrase-gated SSH key gets a prompt this
		// process cannot answer, and the test hangs until the harness kills it —
		// the same trap gitutil.RunGit closes for the production path.
		{"config", "commit.gpgsign", "false"},
		{"config", "tag.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir // never the developer's own repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// chdirOutsideGit establishes and proves the precondition for command tests
// that exercise repository-discovery failures. GIT_CEILING_DIRECTORIES keeps
// the test honest even when a developer points TMPDIR somewhere beneath a
// worktree: Git must stop before inspecting the temporary directory's parent.
func chdirOutsideGit(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	probe := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	require.Error(t, probe.Run(), "fixture must not resolve through an ancestor Git worktree")
	t.Chdir(dir)
}

// runSkillsChange drives a command the way a terminal does: cobra parses the
// flags, cobra validates the positional arguments, and the command's own RunE
// runs. Anything less does not exercise the flag surface a human types.
func runSkillsChange(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	resetSkillsFlags(t, cmd)
	t.Cleanup(func() {
		// Restore the singleton. A command left holding this test's buffer sends a
		// later test's output into memory nobody reads.
		cmd.SetOut(nil)
		cmd.SetErr(nil)
		resetSkillsFlags(t, cmd)
	})

	if err := cmd.ParseFlags(args); err != nil {
		return buf.String(), err
	}
	positional := cmd.Flags().Args()
	if err := cmd.ValidateArgs(positional); err != nil {
		return buf.String(), err
	}
	// Run first, THEN read the buffer: Go evaluates return values left to right,
	// so `return buf.String(), cmd.RunE(...)` reads the buffer before the command
	// has written a byte.
	err := cmd.RunE(cmd, positional)
	return buf.String(), err
}

// resetSkillsFlags returns flag values to their declared defaults. Parsed values
// live on the package-level command singletons, so one subtest's --team would
// otherwise leak into the next.
func resetSkillsFlags(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	for _, name := range []string{"json", "team"} {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			require.NoError(t, cmd.Flags().Set(name, "false"))
			flag.Changed = false
		}
	}
}

// stageTeamPublishRepo wires a project repo to a Team Context checkout, both
// real git repositories, and chdirs into the project.
func stageTeamPublishRepo(t *testing.T) (repo, team string) {
	t.Helper()
	repo = stageInstallRepo(t)
	team = t.TempDir()
	gitInitRepo(t, team)
	// A team context with no commit at all has no HEAD, and `git commit` in one
	// behaves differently enough from the real thing to hide a bug.
	require.NoError(t, os.WriteFile(filepath.Join(team, "README.md"), []byte("team\n"), 0o644))
	gitOutput(t, team, "add", "README.md")
	gitOutput(t, team, "commit", "-m", "init")
	sparseTeamCheckout(t, team)
	wireTeamContext(t, repo, team)
	return repo, team
}

// sparseTeamCheckout makes the fixture a REAL sparse checkout, because that is
// what a Team Context is. Without it the fixture would accept a plain `git add`,
// and every --sparse flag in the publish path would be untested decoration: git
// refuses to stage a path outside the cone unless --sparse is passed, and
// `git rm --cached --ignore-unmatch` without it exits 0 having done nothing.
func sparseTeamCheckout(t *testing.T, team string) {
	t.Helper()
	gitOutput(t, team, "sparse-checkout", "init", "--cone")
	gitOutput(t, team, "sparse-checkout", "set", "docs")
}

// wireTeamContext points a project repo at a Team Context checkout.
func wireTeamContext(t *testing.T, repo, team string) {
	t.Helper()
	const teamID = "team_publish_test"
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		ProjectID: "proj_publish", WorkspaceID: "ws_publish",
		RepoID: "repo_publish", TeamID: teamID, TeamName: "Publish Test Team",
	}))
	require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID: teamID, TeamName: "Publish Test Team", Slug: "publish-test-team", Path: team,
		}},
	}))
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func mustReadString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

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
	requireAdviceResolves(t, got.Guidance)
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

	// Publishing `onboard-team` would install it back as `onboard-team-team`, so
	// the name is refused — but it is the AUTHOR'S skill, not ox's, and the
	// message has to say which. "managed by ox" would send them hunting for a
	// conflict that does not exist.
	t.Run("a name already wearing the team suffix", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		stageLocalPublishableSkill(t, repo, "onboard"+skillmanager.TeamSuffix)

		_, err := runSkillsChange(t, skillsPublishCmd, "onboard"+skillmanager.TeamSuffix)
		require.ErrorContains(t, err, "rename it before publishing")
		require.NotContains(t, err.Error(), "managed by ox",
			"the author's own skill was described as ox's")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", "onboard"+skillmanager.TeamSuffix))
	})

	t.Run("selected unprefixed name", func(t *testing.T) {
		repo, team := stageTeamPublishRepo(t)
		// Recorded directly in the committed lockfile rather than through a real
		// reconcile: what matters to this claim is that ox MANAGES a name the
		// lockfile records as selected, not that the name is a real,
		// currently-shipped catalog skill.
		addDesiredSkillName(t, repo, unprefixedSkillName)

		_, err := runSkillsChange(t, skillsPublishCmd, unprefixedSkillName)
		require.ErrorContains(t, err, "managed by ox")
		require.NoDirExists(t, filepath.Join(team, "agents", "skills", unprefixedSkillName))
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

// TestSkillsPublish_SeedsThePublishedCopyExactlyOnce.
//
// `--team` is a seed, not a managed install: ox writes the files once and the
// team owns the copy from then on. Overwriting would silently discard whatever
// the team edited into it, which is the one thing publishing is supposed to
// enable.
func TestSkillsPublish_SeedsThePublishedCopyExactlyOnce(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	out, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.NoError(t, err, "output: %s", out)
	require.Contains(t, out, "published", "the plain-text render did not report the publish")
	require.Contains(t, out, teamPublishSkill)

	published := filepath.Join(team, "agents", "skills", teamPublishSkill)
	require.FileExists(t, filepath.Join(published, "SKILL.md"))
	require.FileExists(t, filepath.Join(published, "references", "AUTHORING.md"),
		"publishing copied the manifest but dropped the skill's bundled references")

	// Recorded in the Team Context's own history, so a teammate's next sync sees
	// it. The fixture has no remote, so this also proves the command does not try
	// to push — a push would fail the command outright.
	require.Empty(t, gitOutput(t, team, "status", "--porcelain"),
		"the published files were left unrecorded in the Team Context")
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), teamPublishSkill)

	// The team's copy is theirs now. A hand edit must survive a second publish.
	manifest := filepath.Join(published, "SKILL.md")
	require.NoError(t, os.WriteFile(manifest, []byte("---\nname: "+teamPublishSkill+"\ndescription: ours now\n---\n"), 0o644))

	_, err = runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.Error(t, err, "a second publish silently overwrote the team's own copy")
	require.Contains(t, err.Error(), "already published")

	data, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	require.Contains(t, string(data), "ours now")
}

// TestSkillsPublish_RefusesAFoldedDescription.
//
// The Team Context frontmatter reader has no block-scalar support for
// `description:`. Publishing a skill whose description is folded stores the
// literal ">-" as its activation surface, so the skill is on disk and invisible
// to every agent — the worst kind of failure, because it looks like a success.
func TestSkillsPublish_RefusesAFoldedDescription(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)

	local := installedSkillDirPath(repo, "folded-desc")
	require.NoError(t, os.MkdirAll(local, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(local, skills.SkillFileName),
		[]byte("---\nname: folded-desc\ndescription: >-\n  Spread across\n  two lines.\n---\n\nBody.\n"), 0o644))

	_, err := runSkillsChange(t, skillsPublishCmd, "folded-desc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "one line",
		"the refusal does not say what is wrong or how to fix it: %v", err)
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", "folded-desc"))
}

// TestSkillsPublish_AFailedPublishCanBeRetried.
//
// The files are written before git records them, and the pre-flight refuses any
// skill whose directory already exists. Those two facts together turn a seed left
// behind by a failed commit into something far worse than a stray file: the
// publish can never be run again, and the only thing ox says about it is that the
// skill is "already published" — to a team that has never seen it. The human has
// to know to delete a directory in a checkout they did not know they had.
func TestSkillsPublish_AFailedPublishCanBeRetried(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	// A directory where git must write a file. Portable in a way chmod and hook
	// scripts are not (no exec bit, no permission semantics, no global config),
	// deterministic, and it lands on the COMMIT — after `git add` has already
	// staged the seed, which is precisely the window the rollback must cover.
	editMsg := filepath.Join(team, ".git", "COMMIT_EDITMSG")
	require.NoError(t, os.RemoveAll(editMsg))
	require.NoError(t, os.Mkdir(editMsg, 0o755))

	_, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.Error(t, err, "the commit cannot have succeeded with COMMIT_EDITMSG unwritable")

	published := filepath.Join(team, "agents", "skills", teamPublishSkill)
	require.NoDirExists(t, published,
		"the failed publish left its seed on disk, so every retry is refused as already published")
	require.Empty(t, gitOutput(t, team, "diff", "--cached", "--name-only"),
		"the failed publish left the skill staged in the team's index")

	// Whatever caused it has passed, and the human does the obvious thing: runs
	// the command again.
	require.NoError(t, os.RemoveAll(editMsg))

	out, retryErr := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.NoError(t, retryErr, "the retry was refused after a failure the human did not cause: %s", out)
	require.FileExists(t, filepath.Join(published, "SKILL.md"))
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), teamPublishSkill)
}

// TestSkillsPublish_RollsBackInATeamContextWithNoHistory.
//
// A Team Context that has been created but never committed into has no HEAD, and
// the rollback cannot restore an index from a commit that does not exist. It is
// the same customer promise as the retry above — a failed publish leaves nothing
// behind — reached through the branch that a repository's first publish takes.
func TestSkillsPublish_RollsBackInATeamContextWithNoHistory(t *testing.T) {
	repo := stageInstallRepo(t)
	team := t.TempDir()
	gitInitRepo(t, team) // no commit, so HEAD is unborn
	sparseTeamCheckout(t, team)
	wireTeamContext(t, repo, team)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	editMsg := filepath.Join(team, ".git", "COMMIT_EDITMSG")
	require.NoError(t, os.RemoveAll(editMsg))
	require.NoError(t, os.Mkdir(editMsg, 0o755))

	_, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.Error(t, err)

	require.NoDirExists(t, filepath.Join(team, "agents", "skills", teamPublishSkill))
	// ls-files rather than a diff: there is no HEAD to diff against, and the index
	// is the thing that decides what the team's first commit will contain.
	require.Empty(t, gitOutput(t, team, "ls-files", "--", "agents"),
		"the failed publish stayed in the index, so the team's first commit carries a skill ox failed to publish")
}

// TestSkillsPublish_CommitsOnlyWhatItPublished.
//
// A Team Context is a checkout a human works in, so its index can already hold a
// change of theirs. A commit that swept that in would put their unfinished work
// into the team's history under a message about a skill — authored by them,
// pushed by the daemon, and discovered later by someone doing archeology on a
// commit that claims to be about something else.
func TestSkillsPublish_CommitsOnlyWhatItPublished(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	theirs := filepath.Join(team, "README.md")
	require.NoError(t, os.WriteFile(theirs, []byte("team\ntheir unfinished edit\n"), 0o644))
	gitOutput(t, team, "add", "README.md")

	out, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.NoError(t, err, "output: %s", out)

	committed := gitOutput(t, team, "show", "--pretty=format:", "--name-only", "HEAD")
	require.Contains(t, committed, "agents/skills/"+teamPublishSkill+"/SKILL.md",
		"the publish did not record what it published")
	require.NotContains(t, committed, "README.md",
		"ox swept the team's own staged work into a commit titled for its publish")
	require.Contains(t, gitOutput(t, team, "diff", "--cached", "--name-only"), "README.md",
		"the team's staged change did not survive the publish")
}

// TestSkillsPublish_WaitsForTheTeamContextLock.
//
// The daemon fetches, pulls and rebases the same checkout under
// gitutil.WithRepoLock. A publish that ignored it could stage against an index
// another process is mid-way through rewriting, or write files into a tree a
// rebase is about to move — the 2026-09-02 interleaving, reached from the CLI.
//
// The lock is asserted over the FILE WRITES as well as the commit: holding it
// only for the git transaction would still let a rebase run against a tree full
// of untracked files this command had already put there.
func TestSkillsPublish_WaitsForTheTeamContextLock(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	held := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- gitutil.WithRepoLock(context.Background(), team, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// publishRepoSkillsToTeam rather than the cobra command: require's
	// FailNow is not callable from a non-test goroutine.
	published := make(chan error, 1)
	go func() {
		_, err := publishRepoSkillsToTeam(repo, []string{teamPublishSkill})
		published <- err
	}()

	select {
	case err := <-published:
		t.Fatalf("the publish ran straight through a lock another ox process was holding: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", teamPublishSkill),
		"the publish wrote into the Team Context while another ox process held it")

	close(release)
	require.NoError(t, <-holder)
	require.NoError(t, <-published, "the publish failed once the lock was free")
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), teamPublishSkill)
}

// TestSkillsPublish_ConcurrentPublishersDoNotClobberCommittedSeed proves
// the existence check is covered by the same lock as the writes. If both
// callers inspect before locking, both see an empty destination; the loser then
// overwrites the winner, gets "nothing to commit", and its rollback deletes the
// winner's committed files from the worktree.
func TestSkillsPublish_ConcurrentPublishersDoNotClobberCommittedSeed(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	held := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- gitutil.WithRepoLock(context.Background(), team, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := publishRepoSkillsToTeam(repo, []string{teamPublishSkill})
			results <- err
		}()
	}

	// Give both callers time to reach the held repository lock. This is not the
	// assertion; the final file and commit count below deterministically expose
	// the stale pre-lock check even if one goroutine starts a little later.
	time.Sleep(250 * time.Millisecond)
	close(release)
	require.NoError(t, <-holder)

	var succeeded, refused int
	for range 2 {
		if err := <-results; err == nil {
			succeeded++
		} else {
			refused++
			require.Contains(t, err.Error(), "already published")
		}
	}
	require.Equal(t, 1, succeeded, "exactly one publisher must create the team skill")
	require.Equal(t, 1, refused, "the publisher that acquires the lock second must refuse")
	require.FileExists(t, filepath.Join(team, "agents", "skills", teamPublishSkill, "SKILL.md"),
		"the losing publisher's rollback removed the winning publisher's committed seed")
	require.Equal(t, "2", strings.TrimSpace(gitOutput(t, team, "rev-list", "--count", "HEAD")),
		"concurrent publishers created more than one publish commit")
}

func TestPublishTeamSkillSeeds_FilesystemAndGitBoundaries(t *testing.T) {
	seed := teamSkillSeed{name: "demo", relDir: "agents/skills/demo", files: []skills.File{{
		Path: skills.SkillFileName, Content: []byte("---\nname: demo\ndescription: demo\n---\n"),
	}}}

	t.Run("configured path cannot be opened", func(t *testing.T) {
		repo := t.TempDir()
		teamFile := filepath.Join(t.TempDir(), "team-file")
		require.NoError(t, os.WriteFile(teamFile, []byte("not a directory"), 0o600))
		wireTeamContext(t, repo, teamFile)
		_, err := publishTeamSkillSeeds(repo, []string{"demo"}, []teamSkillSeed{seed})
		require.ErrorContains(t, err, "open team context")
	})

	t.Run("non-git Team Context cannot claim an absent destination", func(t *testing.T) {
		repo, team := t.TempDir(), t.TempDir()
		wireTeamContext(t, repo, team)
		_, err := publishTeamSkillSeeds(repo, []string{"demo"}, []teamSkillSeed{seed})
		require.Error(t, err)
	})

	rollbackTeamSeeds(t.TempDir(), nil)
}

// TestSkillsPublish_RefusesASparseExcludedPublishedSkill.
//
// A Team Context is a SPARSE checkout, so a skill tracked in git can be absent
// from the worktree. A worktree-only existence check calls that destination free
// and the path-scoped commit then replaces the team's existing skill in history —
// a silent overwrite of content ox promises to seed exactly once.
func TestSkillsPublish_RefusesASparseExcludedPublishedSkill(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	// Publish once, the ordinary way, so the skill is real history.
	out, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.NoError(t, err, "output: %s", out)
	published := filepath.Join(team, "agents", "skills", teamPublishSkill)
	require.FileExists(t, filepath.Join(published, "SKILL.md"))
	original := gitOutput(t, team, "rev-parse", "HEAD")

	// Now exclude it from the cone. Git still tracks every file; the worktree
	// no longer holds them, which is exactly the state a teammate's checkout is
	// in when their sparse set does not cover agents/.
	gitOutput(t, team, "sparse-checkout", "init", "--cone")
	gitOutput(t, team, "sparse-checkout", "set", "docs")
	require.NoDirExists(t, published,
		"the fixture proves nothing unless sparse checkout actually removed the worktree copy")
	require.NotEmpty(t, gitOutput(t, team, "ls-files", "--", "agents/skills/"+teamPublishSkill),
		"git must still track the skill or this is not the sparse case")

	_, err = runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
	require.Error(t, err, "a sparse-excluded skill was overwritten instead of refused")
	require.Contains(t, err.Error(), "already published")
	require.Equal(t, original, gitOutput(t, team, "rev-parse", "HEAD"),
		"the refusal still wrote a commit over the team's existing skill")
}

// TestSkillsPublish_RefusesASymlinkedParent.
//
// A Team Context is a remote-controlled clone. If an intermediate component —
// "agents" or "agents/skills" — is a symlink out of the tree, MkdirAll and
// WriteFile follow it and deposit the skill outside the checkout entirely, where
// the staging failure afterwards cleans up the wrong place.
func TestSkillsPublish_RefusesASymlinkedParent(t *testing.T) {
	for _, tc := range []struct{ name, link string }{
		{name: "agents is a link", link: "agents"},
		{name: "agents/skills is a link", link: filepath.Join("agents", "skills")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, team := stageTeamPublishRepo(t)
			stageLocalPublishableSkill(t, repo, teamPublishSkill)

			outside := t.TempDir()
			linkPath := filepath.Join(team, tc.link)
			require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0o755))
			require.NoError(t, os.Symlink(outside, linkPath))

			_, err := runSkillsChange(t, skillsPublishCmd, teamPublishSkill)
			require.Error(t, err, "publishing followed a symlink out of the Team Context")

			// Refused AT THE BOUNDARY, not downstream. An "error occurred" assertion
			// is not enough here: without the root handle the files are written
			// outside first and the run still fails later at git staging, so the only
			// thing that distinguishes fixed from broken is WHICH error comes back.
			require.Contains(t, err.Error(), "escapes",
				"the run failed for some other reason, so the symlink was still followed: %v", err)

			// And nothing is left behind outside the checkout. This catches the
			// agents-symlink case specifically, where rollback deletes the seeded
			// directory through the link but leaves its parent standing.
			entries, readErr := os.ReadDir(outside)
			require.NoError(t, readErr)
			require.Empty(t, entries, "publishing created files outside the Team Context")
		})
	}
}

// TestSkillsPublish_RefusesWithoutATeamContext.
//
// Publishing into a Team Context this project has never been wired to would
// have nowhere to write. The refusal has to name the command that diagnoses it,
// because "nowhere to publish" is otherwise indistinguishable from a bug.
func TestSkillsPublish_RefusesWithoutATeamContext(t *testing.T) {
	repo := stageInstallRepo(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	_, err := publishTeamSkillSeeds(repo, []string{teamPublishSkill}, []teamSkillSeed{{
		name: teamPublishSkill, relDir: "agents/skills/" + teamPublishSkill,
		files: []skills.File{{Path: skills.SkillFileName, Content: []byte("---\nname: x\ndescription: y\n---\n")}},
	}})
	require.ErrorContains(t, err, "no Team Context is configured")
	requireAdviceResolves(t, err.Error())
}
