//go:build !short

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox doctor --fix-slug=<one slug>` fixes exactly that slug. Failure prevented:
// repairing one Ledger issue also rewrote every hook in .claude/settings.json
// and appended the prime block to AGENTS.md, leaving tracked project files
// dirty for edits nobody asked for (GH #1209).
func TestDoctorFixSlug_LeavesUnrelatedProjectFilesUntouched(t *testing.T) {
	sandboxDoctorEnv(t)
	project := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	require.NoError(t, os.Chdir(project))
	createFreshSageoxStructure(t, project)

	// stale hooks and an AGENTS.md with no prime block: both are auto-fixed by a bare `ox doctor --fix`
	settings := filepath.Join(project, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings, []byte("{\"hooks\":{\"SessionStart\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"echo stale\"}]}]}}\n"), 0o644))
	agents := filepath.Join(project, "AGENTS.md")
	require.NoError(t, os.WriteFile(agents, []byte("# Project notes\n\nNothing about ox here.\n"), 0o644))
	settingsBefore, err := os.ReadFile(settings)
	require.NoError(t, err)
	agentsBefore, err := os.ReadFile(agents)
	require.NoError(t, err)

	_, err = runDoctorChecks(context.Background(), doctorOptions{
		fix:      true,
		fixSlugs: []string{CheckSlugAuthPermissions},
		forceYes: true,
	})
	require.NoError(t, err)

	settingsAfter, err := os.ReadFile(settings)
	require.NoError(t, err)
	agentsAfter, err := os.ReadFile(agents)
	require.NoError(t, err)
	assert.Equal(t, string(settingsBefore), string(settingsAfter), ".claude/settings.json was rewritten by an unrelated --fix-slug")
	assert.Equal(t, string(agentsBefore), string(agentsAfter), "AGENTS.md was rewritten by an unrelated --fix-slug")
}

// The negative control: a plain --fix still repairs them, so the test above
// cannot pass merely because nothing in the fixture is fixable.
func TestDoctorFix_StillRepairsHooksAndAgentsFile(t *testing.T) {
	sandboxDoctorEnv(t)
	project := testGitRepo(t)
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	require.NoError(t, os.Chdir(project))
	createFreshSageoxStructure(t, project)
	settings := filepath.Join(project, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	stale := "{\"hooks\":{\"SessionStart\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"echo stale\"}]}]}}\n"
	require.NoError(t, os.WriteFile(settings, []byte(stale), 0o644))
	agents := filepath.Join(project, "AGENTS.md")
	require.NoError(t, os.WriteFile(agents, []byte("# Project notes\n"), 0o644))

	_, err := runDoctorChecks(context.Background(), doctorOptions{fix: true, forceYes: true})
	require.NoError(t, err)

	settingsAfter, err := os.ReadFile(settings)
	require.NoError(t, err)
	agentsAfter, err := os.ReadFile(agents)
	require.NoError(t, err)
	changed := string(settingsAfter) != stale || string(agentsAfter) != "# Project notes\n"
	assert.True(t, changed, "a plain --fix must still repair hooks or the prime block")
}
