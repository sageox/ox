package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	session "github.com/sageox/ox/internal/session"
)

// SessionHealthCacheable is implemented by session checks that can use a cached HealthStatus.
// This avoids calling session.CheckHealth() multiple times (expensive: multiple git commands).
type SessionHealthCacheable interface {
	SetHealthStatus(status *session.HealthStatus)
}

// getOrComputeHealth returns the cached status if set, otherwise computes it.
func getOrComputeHealth(cached *session.HealthStatus, gitRoot string) *session.HealthStatus {
	if cached != nil {
		return cached
	}
	return session.CheckHealth(gitRoot)
}

// SessionCheck groups all session-related health checks.
type SessionCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionCheck creates a new session check.
func NewSessionCheck(gitRoot string) *SessionCheck {
	return &SessionCheck{
		gitRoot: gitRoot,
	}
}

// Name returns the check name.
func (c *SessionCheck) Name() string {
	return "Session Health"
}

// Run executes all session health checks.
func (c *SessionCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	// aggregate all sub-checks into a single result
	// this is a simplified version - in practice you might want
	// to return multiple results or a nested structure
	var issues []string

	if !status.StorageWritable {
		issues = append(issues, "storage not writable")
	}
	if !status.RepoCloned && status.RepoError != "ledger not provisioned" {
		issues = append(issues, "repo not cloned")
	}
	if status.PendingCount > 0 {
		issues = append(issues, fmt.Sprintf("%d pending sessions", status.PendingCount))
	}
	if !status.SyncedWithRemote && status.RepoCloned && status.SyncStatus != "no remote configured" {
		issues = append(issues, "not synced with remote")
	}

	if len(issues) > 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d issues found", len(issues)),
			Fix:     "Run 'ox session status' for details",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: "all checks passed",
	}
}

// SessionStorageCheck verifies local session storage is writable.
type SessionStorageCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionStorageCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionStorageCheck creates a storage check.
func NewSessionStorageCheck(gitRoot string) *SessionStorageCheck {
	return &SessionStorageCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionStorageCheck) Name() string {
	return "session storage writable"
}

// Run executes the storage check.
func (c *SessionStorageCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)
	shortPath := session.ShortenPath(status.StoragePath)

	if status.StorageWritable {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: shortPath,
		}
	}

	if status.StorageError != "" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "not writable",
			Fix:     fmt.Sprintf("Error: %s", status.StorageError),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: "unknown",
		Fix:     "Check permissions on ledger directory ({project}_sageox_ledger/)",
	}
}

// SessionRepoCheck verifies the ledger is cloned.
type SessionRepoCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionRepoCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionRepoCheck creates a repo check.
func NewSessionRepoCheck(gitRoot string) *SessionRepoCheck {
	return &SessionRepoCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionRepoCheck) Name() string {
	return "ledger cloned"
}

// Run executes the repo check.
func (c *SessionRepoCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	if status.RepoCloned {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "yes",
		}
	}

	if status.RepoError == "ledger not provisioned" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "not provisioned",
			Fix:     "Ledger is provisioned by cloud. Run 'ox login' then 'ox doctor --fix' to clone.",
		}
	}

	if status.RepoError != "" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "error",
			Fix:     status.RepoError,
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: "not found",
		Fix:     "Run 'ox doctor --fix' to clone ledger from cloud",
	}
}

// SessionRecordingCheck checks if a recording is active.
type SessionRecordingCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionRecordingCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionRecordingCheck creates a recording check.
func NewSessionRecordingCheck(gitRoot string) *SessionRecordingCheck {
	return &SessionRecordingCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionRecordingCheck) Name() string {
	return "recording"
}

// Run executes the recording check.
func (c *SessionRecordingCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	if !status.IsRecordingActive || status.Recording == nil {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	duration := session.RecordingDurationString(status.Recording)
	agentID := status.Recording.AgentID

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("Active (%s, %s)", duration, agentID),
	}
}

