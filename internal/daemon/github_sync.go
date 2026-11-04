package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
)

// GitHubSyncManager handles automatic GitHub PR/issue mirroring in the daemon.
type GitHubSyncManager struct {
	projectRoot string
	logger      *slog.Logger
	ledgerMu    *sync.Mutex // shared with SyncScheduler; guards all ledger git ops
	issues      *IssueTracker
	codedb      *CodeDBManager       // trigger indexing after extraction
	mirror      *GitHubMirrorRelayer // bulletin-board mirror

	// ledgerFetcher builds the GitHub reader for the Ledger sync; nil means the
	// real client. A field so tests can run a full cycle without the network.
	ledgerFetcher func(token string) ledger.GitHubFetcher

	mu           sync.Mutex
	syncing      bool
	lastSync     time.Time
	lastErr      error
	owner, repo  string    // cached from first detection
	failCount    int       // consecutive failures for backoff
	backoffUntil time.Time // rate limit or auth backoff
}

// GitHubSyncStats exposes sync status for ox status / IPC.
type GitHubSyncStats struct {
	LastSync  time.Time `json:"last_sync,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Syncing   bool      `json:"syncing"`
	Owner     string    `json:"owner,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	// Mirror is the bulletin-board mirror's status; absent while the mirror is
	// off and has never run.
	Mirror *GitHubMirrorStats `json:"mirror,omitempty"`
}

// NewGitHubSyncManager creates a new sync manager.
func NewGitHubSyncManager(projectRoot string, ledgerMu *sync.Mutex, logger *slog.Logger) *GitHubSyncManager {
	return &GitHubSyncManager{
		projectRoot: projectRoot,
		logger:      logger,
		ledgerMu:    ledgerMu,
	}
}

// SetIssueTracker sets the issue tracker for reporting auth/sync problems.
func (m *GitHubSyncManager) SetIssueTracker(tracker *IssueTracker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.issues = tracker
}

// SetCodeDBManager sets the CodeDB manager so indexing is triggered
// immediately after extraction (rather than waiting for the next 5m cycle).
func (m *GitHubSyncManager) SetCodeDBManager(codedb *CodeDBManager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codedb = codedb
}

// SetMirrorRelayer sets the bulletin-board mirror that runs at the end of every
// sync cycle. nil (the default) leaves the mirror off.
func (m *GitHubSyncManager) SetMirrorRelayer(r *GitHubMirrorRelayer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mirror = r
}

// Status returns current sync status.
func (m *GitHubSyncManager) Status() GitHubSyncStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := GitHubSyncStats{
		LastSync: m.lastSync,
		Syncing:  m.syncing,
		Owner:    m.owner,
		Repo:     m.repo,
	}
	if m.lastErr != nil {
		stats.LastError = m.lastErr.Error()
	}
	stats.Mirror = m.mirror.Stats()
	if stats.Mirror != nil {
		stats.LastSync = stats.Mirror.LastSuccess
		stats.LastError = stats.Mirror.LastError
	}
	return stats
}

// CheckAndSync runs a non-blocking GitHub sync if conditions are met.
func (m *GitHubSyncManager) CheckAndSync(ctx context.Context, ledgerPath string) {
	m.mu.Lock()
	if m.syncing || m.mirror == nil {
		m.mu.Unlock()
		return
	}

	m.syncing = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.syncing = false
			m.mu.Unlock()
		}()

		m.runCycle(ctx, ledgerPath)
	}()
}

// doSync runs one mirror cycle.
func (m *GitHubSyncManager) doSync(ctx context.Context, ledgerPath string) {
	m.runCycle(ctx, ledgerPath)
}

// runCycle resolves GitHub credentials and settings before relaying a mirror.
func (m *GitHubSyncManager) runCycle(ctx context.Context, ledgerPath string) {
	// check config toggle
	if config.ResolveGitHubSync(m.projectRoot) == config.GitHubSyncDisabled {
		m.logger.Debug("github sync disabled via config")
		return
	}

	// resolve token
	token := identity.GetGitHubToken()
	if token == "" {
		m.logger.Debug("github sync skipped: no token available")
		return
	}

	// detect GitHub remote (cached after first detection)
	owner, repo, err := m.resolveRemote()
	if err != nil {
		m.logger.Debug("github sync skipped: no GitHub remote", "error", err)
		return
	}

	// determine what to sync
	syncPRs := config.ResolveGitHubSyncPRs(m.projectRoot) == config.GitHubSyncEnabled
	syncIssues := config.ResolveGitHubSyncIssues(m.projectRoot) == config.GitHubSyncEnabled
	if !syncPRs && !syncIssues {
		m.logger.Debug("github sync skipped: both PR and issue sync disabled")
		return
	}

	defer m.relayMirror(ctx, MirrorTarget{
		RepoID:       config.GetRepoID(m.projectRoot),
		LedgerPath:   ledgerPath,
		Owner:        owner,
		Repo:         repo,
		GitHubToken:  token,
		PullRequests: syncPRs,
		Issues:       syncIssues,
	})

}

