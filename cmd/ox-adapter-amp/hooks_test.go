package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAmpMarkers_AreUniqueToAmp guards the #527 fix: Amp must not share
// marker strings with Pi (both write AGENTS.md). A shared marker causes
// the second adapter's install to silently no-op via the other's idempotency
// check, hiding bugs and confusing uninstall.
// Failure prevented: Pi+Amp co-install collapsing to one block or wrong ownership.
func TestAmpMarkers_AreUniqueToAmp(t *testing.T) {
	assert.NotEqual(t, "<!-- ox:prime:start -->", ampPrimeMarkerStart,
		"Amp must use a unique marker pair, not the generic legacy pair")
	assert.Contains(t, ampPrimeMarkerStart, "amp",
		"Amp's unique marker should name amp so cross-adapter collisions are visually obvious")
	// sanity: amp's current marker must also differ from pi's unique marker
	assert.NotEqual(t, "<!-- ox:prime:pi:start -->", ampPrimeMarkerStart,
		"Amp and Pi must not share a marker")
}

func TestAmpInstall_IdempotentForCurrentMarker(t *testing.T) {
	dir := t.TempDir()
	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)
	_, err = handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), ampPrimeMarkerStart))
}

// TestAmpInstall_IdempotentForLegacyMarker confirms a pre-#527 Amp install
// (generic markers) is recognized so a fresh install doesn't stack a second
// block alongside the legacy one.
func TestAmpInstall_IdempotentForLegacyMarker(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	seed := ampLegacyPrimeMarkerStart + "\nold block\n" + ampLegacyPrimeMarkerEnd + "\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(seed), 0644))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	data, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, ampLegacyPrimeMarkerStart)
	assert.NotContains(t, content, ampPrimeMarkerStart)
}

func TestAmpUninstall_RemovesCurrentAndLegacyBlocks(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	content := ampPrimeMarkerStart + "\ncurrent\n" + ampPrimeMarkerEnd + "\n\n" +
		ampLegacyPrimeMarkerStart + "\nlegacy\n" + ampLegacyPrimeMarkerEnd + "\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644))

	_, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	if data, readErr := os.ReadFile(agentsPath); readErr == nil {
		got := string(data)
		assert.NotContains(t, got, ampPrimeMarkerStart)
		assert.NotContains(t, got, ampLegacyPrimeMarkerStart)
	}
}

// TestAmpInstall_WritesUserBridgePlugin guards the new architecture:
// install-hooks writes the embedded plugin to ~/.config/amp/plugins/
// once per machine, regardless of which project triggered the install.
// Failure prevented: silent regression to the old "marker-only" install
// that leaves users with a broken adapter (Amp persists no transcripts
// on its own; the bridge is the only source of session data).
func TestAmpInstall_WritesUserBridgePlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)
	require.True(t, resp.Installed)

	bridge, err := userBridgePluginPath()
	require.NoError(t, err)
	data, err := os.ReadFile(bridge)
	require.NoError(t, err, "user-global bridge plugin must be written")
	assert.Equal(t, oxBridgePluginSrc, data)
	assert.Contains(t, resp.FilesWritten, bridge)
}

// TestAmpUninstall_PreservesUserBridgePlugin protects the multi-project
// invariant: removing the marker from project A must NOT delete the
// user-global plugin, because project B on the same machine may still
// rely on it. Failure prevented: cross-project breakage on uninstall.
func TestAmpUninstall_PreservesUserBridgePlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)
	bridge, err := userBridgePluginPath()
	require.NoError(t, err)
	require.FileExists(t, bridge)

	_, err = handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)
	assert.FileExists(t, bridge, "user-global bridge plugin must survive project uninstall")
}
