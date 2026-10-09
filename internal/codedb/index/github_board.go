package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/githubmirror"
)

// maxBoardPostBytes bounds what one post may cost the indexer to read. Rendered
// posts are a few KB, but the board lives in a team-writable git checkout, so a
// runaway file must not be able to exhaust the daemon's memory.
const maxBoardPostBytes = 4 << 20

// ErrBoardPostsFailed is wrapped by the error IndexGitHubBoard returns when
// posts of this repo could not be written to the index. The posts that did
// index are kept and counted in the returned stats; the failed ones are not
// recorded as indexed, so a later run tries them again.
var ErrBoardPostsFailed = errors.New("github board posts failed to index")

// boardItemKey identifies one PR or issue on the board. The board is shared by
// every repo on the team, so a number alone is only unique once the repo
// filter has run.
type boardItemKey struct {
	kind   string
	number int
}

// boardPost is one parsed post that is a candidate for its item's row.
type boardPost struct {
	path  string
	mtime time.Time
	post  *githubmirror.Post
}

// IndexGitHubBoard reads the team bulletin board's mirrored GitHub posts for
// repoFullName ("owner/name") and upserts them into CodeDB's pull_requests and
// issues tables with their comments.
//
// alsoRepos are further spellings of the same repo. The relay publishes under
// GitHub's current name, and after a rename or transfer the git remote can
// still carry the old one, so a caller passes both; posts written before the
// rename keep the old name until they expire. Names compare case-insensitively
// and blanks are ignored.
//
// Run it AFTER IndexGitHubData: a board post wins over a Ledger snapshot for
// the same number. The server scans every post for prompt injection before it
// publishes, so the board is the safer of the two sources, and the two writers
// share rows (number is UNIQUE) rather than keeping parallel copies.
//
// The board holds every team repo's posts, so posts for other repos are
// ignored. During a supersede the board can briefly hold two posts for one
// item; the newest last_material_change wins.
//
// Incremental: a post is skipped when its mtime is already recorded AND the
// row for its number still names this post as its source. The second clause
// matters because the Ledger indexer overwrites the same row whenever a new
// snapshot lands; an mtime-only skip would leave that Ledger row in place and
// silently demote the board.
//
// A post that fails to index does not stop the others. It is not recorded as
// indexed, and the call returns the stats so far with an error wrapping
// ErrBoardPostsFailed, so the caller can decide whether to try again. A post
// that cannot be parsed, or belongs to another repo, is not a failure: trying
// again changes nothing.
//
// Rows are never deleted: a post expires 90 days after the item's last change,
// but GitHub remains the source of truth for what the item was.
func IndexGitHubBoard(ctx context.Context, s *store.Store, postsDir, repoFullName string, progress ProgressFunc, alsoRepos ...string) (*GitHubIndexStats, error) {
	stats := &GitHubIndexStats{}
	repos := boardRepoSet(append([]string{repoFullName}, alsoRepos...))
	// without a repo name the repo filter below can never match; return before
	// reading and parsing the whole shared board for nothing
	if postsDir == "" || len(repos) == 0 {
		return stats, nil
	}

	posts, err := pickBoardPosts(ctx, postsDir, repos)
	if err != nil {
		return stats, err
	}
	if len(posts) == 0 {
		return stats, nil
	}

	knownMtimes, err := loadFileMtimes(ctx, s)
	if err != nil {
		slog.Warn("failed to load github file mtimes, will reindex all board posts", "error", err)
		knownMtimes = make(map[string]int64)
	}

	var changed, failed int
	var firstFailure error
	for _, bp := range posts {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if boardPostUnchanged(ctx, s, bp, knownMtimes) {
			continue
		}
		changed++
		if changed == 1 && progress != nil {
			progress("Indexing changed GitHub board posts...")
		}

		isPR := bp.post.Header.Kind == githubmirror.KindPullRequest
		var indexErr error
		if isPR {
			indexErr = indexBoardPR(ctx, s, bp)
		} else {
			indexErr = indexBoardIssue(ctx, s, bp)
		}
		if indexErr != nil {
			slog.Warn("index github board post failed, skipping", "path", bp.path, "error", indexErr)
			failed++
			if firstFailure == nil {
				firstFailure = indexErr
			}
			continue
		}
		// the mtime captured before the read, so an edit racing the read shows
		// up as changed on the next run instead of being recorded as indexed
		if err := saveFileMtime(ctx, s, bp.path, bp.mtime.UTC().UnixNano()); err != nil {
			slog.Warn("save github board post mtime failed", "path", bp.path, "error", err)
		}
		if isPR {
			stats.PRsIndexed++
		} else {
			stats.IssuesIndexed++
		}
	}

	if stats.PRsIndexed > 0 || stats.IssuesIndexed > 0 {
		slog.Info("github board indexed", "prs", stats.PRsIndexed, "issues", stats.IssuesIndexed)
	}
	if failed > 0 {
		return stats, fmt.Errorf("%w: %d of %d changed posts, first: %w", ErrBoardPostsFailed, failed, changed, firstFailure)
	}
	return stats, nil
}

