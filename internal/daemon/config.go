// Package daemon provides background sync operations for the ledger.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/paths"
)

// cachedWorkspaceID stores the workspace ID computed from CWD on first call.
// This ensures a stable ID even if the daemon's CWD becomes invalid later
// (e.g. tmpdir cleanup on macOS while the daemon is running).
var (
	cachedWorkspaceID     string
	cachedWorkspaceIDOnce sync.Once
)

// IsDaemonDisabled returns true if the daemon has been explicitly disabled
// via the SAGEOX_DAEMON=false environment variable.
func IsDaemonDisabled() bool {
	return strings.ToLower(os.Getenv("SAGEOX_DAEMON")) == "false"
}

// Config holds daemon configuration settings.
type Config struct {
	// SyncIntervalRead is how often to pull changes from remote.
	SyncIntervalRead time.Duration

	// TeamContextSyncInterval is how often to sync team context repos.
	TeamContextSyncInterval time.Duration

	// DebounceWindow batches rapid changes before committing.
	DebounceWindow time.Duration

	// InactivityTimeout is how long the daemon waits without activity before exiting.
	// Zero means never exit due to inactivity.
	InactivityTimeout time.Duration

	// VersionCheckInterval is how often to check GitHub for new releases.
	VersionCheckInterval time.Duration

	// GCCheckInterval is how often to check if any workspace needs a reclone GC.
	// The actual GC cadence is per-workspace from gc_interval_days in the manifest.
	GCCheckInterval time.Duration

	// AutoStart starts daemon on first ox command if true.
	AutoStart bool

	// LedgerPath is the path to the ledger repository.
	LedgerPath string

	// ProjectRoot is the path to the project root (for loading team contexts).
	ProjectRoot string
}

// DefaultConfig returns the default daemon configuration.
func DefaultConfig() *Config {
	return &Config{
		SyncIntervalRead:        5 * time.Minute,  // includes anti-entropy checks
		TeamContextSyncInterval: 1 * time.Minute,
		DebounceWindow:          500 * time.Millisecond,
		VersionCheckInterval:    30 * time.Minute, // ETag conditional requests make this cheap
		GCCheckInterval:         1 * time.Hour,    // check hourly, actual GC cadence is per-workspace
		InactivityTimeout:       1 * time.Hour,    // exit after 1 hour of inactivity
		AutoStart:               true,
		LedgerPath:              "", // resolved at runtime
		ProjectRoot:             "", // resolved at runtime
	}
}

// WorkspaceID generates a stable identifier for a workspace.
// Uses SHA256 of the real (symlink-resolved) absolute path, truncated to 8 chars.
func WorkspaceID(workspacePath string) string {
	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		absPath = workspacePath
	}
	// resolve symlinks to ensure consistent IDs regardless of how the path was accessed
	realPath, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		absPath = realPath
	}
	hash := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(hash[:])[:8]
}

// CurrentWorkspaceID returns the ID for the current working directory.
// The result is cached on first call so the daemon continues to use the
// correct workspace ID even if its CWD is later deleted (e.g. macOS
// tmpdir cleanup while the daemon is running long-term).
func CurrentWorkspaceID() string {
	cachedWorkspaceIDOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			cachedWorkspaceID = "default"
			return
		}
		cachedWorkspaceID = WorkspaceID(cwd)
	})
	return cachedWorkspaceID
}

// SocketPath returns the path to the daemon Unix socket for the current workspace.
func SocketPath() string {
	return SocketPathForWorkspace(CurrentWorkspaceID())
}

// SocketPathForWorkspace returns the socket path for a specific workspace.
func SocketPathForWorkspace(workspaceID string) string {
	return paths.DaemonSocketFile(workspaceID)
}

// StabilizeCWD moves the daemon's working directory to $HOME so that git
// commands don't fail if the original CWD is deleted (e.g. tmpdir cleanup).
// Must be called AFTER CurrentWorkspaceID() has cached the workspace ID.
func StabilizeCWD() {
	// ensure workspace ID is cached before changing CWD
	_ = CurrentWorkspaceID()

	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Chdir(home)
	}
}

// LogPath returns the path to the daemon log file for the current workspace.
// Requires project to be initialized with repo_id.
func LogPath() string {
	cwd, _ := os.Getwd()
	repoID := config.GetRepoID(cwd)
	workspaceID := CurrentWorkspaceID()
	return paths.DaemonLogFile(repoID, workspaceID)
}

// LogPathForWorkspace returns the log path for a specific workspace and repo.
func LogPathForWorkspace(repoID, workspaceID string) string {
	return paths.DaemonLogFile(repoID, workspaceID)
}

// PidPath returns the path to the daemon PID file for the current workspace.
// Note: PID files are NOT used for liveness detection - use file locks instead.
func PidPath() string {
	return PidPathForWorkspace(CurrentWorkspaceID())
}

// PidPathForWorkspace returns the PID path for a specific workspace.
func PidPathForWorkspace(workspaceID string) string {
	return paths.DaemonPidFile(workspaceID)
}

// RegistryPath returns the path to the daemon registry file.
func RegistryPath() string {
	return paths.DaemonRegistryFile()
}
