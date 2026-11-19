package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/addons"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// A coworker pulls from the remote, so a commit in the publisher's checkout
// alone cannot deliver a team add-on.
func TestAddonsWritesReachTeamRemote(t *testing.T) {
	const skill = "agents/skills/post-cutoff/SKILL.md"
	for _, cmd := range []*cobra.Command{addonsInstallCmd, addonsUpdateCmd, addonsRemoveCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			_, team, bare, branch := stageTeamPublishRepoWithRemote(t)
			require.NoError(t, os.MkdirAll(filepath.Join(team, ".sageox"), 0o755))
			require.NoError(t, gitserver.EnsureCheckoutGitignore(team))
			// Seed shared policy. Local-only housekeeping commits must stay local.
			gitOutput(t, team, "push", "origin", branch)

			var installedSkill string
			if cmd != addonsInstallCmd {
				_, err := runSkillsChange(t, addonsInstallCmd, "post-cutoff")
				require.NoError(t, err)
				installedSkill = gitOutput(t, team, "show", "HEAD:"+skill)
				if cmd == addonsUpdateCmd {
					// An update must replace actual team content on the remote.
					require.NoError(t, os.WriteFile(filepath.Join(team, filepath.FromSlash(skill)), []byte("Team edit\n"), 0o644))
					gitOutput(t, team, "commit", "-m", "edit team skill", "--", skill)
				}
				// Each verb must be able to fail on its own before the fix.
				gitOutput(t, team, "push", "origin", branch)
			}
			before := gitOutput(t, bare, "rev-parse", branch)
			out, err := runSkillsChange(t, cmd, "--json", "post-cutoff")
			require.NoError(t, err)
			remoteTree := gitOutput(t, bare, "ls-tree", "-r", "--name-only", branch)
			switch cmd {
			case addonsRemoveCmd:
				require.NotContains(t, remoteTree, skill)
			case addonsUpdateCmd:
				require.Equal(t, installedSkill, gitOutput(t, bare, "show", branch+":"+skill))
			default:
				require.Contains(t, remoteTree, skill)
			}
			require.Contains(t, remoteTree, addons.LockRelativePath)
			newTip := gitOutput(t, bare, "rev-parse", branch)
			require.NotEqual(t, before, newTip, "the remote branch did not move")
			require.Equal(t, gitOutput(t, team, "rev-parse", "HEAD"), newTip)
			var status struct {
				Pushed bool `json:"pushed"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &status))
			require.True(t, status.Pushed, "%s", out)
		})
	}
}

// Saving a valid note through the command must make its bytes available in
// the Team Context remote. Local observation round trips miss this boundary.
func TestMemoryPutReachesTeamRemote(t *testing.T) {
	_, _, bare, branch := stageTeamPublishRepoWithRemote(t)
	const note = "We adopted the weekly release checklist."
	out, err := runSkillsChange(t, memoryPutCmd, `{"content":"`+note+`"}`)
	require.NoError(t, err)
	names := strings.Fields(gitOutput(t, bare, "ls-tree", "-r", "--name-only", branch, "--", "memory/.observations/"))
	require.Len(t, names, 1, "observation did not reach the remote")
	require.Contains(t, gitOutput(t, bare, "show", branch+":"+names[0]), note)
	require.Contains(t, out, "Pushed to the Team Context remote.")
}

// A failed push leaves a usable local commit and must report that the team
// has yet to receive the add-on.
func TestAddonsInstallReportsLocalCommitWhenRemoteUnavailable(t *testing.T) {
	withTeamPublishDaemon(t, true)
	_, team, bare, _ := stageTeamPublishRepoWithRemote(t)
	require.NoError(t, os.Rename(bare, bare+".offline"))

	out, err := runSkillsChange(t, addonsInstallCmd, "post-cutoff")
	require.NoError(t, err)
	require.Contains(t, gitOutput(t, team, "show", "HEAD:"+addons.LockRelativePath), "post-cutoff")
	require.Contains(t, out, "not pushed")
	require.Contains(t, out, "ox sync")
}

// A credential in an observation commit message must keep the commit local and
// tell the writer to redact, never promise a retry that would push it.
func TestPushTeamPublishAsksForRedactionWhenObservationMessageHasSecret(t *testing.T) {
	withTeamPublishDaemon(t, true)
	repo, team, bare, branch := stageTeamPublishRepoWithRemote(t)
	rel := "memory/.observations/2026-11-19/note.jsonl"
	require.NoError(t, os.MkdirAll(filepath.Join(team, filepath.Dir(rel)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(team, rel), []byte(`{"headline":"ordinary"}`+"\n"), 0o644))
	gitOutput(t, team, "add", "--sparse", rel)
	gitOutput(t, team, "commit", "-q", "-m", "observation: record team memory", "-m", "AKIA"+"QWERTYUIOPASDFGH")
	before := gitOutput(t, bare, "rev-parse", branch)

	got := pushTeamPublish(repo, team)

	require.False(t, got.pushed)
	require.True(t, got.requiresRedaction, "reason: %s", got.reason)
	require.False(t, got.retried)
	require.Equal(t, before, gitOutput(t, bare, "rev-parse", branch), "a refused observation reached the remote")
	require.Contains(t, teamWritePushGuidance(team, got), "Clean the sensitive content")
}

func TestTeamWritePushGuidance(t *testing.T) {
	team := "/tmp/team context"
	tests := []struct {
		name   string
		result teamPublishPush
		want   string
	}{
		{name: "pushed", result: teamPublishPush{pushed: true}, want: "Pushed to the Team Context remote."},
		{name: "needs redaction", result: teamPublishPush{reason: "refused", requiresRedaction: true},
			want: "Committed locally. Clean the sensitive content from the unpushed history before retrying. refused"},
		{name: "daemon retries", result: teamPublishPush{reason: "offline", retried: true},
			want: "Committed locally, not pushed: offline. Run `ox sync` to retry now."},
		{name: "human must push", result: teamPublishPush{reason: "foreign commit"},
			want: "Committed locally, not pushed: foreign commit. After resolving this, run `git -C " + shellQuote(team) + " push`."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, teamWritePushGuidance(team, tt.result))
		})
	}
	got := teamPublishGuidance([]string{"deploy"}, skillsChangeOutput{NotPushedReason: "refused", requiresRedaction: true})
	require.Equal(t, "deploy committed locally. Clean the sensitive content from the unpushed history before retrying. refused", got)
}
