package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUninstallAll_RemovesUsagePane verifies `ox integrate uninstall --all`
// takes the /ox-brief pane out of Claude Code along with every other
// integration.
// Failure prevented: a coworker removes ox's integrations and the pane keeps
// loading in every Claude Code session, from a folder ox no longer manages.
func TestUninstallAll_RemovesUsagePane(t *testing.T) {
	setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	env := setupUsagePaneEnv(t, true)
	_, _, err := runUsagePane(t, "enable")
	require.NoError(t, err)
	require.DirExists(t, env.dir)

	require.NoError(t, uninstallAllIntegrations(true))
	assert.NoDirExists(t, env.dir)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	assert.NoFileExists(t, filepath.Join(env.state, "marketplace"))
}

// TestUninstallAll_SaysWhatIsLeftOfThePaneWithoutClaude verifies bulk
// uninstall without the claude CLI removes the pane's files and prints the
// Claude Code commands that finish the job, instead of claiming success.
func TestUninstallAll_SaysWhatIsLeftOfThePaneWithoutClaude(t *testing.T) {
	setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	env := setupUsagePaneEnv(t, false)
	_, _, err := runUsagePane(t, "enable")
	require.NoError(t, err)
	require.DirExists(t, env.dir)

	out := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, uninstallAllIntegrations(true))
	})
	assert.NoDirExists(t, env.dir)
	assert.Contains(t, out, "Integrations uninstalled. To finish removing the brief pane")
	assert.Contains(t, out, "/plugin uninstall ox-cli-brief-pane@ox-cli-brief")
	assert.Contains(t, out, "/plugin marketplace remove ox-cli-brief")
	assert.NotContains(t, out, "All integrations uninstalled")
}
