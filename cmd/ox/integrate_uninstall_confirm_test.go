package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedOMPIntegration gives uninstallAllIntegrations something to find, so it
// reaches the confirmation prompt instead of returning "No integrations found".
func seedOMPIntegration(t *testing.T) string {
	t.Helper()

	repoRoot := setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"omp":      ".omp",
		"opencode": ".opencode",
	})

	marker := filepath.Join(repoRoot, ".omp", "hooks.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("{}\n"), 0o644))
	return marker
}

// TestUninstallAllIntegrations_UnansweredPromptRemovesNothing is the gate on a
// default-YES prompt: before, an unattended run answered "yes" on the user's
// behalf and removed every integration.
func TestUninstallAllIntegrations_UnansweredPromptRemovesNothing(t *testing.T) {
	marker := seedOMPIntegration(t)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = uninstallAllIntegrations(false)
		})
	})

	assert.True(t, errors.Is(err, cli.ErrConfirmationRequired),
		"an unanswered default-yes prompt must fail, not uninstall; got %v", err)
	_, statErr := os.Stat(marker)
	assert.NoError(t, statErr, "nothing may be removed when nobody answered")
}

// TestUninstallAllIntegrations_DeclineRemovesNothing pins that a real "no" is
// still an ordinary, successful cancel — not an error.
func TestUninstallAllIntegrations_DeclineRemovesNothing(t *testing.T) {
	marker := seedOMPIntegration(t)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "n\n", func() {
			err = uninstallAllIntegrations(false)
		})
	})

	require.NoError(t, err)
	_, statErr := os.Stat(marker)
	assert.NoError(t, statErr, "declining must leave integrations in place")
}

// TestUninstallAllIntegrations_PipedYesProceeds proves the prompt still accepts
// a scripted answer — requiring a TTY would break every honest caller.
func TestUninstallAllIntegrations_PipedYesProceeds(t *testing.T) {
	marker := seedOMPIntegration(t)

	var err error
	silenceStdout(t, func() {
		withStdin(t, "y\n", func() {
			err = uninstallAllIntegrations(false)
		})
	})

	require.NoError(t, err)
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a piped yes must actually uninstall")
}

// TestUninstallAllIntegrations_GlobalYesProceeds covers the --yes path through
// a command that has its own --force flag: both must work.
func TestUninstallAllIntegrations_GlobalYesProceeds(t *testing.T) {
	marker := seedOMPIntegration(t)

	cli.SetAssumeYes(true)
	t.Cleanup(func() { cli.SetAssumeYes(false) })

	var err error
	silenceStdout(t, func() {
		withStdin(t, "", func() {
			err = uninstallAllIntegrations(false)
		})
	})

	require.NoError(t, err)
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "--yes must satisfy this prompt")
}
