package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/signing"
	"github.com/sageox/ox/internal/uicatalog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file proves that the recovery hints ox prints name commands (and flags)
// that exist. A hint is a promise to a coworker or an AI coworker: "run this to
// get unstuck". When the command was renamed or never existed, the reader runs
// it, gets "unknown command", and has no next step. The hint text is a plain
// string, so nothing but a test notices the drift.

// quotedHint matches a command quoted inside prose: Run 'ox daemon logs' ...
var quotedHint = regexp.MustCompile(`'(ox [^'\n]+)'`)

// indentedHint matches a command shown on its own indented line.
var indentedHint = regexp.MustCompile(`(?m)^[ \t]+(ox [^\n]+)$`)

// labeledHint matches the "Run: ox ..." line of a suggestion box, which sits
// between box borders rather than at the start of a line.
var labeledHint = regexp.MustCompile(`Run: (ox(?: [a-z][a-z0-9-]*)*(?: --?[a-z][a-z-]*)*)`)

// hintedCommands returns every "ox ..." command a block of user-facing output
// recommends, with styling removed.
func hintedCommands(output string) []string {
	plain := ansi.Strip(output)
	var found []string
	for _, pattern := range []*regexp.Regexp{quotedHint, indentedHint, labeledHint} {
		for _, match := range pattern.FindAllStringSubmatch(plain, -1) {
			found = append(found, strings.TrimSpace(match[1]))
		}
	}
	return found
}

// resolveHint looks a recommended "ox ..." command up in the real command tree.
// It returns the command it names and fails if the hint names no command, has
// words left over that name nothing, or advertises a flag the command lacks.
func resolveHint(t *testing.T, hint string) *cobra.Command {
	t.Helper()

	// a shell pipeline after the command is the reader's business, not ox's
	command, _, _ := strings.Cut(hint, " | ")
	fields := strings.Fields(command)
	require.NotEmpty(t, fields, "hint %q is empty", hint)
	require.Equal(t, "ox", fields[0], "hint %q must start with ox", hint)

	var words, flags []string
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			name, _, _ := strings.Cut(strings.TrimLeft(field, "-"), "=")
			flags = append(flags, name)
			continue
		}
		words = append(words, field)
	}

	cmd, leftover, err := rootCmd.Find(words)
	require.NoError(t, err, "hint %q must resolve to a command", hint)
	require.NotSame(t, rootCmd, cmd, "hint %q resolves to the root command, so it names nothing", hint)
	require.Empty(t, leftover, "hint %q has words after %q that name nothing", hint, cmd.CommandPath())
	for _, name := range flags {
		assert.NotNil(t, cmd.Flag(name), "hint %q advertises --%s, which %q does not have", hint, name, cmd.CommandPath())
	}
	return cmd
}

// requireHintTo fails unless every command the output recommends resolves and
// at least one of them resolves to wantPath. It returns that command so callers
// can check flags. Resolving all of them, not just the first match, is what
// catches a stale hint sitting beside a good one.
func requireHintTo(t *testing.T, output, wantPath string) *cobra.Command {
	t.Helper()
	hints := hintedCommands(output)
	require.NotEmpty(t, hints, "output recommends no ox command:\n%s", output)

	var want *cobra.Command
	for _, hint := range hints {
		if cmd := resolveHint(t, hint); cmd.CommandPath() == wantPath {
			want = cmd
		}
	}
	require.NotNil(t, want, "output recommends %q, none of which resolve to %q", hints, wantPath)
	return want
}

// TestHooksEventsLog_PointsAtRealDaemonLogCommand pins the `hooks` log pointer
// to a daemon log command and a --all flag that exist.
// Failure prevented: the command exists only to tell a coworker where hook
// activity is logged; a stale pointer leaves them with no way to find it.
func TestHooksEventsLog_PointsAtRealDaemonLogCommand(t *testing.T) {
	out := captureStdoutForPlanCLI(t, func() {
		require.NoError(t, hooksEventsLogCmd.RunE(hooksEventsLogCmd, nil))
	})

	cmd := requireHintTo(t, out, "ox daemon logs")
	assert.NotNil(t, cmd.Flag("all"), "the hint filters the whole log, so --all must exist")
}

