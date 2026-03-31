package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
	"github.com/sageox/ox/internal/version"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background sync daemon",
	Long: `The SageOx daemon runs in the background to sync your ledger repository.

It handles:
  - Automatic git push when ledger changes are detected
  - Periodic git pull to fetch remote changes
  - Debouncing rapid changes to avoid excessive commits`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the background daemon",
	Long:  "Starts the SageOx daemon in the background for automatic ledger sync.",
	RunE: func(cmd *cobra.Command, args []string) error {
		foreground, _ := cmd.Flags().GetBool("foreground")

		// check if already running
		if daemon.IsRunning() {
			cli.PrintInfo("Daemon is already running")
			cli.PrintHint("  Run 'ox daemon status' to see details")
			return nil
		}

		// kill any stale daemon for this workspace before starting a new one
		if err := daemon.KillStaleDaemonForCurrentWorkspace(); err != nil {
			return fmt.Errorf("failed to stop stale daemon: %w", err)
		}

		// resolve ledger path (only if it's actually a git repo)
		ledgerPath, err := ledger.DefaultPath()
		if err != nil {
			ledgerPath = "" // no ledger configured
		} else if !isGitRepo(ledgerPath) {
			ledgerPath = "" // ledger path exists but not cloned yet
		}

		if foreground {
			// run in foreground (for debugging)
			return runDaemonForeground(ledgerPath)
		}

		// start in background
		return startDaemonBackground(ledgerPath)
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the background daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !daemon.IsRunning() {
			fmt.Println("Daemon is not running")
			return nil
		}

		client := daemon.NewClientForCurrentRepo()
		if err := client.Stop(); err != nil {
			return fmt.Errorf("failed to stop daemon: %w", err)
		}

		fmt.Println("Daemon stopped")
		return nil
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the background daemon",
	Long:  "Stops the running daemon and starts a new one.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// stop if running
		if daemon.IsRunning() {
			client := daemon.NewClientForCurrentRepo()
			if err := client.Stop(); err != nil {
				return fmt.Errorf("failed to stop daemon: %w", err)
			}
			// wait for daemon to fully stop (max 2 seconds)
			stopped := false
			for i := 0; i < 20; i++ {
				if !daemon.IsRunning() {
					stopped = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !stopped {
				return fmt.Errorf("daemon did not stop within timeout")
			}
		}

		// resolve ledger path (only if it's actually a git repo)
		ledgerPath, err := ledger.DefaultPath()
		if err != nil {
			ledgerPath = "" // no ledger configured
		} else if !isGitRepo(ledgerPath) {
			ledgerPath = "" // ledger path exists but not cloned yet
		}

		// start in background
		return startDaemonBackground(ledgerPath)
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		verbose, _ := cmd.Flags().GetBool("verbose")

		if !daemon.IsRunning() {
			switch daemon.GetState() {
			case daemon.DaemonStateStarting:
				if jsonOutput {
					fmt.Println(`{"running": false, "starting": true}`)
				} else {
					fmt.Print(daemon.FormatStarting())
				}
			case daemon.DaemonStateStuck:
				if jsonOutput {
					fmt.Println(`{"running": false, "stuck": true}`)
				} else {
					fmt.Print(daemon.FormatStuck())
				}
			default:
				if jsonOutput {
					fmt.Println(`{"running": false}`)
				} else {
					fmt.Print(daemon.FormatNotRunning(config.IsInitializedInCwd()))
				}
			}
			return nil
		}

		// status is human-initiated, use longer timeout than default 50ms IPC;
		// retry once with even longer timeout if daemon is busy (initial sync, GC)
		client := daemon.NewClientForCurrentRepoWithTimeout(500 * time.Millisecond)
		status, err := client.Status()
		if err != nil && isTimeoutErr(err) {
			client = daemon.NewClientForCurrentRepoWithTimeout(2 * time.Second)
			status, err = client.Status()
		}
		if err != nil {
			if isTimeoutErr(err) {
				return fmt.Errorf("daemon is busy (likely syncing), try again in a moment")
			}
			return fmt.Errorf("failed to get status: %w", err)
		}

		if jsonOutput {
			cliVer := version.Version
			jsonStatus := daemon.BuildStatusJSON(status, cliVer)
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(jsonStatus); err != nil {
				return fmt.Errorf("failed to marshal status: %w", err)
			}
			fmt.Print(buf.String())
			return nil
		}

		// always fetch history for sparkline; verbose adds sync history table
		history, _ := client.SyncHistory()
		cliVer := version.Version
		if verbose {
			fmt.Print(daemon.FormatStatusVerbose(status, history, cliVer))
		} else {
			fmt.Print(daemon.FormatStatusWithSparkline(status, history, cliVer))
		}

		return nil
	},
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show daemon logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		lines, _ := cmd.Flags().GetInt("lines")
		follow, _ := cmd.Flags().GetBool("follow")
		showPath, _ := cmd.Flags().GetBool("path")
		all, _ := cmd.Flags().GetBool("all")

		logPath := daemon.LogPath()

		if showPath {
			fmt.Println(logPath)
			return nil
		}

		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			fmt.Println("No daemon logs found")
			return nil
		}

		var tailArgs []string
		if all {
			tailArgs = []string{"-n", "+1"} // entire file from line 1
		} else {
			tailArgs = []string{"-n", fmt.Sprintf("%d", lines)}
		}
		if follow {
			tailArgs = append(tailArgs, "-f")
		}
		tailArgs = append(tailArgs, logPath)

		tailCmd := exec.Command("tail", tailArgs...)
		tailCmd.Stderr = os.Stderr

		// colorize when output is a terminal
		colorize := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
		if !colorize {
			tailCmd.Stdout = os.Stdout
			return tailCmd.Run()
		}

		stdout, err := tailCmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create pipe: %w", err)
		}

		if err := tailCmd.Start(); err != nil {
			return fmt.Errorf("failed to start tail: %w", err)
		}

		cli.StreamFormattedLogs(stdout, os.Stdout)
		return tailCmd.Wait()
	},
}

var daemonListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all running ox daemons",
	Long:  "Shows all ox daemons running on this host across all workspaces.",
	RunE: func(cmd *cobra.Command, args []string) error {
		daemons, err := daemon.ListRunningDaemons()
		if err != nil {
			return fmt.Errorf("failed to list daemons: %w", err)
		}

		fmt.Print(daemon.FormatDaemonList(daemons))
		return nil
	},
}

var daemonKillCmd = &cobra.Command{
	Use:   "kill",
	Short: "Kill daemon(s)",
	Long:  "Kill one or more ox daemons. Use --all to kill all running daemons.",
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")

		if !all {
			// kill current workspace daemon
			if !daemon.IsRunning() {
				fmt.Println("No daemon running for current workspace")
				return nil
			}

			client := daemon.NewClientForCurrentRepo()
			if err := client.Stop(); err != nil {
				return fmt.Errorf("failed to stop daemon: %w", err)
			}

			fmt.Println("Daemon stopped")
			return nil
		}

		// kill all daemons
		killed, err := daemon.KillAllDaemons()
		if err != nil {
			return fmt.Errorf("failed to kill daemons: %w", err)
		}

		if len(killed) == 0 {
			fmt.Println("No daemons were running")
			return nil
		}

		for _, d := range killed {
			fmt.Printf("Stopped daemon for %s (PID %d)\n",
				d.WorkspacePath, d.PID)
		}
		fmt.Printf("\n%d daemon(s) stopped\n", len(killed))
		return nil
	},
}

