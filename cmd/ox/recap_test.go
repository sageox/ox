package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newThrowawayRecapFlags builds a standalone cobra.Command carrying the same
// flags recap.go registers on recapCmd, so flag-parsing unit tests don't
// mutate (or race on) the shared global recapCmd.Flags() FlagSet.
func newThrowawayRecapFlags() *cobra.Command {
	cmd := &cobra.Command{Use: "recap"}
	cmd.Flags().Bool("json", false, "structured JSON evidence bundle for AI coworkers")
	cmd.Flags().String("since", "30d", "reporting window")
	cmd.Flags().String("user", "", "report for a specific coworker")
	return cmd
}

// --- resolveRecapSince ---
//
// Failure prevented: a bad --since value either silently resolves to an
// unintended window or panics instead of returning a clear error.

func TestResolveRecapSince(t *testing.T) {
	tests := []struct {
		name    string
		since   string
		wantErr bool
	}{
		{"default 30d", "30d", false},
		{"days shorthand", "7d", false},
		{"weeks shorthand", "2w", false},
		{"go duration", "24h", false},
		{"iso date", "2026-01-01", false},
		{"garbage rejected", "not-a-time-at-all", true},
		{"empty string rejected", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newThrowawayRecapFlags()
			require.NoError(t, cmd.Flags().Set("since", tt.since))

			_, err := resolveRecapSince(cmd)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- terminalWidth ---

func TestTerminalWidth(t *testing.T) {
	tests := []struct {
		name    string
		columns string
		want    int
	}{
		{"unset defaults to 80", "", 80},
		{"valid value honored", "120", 120},
		{"below the 40-column floor falls back to 80", "10", 80},
		{"non-numeric falls back to 80", "not-a-number", 80},
		{"exactly at the floor is honored", "40", 40},
		{"whitespace padded value honored", "  100  ", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("COLUMNS", tt.columns)
			assert.Equal(t, tt.want, terminalWidth())
		})
	}
}

// --- resolveRecapIdentity ---
//
// Failure prevented: --user silently falls through to the ambient
// identity instead of overriding it, so `ox recap --user alice` reports on
// the wrong person.

func TestResolveRecapIdentity_UserOverride(t *testing.T) {
	cmd := newThrowawayRecapFlags()
	require.NoError(t, cmd.Flags().Set("user", "alice"))

	id := resolveRecapIdentity(cmd, "")
	assert.Equal(t, "alice", id.DisplayName)
	assert.Equal(t, "alice", id.Slug)
	assert.Empty(t, id.UserID, "--user overrides by name/slug, never claims a stable user_id it can't verify")
}

func TestResolveRecapIdentity_DefaultDoesNotPanicWithoutAuth(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))

	cmd := newThrowawayRecapFlags() // --user left empty
	assert.NotPanics(t, func() {
		resolveRecapIdentity(cmd, "")
	})
}

// --- flag registration ---

func TestRecapCmd_FlagDefaults(t *testing.T) {
	since := recapCmd.Flags().Lookup("since")
	require.NotNil(t, since)
	assert.Equal(t, "30d", since.DefValue)

	jsonFlag := recapCmd.Flags().Lookup("json")
	require.NotNil(t, jsonFlag)
	assert.Equal(t, "false", jsonFlag.DefValue)

	user := recapCmd.Flags().Lookup("user")
	require.NotNil(t, user)
	assert.Equal(t, "", user.DefValue)

	assert.Equal(t, "knowledge", recapCmd.GroupID)
}

// --- end-to-end: runRecap ---