// TestWarnIfRedactionSignatureInvalid covers each verification status a coworker
// can hit before a session is recorded.
// Failure prevented: a tampered redaction policy goes unannounced or blocks
// work it should not, and the tamper warning points at a command that does not
// exist so the coworker cannot follow it up.
func TestWarnIfRedactionSignatureInvalid(t *testing.T) {
	tests := []struct {
		name         string
		result       signing.VerificationResult
		wantOK       bool
		wantSilent   bool
		wantInOutput []string
		wantHintTo   string
	}{
		{
			name:       "dev build with no signature is silent",
			result:     signing.VerificationResult{Status: signing.StatusNotConfigured},
			wantOK:     true,
			wantSilent: true,
		},
		{
			name:       "valid signature is silent",
			result:     signing.VerificationResult{Status: signing.StatusValid},
			wantOK:     true,
			wantSilent: true,
		},
		{
			name:         "invalid signature warns and reports failure",
			result:       signing.VerificationResult{Status: signing.StatusInvalid},
			wantOK:       false,
			wantInOutput: []string{"Redaction policy signature INVALID", "tampered"},
			wantHintTo:   "ox session redaction verify",
		},
		{
			name:         "missing signature warns but does not block",
			result:       signing.VerificationResult{Status: signing.StatusMissing},
			wantOK:       true,
			wantInOutput: []string{"Could not verify redaction policy signature"},
		},
		{
			name:         "verification error warns, shows why, and does not block",
			result:       signing.VerificationResult{Status: signing.StatusError, Error: assert.AnError},
			wantOK:       true,
			wantInOutput: []string{"Could not verify redaction policy signature", assert.AnError.Error()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// swaps package-level state and os.Stderr, so not parallel
			original := verifyRedactionSignature
			t.Cleanup(func() { verifyRedactionSignature = original })
			verifyRedactionSignature = func() *signing.VerificationResult {
				result := tt.result
				return &result
			}

			var ok bool
			stderr := captureStderr(t, func() { ok = warnIfRedactionSignatureInvalid() })

			assert.Equal(t, tt.wantOK, ok)
			if tt.wantSilent {
				assert.Empty(t, stderr, "a good or unconfigured signature must not warn")
			}
			for _, want := range tt.wantInOutput {
				assert.Contains(t, ansi.Strip(stderr), want)
			}
			if tt.wantHintTo != "" {
				requireHintTo(t, stderr, tt.wantHintTo)
			}
		})
	}
}

// TestPrintErrorBanner covers what a coworker sees at the top of command output
// when the daemon has recorded errors they have not viewed.
// Failure prevented: the "see all N errors" pointer names a command that does
// not exist, or the count line is dropped so the coworker never learns that
// older errors are waiting.
func TestPrintErrorBanner(t *testing.T) {
	// not parallel: resolving the hint walks rootCmd, whose flag lookup lazily
	// mutates shared command state

	now := time.Now()
	newest := daemon.StoredError{Message: "push rejected by remote", Severity: "error", Timestamp: now.Add(-2 * time.Minute)}
	older := daemon.StoredError{Message: "fetch timed out", Severity: "warning", Timestamp: now.Add(-3 * time.Hour)}
	oldest := daemon.StoredError{Message: "auth token expired", Severity: "error", Timestamp: now.Add(-48 * time.Hour)}

	tests := []struct {
		name          string
		errs          []daemon.StoredError
		wantEmpty     bool
		wantContains  []string
		wantCountHint bool
	}{
		{
			name:      "no errors prints nothing",
			errs:      nil,
			wantEmpty: true,
		},
		{
			name:         "single error shows the message and age without a count",
			errs:         []daemon.StoredError{newest},
			wantContains: []string{"push rejected by remote", "2 minutes ago"},
		},
		{
			name:         "single warning is shown the same way",
			errs:         []daemon.StoredError{older},
			wantContains: []string{"fetch timed out", "3 hours ago"},
		},
		{
			name:          "several errors show only the newest plus the total count",
			errs:          []daemon.StoredError{newest, older, oldest},
			wantContains:  []string{"push rejected by remote", "see all 3 errors"},
			wantCountHint: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printErrorBanner(&buf, tt.errs)
			plain := ansi.Strip(buf.String())

			if tt.wantEmpty {
				assert.Empty(t, plain)
				return
			}
			for _, want := range tt.wantContains {
				assert.Contains(t, plain, want)
			}
			if tt.wantCountHint {
				assert.NotContains(t, plain, "fetch timed out", "only the most recent error is shown inline")
				requireHintTo(t, buf.String(), "ox daemon logs")
			} else {
				assert.NotContains(t, plain, "see all", "a lone error has nothing further to point at")
			}
		})
	}
}

// TestPrimeText_LedgerNeededSuggestsDoctorFix covers the prime text an AI
// coworker reads when session recording is on but the ledger has not been
// provisioned yet.
// Failure prevented: the suggestion box tells the coworker to run a repair
// command or flag that does not exist, so recording stays silently unavailable.
func TestPrimeText_LedgerNeededSuggestsDoctorFix(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	output := agentPrimeOutput{
		AgentID: "Oxtest",
		Status:  "fresh",
		Session: &sessionStatus{LedgerNeeded: true, Mode: "all", Source: "user config"},
	}

	// the suggestion box is written to stderr, not the cobra writer
	var err error
	stderr := captureStderr(t, func() { err = outputAgentPrimeText(cmd, output) })
	require.NoError(t, err)

	doctor := requireHintTo(t, stderr, "ox doctor")
	assert.NotNil(t, doctor.Flag("fix"), "the box tells the coworker to run doctor --fix")
	assert.Contains(t, ansi.Strip(stderr), "Ledger Required for Sessions")
	assert.NotContains(t, buf.String(), "Session: Recording", "a ledger that is not provisioned is not a recording session")
}

