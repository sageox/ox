package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/repotools"
)

// TryConnectOrDirect attempts to connect to the daemon.
// Returns nil if daemon is not running or unreachable.
// On transient connection failures, retries once with exponential backoff.
func TryConnectOrDirect() *Client {
	return TryConnectWithRetry(1, 100*time.Millisecond)
}

// TryConnectOrDirectForSync is like TryConnectOrDirect but uses longer timeouts for sync operations.
func TryConnectOrDirectForSync() *Client {
	return TryConnectForSyncWithRetry(1, 100*time.Millisecond)
}

// TryConnectOrDirectForCheckout is like TryConnectOrDirect but uses longer timeouts for checkout operations.
func TryConnectOrDirectForCheckout() *Client {
	return TryConnectForCheckoutWithRetry(1, 100*time.Millisecond)
}

// TryConnectWithRetry attempts to connect to the daemon with retry logic.
// On transient failures, retries up to maxRetries times with exponential backoff.
// initialDelay is the delay before the first retry.
func TryConnectWithRetry(maxRetries int, initialDelay time.Duration) *Client {
	client := NewClientForCurrentRepo()

	// first attempt
	if err := client.Ping(); err == nil {
		return client
	}

	// retry with exponential backoff
	delay := initialDelay
	for i := 0; i < maxRetries; i++ {
		time.Sleep(delay)

		if err := client.Ping(); err == nil {
			return client
		}

		// exponential backoff: 100ms -> 200ms -> 400ms ...
		delay *= 2
	}

	return nil
}

// TryConnectForSyncWithRetry is like TryConnectWithRetry but uses longer timeouts for sync operations.
func TryConnectForSyncWithRetry(maxRetries int, initialDelay time.Duration) *Client {
	client := NewClientForCurrentRepoWithTimeout(30 * time.Second)

	// first attempt
	if err := client.Ping(); err == nil {
		return client
	}

	// retry with exponential backoff
	delay := initialDelay
	for i := 0; i < maxRetries; i++ {
		time.Sleep(delay)

		if err := client.Ping(); err == nil {
			return client
		}

		delay *= 2
	}

	return nil
}

// TryConnectForCheckoutWithRetry is like TryConnectWithRetry but uses longer timeouts for checkout operations.
func TryConnectForCheckoutWithRetry(maxRetries int, initialDelay time.Duration) *Client {
	client := NewClientForCurrentRepoWithTimeout(60 * time.Second)

	// first attempt
	if err := client.Ping(); err == nil {
		return client
	}

	// retry with exponential backoff
	delay := initialDelay
	for i := 0; i < maxRetries; i++ {
		time.Sleep(delay)

		if err := client.Ping(); err == nil {
			return client
		}

		delay *= 2
	}

	return nil
}

// ShouldUseDaemon returns true if we should attempt to use the daemon.
// Returns true if the daemon is currently running.
func ShouldUseDaemon() bool {
	return IsRunning()
}

// EnsureDaemon ensures the daemon is running, starting it if necessary.
// Claude manages the daemon process lifecycle (launching and killing), so
// setsid/detach is no longer needed. The daemon relies on its inactivity
// timeout to self-exit when no heartbeats arrive.
// Returns nil on success (daemon is running), or an error if it couldn't be started.
// This is a no-op if daemon is already running or disabled via SAGEOX_DAEMON=false.
//
// The function waits up to 2 seconds for the daemon to become available after starting.
func EnsureDaemon() error {
	if IsDaemonDisabled() {
		return nil
	}
	return ensureDaemonInternal()
}

// EnsureDaemonAttached is an alias for EnsureDaemon.
// Previously started the daemon without setsid (attached to caller's process group).
// Now that Claude manages the daemon lifecycle, setsid is removed entirely and
// both functions behave identically.
func EnsureDaemonAttached() error {
	if IsDaemonDisabled() {
		return nil
	}
	return ensureDaemonInternal()
}

// ensureDaemonInternal starts the daemon if it's not already running.
func ensureDaemonInternal() error {
	if IsRunning() {
		return nil // already running on new (repo-based) socket
	}

	// A daemon may be alive but not yet listening on its socket (startup/throttle).
	// Wait for it rather than killing it. Skip the wait if it's stuck (past the
	// startup window) — that process needs to be killed and restarted.
	if state := GetState(); state == DaemonStateStarting {
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			switch GetState() {
			case DaemonStateRunning:
				return nil
			case DaemonStateStarting:
				continue
			default:
				// stopped or stuck — fall through to kill+restart
			}
			break
		}
	}

	// Kill any stale daemon for this workspace before starting a new one.
	// Reached when: daemon stopped, daemon stuck (past startup window), or
	// wait loop expired. IPC stop will likely fail; SIGTERM is the fallback.
	if err := KillStaleDaemon(CurrentWorkspaceID()); err != nil {
		return fmt.Errorf("failed to stop stale daemon: %w", err)
	}

	// migration: stop old path-based daemon if one exists on a legacy socket
	stopLegacyDaemon()

	// get the path to the current executable
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// create log directory
	logPath := LogPath()
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
	// The daemon relies on inactivity timeout to self-exit when claude dies.
	cmd := exec.Command(exe, buildDaemonArgs(resolveRepoName())...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// set CWD to project root so daemon computes correct repo-based workspace ID
	// from .sageox/config.json (without this, subdirectory CWDs cause fallback
	// to path-based hashing, creating duplicate daemons per clone/worktree)
	if root := findProjectRootForDaemon(); root != "" {
		cmd.Dir = root
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// don't wait for the process
	logFile.Close()

	// wait for daemon to be ready (up to 2 seconds)
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if IsRunning() {
			return nil
		}
	}

	return fmt.Errorf("daemon started but not responding")
}

