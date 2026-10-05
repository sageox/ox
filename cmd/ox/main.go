package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/sageox/agentx"
	friction "github.com/sageox/frictionax"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/observability"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/spf13/cobra"

	// registers all supported agents for detection
	_ "github.com/sageox/agentx/setup"
)

// maxFrictionRetries limits auto-execute attempts to prevent infinite loops.
// If the corrected command also fails, we stop and show the error.
const maxFrictionRetries = 1

// ansiStripper wraps a writer and strips ANSI escape codes on Write.
type ansiStripper struct {
	w io.Writer
}

func (s *ansiStripper) Write(p []byte) (int, error) {
	_, err := s.w.Write([]byte(ansi.Strip(string(p))))
	return len(p), err // report original length to caller
}

// stdoutDone reports the copy result so main can fail if output was lost.
var stdoutDone chan error

// stderrWg waits for diagnostics after stdout's result has been reported.
var stderrWg sync.WaitGroup

func init() {
	// Agent UX Decision: Auto-disable terminal colors in agent context.
	//
	// Why: ANSI color codes consume ~100 extra tokens per response and create
	// noise in agent logs. Since agents are the primary consumers of ox output,
	// we optimize for their context windows by default.
	//
	// The NO_COLOR standard (https://no-color.org/) is respected by all color-aware
	// libraries we use: glamour, lipgloss, fatih/color, chroma.
	//
	// Override: Set NO_COLOR=0 to force colors even in agent context.
	if agentx.IsAgentContext() && os.Getenv("NO_COLOR") == "" {
		_ = os.Setenv("NO_COLOR", "1") // Setenv fails only on an invalid name
	}

	// Escape hatch: CLICOLOR_FORCE=1 disables the global ANSI stripper below
	// so styled output flows verbatim through pipes. Used by `make
	// catalog-build` to pipe `ox dev catalog --component=…` into
	// charmbracelet/freeze, which needs the ANSI to produce themed SVGs.
	// See .claude/rules/design.md and docs/design/theming.md.
	forceColor, _ := strconv.ParseBool(os.Getenv("CLICOLOR_FORCE"))

	// When stdout/stderr are not a TTY (piped, captured by agent, etc.),
	// intercept with ANSI-stripping pipes. Lipgloss v2 beta always emits ANSI
	// via Style.Render() regardless of NO_COLOR — the colorprofile.Writer is
	// the intended stripping layer, but ~300 call sites use
	// fmt.Print(style.Render(...)) which bypasses it. These pipes catch
	// everything globally for both streams.
	if !forceColor && !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		_ = os.Setenv("NO_COLOR", "1") // keep for libraries that do check it; Setenv fails only on an invalid name

		pr, pw, err := os.Pipe()
		if err == nil {
			realStdout := os.Stdout
			os.Stdout = pw

			stdoutDone = make(chan error, 1)
			go func() {
				// Keep Go's default SIGPIPE behavior for early-closing consumers
				// such as head; other write errors are reported by main.
				_, err := io.Copy(&ansiStripper{realStdout}, pr)
				// Unblock a producer still writing after the destination failed.
				_ = pr.Close()
				stdoutDone <- err
			}()
		}
	}
	if !forceColor && !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		pr, pw, err := os.Pipe()
		if err == nil {
			realStderr := os.Stderr
			os.Stderr = pw

			stderrWg.Add(1)
			go func() {
				defer stderrWg.Done()
				defer pr.Close()
				io.Copy(&ansiStripper{realStderr}, pr) //nolint:errcheck // best-effort
			}()
		}
	}

	// initialize friction handling for "did you mean?" suggestions
	// must happen after all commands are registered via init() in other files
	initFriction(rootCmd)
}

func main() {
	// A detached sender started by telemetry.CapturePostHog: post the event on
	// stdin and exit before any command setup.
	if len(os.Args) == 2 && os.Args[1] == telemetry.PostHogSenderArg {
		telemetry.RunPostHogSender(os.Stdin)
		return
	}

	// Read before setting: this process was started by another ox only if
	// the marker was already there.
	startedByOx = os.Getenv(envStartedByOx) != ""
	_ = os.Setenv(envStartedByOx, "1") // Setenv fails only on an invalid name

	// load .env files if present (silently ignore if not found)
	// order: .env.local (highest priority), .env (base config)
	// supports FEATURE_CLOUD, FEATURE_AUTH, SAGEOX_API, etc.
	// A hosted read must not inherit endpoint or credential selection from
	// whichever source checkout happens to be the working directory.
	if !headlessLedgerReadRequested(os.Args[1:]) {
		_ = godotenv.Load(".env.local")
		_ = godotenv.Load(".env")
	}

	args := applyCatalogTokenRewrites(os.Args[1:], loadFlagAliases(defaultCatalogJSON))
	exitCode := executeWithFrictionRecovery(args, 0)
	// Flush the result before deciding success, while stderr can still report
	// a failed destination. Preserve an existing command failure's exit code.
	os.Stdout.Close()
	if stdoutDone != nil {
		if err := <-stdoutDone; err != nil {
			err = fmt.Errorf("write stdout: %w", err)
			printError(err)
			if exitCode == 0 {
				exitCode = 1
				if cliCtx != nil {
					cliCtx.Err = err
				}
			}
		}
	}
	// Before the trace flush below, which can take seconds and must not count
	// toward the command's reported duration.
	capturePostHogCommand(exitCode, os.Stderr)

	// Record cli.exit_code on the root OTel span and flush. This runs on
	// both success and error paths so failed commands appear in traces
	// with the right exit code — PersistentPostRunE only fires on success
	// in cobra, so it cannot be relied on for error reporting.
	observability.SetExitCode(exitCode)
	observability.Shutdown(context.Background())

	// Flush diagnostics after reporting any stdout failure.
	os.Stderr.Close()
	stderrWg.Wait()

	os.Exit(exitCode)
}

