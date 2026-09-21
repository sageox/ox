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

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/gitutil"
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

// TestSkillsInstallTeam_AFailedPublishCanBeRetried.
//
// The files are written before git records them, and the pre-flight refuses any
// skill whose directory already exists. Those two facts together turn a seed left
// behind by a failed commit into something far worse than a stray file: the
// publish can never be run again, and the only thing ox says about it is that the
// skill is "already published" — to a team that has never seen it. The human has
// to know to delete a directory in a checkout they did not know they had.
func TestSkillsInstallTeam_AFailedPublishCanBeRetried(t *testing.T) {
	_, team := stageTeamPublishRepo(t)

	// A directory where git must write a file. Portable in a way chmod and hook
	// scripts are not (no exec bit, no permission semantics, no global config),
	// deterministic, and it lands on the COMMIT — after `git add` has already
	// staged the seed, which is precisely the window the rollback must cover.
	editMsg := filepath.Join(team, ".git", "COMMIT_EDITMSG")
	require.NoError(t, os.RemoveAll(editMsg))
	require.NoError(t, os.Mkdir(editMsg, 0o755))

	_, err := runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.Error(t, err, "the commit cannot have succeeded with COMMIT_EDITMSG unwritable")

	published := filepath.Join(team, "agents", "skills", catalogOptInSkill)
	require.NoDirExists(t, published,
		"the failed publish left its seed on disk, so every retry is refused as already published")
	require.Empty(t, gitOutput(t, team, "diff", "--cached", "--name-only"),
		"the failed publish left the skill staged in the team's index")

	// Whatever caused it has passed, and the human does the obvious thing: runs
	// the command again.
	require.NoError(t, os.RemoveAll(editMsg))

	out, retryErr := runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.NoError(t, retryErr, "the retry was refused after a failure the human did not cause: %s", out)
	require.FileExists(t, filepath.Join(published, "SKILL.md"))
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), catalogOptInSkill)
}

// TestSkillsInstallTeam_RollsBackInATeamContextWithNoHistory.
//
// A Team Context that has been created but never committed into has no HEAD, and
// the rollback cannot restore an index from a commit that does not exist. It is
// the same customer promise as the retry above — a failed publish leaves nothing
// behind — reached through the branch that a repository's first publish takes.
func TestSkillsInstallTeam_RollsBackInATeamContextWithNoHistory(t *testing.T) {
	repo := stageInstallRepo(t)
	team := t.TempDir()
	gitInitRepo(t, team) // no commit, so HEAD is unborn
	sparseTeamCheckout(t, team)
	wireTeamContext(t, repo, team)

	editMsg := filepath.Join(team, ".git", "COMMIT_EDITMSG")
	require.NoError(t, os.RemoveAll(editMsg))
	require.NoError(t, os.Mkdir(editMsg, 0o755))

	_, err := runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.Error(t, err)

	require.NoDirExists(t, filepath.Join(team, "agents", "skills", catalogOptInSkill))
	// ls-files rather than a diff: there is no HEAD to diff against, and the index
	// is the thing that decides what the team's first commit will contain.
	require.Empty(t, gitOutput(t, team, "ls-files", "--", "agents"),
		"the failed publish stayed in the index, so the team's first commit carries a skill ox failed to publish")
}

// TestSkillsInstallTeam_CommitsOnlyWhatItPublished.
//
// A Team Context is a checkout a human works in, so its index can already hold a
// change of theirs. A commit that swept that in would put their unfinished work
// into the team's history under a message about a skill — authored by them,
// pushed by the daemon, and discovered later by someone doing archeology on a
// commit that claims to be about something else.
func TestSkillsInstallTeam_CommitsOnlyWhatItPublished(t *testing.T) {
	_, team := stageTeamPublishRepo(t)

	theirs := filepath.Join(team, "README.md")
	require.NoError(t, os.WriteFile(theirs, []byte("team\ntheir unfinished edit\n"), 0o644))
	gitOutput(t, team, "add", "README.md")

	out, err := runSkillsChange(t, skillsInstallCmd, "--team", catalogOptInSkill)
	require.NoError(t, err, "output: %s", out)

	committed := gitOutput(t, team, "show", "--pretty=format:", "--name-only", "HEAD")
	require.Contains(t, committed, "agents/skills/"+catalogOptInSkill+"/SKILL.md",
		"the publish did not record what it published")
	require.NotContains(t, committed, "README.md",
		"ox swept the team's own staged work into a commit titled for its publish")
	require.Contains(t, gitOutput(t, team, "diff", "--cached", "--name-only"), "README.md",
		"the team's staged change did not survive the publish")
}

// TestSkillsInstallTeam_WaitsForTheTeamContextLock.
//
// The daemon fetches, pulls and rebases the same checkout under
// gitutil.WithRepoLock. A publish that ignored it could stage against an index
// another process is mid-way through rewriting, or write files into a tree a
// rebase is about to move — the 2026-09-02 interleaving, reached from the CLI.
//
// The lock is asserted over the FILE WRITES as well as the commit: holding it
// only for the git transaction would still let a rebase run against a tree full
// of untracked files this command had already put there.
func TestSkillsInstallTeam_WaitsForTheTeamContextLock(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)

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

	// publishCatalogSkillsToTeam rather than the cobra command: require's
	// FailNow is not callable from a non-test goroutine.
	published := make(chan error, 1)
	go func() {
		_, err := publishCatalogSkillsToTeam(repo, []string{catalogOptInSkill})
		published <- err
	}()

	select {
	case err := <-published:
		t.Fatalf("the publish ran straight through a lock another ox process was holding: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	require.NoDirExists(t, filepath.Join(team, "agents", "skills", catalogOptInSkill),
		"the publish wrote into the Team Context while another ox process held it")

	close(release)
	require.NoError(t, <-holder)
	require.NoError(t, <-published, "the publish failed once the lock was free")
	require.Contains(t, gitOutput(t, team, "log", "-1", "--pretty=%s"), catalogOptInSkill)
}

// TestSkillsInstallTeam_ConcurrentPublishersDoNotClobberCommittedSeed proves
// the existence check is covered by the same lock as the writes. If both
// callers inspect before locking, both see an empty destination; the loser then
// overwrites the winner, gets "nothing to commit", and its rollback deletes the
// winner's committed files from the worktree.
func TestSkillsInstallTeam_ConcurrentPublishersDoNotClobberCommittedSeed(t *testing.T) {
	repo, team := stageTeamPublishRepo(t)

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
			_, err := publishCatalogSkillsToTeam(repo, []string{catalogOptInSkill})
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
	require.FileExists(t, filepath.Join(team, "agents", "skills", catalogOptInSkill, "SKILL.md"),
		"the losing publisher's rollback removed the winning publisher's committed seed")
	require.Equal(t, "2", strings.TrimSpace(gitOutput(t, team, "rev-list", "--count", "HEAD")),
		"concurrent publishers created more than one publish commit")
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
		TeamID: teamID, TeamName: "Publish Test Team",
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
