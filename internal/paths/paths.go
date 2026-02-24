// Package paths provides canonical path resolution for all SageOx data locations.
//
// XDG BASE DIRECTORY SPECIFICATION COMPLIANCE
// ===========================================
//
// The ox CLI MUST follow the XDG Base Directory Specification (v0.8+):
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
// "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY", and "OPTIONAL" in this
// document are to be interpreted as described in RFC 2119.
//
// Requirements:
//
//   - Config data MUST be stored in $XDG_CONFIG_HOME/sageox (default: ~/.config/sageox/)
//   - Persistent data MUST be stored in $XDG_DATA_HOME/sageox (default: ~/.local/share/sageox/)
//   - Cache data MUST be stored in $XDG_CACHE_HOME/sageox (default: ~/.cache/sageox/)
//   - State data SHOULD be stored in $XDG_STATE_HOME/sageox (default: ~/.local/state/sageox/)
//   - Runtime data MAY be stored in $XDG_RUNTIME_DIR/sageox if available
//
// Legacy mode (OX_XDG_DISABLE=1) is provided for backwards compatibility only
// and SHOULD NOT be used in new installations.
//
// Path Construction:
//
//   - All path functions in this package are CANONICAL
//   - Code MUST NOT construct SageOx paths manually with filepath.Join()
//   - Changes to path locations REQUIRE Ryan's review
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/endpoint"
)

var (
	homeDir     string
	homeDirOnce sync.Once
)

// getHomeDir returns the user's home directory, cached for performance.
func getHomeDir() string {
	homeDirOnce.Do(func() {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			homeDir = ""
		}
	})
	return homeDir
}

// -----------------------------------------------------------------------------
// Base Directories
// -----------------------------------------------------------------------------

// SageoxDir returns the legacy base SageOx directory.
//
// XDG mode (default): Returns empty string (use specific *Dir functions instead)
// Legacy mode (OX_XDG_DISABLE=1): ~/.sageox
func SageoxDir() string {
	if useXDGMode() {
		// in XDG mode, there is no single base directory
		return ""
	}
	home := getHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".sageox")
}

// ConfigDir returns the configuration directory per XDG Base Directory Specification.
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// Contains: config.yaml, auth.json, git-credentials.json, verification-cache.json, machine-id
//
// XDG mode (default): $XDG_CONFIG_HOME/sageox (default: ~/.config/sageox)
// Legacy mode (OX_XDG_DISABLE=1): ~/.sageox/config
func ConfigDir() string {
	if useXDGMode() {
		return filepath.Join(xdgConfigHome(), "sageox")
	}
	return filepath.Join(SageoxDir(), "config")
}

// DataDir returns the persistent data directory per XDG Base Directory Specification.
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// Contains: teams/ (team context repositories)
//
// XDG mode (default): $XDG_DATA_HOME/sageox (default: ~/.local/share/sageox)
// Legacy mode (OX_XDG_DISABLE=1): ~/.sageox/data
func DataDir() string {
	if useXDGMode() {
		return filepath.Join(xdgDataHome(), "sageox")
	}
	return filepath.Join(SageoxDir(), "data")
}

// CacheDir returns the cache directory per XDG Base Directory Specification.
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// Contains: guidance/, sessions/, daemon/ (logs)
//
// XDG mode (default): $XDG_CACHE_HOME/sageox (default: ~/.cache/sageox)
// Legacy mode (OX_XDG_DISABLE=1): ~/.sageox/cache
func CacheDir() string {
	if useXDGMode() {
		return filepath.Join(xdgCacheHome(), "sageox")
	}
	return filepath.Join(SageoxDir(), "cache")
}

// StateDir returns the runtime state directory per XDG Base Directory Specification.
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// Contains: daemon/ (sockets, PIDs, registry)
//
// XDG mode (default): $XDG_RUNTIME_DIR/sageox (ephemeral, doesn't persist across reboots)
// Legacy mode (OX_XDG_DISABLE=1): ~/.sageox/state
func StateDir() string {
	if useXDGMode() {
		// use runtime dir for ephemeral daemon state in XDG mode
		return filepath.Join(xdgRuntimeDir(), "sageox")
	}
	return filepath.Join(SageoxDir(), "state")
}

// -----------------------------------------------------------------------------
// Config Files
// -----------------------------------------------------------------------------