// KillStaleDaemon kills any existing daemon for the given workspace before starting a new one.
// Escalation: IPC stop (graceful) → SIGTERM (forceful) → error.
// Returns nil if the stale daemon was successfully stopped or none existed.
// Returns an error if the stale daemon could not be stopped (caller should not start a new one).
// KillStaleDaemon stops a daemon by workspace ID using the full cleanup flow:
// IPC stop → wait → SIGTERM (with PID-reuse guard) → wait → cleanup registry/files.
func KillStaleDaemon(workspaceID string) error {
	reg, err := LoadRegistry()
	if err != nil {
		slog.Debug("failed to load registry for stale daemon check", "error", err)
		return nil
	}

	info := reg.FindByWorkspaceID(workspaceID)
	if info == nil {
		return nil // no existing entry
	}

	// check if process is alive (signal 0)
	if err := signalProcess(info.PID, 0); err != nil {
		// process is dead — clean up stale registry entry and files
		slog.Debug("stale daemon entry (process dead), cleaning up", "workspace_id", workspaceID, "pid", info.PID)
		_ = reg.Unregister(workspaceID)
		_ = os.Remove(PidPathForWorkspace(workspaceID))
		_ = os.Remove(info.SocketPath)
		return nil
	}

	// process is alive — try graceful IPC stop first (2s timeout for busy daemons)
	client := &Client{socketPath: info.SocketPath, timeout: 2 * time.Second}
	if err := client.Ping(); err == nil {
		slog.Info("stopping existing daemon via IPC", "workspace_id", workspaceID, "pid", info.PID)
		if err := client.Stop(); err != nil {
			slog.Warn("IPC stop failed, falling back to SIGTERM", "workspace_id", workspaceID, "pid", info.PID, "error", err)
		}
		if WaitForProcessExit(info.PID, 5*time.Second) {
			_ = reg.Unregister(workspaceID)
			_ = os.Remove(PidPathForWorkspace(workspaceID))
			_ = os.Remove(info.SocketPath)
			return nil
		}
		slog.Warn("daemon did not exit after IPC stop, escalating to SIGTERM", "workspace_id", workspaceID, "pid", info.PID)
	}

	// IPC unreachable or stop didn't work — verify this is actually an ox daemon
	// before sending SIGTERM (guards against PID reuse by an unrelated process)
	if !isOxDaemonProcess(info.PID) {
		slog.Info("PID is not an ox daemon, cleaning up stale entry", "workspace_id", workspaceID, "pid", info.PID)
		_ = reg.Unregister(workspaceID)
		_ = os.Remove(PidPathForWorkspace(workspaceID))
		_ = os.Remove(info.SocketPath)
		return nil
	}

	slog.Info("sending SIGTERM to stale daemon", "workspace_id", workspaceID, "pid", info.PID)
	if err := signalProcess(info.PID, sigTERM); err != nil {
		slog.Warn("failed to SIGTERM stale daemon", "workspace_id", workspaceID, "pid", info.PID, "error", err)
	}

	if WaitForProcessExit(info.PID, 2*time.Second) {
		_ = reg.Unregister(workspaceID)
		_ = os.Remove(PidPathForWorkspace(workspaceID))
		_ = os.Remove(info.SocketPath)
		return nil
	}
	return fmt.Errorf("stale daemon %d did not exit after SIGTERM", info.PID)
}

// WaitForProcessExit polls signal 0 until the process exits or timeout is reached.
// Returns true if the process exited, false if it's still alive after timeout.
func WaitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := signalProcess(pid, 0); err != nil {
			return true // process exited
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// KillStaleDaemonForCurrentWorkspace is the public entry point for pre-start kill.
// Called by `ox daemon start` CLI command.
func KillStaleDaemonForCurrentWorkspace() error {
	return KillStaleDaemon(CurrentWorkspaceID())
}

// buildDaemonArgs constructs command-line arguments for starting a daemon subprocess.
// Includes --repo flag for ps identification when repoName is non-empty.
func buildDaemonArgs(repoName string) []string {
	args := []string{"daemon", "start", "--foreground"}
	if repoName != "" {
		args = append(args, "--repo="+repoName)
	}
	return args
}

// resolveRepoName returns the human-readable repo name (e.g., "owner/repo") for the CWD.
func resolveRepoName() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return repotools.GetRepoName(cwd)
}

// findProjectRootForDaemon returns the project root for setting daemon CWD.
// Uses canonical config.FindProjectRoot; falls back to os.Getwd if no .sageox/ found.
func findProjectRootForDaemon() string {
	if root := config.FindProjectRoot(); root != "" {
		return root
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// stopLegacyDaemon stops a daemon running under the old path-based workspace ID.
// This handles migration from path-hash to repo_id-hash identity: when the new
// repo-based ID differs from the old path-based ID, we stop the old daemon so
// the new one can take over.
func stopLegacyDaemon() {
	legacyID := LegacyWorkspaceID()
	currentID := CurrentWorkspaceID()

	// same hash means no migration needed (non-initialized repo, or hash collision)
	if legacyID == currentID {
		return
	}

	legacySocket := SocketPathForWorkspace(legacyID)
	client := NewClientWithSocket(legacySocket)

	if err := client.Ping(); err != nil {
		// legacy daemon not running, clean up stale socket if present
		_ = os.Remove(legacySocket)
		return
	}

	slog.Info("stopping legacy path-based daemon", "legacy_id", legacyID, "new_id", currentID)

	if err := client.Stop(); err != nil {
		slog.Warn("failed to stop legacy daemon", "legacy_id", legacyID, "error", err)
		return
	}

	// brief wait for graceful shutdown
	time.Sleep(500 * time.Millisecond)

	// clean up stale socket if still present
	_ = os.Remove(legacySocket)
}
