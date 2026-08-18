package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleInstallHooks_MigratesLegacyFeatureFlag(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte(`model = "gpt-5"

[features]
codex_hooks = true
hooks = false
other_feature = "preserve"

[profiles.work]
model = "gpt-5-codex"
`), 0o600))

	response, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	assert.True(t, response.Installed)
	assert.Equal(t, codexHookEvents, response.Hooks)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, toml.Unmarshal(data, &config))
	assert.Equal(t, "gpt-5", config["model"])
	features, ok := config["features"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, features, "codex_hooks")
	assert.Equal(t, false, features["hooks"])
	assert.Equal(t, "preserve", features["other_feature"])
	profiles, ok := config["profiles"].(map[string]any)
	require.True(t, ok)
	work, ok := profiles["work"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gpt-5-codex", work["model"])

	hooksPath := filepath.Join(repoRoot, codexProjectPath, codexHooksFileName)
	hooksMap, _, err := readHooksFile(hooksPath)
	require.NoError(t, err)
	for _, event := range codexHookEvents {
		assert.Truef(t, eventHasOxHook(hooksMap[event]), "%s is missing an ox hook", event)
	}
}

func TestHandleInstallHooks_PreservesTOMLFormattingDuringLegacyMigration(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	config := []byte(`# User-owned Codex configuration
model = 'gpt-5' # Preserve quote style and comments.

[features]
# This comment describes the old SageOx setting.
codex_hooks = true # Remove only this line.
hooks = false # Explicit opt-out must remain.

[profiles.work]
model = 'gpt-5-codex'
`)
	want := []byte(`# User-owned Codex configuration
model = 'gpt-5' # Preserve quote style and comments.

[features]
# This comment describes the old SageOx setting.
hooks = false # Explicit opt-out must remain.

[profiles.work]
model = 'gpt-5-codex'
`)
	require.NoError(t, os.WriteFile(configPath, config, 0o600))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestHandleInstallHooks_PreservesMultilineTOMLContentDuringLegacyMigration(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	config := []byte(`[features]
"notes=internal" = """
codex_hooks = false
codex_hooks = true
"""
codex_hooks = true
hooks = false
`)
	want := []byte(`[features]
"notes=internal" = """
codex_hooks = false
codex_hooks = true
"""
hooks = false
`)
	require.NoError(t, os.WriteFile(configPath, config, 0o600))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestHandleInstallHooks_DoesNotCreateConfigWithoutLegacyFlag(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	_, err = os.Stat(configPath)
	assert.True(t, os.IsNotExist(err), "install should not create config.toml")
}

func TestHandleInstallHooks_DoesNotModifyConfigWithoutLegacyFlag(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	config := []byte("# User-owned configuration\nmodel = \"gpt-5\"\n\n[features]\nhooks = false\n")
	require.NoError(t, os.WriteFile(configPath, config, 0o600))

	_, err := handleInstallHooks(adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, config, got)
}

func TestHandleInstallHooks_IsIdempotentAfterLegacyMigration(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("[features]\ncodex_hooks = true\nhooks = false\n"), 0o600))

	params := adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)
	firstConfig, err := os.ReadFile(configPath)
	require.NoError(t, err)
	firstHooks, err := os.ReadFile(filepath.Join(repoRoot, codexProjectPath, codexHooksFileName))
	require.NoError(t, err)

	_, err = handleInstallHooks(params)
	require.NoError(t, err)
	secondConfig, err := os.ReadFile(configPath)
	require.NoError(t, err)
	secondHooks, err := os.ReadFile(filepath.Join(repoRoot, codexProjectPath, codexHooksFileName))
	require.NoError(t, err)
	assert.Equal(t, firstConfig, secondConfig)
	assert.Equal(t, firstHooks, secondHooks)
}

func TestHandleUninstallHooks_LeavesCodexConfigUntouched(t *testing.T) {
	repoRoot := t.TempDir()
	params := adapterprotocol.HookParams{RepoRoot: repoRoot, Scope: "project"}
	_, err := handleInstallHooks(params)
	require.NoError(t, err)

	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	config := []byte("[features]\ncodex_hooks = true\nhooks = false\n")
	require.NoError(t, os.WriteFile(configPath, config, 0o600))

	_, err = handleUninstallHooks(params)
	require.NoError(t, err)
	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, config, got)
}

func TestHandleDiagnose_ReportsDeprecatedHookFeature(t *testing.T) {
	repoRoot := t.TempDir()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex", "sessions"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	configPath := filepath.Join(repoRoot, codexProjectPath, codexConfigFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o755))
	require.NoError(t, os.WriteFile(configPath, []byte("[features]\ncodex_hooks = true\n"), 0o600))

	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	for _, issue := range result.Issues {
		if issue.Slug == "codex:legacy-hooks-feature" {
			assert.Equal(t, []string{"ox", "integrate", "install", "--codex"}, issue.FixArgv)
			assert.True(t, issue.FixSafe)
			return
		}
	}
	t.Fatal("deprecated Codex hook feature was not reported")
}

func TestHandleDiagnose_CodexAbsentSkipsHookDiagnostics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())

	repoRoot := t.TempDir()
	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	require.Len(t, result.Issues, 1)
	assert.Equal(t, "codex:not-installed", result.Issues[0].Slug)
	assert.NotContains(t, result.Issues[0].Detail, "hooks")
	assert.NoFileExists(t, filepath.Join(repoRoot, codexProjectPath, codexHooksFileName))
}

// fakeCodexOnPath puts an executable named `codex` on PATH and points HOME at
// an empty dir, so detection succeeds via the CLI alone — the state every user
// with Codex installed is in, for every repo on their machine.
func fakeCodexOnPath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binaries require unix")
	}
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", binDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// TestHandleDiagnose_CodexOnPathWithoutProjectConfig verifies that a Codex CLI
// merely installed on the machine produces no hook diagnostics for a repo that
// never opted into Codex.
//
// Failure prevented: `ox doctor` warns about Codex in every repo on the
// machine, and `ox doctor --fix` creates .codex/hooks.json in repos that don't
// use Codex — contradicting doctor's core check, which stays silent for
// "CLI detected, no project config".
func TestHandleDiagnose_CodexOnPathWithoutProjectConfig(t *testing.T) {
	fakeCodexOnPath(t)

	repoRoot := t.TempDir()
	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	assert.True(t, result.OK)
	assert.Empty(t, result.Issues)
	assert.NoDirExists(t, filepath.Join(repoRoot, codexProjectPath))
}

// TestHandleDiagnose_CodexProjectMissingHooks is the positive control for the
// test above: once a repo opts into Codex, missing hooks ARE reported. Without
// it, the silence assertion would pass even if hook diagnostics were deleted.
func TestHandleDiagnose_CodexProjectMissingHooks(t *testing.T) {
	fakeCodexOnPath(t)

	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoRoot, codexProjectPath), 0o755))

	result, err := handleDiagnose(adapterprotocol.DiagnoseParams{RepoRoot: repoRoot, Scope: "project"})
	require.NoError(t, err)
	require.Len(t, result.Issues, 1)
	assert.Equal(t, "codex:hooks-missing", result.Issues[0].Slug)
	assert.True(t, result.Issues[0].FixSafe)
}
