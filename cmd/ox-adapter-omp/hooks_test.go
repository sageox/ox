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

func TestOMPMarkerUsesUniqueNamespace(t *testing.T) {
	assert.Contains(t, ompPrimeMarkerStart, ":omp:")
	assert.NotEqual(t, "<!-- ox:prime:start -->", ompPrimeMarkerStart)
}

func TestInstallHooksUsesNativeOMPContext(t *testing.T) {
	repo := t.TempDir()
	rootAgents := "# Project rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(rootAgents), 0o644))

	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	assert.True(t, resp.Installed)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "ox agent prime --agent omp")
	assert.Contains(t, content, "@../AGENTS.md")
	assert.Less(t, strings.Index(content, "ox agent prime --agent omp"), strings.Index(content, "@../AGENTS.md"),
		"OMP identity must be established before imported generic prime instructions")

	unchanged, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, rootAgents, string(unchanged))
}

func TestInstallHooksPreservesExistingNativeContext(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp"), 0o755))
	native := "# Existing OMP rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".omp", "AGENTS.md"), []byte(native), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Root rules\n"), 0o644))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	content := string(data)
	assert.True(t, strings.HasPrefix(content, ompPrimeMarkerStart))
	assert.Contains(t, content, native)
	assert.NotContains(t, content, "@../AGENTS.md",
		"an existing native context file intentionally owns OMP discovery")
}

func TestInstallHooksIsIdempotent(t *testing.T) {
	repo := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleInstallHooks(params)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), ompPrimeMarkerStart))
}

func TestUninstallHooksRemovesOwnedScaffold(t *testing.T) {
	repo := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleUninstallHooks(params)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(repo, ".omp", "AGENTS.md"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(repo, ".omp"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestUninstallHooksPreservesUserContent(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp"), 0o755))
	native := "# Existing OMP rules\n"
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".omp", "AGENTS.md"), []byte(native), 0o644))
	params := adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	_, err = handleUninstallHooks(params)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repo, ".omp", "AGENTS.md"))
	require.NoError(t, err)
	assert.Equal(t, native, string(data))
}

func TestInstallHooksRejectsUserScope(t *testing.T) {
	resp, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: t.TempDir(), Scope: "user"})
	require.Error(t, err)
	assert.False(t, resp.Installed)
}

// TestInstallHooksRefreshesStaleBlock verifies that an existing .omp/AGENTS.md carrying the OLD
// imperative "BLOCKING … NOW" wording is regenerated in place on the next install — install
// previously no-op'd whenever the marker was present, so existing OMP users kept the old wording
// forever (#809). User content outside the markers and the file mode are preserved.
func TestInstallHooksRefreshesStaleBlock(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".omp"), 0o755))
	staleBlock := ompPrimeMarkerStart +
		"\n**BLOCKING**: Run `ox agent prime --agent omp` NOW before ANY other action. Do not run a later unqualified `ox agent prime` from imported context; this command satisfies it.\n\n" +
		"This loads SageOx Team Context and records this OMP session in the project Ledger.\n" +
		ompPrimeMarkerEnd
	userNote := "\n\n# my own omp notes\n"
	ompPath := filepath.Join(repo, ".omp", "AGENTS.md")
	require.NoError(t, os.WriteFile(ompPath, []byte(staleBlock+userNote), 0o600))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repo, Scope: "project"})
	require.NoError(t, err)

	content := readFile(t, ompPath)
	assert.NotContains(t, content, "**BLOCKING**", "stale imperative wording must be refreshed")
	assert.Contains(t, content, "at session start to load SageOx team context", "new wording must be present")
	assert.Contains(t, content, "# my own omp notes", "user content outside the markers must be preserved")
	assert.Equal(t, 1, strings.Count(content, ompPrimeMarkerStart), "no duplicate block")
	info, _ := os.Stat(ompPath)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "mode must not be broadened")
}

// TestRefreshOMPPrimeBlock verifies the pure refresh: no-op when current, preserves the
// @../AGENTS.md import decision while updating wording, and leaves an orphan marker untouched.
func TestRefreshOMPPrimeBlock(t *testing.T) {
	current := ompPrimeBlock(false)
	if out, changed := refreshOMPPrimeBlock(current); changed || out != current {
		t.Error("expected no change when block already current")
	}

	staleWithImport := ompPrimeMarkerStart + "\nOLD WORDING\n\n@../AGENTS.md\n" + ompPrimeMarkerEnd
	out, changed := refreshOMPPrimeBlock(staleWithImport)
	if !changed {
		t.Fatal("expected refresh of stale block")
	}
	if !strings.Contains(out, "@../AGENTS.md") {
		t.Error("the @../AGENTS.md import decision must be preserved")
	}
	if strings.Contains(out, "OLD WORDING") {
		t.Error("stale wording must be replaced")
	}

	orphan := ompPrimeMarkerStart + "\nsomething\n"
	if out, changed := refreshOMPPrimeBlock(orphan); changed || out != orphan {
		t.Error("orphan start marker (no end) must be left untouched")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
