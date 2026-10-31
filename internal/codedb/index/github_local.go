package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/ledger"
)

// IndexLocalGitHubItems indexes explicitly fetched GitHub data without creating a Ledger snapshot.
// A scanned board post keeps its content; local commit metadata can supplement it.
func IndexLocalGitHubItems(ctx context.Context, s *store.Store, items []githubmirror.Item) (int, error) {
	return indexGitHubItems(ctx, s, items, "github-local")
}

// IndexRepoGitHubItems indexes the server's repository-scoped, access-checked snapshots.
func IndexRepoGitHubItems(ctx context.Context, s *store.Store, repoID string, items []githubmirror.Item) (int, error) {
	return indexGitHubItems(ctx, s, items, "github-repo:"+repoID)
}

func indexGitHubItems(ctx context.Context, s *store.Store, items []githubmirror.Item, source string) (int, error) {
	count := 0
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if it.Kind == githubmirror.KindPullRequest {
			existing, err := s.Queries().GetPRSourceByNumber(ctx, int64(it.Number))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return count, err
			}
			if err == nil && existing.SourcePath.Valid && source == "github-local" && (strings.Contains(existing.SourcePath.String, "/bulletin/github/posts/") || strings.HasPrefix(existing.SourcePath.String, "github-repo:")) {
				samePR, matchErr := sameIndexedPR(s, existing.ID, it.URL)
				if matchErr != nil {
					return count, matchErr
				}
				if samePR {
					if err := addLocalCommitMetadata(ctx, s, existing.ID, it); err != nil {
						return count, err
					}
				}
				count++
				continue
			}
			pr := ledger.PRFile{Number: it.Number, Title: it.Title, Body: it.Body, Author: it.Author.Login, State: it.State, Draft: it.Draft, Labels: it.Labels, CreatedAt: it.CreatedAt, MergedAt: it.MergedAt, ClosedAt: it.ClosedAt, UpdatedAt: it.UpdatedAt, MergeCommit: it.MergeCommit, URL: it.URL}
			if err == nil {
				samePR, matchErr := sameIndexedPR(s, existing.ID, it.URL)
				if matchErr != nil {
					return count, matchErr
				}
				// Board state can lag locally fetched merge metadata, so it cannot identify the SHA's provenance.
				if samePR && pr.MergeCommit == "" && existing.MergeCommit.Valid {
					pr.MergeCommit = existing.MergeCommit.String
				}
				if samePR && len(it.Commits) == 0 {
					shas, listErr := s.Queries().ListPRCommitShas(ctx, existing.ID)
					if listErr != nil {
						return count, listErr
					}
					for _, sha := range shas {
						pr.Commits = append(pr.Commits, ledger.PRCommit{SHA: sha})
					}
				}
			}
			for _, c := range it.Comments {
				pr.Comments = append(pr.Comments, ledger.PRComment{Author: c.Author.Login, Body: c.Body, Path: c.Path, Line: c.Line, CreatedAt: c.CreatedAt})
			}
			for _, c := range it.Commits {
				pr.Commits = append(pr.Commits, ledger.PRCommit{SHA: c.SHA, Author: c.Author, Date: c.Date, Msg: c.Message})
			}
			if _, err := indexPRValue(ctx, s, pr, source, 0); err != nil {
				return count, err
			}
		} else if it.Kind == githubmirror.KindIssue {
			existing, err := s.Queries().GetIssueSourceByNumber(ctx, int64(it.Number))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return count, err
			}
			if err == nil && existing.SourcePath.Valid && source == "github-local" && (strings.Contains(existing.SourcePath.String, "/bulletin/github/posts/") || strings.HasPrefix(existing.SourcePath.String, "github-repo:")) {
				count++
				continue
			}
			issue := ledger.IssueFile{Number: it.Number, Title: it.Title, Body: it.Body, Author: it.Author.Login, State: it.State, Labels: it.Labels, CreatedAt: it.CreatedAt, ClosedAt: it.ClosedAt, UpdatedAt: it.UpdatedAt, URL: it.URL}
			for _, c := range it.Comments {
				issue.Comments = append(issue.Comments, ledger.IssueComment{Author: c.Author.Login, Body: c.Body, CreatedAt: c.CreatedAt})
			}
			if _, err := indexIssueValue(ctx, s, issue, source, 0); err != nil {
				return count, err
			}
		} else {
			return count, fmt.Errorf("unknown GitHub item kind %q", it.Kind)
		}
		count++
	}
	return count, nil
}

// Missing legacy URLs cannot prove a change of PR identity.
func sameIndexedPR(s *store.Store, id int64, url string) (bool, error) {
	var oldURL sql.NullString
	if err := s.QueryRow("SELECT url FROM pull_requests WHERE id = ?", id).Scan(&oldURL); err != nil {
		return false, err
	}
	return !oldURL.Valid || oldURL.String == "" || url == "" || strings.EqualFold(oldURL.String, url), nil
}

func addLocalCommitMetadata(ctx context.Context, s *store.Store, prID int64, it githubmirror.Item) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if it.MergeCommit != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE pull_requests SET merge_commit = ? WHERE id = ?", it.MergeCommit, prID); err != nil {
			return err
		}
	}
	if len(it.Commits) > 0 {
		if _, err = tx.ExecContext(ctx, "DELETE FROM pr_commits WHERE pr_id = ?", prID); err != nil {
			return err
		}
		for _, c := range it.Commits {
			if _, err = tx.ExecContext(ctx, "INSERT INTO pr_commits(pr_id,sha) VALUES(?,?)", prID, c.SHA); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// KnownLocalGitHubItems reuses already indexed snapshots for an explicit local-only
// refresh. It never changes the daemon's publication cursor.
func KnownLocalGitHubItems(ctx context.Context, s *store.Store, owner, repo string) (map[string]githubmirror.ItemState, error) {
	out := make(map[string]githubmirror.ItemState)
	for _, spec := range []struct{ table, kind, segment string }{{"pull_requests", githubmirror.KindPullRequest, "pull"}, {"issues", githubmirror.KindIssue, "issues"}} {
		query := "SELECT number,updated_at,COALESCE(url,''),state"
		if spec.table == "pull_requests" {
			query += ",COALESCE(merge_commit,''),EXISTS(SELECT 1 FROM pr_commits c WHERE c.pr_id=p.id)"
		}
		query += " FROM " + spec.table + " p WHERE updated_at IS NOT NULL"
		rows, err := s.Query(query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			var number int
			var updated int64
			var link, state, merge string
			var hasCommits bool
			if spec.table == "pull_requests" {
				err = rows.Scan(&number, &updated, &link, &state, &merge, &hasCommits)
			} else {
				err = rows.Scan(&number, &updated, &link, &state)
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
			want := fmt.Sprintf("https://github.com/%s/%s/%s/%d", owner, repo, spec.segment, number)
			if !strings.EqualFold(strings.TrimSuffix(link, "/"), want) {
				continue
			}
			if spec.table == "pull_requests" && state == githubmirror.StateMerged && (merge == "" || !hasCommits) {
				continue
			}
			key := githubmirror.SourceKey(owner, repo, spec.kind, number)
			out[key] = githubmirror.ItemState{UpdatedAt: time.Unix(updated, 0).UTC(), CommitMetadata: spec.table == "pull_requests"}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
