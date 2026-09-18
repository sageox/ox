package daemon

import (
	"testing"
)

// TestTeamSkillsTouched is the whole trigger for the daemon's team-skill
// refresh, so it is worth pinning against the shapes `git diff --name-only`
// actually emits.
//
// The legacy coworkers/skills root is in here deliberately: discovery walks both
// roots, and a predicate that knew only the canonical one would leave a skill
// authored in the legacy location discovered-but-never-refreshed — present in the
// team repo, absent from every repository, and silent in both directions.
func TestTeamSkillsTouched(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		changed []string
		want    bool
	}{
		{name: "nothing changed", changed: nil},
		{
			name:    "canonical root",
			changed: []string{"agents/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "legacy root",
			changed: []string{"coworkers/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "a non-manifest file inside a skill still counts",
			changed: []string{"agents/skills/deploy/references/runbook.md"},
			want:    true,
		},
		{
			name:    "mixed diff finds the skill among unrelated files",
			changed: []string{"MEMORY.md", "docs/principles.md", "agents/skills/deploy/SKILL.md"},
			want:    true,
		},
		{
			name:    "team rules are not team skills",
			changed: []string{"agents/rules/postgres.md"},
		},
		{
			// A prefix match without the separator would fire on this, and on any
			// future sibling directory whose name merely starts the same way.
			name:    "a sibling directory sharing the prefix does not count",
			changed: []string{"agents/skills-archive/deploy/SKILL.md"},
		},
		{
			// The roots are relative to the checkout; a path that merely contains
			// one deeper down belongs to some other tree.
			name:    "a nested lookalike path does not count",
			changed: []string{"documents/agents/skills/deploy/SKILL.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := teamSkillsTouched(tt.changed); got != tt.want {
				t.Fatalf("teamSkillsTouched(%v) = %v, want %v", tt.changed, got, tt.want)
			}
		})
	}
}
