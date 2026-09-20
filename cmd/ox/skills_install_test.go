package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// catalogOptInSkill is the one skill ox ships in a NON-default bundle, and
// therefore the only one for which "available but not installed" is reachable.
// Every install/uninstall round trip below turns on that property.
const catalogOptInSkill = "post-cutoff"

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

func installedSkillDirPath(repo, name string) string {
	return filepath.Join(repo, filepath.FromSlash(approvalTargetRoot), name)
}

// lockedNames reads the committed selection back off disk. Asserting against the
// FILE rather than the in-memory decision is what proves the choice survives the
// process — a selection that is not recorded is a selection the next reconcile
// silently undoes.
func lockedNames(t *testing.T, repo string) []string {
	t.Helper()
	data, err := os.ReadFile(skillmanager.LockPath(repo))
	require.NoError(t, err)
	var lock struct {
		Desired struct {
			Names []string `json:"names"`
		} `json:"desired"`
	}
	require.NoError(t, json.Unmarshal(data, &lock))
	return lock.Desired.Names
}

func skillsJSON(t *testing.T, cmd *cobra.Command, args ...string) map[string]any {
	t.Helper()
	out, err := runSkillsChange(t, cmd, append([]string{"--json"}, args...)...)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output was not JSON: %q", out)
	return got
}

// TestSkillsInstall_RecordsTheChoiceAndLandsTheFiles is the behavioral core.
//
// Both halves matter and they fail independently. Recording the name without
// reconciling leaves the repository in exactly the state the install was meant
// to end — the skill still absent — which reads as the command not having
// worked. Reconciling without recording installs a file the next reconcile
// deletes.
func TestSkillsInstall_RecordsTheChoiceAndLandsTheFiles(t *testing.T) {
	repo := stageInstallRepo(t)

	require.NoDirExists(t, installedSkillDirPath(repo, catalogOptInSkill),
		"the opt-in skill must start absent or this test proves nothing")

	out, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill)
	require.NoError(t, err, "output: %s", out)

	require.Contains(t, lockedNames(t, repo), catalogOptInSkill,
		"the name is not in the committed selection, so the next reconcile removes the files again")
	require.FileExists(t, filepath.Join(installedSkillDirPath(repo, catalogOptInSkill), "SKILL.md"),
		"the skill was selected but never materialized")
}

// TestSkillsUninstall_DropsTheChoiceAndRemovesTheFiles is the symmetric half.
func TestSkillsUninstall_DropsTheChoiceAndRemovesTheFiles(t *testing.T) {
	repo := stageInstallRepo(t)

	_, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(installedSkillDirPath(repo, catalogOptInSkill), "SKILL.md"))

	out, err := runSkillsChange(t, skillsUninstallCmd, catalogOptInSkill)
	require.NoError(t, err, "output: %s", out)

	require.NotContains(t, lockedNames(t, repo), catalogOptInSkill,
		"the name is still selected, so the next reconcile puts the files back")
	require.NoDirExists(t, installedSkillDirPath(repo, catalogOptInSkill),
		"ox left behind files it owns and was told to remove")
}

// TestSkillsInstall_IsAllOrNothingOnABadName mirrors `ox skills approve`: a typo
// in the last name must not leave the earlier ones half-installed with no record
// of which.
func TestSkillsInstall_IsAllOrNothingOnABadName(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		repo := stageInstallRepo(t)

		_, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill, "no-such-skill")
		require.Error(t, err)
		require.Contains(t, err.Error(), "no-such-skill")
		require.Contains(t, err.Error(), "nothing was changed")

		require.NotContains(t, lockedNames(t, repo), catalogOptInSkill)
		require.NoDirExists(t, installedSkillDirPath(repo, catalogOptInSkill))
	})

	t.Run("retired", func(t *testing.T) {
		repo := stageInstallRepo(t)

		// `ox-plan` is a real retired name: it shipped before the 0.15.0 rename.
		// Reporting it as merely unknown would send someone hunting for a typo in a
		// name that was correct last release.
		_, err := runSkillsChange(t, skillsInstallCmd, "ox-plan")
		require.Error(t, err)
		require.Contains(t, err.Error(), "retired",
			"a retired name must be distinguishable from a typo: %v", err)

		require.Empty(t, lockedNames(t, repo))
	})
}

