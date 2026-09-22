package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sageox/ox/internal/cli"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	localtrace "github.com/sageox/ox/internal/trace"
	"github.com/sageox/ox/internal/trace/receiver"
	"github.com/spf13/cobra"
)

var sessionTraceCmd = &cobra.Command{
	Use: "trace", Short: "Capture Claude Code traces locally (experimental)",
	Long: `Capture Claude Code traces in a local receiver, separately from session recording.

FEATURE_TRACE=1 exposes these experimental commands. enable saves a machine-local
opt-in and starts the receiver; SessionStart hooks restart it while opted in,
even without FEATURE_TRACE. disable clears the opt-in and stops the receiver.

Claude Code must separately be launched with exporter environment variables.
These commands do not edit Claude Code settings or upload traces to the Ledger.
Start the receiver before Claude Code to avoid missing early startup events.`,
	// Local capture works without login, project initialization, the sync daemon,
	// or an OTLP exporter for ox itself. In particular, serve must not start those.
	PersistentPreRunE: func(_ *cobra.Command, _ []string) error { return nil },
}

var sessionTraceEnableCmd = &cobra.Command{
	Use: "enable", Short: "Save local opt-in and start the trace receiver", Args: cobra.NoArgs,
	RunE: runSessionTraceEnable,
}
var sessionTraceStatusCmd = &cobra.Command{
	Use: "status", Short: "Show receiver and local exporter configuration", Args: cobra.NoArgs,
	RunE: runSessionTraceStatus,
}
var sessionTraceDisableCmd = &cobra.Command{
	Use: "disable", Short: "Clear local opt-in and stop the trace receiver", Args: cobra.NoArgs,
	RunE: runSessionTraceDisable,
}
var sessionTraceServeCmd = &cobra.Command{
	Use: "serve", Short: "Run the trace receiver in the foreground", Hidden: true, Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		port, err := traceCommandPort(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return localtrace.Run(ctx, port, pruneTraceSpool)
	},
}

func init() {
	sessionTraceEnableCmd.Flags().Int("port", localtrace.DefaultPort, "Loopback receiver port")
	sessionTraceServeCmd.Flags().Int("port", localtrace.DefaultPort, "Loopback receiver port")
	sessionTraceDisableCmd.Flags().Bool("purge", false, "Also delete locally captured traces")
	sessionTraceCmd.AddCommand(sessionTraceEnableCmd, sessionTraceStatusCmd, sessionTraceDisableCmd, sessionTraceServeCmd)
}

// Process seams keep command and hook failure tests isolated from real receivers.
var (
	traceEnsureRunning = localtrace.EnsureRunningIf
	traceStop          = localtrace.Stop
	tracePurge         = localtrace.Purge
	traceHealth        = localtrace.Health
)

func traceConfig() (*config.UserConfig, int, error) {
	cfg, err := config.LoadUserConfig()
	if err != nil {
		return nil, 0, fmt.Errorf("load trace opt-in: %w", err)
	}
	if cfg == nil {
		cfg = &config.UserConfig{}
	}
	port := localtrace.DefaultPort
	if cfg.Trace != nil && cfg.Trace.Port != 0 {
		port = cfg.Trace.Port
	}
	return cfg, port, nil
}

func traceCommandPort(cmd *cobra.Command) (int, error) {
	_, port, err := traceConfig()
	if err != nil {
		return 0, err
	}
	if cmd.Flags().Changed("port") {
		port, _ = cmd.Flags().GetInt("port")
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("trace receiver port must be between 1 and 65535")
	}
	return port, nil
}

func traceOptedIn(port int) bool {
	cfg, configured, err := traceConfig()
	return err == nil && cfg.Trace != nil && cfg.Trace.Enabled && configured == port
}

func runSessionTraceEnable(cmd *cobra.Command, _ []string) error {
	cfg, oldPort, err := traceConfig()
	if err != nil {
		return err
	}
	port, err := traceCommandPort(cmd)
	if err != nil {
		return err
	}
	if cfg.Trace != nil && cfg.Trace.Enabled && oldPort >= 1 && oldPort <= 65535 && oldPort != port {
		return fmt.Errorf("disable the trace receiver before changing its port")
	}
	cfg.Trace = &config.TraceConfig{Enabled: true, Port: port}
	if err := config.SaveUserConfig(cfg); err != nil {
		return err
	}
	if err := traceEnsureRunning(cmd.Context(), port, func() bool { return traceOptedIn(port) }); err != nil {
		return fmt.Errorf("trace opt-in saved, but receiver did not start: %w", err)
	}
	return runSessionTraceStatus(cmd, nil)
}

type sessionTraceStatus struct {
	Enabled               bool                    `json:"enabled"`
	Running               bool                    `json:"running"`
	Port                  int                     `json:"port"`
	SpoolPath             string                  `json:"spool_path"`
	Sessions              int                     `json:"sessions"`
	Bytes                 int64                   `json:"bytes"`
	LastReceiptAt         *time.Time              `json:"last_receipt_at,omitempty"`
	LastReceiptAgeSeconds *int64                  `json:"last_receipt_age_seconds,omitempty"`
	Exporter              localtrace.ExportStatus `json:"exporter"`
	Warnings              []string                `json:"warnings,omitempty"`
}

