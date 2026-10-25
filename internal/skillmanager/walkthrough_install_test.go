package skillmanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// The proactive trigger must reach both Claude's native skill directory and
// the shared agent directory on fresh installs and ordinary catalog refreshes.
func TestWalkthroughDefaultInstallAndRefresh(t *testing.T) {
	const version = "2.0.0"
	canonical, err := skills.Selected(version, []string{"ox-cli-walkthrough"})
	require.NoError(t, err)
	require.Len(t, canonical, 1)
	targets := []adapterprotocol.SkillTarget{
		{Key: "claude-project", Root: ".claude/skills", Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject, LinkPolicy: adapterprotocol.SkillLinkPolicyReject},
		sharedTarget(),
	}
	for _, refresh := range []bool{false, true} {
		name := "fresh"
		if refresh {
			name = "refresh"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			desired := DefaultDesired(targets)
			if refresh {
				previous := []byte("---\nname: ox-cli-walkthrough\ndescription: Read an existing walkthrough.\n---\n\nRun ox walkthrough with a recording ID.\n")
				old := fakeCatalog{revision: "previous-walkthrough-catalog", skill: skills.Skill{
					Name: "ox-cli-walkthrough", Content: previous, Version: "1.0.0",
					Files: []skills.File{{Path: "SKILL.md", Content: previous}},
				}}
				plan, err := planWithSource(repo, "1.0.0", desired, targets, old)
				require.NoError(t, err)
				require.NoError(t, Apply(plan))
			}
			plan, err := Reconcile(repo, version, desired, targets)
			require.NoError(t, err)
			require.Empty(t, plan.Conflicts)
			require.Empty(t, plan.Warnings)
			for _, target := range targets {
				relative := filepath.Join(filepath.FromSlash(target.Root), "ox-cli-walkthrough", "SKILL.md")
				installed, err := os.ReadFile(filepath.Join(repo, relative))
				require.NoError(t, err)
				require.Equal(t, canonical[0].Content, installed, "installed copy must match the embedded canonical skill")
				if refresh {
					require.Contains(t, actionPaths(plan.Updates), relative)
				} else {
					require.Contains(t, actionPaths(plan.Creates), relative)
				}
			}
			again, err := Reconcile(repo, version, desired, targets)
			require.NoError(t, err)
			require.Empty(t, again.Creates)
			require.Empty(t, again.Updates)
			require.Empty(t, again.Removes)
			require.Empty(t, again.Conflicts)
		})
	}
}
