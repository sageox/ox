package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/githubmirror"
	"github.com/sageox/ox/internal/ledger"
)

// --- GitHub board stage: the team bulletin board's mirrored posts feed CodeDB ---
//
// The mirror publishes posts through the Team Context pull, so a board change
// never moves this repo's git HEAD. These tests cover the two things the daemon
// adds around index.IndexGitHubBoard: resolving which posts are this repo's, and
// noticing that the board changed when HEAD did not.

// boardProject is a project root wired to a Team Context checkout the way
// config.FindRepoTeamContext expects, plus the board's posts directory.
type boardProject struct {
	root     string
	postsDir string
}

// newBoardProject builds a git repo with one commit, an optional "origin"
// remote, and a Team Context whose board holds no posts yet.
func newBoardProject(t *testing.T, remoteURL string) boardProject {
	t.Helper()
	if testing.Short() {
		t.Skip("short: requires git commands")
	}

	root := t.TempDir()
	seedGitRepo(t, root)
	if remoteURL != "" {
		cmd := exec.Command("git", "remote", "add", "origin", remoteURL)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git remote add: %s", out)
	}

	teamContext := t.TempDir()
	require.NoError(t, config.SaveProjectConfig(root, &config.ProjectConfig{TeamID: "team_board_test"}))
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{
		TeamContexts: []config.TeamContext{{TeamID: "team_board_test", Path: teamContext}},
	}))

	postsDir := githubmirror.PostsDir(teamContext)
	require.NoError(t, os.MkdirAll(postsDir, 0o755))
	return boardProject{root: root, postsDir: postsDir}
}

// writePost writes a minimal pull request post in the spec's rendered layout.
func (p boardProject) writePost(t *testing.T, repo string, number int, title string) {
	t.Helper()
	content := fmt.Sprintf(`---
source: github
repo: %s
kind: pull_request
number: %d
url: https://github.com/%s/pull/%d
state: open
title: %s
author: {login: devon-dev, id: 5550101, association: MEMBER}
trust: member
created: 2026-09-28T17:02:11Z
last_material_change: 2026-10-08T16:59:40Z
omitted: {bot_comments: 0, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# PR #%d — %s

Description of %s.
`, repo, number, repo, number, title, number, title, title)
	require.NoError(t, os.WriteFile(p.postPath(repo, number), []byte(content), 0o644))
}

// postPath is where the server files a pull request post: <slug>-<sha>.md, the
// name the readers' slug-prefix prefilter relies on.
func (p boardProject) postPath(repo string, number int) string {
	owner, name, _ := strings.Cut(repo, "/")
	slug := githubmirror.Slug(owner, name, githubmirror.KindPullRequest, number)
	return filepath.Join(p.postsDir, slug+"-aaaa1111.md")
}

// fingerprint is the board fingerprint the daemon should hold for repos, each
// "owner/name": only their posts are watched.
func (p boardProject) fingerprint(repos ...string) string {
	return index.BoardFingerprint(p.postsDir, slugPrefixesOf(repos)...)
}

func slugPrefixesOf(repos []string) []string {
	prefixes := make([]string, 0, len(repos))
	for _, repo := range repos {
		owner, name, _ := strings.Cut(repo, "/")
		prefixes = append(prefixes, githubmirror.SlugPrefix(owner, name))
	}
	return prefixes
}

func (m *CodeDBManager) recordedBoardFingerprint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastIndexedBoard
}

func prTitle(t *testing.T, db *codedb.DB, number int) (string, bool) {
	t.Helper()
	var title string
	err := db.Store().QueryRow("SELECT title FROM pull_requests WHERE number = ?", number).Scan(&title)
	if err != nil {
		return "", false
	}
	return title, true
}

