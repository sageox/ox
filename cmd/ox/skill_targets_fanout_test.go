package main

import (
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

// TestSelectingManyAgentsStillYieldsOneRootEach is the no-fan-out guarantee,
// asserted at the layer that composes adapters rather than inside one of them.
//
// Six adapters now declare a skills root. Five of them name the same canonical
// `.agents/skills`, and Claude Code names `.claude/skills` because it is the one
// agent that does not read the canonical root. A coworker who selects all six
// must end up with TWO directories, not six — a skill copied into six places is
// six files to keep in sync and six answers when they drift.
func TestSelectingManyAgentsStillYieldsOneRootEach(t *testing.T) {
	canonical := adapterprotocol.SkillTarget{
		Key: "agents-project", Root: ".agents/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}
	claude := adapterprotocol.SkillTarget{
		Key: "claude-project", Root: ".claude/skills",
		Format: adapterprotocol.SkillFormatAgentSkillsV1, Scope: adapterprotocol.SkillScopeProject,
		LinkPolicy: adapterprotocol.SkillLinkPolicyReject,
	}

	// codex, gemini, omp, amp, droid, goose, opencode, pi all declare canonical.
	declared := []adapterprotocol.SkillTarget{
		canonical, canonical, canonical, canonical,
		canonical, canonical, canonical, canonical,
		claude,
	}

	got, err := skillmanager.CanonicalizeTargets(t.TempDir(), declared)
	require.NoError(t, err)

	roots := make([]string, 0, len(got))
	for _, target := range got {
		roots = append(roots, target.Root)
	}
	require.ElementsMatch(t, []string{".agents/skills", ".claude/skills"}, roots,
		"selecting nine agents produced %d skill roots; a skill would be written into each of them", len(roots))
}
