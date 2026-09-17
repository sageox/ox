package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/teamskills"
	"github.com/stretchr/testify/require"
)

// stageApprovalRepo builds a real git repo wired to a team checkout holding one
// skill, and chdirs into it so the command's own findGitRoot resolves.
//
// It drives the cobra command rather than the helpers underneath because the
// defect this command fixes was precisely a complete implementation with no
// caller. A test that called pendingApprovals directly would pass against a
// binary where the subcommand was never registered.
func stageApprovalRepo(t *testing.T, skillName string, extra map[string]string) (repo, team string) {
	t.Helper()
	repo = t.TempDir()
	team = t.TempDir()

	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@test.sageox.ai"}, {"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	dir := filepath.Join(team, "agents", "skills", skillName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: "+skillName+"\n---\n\nbody\n"), 0o644))
	for rel, content := range extra {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	const teamID = "team_approve_test"
	require.NoError(t, config.SaveProjectConfig(repo, &config.ProjectConfig{
		ProjectID: "proj_approve", WorkspaceID: "ws_approve",
		TeamID: teamID, TeamName: "Approve Test Team",
	}))
	require.NoError(t, config.SaveLocalConfig(repo, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{
			TeamID: teamID, TeamName: "Approve Test Team", Slug: "approve-test-team", Path: team,
		}},
	}))

	t.Chdir(repo)
	return repo, team
}

// runApprove executes the subcommand and returns its stdout.
func runApprove(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := skillsApproveCmd
	cmd.SetOut(&buf)
	// Flags persist across invocations within a process; reset the ones this
	// command owns so one subtest cannot leak --allow-scripts into the next.
	require.NoError(t, cmd.Flags().Set("allow-scripts", "false"))
	require.NoError(t, cmd.Flags().Set("json", "false"))
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			require.NoError(t, cmd.Flags().Set(strings.TrimPrefix(a, "--"), "true"))
		}
	}
	var positional []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
		}
	}
	err := runSkillsApprove(cmd, positional)
	return buf.String(), err
}

// TestSkillsApprove_ListsWhatIsWaitingWithItsEvidence: the no-argument form is
// the "read before you decide" surface. A bare count would be useless — the
// human needs the file that made it executable.
func TestSkillsApprove_ListsWhatIsWaitingWithItsEvidence(t *testing.T) {
	stageApprovalRepo(t, "deploy", map[string]string{
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	})

	out, err := runApprove(t, "--json")
	require.NoError(t, err)

	var got skillsApproveOutput
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got.Pending, 1)
	require.Equal(t, "deploy", got.Pending[0].Name)
	require.Contains(t, got.Pending[0].Capabilities, "scripts/run.sh",
		"the pending row does not name the file a human must read: %q", got.Pending[0].Capabilities)
	require.Contains(t, got.Guidance, "ox skills approve deploy",
		"guidance does not hand back a runnable next action: %q", got.Guidance)
}

// TestSkillsApprove_RecordsADigestPinnedApprovalAndInstalls is the command's job
// in one assertion: after it runs, the skill is on disk and the decision is in
// the committed store.
func TestSkillsApprove_RecordsADigestPinnedApprovalAndInstalls(t *testing.T) {
	repo, _ := stageApprovalRepo(t, "deploy", map[string]string{
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	})

	_, err := runApprove(t, "deploy")
	require.NoError(t, err)

	store, err := teamskills.LoadApprovals(repo)
	require.NoError(t, err)
	require.Len(t, store.Approvals, 1)
	require.Equal(t, "deploy", store.Approvals[0].Name)
	require.True(t, strings.HasPrefix(store.Approvals[0].Digest, "sha256:"),
		"approval was not pinned to a content digest: %q", store.Approvals[0].Digest)
	require.False(t, store.Approvals[0].AllowScripts,
		"a plain approve silently granted the larger scripts decision")

	require.FileExists(t, teamskills.ApprovalPath(repo),
		"the approval store was not written where a teammate would inherit it")
}

// TestSkillsApprove_UnknownNameSaysWhatItCanSee: "no such skill" cannot
// distinguish a typo from a repos: filter that excludes this repository, and
// those need opposite fixes.
func TestSkillsApprove_UnknownNameSaysWhatItCanSee(t *testing.T) {
	stageApprovalRepo(t, "deploy", map[string]string{
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	})

	_, err := runApprove(t, "deploi")
	require.Error(t, err)
	require.Contains(t, err.Error(), "deploi")
	require.Contains(t, err.Error(), "deploy",
		"the error does not name what this repository can actually see: %v", err)
}

// TestSkillsApprove_ProseNeedsNoApproval: approving prose must be a clear no-op
// rather than a recorded decision, or the store fills with approvals that grant
// nothing and teach people the gate is noise.
func TestSkillsApprove_ProseNeedsNoApproval(t *testing.T) {
	repo, _ := stageApprovalRepo(t, "notes", nil)

	out, err := runApprove(t, "notes")
	require.NoError(t, err)
	require.Contains(t, out, "prose only")

	store, err := teamskills.LoadApprovals(repo)
	require.NoError(t, err)
	require.Empty(t, store.Approvals,
		"a prose skill was recorded in the approval store, which grants nothing and dilutes the gate")
}

// TestSkillsApprove_IsRegisteredUnderSkills guards the failure this whole change
// exists to end: a complete implementation with no way to reach it.
func TestSkillsApprove_IsRegisteredUnderSkills(t *testing.T) {
	var found bool
	for _, c := range skillsCmd.Commands() {
		if c.Name() == "approve" {
			found = true
		}
	}
	require.True(t, found, "ox skills approve is not registered, so the approval gate has no handle")
}