func TestCodeDBManager_BoardChanged(t *testing.T) {
	t.Parallel()

	t.Run("a project without a Team Context never reports a change", func(t *testing.T) {
		t.Parallel()
		mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
		assert.False(t, mgr.boardChanged())
	})

	t.Run("follows the board from empty to posts to edits", func(t *testing.T) {
		t.Parallel()
		project := newBoardProject(t, "https://github.com/acme/api.git")
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)

		// nothing indexed yet and an empty board: the first index records it
		assert.True(t, mgr.boardChanged(), "an existing board that was never indexed is a change")
		mgr.lastIndexedBoard = project.fingerprint("acme/api")
		assert.False(t, mgr.boardChanged())

		project.writePost(t, "acme/api", 1, "first")
		assert.True(t, mgr.boardChanged(), "a teammate's PR landing on the board")
		mgr.lastIndexedBoard = project.fingerprint("acme/api")
		assert.False(t, mgr.boardChanged())

		require.NoError(t, os.Remove(project.postPath("acme/api", 1)))
		assert.True(t, mgr.boardChanged(), "a post the server removed")
	})

	t.Run("a project with no GitHub remote has no posts to watch", func(t *testing.T) {
		t.Parallel()
		project := newBoardProject(t, "")
		project.writePost(t, "acme/api", 1, "first")
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		assert.False(t, mgr.boardChanged())
	})

	// Failure prevented: another repo's PR sending this project through the
	// whole index pipeline (open stores, read commit hashes, rebuild dirty
	// indexes) although nothing it indexes changed.
	t.Run("another repo's posts are not a change", func(t *testing.T) {
		t.Parallel()
		project := newBoardProject(t, "https://github.com/acme/api.git")
		project.writePost(t, "acme/api", 1, "first")
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		mgr.lastIndexedBoard = project.fingerprint("acme/api")
		require.False(t, mgr.boardChanged())

		project.writePost(t, "acme/web", 4, "someone else's change")
		project.writePost(t, "other/service", 5, "another team repo")
		assert.False(t, mgr.boardChanged(), "posts for acme/web and other/service")

		project.writePost(t, "acme/api", 2, "second")
		assert.True(t, mgr.boardChanged(), "a post for this repo")
	})

	t.Run("a board directory that does not exist yet is not a change", func(t *testing.T) {
		t.Parallel()
		project := newBoardProject(t, "")
		require.NoError(t, os.RemoveAll(project.postsDir))
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		assert.False(t, mgr.boardChanged(), "the mirror creates the directory with its first post; until then there is nothing to index")
	})
}

// TestIndexGitHubBoardStage covers the doIndex board stage against a real CodeDB.
// Failure prevented: the board never reaches ox code prs, or a stage that cannot
// finish leaves CheckFreshness re-running the whole pipeline forever.
func TestIndexGitHubBoardStage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		remote          string
		cancel          bool
		wantTitle       string // title of acme/api #7 in the index; "" means no row
		wantFingerprint bool   // whether the board fingerprint is recorded afterwards
		watchesBoard    bool   // whether the project has a repo whose posts it watches
	}{
		{
			name:            "indexes the posts of the repo the GitHub remote names, ignoring case",
			remote:          "https://github.com/Acme/Api.git",
			wantTitle:       "acme change",
			wantFingerprint: true,
			watchesBoard:    true,
		},
		{
			name:            "scp-style remote resolves too",
			remote:          "git@github.com:acme/api.git",
			wantTitle:       "acme change",
			wantFingerprint: true,
			watchesBoard:    true,
		},
		{
			name:            "no GitHub remote indexes nothing and has no board to watch",
			remote:          "",
			wantFingerprint: true,
		},
		{
			name:            "non-GitHub remote is the same as none",
			remote:          "https://gitlab.com/acme/api.git",
			wantFingerprint: true,
		},
		{
			name:         "canceled stage leaves the fingerprint stale so the next check retries",
			remote:       "https://github.com/acme/api.git",
			cancel:       true,
			wantTitle:    "",
			watchesBoard: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			project := newBoardProject(t, tt.remote)
			// same number in two repos: only this repo's post may become the row
			project.writePost(t, "acme/api", 7, "acme change")
			project.writePost(t, "other/service", 7, "someone else's change")

			db, err := codedb.Open(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}

			mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
			mgr.indexGitHubBoard(ctx, db, nil)

			title, found := prTitle(t, db, 7)
			assert.Equal(t, tt.wantTitle != "", found)
			assert.Equal(t, tt.wantTitle, title)

			if tt.wantFingerprint {
				if tt.watchesBoard {
					assert.NotEmpty(t, mgr.recordedBoardFingerprint())
					assert.Equal(t, project.fingerprint("acme/api"), mgr.recordedBoardFingerprint())
				} else {
					assert.Empty(t, mgr.recordedBoardFingerprint(), "no repo, nothing to watch")
				}
				assert.False(t, mgr.boardChanged(), "an indexed board must not look changed, or every check re-indexes")
			} else {
				assert.Empty(t, mgr.recordedBoardFingerprint())
				assert.True(t, mgr.boardChanged(), "a stage that did not finish must be retried")
			}
		})
	}
}

