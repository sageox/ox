package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/query"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/githubmirror/mirrortest"
	"github.com/sageox/ox/internal/ledger"
)

// The fixtures below are written by hand from the "Rendered post" layout in
// docs/specs/github-bulletin-mirror.md, not produced by the mirror's renderer,
// so these tests also fail if the indexer and the spec drift apart.

const boardMemberPR = `---
source: github
repo: acme/api
kind: pull_request
number: 1287
url: https://github.com/acme/api/pull/1287
state: merged
title: Mirror GitHub activity onto the bulletin board
author: {login: devon-dev, id: 5550101, association: MEMBER}
trust: member
labels: [daemon, "needs review"]
created: 2026-09-28T17:02:11Z
merged: 2026-10-08T16:59:40Z
last_material_change: 2026-10-08T16:59:40Z
review: {approved: [avery-dev]}
files: [internal/daemon/github_sync.go]
omitted: {bot_comments: 9, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# PR #1287 — Mirror GitHub activity onto the bulletin board

Teammates see GitHub context without the bot noise.

## Summary

- Fetch, clean and relay pull requests and issues.

The renderer escapes a reserved line inside a description:
\## Discussion

## Discussion

### @avery-dev · member · 2026-10-02T10:00:00Z

Looks good to me.

### @quinn-dev · member · 2026-10-02T11:30:00Z

Approved after the cleanup tweak.
`

const boardExternalIssue = `---
source: github
repo: acme/api
kind: issue
number: 412
url: https://github.com/acme/api/issues/412
state: open
title: Daemon stalls on large repos
author: {login: drive-by-user, id: 7770001, association: NONE}
trust: external
labels: [bug]
created: 2026-10-01T08:00:00Z
last_material_change: 2026-10-03T10:00:00Z
omitted: {bot_comments: 0, withheld: 1, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# Issue #412 — Daemon stalls on large repos

> The daemon stops syncing after ten minutes.
>
> Steps to reproduce are attached.

## Discussion

### @drive-by-user · external · 2026-10-03T09:00:00Z

> ⚠ Withheld by the SageOx safety scan. Read it on GitHub.

### @avery-dev · member · 2026-10-03T10:00:00Z

Reproduced on main.
`

// boardBareIssue has no labels and no Discussion section, and spells the repo
// with different case than the daemon's remote does.
const boardBareIssue = `---
source: github
repo: Acme/API
kind: issue
number: 77
url: https://github.com/acme/api/issues/77
state: closed
title: Typo in the README
author: {login: devon-dev, id: 5550101, association: OWNER}
trust: member
created: 2026-09-01T08:00:00Z
closed: 2026-09-02T08:00:00Z
last_material_change: 2026-09-02T08:00:00Z
omitted: {bot_comments: 0, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# Issue #77 — Typo in the README

Fix the heading.
`

// boardOtherRepoPR reuses PR number 1287 and is newer than boardMemberPR, so
// it would take over the acme/api row if the repo filter let it through.
const boardOtherRepoPR = `---
source: github
repo: other/service
kind: pull_request
number: 1287
url: https://github.com/other/service/pull/1287
state: open
title: Unrelated change in another repo
author: {login: casey-dev, id: 5550102, association: MEMBER}
trust: member
created: 2026-10-01T08:00:00Z
last_material_change: 2026-10-09T08:00:00Z
omitted: {bot_comments: 0, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# PR #1287 — Unrelated change in another repo

Not for this repo.
`

const (
	boardPRName       = "acme-api-pr-1287-aaaa1111.md"
	boardIssueName    = "acme-api-issue-412-bbbb2222.md"
	boardBareName     = "acme-api-issue-77-cccc3333.md"
	boardOtherName    = "other-service-pr-1287-dddd4444.md"
	boardBrokenName   = "broken.md"
	boardRepoFullName = "acme/api"
)

