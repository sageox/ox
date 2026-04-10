package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/observability"
	"github.com/sageox/ox/internal/signature"
	"github.com/sageox/ox/internal/telemetry"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
)

// Context holds common dependencies for CLI commands.
// It provides centralized initialization and access to shared resources
// like configuration, logging, and telemetry.
type Context struct {
	// Config holds the loaded configuration
	Config *config.Config

	// Logger provides structured logging
	Logger *slog.Logger

	// Telemetry client for tracking command usage
	TelemetryClient *telemetry.Client

	// CommandStartTime tracks when the command started (for telemetry)
	CommandStartTime time.Time

	// Ctx provides the Go context for cancellation and timeouts
	Ctx context.Context
}

// NewContext creates a new CLI context from a Cobra command.
// This centralizes all the initialization logic that was previously
// scattered across PersistentPreRunE in root.go.
func NewContext(cmd *cobra.Command, args []string) (*Context, error) {
	cliCtx := &Context{
		CommandStartTime: time.Now(),
		Ctx:              cmd.Context(),
	}

	// if no context set, use background
	if cliCtx.Ctx == nil {
		cliCtx.Ctx = context.Background()
	}

	// load config from env vars
	cfg := config.Load()
	cliCtx.Config = cfg

	// override config with flags if they were explicitly set
	if cmd.Flags().Changed("verbose") {
		cfg.Verbose, _ = cmd.Flags().GetBool("verbose")
	}
	if cmd.Flags().Changed("quiet") {
		cfg.Quiet, _ = cmd.Flags().GetBool("quiet")
	}
	if cmd.Flags().Changed("json") {
		cfg.JSON, _ = cmd.Flags().GetBool("json")
	}
	if cmd.Flags().Changed("text") {
		cfg.Text, _ = cmd.Flags().GetBool("text")
	}
	if cmd.Flags().Changed("review") {
		cfg.Review, _ = cmd.Flags().GetBool("review")
	}
	if cmd.Flags().Changed("no-interactive") {
		cfg.NoInteractive, _ = cmd.Flags().GetBool("no-interactive")
	}

	// initialize logger
	logger.Init(cfg.Verbose)
	cliCtx.Logger = slog.Default()

	// initialize telemetry client (non-blocking, fire-and-forget)
	// uses auth.NewServerSessionID() for a per-invocation session ID
	// if in a project context, uses disk-based queue for persistence
	projectRoot := config.FindProjectRoot()
	if projectRoot != "" {
		cliCtx.TelemetryClient = telemetry.NewClient(auth.NewServerSessionID(), telemetry.WithProjectRoot(projectRoot))
	} else {
		cliCtx.TelemetryClient = telemetry.NewClient(auth.NewServerSessionID())
	}
	cliCtx.TelemetryClient.Start()

	// check signature and warn if unsigned/invalid (non-blocking)
	signature.CheckAndWarn()

	// set global output mode
	SetJSONMode(cfg.JSON)
	SetNoInteractive(cfg.NoInteractive)

	// initialize OTel tracing — one trace per command invocation
	// uses project endpoint if available, falls back to global endpoint
	apiEndpoint := ""
	if projectRoot != "" {
		apiEndpoint = endpoint.GetForProject(projectRoot)
	} else {
		// Non-project commands (login, init) use global endpoint.
		// See endpoint.Get() docs for why this is safe here.
		apiEndpoint = endpoint.Get()
	}
	// Resource-level attrs propagate to every span produced by this
	// process (the root command span and any HTTP child spans), so they
	// only need to be set once at init. Span-level attrs (set below via
	// SetCommandAttrs) are added on top to keep cmd.args close to the
	// command they describe.
	resourceAttrs := []attribute.KeyValue{
		attribute.String(observability.AttrOXVersion, version.Version),
		attribute.String(observability.AttrHostOS, runtime.GOOS),
		attribute.String(observability.AttrHostArch, runtime.GOARCH),
	}
	// AGENT_ENV is set by adapter hooks (claude-code, aider, gemini, ...)
	// to identify the AI coworker driving this invocation. Only attach it
	// when present so human-driven invocations stay clean.
	if env := os.Getenv("AGENT_ENV"); env != "" {
		resourceAttrs = append(resourceAttrs, attribute.String(observability.AttrAgentEnv, env))
	}
	if err := observability.Init(cliCtx.Ctx, "ox-cli", apiEndpoint, resourceAttrs...); err != nil {
		slog.Debug("otel init failed, continuing without tracing", "error", err)
	}

	// create root span for this command
	cmdPath := cmd.Name()
	if cmd.Parent() != nil && cmd.Parent().Name() != "ox" {
		cmdPath = cmd.Parent().Name() + " " + cmdPath
	}
	cliCtx.Ctx, _ = observability.StartCommand(cliCtx.Ctx, observability.CommandName(cmdPath))

	// Attach universal attributes to the root span: ox.version, host.os,
	// host.arch, ox.command.args (with values redacted). This is the only
	// place we have a clean handle on os.Args before any subcommand runs.
	// cli.exit_code is set later from main() after Execute returns, so it
	// is present even when PersistentPostRunE never runs (cobra skips
	// PostRunE on errors).
	observability.SetCommandAttrs(version.Version, os.Args[1:])

	return cliCtx, nil
}