// SessionPendingCheck checks for pending sessions.
type SessionPendingCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionPendingCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionPendingCheck creates a pending check.
func NewSessionPendingCheck(gitRoot string) *SessionPendingCheck {
	return &SessionPendingCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionPendingCheck) Name() string {
	return "pending sessions"
}

// Run executes the pending check.
func (c *SessionPendingCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	// only show this check if repo is cloned
	if !status.RepoCloned {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	if status.PendingCount == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "none",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: fmt.Sprintf("%d pending commit", status.PendingCount),
		Fix:     "Run 'ox session commit' to save",
	}
}

// SessionStaleCheck checks for stale/abandoned recordings.
type SessionStaleCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionStaleCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionStaleCheck creates a stale recording check.
func NewSessionStaleCheck(gitRoot string) *SessionStaleCheck {
	return &SessionStaleCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionStaleCheck) Name() string {
	return "stale recording"
}

// Run executes the stale recording check.
func (c *SessionStaleCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	// only relevant if there's an active recording
	if !status.IsRecordingActive {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	if !status.IsStaleRecording {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// format the age
	hours := int(status.StaleRecordingAge.Hours())
	days := hours / 24

	var ageStr string
	if days > 0 {
		ageStr = fmt.Sprintf("%d days", days)
	} else {
		ageStr = fmt.Sprintf("%d hours", hours)
	}

	agentID := "unknown"
	if status.Recording != nil {
		agentID = status.Recording.AgentID
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: fmt.Sprintf("abandoned session recording for %s (%s)", ageStr, agentID),
		Fix:     fmt.Sprintf("Run 'ox agent %s session recover' to upload to ledger, or 'ox agent %s session abort --force' to discard", agentID, agentID),
	}
}

// SessionSyncCheck checks remote sync status.
type SessionSyncCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionSyncCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionSyncCheck creates a sync check.
func NewSessionSyncCheck(gitRoot string) *SessionSyncCheck {
	return &SessionSyncCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionSyncCheck) Name() string {
	return "synced with remote"
}

// Run executes the sync check.
func (c *SessionSyncCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	// only show this check if repo is cloned
	if !status.RepoCloned {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	if status.SyncStatus == "no remote configured" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "no remote",
			Fix:     "Configure remote to sync sessions across machines",
		}
	}

	if status.SyncedWithRemote {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: status.SyncStatus,
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: status.SyncStatus,
		Fix:     "Run 'ox sync' to push/pull changes",
	}
}

// SessionModeCheck validates the session_recording configuration.
type SessionModeCheck struct {
	gitRoot string
}

// NewSessionModeCheck creates a session recording validation check.
func NewSessionModeCheck(gitRoot string) *SessionModeCheck {
	return &SessionModeCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionModeCheck) Name() string {
	return "session recording"
}

// Run executes the session recording check.
func (c *SessionModeCheck) Run(ctx context.Context) CheckResult {
	resolved := config.ResolveSessionRecording(c.gitRoot)

	// format source for display
	sourceStr := formatModeSource(resolved.Source)

	// validate mode value
	if !config.IsValidSessionRecordingMode(resolved.Mode) {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: fmt.Sprintf("invalid mode %q", resolved.Mode),
			Fix:     "Set to valid value: ox config set session_recording <none|infra|all>",
		}
	}

	// mode is valid - show effective mode and source
	if resolved.Mode == config.SessionRecordingDisabled || resolved.Mode == "" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("none (%s)", sourceStr),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("%s (%s)", resolved.Mode, sourceStr),
	}
}

// SessionLedgerCheck verifies ledger is available when session recording requires it.
type SessionLedgerCheck struct {
	gitRoot string
}

// NewSessionLedgerCheck creates a ledger requirement check.
func NewSessionLedgerCheck(gitRoot string) *SessionLedgerCheck {
	return &SessionLedgerCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionLedgerCheck) Name() string {
	return "ledger for sessions"
}

