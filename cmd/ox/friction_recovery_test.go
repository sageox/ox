package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sageox/agentx"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive real invocations through executeWithFrictionRecovery, the
// path every ox command takes, and assert on what the coworker sees: the exit
// code, stdout, and stderr. Every target is read-only, so a correction that
// does run changes nothing on the machine.

// agentEnvPrefixes and agentEnvNames cover what agentx reads (and a few
// orchestrators that wrap an agent) to decide a coding agent is driving.
var (
	agentEnvPrefixes = []string{"CLAUDE", "CONDUCTOR_", "CODEX_", "CURSOR_", "AMP_", "GC_", "PI_", "OMP_", "OPENCLAW_", "GEMINI", "DROID", "WINDSURF", "KIRO", "GOOSE", "OPENCODE", "CLINE", "CODY", "COPILOT", "CONTINUE", "AIDER", "CODE_PUPPY", "FACTORY_", "BUZZ_ACP"}
	agentEnvNames    = []string{"AGENT_ENV", "AI_AGENT", "ORCHESTRATOR_ENV", "GASCITY", "CODEIUM_AGENT", "_"}
)

// withoutAgentContext removes every variable that makes agentx report a coding
// agent, so output assertions describe a person at a terminal. t.Setenv records
// the original value; unsetting afterwards makes the variable truly absent.
func withoutAgentContext(t *testing.T) {
	t.Helper()
	for _, pair := range os.Environ() { // safe: reads names only, to unset agent-detection vars
		name, _, _ := strings.Cut(pair, "=")
		drop := false
		for _, prefix := range agentEnvPrefixes {
			drop = drop || strings.HasPrefix(name, prefix)
		}
		for _, exact := range agentEnvNames {
			drop = drop || name == exact
		}
		if drop {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}
	require.False(t, agentx.IsAgentContext(), "an agent signal is still set; the output assertions below assume a person")
}

// recoveryRun is what one invocation printed and returned.
type recoveryRun struct {
	code           int
	stdout, stderr string
}

// recoveryEnv prepares an offline, non-tracking, non-repository environment and
// puts the shared command tree back the way it was found when the test ends.
func recoveryEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("OX_NO_DAEMON", "1")
	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("HOME", t.TempDir())
	// the recovery path marks retries in the process environment and never clears it
	t.Setenv("OX_FRICTION_RETRY", "")
	require.NoError(t, os.Unsetenv("OX_FRICTION_RETRY"))
	t.Chdir(t.TempDir())

	restoreRootCmdState(t)
	savedCfg := cfg
	t.Cleanup(func() { cfg = savedCfg })

	// flags parsed by one invocation (--help above all) would otherwise still be
	// set when the next test runs the same command
	t.Cleanup(func() { resetFlagsInTree(rootCmd) })
}

// resetFlagsInTree puts every flag of cmd and its descendants back to its
// default. Cobra never does this itself, because a process runs one command.
func resetFlagsInTree(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	cmd.Flags().VisitAll(reset)
	cmd.PersistentFlags().VisitAll(reset)
	for _, child := range cmd.Commands() {
		resetFlagsInTree(child)
	}
}

// runRecovery runs args through the real recovery path and captures the result.
func runRecovery(t *testing.T, args ...string) recoveryRun {
	t.Helper()
	var run recoveryRun
	run.stderr = captureStderr(t, func() {
		run.stdout = captureStdoutForPlanCLI(t, func() {
			run.code = executeWithFrictionRecovery(context.Background(), args, 0)
		})
	})
	return run
}

