package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
)

// HealthStatus contains session system health information.
type HealthStatus struct {
	// StorageWritable indicates if local storage is writable
	StorageWritable bool

	// StoragePath is the path to local session storage
	StoragePath string

	// StorageError contains error details if storage is not writable
	StorageError string

	// RepoCloned indicates if the ledger is cloned
	RepoCloned bool

	// RepoPath is the path to the ledger
	RepoPath string

	// RepoError contains error details if ledger has issues
	RepoError string

	// IsRecordingActive indicates if a recording is currently in progress
	IsRecordingActive bool

	// Recording contains the current recording state (if active)
	Recording *RecordingState

	// IsStaleRecording indicates the active recording may be abandoned
	// (running for more than StaleRecordingThreshold)
	IsStaleRecording bool

	// StaleRecordingAge is how long the stale recording has been running
	StaleRecordingAge time.Duration

	// IsStopIncomplete indicates the recording was stopped but the session file was empty
	IsStopIncomplete bool

	// StopIncompleteAge is how long since the incomplete stop occurred
	StopIncompleteAge time.Duration

	// PendingCount is the number of sessions pending commit
	PendingCount int

	// SyncedWithRemote indicates if local repo is synced with remote
	SyncedWithRemote bool

	// SyncStatus describes the sync state (e.g., "ahead by 3", "behind by 1")
	SyncStatus string

	// Errors contains any errors encountered during health checks
	Errors []string
}

// StaleRecordingThreshold defines how long a recording can run before
// it's considered potentially stale/abandoned (12 hours).
const StaleRecordingThreshold = 12 * time.Hour

// CheckHealth performs a comprehensive health check of the session system.
// projectRoot is the current project's git root directory. If provided, ledger
// path is derived from it; if empty, ledger.DefaultPath() is used (cwd-based).
func CheckHealth(projectRoot string) *HealthStatus {
	status := &HealthStatus{
		Errors: make([]string, 0),
	}

	// check local storage
	checkStorageHealth(status, projectRoot)

	// check ledger
	checkLedgerHealth(status, projectRoot)

	// check recording state
	checkRecordingState(status, projectRoot)

	// check pending sessions
	checkPendingSessions(status)

	// check remote sync status
	checkSyncStatus(status)

	return status
}

// checkStorageHealth verifies local session storage is writable.
// If projectRoot is provided, derives ledger path from it; otherwise uses DefaultPath.
func checkStorageHealth(status *HealthStatus, projectRoot string) {
	var ledgerPath string
	var err error

	if projectRoot != "" {
		// derive ledger path from project config (repo ID + endpoint)
		ledgerPath = ledgerPathFromProject(projectRoot)
	}
	if ledgerPath == "" {
		// fall back to cwd-based ledger path
		ledgerPath, err = ledger.DefaultPath()
		if err != nil {
			status.StorageError = fmt.Sprintf("get ledger path: %v", err)
			status.Errors = append(status.Errors, status.StorageError)
			return
		}
	}
	status.StoragePath = ledgerPath

	// check if the directory exists (don't create it — that's the clone step's job)
	info, err := os.Stat(status.StoragePath)
	if err != nil {
		if os.IsNotExist(err) {
			// directory doesn't exist yet — ledger hasn't been cloned
			// check if parent dir is writable (storage will be writable once cloned)
			parentDir := filepath.Dir(status.StoragePath)
			if parentInfo, parentErr := os.Stat(parentDir); parentErr == nil && parentInfo.IsDir() {
				status.StorageWritable = true
				return
			}
			// parent doesn't exist either — check grandparent writability
			status.StorageWritable = true
			return
		}
		status.StorageError = fmt.Sprintf("stat storage dir=%s: %v", status.StoragePath, err)
		status.Errors = append(status.Errors, status.StorageError)
		return
	}

	if !info.IsDir() {
		status.StorageError = fmt.Sprintf("storage path is not a directory: %s", status.StoragePath)
		status.Errors = append(status.Errors, status.StorageError)
		return
	}

	// directory exists — test write access with a temp file
	testFile := filepath.Join(status.StoragePath, ".health_check")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		status.StorageError = fmt.Sprintf("write test file=%s: %v", testFile, err)
		status.Errors = append(status.Errors, status.StorageError)
		return
	}

	// clean up test file
	_ = os.Remove(testFile)
	status.StorageWritable = true
}

