package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// Doctor must repair obsolete desired state even after background cleanup left no files to change.
func TestDoctorRepairsRetiredSkillSelections(t *testing.T) {
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		t.Run(root, func(t *testing.T) {
			repo := t.TempDir()
			cmd := exec.Command("git", "init", "--quiet")
			cmd.Dir = repo
			require.NoError(t, cmd.Run())
			t.Chdir(repo)
			target := adapterprotocol.SkillTarget{Key: "project", Root: root, Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject}
			targets := []adapterprotocol.SkillTarget{target}
			desired := skillmanager.AddBundles(skillmanager.DefaultDesired(targets), "attest")
			desired.Names = []string{"ox-cli-attest-goal", "ox-attest-create"}
			// The background path preserves saved intent while skipping the retired catalog entries.
			_, err := skillmanager.Reconcile(repo, version.Version, desired, targets)
			require.NoError(t, err)
			before, err := os.ReadFile(skillmanager.LockPath(repo))
			require.NoError(t, err)
			_, err = reconcileCommittedSkillsNonBlocking(repo)
			require.NoError(t, err)
			after, err := os.ReadFile(skillmanager.LockPath(repo))
			require.NoError(t, err)
			require.Equal(t, before, after)
			plan, err := planCommittedSkills(repo)
			require.NoError(t, err)
			require.Empty(t, plan.Creates)
			require.Empty(t, plan.Updates)
			require.Empty(t, plan.Removes)
			result := checkClaudeSkills(false)
			require.False(t, result.passed)
			require.Contains(t, result.message, "retired skill selections")
			result = checkClaudeSkills(true)
			require.True(t, result.passed, "%+v", result)
			saved, _, err := skillmanager.LoadDesired(repo)
			require.NoError(t, err)
			require.Empty(t, saved.Names)
			require.NotContains(t, saved.Bundles, skillmanager.BundleRef{ID: "attest"})
			require.FileExists(t, filepath.Join(repo, root, "ox-cli-consult", "SKILL.md"))
			require.True(t, checkClaudeSkills(false).passed)
		})
	}
}
