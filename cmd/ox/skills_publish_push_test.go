package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// stageTeamPublishRepoWithRemote is stageTeamPublishRepo with a real remote: the
// Team Context tracks a bare repository, as a daemon clone does.
func stageTeamPublishRepoWithRemote(t *testing.T) (repo, team, bare, branch string) {
	t.Helper()
	repo = stageInstallRepo(t)
	bare = filepath.Join(t.TempDir(), "team.git")
	initBareRemote(t, bare)
	team = t.TempDir()
	gitInitRepo(t, team)
	require.NoError(t, os.WriteFile(filepath.Join(team, "README.md"), []byte("team\n"), 0o644))
	gitOutput(t, team, "add", "README.md")
	gitOutput(t, team, "commit", "-q", "-m", "init")
	gitOutput(t, team, "remote", "add", "origin", bare)
	gitOutput(t, team, "push", "-q", "-u", "origin", "HEAD")
	branch = strings.TrimSpace(gitOutput(t, team, "rev-parse", "--abbrev-ref", "HEAD"))
	sparseTeamCheckout(t, team)
	wireTeamContext(t, repo, team)
	return repo, team, bare, branch
}

func publishJSON(t *testing.T, name string) skillsChangeOutput {
	t.Helper()
	out, err := runSkillsChange(t, skillsPublishCmd, "--json", name)
	require.NoError(t, err, "a publish whose commit succeeded must not fail; output: %s", out)
	var got skillsChangeOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output was not JSON: %q", out)
	return got
}

// Publishing must put the skill where coworkers' syncs read it — the remote —
// and say so only when it did.
// Failure prevented (ox#1477): publish committed into the Team Context, printed
// "Distribution is automatic", and nothing ever pushed the commit; no coworker
// received the skill.
func TestSkillsPublish_PushesThePublishCommit(t *testing.T) {
	repo, _, bare, branch := stageTeamPublishRepoWithRemote(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)

	got := publishJSON(t, teamPublishSkill)

	require.True(t, got.Pushed, "guidance: %s", got.Guidance)
	require.Empty(t, got.NotPushedReason)
	require.Equal(t, "add team skill: "+teamPublishSkill,
		strings.TrimSpace(gitOutput(t, bare, "log", "-1", "--format=%s", branch)),
		"the publish commit is not on the Team Context remote")
	require.Contains(t, got.Guidance, "pushed")
	require.NotContains(t, got.Guidance, "NOT pushed")
	requireAdviceResolves(t, got.Guidance)
}

// A failed push leaves a correct local commit: the publish succeeds, says the
// commit is local only, and names the retry the daemon will also run.
func TestSkillsPublish_ReportsAFailedPushTruthfully(t *testing.T) {
	withTeamPublishDaemon(t, true)
	repo, team, bare, branch := stageTeamPublishRepoWithRemote(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)
	before := strings.TrimSpace(gitOutput(t, bare, "rev-parse", branch))
	// The remote vanishes after the clone: the commit is ox's to push, but the
	// push cannot reach anything.
	require.NoError(t, os.RemoveAll(bare))

	got := publishJSON(t, teamPublishSkill)

	require.False(t, got.Pushed)
	require.NotEmpty(t, got.NotPushedReason)
	require.Contains(t, got.Guidance, "NOT pushed yet, so no coworker has it")
	require.Contains(t, got.Guidance, "`ox sync`", "the publisher must learn how to retry")
	require.NotContains(t, got.Guidance, "Distribution is automatic")
	requireAdviceResolves(t, got.Guidance)
	require.Equal(t, "add team skill: "+teamPublishSkill,
		strings.TrimSpace(gitOutput(t, team, "log", "-1", "--format=%s")), "the local commit must survive a failed push")
	require.NotEmpty(t, before)
}