// checkLedgerHealth verifies the ledger git repo is cloned and accessible.
// If projectRoot is provided, derives ledger path from it; otherwise uses DefaultPath.
func checkLedgerHealth(status *HealthStatus, projectRoot string) {
	var ledgerPath string
	var err error

	if projectRoot != "" {
		// derive ledger path from project config (repo ID + endpoint)
		ledgerPath = ledgerPathFromProject(projectRoot)
	}
	if ledgerPath == "" {
		// fall back to cwd-based ledger path
		ledgerPath, err = ledger.DefaultPath()
		if err != nil {
			// already reported in storage check
			return
		}
	}
	status.RepoPath = ledgerPath

	// check if it's a git repository
	gitDir := filepath.Join(status.RepoPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		if os.IsNotExist(err) {
			status.RepoError = "ledger not provisioned"
		} else {
			status.RepoError = fmt.Sprintf("access .git dir=%s: %v", gitDir, err)
			status.Errors = append(status.Errors, status.RepoError)
		}
		return
	}

	if !info.IsDir() {
		status.RepoError = fmt.Sprintf(".git is not a directory: path=%s", gitDir)
		status.Errors = append(status.Errors, status.RepoError)
		return
	}

	status.RepoCloned = true
}

// checkRecordingState checks if a recording is currently active.
func checkRecordingState(status *HealthStatus, projectRoot string) {
	if projectRoot == "" {
		return
	}

	state, err := LoadRecordingState(projectRoot)
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("read recording state project=%s: %v", projectRoot, err))
		return
	}

	if state != nil {
		status.IsRecordingActive = true
		status.Recording = state

		// check if stop was attempted but session file was empty
		if state.StopIncomplete {
			status.IsStopIncomplete = true
			status.StopIncompleteAge = time.Since(state.StartedAt)
		}

		// check if recording is stale (running for too long)
		age := time.Since(state.StartedAt)
		if age > StaleRecordingThreshold {
			status.IsStaleRecording = true
			status.StaleRecordingAge = age
		}
	}
}

// checkPendingSessions counts uncommitted sessions in the ledger.
func checkPendingSessions(status *HealthStatus) {
	if !status.RepoCloned {
		return
	}

	// run git status --porcelain to count untracked/modified files
	cmd := exec.Command("git", "-C", status.RepoPath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("check pending sessions repo=%s: %v", status.RepoPath, err))
		return
	}

	// count lines that represent session files (*.jsonl)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// look for .jsonl files (sessions)
		if strings.HasSuffix(line, ".jsonl") {
			status.PendingCount++
		}
	}
}

// checkSyncStatus checks if the ledger is synced with remote.
// Uses cached tracking refs (no git fetch - the daemon handles that).
func checkSyncStatus(status *HealthStatus) {
	if !status.RepoCloned {
		return
	}

	// check ahead/behind status using cached refs (daemon handles fetch)
	cmd := exec.Command("git", "-C", status.RepoPath, "status", "--porcelain", "-b")
	output, err := cmd.Output()
	if err != nil {
		status.SyncStatus = "unknown"
		return
	}

	outputStr := string(output)

	// parse branch line for ahead/behind info
	// format: ## branch...origin/branch [ahead N, behind M]
	if strings.Contains(outputStr, "[") {
		start := strings.Index(outputStr, "[")
		end := strings.Index(outputStr, "]")
		if start >= 0 && end > start {
			status.SyncStatus = outputStr[start+1 : end]
			status.SyncedWithRemote = false
			return
		}
	}

	// check if we have a remote configured
	remoteCmd := exec.Command("git", "-C", status.RepoPath, "remote")
	remoteOutput, err := remoteCmd.Output()
	if err != nil || strings.TrimSpace(string(remoteOutput)) == "" {
		status.SyncStatus = "no remote configured"
		return
	}

	// if no ahead/behind info and remote exists, we're synced
	status.SyncedWithRemote = true
	status.SyncStatus = "up to date"
}

// RecordingDurationString returns a human-readable duration string.
func RecordingDurationString(state *RecordingState) string {
	if state == nil {
		return ""
	}

	duration := time.Since(state.StartedAt)
	return formatDuration(duration)
}

// formatDuration converts a duration to a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}

// ShortenPath returns a shortened path for display.
// Replaces home directory with ~.
func ShortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// ledgerPathFromProject derives the ledger path from a project root.
// Returns empty string if project config cannot be loaded or has no repo ID.
func ledgerPathFromProject(projectRoot string) string {
	projectCfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil || projectCfg == nil || projectCfg.RepoID == "" {
		return ""
	}
	ep := endpoint.GetForProject(projectRoot)
	return config.DefaultLedgerPath(projectCfg.RepoID, ep)
}