// Run executes the ledger check.
func (c *SessionLedgerCheck) Run(ctx context.Context) CheckResult {
	resolved := config.ResolveSessionRecording(c.gitRoot)

	// skip if session recording is disabled
	if !resolved.ShouldRecord() {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check if ledger exists
	if ledger.Exists("") {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "available",
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: "not provisioned",
		Fix:     "Run 'ox doctor --fix' to clone ledger from cloud",
	}
}

// formatModeSource formats the SessionRecordingSource for display.
func formatModeSource(source config.SessionRecordingSource) string {
	switch source {
	case config.SessionRecordingSourceRepo:
		return "from .sageox/config.json"
	case config.SessionRecordingSourceTeam:
		return "from team config"
	case config.SessionRecordingSourceUser:
		return "from user config"
	case config.SessionRecordingSourceDefault:
		return "default"
	default:
		return string(source)
	}
}

// SessionOrphanedCheck detects orphaned .recording.json files.
type SessionOrphanedCheck struct {
	gitRoot      string
	cachedStatus *session.HealthStatus
}

func (c *SessionOrphanedCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionOrphanedCheck creates an orphaned recording check.
func NewSessionOrphanedCheck(gitRoot string) *SessionOrphanedCheck {
	return &SessionOrphanedCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionOrphanedCheck) Name() string {
	return "orphaned recordings"
}

// Run executes the orphaned recording check.
func (c *SessionOrphanedCheck) Run(ctx context.Context) CheckResult {
	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)

	// check for orphaned recordings (recording state exists but is very old)
	if !status.IsRecordingActive {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// a recording older than 24 hours without activity is likely orphaned
	if status.IsStaleRecording && status.StaleRecordingAge.Hours() > 24 {
		agentID := "unknown"
		if status.Recording != nil {
			agentID = status.Recording.AgentID
		}

		days := int(status.StaleRecordingAge.Hours()) / 24

		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: fmt.Sprintf("found %d-day old recording (%s)", days, agentID),
			Fix:     fmt.Sprintf("Run 'ox agent %s session recover' to upload to ledger, or 'ox agent %s session abort --force' to discard", agentID, agentID),
		}
	}

	return CheckResult{
		Name:   c.Name(),
		Status: StatusSkip,
	}
}

// ValidSessionRecordingModesString returns comma-separated list of valid modes.
func ValidSessionRecordingModesString() string {
	return strings.Join(config.ValidSessionRecordingModes, ", ")
}

// SessionIncompleteCheck detects sessions missing summary/html or uncommitted.
type SessionIncompleteCheck struct {
	gitRoot string
}

// NewSessionIncompleteCheck creates an incomplete session check.
func NewSessionIncompleteCheck(gitRoot string) *SessionIncompleteCheck {
	return &SessionIncompleteCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionIncompleteCheck) Name() string {
	return "incomplete sessions"
}

// IncompleteSessionIssue describes what's missing for a session file.
type IncompleteSessionIssue struct {
	Path           string
	MissingHTML    bool
	MissingSummary bool
	Untracked      bool
	Unstaged       bool
}

// Run executes the incomplete session check.
func (c *SessionIncompleteCheck) Run(ctx context.Context) CheckResult {
	// get ledger path using same pattern as SessionStorageCheck
	ledgerPath := c.getLedgerPath()
	if ledgerPath == "" {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check if ledger exists
	if !ledger.Exists(ledgerPath) {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// find incomplete sessions
	issues := c.findIncompleteSessionsInLedger(ledgerPath)
	if len(issues) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "none",
		}
	}

	// categorize issues
	missingHTML := 0
	missingSummary := 0
	uncommitted := 0

	for _, issue := range issues {
		if issue.MissingHTML {
			missingHTML++
		}
		if issue.MissingSummary {
			missingSummary++
		}
		if issue.Untracked || issue.Unstaged {
			uncommitted++
		}
	}

	// build message
	var parts []string
	if missingHTML > 0 {
		parts = append(parts, fmt.Sprintf("%d missing HTML", missingHTML))
	}
	if missingSummary > 0 {
		parts = append(parts, fmt.Sprintf("%d missing summary", missingSummary))
	}
	if uncommitted > 0 {
		parts = append(parts, fmt.Sprintf("%d uncommitted", uncommitted))
	}

	message := strings.Join(parts, ", ")

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarn,
		Message: fmt.Sprintf("%d incomplete: %s", len(issues), message),
		Fix:     "Run 'ox session stop' inside an agent to regenerate summaries, or 'ox session commit' to commit",
	}
}

