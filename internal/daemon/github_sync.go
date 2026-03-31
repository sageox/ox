package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	gh "github.com/sageox/ox/internal/github"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/identity"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/repotools"
)

// GitHubSyncManager handles automatic GitHub PR/issue sync in the daemon.
// It fetches from the GitHub API, writes JSON files to the ledger, and
// commits+pushes so the data is available for distillation and cross-coworker search.
type GitHubSyncManager struct {
	projectRoot string
	logger      *slog.Logger
	ledgerMu    *sync.Mutex // shared with SyncScheduler; guards all ledger git ops
	issues      *IssueTracker
	codedb      *CodeDBManager // trigger indexing after extraction

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
	return stats
}

// CheckAndSync runs a non-blocking GitHub sync if conditions are met.
func (m *GitHubSyncManager) CheckAndSync(ctx context.Context, ledgerPath string) {
	m.mu.Lock()
	if m.syncing {
		m.mu.Unlock()
		return
	}
	if time.Now().Before(m.backoffUntil) {
		m.logger.Debug("github sync in backoff", "until", m.backoffUntil.Format(time.RFC3339))
		m.mu.Unlock()
		return
	}
	if m.failCount > 5 {
		m.logger.Debug("github sync suspended after too many failures", "fail_count", m.failCount)
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

		m.doSync(ctx, ledgerPath)
	}()
}

func (m *GitHubSyncManager) doSync(ctx context.Context, ledgerPath string) {
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

	m.logger.Info("starting github sync", "owner", owner, "repo", repo)
	start := time.Now()

	fetcher := gh.NewFetcher(gh.NewClient(token))
	maxDays := ledger.DefaultGitHubDataWindowDays
	combined := &ledger.SyncResult{}

	if syncPRs {
		prResult, prErr := ledger.SyncPRs(ctx, fetcher, ledgerPath, owner, repo, maxDays, m.logger)
		if prErr != nil {
			m.handleError(fmt.Errorf("sync PRs: %w", prErr))
			return
		}
		combined.PRTotal = prResult.PRTotal
		combined.PRCreated = prResult.PRCreated
		combined.PRUpdated = prResult.PRUpdated
	}

	if syncIssues {
		issueResult, issueErr := ledger.SyncIssues(ctx, fetcher, ledgerPath, owner, repo, maxDays, m.logger)
		if issueErr != nil {
			m.handleError(fmt.Errorf("sync issues: %w", issueErr))
			return
		}
		combined.IssueTotal = issueResult.IssueTotal
		combined.IssueCreated = issueResult.IssueCreated
		combined.IssueUpdated = issueResult.IssueUpdated
	}

	totalItems := combined.PRTotal + combined.IssueTotal
	if totalItems > 0 {
		// acquire ledger mutex before git operations
		m.ledgerMu.Lock()
		pushErr := func() error {
			defer m.ledgerMu.Unlock()
			return ledger.CommitAndPushGitHubData(ctx, ledgerPath, owner, repo, combined, m.pushLedger)
		}()

		if pushErr != nil {
			m.handleError(fmt.Errorf("commit/push: %w", pushErr))
			return
		}
	}

	duration := time.Since(start)
	m.mu.Lock()
	m.lastSync = time.Now()
	m.lastErr = nil
	m.failCount = 0
	m.mu.Unlock()

	m.logger.Info("github sync completed",
		"prs", combined.PRTotal, "issues", combined.IssueTotal,
		"duration", duration.Round(time.Millisecond))

	// trigger immediate CodeDB indexing so new PRs/issues are searchable
	// without waiting for the next 5m freshness check cycle
	if totalItems > 0 && m.codedb != nil {
		m.codedb.CheckFreshness(ctx)
	}
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

func (m *GitHubSyncManager) handleError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastErr = err
	m.failCount++

	if errors.Is(err, gh.ErrGitHubAuth) {
		m.backoffUntil = time.Now().Add(1 * time.Hour)
		m.logger.Warn("github auth failed, backing off 1h", "error", err)
		if m.issues != nil {
			m.issues.SetIssue(DaemonIssue{
				Type:     IssueTypeSyncBackoff,
				Severity: SeverityWarning,
				Summary:  "GitHub authentication failed — check GITHUB_TOKEN or gh auth login",
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

// pushLedger pushes the ledger with retry and conflict resolution.
// This is the daemon's version — uses shared ledgerMu (already held by caller).
func (m *GitHubSyncManager) pushLedger(ctx context.Context, ledgerPath string) error {
	return gitutil.PushWithRetry(ctx, ledgerPath, gitutil.PushOpts{
		AutoResolvePrefixes: ledger.AutoResolvePrefixes,
		RepairLFS:           true,
		Logger:              m.logger,
		PrePush: func(repoPath string) error {
			ep := endpoint.GetForProject(m.projectRoot)
			if ep != "" {
				if err := gitserver.RefreshRemoteCredentials(repoPath, ep); err != nil {
					return fmt.Errorf("credential refresh: %w", err)
				}
			}
			return nil
		},
	})
}