// relayMirror contains panics so a failed mirror cannot terminate the daemon.
func (m *GitHubSyncManager) relayMirror(ctx context.Context, target MirrorTarget) {
	m.mu.Lock()
	relayer := m.mirror
	m.mu.Unlock()
	if relayer == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			m.logger.Error("github mirror cycle panicked", "panic", fmt.Sprint(rec))
		}
	}()
	relayer.Run(ctx, target)
}

// resolveRemote detects the GitHub owner/repo from git remotes.
// Results are cached after first successful detection.
func (m *GitHubSyncManager) resolveRemote() (string, string, error) {
	m.mu.Lock()
	if m.owner != "" && m.repo != "" {
		owner, repo := m.owner, m.repo
		m.mu.Unlock()
		return owner, repo, nil
	}
	m.mu.Unlock()

	// offline-safe: returns error for local-only repos; GitHub sync skips entirely
	urls, err := repotools.GetRemoteURLsForDir(m.projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("get git remotes: %w", err)
	}

	for _, url := range urls {
		o, r, ok := gh.ParseGitHubRemote(url)
		if ok {
			m.mu.Lock()
			m.owner = o
			m.repo = r
			m.mu.Unlock()
			return o, r, nil
		}
	}

	return "", "", fmt.Errorf("no GitHub remote found")
}

// handlePushError records a failed commit/push. The data is committed locally
// and rides out with the next push that gets through, so a wedged ledger is not
// a GitHub failure: counting it would suspend GitHub sync for good once
// failCount passes its limit, long after the ledger recovered.
func (m *GitHubSyncManager) handlePushError(pushErr error) {
	if errors.Is(pushErr, gitutil.ErrPushWedged) {
		m.logger.Debug("github sync push deferred: ledger push wedged", "error", pushErr)
		return
	}
	m.handleError(fmt.Errorf("commit/push: %w", pushErr))
}

func (m *GitHubSyncManager) handleError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastErr = err
	m.failCount++

	if errors.Is(err, gh.ErrGitHubAuth) {
		m.backoffUntil = time.Now().Add(1 * time.Hour)
		m.logger.Warn("github auth failed, backing off 1h", "error", err)
		if m.issues != nil {
			// Surface the specific API error (e.g. "Resource not accessible by
			// personal access token") instead of a generic message.
			summary := "GitHub authentication failed — check GITHUB_TOKEN or gh auth login"
			if detail := extractGitHubDetail(err); detail != "" {
				summary = fmt.Sprintf("GitHub authentication failed — %s", detail)
			}
			m.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeGitHubAuth,
				Severity: SeverityWarning,
				Summary:  summary,
			})
		}
		return
	}

	if errors.Is(err, gh.ErrGitHubRateLimited) {
		m.backoffUntil = time.Now().Add(30 * time.Minute)
		m.logger.Warn("github rate limited, backing off 30m", "error", err)
		return
	}

	m.logger.Warn("github sync failed", "error", err, "fail_count", m.failCount)
}

// extractGitHubDetail extracts the human-readable message from a GitHub API
// error. The error chain from the client looks like:
//
//	"sync PRs: list PRs: github authentication failed: status 403: {\"message\":\"...\",…}"
//
// We try to parse the JSON body first; if that fails we fall back to the raw
// text after the status code prefix.
func extractGitHubDetail(err error) string {
	s := err.Error()

	// Find the JSON body embedded in the error string.
	idx := strings.Index(s, "{\"")
	if idx >= 0 {
		var body struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(s[idx:]), &body) == nil && body.Message != "" {
			return body.Message
		}
	}

	// Fallback: grab text after "status NNN: "
	if i := strings.Index(s, ": status "); i >= 0 {
		rest := s[i+2:] // "status NNN: …"
		if j := strings.Index(rest, ": "); j >= 0 {
			return rest[j+2:]
		}
	}

	return ""
}