// UserConfigFile returns the path to the user configuration file.
// Contains user preferences like tips_enabled, telemetry settings, etc.
func UserConfigFile() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

// AuthFile returns the path to the authentication tokens file.
// Contains per-endpoint auth tokens. Should have 0600 permissions.
func AuthFile() string {
	return filepath.Join(ConfigDir(), "auth.json")
}

// GitCredentialsFile returns the path to git server credentials.
// Fallback storage when keychain is unavailable. Should have 0600 permissions.
func GitCredentialsFile() string {
	return filepath.Join(ConfigDir(), "git-credentials.json")
}

// VerificationCacheFile returns the path to the signature verification cache.
// Contains HMAC-protected cache entries for guidance signature verification.
func VerificationCacheFile() string {
	return filepath.Join(ConfigDir(), "verification-cache.json")
}

// MachineIDFile returns the path to the machine identifier file.
// Contains a unique machine ID used for HMAC operations.
func MachineIDFile() string {
	return filepath.Join(ConfigDir(), "machine-id")
}

// -----------------------------------------------------------------------------
// Cache Paths
// -----------------------------------------------------------------------------

// GuidanceCacheDir returns the directory for cached guidance content.
// Contains obfuscated guidance cache files.
func GuidanceCacheDir() string {
	return filepath.Join(CacheDir(), "guidance")
}

// SessionCacheDir returns the directory for session cache.
// If repoID is empty, returns the base sessions directory.
// Otherwise returns the repo-specific session directory.
func SessionCacheDir(repoID string) string {
	base := filepath.Join(CacheDir(), "sessions")
	if repoID == "" {
		return base
	}
	return filepath.Join(base, repoID)
}

// TempDir returns the base temporary directory for SageOx ephemeral files.
//
// CRITICAL: Uses /tmp/<username>/sageox/ to avoid multi-user permission conflicts.
//
// Why /tmp/<username>/sageox/ (not /tmp/sageox/<username>/)?
//   - If user A creates /tmp/sageox/ first, user B cannot write into it (owned by A)
//   - /tmp/<username>/ is always owned by that user, so each user can create sageox/ inside
//   - Daemon logs are ephemeral - only useful while daemon is running
//   - OS automatically cleans /tmp (on reboot or via tmpwatch/systemd-tmpfiles)
//   - Standard pattern (many apps use /tmp/<username>/)
//
// Structure:
//
//	/tmp/<username>/sageox/logs/daemon-<workspace_id>.log
//
// Example:
//
//	ryan: /tmp/ryan/sageox/logs/daemon-abc123.log
//	ajit: /tmp/ajit/sageox/logs/daemon-def456.log
//
// Note: Heartbeats use cache (bounded size), logs use /tmp (OS cleanup).
func TempDir() string {
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME") // Windows
	}
	if username == "" {
		username = "sageox"
	}

	// on macOS/Linux use /tmp explicitly — os.TempDir() returns /var/folders/.../T/
	// on macOS which is a per-user cache dir that gets purged unpredictably.
	// /tmp is stable, well-known, and matches our documented paths.
	// on Windows fall back to os.TempDir() (e.g. C:\Users\<user>\AppData\Local\Temp).
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = os.TempDir()
	}
	return filepath.Join(base, username, "sageox")
}

// DaemonLogFile returns the path to a specific daemon's log file.
// Ensures the log directory exists with proper permissions.
//
// Returns /tmp/<username>/sageox/logs/daemon_<repo_id>_<workspace_id>.log
//
// Uses composite identifier (repo_id + workspace_id) for consistency with heartbeats:
//   - repo_id makes logs debuggable (can see which repo)
//   - workspace_id ensures uniqueness (supports multiple worktrees)
//
// Creates entire directory tree with 0700 permissions (owner-only access):
//
//	/tmp/<username>/sageox/       (0700 - owner-only)
//	/tmp/<username>/sageox/logs/  (0700 - owner-only)
//
// CRITICAL: All directories are 0700 to prevent other users from accessing
// daemon logs, which may contain sensitive information.
//
// If directory creation fails, still returns the path (caller will get error when opening file).
func DaemonLogFile(repoID, workspaceID string) string {
	// Ensure base sageox directory exists with 0700
	sageoxDir := TempDir()
	_ = os.MkdirAll(sageoxDir, 0700)
	// Fix permissions even if directory already existed
	_ = os.Chmod(sageoxDir, 0700)

	// Ensure logs subdirectory exists with 0700
	logDir := filepath.Join(sageoxDir, "logs")
	_ = os.MkdirAll(logDir, 0700)
	// Fix permissions even if directory already existed
	_ = os.Chmod(logDir, 0700)

	return filepath.Join(logDir, fmt.Sprintf("daemon_%s_%s.log", repoID, workspaceID))
}

