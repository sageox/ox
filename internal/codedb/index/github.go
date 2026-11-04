package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
// The ledger stores append-only snapshots in time-partitioned directories:
//
//	data/github/YYYY/MM/DD/pr/{number}-{hash}.json
//	data/github/YYYY/MM/DD/issue/{number}-{hash}.json
//
// A single PR may have many snapshot files representing different points in
// time as its state evolves (open → reviewed → merged). Lex order of the
// content-hash filenames has no relationship to chronological order, so we
// group snapshots by object number and explicitly pick the one with the
// latest updated_at as the winner — only the winner is written to the DB.
//
// Incremental: a group is skipped if every file in it has its current mtime
// already recorded. Adding a new snapshot to a group invalidates the group
// and triggers a re-pick.
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

	prGroups := groupSnapshotsByNumber(prFiles)

	var changedPR int
	for _, group := range prGroups {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if groupUnchanged(group, knownMtimes) || newerLocalGitHubRow(ctx, s, group.winner, true) {
			continue
		}
		changedPR++
		if changedPR == 1 && progress != nil {
			progress("Indexing changed PR files from ledger...")
		}
		if _, indexErr := indexPRFile(ctx, s, group.winner); indexErr != nil {
			slog.Warn("index PR file failed, skipping", "path", group.winner, "error", indexErr)
			continue
		}
		recordGroupMtimes(ctx, s, group)
		stats.PRsIndexed++
	}

	// index issues
	issueFiles, err := ledger.ListGitHubDataFiles(ledgerPath, "issue")
	if err != nil {
		return stats, fmt.Errorf("list issue files: %w", err)
	}

	issueGroups := groupSnapshotsByNumber(issueFiles)

	var changedIssue int
	for _, group := range issueGroups {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if groupUnchanged(group, knownMtimes) || newerLocalGitHubRow(ctx, s, group.winner, false) {
			continue
		}
		changedIssue++
		if changedIssue == 1 && progress != nil {
			progress("Indexing changed issue files from ledger...")
		}
		if _, indexErr := indexIssueFile(ctx, s, group.winner); indexErr != nil {
			slog.Warn("index issue file failed, skipping", "path", group.winner, "error", indexErr)
			continue
		}
		recordGroupMtimes(ctx, s, group)
		stats.IssuesIndexed++
	}

	if stats.PRsIndexed > 0 || stats.IssuesIndexed > 0 {
		slog.Info("github data indexed", "prs", stats.PRsIndexed, "issues", stats.IssuesIndexed)
	}

	return stats, nil
}

// snapshotGroup represents N append-only snapshots of the same PR/issue.
// One file is chosen as the winner (latest updated_at); the rest are tracked
// so the indexer can record mtimes for siblings — preventing the unchanged
// skip from being defeated when only one snapshot in a group has been touched.
type snapshotGroup struct {
	winner   string   // path to the snapshot with latest updated_at
	allPaths []string // every snapshot path sharing the same PR/issue number
}

// snapshotCandidate is one parsed snapshot file under consideration as the
// winner of its group. parsed=false means JSON unmarshal failed (or the file
// couldn't be read), in which case updatedAt is the zero value.
type snapshotCandidate struct {
	path      string
	updatedAt time.Time
	mtime     time.Time
	parsed    bool
}

// candidateBeats reports whether `cur` should replace `best` as the winner of
// its group. The comparison is total and deterministic: every pair of distinct
// candidates produces a definite winner, regardless of map iteration order or
// filesystem walk order.
//
// Order of precedence:
//  1. parsed JSON beats unparsed
//  2. later updated_at wins
//  3. later filesystem mtime breaks ties
//  4. lex-greater path is the final fallback
//
// Step 4 is reached only when every other field is identical and ensures the
// outcome doesn't depend on goroutine scheduling, walker implementation, or
// map ordering — properties that are easy to silently break in a refactor.
//
// Identical inputs (same struct value, including same path) return false in
// both directions — neither beats the other. This is unreachable from the
// selection loop in groupSnapshotsByNumber (which never compares an element
// against itself) but matters for direct callers.
func candidateBeats(cur, best snapshotCandidate) bool {
	if cur.parsed != best.parsed {
		return cur.parsed
	}
	if !cur.updatedAt.Equal(best.updatedAt) {
		return cur.updatedAt.After(best.updatedAt)
	}
	if !cur.mtime.Equal(best.mtime) {
		return cur.mtime.After(best.mtime)
	}
	return cur.path > best.path
}

