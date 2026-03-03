package main

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/joho/godotenv"
	"github.com/mattn/go-isatty"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/uxfriction"
	"github.com/sageox/ox/pkg/agentx"

	// registers all supported agents for detection
	_ "github.com/sageox/ox/pkg/agentx/setup"
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

// stripWg is signaled when the ANSI-stripping goroutine finishes flushing.
var stripWg sync.WaitGroup

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
		os.Setenv("NO_COLOR", "1")
	}

	// When stdout/stderr are not a TTY (piped, captured by agent, etc.),
	// intercept with ANSI-stripping pipes. Lipgloss v2 beta always emits ANSI
	// via Style.Render() regardless of NO_COLOR — the colorprofile.Writer is
	// the intended stripping layer, but ~300 call sites use
	// fmt.Print(style.Render(...)) which bypasses it. These pipes catch
	// everything globally for both streams.
	if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		os.Setenv("NO_COLOR", "1") // keep for libraries that do check it

		pr, pw, err := os.Pipe()
		if err == nil {
			realStdout := os.Stdout
			os.Stdout = pw

			stripWg.Add(1)
			go func() {
				defer stripWg.Done()
				io.Copy(&ansiStripper{realStdout}, pr) //nolint:errcheck // best-effort
			}()
		}
	}
	if !isatty.IsTerminal(os.Stderr.Fd()) && !isatty.IsCygwinTerminal(os.Stderr.Fd()) {
		pr, pw, err := os.Pipe()
		if err == nil {
			realStderr := os.Stderr
			os.Stderr = pw

			stripWg.Add(1)
			go func() {
				defer stripWg.Done()
				io.Copy(&ansiStripper{realStderr}, pr) //nolint:errcheck // best-effort
			}()
		}
	}

	// initialize friction handling for "did you mean?" suggestions
	// must happen after all commands are registered via init() in other files
	initFriction(rootCmd)
}

func main() {
	// load .env files if present (silently ignore if not found)
	// order: .env.local (highest priority), .env (base config)
	// supports FEATURE_CLOUD, FEATURE_AUTH, SAGEOX_API, etc.
	_ = godotenv.Load(".env.local")
	_ = godotenv.Load(".env")

	args := os.Args[1:]
	exitCode := executeWithFrictionRecovery(args, 0)

	// close pipe writers so ANSI-stripping goroutines see EOF and flush
	os.Stdout.Close()
	os.Stderr.Close()
	stripWg.Wait()

	os.Exit(exitCode)
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
	rootCmd.SetArgs(args)

	// mark retry attempts to avoid telemetry double-counting
	if attempt > 0 {
		os.Setenv("OX_FRICTION_RETRY", "1")
	}

	err := rootCmd.Execute()
	if err == nil {
		return 0
	}

	// try friction recovery
	if frictionHandler == nil {
		printError(err)
		return 1
	}

	result := frictionHandler.HandleWithAutoExecute(args, err)
	if result == nil {
		printError(err)
		return 1
	}

	// send friction event to daemon for analytics (fire-and-forget)
	sendFrictionEvent(result.Event)

	// determine output mode
	jsonMode := cfg != nil && cfg.JSON

	// emit correction/suggestion for learning
	uxfriction.EmitCorrection(result, jsonMode)

	// auto-execute if high confidence and within retry limit
	if result.Action == uxfriction.ActionAutoExecute && attempt < maxFrictionRetries {
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