// TestRecovery_AutoCorrections covers the corrections that run unprompted.
// Failure prevented: the shapes agents actually typed (replayed from real
// friction events) ending in `unknown command` instead of the command they
// meant.
func TestRecovery_AutoCorrections(t *testing.T) {
	withoutAgentContext(t)

	tests := []struct {
		name       string
		args       []string
		wantStdout string // substring of the command's own output
		wantNote   string // substring of the correction line on stderr
	}{
		{
			name:       "a command line passed as one word",
			args:       []string{"plan list", "--help"},
			wantStdout: "ox plan list",
			wantNote:   "-> Correcting to: plan list --help (zsh does not split",
		},
		{
			name:       "a mapped command with a flag after it",
			args:       []string{"auth", "--help"},
			wantStdout: "ox login",
			wantNote:   "-> Correcting to: login --help",
		},
		{
			name:       "a synonym with a flag after it",
			args:       []string{"whoami", "--help"},
			wantStdout: "ox status",
			wantNote:   "-> Correcting to: status --help",
		},
		{
			// help keeps the run read-only; daemon list would read the real registry
			name:       "a subcommand typed as a flag",
			args:       []string{"plan", "--list", "--help"},
			wantStdout: "ox plan list",
			wantNote:   "-> Correcting to: plan list --help (--list is a subcommand, not a flag",
		},
		{
			name:       "the help convention",
			args:       []string{"plan", "help"},
			wantStdout: "ox plan",
			wantNote:   "-> Correcting to: plan --help",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recoveryEnv(t)
			run := runRecovery(t, tt.args...)
			assert.Equal(t, 0, run.code, "stderr: %s", run.stderr)
			assert.Contains(t, run.stdout, tt.wantStdout)
			assert.Contains(t, run.stderr, tt.wantNote)
			assert.NotContains(t, run.stderr, "unknown command", "the original error must not be shown for a correction that worked")
		})
	}
}

// TestRecovery_CarriedArgumentsReachTheCorrectedCommand proves a catalog match
// on the leading words keeps the rest of the invocation.
// Failure prevented: `ox plan show <slug>` being corrected to a bare
// `ox plan view`, which asks for the slug it just threw away.
func TestRecovery_CarriedArgumentsReachTheCorrectedCommand(t *testing.T) {
	withoutAgentContext(t)
	recoveryEnv(t)

	run := runRecovery(t, "plan", "show", "no-such-plan")
	assert.Contains(t, run.stderr, "-> Correcting to: plan view no-such-plan")
	// plan view ran and failed on the missing plan; it did not fail as a usage error
	assert.NotContains(t, run.stderr, `unknown command "show"`)
	assert.NotContains(t, run.stderr, "accepts 1 arg")
}

// TestRecovery_SuggestOnly covers the corrections that must not run.
// Failure prevented: an agent that mistypes a verb having a different, possibly
// destructive, command run for it.
func TestRecovery_SuggestOnly(t *testing.T) {
	withoutAgentContext(t)

	tests := []struct {
		name        string
		args        []string
		wantSuggest string
		wantError   string
	}{
		{
			name:        "update is upgrade, which replaces the binary",
			args:        []string{"update"},
			wantSuggest: "upgrade",
			wantError:   `unknown command "update"`,
		},
		{
			name:        "auth token is not login: login takes no argument",
			args:        []string{"auth", "token"},
			wantSuggest: "login token",
			wantError:   `unknown command "auth"`,
		},
		{
			name:        "signout is logout, which discards credentials",
			args:        []string{"signout"},
			wantSuggest: "logout",
			wantError:   `unknown command "signout"`,
		},
		{
			name:        "a destructive subcommand typed as a flag",
			args:        []string{"daemon", "--kill"},
			wantSuggest: "daemon kill",
			wantError:   "unknown flag: --kill",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recoveryEnv(t)
			run := runRecovery(t, tt.args...)
			assert.Equal(t, 1, run.code, "a suggestion is not a success")
			assert.Contains(t, run.stderr, "Did you mean?")
			assert.Contains(t, run.stderr, tt.wantSuggest)
			assert.Contains(t, run.stderr, tt.wantError, "the original error is still shown")
			assert.NotContains(t, run.stderr, "Correcting to", "nothing was run on the coworker's behalf")
			assert.Empty(t, run.stdout, "the suggested command must not have run")
		})
	}
}