func TestIndexGitHubBoardStage_NoTeamContext(t *testing.T) {
	t.Parallel()

	db, err := codedb.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.indexGitHubBoard(context.Background(), db, nil)
	assert.Empty(t, mgr.recordedBoardFingerprint())
}

func TestCodeDBManager_UpdateProjectRootForgetsGitHubRepo(t *testing.T) {
	t.Parallel()

	project := newBoardProject(t, "https://github.com/acme/api.git")
	mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
	require.Equal(t, "acme/api", mgr.githubRepoFullName())

	// the root only switches once the old one is gone (a deleted Conductor workspace)
	other := newBoardProject(t, "https://github.com/acme/web.git")
	require.NoError(t, os.RemoveAll(project.root))
	mgr.UpdateProjectRoot(other.root)
	assert.Equal(t, "acme/web", mgr.githubRepoFullName())
}

// TestCheckFreshness_ReindexesWhenOnlyTheBoardChanged is the gate: with git HEAD
// unchanged, CheckFreshness skips the pipeline unless the board moved.
// Failure prevented: a teammate's PR reaches the board and stays unsearchable
// until somebody commits.
func TestCheckFreshness_ReindexesWhenOnlyTheBoardChanged(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*CodeDBManager, boardProject, *atomic.Int64) {
		t.Helper()
		project := newBoardProject(t, "https://github.com/acme/api.git")
		project.writePost(t, "acme/api", 1, "first")

		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		var runs atomic.Int64
		mgr.testHook = func() { runs.Add(1) }

		// the state right after a successful index at the current HEAD and board
		head := readHeadFingerprint(context.Background(), project.root)
		require.NotEmpty(t, head)
		mgr.lastIndexedHead = head
		mgr.lastIndexedBoard = project.fingerprint("acme/api")
		require.NoError(t, os.MkdirAll(mgr.resolveSharedDataDir(), 0o755), "the pre-check only trusts the HEAD cache while the index directory exists")
		return mgr, project, &runs
	}

	t.Run("nothing moved: the pipeline is skipped", func(t *testing.T) {
		t.Parallel()
		mgr, _, runs := setup(t)
		mgr.CheckFreshness(context.Background())
		waitForIndexingDone(t, mgr)
		assert.Zero(t, runs.Load())
	})

	t.Run("a post arrived: the pipeline runs, the board outranks the Ledger, the new board is recorded", func(t *testing.T) {
		t.Parallel()
		mgr, project, runs := setup(t)
		project.writePost(t, "acme/api", 2, "second")
		want := project.fingerprint("acme/api")

		// the Ledger holds an older snapshot of PR 1, which the board also has: the
		// board stage must run after the Ledger stage for the post to win
		ledgerPath := t.TempDir()
		created := time.Date(2026, 9, 28, 17, 2, 11, 0, time.UTC)
		require.NoError(t, ledger.WriteGitHubPR(ledgerPath, &ledger.PRFile{
			Number: 1, Title: "ledger snapshot", State: "open", Author: "devon-dev", CreatedAt: created, UpdatedAt: created,
		}))
		mgr.SetLedgerPath(ledgerPath)

		mgr.CheckFreshness(context.Background())
		require.Eventually(t, func() bool { return runs.Load() == 1 }, 10*time.Second, 10*time.Millisecond, "doIndex never started")
		require.Eventually(t, func() bool {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			return !mgr.indexing
		}, 60*time.Second, 25*time.Millisecond, "indexing did not finish")

		assert.Equal(t, want, mgr.recordedBoardFingerprint())
		assert.False(t, mgr.boardChanged())

		db, err := codedb.OpenSQLOnly(mgr.resolveSharedDataDir())
		require.NoError(t, err)
		defer db.Close()
		title, found := prTitle(t, db, 1)
		require.True(t, found, "PR 1 should be indexed")
		assert.Equal(t, "first", title, "the Ledger snapshot must not outrank the board post")
		_, found = prTitle(t, db, 2)
		assert.True(t, found, "the post that moved the board should be searchable")

		// and the next check is quiet again
		mgr.CheckFreshness(context.Background())
		waitForIndexingDone(t, mgr)
		assert.Equal(t, int64(1), runs.Load())
	})
}