// TestSkillsUninstall_NeverDeletesASkillOxDoesNotOwn.
//
// A hand-authored skill is the majority of what is in a real repository's skills
// directory, and ox has no record of it, no way to restore it, and no claim on
// it. Deleting one on a name collision would be unrecoverable data loss from a
// command the human believed was scoped to ox's own files.
func TestSkillsUninstall_NeverDeletesASkillOxDoesNotOwn(t *testing.T) {
	repo := stageInstallRepo(t)

	local := installedSkillDirPath(repo, "my-own-skill")
	require.NoError(t, os.MkdirAll(local, 0o755))
	manifest := filepath.Join(local, "SKILL.md")
	require.NoError(t, os.WriteFile(manifest,
		[]byte("---\nname: my-own-skill\ndescription: mine\n---\n\nbody\n"), 0o644))

	_, err := runSkillsChange(t, skillsUninstallCmd, "my-own-skill")
	require.Error(t, err)
	require.Contains(t, err.Error(), "my-own-skill")
	require.FileExists(t, manifest, "ox deleted a skill it does not own")
}

// TestSkillsUninstall_RefusesASkillSelectedByABundle.
//
// Dropping the NAME of a bundle-selected skill changes nothing: the bundle still
// selects it, so reconcile reinstalls it on the spot. Reporting success there
// would be the worst available outcome — the command says the skill is gone and
// the file is still on disk.
func TestSkillsUninstall_RefusesASkillSelectedByABundle(t *testing.T) {
	repo := stageInstallRepo(t)

	installed := filepath.Join(installedSkillDirPath(repo, "ox-cli-plan"), "SKILL.md")
	require.FileExists(t, installed, "the baseline must already hold a bundle-selected skill")

	_, err := runSkillsChange(t, skillsUninstallCmd, "ox-cli-plan")
	require.Error(t, err)
	require.Contains(t, err.Error(), "core", "the refusal does not name the bundle responsible: %v", err)
	require.FileExists(t, installed)
}

// TestSkillsChange_JSONAlwaysAnswersEveryQuestionItCanAnswer is the wire
// contract, asserted against the BYTES an AI coworker parses.
//
// Every array is present and `[]` when empty, and guidance travels in the
// payload. A key that appears only sometimes forces every reader to guess
// whether its absence means "none" or "this ox does not report that."
func TestSkillsChange_JSONAlwaysAnswersEveryQuestionItCanAnswer(t *testing.T) {
	stageInstallRepo(t)

	got := skillsJSON(t, skillsInstallCmd, catalogOptInSkill)

	for _, key := range []string{"skills", "written", "removed"} {
		raw, ok := got[key]
		require.True(t, ok, "the %q key is absent, so a reader cannot tell empty from unreported: %v", key, got)
		require.NotNil(t, raw, "%q was null rather than an empty array: %v", key, got)
		_, isList := raw.([]any)
		require.True(t, isList, "%q is not an array: %T", key, raw)
	}
	require.NotEmpty(t, got["guidance"], "guidance must travel in the payload, not only in the terminal rendering")

	rows, ok := got["skills"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	row, ok := rows[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, catalogOptInSkill, row["name"])
	require.Equal(t, skillChangeInstalled, row["state"])
}

// TestSkillsInstall_SaysTheChoiceIsSharedWithoutGitJargon.
//
// .sageox/skills.lock.json is committed on purpose, and a reader has to know
// their choice reaches their coworkers. Saying so in git vocabulary teaches
// people plumbing they should never need — git is an implementation detail here
// (see the header of skills_status.go's sibling, cmd/ox/session_commit.go).
func TestSkillsInstall_SaysTheChoiceIsSharedWithoutGitJargon(t *testing.T) {
	stageInstallRepo(t)

	out, err := runSkillsChange(t, skillsInstallCmd, catalogOptInSkill)
	require.NoError(t, err)

	require.Contains(t, out, "skills.lock.json",
		"the output never names the file that carries the choice: %q", out)
	for _, jargon := range []string{"commit", "push", "git "} {
		require.NotContains(t, strings.ToLower(out), jargon,
			"user-facing text leaks git vocabulary %q: %q", jargon, out)
	}
}

// TestSkillsInstallTeam_SeedsThePublishedCopyExactlyOnce.
//
// `--team` is a seed, not a managed install: ox writes the files once and the
// team owns the copy from then on. Overwriting would silently discard whatever
// the team edited into it, which is the one thing publishing is supposed to
// enable.
func TestSkillsInstallTeam_SeedsThePublishedCopyExactlyOnce(t *testing.T) {
	_, team := stageTeamPublishRepo(t)

	out, err := runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.NoError(t, err, "output: %s", out)

	published := filepath.Join(team, "agents", "skills", catalogOptInSkill)
	require.FileExists(t, filepath.Join(published, "SKILL.md"))
	require.FileExists(t, filepath.Join(published, "references", "AUTHORING.md"),
		"publishing copied the manifest but dropped the skill's bundled references")

	// Recorded in the Team Context's own history, so a teammate's next sync sees
	// it. The fixture has no remote, so this also proves the command does not try
	// to push — a push would fail the command outright.
	require.Empty(t, gitOutput(t, team, "status", "--porcelain"),
		"the published files were left unrecorded in the Team Context")
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), catalogOptInSkill)

	// The team's copy is theirs now. A hand edit must survive a second publish.
	manifest := filepath.Join(published, "SKILL.md")
	require.NoError(t, os.WriteFile(manifest, []byte("---\nname: post-cutoff\ndescription: ours now\n---\n"), 0o644))

	_, err = runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.Error(t, err, "a second publish silently overwrote the team's own copy")
	require.Contains(t, err.Error(), "already published")

	data, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	require.Contains(t, string(data), "ours now")
}