// commandExitError carries the exit status for a command that has already
// handled its output, bypassing friction recovery and duplicate error output.
type commandExitError struct {
	ExitCode int
	Message  string
}

func (e *commandExitError) Error() string {
	return e.Message
}

// executeWithFrictionRecovery runs the command with friction recovery support.
// If the command fails and we can auto-correct with high confidence, we retry
// with the corrected args. Returns the exit code.
func executeWithFrictionRecovery(args []string, attempt int) int {
	// CRITICAL: Reset Cobra state before re-execution to prevent flag pollution
	// from the previous attempt. Without this, flags may carry over incorrectly.
	rootCmd.ResetFlags()
	// Re-register persistent flags after ResetFlags() clears them
	registerPersistentFlags()
	// Resolve cached/env feature flags before Cobra parses the command. This is
	// required for default-off commands: help and command lookup both happen
	// before PersistentPreRunE.
	if !headlessLedgerReadRequested(args) {
		initFeatureFlags(rootCmd)
		syncFeatureGatedCommands(rootCmd)
	}
	rootCmd.SetArgs(args)
	preRunReached = false
	start := time.Now()

	// mark retry attempts to avoid telemetry double-counting
	if attempt > 0 {
		_ = os.Setenv("OX_FRICTION_RETRY", "1") // Setenv fails only on an invalid name
	}

	// Cobra shows help for non-runnable groups before validating arguments.
	// Validate those groups explicitly, using Cobra's flag parser so values
	// and arguments after -- retain their normal meaning. Leave explicit help
	// and runnable commands to Cobra, including the custom agent dispatcher.
	cmd, groupArgs, findErr := rootCmd.Find(args)
	var err error
	if findErr == nil && cmd.HasParent() && !cmd.Runnable() {
		cmd.InitDefaultHelpFlag()
		if flagErr := cmd.ParseFlags(groupArgs); flagErr == nil {
			help, _ := cmd.Flags().GetBool("help")
			if !help {
				err = cobra.NoArgs(cmd, cmd.Flags().Args())
			}
		}
	}
	if err == nil {
		cmd, err = rootCmd.ExecuteC()
	}
	unknownCommand := err != nil && strings.HasPrefix(err.Error(), `unknown command "`)
	if unknownCommand {
		err = fmt.Errorf("%w\n\nRun '%s --help' for usage", err, cmd.CommandPath())
	}
	if err != nil && !preRunReached && !headlessLedgerReadRequested(args) {
		// Cobra rejected the invocation before ox built its context. Give
		// usage telemetry one, so the mistake is counted like any failure.
		err = errkind.WithDetail(errkind.Usage, cobraRejection(err), err)
		cliCtx = &cli.Context{Cmd: cmd, CommandStartTime: start, Ctx: context.Background()}
	}
	if cliCtx != nil {
		cliCtx.Err = err
	}
	if err == nil {
		return 0
	}

	// Commands that have already rendered an error envelope may return a
	// typed exit-code error (conversation and ledger read commands use this path to
	// surface usage_error as exit 2 without going through the default
	// error printer or friction recovery). RunE writes the envelope to
	// stdout before returning; here we only need to honor the code.
	var commandExit *commandExitError
	if errors.As(err, &commandExit) {
		return commandExit.ExitCode
	}
	if errors.Is(err, tea.ErrInterrupted) {
		fmt.Fprintln(os.Stderr, "Interrupted.")
		return 130
	}
	if headlessLedgerReadRequested(args) {
		// Cobra flag parsing can fail before RunE. Keep even that path out
		// of friction recovery, which sends daemon events and can retry.
		if cmd, _, _ := rootCmd.Find(args); cmd == gitCredentialHelperCmd {
			return 2 // Git helpers must keep their protocol output silent.
		}
		return writeReadSyncUsageError(rootCmd, args)
	}

	// try friction recovery
	if frictionEngine == nil {
		printError(err)
		return 1
	}

	result := frictionEngine.Handle(args, err)
	if result == nil {
		printError(err)
		return 1
	}

	// send friction event to daemon for analytics (fire-and-forget)
	sendFrictionEvent(result.Event)

	// Friction's fuzzy candidates are full command paths, so a nested typo
	// such as "session lsit" can suggest an unrelated root command. Use the
	// group's own subcommands for guesses, retaining curated catalog remaps.
	if unknownCommand && cmd.HasParent() && (!cmd.Runnable() || cmd.HasSubCommands()) &&
		(result.Suggestion == nil || result.Suggestion.Type == friction.SuggestionLevenshtein) {
		printError(err)
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2 // Cobra's default edit distance
		}
		for _, suggestion := range cmd.SuggestionsFor(cmd.Flags().Arg(0)) {
			fmt.Fprintf(os.Stderr, "Did you mean '%s %s'?\n", cmd.CommandPath(), suggestion)
		}
		return 1
	}

	// determine output mode
	jsonMode := cfg != nil && cfg.JSON

	// emit correction/suggestion for learning
	result.Emit(jsonMode || agentx.IsAgentContext())

	// auto-execute if high confidence and within retry limit
	if result.AutoExecute && attempt < maxFrictionRetries {
		return executeWithFrictionRecovery(result.CorrectedArgs, attempt+1)
	}

	// no auto-execute - show the original error
	printError(err)
	return 1
}

// printError prints an error message with styling if appropriate.
func printError(err error) {
	if !cli.IsSilent(err) {
		fmt.Fprintf(os.Stderr, "%s %s\n", cli.Styles.Error.Render("Error:"), err)
	}
}