// IsVerbose returns true if verbose logging is enabled
func (c *Context) IsVerbose() bool {
	return c.Config.Verbose
}

// IsQuiet returns true if quiet mode is enabled
func (c *Context) IsQuiet() bool {
	return c.Config.Quiet
}

// IsJSON returns true if JSON output mode is enabled
func (c *Context) IsJSON() bool {
	return c.Config.JSON
}

// IsText returns true if text output mode is enabled
func (c *Context) IsText() bool {
	return c.Config.Text
}

// IsReview returns true if review (security audit) mode is enabled
func (c *Context) IsReview() bool {
	return c.Config.Review
}

// IsNoInteractive returns true if non-interactive mode is enabled (CI or --no-interactive)
func (c *Context) IsNoInteractive() bool {
	return c.Config.NoInteractive
}

// TrackCommandCompletion tracks a successful command completion via telemetry
func (c *Context) TrackCommandCompletion(cmd *cobra.Command) {
	if c.TelemetryClient == nil {
		return
	}

	duration := time.Since(c.CommandStartTime)

	// build full command path (e.g., "agent prime" not just "prime")
	cmdPath := cmd.Name()
	if cmd.Parent() != nil && cmd.Parent().Name() != "ox" {
		cmdPath = cmd.Parent().Name() + " " + cmdPath
	}

	c.TelemetryClient.TrackCommand(cmdPath, duration, true, "")
}

// TrackCommandError tracks a command error via telemetry
func (c *Context) TrackCommandError(cmd *cobra.Command, err error) {
	if c.TelemetryClient == nil || cmd == nil {
		return
	}

	duration := time.Since(c.CommandStartTime)

	// build full command path
	cmdPath := cmd.Name()
	if cmd.Parent() != nil && cmd.Parent().Name() != "ox" {
		cmdPath = cmd.Parent().Name() + " " + cmdPath
	}

	// extract error code if available
	errorCode := "ERR_UNKNOWN"
	if err != nil {
		// use first 50 chars of error as code (simplified)
		errStr := err.Error()
		if len(errStr) > 50 {
			errStr = errStr[:50]
		}
		errorCode = errStr
	}

	c.TelemetryClient.TrackCommand(cmdPath, duration, false, errorCode)
}

// Shutdown performs cleanup operations.
// Should be called in PersistentPostRunE or deferred.
//
// Note: this intentionally does NOT shut down OTel tracing. PostRunE only
// runs on success — cobra skips it when RunE returns an error. To make
// cli.exit_code observable on both success and error paths, the OTel root
// span is ended from main() *after* Execute returns, where the final exit
// code is known. See cmd/ox/main.go.
func (c *Context) Shutdown() {
	if c.TelemetryClient != nil {
		c.TelemetryClient.Stop() // flush and shutdown (non-blocking, short timeout)
	}
}

// LogInfo logs an info-level message
func (c *Context) LogInfo(msg string, args ...any) {
	if c.Logger != nil {
		c.Logger.Info(msg, args...)
	}
}

// LogDebug logs a debug-level message
func (c *Context) LogDebug(msg string, args ...any) {
	if c.Logger != nil {
		c.Logger.Debug(msg, args...)
	}
}

// LogWarn logs a warning-level message
func (c *Context) LogWarn(msg string, args ...any) {
	if c.Logger != nil {
		c.Logger.Warn(msg, args...)
	}
}

// LogError logs an error-level message
func (c *Context) LogError(msg string, args ...any) {
	if c.Logger != nil {
		c.Logger.Error(msg, args...)
	}
}

// PrintSuccess prints a success message (unless quiet mode)
func (c *Context) PrintSuccess(msg string) {
	if !c.IsQuiet() && !c.IsJSON() {
		PrintSuccess(msg)
	}
}

// PrintWarning prints a warning message (unless quiet mode)
func (c *Context) PrintWarning(msg string) {
	if !c.IsQuiet() && !c.IsJSON() {
		PrintWarning(msg)
	}
}

// PrintError prints an error message
func (c *Context) PrintError(msg string) {
	if !c.IsJSON() {
		PrintError(msg)
	}
}

// PrintPreserved prints a preserved/unchanged message (unless quiet mode)
func (c *Context) PrintPreserved(msg string) {
	if !c.IsQuiet() && !c.IsJSON() {
		PrintPreserved(msg)
	}
}

// Printf prints a formatted message to stdout (respects quiet/json modes)
func (c *Context) Printf(format string, args ...any) {
	if !c.IsQuiet() && !c.IsJSON() {
		fmt.Printf(format, args...)
	}
}

// Println prints a message to stdout (respects quiet/json modes)
func (c *Context) Println(msg string) {
	if !c.IsQuiet() && !c.IsJSON() {
		fmt.Println(msg)
	}
}

// Fprintf writes formatted output to a writer (useful for stderr)
func (c *Context) Fprintf(w *os.File, format string, args ...any) {
	if !c.IsJSON() {
		fmt.Fprintf(w, format, args...)
	}
}