// getLedgerPath returns the ledger path for this check's git root.
func (c *SessionIncompleteCheck) getLedgerPath() string {
	if c.gitRoot == "" {
		path, err := ledger.DefaultPath()
		if err != nil {
			return ""
		}
		return path
	}

	return ledgerPathFromProject(c.gitRoot)
}

// gitFileStatus holds the git status for a file or directory.
type gitFileStatus struct {
	untracked bool // file/dir is not tracked by git (status ??)
	staged    bool // file has changes staged for commit
	modified  bool // file has unstaged modifications in worktree
	isDir     bool // true if this status is for a directory (path ends with /)
}

// findIncompleteSessionsInLedger scans the ledger's sessions/ directory.
func (c *SessionIncompleteCheck) findIncompleteSessionsInLedger(ledgerPath string) []IncompleteSessionIssue {
	sessionsDir := filepath.Join(ledgerPath, "sessions")

	// check if sessions directory exists
	info, err := os.Stat(sessionsDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	// get git status for tracking info
	fileStatuses, untrackedDirs := c.getGitFileStatuses(ledgerPath)

	var issues []IncompleteSessionIssue

	// walk sessions directory looking for .jsonl files
	err = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip files we can't access
		}

		if info.IsDir() {
			return nil
		}

		// only check .jsonl files (session data files)
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		issue := c.checkSessionFile(path, ledgerPath, fileStatuses, untrackedDirs)
		if issue != nil {
			issues = append(issues, *issue)
		}

		return nil
	})

	if err != nil {
		return nil
	}

	return issues
}

// checkSessionFile checks if a session .jsonl file has its companion files.
func (c *SessionIncompleteCheck) checkSessionFile(jsonlPath, ledgerPath string, fileStatuses map[string]gitFileStatus, untrackedDirs []string) *IncompleteSessionIssue {
	// derive companion file paths
	basePath := strings.TrimSuffix(jsonlPath, ".jsonl")
	htmlPath := basePath + ".html"
	summaryPath := basePath + "_summary.md"

	// check what's missing
	_, htmlErr := os.Stat(htmlPath)
	_, summaryErr := os.Stat(summaryPath)

	missingHTML := os.IsNotExist(htmlErr)
	missingSummary := os.IsNotExist(summaryErr)

	// get relative path for git status check
	relPath, err := filepath.Rel(ledgerPath, jsonlPath)
	if err != nil {
		relPath = jsonlPath
	}

	// check git tracking status
	status, hasStatus := fileStatuses[relPath]

	// determine if this session has issues
	var untracked, unstaged bool

	if hasStatus {
		// file appears in git status output, meaning it has some pending change
		untracked = status.untracked
		unstaged = status.modified && !status.staged
	} else {
		// file doesn't appear in status output directly
		// check if it falls under an untracked directory
		for _, dir := range untrackedDirs {
			if strings.HasPrefix(relPath, dir) {
				untracked = true
				break
			}
		}
	}

	// only report if there are actual issues
	if !missingHTML && !missingSummary && !untracked && !unstaged {
		return nil
	}

	return &IncompleteSessionIssue{
		Path:           relPath,
		MissingHTML:    missingHTML,
		MissingSummary: missingSummary,
		Untracked:      untracked,
		Unstaged:       unstaged,
	}
}

