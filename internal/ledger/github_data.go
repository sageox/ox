package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	Commits     []PRCommit  `json:"commits,omitempty"`
}

// PRCommit represents a commit from a PR's branch, fetched via the GitHub
// /repos/{owner}/{repo}/pulls/{number}/commits endpoint.
// Only fetched for merged PRs (immutable once merged).
type PRCommit struct {
	SHA    string    `json:"sha"`
	Author string    `json:"author"`
	Date   time.Time `json:"date"`
	Msg    string    `json:"message"`
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
	// KnownItems tracks the last-seen state and updated_at of each item by number.
	// Used to detect state transitions (open→closed/merged) and content updates
	// (new comments, edits) which trigger a re-extract.
	KnownItems map[int]KnownItem `json:"known_items,omitempty"`

	// KnownStates is the legacy format (state-only). Preserved for backward
	// compatibility when reading old cache files. On write, only KnownItems is used.
	KnownStates map[int]string `json:"known_states,omitempty"`
}

// KnownItem tracks the last-seen state and updated_at for a PR or issue.
type KnownItem struct {
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
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

// FieldRedactor is an optional function that scrubs credential patterns from
// user-authored text fields before a PR or Issue is serialized to disk.
// `internal/ledger` cannot import `internal/session` (cycle via session/health),
// so the secret redactor lives in a sibling package and is injected from above.
// When SetFieldRedactor has not been called, WriteGitHubPR/WriteGitHubIssue
// passes content through unchanged — but every CLI/daemon entry point that can
// reach these writers MUST set a redactor at startup (see cmd/ox/main.go).
//
// Per ox-8bkk: the forensic scan on 2026-05-10 found 16 Authorization: Bearer
// headers across 8 PR cache files. They came from PR descriptions and comments
// (user-authored text that pasted curl examples), not from any HTTP-header
// capture in ox itself. Redaction at the cache writer closes that hole.
type FieldRedactor func(string) string

var fieldRedactor FieldRedactor

// SetFieldRedactor installs the credential-redaction hook for cache writers.
// Intended to be called once at process startup. Idempotent; replaces any
// previously installed redactor.
func SetFieldRedactor(r FieldRedactor) {
	fieldRedactor = r
}

func redactField(s string) string {
	if fieldRedactor == nil {
		return s
	}
	return fieldRedactor(s)
}

// redactPR returns a copy of pr with user-authored text fields scrubbed.
// The caller's struct is not mutated.
func redactPR(pr *PRFile) *PRFile {
	if pr == nil {
		return nil
	}
	out := *pr
	out.Title = redactField(pr.Title)
	out.Body = redactField(pr.Body)
	if len(pr.Comments) > 0 {
		out.Comments = make([]PRComment, len(pr.Comments))
		for i, c := range pr.Comments {
			out.Comments[i] = c
			out.Comments[i].Body = redactField(c.Body)
		}
	}
	if len(pr.Commits) > 0 {
		out.Commits = make([]PRCommit, len(pr.Commits))
		for i, c := range pr.Commits {
			out.Commits[i] = c
			out.Commits[i].Msg = redactField(c.Msg)
		}
	}
	return &out
}

// redactIssue is the IssueFile analog of redactPR.
func redactIssue(issue *IssueFile) *IssueFile {
	if issue == nil {
		return nil
	}
	out := *issue
	out.Title = redactField(issue.Title)
	out.Body = redactField(issue.Body)
	if len(issue.Comments) > 0 {
		out.Comments = make([]IssueComment, len(issue.Comments))
		for i, c := range issue.Comments {
			out.Comments[i] = c
			out.Comments[i].Body = redactField(c.Body)
		}
	}
	return &out
}

// WriteGitHubPR writes a PR to its date-partitioned directory based on created_at.
// Creates the directory structure if it does not exist.
//
// Credentials in user-authored fields (Title, Body, Comments, commit messages)
// are redacted before serialization when a FieldRedactor has been installed via
// SetFieldRedactor. Per ox-8bkk, this closes the cache-side credential leak that
// forensic scans observed in PR JSON files.
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

	redacted := redactPR(pr)
	data, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal PR %d: %w", pr.Number, err)
	}

	hash := contentHash(data)
	filename := fmt.Sprintf("%d-%s.json", pr.Number, hash)
	path := filepath.Join(dir, filename)

	// idempotent: skip if a file with this exact hash already exists
	if _, err := os.Stat(path); err == nil {
		return nil
	}

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

	redacted := redactIssue(issue)
	data, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal issue %d: %w", issue.Number, err)
	}

	hash := contentHash(data)
	filename := fmt.Sprintf("%d-%s.json", issue.Number, hash)
	path := filepath.Join(dir, filename)

	// idempotent: skip if a file with this exact hash already exists
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write issue %d: %w", issue.Number, err)
	}

	return nil
}