func openBoardStore(t *testing.T) *store.Store {
	t.Helper()
	if testing.Short() {
		t.Skip("short: opening a CodeDB store takes ~0.7s")
	}
	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// board replacement must preserve inline reviewer classifications without reviving withheld text.
func TestIndexGitHubBoard_PreservesReviewerClassifications(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	line := 12
	person := func(login string, id int64) githubmirror.Author {
		return githubmirror.Author{Login: login, ID: id, Type: "User", Association: "MEMBER"}
	}
	item := githubmirror.Item{
		Kind: githubmirror.KindPullRequest, Number: 91, State: githubmirror.StateOpen,
		Title: "Preserve review classifications", Author: person("author-one", 1),
		CreatedAt: created, LastMaterialChangeAt: created,
		Comments: []githubmirror.Comment{
			{ID: 1, Author: person("reviewer-one", 2), Body: "Current inline review", Path: "current.go", Line: &line, CreatedAt: created},
			{ID: 2, Author: githubmirror.Author{Login: "automation[bot]", ID: 3, Type: "Bot"}, Body: "Bot noise", Path: "bot.go", CreatedAt: created},
			{ID: 3, Author: person("withheld-reviewer", 4), Body: "Withheld text", Path: "withheld.go", Line: &line, CreatedAt: created},
			{ID: 4, Author: person("discussant-one", 5), Body: "Discussion", CreatedAt: created},
			{ID: 5, Author: person("reviewer-two", 6), Body: "Outdated inline review", Path: "outdated.go", CreatedAt: created},
		},
	}
	rendered := mirrortest.RenderPost(githubmirror.Repo{Owner: "acme", Name: "api"}, item, map[int64]bool{3: true})
	require.NotContains(t, string(rendered), "Withheld text")
	require.NotContains(t, string(rendered), "withheld.go")
	dir := t.TempDir()
	path := writeBoardFile(t, dir, "acme-api-pr-91.md", string(rendered))
	setBoardMtime(t, path, created)

	for pass, wantIndexed := range []int{1, 0, 1} {
		if pass == 2 {
			item.Title = "Updated review classifications"
			rendered = mirrortest.RenderPost(githubmirror.Repo{Owner: "acme", Name: "api"}, item, map[int64]bool{3: true})
			require.NoError(t, os.WriteFile(path, rendered, 0o644))
			setBoardMtime(t, path, created.Add(time.Second))
		}
		stats, err := IndexGitHubBoard(ctx, s, dir, boardRepoFullName, nil)
		require.NoError(t, err)
		assert.Equal(t, wantIndexed, stats.PRsIndexed)
		rows, err := query.TriagePRs(ctx, s, query.TriageOpts{})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, item.Title, rows[0].Title)
		assert.Equal(t, 2, rows[0].Reviewers)
		assert.Equal(t, 1, rows[0].Discussants)
		assert.Equal(t, 3, rows[0].Comments)
		assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pr_comments WHERE path = 'current.go' AND line = 12`))
		assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pr_comments WHERE path = 'outdated.go' AND line IS NULL`))
		assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM pr_comments WHERE body IN ('Withheld text', 'Bot noise')`))
	}
}

func writeBoardFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func setBoardMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, mtime, mtime))
}

// boardPR renders a minimal acme/api pull request post. title and lastChange
// vary so a test can tell two posts for one item apart.
func boardPR(number int, title, lastChange string) string {
	return fmt.Sprintf(`---
