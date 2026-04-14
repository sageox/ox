// Package daemon provides background sync operations for the ledger.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
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

	cachedLegacyWorkspaceID     string
	cachedLegacyWorkspaceIDOnce sync.Once
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

	// DistillInterval is how often to trigger memory distillation.
	// Zero disables automatic distillation.
	DistillInterval time.Duration

	// CodeDBCheckInterval is how often to run CheckFreshness to detect new commits
	// (branch switches, manual commits, pulled history). Decoupled from git pull
	// cadence because the dirty overlay (via fsnotify) handles uncommitted file
	// search with ~5s latency — full reindex only needs to catch new commits.
	// Zero disables automatic codedb freshness checks.
	CodeDBCheckInterval time.Duration

	// LedgerCheckInterval is how often to check if the codedb ledger index
	// needs rebuilding (ledger HEAD changed). Independent of ledger pull cadence
	// so ledger index rebuilds don't scale with sync frequency.
	// Zero disables automatic ledger index checks.
	LedgerCheckInterval time.Duration

	// GitHubSyncInterval is how often to sync PRs/issues from GitHub.
	// Zero disables automatic GitHub sync.
	GitHubSyncInterval time.Duration

	// MurmurNudgeInterval is how often to nudge agents to self-report
	// what they're working on via ox murmur. Minimum 10 minutes.
	// Zero disables nudging (used when murmuring config is "manual").
	MurmurNudgeInterval time.Duration

	// RecordingReminderInterval is how often to remind agents that their
	// session is being recorded. Default 1 hour. Not user-visible in
	// ox config but settable via daemon config for testing (e.g., 1 minute).
	// Zero disables reminders.
	RecordingReminderInterval time.Duration

	// RecordingReminderTick is how often the reminder source checks for
	// agents that need reminding. Default 1 minute. Set lower for testing.
	// Zero uses the source default (1 minute).
	RecordingReminderTick time.Duration

	// SocketCheckInterval is how often the daemon checks the registry to see if
	// its PID is still registered. If a different PID is registered, the daemon
	// assumes it has been superseded and exits gracefully.
	// Zero disables the check.
	SocketCheckInterval time.Duration

	// PendingWorkGracePeriod is the maximum time the daemon stays alive
	// solely for pending work after inactivity timeout is reached.
	// Prevents stuck daemons when finalization hangs.
	PendingWorkGracePeriod time.Duration

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
		SyncIntervalRead:        60 * time.Second, // git pull from remote (ledger, team contexts)
		CodeDBCheckInterval:     15 * time.Minute, // full reindex for new commits; dirty overlay handles file edits via fsnotify
		TeamContextSyncInterval: 15 * time.Second,
		DebounceWindow:          500 * time.Millisecond,
		VersionCheckInterval:    30 * time.Minute, // ETag conditional requests make this cheap
		GCCheckInterval:         1 * time.Hour,    // check hourly, actual GC cadence is per-workspace
		DistillInterval:         6 * time.Hour,    // distill memory every 6 hours
		LedgerCheckInterval:     15 * time.Minute, // check if ledger index needs rebuild every 15 minutes
		GitHubSyncInterval:      15 * time.Minute, // sync PRs/issues every 15 minutes
		MurmurNudgeInterval:     15 * time.Minute, // nudge agents to self-report every 15 minutes
		RecordingReminderInterval: 1 * time.Hour,  // remind agents recording is active
		InactivityTimeout:       1 * time.Hour,    // exit after 1 hour of inactivity
		SocketCheckInterval:     30 * time.Second, // detect socket takeover by new daemon
		PendingWorkGracePeriod:  10 * time.Minute, // max time to stay alive for pending finalization
		AutoStart:               true,
		LedgerPath:              "", // resolved at runtime
		ProjectRoot:             "", // resolved at runtime
	}
}

// WorkspaceID generates a stable identifier for a workspace path.
// Uses SHA256 of the real (symlink-resolved) absolute path, truncated to 8 chars.
// This is the legacy path-based ID, still used for non-initialized repos.
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

// RepoBasedWorkspaceID returns a workspace ID derived from repo_id in .sageox/config.json.
// Multiple clones or worktrees of the same repo produce the same ID, so they
// share a single daemon. Falls back to path-based WorkspaceID if repo_id is unavailable.
func RepoBasedWorkspaceID(projectRoot string) string {
	repoID := config.GetRepoID(projectRoot)
	if repoID == "" {
		return WorkspaceID(projectRoot)
	}
	hash := sha256.Sum256([]byte(repoID))
	return hex.EncodeToString(hash[:])[:8]
}

// CurrentWorkspaceID returns the ID for the current working directory.
// Prefers repo_id-based identity so multiple clones/worktrees of the same repo
// share one daemon. Falls back to path-based ID for non-initialized repos.
// The result is cached on first call so the daemon continues to use the
// correct workspace ID even if its CWD is later deleted (e.g. macOS
// tmpdir cleanup while the daemon is running long-term).
//
// Note: This uses raw os.Getwd() for the direct socket path. Subdirectory
// normalization happens in resolveSocketPath() (registry fallback) and
// findProjectRootForDaemon() (daemon startup CWD), not here, because the
// sync.Once caching makes it unsafe to depend on walk-up discovery in tests.
func CurrentWorkspaceID() string {
	cachedWorkspaceIDOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			cachedWorkspaceID = "default"
			return
		}
		cachedWorkspaceID = RepoBasedWorkspaceID(cwd)
		slog.Debug("resolved workspace ID", "workspace_id", cachedWorkspaceID, "cwd", cwd)
	})
	return cachedWorkspaceID
}

// LegacyWorkspaceID returns the old path-based workspace ID for the current working directory.
// Needed for migration: stopping daemons that were started under the old path-hash scheme.
// Cached separately from CurrentWorkspaceID to avoid interference.
func LegacyWorkspaceID() string {
	cachedLegacyWorkspaceIDOnce.Do(func() {
		cwd, err := os.Getwd()
		if err != nil {
			cachedLegacyWorkspaceID = "default"
			return
		}
		cachedLegacyWorkspaceID = WorkspaceID(cwd)
	})
	return cachedLegacyWorkspaceID
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
	// also cache legacy ID while CWD is still valid
	_ = LegacyWorkspaceID()

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