// DaemonCacheDir returns the base directory for daemon cache files.
// Used for heartbeats and other daemon-managed cache data.
//
//	~/.cache/sageox/ (or XDG equivalent)
func DaemonCacheDir() string {
	return CacheDir()
}

// HeartbeatCacheDir returns the directory for daemon heartbeat files.
// Heartbeats are organized by endpoint to support multi-environment usage.
//
//	~/.cache/sageox/<endpoint>/heartbeats/
//
// The endpoint is normalized via NormalizeSlug() which strips common prefixes
// (api., www., app.) and removes port numbers.
//
// IMPORTANT: ep is REQUIRED. Use endpoint.GetForProject(projectRoot) to get the
// correct endpoint for a project context.
func HeartbeatCacheDir(ep string) string {
	if ep == "" {
		panic("HeartbeatCacheDir: endpoint is required - use endpoint.GetForProject(projectRoot)")
	}

	slug := endpoint.NormalizeSlug(ep)
	if slug == "" {
		slug = "unknown"
	}
	return filepath.Join(CacheDir(), slug, "heartbeats")
}

// -----------------------------------------------------------------------------
// Data Paths
// -----------------------------------------------------------------------------

// CRITICAL DESIGN NOTE: Endpoint Directory Structure
//
// All data paths include an endpoint subdirectory (e.g., sageox.ai/, staging.sageox.ai/).
// This is INTENTIONAL and REQUIRED for correct operation across dev/test/production.
//
// DO NOT remove the endpoint directory layer to "simplify" paths. This has been
// attempted multiple times during ox CLI development and ALWAYS leads to:
//   - Data from one environment leaking into another
//   - Test data polluting production configs
//   - Race conditions when switching endpoints
//   - Authentication state mismatches
//
// The endpoint directory ensures:
//   - Clean isolation between dev/test/prod environments
//   - Safe parallel development against multiple backends
//   - No accidental cross-contamination of team contexts or ledgers
//
// Ergonomics improvements (shorter paths, symlinks, etc.) are NICE TO HAVE.
// Working correctly across all environments is REQUIRED.

// TeamsDataDir returns the directory containing all team context repositories.
//
// All endpoints use a consistent namespaced structure:
//
//	~/.sageox/data/<endpoint>/teams/
//	e.g. ~/.sageox/data/sageox.ai/teams/
//	e.g. ~/.sageox/data/staging.sageox.ai/teams/
//	e.g. ~/.sageox/data/localhost/teams/
//
// The endpoint is normalized via NormalizeSlug() which strips common prefixes
// (api., www., app.) and removes port numbers.
//
// IMPORTANT: ep is REQUIRED. Use endpoint.GetForProject(projectRoot) to get the
// correct endpoint for a project context. Only use endpoint.Get() during login
// or when no project context exists.
func TeamsDataDir(ep string) string {
	if ep == "" {
		panic("TeamsDataDir: endpoint is required - use endpoint.GetForProject(projectRoot) not empty string")
	}

	slug := endpoint.NormalizeSlug(ep)
	if slug == "" {
		slug = "unknown"
	}
	return filepath.Join(DataDir(), slug, "teams")
}

// TeamContextDir returns the CANONICAL directory for a specific team context repository.
//
//	~/.sageox/data/<endpoint>/teams/<team_id>/
//
// IMPORTANT: This is the ONLY function that should determine team context paths.
// NEVER construct team context paths manually (e.g., filepath.Join(projectRoot, ".team-contexts", ...)).
// Team contexts belong in the user's home directory, NOT in the project working tree.
//
// If ep is empty, uses the current endpoint from environment.
func TeamContextDir(teamID, ep string) string {
	safeID := sanitizePathComponent(teamID)
	return filepath.Join(TeamsDataDir(ep), safeID)
}