// boardRepoSet lowercases repo names into a set, dropping blanks.
func boardRepoSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// BoardFingerprint is a cheap change marker for one repo's posts in a board
// posts directory: how many there are and the newest mtime. The daemon compares
// it between freshness checks so a board-only change (a teammate's PR landing
// in the Team Context pull) re-runs the GitHub stages even though git HEAD did
// not move.
//
// The board holds every team repo's posts, but only this repo's can change what
// the index holds, so only post files whose name starts with one of
// slugPrefixes (githubmirror.SlugPrefix of each spelling of the repo) are
// counted. Another repo's post must not send this project through the whole
// index pipeline. The file name is only a prefilter: a sibling repo whose name
// extends this one shares the prefix and is over-counted, which costs a
// harmless re-run, never a missed one.
//
// It returns "" when there is no prefix to watch (the project has no GitHub
// remote) or the directory is missing or unreadable.
func BoardFingerprint(postsDir string, slugPrefixes ...string) string {
	prefixes := make([]string, 0, len(slugPrefixes))
	for _, prefix := range slugPrefixes {
		if prefix != "" { // "" matches every file; it means "unknown repo", not "all repos"
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return ""
	}
	entries, err := os.ReadDir(postsDir)
	if err != nil {
		return ""
	}
	var count int
	var newest int64
	for _, entry := range entries {
		if !isBoardPostName(entry) || !hasAnyPrefix(entry.Name(), prefixes) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue // removed since the listing; the count change shows next time
		}
		count++
		if m := info.ModTime().UnixNano(); m > newest {
			newest = m
		}
	}
	return fmt.Sprintf("%d:%d", count, newest)
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isBoardPostName reports whether a directory entry is a post file. Posts sit
// beside their .meta.json, which this indexer does not read.
func isBoardPostName(entry fs.DirEntry) bool {
	return entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md")
}

// pickBoardPosts returns, for each PR/issue of this repo on the board, the
// one post that should back its row, ordered by kind then number. repos is the
// lowercased set of names that count as this repo.
func pickBoardPosts(ctx context.Context, postsDir string, repos map[string]struct{}) ([]boardPost, error) {
	entries, err := os.ReadDir(postsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no board yet: the mirror is off or has not synced
	}
	if err != nil {
		return nil, fmt.Errorf("list board posts: %w", err)
	}

	winners := make(map[boardItemKey]boardPost)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !isBoardPostName(entry) {
			continue
		}
		bp, ok := readBoardPost(filepath.Join(postsDir, entry.Name()), entry, repos)
		if !ok {
			continue
		}
		key := boardItemKey{kind: bp.post.Header.Kind, number: bp.post.Header.Number}
		if cur, exists := winners[key]; !exists || boardPostBeats(bp, cur) {
			winners[key] = bp
		}
	}

	posts := make([]boardPost, 0, len(winners))
	for _, bp := range winners {
		posts = append(posts, bp)
	}
	sort.Slice(posts, func(i, j int) bool {
		a, b := posts[i].post.Header, posts[j].post.Header
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Number < b.Number
	})
	return posts, nil
}

