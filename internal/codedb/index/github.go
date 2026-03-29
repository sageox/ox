package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/ledger"
)

// GitHubIndexStats tracks how many PRs and issues were indexed.
type GitHubIndexStats struct {
	PRsIndexed    int
	IssuesIndexed int
}

// IndexGitHubData reads PR and issue JSON files from the ledger and upserts
// them into CodeDB's pull_requests/issues tables with their comments.
//
// The ledger stores data in time-partitioned directories:
//
//	data/github/YYYY/MM/DD/pr/NNN.json
//	data/github/YYYY/MM/DD/issue/NNN.json
//
// Each file is a self-contained JSON blob that we upsert by number.
// Existing records are replaced (delete + insert) to pick up state changes,
// new comments, etc.
//
// Incremental: files are skipped if their mtime hasn't changed since the
// last successful index. This reduces steady-state cost from O(all files)
// to O(changed files) per run.
func IndexGitHubData(ctx context.Context, s *store.Store, ledgerPath string, progress ProgressFunc) (*GitHubIndexStats, error) {
	if ledgerPath == "" {
		return &GitHubIndexStats{}, nil
	}

	stats := &GitHubIndexStats{}

	// load known mtimes for skip check
	knownMtimes, err := loadFileMtimes(ctx, s)
	if err != nil {
		slog.Warn("failed to load github file mtimes, will reindex all", "error", err)
		knownMtimes = make(map[string]int64)
	}

	// index PRs
	prFiles, err := ledger.ListGitHubDataFiles(ledgerPath, "pr")
	if err != nil {
		return stats, fmt.Errorf("list PR files: %w", err)
	}

	var changedPR int
	for _, path := range prFiles {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if fileUnchanged(path, knownMtimes) {
			continue
		}
		changedPR++
		if changedPR == 1 && progress != nil {
			progress("Indexing changed PR files from ledger...")
		}
		mtime, indexErr := indexPRFile(ctx, s, path)
		if indexErr != nil {
			slog.Warn("index PR file failed, skipping", "path", path, "error", indexErr)
			continue
		}
		if err := saveFileMtime(ctx, s, path, mtime); err != nil {
			slog.Warn("save PR file mtime failed", "path", path, "error", err)
		}
		stats.PRsIndexed++
	}

	// index issues
	issueFiles, err := ledger.ListGitHubDataFiles(ledgerPath, "issue")
	if err != nil {
		return stats, fmt.Errorf("list issue files: %w", err)
	}

	var changedIssue int
	for _, path := range issueFiles {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if fileUnchanged(path, knownMtimes) {
			continue
		}
		changedIssue++
		if changedIssue == 1 && progress != nil {
			progress("Indexing changed issue files from ledger...")
		}
		mtime, indexErr := indexIssueFile(ctx, s, path)
		if indexErr != nil {
			slog.Warn("index issue file failed, skipping", "path", path, "error", indexErr)
			continue
		}
		if err := saveFileMtime(ctx, s, path, mtime); err != nil {
			slog.Warn("save issue file mtime failed", "path", path, "error", err)
		}
		stats.IssuesIndexed++
	}

	if stats.PRsIndexed > 0 || stats.IssuesIndexed > 0 {
		slog.Info("github data indexed", "prs", stats.PRsIndexed, "issues", stats.IssuesIndexed)
	}

	return stats, nil
}

// fileUnchanged returns true if the file's current mtime matches the stored mtime.
func fileUnchanged(path string, knownMtimes map[string]int64) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false // can't stat -> treat as changed
	}
	stored, ok := knownMtimes[path]
	if !ok {
		return false // never indexed
	}
	return info.ModTime().UTC().UnixNano() == stored
}

// loadFileMtimes reads all stored mtimes into a map for O(1) lookup.
func loadFileMtimes(ctx context.Context, s *store.Store) (map[string]int64, error) {
	items, err := s.Queries().ListFileMtimes(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(items))
	for _, item := range items {
		m[item.SourcePath] = item.MtimeUnix
	}
	return m, nil
}

// saveFileMtime records a file's mtime after successful indexing.
func saveFileMtime(ctx context.Context, s *store.Store, path string, mtimeNano int64) error {
	return s.Queries().UpsertFileMtime(ctx, codedbsqlc.UpsertFileMtimeParams{
		SourcePath: path,
		MtimeUnix:  mtimeNano,
	})
}