// LedgersDataDir returns the CANONICAL directory for ledger git checkouts.
//
// Format:
//
//	~/.local/share/sageox/<endpoint>/ledgers/<repo_id>/
//
// This centralized location allows all worktrees to share a single ledger
// and enables daemons to discover ledgers at a known location.
//
// The endpoint is normalized via NormalizeSlug() which strips common prefixes
// (api., www., app.) and removes port numbers.
//
// IMPORTANT: ep is REQUIRED. Use endpoint.GetForProject(projectRoot) to get the
// correct endpoint for a project context. Only use endpoint.Get() during login
// or when no project context exists.
//
// If repoID is empty, returns the base ledgers directory for that endpoint.
func LedgersDataDir(repoID, ep string) string {
	if ep == "" {
		panic("LedgersDataDir: endpoint is required - use endpoint.GetForProject(projectRoot) not empty string")
	}

	slug := endpoint.NormalizeSlug(ep)
	if slug == "" {
		slug = "unknown"
	}

	base := filepath.Join(DataDir(), slug, "ledgers")
	if repoID == "" {
		return base
	}
	return filepath.Join(base, sanitizePathComponent(repoID))
}

// -----------------------------------------------------------------------------
// State Paths (Daemon)
// -----------------------------------------------------------------------------

// DaemonStateDir returns the directory for daemon runtime state.
// Contains sockets, PIDs, and the daemon registry.
func DaemonStateDir() string {
	return filepath.Join(StateDir(), "daemon")
}

// DaemonSocketFile returns the path to a daemon's Unix socket.
func DaemonSocketFile(workspaceID string) string {
	return filepath.Join(DaemonStateDir(), "daemon-"+workspaceID+".sock")
}

// DaemonPidFile returns the path to a daemon's PID file.
func DaemonPidFile(workspaceID string) string {
	return filepath.Join(DaemonStateDir(), "daemon-"+workspaceID+".pid")
}

// DaemonRegistryFile returns the path to the daemon registry.
// Contains JSON registry of all active daemons.
func DaemonRegistryFile() string {
	return filepath.Join(DaemonStateDir(), "registry.json")
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// sanitizePathComponent removes or replaces characters that are unsafe for
// directory/file names.
func sanitizePathComponent(s string) string {
	if s == "" {
		return "unknown"
	}

	// replace unsafe characters with underscores
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
		" ", "_",
		"..", "_", // prevent directory traversal
	)
	sanitized := replacer.Replace(s)

	// remove leading/trailing underscores and dots
	sanitized = strings.Trim(sanitized, "_.")

	if sanitized == "" {
		return "unknown"
	}

	return sanitized
}

// EnsureDir creates a directory and all parent directories if they don't exist.
// Returns the path for convenience in chained calls.
// SECURITY: Uses 0700 (owner-only) to prevent other users from listing contents.
func EnsureDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureDirForFile creates the parent directory for a file path.
// Returns the original path for convenience.
// SECURITY: Uses 0700 (owner-only) to prevent other users from listing contents.
func EnsureDirForFile(filePath string) (string, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filePath, nil
}

// EndpointSlug extracts the endpoint slug from a SageOx data path.
// This is used to verify endpoint consistency between project config and local paths.
//
// The function looks for paths in the data directory structure:
//
//	~/.local/share/sageox/<endpoint>/teams/...
//	~/.local/share/sageox/<endpoint>/ledgers/...
//
// Returns the endpoint slug (e.g., "sageox.ai", "localhost") if found,
// or empty string if the path is not a SageOx data path or the endpoint
// cannot be determined.
//
// Examples:
//
//	~/.local/share/sageox/sageox.ai/teams/team_abc -> "sageox.ai"
//	~/.local/share/sageox/localhost/ledgers/xyz123 -> "localhost"
//	/some/other/path -> ""
func EndpointSlug(path string) string {
	if path == "" {
		return ""
	}

	// normalize the path
	path = filepath.Clean(path)

	// get the data directory base
	dataDir := DataDir()
	if dataDir == "" {
		return ""
	}

	// check if the path is under the data directory
	if !strings.HasPrefix(path, dataDir+string(filepath.Separator)) {
		return ""
	}

	// extract the relative path from data directory
	relativePath := strings.TrimPrefix(path, dataDir+string(filepath.Separator))
	if relativePath == "" {
		return ""
	}

	// the first component should be the endpoint slug
	parts := strings.SplitN(relativePath, string(filepath.Separator), 2)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}

	return parts[0]
}