func readSessionTraceStatus(ctx context.Context, projectRoot string, prune bool) (sessionTraceStatus, error) {
	cfg, port, err := traceConfig()
	if err != nil {
		return sessionTraceStatus{}, err
	}
	status := sessionTraceStatus{Enabled: cfg.Trace != nil && cfg.Trace.Enabled, Port: port,
		SpoolPath: localtrace.SpoolDir(), Exporter: localtrace.InspectExport(projectRoot, port)}
	if prune {
		if err := pruneTraceSpool(); err != nil {
			status.Warnings = append(status.Warnings, "retention skipped: "+err.Error())
		}
	}
	healthCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := traceHealth(healthCtx, port); err == nil {
		status.Running = true
	} else if status.Enabled {
		status.Warnings = append(status.Warnings, "receiver unavailable: "+err.Error())
	}
	stats, err := receiver.Stats(status.SpoolPath)
	if err != nil {
		return status, fmt.Errorf("inspect trace spool: %w", err)
	}
	status.Sessions, status.Bytes = stats.Sessions, stats.Bytes
	if !stats.LastSeen.IsZero() {
		status.LastReceiptAt = &stats.LastSeen
		seconds := max(int64(0), int64(time.Since(stats.LastSeen).Seconds()))
		status.LastReceiptAgeSeconds = &seconds
	}
	return status, nil
}

func runSessionTraceStatus(cmd *cobra.Command, _ []string) error {
	status, err := readSessionTraceStatus(cmd.Context(), findGitRoot(), true)
	if err != nil {
		return err
	}
	if traceJSONOutput(cmd) {
		return cli.PrintJSONTo(cmd.OutOrStdout(), status)
	}
	return printSessionTraceStatus(cmd.OutOrStdout(), status)
}

func printSessionTraceStatus(w io.Writer, status sessionTraceStatus) error {
	fmt.Fprintf(w, "Trace opt-in: %t\nReceiver listening: %t (127.0.0.1:%d)\nLocal traces: %s\nSessions: %d; bytes: %d\n",
		status.Enabled, status.Running, status.Port, status.SpoolPath, status.Sessions, status.Bytes)
	if status.LastReceiptAgeSeconds != nil {
		fmt.Fprintf(w, "Last receipt: %ds ago\n", *status.LastReceiptAgeSeconds)
	}
	fmt.Fprintf(w, "Local exporter configuration ready: %t\n", status.Exporter.Ready)
	for _, warning := range append(status.Warnings, status.Exporter.Issues...) {
		fmt.Fprintf(w, "Warning: %s\n", warning)
	}
	for _, note := range status.Exporter.Notes {
		fmt.Fprintln(w, note)
	}
	if !status.Exporter.Ready {
		fmt.Fprintf(w, "\nLaunch Claude Code from your shell with:\n%s\n", traceLaunchExample(status.Port))
	}
	return nil
}

func traceLaunchExample(port int) string {
	return fmt.Sprintf(`CLAUDE_CODE_ENABLE_TELEMETRY=1 CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 \
OTEL_TRACES_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=none \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:%d claude`, port)
}

func runSessionTraceDisable(cmd *cobra.Command, _ []string) error {
	cfg, port, err := traceConfig()
	if err != nil {
		return err
	}
	// Stop discovers the actual receiver from private process state. A malformed
	// preference must never trap a coworker in enabled capture.
	if port < 1 || port > 65535 {
		port = localtrace.DefaultPort
	}
	// Clear first: a SessionStart racing the stop must not respawn the receiver.
	cfg.Trace = nil
	if err := config.SaveUserConfig(cfg); err != nil {
		return err
	}
	purge, _ := cmd.Flags().GetBool("purge")
	if purge {
		err = tracePurge(cmd.Context(), port)
	} else {
		err = traceStop(cmd.Context(), port)
	}
	if err != nil {
		return fmt.Errorf("trace opt-in cleared, but receiver cleanup failed: %w", err)
	}
	if traceJSONOutput(cmd) {
		return cli.PrintJSONTo(cmd.OutOrStdout(), map[string]bool{"enabled": false, "purged": purge})
	}
	if purge {
		fmt.Fprintln(cmd.OutOrStdout(), "Trace receiver disabled; local traces deleted.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Trace receiver disabled; local traces retained.")
	}
	return nil
}

func startTraceReceiverForHook(hook *HookContext) {
	if hook.AgentType != "claude-code" {
		return
	}
	cfg, port, err := traceConfig()
	if err != nil {
		slog.Debug("trace hook config unavailable", "error", err)
		return
	}
	if cfg.Trace == nil || !cfg.Trace.Enabled {
		return
	}
	// Bounded readiness helps the first model request; it is not a guarantee
	// against telemetry exported earlier in Claude Code's own startup.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := traceEnsureRunning(ctx, port, func() bool { return traceOptedIn(port) }); err != nil {
		slog.Debug("trace hook receiver unavailable", "error", err)
	}
}

func pruneTraceSpool() error {
	protected, err := session.UnfinalizedNativeSessionIDs()
	if err != nil {
		return err
	}
	return receiver.Prune(localtrace.SpoolDir(), time.Now(), protected)
}

func traceJSONOutput(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	if !v {
		v, _ = cmd.Root().PersistentFlags().GetBool("json")
	}
	return v
}
