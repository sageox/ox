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

// legacyPrimeMarkerStart / End and legacyInProcessMarkerStart / End pin the
// pre-#527 marker bytes literally, rather than referencing
// piLegacyPrimeMarkerStart/piLegacyInProcessMarkerStart from hooks.go. A test
// that builds its "legacy" fixture from the same constant the implementation
// uses can never detect a change to that constant — it moves with the code
// instead of pinning the historical value it exists to guard.
const (
	legacyPrimeMarkerStart     = "<!-- ox:prime:start -->"
	legacyPrimeMarkerEnd       = "<!-- ox:prime:end -->"
	legacyInProcessMarkerStart = "<!-- ox:pi-prime:start -->"
	legacyInProcessMarkerEnd   = "<!-- ox:pi-prime:end -->"
)

// --- Marker uniqueness ---

// TestPiMarkers_AreUniqueToPi guards the #527 fix: the current Pi marker
// must not be the generic <!-- ox:prime:* --> pair that used to collide
// with Amp in shared AGENTS.md.
// Failure prevented: reintroducing a generic marker that silently no-ops
// a second adapter's install via shared-marker idempotency.
func TestPiMarkers_AreUniqueToPi(t *testing.T) {
	assert.NotEqual(t, "<!-- ox:prime:start -->", piPrimeMarkerStart,
		"Pi must use a unique marker pair, not the generic legacy pair")
	assert.Contains(t, piPrimeMarkerStart, "pi",
		"Pi's unique marker should name pi so cross-adapter collisions are visually obvious")
}

// --- Install idempotency across marker generations ---

// TestInstall_IdempotentForCurrentMarker confirms a second install no-ops
// when the current marker pair is already present.
// Failure prevented: duplicate Pi blocks appearing in AGENTS.md across repeated installs.
func TestInstall_IdempotentForCurrentMarker(t *testing.T) {
	dir := t.TempDir()

	// first install
	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	// second install — must be a no-op
	_, err = handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	require.NoError(t, err)
	content := string(data)
	assert.Equal(t, 1, strings.Count(content, piPrimeMarkerStart),
		"current Pi start marker must appear exactly once after two installs")
}

// TestInstall_IdempotentForLegacyInProcessMarker confirms a pre-#527
// installation that only the in-process hooks_pi.go ever emitted
// (<!-- ox:pi-prime:start -->) is recognized so a fresh install does
// not add a duplicate current-marker block alongside it.
// Failure prevented: repos with legacy markers get two blocks after upgrade.
func TestInstall_IdempotentForLegacyGenericMarker(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	// seed with the pre-#527 generic marker pair that the external
	// adapter used to emit
	seed := legacyPrimeMarkerStart + "\nold block\n" + legacyPrimeMarkerEnd + "\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(seed), 0644))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	data, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	content := string(data)
	// full-file equality, not just Contains: a legacy block already present
	// must make install a complete no-op, byte for byte.
	assert.Equal(t, seed, content,
		"installing over an existing legacy block must not modify the file at all")
	assert.NotContains(t, content, piPrimeMarkerStart,
		"legacy presence must prevent adding a second (current-marker) block")
}

// --- Uninstall sweeps all generations ---

// TestUninstall_RemovesCurrentAndLegacyBlocks confirms a single uninstall
// cleans up every recognized marker pair, even ones that pre-date the #527 fix.
// Failure prevented: orphan legacy blocks surviving uninstall and corrupting
// subsequent installs.
func TestUninstall_RemovesCurrentAndLegacyBlocks(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	// seed with both a current and a legacy block adjacent — simulating
	// a repo that was installed pre-fix and then again post-fix
	content := piPrimeMarkerStart + "\ncurrent\n" + piPrimeMarkerEnd + "\n\n" +
		legacyPrimeMarkerStart + "\nlegacy\n" + legacyPrimeMarkerEnd + "\n"
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644))

	_, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"})
	require.NoError(t, err)

	// the seed file was entirely our two blocks with nothing else, so
	// removing both must leave nothing behind — assert the file is gone
	// rather than only conditionally checking its content if it happens
	// to still exist.
	_, readErr := os.ReadFile(agentsPath)
	require.Error(t, readErr, "AGENTS.md should have been removed — it contained only our blocks")
	assert.True(t, os.IsNotExist(readErr), "expected a not-exist error, got %v", readErr)
}

// TestInstall_IdempotentForLegacyInProcessMarker covers the marker that only
// cmd/ox/hooks_pi.go ever wrote. The adapter did not recognize it, so `ox init`
// on a repo previously set up with `ox integrate install --pi` appended a
// second block.
// Failure prevented: duplicate SageOx blocks stacking in AGENTS.md on upgrade.
func TestInstall_IdempotentForLegacyInProcessMarker(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	legacy := legacyInProcessMarkerStart + "\n## SageOx Team Context\n\nold body\n" + legacyInProcessMarkerEnd + "\n"
	if err := os.WriteFile(agentsPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	// full-file equality: a legacy in-process block already present must
	// make install a complete no-op, byte for byte — not merely "the new
	// marker is absent somewhere in a longer file."
	if content != legacy {
		t.Errorf("install over an existing legacy in-process block must not modify the file at all:\ngot:  %q\nwant: %q", content, legacy)
	}
}

// TestUninstall_RemovesLegacyInProcessBlock verifies the orphan is cleaned up.
// Failure prevented: uninstall leaving a stale SageOx block in AGENTS.md.
func TestUninstall_RemovesLegacyInProcessBlock(t *testing.T) {
	dir := t.TempDir()
	agentsPath := filepath.Join(dir, "AGENTS.md")

	content := "# Project\n\n" +
		legacyInProcessMarkerStart + "\nold body\n" + legacyInProcessMarkerEnd + "\n"
	if err := os.WriteFile(agentsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := handleUninstallHooks(adapterprotocol.HookParams{RepoRoot: dir, Scope: "project"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md should survive — it had non-ox content: %v", err)
	}
	// full-file equality: only the legacy block should be gone, and nothing
	// else about the surrounding content should shift.
	want := "# Project\n"
	if string(data) != want {
		t.Errorf("uninstall left unexpected content:\ngot:  %q\nwant: %q", data, want)
	}
}

// TestPiPrimeBlock_MatchesInProcessInstaller pins the two installers together.
// Failure prevented: divergent block text making the idempotency check miss.
func TestPiPrimeBlock_MatchesInProcessInstaller(t *testing.T) {
	// mirrors cmd/ox/hooks_pi.go:piPrimeBlock — if you change one, change both
	want := piPrimeMarkerStart + "\n" +
		"## SageOx Team Context\n" +
		"\n" +
		"This project uses [SageOx](https://sageox.ai) for team context. Run the following command at the start of every session to load team knowledge:\n" +
		"\n" +
		"```bash\n" +
		"ox agent prime\n" +
		"```\n" +
		"\n" +
		"This provides architectural decisions, coding conventions, and session history from your team.\n" +
		piPrimeMarkerEnd

	if piPrimeBlock != want {
		t.Errorf("piPrimeBlock drifted from the in-process installer:\ngot:  %q\nwant: %q", piPrimeBlock, want)
	}
	if strings.Contains(piPrimeBlock, "@sageox/pi-ox") {
		t.Error("block advertises @sageox/pi-ox, which is not a published package")
	}
}