// TestSkillsInstallTeam_RefusesAFoldedDescription.
//
// The Team Context frontmatter reader has no block-scalar support for
// `description:`. Publishing a skill whose description is folded stores the
// literal ">-" as its activation surface, so the skill is on disk and invisible
// to every agent — the worst kind of failure, because it looks like a success.
func TestSkillsInstallTeam_RefusesAFoldedDescription(t *testing.T) {
	_, team := stageTeamPublishRepo(t)

	// ox-cli-plan's own description is a folded scalar in the shipped catalog.
	_, err := runSkillsChange(t, skillsInstallCmd, "--team", "ox-cli-plan")
	require.Error(t, err)
	require.Contains(t, err.Error(), "single line",
		"the refusal does not say what is wrong or how to fix it: %v", err)
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", "ox-cli-plan"))
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
	// A REAL sparse checkout, because that is what a Team Context is. Without it
	// the fixture would accept a plain `git add` and the --sparse flag in
	// recordTeamPublish would be untested decoration: git refuses to stage a path
	// outside the cone unless --sparse is passed.
	gitOutput(t, team, "sparse-checkout", "init", "--cone")
	gitOutput(t, team, "sparse-checkout", "set", "docs")

	const teamID = "team_publish_test"
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		ProjectID: "proj_publish", WorkspaceID: "ws_publish",
		TeamID: teamID, TeamName: "Publish Test Team",
	}))
	require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID: teamID, TeamName: "Publish Test Team", Slug: "publish-test-team", Path: team,
		}},
	}))
	return repo, team
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// TestSkillsCommands_ResolveThroughRootCmd guards the failure the sibling
// approval command already guards: a complete implementation with no way to
// reach it.
//
// Resolved through rootCmd rather than by walking skillsCmd's own children.
// Walking skillsCmd stays green when `rootCmd.AddCommand(skillsCmd)` is deleted
// — the entire `ox skills` family would vanish from the binary while the test
// that claims to guard reachability kept passing.
func TestSkillsCommands_ResolveThroughRootCmd(t *testing.T) {
	for _, tc := range []struct {
		path []string
		want *cobra.Command
	}{
		{[]string{"skills", "list"}, skillsListCmd},
		{[]string{"skills", "catalog"}, skillsCatalogCmd},
		{[]string{"skills", "install"}, skillsInstallCmd},
		{[]string{"skills", "add"}, skillsInstallCmd},
		{[]string{"skills", "uninstall"}, skillsUninstallCmd},
		{[]string{"skills", "remove"}, skillsUninstallCmd},
	} {
		t.Run(strings.Join(tc.path, " "), func(t *testing.T) {
			found, args, err := rootCmd.Find(tc.path)
			require.NoError(t, err)
			require.Empty(t, args)
			require.Same(t, tc.want, found, "`ox %s` does not resolve to its command", strings.Join(tc.path, " "))
			require.True(t, found.Runnable())
		})
	}
}

// TestSkillsChangeAdviceNamesCommandsThatExist: guidance that promises a command
// which does not exist is worse than silence — it sends a human to a terminal to
// be told "unknown command" by the tool that just told them to run it.
func TestSkillsChangeAdviceNamesCommandsThatExist(t *testing.T) {
	t.Run("install", func(t *testing.T) {
		requireAdviceResolves(t, skillsChangeGuidance(skillsChangeOutput{
			Skills: []skillChangeRow{{Name: "post-cutoff", State: skillChangeInstalled}},
		}))
	})
	t.Run("team publish", func(t *testing.T) {
		requireAdviceResolves(t, skillsChangeGuidance(skillsChangeOutput{
			TeamContext: "/tmp/team",
			Skills:      []skillChangeRow{{Name: "post-cutoff", State: skillChangePublished}},
		}))
	})
	t.Run("uninstall", func(t *testing.T) {
		requireAdviceResolves(t, skillsChangeGuidance(skillsChangeOutput{
			Skills: []skillChangeRow{{Name: "post-cutoff", State: skillChangeUninstalled}},
		}))
	})
	t.Run("nothing to do", func(t *testing.T) {
		requireAdviceResolves(t, skillsChangeGuidance(skillsChangeOutput{
			Skills: []skillChangeRow{{Name: "post-cutoff", State: skillChangeAlready}},
		}))
	})
}
