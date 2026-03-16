package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PRFile represents the JSON structure stored in ledger/data/github/YYYY/MM/DD/pr/NNN.json
type PRFile struct {
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	Author      string      `json:"author"`
	State       string      `json:"state"` // "open", "closed", "merged"
	Labels      []string    `json:"labels,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	MergedAt    *time.Time  `json:"merged_at,omitempty"`
	ClosedAt    *time.Time  `json:"closed_at,omitempty"`
	UpdatedAt   time.Time   `json:"updated_at"`
	MergeCommit string      `json:"merge_commit,omitempty"`
	URL         string      `json:"url"`
	Comments    []PRComment `json:"comments,omitempty"`
}

// PRComment represents a comment on a pull request (issue comment or review comment).
type PRComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Path      string    `json:"path,omitempty"` // file path for review comments
	Line      *int      `json:"line,omitempty"` // line number for review comments
	CreatedAt time.Time `json:"created_at"`
}

// IssueFile represents the JSON structure stored in ledger/data/github/YYYY/MM/DD/issue/NNN.json
type IssueFile struct {
	Number    int            `json:"number"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	Author    string         `json:"author"`
	State     string         `json:"state"` // "open", "closed"
	Labels    []string       `json:"labels,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ClosedAt  *time.Time     `json:"closed_at,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
	URL       string         `json:"url"`
	Comments  []IssueComment `json:"comments,omitempty"`
}

// IssueComment represents a comment on an issue.
type IssueComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// GitHubTypeSyncState tracks incremental sync progress for a single GitHub data type
// (PRs or issues). Each type gets its own file so --full --prs-only doesn't affect
// issue sync cursors and vice versa.
//
// Stored locally in .sageox/cache/github_sync/ (NOT in the ledger) because:
//   - It changes on every sync, creating unnecessary merge conflicts
//   - It contains per-machine state (timestamps, known state maps)
//   - It does not need to be shared across coworkers
type GitHubTypeSyncState struct {
	LastSyncAt time.Time `json:"last_sync_at"`
	Count      int       `json:"count"`
	// KnownStates tracks the last-seen state of each item by number.
	// Used to detect state transitions (open→closed/merged) which trigger
	// a full re-extract of all comments.
	KnownStates map[int]string `json:"known_states,omitempty"`
}

const (
	githubDataDir      = "data/github"
	prSyncStateFile    = "pr_sync_state.json"
	issueSyncStateFile = "issue_sync_state.json"
	githubSyncCacheDir = "github_sync"

	// DefaultGitHubDataWindowDays controls how many days of GitHub data to keep
	// checked out locally via sparse checkout. Older data lives in git history
	// and is available for cloud-side long-term search. Keep small to minimize
	// local disk usage (<10MB target per ledger).
	DefaultGitHubDataWindowDays = 30
)

// GitHubDataDir returns the path to data/github/ within a ledger.
func GitHubDataDir(ledgerPath string) string {
	return filepath.Join(ledgerPath, githubDataDir)
}

// DateDir returns data/github/YYYY/MM/DD/<dataType>/ for a given timestamp.
func DateDir(ledgerPath string, t time.Time, dataType string) string {
	return filepath.Join(ledgerPath, githubDataDir,
		fmt.Sprintf("%d", t.Year()),
		fmt.Sprintf("%02d", t.Month()),
		fmt.Sprintf("%02d", t.Day()),
		dataType,
	)
}

// WriteGitHubPR writes a PR to its date-partitioned directory based on created_at.
// Creates the directory structure if it does not exist.
//
// Design decision: files stay in the created_at date directory permanently — we do NOT
// move them on close/merge. This means long-lived open issues may fall outside the
// sliding sparse-checkout window and drop out of local search. Accepted tradeoff:
// simplicity (no git mv, no reindex, no broken references) outweighs losing visibility
// on very old open items. Re-evaluate if this becomes a real problem.
func WriteGitHubPR(ledgerPath string, pr *PRFile) error {
	dir := DateDir(ledgerPath, pr.CreatedAt, "pr")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create pr dir: %w", err)
	}

	data, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal PR %d: %w", pr.Number, err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%d.json", pr.Number))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write PR %d: %w", pr.Number, err)
	}

	return nil
}

