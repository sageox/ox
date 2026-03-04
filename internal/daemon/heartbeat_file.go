// Package daemon provides heartbeat file management for daemon health monitoring.
//
// # Heartbeat Storage Strategy
//
// Heartbeats are stored in different locations depending on what they're monitoring:
//
//  1. WORKSPACE heartbeats: ~/.cache/sageox/<endpoint>/heartbeats/<workspace_id>.jsonl
//  2. LEDGER heartbeats:    ~/.cache/sageox/<endpoint>/heartbeats/<workspace_id>_ledger.jsonl
//  3. TEAM heartbeats:      ~/.cache/sageox/<endpoint>/heartbeats/<team_id>.jsonl
//
// # Why workspace_id (not repo_id) for workspace/ledger heartbeats?
//
// CRITICAL: workspace_id is a hash of the project root PATH, not the repo identity.
// This prevents collisions when users have multiple git worktrees of the same repo.
//
// Scenario that breaks with repo_id:
//   - User has repo "foo" with repo_id="repo_abc123"
//   - User creates worktrees: ~/work/foo-main, ~/work/foo-feature1, ~/work/foo-feature2
//   - Each worktree has its own daemon instance (one per project root)
//   - All three daemons would write to the SAME heartbeat file: repo_abc123.jsonl
//   - Result: Race conditions, confusing PID/workspace data, can't track per-worktree health
//
// By using workspace_id (hash of ~/work/foo-main vs ~/work/foo-feature1):
//   - Each worktree gets its own heartbeat file: a1b2c3d4.jsonl, e5f6g7h8.jsonl, etc.
//   - Each daemon's heartbeat is isolated
//   - Can track health of each worktree independently
//
// # Why global cache (not in-repo .sageox/cache/)?
//
// Originally heartbeats were written to .sageox/cache/heartbeats.jsonl inside each repo.
// This worked fine for workspaces (each worktree has its own .sageox/), but caused problems
// for ledgers and team contexts:
//
//   - Ledgers are SHARED git repos (in sibling dirs like project_sageox/)
//   - Team contexts are SHARED git repos (in ~/.local/share/sageox/<endpoint>/teams/)
//   - Writing .sageox/ directories into shared repos pollutes them with machine-specific data
//   - Even with .gitignore, the directories shouldn't exist in shared repos
//
// Solution: Global cache for all heartbeats, using appropriate identifiers:
//   - workspace_id: Unique per daemon instance (solves worktree problem)
//   - team_id: Shared across workspaces (team contexts are multi-project)
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/paths"
)

// HeartbeatEntry represents a single heartbeat written to a repo.
type HeartbeatEntry struct {
	Timestamp     time.Time `json:"ts"`
	DaemonPID     int       `json:"pid"`
	DaemonVersion string    `json:"version"`
	Workspace     string    `json:"workspace"`
	LastSync      time.Time `json:"last_sync,omitempty"`
	Status        string    `json:"status"` // "healthy", "error", "starting"
	ErrorCount    int       `json:"error_count,omitempty"`
}

const (
	maxHeartbeatHistory = 5 // keep last 5 heartbeats (prevents unbounded growth)
)

// UserHeartbeatPath returns the global cache path for workspace heartbeat.
//
// CRITICAL: Uses BOTH repo_id AND workspace_id in filename.
//
// Why both?
//   - workspace_id (hash of path) prevents collisions between worktrees
//   - repo_id makes debugging easier - you can see which repo the heartbeat belongs to
//
// Format: ~/.cache/sageox/<endpoint>/heartbeats/<repo_id>_<workspace_id>.jsonl
//
// Example with multiple worktrees of repo "foo" (repo_id=repo_abc123):
//   - Worktree ~/work/foo-main   → workspace_id=a1b2c3d4 → repo_abc123_a1b2c3d4.jsonl
//   - Worktree ~/work/foo-fix123 → workspace_id=e5f6g7h8 → repo_abc123_e5f6g7h8.jsonl
//
// Both have same repo_id but different workspace_ids → no collision, easy to identify.
func UserHeartbeatPath(ep, repoID, workspaceID string) string {
	return filepath.Join(
		paths.HeartbeatCacheDir(ep),
		fmt.Sprintf("%s_%s.jsonl", repoID, workspaceID),
	)
}