func init() {
	daemonStartCmd.Flags().Bool("foreground", false, "run in foreground (for debugging)")
	daemonStartCmd.Flags().String("repo", "", "repo name (for ps identification)")
	daemonStartCmd.Flags().MarkHidden("repo")
	daemonStatusCmd.Flags().BoolP("verbose", "v", false, "show detailed sync history")
	daemonLogsCmd.Flags().IntP("lines", "n", 50, "number of lines to show")
	daemonLogsCmd.Flags().BoolP("follow", "f", false, "follow log output")
	daemonLogsCmd.Flags().Bool("path", false, "print log file path and exit")
	daemonLogsCmd.Flags().Bool("all", false, "show all log lines")
	daemonKillCmd.Flags().Bool("all", false, "kill all running ox daemons")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonListCmd)
	daemonCmd.AddCommand(daemonKillCmd)

	// hidden alias: 'ox daemon log' → 'ox daemon logs'
	daemonLogCmd := &cobra.Command{
		Use:    "log",
		Hidden: true,
		RunE:   daemonLogsCmd.RunE,
	}
	daemonLogCmd.Flags().IntP("lines", "n", 50, "number of lines to show")
	daemonLogCmd.Flags().BoolP("follow", "f", false, "follow log output")
	daemonLogCmd.Flags().Bool("path", false, "print log file path and exit")
	daemonLogCmd.Flags().Bool("all", false, "show all log lines")
	daemonCmd.AddCommand(daemonLogCmd)
}

// runDaemonForeground runs the daemon in the foreground.
func runDaemonForeground(ledgerPath string) error {
	cfg := daemon.DefaultConfig()
	cfg.LedgerPath = ledgerPath
	cfg.ProjectRoot = findGitRoot() // required for team context syncing

	// default to INFO; OX_LOG_LEVEL=debug enables verbose output
	logLevel := slog.LevelInfo
	if os.Getenv("OX_LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	// include PID so multi-daemon scenarios and auto-restarts can be distinguished
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})).With("pid", os.Getpid())

	d := daemon.New(cfg, logger)
	if err := d.Start(); err != nil {
		return err
	}

	if !d.RestartRequested() {
		return nil
	}

	// version mismatch: re-exec with updated binary (same PID, same FDs)
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("self-restart: resolve executable: %w", err)
	}
	logger.Info("re-executing daemon with updated binary", "exe", exe)
	return syscall.Exec(exe, os.Args, os.Environ())
}

// startDaemonBackground starts the daemon as a background process.
func startDaemonBackground(ledgerPath string) error {
	// get the path to the current executable
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// create log directory
	logPath := daemon.LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// open log file
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	// build args with repo name for ps visibility
	args := []string{"daemon", "start", "--foreground"}
	if gitRoot := findGitRoot(); gitRoot != "" {
		if name := repotools.GetRepoName(gitRoot); name != "" {
			args = append(args, "--repo="+name)
		}
	}

	// start daemon process
	// NOTE: No setsid/detach — Claude manages the daemon process lifecycle.
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// set CWD to git root so daemon computes correct repo-based workspace ID
	// from .sageox/config.json (without this, subdirectory CWDs cause fallback
	// to path-based hashing, creating duplicate daemons per clone/worktree)
	if gitRoot := findGitRoot(); gitRoot != "" {
		cmd.Dir = gitRoot
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// don't wait for the process
	logFile.Close()

	cli.PrintSuccess(fmt.Sprintf("Daemon started (pid %d)", cmd.Process.Pid))

	// brief readiness poll (up to 2s) to confirm daemon is accepting IPC
	ready := false
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if daemon.IsRunning() {
			ready = true
			break
		}
	}
	if !ready {
		cli.PrintHint("  May be delayed due to recent restarts")
	}

	cli.PrintHint(fmt.Sprintf("  Logs: %s", logPath))
	cli.PrintHint("  Run 'ox daemon status' to see details")
	return nil
}

// isTimeoutErr returns true if the error is a socket I/O timeout.
func isTimeoutErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "i/o timeout")
}