// newRecapCmdFixture builds a git-initialized, .sageox-initialized project
// whose local config points at an empty ledger, fully isolated from the
// developer's real SageOx config/auth/ledger via XDG env overrides. The
// caller MUST t.Chdir(projectRoot): findGitRoot() shells `git rev-parse
// --show-toplevel` against the process cwd, not an injected path. Because it
// mutates process env vars, callers must not use t.Parallel().
func newRecapCmdFixture(t *testing.T) (projectRoot, ledgerDir string) {
	t.Helper()

	// isolate from ~/.config/sageox and ~/.local/share/sageox on the dev machine
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	// this test process is itself a running ox agent instance (SAGEOX_AGENT_ID
	// is set in the real environment) — neutralize it so detectAgentContext
	// never resolves a real instance and force-flips --json underneath us.
	t.Setenv("SAGEOX_AGENT_ID", "")

	projectRoot = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".sageox"), 0o755))

	// runGit is the shared cmd/ox test helper (session_upload_push_test.go).
	runGit(t, projectRoot, "init")
	runGit(t, projectRoot, "config", "user.name", "ox-test")
	runGit(t, projectRoot, "config", "user.email", "test@test.sageox.ai")

	ledgerDir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0o755))
	require.NoError(t, config.SaveLocalConfig(projectRoot, &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: ledgerDir},
	}))

	return projectRoot, ledgerDir
}

// captureRealStdout swaps os.Stdout for the duration of fn and returns everything
// written to it. Drains via a goroutine so writes larger than the OS pipe
// buffer can't deadlock the caller.
func captureRealStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()

	fn()

	require.NoError(t, w.Close())
	return <-done
}

func TestRunRecap_JSONColdStart_NoErrorStableKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git, chdir")
	}
	projectRoot, _ := newRecapCmdFixture(t)
	t.Chdir(projectRoot)

	cmd := newThrowawayRecapFlags()
	require.NoError(t, cmd.Flags().Set("json", "true"))
	require.NoError(t, cmd.Flags().Set("since", "30d"))
	require.NoError(t, cmd.Flags().Set("user", ""))

	var runErr error
	stdout := captureRealStdout(t, func() {
		runErr = runRecap(cmd, nil)
	})
	require.NoError(t, runErr, "recap must not error on a freshly initialized project with an empty ledger")

	var out map[string]any
	require.NoError(t, json.Unmarshal(stdout, &out), "output must be valid JSON: %s", stdout)

	// Golden shape: the keys the calling agent's narration contract depends on.
	for _, key := range []string{"user", "scope", "since", "until", "coverage", "guidance"} {
		assert.Contains(t, out, key, "JSON output missing expected key %q", key)
	}
	assert.Equal(t, "personal", out["scope"])
	assert.NotEmpty(t, out["guidance"], "guidance must be present even for a cold-start bundle")

	coverage, ok := out["coverage"].(map[string]any)
	require.True(t, ok, "coverage must be an object")
	assert.Equal(t, float64(0), coverage["ledger_all_time"], "empty ledger must report zero, not omit the field")
}

func TestRunRecap_UserOverride_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git, chdir")
	}
	projectRoot, _ := newRecapCmdFixture(t)
	t.Chdir(projectRoot)

	cmd := newThrowawayRecapFlags()
	require.NoError(t, cmd.Flags().Set("json", "true"))
	require.NoError(t, cmd.Flags().Set("since", "30d"))
	require.NoError(t, cmd.Flags().Set("user", "alice"))

	var runErr error
	stdout := captureRealStdout(t, func() {
		runErr = runRecap(cmd, nil)
	})
	require.NoError(t, runErr)

	var out map[string]any
	require.NoError(t, json.Unmarshal(stdout, &out))
	assert.Equal(t, "alice", out["user"], "--user must override whichever identity would otherwise be resolved")
}

func TestRunRecap_HumanMode_ColdStartNoError(t *testing.T) {
	if testing.Short() {
		t.Skip("short: shells git, chdir")
	}
	projectRoot, _ := newRecapCmdFixture(t)
	t.Chdir(projectRoot)

	cmd := newThrowawayRecapFlags()
	require.NoError(t, cmd.Flags().Set("json", "false"))
	require.NoError(t, cmd.Flags().Set("since", "30d"))
	require.NoError(t, cmd.Flags().Set("user", ""))

	var runErr error
	stdout := captureRealStdout(t, func() {
		runErr = runRecap(cmd, nil)
	})
	require.NoError(t, runErr)

	// ansi.Strip mirrors the stripping cmd/ox/main.go's init() applies to real
	// stdout when it isn't a TTY; this test captures stdout directly (bypassing
	// that pipe), so strip explicitly before asserting on visible content.
	visible := ansi.Strip(string(stdout))
	assert.Contains(t, visible, "value starts the moment you begin", "a cold-start human-mode report must lead with next-actions prose, not a bare statistic")
}
