package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/internal/pet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUninstallAll_RemovesOxy verifies `ox integrate uninstall --all` takes
// Oxy out of Claude Code along with every other integration.
// Failure prevented: a coworker removes ox's integrations and Oxy keeps
// loading in every Claude Code session, from a folder ox no longer manages.
func TestUninstallAll_RemovesOxy(t *testing.T) {
	setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	env := setupPetEnv(t, true)
	_, _, err := runPet(t, "enable")
	require.NoError(t, err)
	require.DirExists(t, env.dir)

	require.NoError(t, uninstallAllIntegrations(true))
	assert.NoDirExists(t, env.dir)
	assert.NoFileExists(t, filepath.Join(env.state, "installed"))
	assert.NoFileExists(t, filepath.Join(env.state, "marketplace"))
}

// TestUninstallAll_SaysWhatIsLeftWithoutClaude verifies bulk uninstall on a
// machine without the claude CLI removes Oxy's files and prints the two
// Claude Code commands that finish the job, instead of claiming success.
// Failure prevented: "All integrations uninstalled" while Claude Code still
// lists the plugin and its marketplace.
func TestUninstallAll_SaysWhatIsLeftWithoutClaude(t *testing.T) {
	setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	env := setupPetEnv(t, false)
	_, _, err := runPet(t, "enable", petHostClaude)
	require.NoError(t, err)
	require.DirExists(t, env.dir)

	out := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, uninstallAllIntegrations(true))
	})
	assert.NoDirExists(t, env.dir)
	assert.Contains(t, out, "/plugin uninstall ox-cli-oxy@ox-cli-pets")
	assert.Contains(t, out, "/plugin marketplace remove ox-cli-pets")
	assert.NotContains(t, out, "All integrations uninstalled")
}

// TestPetPresent_OnlyMissingMeansAbsent verifies a pet folder ox cannot
// inspect is an error, not "not installed".
// Failure prevented: bulk uninstall reports success, or "No integrations
// found", while Oxy stays installed behind an unreadable path.
func TestPetPresent_OnlyMissingMeansAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file used as a directory fails differently on Windows")
	}
	root := t.TempDir()
	present, err := petPresent(filepath.Join(root, "missing"))
	require.NoError(t, err)
	assert.False(t, present)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "there"), 0o755))
	present, err = petPresent(filepath.Join(root, "there"))
	require.NoError(t, err)
	assert.True(t, present)

	// a regular file where a directory component should be: stat fails with
	// ENOTDIR, which must not read as absent
	blocker := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err = petPresent(filepath.Join(blocker, "oxy"))
	assert.Error(t, err)
}

// TestUninstallAll_RemovesOxyFromCodex verifies `ox integrate uninstall --all`
// lists Oxy's Codex pet, removes his folder, and stops Codex selecting him.
// Failure prevented: after removing every ox integration, Codex still draws
// Oxy from a folder ox no longer manages, or points tui.pet at a deleted pet.
func TestUninstallAll_RemovesOxyFromCodex(t *testing.T) {
	setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	env := setupPetEnv(t, false)
	env.writeCodexConfig(t, "model = \"gpt-5\"\n")
	_, _, err := runPet(t, "enable", petHostCodex)
	require.NoError(t, err)
	require.DirExists(t, pet.CodexDir(env.codexHome))
	require.Equal(t, pet.CodexPetSelector, env.codexPet(t))

	out := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, uninstallAllIntegrations(true))
	})
	assert.Contains(t, out, "Oxy pet (Codex, user)")
	assert.NotContains(t, out, "Oxy pet (Claude Code, user)")
	assert.NoDirExists(t, pet.CodexDir(env.codexHome))
	assert.Empty(t, env.codexPet(t))
	cfg, err := os.ReadFile(filepath.Join(env.codexHome, "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), `model = "gpt-5"`)
}

// TestCodexPetPresent_OnlyMissingMeansAbsent mirrors petPresent for Codex,
// and counts a complete copy an interrupted refresh left beside the folder.
func TestCodexPetPresent_OnlyMissingMeansAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file used as a directory fails differently on Windows")
	}
	root := t.TempDir()
	present, err := codexPetPresent(filepath.Join(root, "missing"))
	require.NoError(t, err)
	assert.False(t, present)

	home := t.TempDir()
	writeCodexPet("1.0.0")(t, home)
	present, err = codexPetPresent(pet.CodexDir(home))
	require.NoError(t, err)
	assert.True(t, present)

	require.NoError(t, os.Rename(pet.CodexDir(home), pet.CodexDir(home)+".old"))
	present, err = codexPetPresent(pet.CodexDir(home))
	require.NoError(t, err)
	assert.True(t, present, "an interrupted refresh is still an enabled pet")

	blocker := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))
	_, err = codexPetPresent(filepath.Join(blocker, "oxy"))
	assert.Error(t, err)
}