// ReadGitHubPR reads an existing PR JSON file from the ledger by number and creation date.
// When multiple content-hash versions exist ({number}-{hash}.json), returns the one
// with the latest updated_at field. Falls back to legacy {number}.json for backward
// compatibility. Returns os.ErrNotExist if no file exists.
func ReadGitHubPR(ledgerPath string, number int, createdAt time.Time) (*PRFile, error) {
	dir := DateDir(ledgerPath, createdAt, "pr")
	path, err := findLatestFile(dir, number)
	if err != nil {
		return nil, fmt.Errorf("read PR %d: %w", number, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read PR %d: %w", number, err)
	}
	var pr PRFile
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal PR %d: %w", number, err)
	}
	return &pr, nil
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
				KnownItems: make(map[int]KnownItem),
			}, nil
		}
		return nil, fmt.Errorf("read %s sync state: %w", dataType, err)
	}

	var state GitHubTypeSyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal %s sync state: %w", dataType, err)
	}

	// Migrate legacy KnownStates → KnownItems on read
	if state.KnownItems == nil && len(state.KnownStates) > 0 {
		state.KnownItems = make(map[int]KnownItem, len(state.KnownStates))
		for num, st := range state.KnownStates {
			state.KnownItems[num] = KnownItem{State: st}
		}
	}
	if state.KnownItems == nil {
		state.KnownItems = make(map[int]KnownItem)
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
// dataType should be "pr" or "issue". Returns paths including both legacy ({number}.json)
// and content-hash ({number}-{hash}.json) formats. Multiple files for the same number
// may be returned — callers should deduplicate if needed.
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

// rebuildSyncStateFromDisk reconstructs a GitHubTypeSyncState from existing
// JSON files in the ledger. This handles the cold-start case where the cache
// file is missing (first run, daemon reclone, cache cleared) but the data
// files already exist on disk from a prior sync.
func rebuildSyncStateFromDisk(ledgerPath, dataType string, logger *slog.Logger) (*GitHubTypeSyncState, error) {
	files, err := ListGitHubDataFiles(ledgerPath, dataType)
	if err != nil {
		return nil, fmt.Errorf("list %s files: %w", dataType, err)
	}
	if len(files) == 0 {
		return &GitHubTypeSyncState{KnownItems: make(map[int]KnownItem)}, nil
	}

	state := &GitHubTypeSyncState{
		KnownItems: make(map[int]KnownItem, len(files)),
	}
	var latestUpdate time.Time

	// Track per-number latest updated_at for deduplication across hash variants
	type itemInfo struct {
		state     string
		updatedAt time.Time
	}
	bestByNumber := make(map[int]itemInfo, len(files))

	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.Debug("rebuild sync state: skip unreadable file", "path", path, "error", readErr)
			continue
		}

		// lightweight parse — only extract the fields we need
		var stub struct {
			Number    int       `json:"number"`
			State     string    `json:"state"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if jsonErr := json.Unmarshal(data, &stub); jsonErr != nil {
			logger.Debug("rebuild sync state: skip unparseable file", "path", path, "error", jsonErr)
			continue
		}

		// deduplicate: keep only the version with the latest updated_at per number
		prev, exists := bestByNumber[stub.Number]
		if !exists || stub.UpdatedAt.After(prev.updatedAt) {
			bestByNumber[stub.Number] = itemInfo{state: stub.State, updatedAt: stub.UpdatedAt}
		}

		if stub.UpdatedAt.After(latestUpdate) {
			latestUpdate = stub.UpdatedAt
		}
	}

	for num, info := range bestByNumber {
		state.KnownItems[num] = KnownItem{State: info.state, UpdatedAt: info.updatedAt}
	}

	state.Count = len(state.KnownItems)
	state.LastSyncAt = latestUpdate

	return state, nil
}

// contentHash returns the first 8 hex characters of the SHA256 hash of data.
func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])[:8]
}

// hashFilePattern matches content-hash filenames: {number}-{8hexchars}.json
var hashFilePattern = regexp.MustCompile(`^(\d+)-([0-9a-f]{8})\.json$`)

// parseHashFilename extracts the number from a content-hash filename.
// Returns -1 if the filename doesn't match the pattern.
func parseHashFilename(name string) int {
	m := hashFilePattern.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// findLatestFile finds the most recent version of a numbered file in dir.
// Looks for {number}-{hash}.json files and picks the one with the latest
// updated_at. Falls back to legacy {number}.json for backward compatibility.
// Returns os.ErrNotExist if no matching file is found.
func findLatestFile(dir string, number int) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", err
	}

	prefix := fmt.Sprintf("%d-", number)
	var bestPath string
	var bestUpdated time.Time
	var bestMtime time.Time
	found := false

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// match {number}-{hash}.json
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		if parseHashFilename(name) != number {
			continue
		}

		path := filepath.Join(dir, name)
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		mtime := info.ModTime()

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}

		var stub struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		if jsonErr := json.Unmarshal(data, &stub); jsonErr != nil {
			continue
		}

		// prefer latest updated_at; break ties with filesystem mtime
		better := !found ||
			stub.UpdatedAt.After(bestUpdated) ||
			(stub.UpdatedAt.Equal(bestUpdated) && mtime.After(bestMtime))
		if better {
			bestPath = path
			bestUpdated = stub.UpdatedAt
			bestMtime = mtime
			found = true
		}
	}

	if found {
		return bestPath, nil
	}

	// backward compatibility: try legacy {number}.json
	legacyPath := filepath.Join(dir, fmt.Sprintf("%d.json", number))
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath, nil
	}

	return "", os.ErrNotExist
}

// ComputeGitHubDataPaths returns sparse checkout patterns for the last N days
// of GitHub data. Used by ConfigureSparseCheckout() to include recent data.
func ComputeGitHubDataPaths(days int) []string {
	now := time.Now().UTC()
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