// indexPRFile indexes a single PR JSON file. Returns the file's mtime (UnixNano) on success.
func indexPRFile(ctx context.Context, s *store.Store, path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	mtimeNano := info.ModTime().UTC().UnixNano()

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	var pr ledger.PRFile
	if err := json.Unmarshal(data, &pr); err != nil {
		return 0, fmt.Errorf("unmarshal %s: %w", path, err)
	}

	tx, err := s.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := codedbsqlc.New(tx)

	// delete existing record + comments + commits (upsert via delete-insert)
	existingID, err := q.GetPRIDByNumber(ctx, int64(pr.Number))
	if err == nil {
		if err := q.DeletePRCommentsByPR(ctx, existingID); err != nil {
			return 0, fmt.Errorf("delete pr comments: %w", err)
		}
		if err := q.DeletePRCommitsByPR(ctx, existingID); err != nil {
			return 0, fmt.Errorf("delete pr commits: %w", err)
		}
		if err := q.DeletePullRequest(ctx, existingID); err != nil {
			return 0, fmt.Errorf("delete pr: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check existing PR %d: %w", pr.Number, err)
	}

	// JSON-encode labels to handle commas in label names
	labelsJSON, _ := json.Marshal(pr.Labels)
	res, err := q.InsertPullRequest(ctx, codedbsqlc.InsertPullRequestParams{
		Number:      int64(pr.Number),
		Title:       pr.Title,
		Body:        toNullString(pr.Body),
		Author:      toNullString(pr.Author),
		State:       pr.State,
		Labels:      toNullString(string(labelsJSON)),
		CreatedAt:   timeToNullInt64(pr.CreatedAt),
		MergedAt:    timePtrToNullInt64(pr.MergedAt),
		ClosedAt:    timePtrToNullInt64(pr.ClosedAt),
		UpdatedAt:   timeToNullInt64(pr.UpdatedAt),
		MergeCommit: toNullString(pr.MergeCommit),
		Url:         toNullString(pr.URL),
		SourcePath:  toNullString(path),
	})
	if err != nil {
		return 0, fmt.Errorf("insert PR %d: %w", pr.Number, err)
	}

	prID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get PR id: %w", err)
	}

	// insert comments
	for _, c := range pr.Comments {
		if err := q.InsertPRComment(ctx, codedbsqlc.InsertPRCommentParams{
			PrID:      prID,
			Author:    toNullString(c.Author),
			Body:      toNullString(c.Body),
			Path:      toNullString(c.Path),
			Line:      ptrIntToNullInt64(c.Line),
			CreatedAt: timeToNullInt64(c.CreatedAt),
		}); err != nil {
			return 0, fmt.Errorf("insert PR %d comment: %w", pr.Number, err)
		}
	}

	// insert commits
	for _, c := range pr.Commits {
		if err := q.InsertPRCommit(ctx, codedbsqlc.InsertPRCommitParams{
			PrID: prID,
			Sha:  c.SHA,
		}); err != nil {
			return 0, fmt.Errorf("insert PR %d commit: %w", pr.Number, err)
		}
	}

	return mtimeNano, tx.Commit()
}

// indexIssueFile indexes a single issue JSON file. Returns the file's mtime (UnixNano) on success.
func indexIssueFile(ctx context.Context, s *store.Store, path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	mtimeNano := info.ModTime().UTC().UnixNano()

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	var issue ledger.IssueFile
	if err := json.Unmarshal(data, &issue); err != nil {
		return 0, fmt.Errorf("unmarshal %s: %w", path, err)
	}

	tx, err := s.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := codedbsqlc.New(tx)

	// delete existing record + comments (upsert via delete-insert)
	existingID, err := q.GetIssueIDByNumber(ctx, int64(issue.Number))
	if err == nil {
		if err := q.DeleteIssueCommentsByIssue(ctx, existingID); err != nil {
			return 0, fmt.Errorf("delete issue comments: %w", err)
		}
		if err := q.DeleteIssue(ctx, existingID); err != nil {
			return 0, fmt.Errorf("delete issue: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check existing issue %d: %w", issue.Number, err)
	}

	// JSON-encode labels to handle commas in label names
	labelsJSON, _ := json.Marshal(issue.Labels)
	res, err := q.InsertIssue(ctx, codedbsqlc.InsertIssueParams{
		Number:     int64(issue.Number),
		Title:      issue.Title,
		Body:       toNullString(issue.Body),
		Author:     toNullString(issue.Author),
		State:      issue.State,
		Labels:     toNullString(string(labelsJSON)),
		CreatedAt:  timeToNullInt64(issue.CreatedAt),
		ClosedAt:   timePtrToNullInt64(issue.ClosedAt),
		UpdatedAt:  timeToNullInt64(issue.UpdatedAt),
		Url:        toNullString(issue.URL),
		SourcePath: toNullString(path),
	})
	if err != nil {
		return 0, fmt.Errorf("insert issue %d: %w", issue.Number, err)
	}

	issueID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get issue id: %w", err)
	}

	// insert comments
	for _, c := range issue.Comments {
		if err := q.InsertIssueComment(ctx, codedbsqlc.InsertIssueCommentParams{
			IssueID:   issueID,
			Author:    toNullString(c.Author),
			Body:      toNullString(c.Body),
			CreatedAt: timeToNullInt64(c.CreatedAt),
		}); err != nil {
			return 0, fmt.Errorf("insert issue %d comment: %w", issue.Number, err)
		}
	}

	return mtimeNano, tx.Commit()
}

func toNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func ptrIntToNullInt64(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func timeToNullInt64(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func timePtrToNullInt64(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return timeToNullInt64(*t)
}