// WriteGitHubIssue writes an issue to its date-partitioned directory based on created_at.
// Creates the directory structure if it does not exist.
//
// Design decision: files stay in the created_at date directory permanently.
// See WriteGitHubPR for rationale.
func WriteGitHubIssue(ledgerPath string, issue *IssueFile) error {
	dir := DateDir(ledgerPath, issue.CreatedAt, "issue")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create issue dir: %w", err)
	}

	data, err := json.MarshalIndent(issue, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal issue %d: %w", issue.Number, err)
	}

	path := filepath.Join(dir, fmt.Sprintf("%d.json", issue.Number))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write issue %d: %w", issue.Number, err)
	}

	return nil
}

// GitHubSyncCacheDir returns the local cache directory for GitHub sync state.
// Path: <ledger>/.sageox/cache/github_sync/ — gitignored, local-only, not committed.
// Uses the ledger path (not project root) so the cursor is shared across worktrees.
func GitHubSyncCacheDir(ledgerPath string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", githubSyncCacheDir)
}

// ReadGitHubTypeSyncState reads the sync cursor for a specific data type.
// dataType should be "pr" or "issue".
// Returns a zero-value state (not an error) if the file does not exist.
func ReadGitHubTypeSyncState(ledgerPath, dataType string) (*GitHubTypeSyncState, error) {
	path := filepath.Join(GitHubSyncCacheDir(ledgerPath), syncStateFile(dataType))

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &GitHubTypeSyncState{
				KnownStates: make(map[int]string),
			}, nil
		}
		return nil, fmt.Errorf("read %s sync state: %w", dataType, err)
	}

	var state GitHubTypeSyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal %s sync state: %w", dataType, err)
	}
	if state.KnownStates == nil {
		state.KnownStates = make(map[int]string)
	}

	return &state, nil
}

// WriteGitHubTypeSyncState writes the sync cursor for a specific data type.
// dataType should be "pr" or "issue".
func WriteGitHubTypeSyncState(ledgerPath, dataType string, state *GitHubTypeSyncState) error {
	dir := GitHubSyncCacheDir(ledgerPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create github sync cache dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s sync state: %w", dataType, err)
	}

	path := filepath.Join(dir, syncStateFile(dataType))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s sync state: %w", dataType, err)
	}

	return nil
}

// ResetGitHubTypeSyncState removes the sync state file for a specific data type,
// causing the next sync to re-fetch everything within the --days window.
func ResetGitHubTypeSyncState(ledgerPath, dataType string) error {
	path := filepath.Join(GitHubSyncCacheDir(ledgerPath), syncStateFile(dataType))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s sync state: %w", dataType, err)
	}
	return nil
}

func syncStateFile(dataType string) string {
	switch dataType {
	case "pr":
		return prSyncStateFile
	case "issue":
		return issueSyncStateFile
	default:
		return dataType + "_sync_state.json"
	}
}

// ListGitHubDataFiles returns all JSON file paths under data/github/ matching the given type.
// dataType should be "pr" or "issue". Returns paths like data/github/2026/03/11/pr/178.json.
// Returns an empty slice (not an error) if no files exist.
func ListGitHubDataFiles(ledgerPath string, dataType string) ([]string, error) {
	baseDir := GitHubDataDir(ledgerPath)

	var paths []string
	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		// match pattern: .../YYYY/MM/DD/<dataType>/NNN.json
		if !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}
		// check parent dir matches dataType
		parentDir := filepath.Base(filepath.Dir(path))
		if parentDir == dataType {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("walk github data dir: %w", err)
	}

	return paths, nil
}

// ComputeGitHubDataPaths returns sparse checkout patterns for the last N days
// of GitHub data. Used by ConfigureSparseCheckout() to include recent data.
func ComputeGitHubDataPaths(days int) []string {
	now := time.Now()
	seen := make(map[string]bool)
	var paths []string

	for i := 0; i < days; i++ {
		t := now.AddDate(0, 0, -i)
		p := fmt.Sprintf("data/github/%d/%02d/%02d/", t.Year(), t.Month(), t.Day())
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	return paths
}
