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
	knownMtimes, err := loadFileMtimes(s)
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
		mtime, indexErr := indexPRFile(s, path)
		if indexErr != nil {
			slog.Warn("index PR file failed, skipping", "path", path, "error", indexErr)
			continue
		}
		if err := saveFileMtime(s, path, mtime); err != nil {
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
		mtime, indexErr := indexIssueFile(s, path)
		if indexErr != nil {
			slog.Warn("index issue file failed, skipping", "path", path, "error", indexErr)
			continue
		}
		if err := saveFileMtime(s, path, mtime); err != nil {
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
		return false // can't stat → treat as changed
	}
	stored, ok := knownMtimes[path]
	if !ok {
		return false // never indexed
	}
	return info.ModTime().UnixNano() == stored
}

// loadFileMtimes reads all stored mtimes into a map for O(1) lookup.
func loadFileMtimes(s *store.Store) (map[string]int64, error) {
	rows, err := s.Query("SELECT source_path, mtime_unix FROM github_file_mtimes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]int64)
	for rows.Next() {
		var path string
		var mtime int64
		if err := rows.Scan(&path, &mtime); err != nil {
			continue
		}
		m[path] = mtime
	}
	return m, rows.Err()
}

// saveFileMtime records a file's mtime after successful indexing.
func saveFileMtime(s *store.Store, path string, mtimeNano int64) error {
	_, err := s.Exec(
		"INSERT OR REPLACE INTO github_file_mtimes (source_path, mtime_unix) VALUES (?, ?)",
		path, mtimeNano,
	)
	return err
}

// indexPRFile indexes a single PR JSON file. Returns the file's mtime (UnixNano) on success.
func indexPRFile(s *store.Store, path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	mtimeNano := info.ModTime().UnixNano()

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

	// delete existing record + comments (upsert via delete-insert)
	var existingID int64
	err = tx.QueryRow("SELECT id FROM pull_requests WHERE number = ?", pr.Number).Scan(&existingID)
	if err == nil {
		// record exists — delete comments first, then the PR
		if _, err := tx.Exec("DELETE FROM pr_comments WHERE pr_id = ?", existingID); err != nil {
			return 0, fmt.Errorf("delete pr comments: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM pull_requests WHERE id = ?", existingID); err != nil {
			return 0, fmt.Errorf("delete pr: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check existing PR %d: %w", pr.Number, err)
	}

	// insert PR — JSON-encode labels to handle commas in label names
	labelsJSON, _ := json.Marshal(pr.Labels)
	labels := string(labelsJSON)
	res, err := tx.Exec(`INSERT INTO pull_requests
		(number, title, body, author, state, labels, created_at, merged_at, closed_at, updated_at, merge_commit, url, source_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pr.Number, pr.Title, pr.Body, pr.Author, pr.State, labels,
		timeToUnix(pr.CreatedAt), timeToUnixPtr(pr.MergedAt), timeToUnixPtr(pr.ClosedAt),
		timeToUnix(pr.UpdatedAt), pr.MergeCommit, pr.URL, path,
	)
	if err != nil {
		return 0, fmt.Errorf("insert PR %d: %w", pr.Number, err)
	}

	prID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get PR id: %w", err)
	}

	// insert comments
	for _, c := range pr.Comments {
		_, err := tx.Exec(`INSERT INTO pr_comments (pr_id, author, body, path, line, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			prID, c.Author, c.Body, c.Path, c.Line, timeToUnix(c.CreatedAt),
		)
		if err != nil {
			return 0, fmt.Errorf("insert PR %d comment: %w", pr.Number, err)
		}
	}

	return mtimeNano, tx.Commit()
}

// indexIssueFile indexes a single issue JSON file. Returns the file's mtime (UnixNano) on success.
func indexIssueFile(s *store.Store, path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	mtimeNano := info.ModTime().UnixNano()

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

	// delete existing record + comments (upsert via delete-insert)
	var existingID int64
	err = tx.QueryRow("SELECT id FROM issues WHERE number = ?", issue.Number).Scan(&existingID)
	if err == nil {
		if _, err := tx.Exec("DELETE FROM issue_comments WHERE issue_id = ?", existingID); err != nil {
			return 0, fmt.Errorf("delete issue comments: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM issues WHERE id = ?", existingID); err != nil {
			return 0, fmt.Errorf("delete issue: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check existing issue %d: %w", issue.Number, err)
	}

	// insert issue — JSON-encode labels to handle commas in label names
	labelsJSON, _ := json.Marshal(issue.Labels)
	labels := string(labelsJSON)
	res, err := tx.Exec(`INSERT INTO issues
		(number, title, body, author, state, labels, created_at, closed_at, updated_at, url, source_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issue.Number, issue.Title, issue.Body, issue.Author, issue.State, labels,
		timeToUnix(issue.CreatedAt), timeToUnixPtr(issue.ClosedAt),
		timeToUnix(issue.UpdatedAt), issue.URL, path,
	)
	if err != nil {
		return 0, fmt.Errorf("insert issue %d: %w", issue.Number, err)
	}

	issueID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get issue id: %w", err)
	}

	// insert comments
	for _, c := range issue.Comments {
		_, err := tx.Exec(`INSERT INTO issue_comments (issue_id, author, body, created_at)
			VALUES (?, ?, ?, ?)`,
			issueID, c.Author, c.Body, timeToUnix(c.CreatedAt),
		)
		if err != nil {
			return 0, fmt.Errorf("insert issue %d comment: %w", issue.Number, err)
		}
	}

	return mtimeNano, tx.Commit()
}

func timeToUnix(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.Unix()
	return &v
}

func timeToUnixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	return timeToUnix(*t)
}
