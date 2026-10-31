package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/ledger"
)

// Existing sync checks run at five-minute intervals. Fifteen minutes matches
// GitHub sync's normal cadence without a new background job.
const githubRepoReadInterval = 15 * time.Minute

// indexGitHubRepo reads selected snapshots through the existing repo access check
// for members who cannot clone the owner team's Team Context.
func (m *CodeDBManager) indexGitHubRepo(ctx context.Context, db *codedb.DB) (resultErr error) {
	m.mu.Lock()
	root := m.projectRoot
	retryAfter := m.githubRepoRetryAfter
	m.mu.Unlock()
	if time.Now().Before(retryAfter) {
		return fmt.Errorf("GitHub repository read is in retry backoff")
	}
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if resultErr != nil {
			m.githubRepoRetryAfter = time.Now().Add(githubRepoReadInterval)
		} else {
			m.githubRepoRetryAfter = time.Time{}
		}
	}()
	repoID := config.GetRepoID(root)
	if repoID == "" {
		return fmt.Errorf("repository ID unavailable")
	}
	ep := endpoint.GetForProject(root)
	token, err := auth.EnsureValidTokenForEndpointContext(ctx, ep, 300)
	if err != nil {
		return err
	}
	if token == nil || token.AccessToken == "" {
		return fmt.Errorf("SageOx authentication unavailable")
	}
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)
	known := make(map[string]bool)
	for kind, table := range map[string]string{"pr": "pull_requests", "issue": "issues"} {
		rows, err := db.Store().Query("SELECT source_path FROM "+table+" WHERE source_path LIKE ? OR source_path LIKE ?", "github-repo:"+repoID+":%", "%/bulletin/github/posts/%")
		if err != nil {
			return err
		}
		for rows.Next() {
			var source string
			if err := rows.Scan(&source); err != nil {
				rows.Close()
				return err
			}
			if strings.HasPrefix(source, "github-repo:"+repoID+":") {
				known[kind+":"+strings.TrimPrefix(source, "github-repo:"+repoID+":")] = true
			} else {
				name := strings.TrimSuffix(filepath.Base(source), ".md")
				if at := strings.LastIndexByte(name, '-'); at >= 0 && len(name[at+1:]) == 64 {
					known[kind+":"+name[at+1:]] = true
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	bodies, entries, err := client.GitHubRepoSnapshots(ctx, repoID, known)
	if err != nil {
		return err
	}
	// Keep healthy snapshots searchable while leaving a partial pass eligible for repair.
	var failures []error
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		itemErr := func() error {
			var item githubmirror.Item
			if entry.Kind == "pr" {
				var pr ledger.PRFile
				if err := json.Unmarshal(bodies[i], &pr); err != nil {
					return err
				}
				if pr.Number != entry.Number {
					return fmt.Errorf("repository PR number mismatch")
				}
				item = githubmirror.Item{Kind: githubmirror.KindPullRequest, Number: pr.Number, Title: pr.Title, Body: pr.Body, Author: githubmirror.Author{Login: pr.Author}, State: pr.State, Draft: pr.Draft, Labels: pr.Labels, CreatedAt: pr.CreatedAt, MergedAt: pr.MergedAt, ClosedAt: pr.ClosedAt, UpdatedAt: pr.UpdatedAt, MergeCommit: pr.MergeCommit, URL: pr.URL}
				for _, c := range pr.Comments {
					item.Comments = append(item.Comments, githubmirror.Comment{Author: githubmirror.Author{Login: c.Author}, Body: c.Body, Path: c.Path, Line: c.Line, CreatedAt: c.CreatedAt})
				}
				for _, c := range pr.Commits {
					item.Commits = append(item.Commits, githubmirror.Commit{SHA: c.SHA, Author: c.Author, Date: c.Date, Message: c.Msg})
				}
			} else {
				var issue ledger.IssueFile
				if err := json.Unmarshal(bodies[i], &issue); err != nil {
					return err
				}
				if issue.Number != entry.Number {
					return fmt.Errorf("repository issue number mismatch")
				}
				item = githubmirror.Item{Kind: githubmirror.KindIssue, Number: issue.Number, Title: issue.Title, Body: issue.Body, Author: githubmirror.Author{Login: issue.Author}, State: issue.State, Labels: issue.Labels, CreatedAt: issue.CreatedAt, ClosedAt: issue.ClosedAt, UpdatedAt: issue.UpdatedAt, URL: issue.URL}
				for _, c := range issue.Comments {
					item.Comments = append(item.Comments, githubmirror.Comment{Author: githubmirror.Author{Login: c.Author}, Body: c.Body, CreatedAt: c.CreatedAt})
				}
			}
			_, err := index.IndexRepoGitHubItems(ctx, db.Store(), repoID+":"+entry.ID, []githubmirror.Item{item})
			return err
		}()
		if itemErr != nil {
			failures = append(failures, fmt.Errorf("repository %s %d: %w", entry.Kind, entry.Number, itemErr))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	m.mu.Lock()
	m.lastGitHubRepoRead = time.Now()
	m.mu.Unlock()
	return nil
}

// refreshGitHubRepoIfDue uses the existing freshness tick without rebuilding
// code and symbols just because a repo-scoped GitHub snapshot may have changed.
func (m *CodeDBManager) refreshGitHubRepoIfDue(ctx context.Context, dataDir string) {
	if !m.githubRepoReadEligible() {
		return
	}
	m.mu.Lock()
	if m.indexing || (!m.lastGitHubRepoRead.IsZero() && time.Since(m.lastGitHubRepoRead) < githubRepoReadInterval) {
		m.mu.Unlock()
		return
	}
	m.indexing = true
	m.lastGitHubRepoRead = time.Now()
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.indexing = false; m.mu.Unlock() }()
		db, err := codedb.OpenSQLOnly(dataDir)
		if err != nil {
			m.logger.Warn("github repo index open failed", "error", err)
			return
		}
		defer db.Close()
		if err = m.indexGitHubRepo(ctx, db); err != nil {
			m.logger.Warn("github repo indexing failed; will retry", "error", err)
		}
	}()
}

// Reading teammates' snapshots is independent of the local GitHub producer setting.
func (m *CodeDBManager) githubRepoReadEligible() bool {
	m.mu.Lock()
	root := m.projectRoot
	m.mu.Unlock()
	return config.GetRepoID(root) != "" && m.githubRepoFullName() != ""
}