// getGitFileStatuses returns a map of file paths to their git status,
// and a list of untracked directories (paths ending with /).
// Only files with pending changes appear in the map.
// Committed files with no changes are NOT in this map.
func (c *SessionIncompleteCheck) getGitFileStatuses(ledgerPath string) (map[string]gitFileStatus, []string) {
	statuses := make(map[string]gitFileStatus)
	var untrackedDirs []string

	// get status to find files with changes
	statusCmd := exec.Command("git", "-C", ledgerPath, "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return statuses, untrackedDirs
	}

	lines := strings.Split(strings.TrimSpace(string(statusOutput)), "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		// porcelain format: XY filename
		// X = index status, Y = worktree status
		// ' ' = unmodified, M = modified, A = added, D = deleted, R = renamed
		// C = copied, U = updated but unmerged, ? = untracked, ! = ignored
		indexStatus := line[0]
		worktreeStatus := line[1]
		filePath := strings.TrimSpace(line[3:])

		status := gitFileStatus{}

		// ?? means untracked
		if indexStatus == '?' && worktreeStatus == '?' {
			status.untracked = true

			// if path ends with /, it's an untracked directory
			// all files under it are also untracked
			if strings.HasSuffix(filePath, "/") {
				status.isDir = true
				untrackedDirs = append(untrackedDirs, filePath)
			}
		}

		// determine if file is staged
		// if index status is A (added), M (modified), D (deleted), R (renamed), C (copied)
		// then file has staged changes
		if indexStatus == 'A' || indexStatus == 'M' || indexStatus == 'D' || indexStatus == 'R' || indexStatus == 'C' {
			status.staged = true
		}

		// determine if file has unstaged modifications
		// if worktree status is M (modified) or D (deleted), there are unstaged changes
		if worktreeStatus == 'M' || worktreeStatus == 'D' {
			status.modified = true
		}

		statuses[filePath] = status
	}

	return statuses, untrackedDirs
}

// SessionAutoStageCheck detects and auto-stages session files in the ledger.
// This is a FixLevelAuto check that runs automatically without --fix flag.
type SessionAutoStageCheck struct {
	gitRoot string
}

// NewSessionAutoStageCheck creates a session auto-stage check.
func NewSessionAutoStageCheck(gitRoot string) *SessionAutoStageCheck {
	return &SessionAutoStageCheck{gitRoot: gitRoot}
}

// Name returns the check name.
func (c *SessionAutoStageCheck) Name() string {
	return "session files staged"
}

// Run executes the auto-stage check.
// This check automatically stages unstaged session files in the ledger's sessions/ directory.
func (c *SessionAutoStageCheck) Run(ctx context.Context) CheckResult {
	// get ledger path
	ledgerPath := c.getLedgerPath()
	if ledgerPath == "" {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check if ledger exists
	if !ledger.Exists(ledgerPath) {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check for unstaged session files
	unstaged := c.findUnstagedSessionFiles(ledgerPath)
	if len(unstaged) == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "all staged",
		}
	}

	// auto-stage the files (this is FixLevelAuto behavior)
	stagedCount, err := c.stageSessionFiles(ledgerPath, unstaged)
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: fmt.Sprintf("staging failed: %v", err),
			Fix:     fmt.Sprintf("Manual fix: git -C %s add sessions/", ledgerPath),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("staged %d file(s)", stagedCount),
	}
}

// getLedgerPath returns the ledger path for the project.
func (c *SessionAutoStageCheck) getLedgerPath() string {
	if c.gitRoot == "" {
		path, err := ledger.DefaultPath()
		if err != nil {
			return ""
		}
		return path
	}

	return ledgerPathFromProject(c.gitRoot)
}