// A commit ox did not make, sitting ahead of the publish, keeps the whole branch
// local — and the publisher is told exactly which commit and what to run.
func TestSkillsPublish_DoesNotPushACoworkersOwnCommit(t *testing.T) {
	repo, team, bare, branch := stageTeamPublishRepoWithRemote(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)
	require.NoError(t, os.MkdirAll(filepath.Join(team, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(team, "docs", "draft.md"), []byte("not ready\n"), 0o644))
	gitOutput(t, team, "add", "--sparse", "docs/draft.md")
	gitOutput(t, team, "commit", "-q", "-m", "my private draft")
	before := strings.TrimSpace(gitOutput(t, bare, "rev-parse", branch))

	got := publishJSON(t, teamPublishSkill)

	require.False(t, got.Pushed)
	require.Contains(t, got.NotPushedReason, `"my private draft"`)
	require.Contains(t, got.Guidance, "git -C "+team+" push")
	require.NotContains(t, got.Guidance, "retries the push", "ox must not promise a push its own rule refuses")
	require.Equal(t, before, strings.TrimSpace(gitOutput(t, bare, "rev-parse", branch)),
		"publish pushed a commit that was not ox's")
}

// With the daemon disabled nothing retries a failed publish push, so the
// publisher must get the manual command, never a promise that ox retries.
func TestSkillsPublish_FailedPushWithDaemonDisabledGivesTheManualPush(t *testing.T) {
	withTeamPublishDaemon(t, false)
	repo, team, bare, _ := stageTeamPublishRepoWithRemote(t)
	stageLocalPublishableSkill(t, repo, teamPublishSkill)
	require.NoError(t, os.RemoveAll(bare))

	got := publishJSON(t, teamPublishSkill)

	require.False(t, got.Pushed)
	require.Contains(t, got.NotPushedReason, "daemon is disabled")
	require.Contains(t, got.Guidance, "git -C "+team+" push")
	require.NotContains(t, got.Guidance, "ox retries the push")
}

// withTeamPublishDaemon pins whether a daemon retries failed publish pushes,
// so the guidance under test does not depend on this host.
func withTeamPublishDaemon(t *testing.T, retries bool) {
	t.Helper()
	orig := teamPublishDaemonRetries
	t.Cleanup(func() { teamPublishDaemonRetries = orig })
	teamPublishDaemonRetries = func() bool { return retries }
}

// pushTeamPublish reads what is ahead of the remote before it runs git push.
// Nothing ahead means a concurrent sync already carried the commit, which is
// published, not a failure; a branch with no upstream is a human's to fix.
func TestPushTeamPublish_DecidesFromWhatIsAheadOfTheRemote(t *testing.T) {
	tests := []struct {
		name       string
		stage      func(t *testing.T) (repo, team string)
		wantPushed bool
		wantReason string
	}{
		{
			name: "nothing ahead of the remote",
			stage: func(t *testing.T) (string, string) {
				repo, team, _, _ := stageTeamPublishRepoWithRemote(t)
				return repo, team
			},
			wantPushed: true,
		},
		{
			name: "no upstream branch",
			stage: func(t *testing.T) (string, string) {
				team := t.TempDir()
				gitInitRepo(t, team)
				require.NoError(t, os.WriteFile(filepath.Join(team, "README.md"), []byte("team\n"), 0o644))
				gitOutput(t, team, "add", "README.md")
				gitOutput(t, team, "commit", "-q", "-m", "add team skill: deploy")
				return team, team
			},
			wantReason: "tracks no remote branch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, team := tt.stage(t)
			head := strings.TrimSpace(gitOutput(t, team, "rev-parse", "HEAD"))

			got := pushTeamPublish(repo, team)

			require.Equal(t, tt.wantPushed, got.pushed)
			require.False(t, got.retried, "nothing here is a failed attempt for the daemon to retry")
			if tt.wantReason == "" {
				require.Empty(t, got.reason)
			} else {
				require.Contains(t, got.reason, tt.wantReason)
			}
			require.Equal(t, head, strings.TrimSpace(gitOutput(t, team, "rev-parse", "HEAD")), "deciding must not move the branch")
		})
	}
}

// The default predicate is wired to the real daemon switch: with the daemon
// turned off, nothing retries a failed publish push, so none may be promised.
func TestTeamPublishDaemonRetries_FollowsTheDaemonSwitch(t *testing.T) {
	t.Setenv("SAGEOX_DAEMON", "false")
	require.False(t, teamPublishDaemonRetries())
}
