package daemon

import (
	"slices"
	"sync"
	"time"
)

// DaemonIssue represents something the daemon cannot resolve with deterministic code.
// If the daemon could fix it programmatically, it already would have.
// These issues require LLM reasoning or human judgment to resolve.
//
// Design principles:
//   - Issue granularity is (Type, Repo), not file-level. The daemon flags that a repo
//     has a problem; the LLM investigates the repo to understand details.
//   - No "info" severity level. If something is just informational, the daemon doesn't
//     need help - it's a notification, not a request for reasoning.
//   - Severity drives CLI behavior: warning=mention, error=fix now, critical=urgent.
//   - RequiresConfirm separates urgency from authority: some issues need human approval
//     even if the agent could technically attempt a fix.
type DaemonIssue struct {
	// Type categorizes the issue.
	// Examples: "merge_conflict", "missing_scaffolding", "diverged", "auth_expiring"
	Type string `json:"type"`

	// Severity indicates urgency. No "info" level exists.
	//   - "warning": should address soon, not blocking operations
	//   - "error": blocking normal operation, agent should fix now
	//   - "critical": data at risk, urgent attention required
	Severity string `json:"severity"`

	// Repo identifies which repository has the issue.
	// Examples: "ledger", "team-context-abc123"
	// Empty string for global issues (e.g., auth expiring).
	Repo string `json:"repo,omitempty"`

	// Summary is a human-readable one-liner for display.
	// The LLM will investigate the repo directly to understand details.
	Summary string `json:"summary"`

	// Since tracks when the issue was first detected.
	// Useful for understanding how long an issue has been outstanding.
	Since time.Time `json:"since"`

	// RequiresConfirm indicates the resolution needs human approval before execution.
	// When true, the agent should propose a fix and wait for user confirmation.
	// When false, the agent can attempt to resolve automatically.
	// This separates urgency (Severity) from authority (who decides).
	RequiresConfirm bool `json:"requires_confirm,omitempty"`
}

// FormatLine returns a formatted single-line representation of the issue.
// If includeSeverity is true, includes the severity tag in brackets.
func (i DaemonIssue) FormatLine(includeSeverity bool) string {
	var line string
	if includeSeverity {
		line = "[" + i.Severity + "] "
	}

	if i.Repo != "" {
		line += i.Repo + ": "
	}

	line += i.Summary

	if i.RequiresConfirm {
		line += " [CONFIRM REQUIRED]"
	}

	return line
}

// HasConfirmRequired returns true if any issue in the slice requires confirmation.
func HasConfirmRequired(issues []DaemonIssue) bool {
	for _, issue := range issues {
		if issue.RequiresConfirm {
			return true
		}
	}
	return false
}

// MaxIssueSeverity returns the highest severity among the given issues.
// Returns empty string if the slice is empty.
func MaxIssueSeverity(issues []DaemonIssue) string {
	maxRank := 0
	maxSeverity := ""
	for _, issue := range issues {
		rank := severityRank(issue.Severity)
		if rank > maxRank {
			maxRank = rank
			maxSeverity = issue.Severity
		}
	}
	return maxSeverity
}

// Severity constants for DaemonIssue.
// No "info" level - if the daemon needs help, it's at least a warning.
const (
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// Issue type constants.
const (
	IssueTypeMergeConflict      = "merge_conflict"
	IssueTypeMissingScaffolding = "missing_scaffolding"
	IssueTypeDiverged           = "diverged"
	IssueTypeAuthExpiring       = "auth_expiring"
	IssueTypeGitLock            = "git_lock"
	IssueTypeCloneFailed        = "clone_failed"
	IssueTypeSyncBackoff        = "sync_backoff"
	IssueTypeDirtyWorkspace     = "dirty_workspace"
)

// severityRank returns a numeric rank for sorting (higher = more severe).
func severityRank(severity string) int {
	switch severity {
	case SeverityCritical:
		return 3
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}

// IssueTracker maintains the daemon's issue cache.
//
// Design: The daemon detects issues during sync operations and caches them in memory.
// CLI reads are O(1) memory access - no blocking on git operations. This is critical
// because CLI commands block the agent's event loop and must be fast (< 1ms).
//
// Thread safety: Sync loop writes, IPC handlers read. RWMutex allows concurrent reads.
//
// Deduplication: Only one issue per (Type, Repo) combination. If the same issue is
// set again, it updates the existing entry (e.g., severity might change).
type IssueTracker struct {
	mu     sync.RWMutex
	issues []DaemonIssue
}

// NewIssueTracker creates a new issue tracker.
func NewIssueTracker() *IssueTracker {
	return &IssueTracker{
		issues: make([]DaemonIssue, 0),
	}
}

// NeedsHelp returns true if any issues exist.
// This is the fast-path check for CLI - just reading a length.
func (t *IssueTracker) NeedsHelp() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.issues) > 0
}

// GetIssues returns a copy of all issues, sorted by severity (critical first).
// Returns a copy to prevent races with concurrent modifications.
func (t *IssueTracker) GetIssues() []DaemonIssue {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.issues) == 0 {
		return nil
	}

	// Return sorted copy
	result := slices.Clone(t.issues)
	slices.SortFunc(result, func(a, b DaemonIssue) int {
		// Sort by severity descending (critical first)
		return severityRank(b.Severity) - severityRank(a.Severity)
	})
	return result
}

// SetIssue adds or updates an issue.
// Deduplicates by (Type, Repo) - only one issue per combination exists.
// If an issue with the same (Type, Repo) exists, it is updated.
func (t *IssueTracker) SetIssue(issue DaemonIssue) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Look for existing issue with same (Type, Repo)
	for i, existing := range t.issues {
		if existing.Type == issue.Type && existing.Repo == issue.Repo {
			// Update existing - preserve original Since time unless explicitly set
			if issue.Since.IsZero() {
				issue.Since = existing.Since
			}
			t.issues[i] = issue
			return
		}
	}

	// New issue - set Since if not already set
	if issue.Since.IsZero() {
		issue.Since = time.Now()
	}
	t.issues = append(t.issues, issue)
}

// ClearIssue removes an issue by type and repo.
// No-op if the issue doesn't exist.
func (t *IssueTracker) ClearIssue(issueType, repo string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.issues = slices.DeleteFunc(t.issues, func(issue DaemonIssue) bool {
		return issue.Type == issueType && issue.Repo == repo
	})
}

// ClearRepo removes all issues for a specific repo.
// Useful when a repo is removed or all its issues are resolved.
func (t *IssueTracker) ClearRepo(repo string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.issues = slices.DeleteFunc(t.issues, func(issue DaemonIssue) bool {
		return issue.Repo == repo
	})
}

// Clear removes all issues.
func (t *IssueTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.issues = t.issues[:0]
}

// Count returns the number of issues.
func (t *IssueTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.issues)
}

// MaxSeverity returns the highest severity among all issues.
// Returns empty string if no issues exist.
func (t *IssueTracker) MaxSeverity() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	maxRank := 0
	maxSeverity := ""
	for _, issue := range t.issues {
		rank := severityRank(issue.Severity)
		if rank > maxRank {
			maxRank = rank
			maxSeverity = issue.Severity
		}
	}
	return maxSeverity
}