// readBoardPost parses one post file. ok is false for anything that should not
// back a row: an unreadable or unparseable file (logged — a bad post must not
// fail the stage), a post that is not a GitHub mirror post, or a post for
// another repo (silent — that is the normal state of a shared board).
func readBoardPost(path string, entry fs.DirEntry, repos map[string]struct{}) (boardPost, bool) {
	info, err := entry.Info()
	if err != nil {
		return boardPost{}, false // removed since the listing
	}
	if info.Size() > maxBoardPostBytes {
		slog.Warn("skipping oversized github board post", "path", path, "bytes", info.Size())
		return boardPost{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("skipping unreadable github board post", "path", path, "error", err)
		return boardPost{}, false
	}
	post, err := githubmirror.ParsePost(data)
	if err != nil {
		slog.Warn("skipping unparseable github board post", "path", path, "error", err)
		return boardPost{}, false
	}

	header := post.Header
	if _, ours := repos[strings.ToLower(header.Repo)]; header.Source != "github" || !ours {
		return boardPost{}, false
	}
	if header.Kind != githubmirror.KindPullRequest && header.Kind != githubmirror.KindIssue {
		slog.Warn("skipping github board post with unknown kind", "path", path, "kind", header.Kind)
		return boardPost{}, false
	}
	if header.Number <= 0 {
		slog.Warn("skipping github board post without a number", "path", path, "number", header.Number)
		return boardPost{}, false
	}
	return boardPost{path: path, mtime: info.ModTime(), post: post}, true
}

// boardPostBeats reports whether cur should replace best as the post backing
// an item. The comparison is total so the winner never depends on directory
// listing order:
//  1. later last_material_change wins
//  2. later file mtime breaks ties (a title edit mints a new post without
//     advancing last_material_change)
//  3. lex-greater path is the final fallback
func boardPostBeats(cur, best boardPost) bool {
	curChange, bestChange := cur.post.Header.LastMaterialChange, best.post.Header.LastMaterialChange
	if !curChange.Equal(bestChange) {
		return curChange.After(bestChange)
	}
	if !cur.mtime.Equal(best.mtime) {
		return cur.mtime.After(best.mtime)
	}
	return cur.path > best.path
}

// boardPostUnchanged reports whether a post can be skipped: its mtime is
// recorded as indexed and its row still names it as the source. Any doubt —
// a missing row, a lookup error — means "changed", because re-indexing is
// idempotent and skipping wrongly is not.
func boardPostUnchanged(ctx context.Context, s *store.Store, bp boardPost, knownMtimes map[string]int64) bool {
	stored, ok := knownMtimes[bp.path]
	if !ok || stored != bp.mtime.UTC().UnixNano() {
		return false
	}

	header := bp.post.Header
	var source sql.NullString
	if header.Kind == githubmirror.KindPullRequest {
		row, err := s.Queries().GetPRSourceByNumber(ctx, int64(header.Number))
		if err != nil {
			return false
		}
		source = row.SourcePath
	} else {
		row, err := s.Queries().GetIssueSourceByNumber(ctx, int64(header.Number))
		if err != nil {
			return false
		}
		source = row.SourcePath
	}
	return source.Valid && source.String == bp.path
}

// indexBoardPR upserts one PR post via delete-insert, like indexPRFile.
//
// A post carries no commit list and no merge commit, but a Ledger snapshot
// that this row replaces may. Plan collision detection joins merged PRs to
// diffs through exactly those, so they are carried across the overwrite rather
// than dropped: the board wins on every field it can supply.
func indexBoardPR(ctx context.Context, s *store.Store, bp boardPost) error {
	header := bp.post.Header
	labels, err := boardLabels(header.Labels)
	if err != nil {
		return err
	}

	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := codedbsqlc.New(tx)

	var mergeCommit sql.NullString
	var commitSHAs []string
	existing, err := q.GetPRSourceByNumber(ctx, int64(header.Number))
	switch {
	case err == nil:
		mergeCommit = existing.MergeCommit
		if commitSHAs, err = q.ListPRCommitShas(ctx, existing.ID); err != nil {
			return fmt.Errorf("list pr commits: %w", err)
		}
		if err := q.DeletePRCommentsByPR(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete pr comments: %w", err)
		}
		if err := q.DeletePRCommitsByPR(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete pr commits: %w", err)
		}
		if err := q.DeletePullRequest(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete pr: %w", err)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("check existing PR %d: %w", header.Number, err)
	}

	res, err := q.InsertPullRequest(ctx, codedbsqlc.InsertPullRequestParams{
		Number:      int64(header.Number),
		Title:       header.Title,
		Body:        toNullString(bp.post.Body),
		Author:      toNullString(header.Author.Login),
		State:       header.State,
		Labels:      labels,
		CreatedAt:   timeToNullInt64(header.Created),
		MergedAt:    timePtrToNullInt64(header.Merged),
		ClosedAt:    timePtrToNullInt64(header.Closed),
		UpdatedAt:   timeToNullInt64(header.LastMaterialChange),
		MergeCommit: mergeCommit,
		Url:         toNullString(header.URL),
		SourcePath:  toNullString(bp.path),
	})
	if err != nil {
		return fmt.Errorf("insert PR %d: %w", header.Number, err)
	}
	prID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get PR id: %w", err)
	}

	for _, c := range bp.post.Comments {
		if c.Withheld {
			continue // the post holds only a notice; there is no text to index
		}
		if err := q.InsertPRComment(ctx, codedbsqlc.InsertPRCommentParams{
			PrID:      prID,
			Author:    toNullString(c.Login),
			Body:      toNullString(c.Body),
			Path:      toNullString(c.Path),
			Line:      ptrIntToNullInt64(c.Line),
			CreatedAt: timeToNullInt64(c.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert PR %d comment: %w", header.Number, err)
		}
	}
	for _, sha := range commitSHAs {
		if err := q.InsertPRCommit(ctx, codedbsqlc.InsertPRCommitParams{PrID: prID, Sha: sha}); err != nil {
			return fmt.Errorf("insert PR %d commit: %w", header.Number, err)
		}
	}

	return tx.Commit()
}

// indexBoardIssue upserts one issue post via delete-insert, like indexIssueFile.
func indexBoardIssue(ctx context.Context, s *store.Store, bp boardPost) error {
	header := bp.post.Header
	labels, err := boardLabels(header.Labels)
	if err != nil {
		return err
	}

	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	q := codedbsqlc.New(tx)

	existing, err := q.GetIssueSourceByNumber(ctx, int64(header.Number))
	switch {
	case err == nil:
		if err := q.DeleteIssueCommentsByIssue(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete issue comments: %w", err)
		}
		if err := q.DeleteIssue(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete issue: %w", err)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("check existing issue %d: %w", header.Number, err)
	}

	res, err := q.InsertIssue(ctx, codedbsqlc.InsertIssueParams{
		Number:     int64(header.Number),
		Title:      header.Title,
		Body:       toNullString(bp.post.Body),
		Author:     toNullString(header.Author.Login),
		State:      header.State,
		Labels:     labels,
		CreatedAt:  timeToNullInt64(header.Created),
		ClosedAt:   timePtrToNullInt64(header.Closed),
		UpdatedAt:  timeToNullInt64(header.LastMaterialChange),
		Url:        toNullString(header.URL),
		SourcePath: toNullString(bp.path),
	})
	if err != nil {
		return fmt.Errorf("insert issue %d: %w", header.Number, err)
	}
	issueID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("get issue id: %w", err)
	}

	for _, c := range bp.post.Comments {
		if c.Withheld {
			continue
		}
		if err := q.InsertIssueComment(ctx, codedbsqlc.InsertIssueCommentParams{
			IssueID:   issueID,
			Author:    toNullString(c.Login),
			Body:      toNullString(c.Body),
			CreatedAt: timeToNullInt64(c.CreatedAt),
		}); err != nil {
			return fmt.Errorf("insert issue %d comment: %w", header.Number, err)
		}
	}

	return tx.Commit()
}

// boardLabels JSON-encodes labels like the Ledger indexer (label names may
// contain commas). No labels is NULL rather than "[]" or "null": triage splits
// the raw column on commas and would show those literals as a label.
func boardLabels(labels []string) (sql.NullString, error) {
	if len(labels) == 0 {
		return sql.NullString{}, nil
	}
	encoded, err := json.Marshal(labels)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode labels: %w", err)
	}
	return toNullString(string(encoded)), nil
}