// TestRecovery_NoUnrelatedGuess covers the edit-distance guard.
// Failure prevented: `ox mcp` suggesting `gc`, a command that re-clones
// repositories, because the two names are two edits apart.
func TestRecovery_NoUnrelatedGuess(t *testing.T) {
	withoutAgentContext(t)

	for _, word := range []string{"mcp", "doc", "api"} {
		t.Run(word, func(t *testing.T) {
			recoveryEnv(t)
			run := runRecovery(t, word)
			assert.Equal(t, 1, run.code)
			assert.Contains(t, run.stderr, `unknown command "`+word+`"`)
			assert.NotContains(t, run.stderr, "Did you mean", "no near-miss exists for this word")
		})
	}
}

// TestRecovery_RealTyposStillGetAGuess proves the guard does not silence the
// help it exists to protect.
func TestRecovery_RealTyposStillGetAGuess(t *testing.T) {
	withoutAgentContext(t)
	recoveryEnv(t)

	run := runRecovery(t, "sesion")
	assert.Equal(t, 1, run.code)
	assert.Contains(t, run.stderr, "Did you mean?")
	assert.Contains(t, run.stderr, "session")
}

// TestRecovery_GuideListStaysAnError proves a flag is not turned into a
// subcommand that means something else: `guide` reads "list" as a topic name.
func TestRecovery_GuideListStaysAnError(t *testing.T) {
	withoutAgentContext(t)
	recoveryEnv(t)

	run := runRecovery(t, "guide", "--list")
	assert.Equal(t, 1, run.code)
	assert.Contains(t, run.stderr, "unknown flag: --list")
	assert.NotContains(t, run.stderr, "Correcting to")
}

// TestRecovery_AgentGetsJSON proves an agent reads the correction as data.
// Failure prevented: the correction arriving as prose on stderr, where an agent
// that only parses stdout never sees why its command changed.
func TestRecovery_AgentGetsJSON(t *testing.T) {
	recoveryEnv(t)
	t.Setenv("AGENT_ENV", "claude-code")
	require.True(t, agentx.IsAgentContext())

	run := runRecovery(t, "plan list", "--help")
	assert.Equal(t, 0, run.code, "stderr: %s", run.stderr)
	first, _, _ := strings.Cut(run.stdout, "\n")
	assert.JSONEq(t, `{"_corrected":{"was":"'plan list' --help","now":"plan list --help","note":"zsh does not split $var into words; use an array or ${=var}"}}`, first)
	assert.NotContains(t, run.stderr, "Correcting to")
}

// TestRecovery_TelemetryRecordsTheOffer pins the call order main.go must keep:
// the suggestion is attached to the event before the event is sent.
// Failure prevented: friction analytics that record the failure but never the
// fix. (The event leaves through a daemon socket; this checks the source
// contract, as TestFrictionIPC_CalledFromRecoveryPath does for the send.)
func TestRecovery_TelemetryRecordsTheOffer(t *testing.T) {
	src, err := os.ReadFile(repoPath("main.go"))
	require.NoError(t, err)

	attach := strings.Index(string(src), "attachSuggestion(result)")
	send := strings.Index(string(src), "sendFrictionEvent(result.Event)")
	require.GreaterOrEqual(t, attach, 0, "main.go must attach the suggestion to the friction event")
	require.GreaterOrEqual(t, send, 0)
	assert.Less(t, attach, send, "the suggestion must be attached before the event is sent")
}

// TestRecovery_GatedCommandIsFixedBeforeCobra prevents an enabled feature-gated
// command from missing the pre-cobra rewrite. Normalization must run after the
// feature-gate sync; otherwise "carts --help" typed as one word cannot resolve,
// cobra rejects it, and the correction costs a failed attempt and a retry.
func TestRecovery_GatedCommandIsFixedBeforeCobra(t *testing.T) {
	recoveryEnv(t)
	t.Setenv("FEATURE_CARTS", "true")
	wasRegistered := commandRegistered(rootCmd, cartsCmd)
	t.Cleanup(func() { setCommandRegistered(rootCmd, cartsCmd, wasRegistered) })

	run := runRecovery(t, "carts --help")

	require.Equal(t, 0, run.code, "stdout=%s stderr=%s", run.stdout, run.stderr)
	assert.Empty(t, os.Getenv("OX_FRICTION_RETRY"), "the one-word command must be fixed on the first attempt, not by a retry")
	assert.Contains(t, run.stdout+run.stderr, "carts")
}