// findUnstagedSessionFiles returns session files that are untracked or modified but not staged.
func (c *SessionAutoStageCheck) findUnstagedSessionFiles(ledgerPath string) []string {
	// run git status --porcelain to find unstaged files
	cmd := exec.Command("git", "-C", ledgerPath, "status", "--porcelain", "sessions/")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var files []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		// git status --porcelain format: XY filename
		// X = index status, Y = worktree status
		// ?? = untracked
		// _M = modified in worktree but not staged (space + M)
		// _A = added to worktree (for new files not yet staged)
		if len(line) < 3 {
			continue
		}

		status := line[:2]
		filename := strings.TrimSpace(line[3:])

		// check if this is an untracked directory (e.g., "?? sessions/")
		// when the entire sessions/ directory is untracked, git shows the directory
		// not the individual files - but we should still stage them
		if status == "??" && strings.HasSuffix(filename, "/") && strings.HasPrefix(filename, "sessions") {
			// directory is untracked - scan for actual session files
			sessionsDir := filepath.Join(ledgerPath, "sessions")
			if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
				sessionFiles := c.scanSessionFilesInDir(sessionsDir)
				files = append(files, sessionFiles...)
			}
			continue
		}

		// only process session files (.jsonl, .html, summary.md)
		if !isSessionFile(filename) {
			continue
		}

		// check if file needs staging:
		// ?? = untracked (needs git add)
		// " M" = modified but not staged (needs git add)
		// " A" = added but not staged
		if status == "??" || status[1] == 'M' || status[1] == 'A' || status[1] == 'D' {
			files = append(files, filename)
		}
	}

	return files
}

// scanSessionFilesInDir recursively finds session files in the given directory.
func (c *SessionAutoStageCheck) scanSessionFilesInDir(dir string) []string {
	var files []string

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// get relative path from ledger root (parent of sessions dir)
		ledgerPath := filepath.Dir(dir) // go up from sessions/ to ledger
		relPath, err := filepath.Rel(ledgerPath, path)
		if err != nil {
			return nil
		}

		if isSessionFile(relPath) {
			files = append(files, relPath)
		}
		return nil
	})

	return files
}

// isSessionFile returns true if the filename is a session-related file.
func isSessionFile(filename string) bool {
	// session files are in sessions/ directory
	if !strings.HasPrefix(filename, "sessions/") {
		return false
	}

	// check for known session file extensions/patterns
	return strings.HasSuffix(filename, ".jsonl") ||
		strings.HasSuffix(filename, ".html") ||
		strings.HasSuffix(filename, "summary.md") ||
		strings.HasSuffix(filename, "summary.json")
}

// stageSessionFiles stages the given session files.
func (c *SessionAutoStageCheck) stageSessionFiles(ledgerPath string, files []string) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}

	// stage files using git add
	// use sessions/ directory to catch all session files in one command
	cmd := exec.Command("git", "-C", ledgerPath, "add", "sessions/")
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("git add sessions/: %w", err)
	}

	return len(files), nil
}

// SessionPushCheck checks if local ledger is ahead of remote and can push.
// When fix=true, automatically pushes to remote.
type SessionPushCheck struct {
	gitRoot      string
	ledgerPath   string
	fix          bool
	cachedStatus *session.HealthStatus
}

func (c *SessionPushCheck) SetHealthStatus(status *session.HealthStatus) {
	c.cachedStatus = status
}

// NewSessionPushCheck creates a session push check.
// gitRoot is the project root, fix indicates whether to push when ahead.
func NewSessionPushCheck(gitRoot string, fix bool) *SessionPushCheck {
	return &SessionPushCheck{
		gitRoot: gitRoot,
		fix:     fix,
	}
}

// Name returns the check name.
func (c *SessionPushCheck) Name() string {
	return "session push"
}

