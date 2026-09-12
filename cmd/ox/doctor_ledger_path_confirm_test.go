package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerPathFixture puts the process inside an initialized git repo with
// isolated XDG dirs, so ledger.DefaultPath() resolves somewhere disposable.
func ledgerPathFixture(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	initGitRepo(t, repoRoot)
	t.Chdir(repoRoot)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	// ledger.DefaultPath() resolves through the project's repo_id, so the repo
	// has to look initialized.
	require.NoError(t, config.SaveProjectConfig(repoRoot, &config.ProjectConfig{
		RepoID:      "repo_01jfk3mabtestledgerpath",
		ProjectID:   "test_project",
		WorkspaceID: "test_workspace",
	}))
	return repoRoot
}

// makeLedgerAt makes path look like a real ledger to ledger.Exists.
func makeLedgerAt(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	initGitRepo(t, path)
}

// `ox doctor --yes` promised to answer every prompt but reached one of nine.
// These cover the ledger-path repairs, which write config.local.toml.
//
// Note these prompts intentionally keep ConfirmYesNo (not the Required
// variant): every one is behind `--fix`, which is itself the user's consent to
// repair, and doctor's documented posture is auto-fix by default.
func TestCheckLedgerPathMismatch_HonorsAssumeYes(t *testing.T) {
	t.Run("global yes adds a discovered ledger to config", func(t *testing.T) {
		repoRoot := ledgerPathFixture(t)

		// no ledger entry in config, but one exists at the default path
		localCfg, err := config.LoadLocalConfig(repoRoot)
		require.NoError(t, err)
		require.True(t, localCfg.Ledger == nil || localCfg.Ledger.Path == "",
			"fixture must start with no configured ledger")

		defaultPath := defaultLedgerPathForTest(t)
		makeLedgerAt(t, defaultPath)

		cli.SetAssumeYes(true)
		t.Cleanup(func() { cli.SetAssumeYes(false) })

		var res checkResult
		silenceStdout(t, func() {
			withStdin(t, "", func() { res = checkLedgerPathMismatch(true) })
		})

		assert.False(t, res.warning, "--yes must apply the repair, not warn: %+v", res)
		assert.True(t, res.passed, "%+v", res)

		saved, err := config.LoadLocalConfig(repoRoot)
		require.NoError(t, err)
		require.NotNil(t, saved.Ledger)
		assert.Equal(t, defaultPath, saved.Ledger.Path)
	})

	t.Run("without an answer the repair is declined, not applied", func(t *testing.T) {
		repoRoot := ledgerPathFixture(t)
		defaultPath := defaultLedgerPathForTest(t)
		makeLedgerAt(t, defaultPath)

		var res checkResult
		silenceStdout(t, func() {
			withStdin(t, "n\n", func() { res = checkLedgerPathMismatch(true) })
		})

		// WarningCheck is a non-blocking pass in this codebase, so the
		// load-bearing assertion is the config below, not the status flag.
		assert.True(t, res.warning, "declining must surface a warning: %+v", res)

		saved, err := config.LoadLocalConfig(repoRoot)
		require.NoError(t, err)
		assert.True(t, saved.Ledger == nil || saved.Ledger.Path == "",
			"declining must not rewrite config")
	})

	t.Run("without --fix nothing prompts and nothing is written", func(t *testing.T) {
		repoRoot := ledgerPathFixture(t)
		makeLedgerAt(t, defaultLedgerPathForTest(t))

		var res checkResult
		silenceStdout(t, func() {
			withStdin(t, "", func() { res = checkLedgerPathMismatch(false) })
		})

		saved, err := config.LoadLocalConfig(repoRoot)
		require.NoError(t, err)
		assert.True(t, saved.Ledger == nil || saved.Ledger.Path == "",
			"a plain `ox doctor` must never mutate config")
		assert.True(t, res.warning, "a plain `ox doctor` must report the mismatch: %+v", res)
	})
}

// defaultLedgerPathForTest resolves the same default path the check computes.
func defaultLedgerPathForTest(t *testing.T) string {
	t.Helper()
	p, err := ledger.DefaultPath()
	require.NoError(t, err)
	return p
}
