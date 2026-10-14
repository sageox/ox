package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain points the home directory at a throwaway sandbox for the whole
// package. handleInstallHooks writes the bridge plugin user-globally, so a test
// that forgets t.Setenv("HOME", ...) would otherwise overwrite the developer's
// real ox-bridge.ts in Amp's plugin dir with whatever branch is under test.
// Failure prevented: `make test` silently replacing a working Amp install.
func TestMain(m *testing.M) {
	sandbox, err := os.MkdirTemp("", "ox-adapter-amp-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ox-adapter-amp tests: cannot create home sandbox: %v\n", err)
		os.Exit(1)
	}
	// USERPROFILE is what os.UserHomeDir reads on Windows.
	_ = os.Setenv("HOME", sandbox)
	_ = os.Setenv("USERPROFILE", sandbox)
	// The plugin follows XDG_CONFIG_HOME ahead of HOME. Unset rather than point
	// into the sandbox, so each test's t.Setenv("HOME", ...) still gives it a
	// plugin dir of its own; tests that need XDG_CONFIG_HOME set it themselves.
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	// Fail fast, before any test runs, if the redirect did not take on this
	// platform: the bridge plugin must resolve inside the sandbox.
	bridge, err := userBridgePluginPath()
	if err != nil || !strings.HasPrefix(bridge, sandbox+string(filepath.Separator)) {
		fmt.Fprintf(os.Stderr,
			"ox-adapter-amp tests: bridge plugin resolves to %q (err=%v), outside home sandbox %s; refusing to run\n",
			bridge, err, sandbox)
		_ = os.RemoveAll(sandbox)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(sandbox)
	os.Exit(code)
}

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
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())
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
