package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/version"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// TestReconcile_ProjectsEveryDefaultSkillIntoBothRoots drives the real skill
// reconcile in a scratch git repo with both native targets selected, then
// asserts every default-catalog skill lands under `.claude/skills` (Claude Code)
// AND `.agents/skills` (Codex, Gemini, OMP) with byte-identical bodies.
//
// Failure prevented: a skill added to extensions/skills (most recently
// ox-cli-usage) that is missing from the catalog, or that one root receives and
// the other does not, so `/ox-cli-usage` works in Claude Code but never appears
// in Codex's skill picker — or the two copies drift and answer differently.
func TestReconcile_ProjectsEveryDefaultSkillIntoBothRoots(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo)

	target := func(key, root string) adapterprotocol.SkillTarget {
		return adapterprotocol.SkillTarget{
			Key: key, Root: root,
			Format:     adapterprotocol.SkillFormatAgentSkillsV1,
			Scope:      adapterprotocol.SkillScopeProject,
			LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
		}
	}
	_, err := reconcileSelectedSkills(repo, []adapterprotocol.SkillTarget{
		target("claude-project", ".claude/skills"),
		target("agents-project", ".agents/skills"),
	})
	require.NoError(t, err)

	defaults, err := skills.Selected(version.Version, nil)
	require.NoError(t, err)

	names := make([]string, 0, len(defaults))
	for _, s := range defaults {
		names = append(names, s.Name)
	}
	// Pinned explicitly: dropping it from the catalog would otherwise just
	// shrink the table and pass.
	require.Contains(t, names, "ox-cli-usage", "ox-cli-usage is not in the default catalog")

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			claude, err := os.ReadFile(filepath.Join(repo, ".claude", "skills", name, skills.SkillFileName))
			require.NoError(t, err, "%s missing from .claude/skills", name)
			agents, err := os.ReadFile(filepath.Join(repo, ".agents", "skills", name, skills.SkillFileName))
			require.NoError(t, err, "%s missing from .agents/skills", name)
			require.Equal(t, string(claude), string(agents),
				"%s: the Claude Code and shared .agents projections diverged", name)
		})
	}
}