// groupSnapshotsByNumber groups ledger PR/issue files by parsed object number,
// picking the file with the latest updated_at as the winner per group.
//
// Why this exists: the ledger stores append-only snapshots ({number}-{hash}.json),
// so a single PR may have many files representing different points in time.
// Lex order of content-hash filenames has no relationship to chronological order,
// so the indexer must explicitly choose the latest by reading each file's
// updated_at — otherwise whichever file happens to lex-sort last wins, which
// can be a chronologically-earlier (and stale) snapshot. See issue #474.
//
// Grouping key is (parent directory, parsed number). Files for the same PR
// always live in the same created_at date directory (see WriteGitHubPR in
// internal/ledger/github_data.go), so dir+number is a stable identity. If the
// ledger ever starts writing the same PR number across multiple date dirs,
// each dir will become its own group and the later dir's index pass will
// upsert second — correct by accident, but a comment in WriteGitHubPR would
// be safer than relying on this.
//
// Files with unparseable filenames or unparseable JSON each form their own
// single-file group so they're still indexed (defensive — preserves prior
// behavior for malformed inputs).
//
// Winner selection within a group:
//  1. parsed JSON beats unparsed
//  2. later updated_at wins
//  3. later filesystem mtime breaks ties
//  4. lex-greater path is the final deterministic fallback
//
// Steps 1–3 are the substantive selection rule. Step 4 only matters when
// every other signal is identical (e.g., all candidates failed to parse JSON
// and were created with the same mtime), but it removes any dependence on
// map iteration order or filesystem walk order — guaranteeing the chosen
// winner is reproducible across runs.
func groupSnapshotsByNumber(paths []string) []snapshotGroup {
	// group by (parent_dir, number) — files for the same PR live in the
	// same created_at date directory, so dir+number is a stable identifier
	byKey := make(map[string][]snapshotCandidate)
	var unparseable []string

	for _, path := range paths {
		num := parseSnapshotNumber(filepath.Base(path))
		if num < 0 {
			unparseable = append(unparseable, path)
			continue
		}
		key := filepath.Dir(path) + "\x00" + strconv.Itoa(num)

		c := snapshotCandidate{path: path}
		if info, err := os.Stat(path); err == nil {
			c.mtime = info.ModTime()
		}
		if data, err := os.ReadFile(path); err == nil {
			var stub struct {
				UpdatedAt time.Time `json:"updated_at"`
			}
			if json.Unmarshal(data, &stub) == nil {
				c.updatedAt = stub.UpdatedAt
				c.parsed = true
			}
		}
		byKey[key] = append(byKey[key], c)
	}

	groups := make([]snapshotGroup, 0, len(byKey)+len(unparseable))
	for _, candidates := range byKey {
		winnerIdx := 0
		for i := 1; i < len(candidates); i++ {
			if candidateBeats(candidates[i], candidates[winnerIdx]) {
				winnerIdx = i
			}
		}
		all := make([]string, len(candidates))
		for i, c := range candidates {
			all[i] = c.path
		}
		groups = append(groups, snapshotGroup{
			winner:   candidates[winnerIdx].path,
			allPaths: all,
		})
	}
	for _, p := range unparseable {
		groups = append(groups, snapshotGroup{winner: p, allPaths: []string{p}})
	}

	// deterministic ordering simplifies test assertions and debugging
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].winner < groups[j].winner
	})

	return groups
}

// parseSnapshotNumber extracts the PR/issue number from a snapshot filename.
// Accepts both content-hashed ({number}-{hash}.json) and legacy ({number}.json)
// formats. Returns -1 if the filename doesn't match either, or if the parsed
// number is non-positive (PR/issue numbers are always >= 1).
func parseSnapshotNumber(name string) int {
	if !strings.HasSuffix(name, ".json") {
		return -1
	}
	base := strings.TrimSuffix(name, ".json")
	candidate := base
	if i := strings.IndexByte(base, '-'); i > 0 {
		candidate = base[:i]
	}
	// strconv.Atoi accepts a leading "-"; reject anything that isn't a pure
	// positive integer literal.
	for _, r := range candidate {
		if r < '0' || r > '9' {
			return -1
		}
	}
	n, err := strconv.Atoi(candidate)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// groupUnchanged returns true if every file in the group has its current
// mtime recorded — meaning nothing in the group has been added or modified
// since the last successful index.
func groupUnchanged(g snapshotGroup, knownMtimes map[string]int64) bool {
	for _, p := range g.allPaths {
		if !fileUnchanged(p, knownMtimes) {
			return false
		}
	}
	return true
}

// recordGroupMtimes records the current mtime for every file in the group so
// future runs can skip the group when nothing has changed.
func recordGroupMtimes(ctx context.Context, s *store.Store, g snapshotGroup) {
	for _, p := range g.allPaths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if err := saveFileMtime(ctx, s, p, info.ModTime().UTC().UnixNano()); err != nil {
			slog.Warn("save github file mtime failed", "path", p, "error", err)
		}
	}
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

	return indexPRValue(ctx, s, pr, path, mtimeNano)
}

func indexPRValue(ctx context.Context, s *store.Store, pr ledger.PRFile, path string, mtimeNano int64) (int64, error) {
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

	return indexIssueValue(ctx, s, issue, path, mtimeNano)
}

func indexIssueValue(ctx context.Context, s *store.Store, issue ledger.IssueFile, path string, mtimeNano int64) (int64, error) {
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

// A rebuildable local row may be newer than a historical Ledger snapshot.
// Repo-scoped rows are already selected by the server's board-first reader.
func newerLocalGitHubRow(ctx context.Context, s *store.Store, path string, pr bool) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var item struct {
		Number    int       `json:"number"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	table := "issues"
	if pr {
		table = "pull_requests"
	}
	var source sql.NullString
	var updated sql.NullInt64
	err = s.QueryRow("SELECT source_path, updated_at FROM "+table+" WHERE number = ?", item.Number).Scan(&source, &updated)
	if err != nil || !source.Valid {
		return false
	}
	if strings.HasPrefix(source.String, "github-repo:") {
		return true
	}
	return source.String == "github-local" && updated.Valid && updated.Int64 >= item.UpdatedAt.Unix()
}