// TestSessionStopRetry_NamesLoggingCommandsThatExist covers `session stop` on a
// generic-adapter recording whose drop file captured nothing.
// Failure prevented: the retry guidance tells an AI coworker to feed entries
// through `session log --role ... --content ...` or `session record`, but those
// flags are rejected as unknown (the guidance was once wrong in exactly this
// way), so the coworker can never produce a non-empty session to stop.
func TestSessionStopRetry_NamesLoggingCommandsThatExist(t *testing.T) {
	const agentID = "OxRetry"
	setupGenericEmptyDropFileRecording(t, agentID)
	inst := &agentinstance.Instance{AgentID: agentID, AgentType: "generic"}

	var err error
	stdout := captureStdoutForPlanCLI(t, func() { err = runAgentSessionStop(inst) })
	require.NoError(t, err, "an empty capture is guidance, not a failure")

	var retry struct {
		Success       bool     `json:"success"`
		Type          string   `json:"type"`
		AgentID       string   `json:"agent_id"`
		RetryGuidance string   `json:"retry_guidance"`
		NextActions   []string `json:"next_actions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &retry), "stdout must be one JSON document:\n%s", stdout)
	assert.False(t, retry.Success)
	assert.Equal(t, "session_stop_retry", retry.Type)
	assert.Equal(t, agentID, retry.AgentID)

	// the guidance names the verbs and flags; the agent dispatcher must accept them
	assert.Contains(t, retry.RetryGuidance, "session log --role user --content")
	assert.Contains(t, retry.RetryGuidance, "session record")
	for _, flag := range []string{"role", "content"} {
		assert.NotNil(t, agentCmd.Flags().Lookup(flag), "guidance uses --%s, which the agent dispatcher must register", flag)
		assert.Contains(t, agentSessionStringFlags, flag, "--%s must be handed back to the session log parser", flag)
	}
	joinedActions := strings.Join(retry.NextActions, "\n")
	assert.Contains(t, joinedActions, "session record")
	assert.Contains(t, joinedActions, "ox agent "+agentID+" session stop")

	// the recording stays restartable: stop marked it incomplete rather than clearing it
	root, rootErr := findProjectRoot()
	require.NoError(t, rootErr)
	state, stateErr := session.LoadRecordingStateForAgent(root, agentID)
	require.NoError(t, stateErr)
	require.NotNil(t, state, "the recording must survive so the coworker can log entries and retry")
	assert.True(t, state.StopIncomplete)
}

// setupGenericEmptyDropFileRecording leaves an isolated project holding a live
// generic-adapter recording whose drop file does not exist, which is the state
// `session stop` treats as "nothing was captured".
func setupGenericEmptyDropFileRecording(t *testing.T, agentID string) {
	t.Helper()

	// offline: nothing in the stop path may reach a real endpoint or the real home
	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	projectRoot := createInitializedProject(t)
	origCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	require.NoError(t, os.Chdir(projectRoot))
	// the project root findProjectRoot resolves has symlinks evaluated (macOS /var -> /private/var)
	projectRoot, err = filepath.EvalSymlinks(projectRoot)
	require.NoError(t, err)

	// stop reads the output mode from the global config that cobra normally sets up
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })

	// legacy in-repo location is the one every recording search path includes
	sessionPath := filepath.Join(projectRoot, "sessions", "2026-01-01T00-00-test-"+agentID)
	require.NoError(t, session.SaveRecordingState(projectRoot, &session.RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().Add(-time.Minute),
		SessionPath: sessionPath,
		AdapterName: "generic",
		AgentType:   "generic",
		SessionFile: filepath.Join(sessionPath, "input.jsonl"),
	}))
	require.True(t, session.IsRecordingForAgent(projectRoot, agentID), "fixture must be a live recording")
}

// paletteCommand matches an ox command (with optional flags) inside a rendered
// catalog row, stopping before the box border and padding.
var paletteCommand = regexp.MustCompile(`\box( [a-z][a-z0-9-]*)+( --[a-z][a-z-]*)*`)

// TestUICatalogModal_PaletteCommandsResolve checks the command palette shown in
// the design catalog against the real command tree.
// Failure prevented: the catalog teaches `ox sync --read-only` (or any palette
// row) after the command or flag was renamed, and the next component author
// copies a command that no longer works.
func TestUICatalogModal_PaletteCommandsResolve(t *testing.T) {
	entry, ok := uicatalog.Get("modal")
	require.True(t, ok)

	commands := paletteCommand.FindAllString(ansi.Strip(entry.Render()), -1)
	require.Len(t, commands, 4, "the palette lists four commands: %q", commands)
	for _, command := range commands {
		resolveHint(t, command)
	}
}
