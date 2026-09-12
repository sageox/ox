package main

import (
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `ox doctor --yes` promises to "answer yes to all prompts" but historically
// reached exactly one of its prompts. These gates pin that promise for the
// prompts it now covers, and — just as importantly — pin the one prompt it must
// NOT cover.

// TestPromptOfflineMigration_HonorsAssumeYes covers the registration prompt,
// which is irreversible in the CLI once answered.
func TestPromptOfflineMigration_HonorsAssumeYes(t *testing.T) {
	t.Run("global yes registers without reading stdin", func(t *testing.T) {
		cli.SetAssumeYes(true)
		t.Cleanup(func() { cli.SetAssumeYes(false) })

		var got bool
		silenceStdout(t, func() {
			withStdin(t, "", func() { got = promptOfflineMigration(false) })
		})
		assert.True(t, got)
	})

	t.Run("force short-circuits before any output", func(t *testing.T) {
		var got bool
		silenceStdout(t, func() {
			withStdin(t, "", func() { got = promptOfflineMigration(true) })
		})
		assert.True(t, got)
	})

	t.Run("without yes an unanswered prompt still declines", func(t *testing.T) {
		var got bool
		silenceStdout(t, func() {
			withStdin(t, "", func() { got = promptOfflineMigration(false) })
		})
		assert.False(t, got, "doctor must not register a repo nobody agreed to register")
	})

	t.Run("a piped yes registers", func(t *testing.T) {
		var got bool
		silenceStdout(t, func() {
			withStdin(t, "y\n", func() { got = promptOfflineMigration(false) })
		})
		assert.True(t, got)
	})
}

// TestFixLegacyStructure_HonorsAssumeYes covers a default-yes repair prompt.
// Doctor's documented posture is auto-fix, so the default is deliberately kept
// when nobody answers — but --yes must reach it too.
func TestFixLegacyStructure_HonorsAssumeYes(t *testing.T) {
	issue := repoPathIssue{repoType: "ledger", path: t.TempDir(), issue: "legacy-structure"}

	t.Run("global yes keeps the current location", func(t *testing.T) {
		cli.SetAssumeYes(true)
		t.Cleanup(func() { cli.SetAssumeYes(false) })

		localCfg := &config.LocalConfig{}
		var got bool
		silenceStdout(t, func() {
			withStdin(t, "", func() { got = fixLegacyStructure(t.TempDir(), localCfg, issue) })
		})

		require.True(t, got)
		require.NotNil(t, localCfg.Ledger, "--yes must actually apply the fix")
		assert.Equal(t, issue.path, localCfg.Ledger.Path)
	})

	t.Run("an explicit no skips the fix", func(t *testing.T) {
		localCfg := &config.LocalConfig{}
		var got bool
		silenceStdout(t, func() {
			withStdin(t, "n\n", func() { got = fixLegacyStructure(t.TempDir(), localCfg, issue) })
		})

		assert.False(t, got)
		assert.Nil(t, localCfg.Ledger, "declining must not rewrite config")
	})
}

// TestFixBrokenSymlink_DeclineSkips covers the negated form of the same
// pattern, where --yes short-circuits an early return rather than a body.
func TestFixBrokenSymlink_DeclineSkips(t *testing.T) {
	issue := repoPathIssue{repoType: "team-context-symlink", path: t.TempDir(), issue: "broken-symlink"}

	var got bool
	silenceStdout(t, func() {
		withStdin(t, "n\n", func() { got = fixBrokenSymlink(&config.LocalConfig{}, issue) })
	})

	assert.False(t, got, "an explicit no must skip the re-clone")
}

// TestRunAdapterFix_ScopeEscalationIgnoresAssumeYes is the security carve-out:
// the argv in this prompt comes from an adapter, not from ox, and it escalates
// to global/system git scope. "Answer yes to prompts" must never become "let
// any installed adapter mutate global config unattended."
func TestRunAdapterFix_ScopeEscalationIgnoresAssumeYes(t *testing.T) {
	escalating := adapterprotocol.DiagnoseIssue{
		FixSafe: true,
		Fix:     "git config --global core.hooksPath /tmp/evil",
		FixArgv: []string{"git", "config", "--global", "core.hooksPath", "/tmp/evil"},
	}
	require.True(t, argvHasScopeEscalation(escalating.FixArgv),
		"sanity: this argv must be classified as a scope escalation")

	cli.SetAssumeYes(true)
	t.Cleanup(func() { cli.SetAssumeYes(false) })

	var err error
	silenceStdout(t, func() {
		withStdin(t, "y\n", func() { err = runAdapterFix(escalating, true) })
	})

	require.Error(t, err, "--yes and --force must not run an adapter-supplied global-scope command")
	assert.Contains(t, err.Error(), "auto-fix refused")

	// negative control: the same posture does not block an ordinary,
	// non-escalating fix from being considered.
	assert.False(t, argvHasScopeEscalation([]string{"git", "config", "core.hooksPath", ".githooks"}),
		"a repo-scoped config write is not an escalation")
}