source: github
repo: acme/api
kind: pull_request
number: %d
url: https://github.com/acme/api/pull/%d
state: open
title: %s
author: {login: devon-dev, id: 5550101, association: MEMBER}
trust: member
created: 2026-09-28T17:02:11Z
last_material_change: %s
omitted: {bot_comments: 0, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# PR #%d — %s

Body of %s.
`, number, number, title, lastChange, number, title, title)
}

type boardPRRow struct {
	title, body, author, state string
	labels, url, source, merge sql.NullString
	created, merged, updated   sql.NullInt64
}

func readPRRow(t *testing.T, s *store.Store, number int) boardPRRow {
	t.Helper()
	var r boardPRRow
	err := s.QueryRow(`SELECT title, COALESCE(body, ''), COALESCE(author, ''), state, labels, url, source_path,
		merge_commit, created_at, merged_at, updated_at FROM pull_requests WHERE number = ?`, number).
		Scan(&r.title, &r.body, &r.author, &r.state, &r.labels, &r.url, &r.source, &r.merge, &r.created, &r.merged, &r.updated)
	require.NoError(t, err, "PR %d row", number)
	return r
}

func readIssueRow(t *testing.T, s *store.Store, number int) (title, body, state string, labels, source sql.NullString) {
	t.Helper()
	err := s.QueryRow(`SELECT title, COALESCE(body, ''), state, labels, source_path FROM issues WHERE number = ?`, number).
		Scan(&title, &body, &state, &labels, &source)
	require.NoError(t, err, "issue %d row", number)
	return title, body, state, labels, source
}

func countRows(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.QueryRow(query, args...).Scan(&n))
	return n
}

// commentLines returns "author: body" for each comment of the item, oldest first.
func commentLines(t *testing.T, s *store.Store, query string, number int) []string {
	t.Helper()
	rows, err := s.Query(query, number)
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var author, body string
		require.NoError(t, rows.Scan(&author, &body))
		lines = append(lines, author+": "+body)
	}
	require.NoError(t, rows.Err())
	return lines
}

func prCommentLines(t *testing.T, s *store.Store, number int) []string {
	return commentLines(t, s, `SELECT COALESCE(c.author, ''), COALESCE(c.body, '') FROM pr_comments c
		JOIN pull_requests p ON p.id = c.pr_id WHERE p.number = ? ORDER BY c.id`, number)
}

func issueCommentLines(t *testing.T, s *store.Store, number int) []string {
	return commentLines(t, s, `SELECT COALESCE(c.author, ''), COALESCE(c.body, '') FROM issue_comments c
		JOIN issues i ON i.id = c.issue_id WHERE i.number = ? ORDER BY c.id`, number)
}

func prCommitSHAs(t *testing.T, s *store.Store, number int) []string {
	t.Helper()
	rows, err := s.Query(`SELECT c.sha FROM pr_commits c JOIN pull_requests p ON p.id = c.pr_id WHERE p.number = ? ORDER BY c.id`, number)
	require.NoError(t, err)
	defer rows.Close()
	var shas []string
	for rows.Next() {
		var sha string
		require.NoError(t, rows.Scan(&sha))
		shas = append(shas, sha)
	}
	require.NoError(t, rows.Err())
	return shas
}

func unix(year int, month time.Month, day, hour, minute, second int) int64 {
	return time.Date(year, month, day, hour, minute, second, 0, time.UTC).Unix()
}

func TestIndexGitHubBoard_IndexesThisReposPosts(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	prPath := writeBoardFile(t, dir, boardPRName, boardMemberPR)
	issuePath := writeBoardFile(t, dir, boardIssueName, boardExternalIssue)
	barePath := writeBoardFile(t, dir, boardBareName, boardBareIssue)
	otherPath := writeBoardFile(t, dir, boardOtherName, boardOtherRepoPR)
	brokenPath := writeBoardFile(t, dir, boardBrokenName, "this is not a post\n")
	// the server writes a .meta.json beside every post, and a folder can hold strays
	writeBoardFile(t, dir, "acme-api-pr-1287-aaaa1111.meta.json", `{"board":"github","slug":"acme-api-pr-1287"}`)
	writeBoardFile(t, dir, "notes.txt", "not a post")

	var progress []string
	stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, func(msg string) { progress = append(progress, msg) })
	require.NoError(t, err)
	assert.Equal(t, 1, stats.PRsIndexed)
	assert.Equal(t, 2, stats.IssuesIndexed)
	assert.NotEmpty(t, progress, "first change should announce itself")

	t.Run("member PR carries header, body and comments", func(t *testing.T) {
		row := readPRRow(t, s, 1287)
		assert.Equal(t, "Mirror GitHub activity onto the bulletin board", row.title)
		assert.True(t, strings.HasPrefix(row.body, "Teammates see GitHub context without the bot noise."), "body: %q", row.body)
		// the description runs to the first exact "## Discussion"/"## Files touched" line:
		// a member's own heading stays, and an escaped reserved line loses its one backslash
		assert.Contains(t, row.body, "\n## Summary\n")
		assert.Contains(t, row.body, "Fetch, clean and relay pull requests and issues.")
		assert.Contains(t, row.body, "\n## Discussion")
		assert.NotContains(t, row.body, `\## Discussion`)
		assert.Equal(t, "devon-dev", row.author)
		assert.Equal(t, "merged", row.state)
		assert.JSONEq(t, `["daemon","needs review"]`, row.labels.String)
		assert.Equal(t, "https://github.com/acme/api/pull/1287", row.url.String)
		assert.Equal(t, prPath, row.source.String)
		assert.False(t, row.merge.Valid, "a post carries no merge commit")
		assert.Equal(t, unix(2026, 9, 28, 17, 2, 11), row.created.Int64)
		assert.Equal(t, unix(2026, 10, 8, 16, 59, 40), row.merged.Int64)
		assert.Equal(t, unix(2026, 10, 8, 16, 59, 40), row.updated.Int64, "updated_at is last_material_change")

		assert.Equal(t, []string{
			"avery-dev: Looks good to me.",
			"quinn-dev: Approved after the cleanup tweak.",
		}, prCommentLines(t, s, 1287))
	})

	t.Run("external issue is unquoted and its withheld comment is not indexed", func(t *testing.T) {
		title, body, state, labels, source := readIssueRow(t, s, 412)
		assert.Equal(t, "Daemon stalls on large repos", title)
		assert.Equal(t, "open", state)
		assert.JSONEq(t, `["bug"]`, labels.String)
		assert.Equal(t, issuePath, source.String)
		assert.Contains(t, body, "The daemon stops syncing after ten minutes.")
		assert.Contains(t, body, "Steps to reproduce are attached.")
		for _, line := range strings.Split(body, "\n") {
			assert.False(t, strings.HasPrefix(line, ">"), "the post's quoting is presentation, not content: %q", line)
		}

		comments := issueCommentLines(t, s, 412)
		assert.Equal(t, []string{"avery-dev: Reproduced on main."}, comments)
	})

	t.Run("repo match ignores case; no labels is NULL; no Discussion means no comments", func(t *testing.T) {
		title, _, state, labels, source := readIssueRow(t, s, 77)
		assert.Equal(t, "Typo in the README", title)
		assert.Equal(t, "closed", state)
		assert.False(t, labels.Valid, "no labels must not be stored as the literal null or []")
		assert.Equal(t, barePath, source.String)
		assert.Empty(t, issueCommentLines(t, s, 77))
	})

	t.Run("another repo's post and bad files never become rows", func(t *testing.T) {
		assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pull_requests`), "other/service #1287 must not replace acme/api #1287")
		assert.Equal(t, 2, countRows(t, s, `SELECT COUNT(*) FROM issues`))
		assert.Equal(t, 0, countRows(t, s, `SELECT COUNT(*) FROM github_file_mtimes WHERE source_path IN (?, ?)`, otherPath, brokenPath),
			"skipped files must not be recorded as indexed")
	})

	t.Run("indexed posts are recorded for the unchanged skip", func(t *testing.T) {
		assert.Equal(t, 3, countRows(t, s, `SELECT COUNT(*) FROM github_file_mtimes WHERE source_path IN (?, ?, ?)`, prPath, issuePath, barePath))
	})
}

func TestIndexGitHubBoard_NoopInputs(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	writeBoardFile(t, existing, boardPRName, boardMemberPR)

	tests := []struct {
		name     string
		postsDir string
		repo     string
	}{
		{"missing posts dir", filepath.Join(t.TempDir(), "bulletin", "github", "posts"), boardRepoFullName},
		{"empty posts dir argument", "", boardRepoFullName},
		{"empty repo name must not import every repo's items", existing, ""},
		{"empty folder", t.TempDir(), boardRepoFullName},
		{"repo with no posts", existing, "acme/unrelated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := openBoardStore(t)
			stats, err := IndexGitHubBoard(context.Background(), s, tt.postsDir, tt.repo, nil)
			require.NoError(t, err)
			require.NotNil(t, stats)
			assert.Zero(t, stats.PRsIndexed)
			assert.Zero(t, stats.IssuesIndexed)
			assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM pull_requests`))
		})
	}
}