// Run executes the session push check.
func (c *SessionPushCheck) Run(ctx context.Context) CheckResult {
	// get ledger path
	ledgerPath := c.getLedgerPath()
	if ledgerPath == "" {
		return CheckResult{
			Name:   c.Name(),
			Status: StatusSkip,
		}
	}

	// check if ledger is a git repo with remote
	if !c.hasRemote(ledgerPath) {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusSkip,
			Message: "no remote",
		}
	}

	// check if local is ahead of remote
	aheadCount := c.countCommitsAhead(ledgerPath)
	if aheadCount == 0 {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: "up to date",
		}
	}

	// local is ahead - need to push
	if !c.fix {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarn,
			Message: fmt.Sprintf("%d commit(s) to push", aheadCount),
			Fix:     "Run 'ox doctor --fix' to push session data to remote",
		}
	}

	// attempt to push with retries
	const maxRetries = 3
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := c.pushToRemote(ledgerPath); err != nil {
			lastErr = err
			// don't retry auth errors
			errStr := err.Error()
			if strings.Contains(errStr, "Permission denied") ||
				strings.Contains(errStr, "authentication") ||
				strings.Contains(errStr, "could not read Username") ||
				strings.Contains(errStr, "401") {
				return CheckResult{
					Name:    c.Name(),
					Status:  StatusFail,
					Message: "push failed (auth error)",
					Fix:     "Check git credentials - run `ox login` to refresh",
				}
			}
			// 403 = server rejected — could be permissions or stale token, don't retry
			if strings.Contains(errStr, "403") {
				return CheckResult{
					Name:    c.Name(),
					Status:  StatusFail,
					Message: "push rejected (HTTP 403)",
					Fix:     "Server rejected push - check remote permissions or run `ox login` to refresh token",
				}
			}
			continue
		}
		lastErr = nil
		break
	}
	if lastErr != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: fmt.Sprintf("push failed after %d retries", maxRetries),
			Fix:     fmt.Sprintf("Run `git -C %s push` to retry manually", ledgerPath),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("pushed %d commit(s)", aheadCount),
	}
}

// getLedgerPath returns the ledger path for the current project.
func (c *SessionPushCheck) getLedgerPath() string {
	if c.ledgerPath != "" {
		return c.ledgerPath
	}

	if c.gitRoot == "" {
		return ""
	}

	status := getOrComputeHealth(c.cachedStatus, c.gitRoot)
	if !status.RepoCloned {
		return ""
	}

	return status.RepoPath
}

// hasRemote checks if the ledger has a remote configured.
func (c *SessionPushCheck) hasRemote(ledgerPath string) bool {
	cmd := exec.Command("git", "-C", ledgerPath, "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// countCommitsAhead returns the number of commits local is ahead of remote.
// Returns 0 if not ahead or on error.
func (c *SessionPushCheck) countCommitsAhead(ledgerPath string) int {
	// get current branch
	branchCmd := exec.Command("git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return 0
	}
	branch := strings.TrimSpace(string(branchOutput))
	if branch == "HEAD" {
		return 0 // detached HEAD
	}

	// check if tracking branch exists
	trackingCmd := exec.Command("git", "-C", ledgerPath, "rev-parse", "--abbrev-ref", branch+"@{upstream}")
	if _, err := trackingCmd.Output(); err != nil {
		return 0 // no tracking branch
	}

	// count commits ahead using @{u}..HEAD
	aheadCmd := exec.Command("git", "-C", ledgerPath, "rev-list", "--count", "@{u}..HEAD")
	aheadOutput, err := aheadCmd.Output()
	if err != nil {
		return 0
	}

	var count int
	fmt.Sscanf(strings.TrimSpace(string(aheadOutput)), "%d", &count)
	return count
}

// pushToRemote pushes the ledger to remote.
func (c *SessionPushCheck) pushToRemote(ledgerPath string) error {
	cmd := exec.Command("git", "-C", ledgerPath, "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ledgerPathFromProject derives the ledger path from a project root.
// Returns empty string if project config cannot be loaded or has no repo ID.
func ledgerPathFromProject(gitRoot string) string {
	projectCfg, err := config.LoadProjectConfig(gitRoot)
	if err != nil || projectCfg == nil || projectCfg.RepoID == "" {
		return ""
	}
	ep := endpoint.GetForProject(gitRoot)
	return config.DefaultLedgerPath(projectCfg.RepoID, ep)
}
