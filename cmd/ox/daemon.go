package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ledger"
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
			fmt.Println("Daemon is already running")
			return nil
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

		client := daemon.NewClient()
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
			client := daemon.NewClient()
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
		if err := startDaemonBackground(ledgerPath); err != nil {
			return err
		}

		fmt.Println("Daemon restarted")
		return nil
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		verbose, _ := cmd.Flags().GetBool("verbose")

		if !daemon.IsRunning() {
			if daemon.IsStarting() {
				if jsonOutput {
					fmt.Println(`{"running": false, "starting": true}`)
				} else {
					fmt.Print(daemon.FormatStarting())
				}
			} else {
				if jsonOutput {
					fmt.Println(`{"running": false}`)
				} else {
					fmt.Print(daemon.FormatNotRunning(config.IsInitializedInCwd()))
				}
			}
			return nil
		}

		client := daemon.NewClient()
		status, err := client.Status()
		if err != nil {
			return fmt.Errorf("failed to get status: %w", err)
		}

		if jsonOutput {
			fmt.Printf(`{"running": true, "pid": %d, "ledger_path": %q, "last_sync": %q}`,
				status.Pid, status.LedgerPath, status.LastSync.Format(time.RFC3339))
			fmt.Println()
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

		logPath := daemon.LogPath()

		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			fmt.Println("No daemon logs found")
			return nil
		}

		tailArgs := []string{"-n", fmt.Sprintf("%d", lines)}
		if follow {
			tailArgs = append(tailArgs, "-f")
		}
		tailArgs = append(tailArgs, logPath)

		tailCmd := exec.Command("tail", tailArgs...)
		tailCmd.Stdout = os.Stdout
		tailCmd.Stderr = os.Stderr
		return tailCmd.Run()
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

			client := daemon.NewClient()
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
	daemonStatusCmd.Flags().BoolP("verbose", "v", false, "show detailed sync history")
	daemonLogsCmd.Flags().IntP("lines", "n", 50, "number of lines to show")
	daemonLogsCmd.Flags().BoolP("follow", "f", false, "follow log output")
	daemonKillCmd.Flags().Bool("all", false, "kill all running ox daemons")

	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonListCmd)
	daemonCmd.AddCommand(daemonKillCmd)
}

// runDaemonForeground runs the daemon in the foreground.
func runDaemonForeground(ledgerPath string) error {
	cfg := daemon.DefaultConfig()
	cfg.LedgerPath = ledgerPath
	cfg.ProjectRoot = findGitRoot() // required for team context syncing

	// use INFO level logging to stderr (which gets redirected to log file)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	d := daemon.New(cfg, logger)
	return d.Start()
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

	// start daemon process
	// NOTE: No setsid/detach — Claude manages the daemon process lifecycle.
	cmd := exec.Command(exe, "daemon", "start", "--foreground")
	cmd.Stdout = logFile
	cmd.Stderr = logFile

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
		cli.PrintHint("  Daemon starting (may be delayed due to recent restarts)")
	}

	cli.PrintHint(fmt.Sprintf("  Logs: %s", shortenPath(logPath)))
	cli.PrintHint("  Status: ox daemon status")
	return nil
}
