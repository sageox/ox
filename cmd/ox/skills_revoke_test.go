package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/teamskills"
	"github.com/stretchr/testify/require"
)

func runRevoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := skillsRevokeCmd
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.NoError(t, cmd.Flags().Set("json", "false"))
	cmd.Flags().Lookup("json").Changed = false
	t.Cleanup(func() {
		cmd.SetOut(nil)
		cmd.SetErr(nil)
		require.NoError(t, cmd.Flags().Set("json", "false"))
		cmd.Flags().Lookup("json").Changed = false
	})

	if err := cmd.ParseFlags(args); err != nil {
		return output.String(), err
	}
	positional := cmd.Flags().Args()
	if err := cmd.ValidateArgs(positional); err != nil {
		return output.String(), err
	}
	err := cmd.RunE(cmd, positional)
	return output.String(), err
}

func TestSkillsRevoke_RemovesApprovalAndRunnableSkillEndToEnd(t *testing.T) {
	repo, team := stageApprovalRepo(t, "deploy", nil)
	teamManifest := filepath.Join(team, "agents", "skills", "deploy", "SKILL.md")
	require.NoError(t, os.WriteFile(teamManifest,
		[]byte("---\nname: deploy\nallowed-tools: Bash\n---\n\nbody\n"), 0o644))

	_, err := runApprove(t, "deploy")
	require.NoError(t, err)
	installed := installedSkillDir(repo, "deploy")
	require.FileExists(t, filepath.Join(installed, "SKILL.md"))

	output, err := runRevoke(t, "deploy")
	require.NoError(t, err)
	require.Contains(t, output, "revoked deploy")
	require.Contains(t, output, "removed from every selected skill target")
	require.NoDirExists(t, installed,
		"revoking the manifest approval left runnable Team Skill content on disk")

	store, err := teamskills.LoadApprovals(repo)
	require.NoError(t, err)
	require.Empty(t, store.Approvals, "the digest-pinned approval survived revocation")
}

func TestSkillsRevoke_RemovesScriptsButKeepsUngatedInstructions(t *testing.T) {
	repo, _ := stageApprovalRepo(t, "deploy", map[string]string{
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	})
	_, err := runApprove(t, "--allow-scripts", "deploy")
	require.NoError(t, err)
	installed := installedSkillDir(repo, "deploy")
	require.FileExists(t, filepath.Join(installed, "scripts", "run.sh"))

	_, err = runRevoke(t, "deploy")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(installed, "SKILL.md"),
		"revoking a scripts-only grant removed instructions that never needed approval")
	require.NoFileExists(t, filepath.Join(installed, "scripts", "run.sh"),
		"revoking the approval left its bundled script runnable")
}

func TestSkillsRevoke_IsAllOrNothingOnAnUnapprovedName(t *testing.T) {
	repo, team := stageApprovalRepo(t, "deploy", nil)
	teamManifest := filepath.Join(team, "agents", "skills", "deploy", "SKILL.md")
	require.NoError(t, os.WriteFile(teamManifest,
		[]byte("---\nname: deploy\nallowed-tools: Bash\n---\n\nbody\n"), 0o644))
	_, err := runApprove(t, "deploy")
	require.NoError(t, err)

	_, err = runRevoke(t, "deploy", "typo")
	require.ErrorContains(t, err, "nothing was revoked")

	store, err := teamskills.LoadApprovals(repo)
	require.NoError(t, err)
	require.Len(t, store.Approvals, 1,
		"a later invalid name left the earlier approval half-revoked")
	require.FileExists(t, filepath.Join(installedSkillDir(repo, "deploy"), "SKILL.md"))
}

func TestSkillsRevoke_JSONContractAndCommandDiscovery(t *testing.T) {
	_, team := stageApprovalRepo(t, "deploy", nil)
	teamManifest := filepath.Join(team, "agents", "skills", "deploy", "SKILL.md")
	require.NoError(t, os.WriteFile(teamManifest,
		[]byte("---\nname: deploy\nallowed-tools: Bash\n---\n\nbody\n"), 0o644))
	_, err := runApprove(t, "deploy")
	require.NoError(t, err)

	output, err := runRevoke(t, "--json", "deploy")
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &payload))
	require.Equal(t, []any{"deploy"}, payload["revoked"])
	require.NotEmpty(t, payload["guidance"])

	found, args, err := rootCmd.Find([]string{"skills", "revoke"})
	require.NoError(t, err)
	require.Empty(t, args)
	require.Same(t, skillsRevokeCmd, found)
	require.Contains(t, skillsRevokeCmd.Long, ".sageox/team-skills.approvals.json")
}
