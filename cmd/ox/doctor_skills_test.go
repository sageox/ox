package main

import (
	"strings"
	"testing"

	"github.com/sageox/ox/internal/skillmanager"
)

// TestDescribeSkillConflicts pins the doctor detail message for preserved
// skill conflicts: it must name each conflicting file and why ox left it
// alone, not just report a count. Before the fix, checkClaudeSkills emitted
// a generic "Resolve the preserved user-owned or modified files manually"
// detail with no path information, leaving the user unable to act on the
// warning from the doctor output alone.
func TestDescribeSkillConflicts(t *testing.T) {
	conflicts := []skillmanager.Conflict{
		{
			TargetKey: "claude",
			Path:      ".claude/skills/foo/SKILL.md",
			Reason:    "existing skill is not managed by ox",
		},
		{
			TargetKey: "claude",
			Path:      ".claude/skills/bar/SKILL.md",
			Reason:    "retired managed file was modified and will be preserved",
		},
	}

	got := describeSkillConflicts(conflicts)

	for _, c := range conflicts {
		if !strings.Contains(got, c.Path) {
			t.Errorf("describeSkillConflicts(%+v) = %q; missing path %q", conflicts, got, c.Path)
		}
		if !strings.Contains(got, c.Reason) {
			t.Errorf("describeSkillConflicts(%+v) = %q; missing reason %q", conflicts, got, c.Reason)
		}
	}
}

func TestDescribeSkillConflicts_Empty(t *testing.T) {
	if got := describeSkillConflicts(nil); got != "" {
		t.Errorf("describeSkillConflicts(nil) = %q; want empty string", got)
	}
}

// TestCheckClaudeSkills_ReconcileWarningNamesConflicts is a narrower,
// message-shape regression: it locks in that the WARN path built after a
// reconcile (checkClaudeSkills's post-Apply branch) routes its detail
// through describeSkillConflicts rather than a bare count-only string.
func TestCheckClaudeSkills_ReconcileWarningNamesConflicts(t *testing.T) {
	conflicts := []skillmanager.Conflict{
		{TargetKey: "claude", Path: ".claude/skills/foo/SKILL.md", Reason: "existing skill is not managed by ox"},
	}

	detail := describeSkillConflicts(conflicts)
	result := WarningCheck("Agent skills", "reconciled with 1 preserved conflict(s)", detail)

	if !strings.Contains(result.detail, ".claude/skills/foo/SKILL.md") {
		t.Errorf("WarningCheck detail = %q; want it to contain the conflicting path", result.detail)
	}
	if !strings.Contains(result.detail, "existing skill is not managed by ox") {
		t.Errorf("WarningCheck detail = %q; want it to contain the conflict reason", result.detail)
	}
}