// UserLedgerHeartbeatPath returns the global cache path for ledger heartbeat.
//
// CRITICAL: Uses BOTH repo_id AND workspace_id because each worktree has its own ledger.
// Ledgers use the sibling directory pattern: <project>_sageox/<endpoint>/ledger
// So different worktrees → different ledger paths → need different heartbeat files.
//
// Format: ~/.cache/sageox/<endpoint>/heartbeats/<repo_id>_<workspace_id>_ledger.jsonl
//
// Example:
//   - Worktree ~/work/foo-main has ledger ~/work/foo-main_sageox/sageox.ai/ledger
//     → repo_abc123_a1b2c3d4_ledger.jsonl
//   - Worktree ~/work/foo-fix has ledger ~/work/foo-fix_sageox/sageox.ai/ledger
//     → repo_abc123_e5f6g7h8_ledger.jsonl
//
// These are DIFFERENT ledger repos → separate heartbeats, but same repo_id for grouping.
func UserLedgerHeartbeatPath(ep, repoID, workspaceID string) string {
	return filepath.Join(
		paths.HeartbeatCacheDir(ep),
		fmt.Sprintf("%s_%s_ledger.jsonl", repoID, workspaceID),
	)
}

// UserTeamHeartbeatPath returns the global cache path for team context heartbeat.
//
// Uses team_id (NOT workspace_id) because team contexts are shared across projects.
// A team context repo at ~/.local/share/sageox/<endpoint>/teams/<team_id>/ may be
// used by multiple projects/worktrees simultaneously. All daemons monitoring this
// team context write to the same heartbeat file (last-write-wins is acceptable for
// monitoring data - we just care that SOME daemon is syncing it).
//
//	~/.cache/sageox/<endpoint>/heartbeats/<team_id>.jsonl
//
// Example:
//   - Team "engineering" (team_id=team_abc123) is used by projects A, B, C
//   - All three daemons write to the same heartbeat: team_abc123.jsonl
//   - Doctor sees "team synced 2m ago" - doesn't matter which daemon did it
func UserTeamHeartbeatPath(ep, teamID string) string {
	return filepath.Join(
		paths.HeartbeatCacheDir(ep),
		fmt.Sprintf("%s.jsonl", teamID),
	)
}

// WriteHeartbeatToPath writes a heartbeat entry to an explicit path (for global cache).
// Maintains a rolling history of the last N heartbeats.
// Used for writing to ~/.cache/sageox/<endpoint>/heartbeats/<id>.jsonl
func WriteHeartbeatToPath(heartbeatPath string, entry HeartbeatEntry) error {
	// ensure directory exists
	dir := filepath.Dir(heartbeatPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// read existing entries
	existing, _ := readHeartbeatsFromPath(heartbeatPath)

	// append and trim
	existing = append(existing, entry)
	if len(existing) > maxHeartbeatHistory {
		existing = existing[len(existing)-maxHeartbeatHistory:]
	}

	// write atomically
	return writeHeartbeatsToPath(heartbeatPath, existing)
}

// readHeartbeatsFromPath reads heartbeat entries from an explicit path.
func readHeartbeatsFromPath(heartbeatPath string) ([]HeartbeatEntry, error) {
	f, err := os.Open(heartbeatPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat heartbeat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", heartbeatPath)
	}

	var entries []HeartbeatEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry HeartbeatEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// writeHeartbeatsToPath writes heartbeat entries to an explicit path atomically.
// Uses temp file + rename for atomic writes (prevents corrupted files on crash).
func writeHeartbeatsToPath(heartbeatPath string, entries []HeartbeatEntry) error {
	// write to temp file first
	tmpPath := heartbeatPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(f)
	for _, e := range entries {
		if err := encoder.Encode(e); err != nil {
			f.Close()
			os.Remove(tmpPath) // clean up temp file on error
			return err
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// atomic rename (POSIX guarantee)
	if err := os.Rename(tmpPath, heartbeatPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return nil
}

// ReadLastHeartbeatFromPath reads the most recent heartbeat from an explicit path.
// Used for reading from ~/.cache/sageox/<endpoint>/heartbeats/<id>.jsonl
func ReadLastHeartbeatFromPath(heartbeatPath string) (*HeartbeatEntry, error) {
	entries, err := readHeartbeatsFromPath(heartbeatPath)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return &entries[len(entries)-1], nil
}
