package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/internal/skillmanager"
	"github.com/stretchr/testify/require"
)

// TestCheckTeamSuffixShadow_ReportsOnlySkillsOxDoesNotOwn pins the one judgment
// this check has to make.
//
// Ox's own Team Skill projections end in "-team" too — that is the entire point
// of the namespace — so a check that reported every "-team" directory would fire
// on every repository with a Team Context and be turned off within a day. Only a
// directory ox cannot prove it wrote is somebody's work going missing.
func TestCheckTeamSuffixShadow_ReportsOnlySkillsOxDoesNotOwn(t *testing.T) {
	repo, _ := stageTeamPublishRepo(t)
	root := ".claude/skills"

	write := func(name string, manifest []byte) {
		t.Helper()
		dir := filepath.Join(repo, filepath.FromSlash(root), name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), manifest, 0o644))
	}

	plain := []byte("---\nname: x\ndescription: d\n---\n\nbody\n")
	write("fork-scout"+skillmanager.TeamSuffix, skillmanager.TeamSkillStamp.Apply(plain))
	write("notify"+skillmanager.TeamSuffix, plain)
	write("my-own-skill", plain)

	result := checkTeamSuffixShadowIn(repo, false)

	require.True(t, result.warning, "a hidden hand-authored skill was not reported: %+v", result)
	require.Contains(t, result.message, "notify"+skillmanager.TeamSuffix)
	require.NotContains(t, result.message, "fork-scout",
		"ox's own projection is correctly hidden and must not be reported as a collision")
	require.NotContains(t, result.message, "my-own-skill",
		"a skill outside the namespace is not hidden and must not be reported")
	require.Contains(t, strings.ToLower(result.detail), "rename")
}

// TestCheckTeamSuffixShadow_PassesWhenNothingIsHidden keeps the check quiet in
// the overwhelmingly common case. A diagnostic that warns on a healthy
// repository is a diagnostic people learn to scroll past.
func TestCheckTeamSuffixShadow_PassesWhenNothingIsHidden(t *testing.T) {
	repo, _ := stageTeamPublishRepo(t)
	dir := filepath.Join(repo, ".claude", "skills", "my-own-skill")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: my-own-skill\ndescription: d\n---\n\nbody\n"), 0o644))

	result := checkTeamSuffixShadowIn(repo, false)
	require.True(t, result.passed, result.message)
}

// TestCheckTeamSuffixShadow_StopsWarningOnceTheSkillIsTracked is the case that
// makes this check ask git instead of inferring from the name.
//
// The check's own remedy is `git add -f <path>`. A name-based check keeps warning
// after the author follows it — the file is tracked, the problem is gone, and the
// diagnostic still fires forever. A checker that cannot be satisfied is a checker
// people learn to scroll past, which costs far more than the one warning it saves.
func TestCheckTeamSuffixShadow_StopsWarningOnceTheSkillIsTracked(t *testing.T) {
	repo, _ := stageTeamPublishRepo(t)
	rel := ".claude/skills/notify" + skillmanager.TeamSuffix + "/" + skills.SkillFileName
	path := filepath.Join(repo, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path,
		[]byte("---\nname: notify-team\ndescription: pings on-call\n---\n\nbody\n"), 0o644))

	// Untracked and matching the namespace glob: this is the state worth warning about.
	require.True(t, checkTeamSuffixShadowIn(repo, false).warning,
		"an untracked skill hidden by the namespace should be reported")

	// The author takes the advice.
	gitOutput(t, repo, "add", "-f", "--", rel)

	result := checkTeamSuffixShadowIn(repo, false)
	require.True(t, result.passed,
		"the warning survived the fix it recommended: %s", result.message)
}