func TestIndexGitHubBoard_NewestPostWinsForOneItem(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	tests := []struct {
		name      string
		a, b      string // file names; both posts are PR 1
		aChange   string
		bChange   string
		aMtime    time.Time
		bMtime    time.Time
		wantTitle string
	}{
		{
			name: "later last_material_change wins even from a lex-lower name and older file",
			a:    "acme-api-pr-1-aaaa.md", b: "acme-api-pr-1-zzzz.md",
			aChange: "2026-10-05T00:00:00Z", bChange: "2026-10-01T00:00:00Z",
			aMtime: older, bMtime: newer,
			wantTitle: "post-a",
		},
		{
			name: "equal change time: newer file mtime wins (a title edit mints a post without moving the clock)",
			a:    "acme-api-pr-1-zzzz.md", b: "acme-api-pr-1-aaaa.md",
			aChange: "2026-10-05T00:00:00Z", bChange: "2026-10-05T00:00:00Z",
			aMtime: older, bMtime: newer,
			wantTitle: "post-b",
		},
		{
			name: "equal change time and mtime: lex-greater name is the deterministic fallback",
			a:    "acme-api-pr-1-aaaa.md", b: "acme-api-pr-1-zzzz.md",
			aChange: "2026-10-05T00:00:00Z", bChange: "2026-10-05T00:00:00Z",
			aMtime: older, bMtime: older,
			wantTitle: "post-b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := openBoardStore(t)
			dir := t.TempDir()
			pathA := writeBoardFile(t, dir, tt.a, boardPR(1, "post-a", tt.aChange))
			pathB := writeBoardFile(t, dir, tt.b, boardPR(1, "post-b", tt.bChange))
			setBoardMtime(t, pathA, tt.aMtime)
			setBoardMtime(t, pathB, tt.bMtime)

			stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, stats.PRsIndexed, "one item is one row, however many posts exist")
			assert.Equal(t, tt.wantTitle, readPRRow(t, s, 1).title)
			assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pull_requests`))

			// the loser stays on disk until the server cleans it up; it must not flip the row back
			again, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
			require.NoError(t, err)
			assert.Zero(t, again.PRsIndexed)
			assert.Equal(t, tt.wantTitle, readPRRow(t, s, 1).title)
		})
	}
}

// writeLedgerPR indexes a Ledger snapshot of PR 1287 the way the daemon's
// Ledger stage does, so a test can start from a row the Ledger wrote.
func writeLedgerPR(t *testing.T, s *store.Store, ledgerPath, title string) {
	t.Helper()
	created := time.Date(2026, 9, 28, 17, 2, 11, 0, time.UTC)
	require.NoError(t, ledger.WriteGitHubPR(ledgerPath, &ledger.PRFile{
		Number:      1287,
		Title:       title,
		Body:        "ledger body",
		Author:      "ledger-author",
		State:       "open",
		Labels:      []string{"stale"},
		CreatedAt:   created,
		UpdatedAt:   created.Add(24 * time.Hour),
		MergeCommit: "merge123",
		URL:         "https://github.com/acme/api/pull/1287",
		Comments:    []ledger.PRComment{{Author: "ledger-commenter", Body: "ledger comment", CreatedAt: created}},
		Commits:     []ledger.PRCommit{{SHA: "aaa111", Author: "ledger-author", Date: created}, {SHA: "bbb222", Author: "ledger-author", Date: created}},
	}))
	_, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	require.NoError(t, err)
}

func TestIndexGitHubBoard_BoardWinsOverLedgerSnapshot(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	ledgerPath := t.TempDir()
	writeLedgerPR(t, s, ledgerPath, "Ledger title")
	require.NoError(t, ledger.WriteGitHubIssue(ledgerPath, &ledger.IssueFile{
		Number: 412, Title: "Ledger issue title", Body: "ledger issue body", Author: "ledger-author", State: "closed",
		CreatedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC),
		Comments: []ledger.IssueComment{{Author: "ledger-commenter", Body: "ledger issue comment"}},
	}))
	_, err := IndexGitHubData(context.Background(), s, ledgerPath, nil)
	require.NoError(t, err)

	require.Equal(t, "Ledger title", readPRRow(t, s, 1287).title, "precondition: the Ledger wrote the PR row")
	require.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM issues WHERE title = 'Ledger issue title'`), "precondition: the Ledger wrote the issue row")

	dir := t.TempDir()
	prPath := writeBoardFile(t, dir, boardPRName, boardMemberPR)
	issuePath := writeBoardFile(t, dir, boardIssueName, boardExternalIssue)

	stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.PRsIndexed)
	assert.Equal(t, 1, stats.IssuesIndexed)

	t.Run("PR row and comments are the board's", func(t *testing.T) {
		row := readPRRow(t, s, 1287)
		assert.Equal(t, "Mirror GitHub activity onto the bulletin board", row.title)
		assert.Equal(t, "merged", row.state)
		assert.Equal(t, prPath, row.source.String)
		assert.Equal(t, 2, len(prCommentLines(t, s, 1287)))
		for _, line := range prCommentLines(t, s, 1287) {
			assert.NotContains(t, line, "ledger", "Ledger comments must not survive the overwrite")
		}
		assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pull_requests WHERE number = 1287`))
	})

	t.Run("facts a post cannot carry survive the overwrite", func(t *testing.T) {
		// plan collision detection joins merged PRs to diffs through these
		assert.Equal(t, "merge123", readPRRow(t, s, 1287).merge.String)
		assert.Equal(t, []string{"aaa111", "bbb222"}, prCommitSHAs(t, s, 1287))
	})

	t.Run("issue row and comments are the board's", func(t *testing.T) {
		title, _, state, _, source := readIssueRow(t, s, 412)
		assert.Equal(t, "Daemon stalls on large repos", title)
		assert.Equal(t, "open", state)
		assert.Equal(t, issuePath, source.String)
		assert.Equal(t, []string{"avery-dev: Reproduced on main."}, issueCommentLines(t, s, 412))
	})
}

func TestIndexGitHubBoard_LedgerOverwriteIsUndoneOnNextRun(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	prPath := writeBoardFile(t, dir, boardPRName, boardMemberPR)
	ledgerPath := t.TempDir()

	first, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	require.Equal(t, 1, first.PRsIndexed)

	// a new Ledger snapshot lands and the Ledger stage overwrites the shared row;
	// the post file itself is untouched, so its recorded mtime still matches
	writeLedgerPR(t, s, ledgerPath, "Ledger title")
	require.Equal(t, "Ledger title", readPRRow(t, s, 1287).title, "precondition: the Ledger took the row")
	require.NotEqual(t, prPath, readPRRow(t, s, 1287).source.String, "precondition: the post is no longer the source")

	second, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, second.PRsIndexed, "an unchanged post must be re-indexed once the Ledger replaced its row")
	row := readPRRow(t, s, 1287)
	assert.Equal(t, "Mirror GitHub activity onto the bulletin board", row.title)
	assert.Equal(t, prPath, row.source.String)

	third, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Zero(t, third.PRsIndexed, "once the post owns the row again nothing is left to do")
}

func TestIndexGitHubBoard_UnchangedPostIsSkipped(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	prPath := writeBoardFile(t, dir, boardPRName, boardMemberPR)
	issuePath := writeBoardFile(t, dir, boardIssueName, boardExternalIssue)

	first, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	require.Equal(t, 2, first.PRsIndexed+first.IssuesIndexed)

	// a sentinel only a rewrite could erase proves the second run skipped the
	// posts, rather than re-indexing them and merely reporting zero
	_, err = s.Exec(`UPDATE pull_requests SET title = 'sentinel' WHERE number = 1287`)
	require.NoError(t, err)
	_, err = s.Exec(`UPDATE issues SET title = 'sentinel' WHERE number = 412`)
	require.NoError(t, err)

	var progress []string
	second, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, func(msg string) { progress = append(progress, msg) })
	require.NoError(t, err)
	assert.Zero(t, second.PRsIndexed+second.IssuesIndexed)
	assert.Empty(t, progress, "nothing changed, so nothing to announce")
	assert.Equal(t, "sentinel", readPRRow(t, s, 1287).title)

	// a post whose mtime moved is a changed post, and only that post
	later := time.Now().Add(time.Hour)
	setBoardMtime(t, prPath, later)
	third, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, third.PRsIndexed)
	assert.Zero(t, third.IssuesIndexed)
	assert.Equal(t, "Mirror GitHub activity onto the bulletin board", readPRRow(t, s, 1287).title)
	title, _, _, _, source := readIssueRow(t, s, 412)
	assert.Equal(t, "sentinel", title)
	assert.Equal(t, issuePath, source.String)
}

func TestIndexGitHubBoard_ReplacedPostReplacesTheRow(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	oldPath := writeBoardFile(t, dir, "acme-api-pr-5-aaaa.md", boardPR(5, "before the edit", "2026-10-01T00:00:00Z"))

	_, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	require.Equal(t, oldPath, readPRRow(t, s, 5).source.String)

	// the server publishes the edit as a new post and removes the old one
	newPath := writeBoardFile(t, dir, "acme-api-pr-5-bbbb.md", boardPR(5, "after the edit", "2026-10-02T00:00:00Z"))
	require.NoError(t, os.Remove(oldPath))

	stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.PRsIndexed)
	row := readPRRow(t, s, 5)
	assert.Equal(t, "after the edit", row.title)
	assert.Equal(t, newPath, row.source.String)
	assert.Equal(t, 1, countRows(t, s, `SELECT COUNT(*) FROM pull_requests WHERE number = 5`))
}

func TestIndexGitHubBoard_ExpiredPostLeavesItsRow(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	path := writeBoardFile(t, dir, boardPRName, boardMemberPR)

	_, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)

	// the server removes a post 90 days after the last change; GitHub remains the
	// source of truth, so the history the index already holds stays searchable
	require.NoError(t, os.Remove(path))
	stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
	require.NoError(t, err)
	assert.Zero(t, stats.PRsIndexed)
	assert.Equal(t, "Mirror GitHub activity onto the bulletin board", readPRRow(t, s, 1287).title)
}

func TestIndexGitHubBoard_SkipsFilesThatAreNotPosts(t *testing.T) {
	t.Parallel()

	t.Run("oversized post", func(t *testing.T) {
		t.Parallel()
		s := openBoardStore(t)
		dir := t.TempDir()
		huge := boardPR(9, "huge", "2026-10-01T00:00:00Z") + strings.Repeat("x", maxBoardPostBytes)
		writeBoardFile(t, dir, "acme-api-pr-9-aaaa.md", huge)
		writeBoardFile(t, dir, "acme-api-pr-10-bbbb.md", boardPR(10, "fine", "2026-10-01T00:00:00Z"))

		stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, stats.PRsIndexed)
		assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM pull_requests WHERE number = 9`))
	})

	t.Run("symlink and subdirectory", func(t *testing.T) {
		t.Parallel()
		s := openBoardStore(t)
		dir := t.TempDir()
		outside := writeBoardFile(t, t.TempDir(), "elsewhere.md", boardPR(11, "outside the board", "2026-10-01T00:00:00Z"))
		if err := os.Symlink(outside, filepath.Join(dir, "acme-api-pr-11-aaaa.md")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		require.NoError(t, os.Mkdir(filepath.Join(dir, "nested.md"), 0o755))

		stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
		require.NoError(t, err)
		assert.Zero(t, stats.PRsIndexed, "a post is a regular file the board wrote, not a link to one")
	})

	t.Run("source other than github, or no number", func(t *testing.T) {
		t.Parallel()
		s := openBoardStore(t)
		dir := t.TempDir()
		writeBoardFile(t, dir, "acme-api-pr-12-aaaa.md", strings.Replace(boardPR(12, "wrong source", "2026-10-01T00:00:00Z"), "source: github", "source: gitlab", 1))
		writeBoardFile(t, dir, "acme-api-pr-0-bbbb.md", boardPR(0, "no number", "2026-10-01T00:00:00Z"))
		writeBoardFile(t, dir, "acme-api-pr-13-cccc.md", strings.Replace(boardPR(13, "odd kind", "2026-10-01T00:00:00Z"), "kind: pull_request", "kind: discussion", 1))

		stats, err := IndexGitHubBoard(context.Background(), s, dir, boardRepoFullName, nil)
		require.NoError(t, err)
		assert.Zero(t, stats.PRsIndexed+stats.IssuesIndexed)
		assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM pull_requests`))
	})
}

func TestIndexGitHubBoard_CanceledContext(t *testing.T) {
	t.Parallel()

	s := openBoardStore(t)
	dir := t.TempDir()
	writeBoardFile(t, dir, boardPRName, boardMemberPR)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := IndexGitHubBoard(ctx, s, dir, boardRepoFullName, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Zero(t, countRows(t, s, `SELECT COUNT(*) FROM pull_requests`))
}

func TestBoardFingerprint(t *testing.T) {
	t.Parallel()

	t.Run("missing directory has no fingerprint", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, BoardFingerprint(filepath.Join(t.TempDir(), "absent")))
	})

	t.Run("tracks posts coming, going and changing, and ignores everything else", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

		empty := BoardFingerprint(dir)
		require.NotEmpty(t, empty, "an existing empty board still has a fingerprint")
		assert.Equal(t, empty, BoardFingerprint(dir), "stable while nothing changes")

		first := writeBoardFile(t, dir, "acme-api-pr-1-aaaa.md", boardPR(1, "one", "2026-10-01T00:00:00Z"))
		setBoardMtime(t, first, base)
		withOne := BoardFingerprint(dir)
		assert.NotEqual(t, empty, withOne, "a new post changes the fingerprint")

		writeBoardFile(t, dir, "acme-api-pr-1-aaaa.meta.json", "{}")
		writeBoardFile(t, dir, "notes.txt", "x")
		assert.Equal(t, withOne, BoardFingerprint(dir), "meta files and strays are not posts")

		second := writeBoardFile(t, dir, "acme-api-pr-2-bbbb.md", boardPR(2, "two", "2026-10-01T00:00:00Z"))
		setBoardMtime(t, second, base.Add(time.Hour))
		withTwo := BoardFingerprint(dir)
		assert.NotEqual(t, withOne, withTwo)

		setBoardMtime(t, first, base.Add(2*time.Hour))
		rewritten := BoardFingerprint(dir)
		assert.NotEqual(t, withTwo, rewritten, "an in-place rewrite moves the newest mtime")

		require.NoError(t, os.Remove(second))
		assert.NotEqual(t, rewritten, BoardFingerprint(dir), "a removed post changes the count")
	})
}