// openUnwritableDB returns a CodeDB whose store is already closed, so every
// post written to it fails the way a transient database error would.
func openUnwritableDB(t *testing.T) *codedb.DB {
	t.Helper()
	db, err := codedb.Open(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return db
}

func openBoardTestDB(t *testing.T) *codedb.DB {
	t.Helper()
	db, err := codedb.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// TestIndexGitHubBoardStage_RenamedRepo: the relay publishes under GitHub's
// current name, which the relay records in its state file, while the git remote
// may still say the old one.
//
// Failure prevented: after a repo rename or transfer, new posts carry the new
// name, the daemon only accepts the remote's spelling, and the repo's PRs and
// issues quietly stop appearing in ox code prs.
func TestIndexGitHubBoardStage_RenamedRepo(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) boardProject {
		t.Helper()
		project := newBoardProject(t, "https://github.com/acme/api.git")
		project.writePost(t, "acme/api-v2", 5, "after the rename")
		project.writePost(t, "acme/api", 6, "before the rename")
		project.writePost(t, "other/service", 7, "unrelated")
		return project
	}
	recordRename := func(t *testing.T) string {
		t.Helper()
		ledgerPath := t.TempDir()
		require.NoError(t, githubmirror.SaveState(ledgerPath, &githubmirror.State{
			Repo:     "acme/api",
			RepoMeta: &githubmirror.Repo{Owner: "acme", Name: "api-v2", FullName: "acme/api-v2"},
		}))
		return ledgerPath
	}

	t.Run("the relay's canonical name and the remote's name are both this repo", func(t *testing.T) {
		t.Parallel()
		project := setup(t)
		db := openBoardTestDB(t)
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		mgr.SetLedgerPath(recordRename(t))

		mgr.indexGitHubBoard(context.Background(), db, nil)

		title, found := prTitle(t, db, 5)
		assert.True(t, found, "a post under the new name must be indexed")
		assert.Equal(t, "after the rename", title)
		title, found = prTitle(t, db, 6)
		assert.True(t, found, "a post written before the rename is still this repo's")
		assert.Equal(t, "before the rename", title)
		_, found = prTitle(t, db, 7)
		assert.False(t, found, "another repo's post is not")

		assert.Equal(t, project.fingerprint("acme/api", "acme/api-v2"), mgr.recordedBoardFingerprint())
		require.False(t, mgr.boardChanged())
		project.writePost(t, "acme/api-v2", 8, "a later post under the new name")
		assert.True(t, mgr.boardChanged(), "a new post under the canonical name moves the board")
	})

	t.Run("without relay state only the remote's spelling is known", func(t *testing.T) {
		t.Parallel()
		project := setup(t)
		db := openBoardTestDB(t)
		mgr := NewCodeDBManager(project.root, codedbTestLogger(), nil)
		mgr.SetLedgerPath(t.TempDir())

		mgr.indexGitHubBoard(context.Background(), db, nil)

		_, found := prTitle(t, db, 5)
		assert.False(t, found)
		_, found = prTitle(t, db, 6)
		assert.True(t, found)
	})
}

// TestIndexGitHubBoardStage_RetriesFailedPostsBoundedTimes: a post that fails
// to index keeps the board "changed" so the next freshness check retries it,
// but only three times for the same board state.
//
// Failure prevented, both directions: a transient error losing a post's row for
// good because the stage recorded the board as indexed; and (incident #1144) a
// persistent error re-running the whole index pipeline on every freshness
// check.
func TestIndexGitHubBoardStage_RetriesFailedPostsBoundedTimes(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*CodeDBManager, boardProject) {
		t.Helper()
		project := newBoardProject(t, "https://github.com/acme/api.git")
		project.writePost(t, "acme/api", 1, "first")
		return NewCodeDBManager(project.root, codedbTestLogger(), nil), project
	}
	ctx := context.Background()

	t.Run("two failures stay stale, the third is recorded", func(t *testing.T) {
		t.Parallel()
		mgr, project := setup(t)
		broken := openUnwritableDB(t)

		for attempt := 1; attempt <= 2; attempt++ {
			mgr.indexGitHubBoard(ctx, broken, nil)
			assert.NotEqual(t, project.fingerprint("acme/api"), mgr.recordedBoardFingerprint(), "attempt %d", attempt)
			assert.True(t, mgr.boardChanged(), "attempt %d must be retried by the next freshness check", attempt)
		}

		mgr.indexGitHubBoard(ctx, broken, nil)
		assert.Equal(t, project.fingerprint("acme/api"), mgr.recordedBoardFingerprint())
		assert.False(t, mgr.boardChanged(), "after three attempts a persistent failure stops re-running the pipeline")

		// the pipeline also runs when HEAD moves; the board has not changed, so
		// the retries must not start over
		mgr.indexGitHubBoard(ctx, broken, nil)
		assert.False(t, mgr.boardChanged(), "a given-up board state is not retried on every later run")
	})

	t.Run("a retry that succeeds indexes the post and records the board", func(t *testing.T) {
		t.Parallel()
		mgr, project := setup(t)
		mgr.indexGitHubBoard(ctx, openUnwritableDB(t), nil)
		require.True(t, mgr.boardChanged())

		db := openBoardTestDB(t)
		mgr.indexGitHubBoard(ctx, db, nil)
		title, found := prTitle(t, db, 1)
		assert.True(t, found)
		assert.Equal(t, "first", title)
		assert.Equal(t, project.fingerprint("acme/api"), mgr.recordedBoardFingerprint())
		assert.False(t, mgr.boardChanged())
	})

	t.Run("a clean run resets the count", func(t *testing.T) {
		t.Parallel()
		mgr, _ := setup(t)
		broken := openUnwritableDB(t)

		mgr.indexGitHubBoard(ctx, broken, nil)
		mgr.indexGitHubBoard(ctx, broken, nil)
		mgr.indexGitHubBoard(ctx, openBoardTestDB(t), nil)
		require.False(t, mgr.boardChanged(), "the clean run recorded the board")

		mgr.indexGitHubBoard(ctx, broken, nil)
		assert.True(t, mgr.boardChanged(), "after a clean run a new failure gets its own retries")
	})

	t.Run("a changed board resets the count", func(t *testing.T) {
		t.Parallel()
		mgr, project := setup(t)
		broken := openUnwritableDB(t)

		for range 3 {
			mgr.indexGitHubBoard(ctx, broken, nil)
		}
		require.False(t, mgr.boardChanged(), "given up on this board state")

		project.writePost(t, "acme/api", 2, "second")
		require.True(t, mgr.boardChanged(), "a new post is a new board state")
		mgr.indexGitHubBoard(ctx, broken, nil)
		assert.True(t, mgr.boardChanged(), "the new board state gets its own retries")
	})

	t.Run("a canceled stage stays stale and does not use up a retry", func(t *testing.T) {
		t.Parallel()
		mgr, _ := setup(t)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		broken := openUnwritableDB(t)

		for range 5 {
			mgr.indexGitHubBoard(canceled, broken, nil)
		}
		assert.True(t, mgr.boardChanged())
		mgr.indexGitHubBoard(ctx, broken, nil)
		mgr.indexGitHubBoard(ctx, broken, nil)
		assert.True(t, mgr.boardChanged(), "two real failures are still within the three attempts")
	})
}
