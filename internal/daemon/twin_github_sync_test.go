//go:build slow

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/ledger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twinMockFetcher implements ledger.GitHubFetcher for digital twin tests.
// Note: SyncPRs calls BOTH ListPRComments (review comments) and
// ListIssueComments (conversation comments) for each PR, then merges them.
// Use prReviewComments for review comments and issueComments for conversation
// comments to avoid duplication.
type twinMockFetcher struct {
	prs              []ledger.FetchedPR
	issues           []ledger.FetchedIssue
	prReviewComments map[int][]ledger.FetchedComment // ListPRComments (review)
	issueComments    map[int][]ledger.FetchedComment // ListIssueComments (conversation)
	prCommits        map[int][]ledger.FetchedPRCommit
}

func (m *twinMockFetcher) ListPullRequests(_ context.Context, _, _ string, _ ledger.ListPRsOptions) ([]ledger.FetchedPR, *ledger.FetchRateLimit, error) {
	return m.prs, nil, nil
}

func (m *twinMockFetcher) ListIssues(_ context.Context, _, _ string, _ ledger.ListIssuesOptions) ([]ledger.FetchedIssue, *ledger.FetchRateLimit, error) {
	return m.issues, nil, nil
}

func (m *twinMockFetcher) ListPRComments(_ context.Context, _, _ string, number int) ([]ledger.FetchedComment, error) {
	if m.prReviewComments != nil {
		return m.prReviewComments[number], nil
	}
	return nil, nil
}

func (m *twinMockFetcher) ListIssueComments(_ context.Context, _, _ string, number int) ([]ledger.FetchedComment, error) {
	if m.issueComments != nil {
		return m.issueComments[number], nil
	}
	return nil, nil
}

func (m *twinMockFetcher) ListPRCommits(_ context.Context, _, _ string, number int) ([]ledger.FetchedPRCommit, error) {
	if m.prCommits != nil {
		return m.prCommits[number], nil
	}
	return nil, nil
}

// stripLFSConfig removes git-lfs filter config from a clone to avoid
// LFS smudge failures in test environments without git-lfs installed.
func stripLFSConfig(path string) {
	exec.Command("git", "-C", path, "config", "--unset-all", "filter.lfs.required").Run()
	exec.Command("git", "-C", path, "config", "--unset-all", "filter.lfs.clean").Run()
	exec.Command("git", "-C", path, "config", "--unset-all", "filter.lfs.smudge").Run()
	exec.Command("git", "-C", path, "config", "--unset-all", "filter.lfs.process").Run()
}

// setupLedgerTwin creates a Gitea repo with ledger directory structure and
// clones it twice to simulate two independent nodes (Node A and Node B).
// Returns both clone paths and the gitea fixture.
func setupLedgerTwin(t *testing.T) (nodeAPath, nodeBPath string, g *giteaFixture) {
	t.Helper()

	g = getSharedGitea(t)

	// sanitize test name for repo name (Gitea rejects special chars)
	safeName := strings.ReplaceAll(t.Name(), "/", "-")
	safeName = strings.ReplaceAll(safeName, " ", "-")
	cloneURL := g.createRepo(t, "twin-gh-"+safeName)

	// create a seed clone with ledger directory structure
	seedDir := filepath.Join(t.TempDir(), "seed")
	g.cloneRepo(t, cloneURL, seedDir)
	stripLFSConfig(seedDir)

	// create ledger directory structure
	dirs := []string{
		"data/github/2026/04/01/pr",
		".sageox",
		".sync",
	}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(seedDir, d), 0o755))
	}

	// write placeholder files so git tracks the directories
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, ".sageox", ".gitkeep"), []byte(""), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, ".sync", ".gitkeep"), []byte(""), 0o644))

	// commit and push seed structure
	runGit(t, seedDir, "add", ".")
	runGit(t, seedDir, "commit", "-m", "seed ledger structure")
	runGit(t, seedDir, "push")

	// clone twice: Node A and Node B
	nodeAPath = filepath.Join(t.TempDir(), "nodeA")
	nodeBPath = filepath.Join(t.TempDir(), "nodeB")

	g.cloneRepo(t, cloneURL, nodeAPath)
	stripLFSConfig(nodeAPath)

	g.cloneRepo(t, cloneURL, nodeBPath)
	stripLFSConfig(nodeBPath)

	return nodeAPath, nodeBPath, g
}

// simplePush is a PushFunc that does a plain git push (no retry, no rebase).
func simplePush(_ context.Context, ledgerPath string) error {
	cmd := exec.Command("git", "-C", ledgerPath, "push")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push failed: %s: %w", string(out), err)
	}
	return nil
}

// --- Scenario 1: Comment preservation on re-sync (ox-94r) ---
//
// Verifies that SyncPRs does NOT overwrite a known, unchanged PR — preserving
// previously-fetched comments. Before the fix, re-syncing a known PR with
// unchanged state would write a new file without comments (because the API
// list endpoint doesn't return comments, and we skip per-PR comment fetching
// for "known" PRs).
//
// Failure prevented: comments silently dropped on daemon restart or re-sync.

func TestTwinGitHubSync_CommentPreservation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	requireDocker(t)

	nodeAPath, _, _ := setupLedgerTwin(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// First sync: PR 409 is new, comments are fetched and written
	fetcher := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Fix unmarshal bug",
				Body:      "Fixes JSON parsing",
				State:     "open",
				Author:    "alice",
				Labels:    []string{"bug"},
				CreatedAt: now,
				UpdatedAt: now,
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		prReviewComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "carol", Body: "Needs one more test", Path: "main.go", CreatedAt: now},
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "bob", Body: "LGTM, great fix!", CreatedAt: now},
			},
		},
	}

	result1, err := ledger.SyncPRs(ctx, fetcher, nodeAPath, "org", "repo", 30, logger)
	require.NoError(t, err, "first sync should succeed")
	assert.Equal(t, 1, result1.PRTotal, "first sync should process 1 PR")
	assert.Equal(t, 1, result1.PRCreated, "first sync should create 1 PR")

	// verify file was created with comments
	// Note: fetchPRComments appends review comments first, then issue comments
	pr, err := ledger.ReadGitHubPR(nodeAPath, 409, now)
	require.NoError(t, err, "should read PR after first sync")
	assert.Len(t, pr.Comments, 2, "PR should have 2 comments after first sync")
	assert.Equal(t, "Needs one more test", pr.Comments[0].Body, "review comment comes first")
	assert.Equal(t, "LGTM, great fix!", pr.Comments[1].Body, "issue comment comes second")

	// Second sync: same PR, same state — should be skipped entirely
	result2, err := ledger.SyncPRs(ctx, fetcher, nodeAPath, "org", "repo", 30, logger)
	require.NoError(t, err, "second sync should succeed")
	assert.Equal(t, 0, result2.PRTotal, "second sync should skip known/unchanged PR")
	assert.Equal(t, 0, result2.PRCreated, "no new PRs on second sync")
	assert.Equal(t, 0, result2.PRUpdated, "no updated PRs on second sync")

	// verify comments are still present (NOT dropped)
	pr2, err := ledger.ReadGitHubPR(nodeAPath, 409, now)
	require.NoError(t, err, "should read PR after second sync")
	assert.Len(t, pr2.Comments, 2, "comments must be preserved after re-sync")
	assert.Equal(t, "Needs one more test", pr2.Comments[0].Body, "review comment preserved")
	assert.Equal(t, "LGTM, great fix!", pr2.Comments[1].Body, "issue comment preserved")
}

// --- Scenario 2: Content-hash filenames prevent conflicts (ox-0js) ---
//
// When two nodes sync the same PR at slightly different times, they produce
// different JSON content (different updated_at). With old deterministic names
// (409.json), this causes merge conflicts. With content-hash names
// (409-{hash}.json), both files coexist — no conflict.
//
// Failure prevented: git merge conflict blocks ledger push on multi-node teams.

func TestTwinGitHubSync_ContentHashPreventsConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	requireDocker(t)

	nodeAPath, nodeBPath, _ := setupLedgerTwin(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	laterA := now.Add(1 * time.Minute) // Node A sees updated_at slightly later
	laterB := now.Add(2 * time.Minute) // Node B sees updated_at even later

	// Node A: sync PR 409 with timestamp A
	fetcherA := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Fix unmarshal bug",
				State:     "open",
				Author:    "alice",
				CreatedAt: now,
				UpdatedAt: laterA,
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {{Author: "bob", Body: "LGTM from node A", CreatedAt: now}},
		},
	}

	resultA, err := ledger.SyncPRs(ctx, fetcherA, nodeAPath, "org", "repo", 30, logger)
	require.NoError(t, err, "Node A sync should succeed")
	assert.Equal(t, 1, resultA.PRTotal)

	// Node A: commit and push
	err = ledger.CommitAndPushGitHubData(ctx, nodeAPath, "org", "repo", resultA, simplePush)
	require.NoError(t, err, "Node A push should succeed")

	// Node B: sync PR 409 with timestamp B (different content = different hash)
	fetcherB := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Fix unmarshal bug",
				State:     "open",
				Author:    "alice",
				CreatedAt: now,
				UpdatedAt: laterB,
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {{Author: "carol", Body: "LGTM from node B", CreatedAt: now}},
		},
	}

	resultB, err := ledger.SyncPRs(ctx, fetcherB, nodeBPath, "org", "repo", 30, logger)
	require.NoError(t, err, "Node B sync should succeed")
	assert.Equal(t, 1, resultB.PRTotal)

	// Node B: commit
	runGit(t, nodeBPath, "add", "--sparse", ledger.GitHubDataDir(nodeBPath))
	runGit(t, nodeBPath, "commit", "--no-verify", "-m", "github: sync from node B")

	// Node B: push will fail (non-fast-forward), so pull --rebase first
	pushCmd := exec.Command("git", "-C", nodeBPath, "push")
	pushOut, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		// Expected: non-fast-forward. Rebase and retry.
		t.Logf("Node B initial push failed (expected): %s", string(pushOut))
		runGit(t, nodeBPath, "pull", "--rebase", "--autostash")
		runGit(t, nodeBPath, "push")
	}

	// Verify: NO merge conflict markers in any file
	prDir := ledger.DateDir(nodeBPath, now, "pr")
	entries, err := os.ReadDir(prDir)
	require.NoError(t, err, "should read pr dir after rebase")

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(prDir, e.Name()))
		require.NoError(t, readErr)
		assert.NotContains(t, string(data), "<<<<<<<", "conflict marker in %s", e.Name())
		assert.NotContains(t, string(data), ">>>>>>>", "conflict marker in %s", e.Name())
	}

	// Verify: both hash-variant files exist (409-{hashA}.json and 409-{hashB}.json)
	var prFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "409-") && strings.HasSuffix(e.Name(), ".json") {
			prFiles = append(prFiles, e.Name())
		}
	}
	assert.Len(t, prFiles, 2, "both hash-variant files should exist: %v", prFiles)

	// Verify: ReadGitHubPR returns the one with the latest updated_at
	pr, err := ledger.ReadGitHubPR(nodeBPath, 409, now)
	require.NoError(t, err, "ReadGitHubPR should succeed")
	assert.Equal(t, laterB, pr.UpdatedAt, "ReadGitHubPR should return the version with latest updated_at")
}

// --- Scenario 3: Backward compatibility with legacy files (ox-0js) ---
//
// Verifies that ReadGitHubPR can read legacy {number}.json files (no hash)
// and that new hash-named files are preferred when both exist. This ensures
// a smooth upgrade path for existing ledgers.
//
// Failure prevented: crash or missing data after upgrading ox on repos with
// pre-hash PR files.

func TestTwinGitHubSync_LegacyFileBackwardCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	requireDocker(t)

	nodeAPath, nodeBPath, _ := setupLedgerTwin(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	// On Node A: manually write a legacy 409.json file (no hash in name)
	prDir := ledger.DateDir(nodeAPath, now, "pr")
	require.NoError(t, os.MkdirAll(prDir, 0o755))

	legacyPR := ledger.PRFile{
		Number:    409,
		Title:     "Legacy PR",
		Body:      "Written before content-hash feature",
		Author:    "alice",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
		URL:       "https://github.com/org/repo/pull/409",
		Comments: []ledger.PRComment{
			{Author: "bob", Body: "Old comment", CreatedAt: now},
		},
	}
	legacyData, err := json.MarshalIndent(&legacyPR, "", "  ")
	require.NoError(t, err)
	legacyPath := filepath.Join(prDir, "409.json")
	require.NoError(t, os.WriteFile(legacyPath, legacyData, 0o644))

	// commit and push the legacy file
	runGit(t, nodeAPath, "add", "--sparse", ledger.GitHubDataDir(nodeAPath))
	runGit(t, nodeAPath, "commit", "--no-verify", "-m", "add legacy PR file")
	runGit(t, nodeAPath, "push")

	// Node B: pull to get the legacy file
	runGit(t, nodeBPath, "pull", "--rebase", "--autostash")

	// Node B: verify ReadGitHubPR reads the legacy file
	pr, err := ledger.ReadGitHubPR(nodeBPath, 409, now)
	require.NoError(t, err, "ReadGitHubPR should read legacy file")
	assert.Equal(t, "Legacy PR", pr.Title)
	assert.Len(t, pr.Comments, 1, "legacy file should have 1 comment")

	// Node B: run SyncPRs with updated data — creates new hash-named file
	fetcher := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Updated PR",
				Body:      "Now with content-hash filename",
				State:     "merged",
				Author:    "alice",
				CreatedAt: now,
				UpdatedAt: later,
				MergedAt:  &later,
				MergeSHA:  "abc123",
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "bob", Body: "Old comment", CreatedAt: now},
				{Author: "carol", Body: "Merge approved", CreatedAt: later},
			},
		},
	}

	// SyncPRs won't know about legacy 409 in KnownStates (fresh state),
	// so it will treat it as new and create a hash-named file.
	_, err = ledger.SyncPRs(ctx, fetcher, nodeBPath, "org", "repo", 30, logger)
	require.NoError(t, err, "SyncPRs should succeed")

	// Verify: ReadGitHubPR now returns the hash-named file (prefers it over legacy)
	pr2, err := ledger.ReadGitHubPR(nodeBPath, 409, now)
	require.NoError(t, err, "ReadGitHubPR should succeed after sync")
	assert.Equal(t, "Updated PR", pr2.Title, "hash-named file should be preferred over legacy")
	assert.Equal(t, "merged", pr2.State)
	assert.Len(t, pr2.Comments, 2, "updated file should have 2 comments")

	// Verify: both files exist on disk (legacy + hash-named)
	nodeBPRDir := ledger.DateDir(nodeBPath, now, "pr")
	entries, err := os.ReadDir(nodeBPRDir)
	require.NoError(t, err)

	var legacyFound, hashFound bool
	for _, e := range entries {
		if e.Name() == "409.json" {
			legacyFound = true
		}
		if strings.HasPrefix(e.Name(), "409-") && strings.HasSuffix(e.Name(), ".json") {
			hashFound = true
		}
	}
	assert.True(t, legacyFound, "legacy 409.json should still exist")
	assert.True(t, hashFound, "hash-named 409-{hash}.json should exist")
}

// --- Scenario 5: Full daemon pull cycle — reproduces the original bug ---
//
// Reproduces the EXACT scenario that caused the PR 409 merge conflict corruption.
// Node A syncs and pushes PR 409. Node B syncs PR 409 with slightly different
// content (different updated_at, different comments) producing a dirty file.
// A third clone pushes an unrelated commit, then Node B runs
// `git pull --rebase --autostash` (mimicking pullManagedRepo). With content-hash
// filenames, the stash-pop cannot conflict because the files have different names.
//
// Failure prevented: autostash stash-pop leaves conflict markers in JSON files,
// which get blindly committed and pushed, corrupting the ledger for all nodes.

func TestTwinGitHubSync_DaemonPullNeverCorruptsData(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	requireDocker(t)

	nodeAPath, nodeBPath, g := setupLedgerTwin(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	t1 := now.Add(1 * time.Minute) // Node A sees this updated_at
	t2 := now.Add(2 * time.Minute) // Node B sees this updated_at

	// --- Step 1: Node A syncs PR 409 with full data and pushes ---

	fetcherA := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Fix unmarshal bug",
				Body:      "Fixes JSON parsing in adapter layer",
				State:     "merged",
				Author:    "alice",
				Labels:    []string{"bug", "critical"},
				CreatedAt: now,
				UpdatedAt: t1,
				MergedAt:  &t1,
				MergeSHA:  "aaa111",
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		prReviewComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "carol", Body: "Looks good, one nit", Path: "adapter.go", CreatedAt: now},
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "bob", Body: "LGTM!", CreatedAt: now},
				{Author: "dave", Body: "Ship it", CreatedAt: t1},
			},
		},
		prCommits: map[int][]ledger.FetchedPRCommit{
			409: {
				{SHA: "aaa000", Msg: "fix: unmarshal bug", Author: "alice", Date: now},
				{SHA: "aaa111", Msg: "fix: address review feedback", Author: "alice", Date: t1},
			},
		},
	}

	resultA, err := ledger.SyncPRs(ctx, fetcherA, nodeAPath, "org", "repo", 30, logger)
	require.NoError(t, err, "Node A SyncPRs should succeed")
	assert.Equal(t, 1, resultA.PRCreated, "Node A should create 1 PR")

	err = ledger.CommitAndPushGitHubData(ctx, nodeAPath, "org", "repo", resultA, simplePush)
	require.NoError(t, err, "Node A CommitAndPushGitHubData should succeed")

	// --- Step 2: Node B syncs PR 409 with different content (before pulling) ---
	// This is the real scenario: Node B hasn't pulled yet, so it doesn't know
	// about Node A's file. It writes its own version with different content.

	fetcherB := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "Fix unmarshal bug",
				Body:      "Fixes JSON parsing in adapter layer",
				State:     "merged",
				Author:    "alice",
				Labels:    []string{"bug", "critical"},
				CreatedAt: now,
				UpdatedAt: t2, // different updated_at → different content hash
				MergedAt:  &t1,
				MergeSHA:  "aaa111",
				HTMLURL:   "https://github.com/org/repo/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "bob", Body: "LGTM!", CreatedAt: now},
			},
		},
		prCommits: map[int][]ledger.FetchedPRCommit{
			409: {
				{SHA: "aaa000", Msg: "fix: unmarshal bug", Author: "alice", Date: now},
				{SHA: "aaa111", Msg: "fix: address review feedback", Author: "alice", Date: t1},
			},
		},
	}

	resultB, err := ledger.SyncPRs(ctx, fetcherB, nodeBPath, "org", "repo", 30, logger)
	require.NoError(t, err, "Node B SyncPRs should succeed")
	assert.Equal(t, 1, resultB.PRCreated, "Node B should create 1 PR (different hash)")

	// Node B now has a dirty (uncommitted) PR file: 409-{hashB}.json

	// --- Step 3: Push an unrelated commit from a third clone ---

	// Get the clone URL from Node A's remote
	remoteCmd := exec.Command("git", "-C", nodeAPath, "remote", "get-url", "origin")
	remoteOut, err := remoteCmd.Output()
	require.NoError(t, err, "should get remote URL")
	cloneURL := strings.TrimSpace(string(remoteOut))

	thirdClonePath := filepath.Join(t.TempDir(), "thirdClone")
	g.cloneRepo(t, cloneURL, thirdClonePath)
	stripLFSConfig(thirdClonePath)

	// Write an unrelated issue file from the third clone
	issueDir := filepath.Join(thirdClonePath, "data", "github", "2026", "04", "01", "issue")
	require.NoError(t, os.MkdirAll(issueDir, 0o755))
	issueData := []byte(`{"number":100,"title":"Unrelated issue","state":"open","author":"eve"}`)
	require.NoError(t, os.WriteFile(filepath.Join(issueDir, "100-abcd1234.json"), issueData, 0o644))

	runGit(t, thirdClonePath, "pull", "--rebase", "--autostash")
	runGit(t, thirdClonePath, "add", ".")
	runGit(t, thirdClonePath, "commit", "--no-verify", "-m", "add unrelated issue")
	runGit(t, thirdClonePath, "push")

	// --- Step 4: Simulate daemon pull on Node B (with dirty file) ---
	// Node B has a dirty 409-{hashB}.json. The pull brings in Node A's
	// 409-{hashA}.json (different file!) plus the third clone's issue file.

	// Verify Node B has uncommitted changes before pull
	statusCmd := exec.Command("git", "-C", nodeBPath, "status", "--porcelain")
	statusOut, err := statusCmd.Output()
	require.NoError(t, err)
	assert.NotEmpty(t, string(statusOut), "Node B should have dirty files before pull")
	t.Logf("Node B status before pull:\n%s", string(statusOut))

	// This is what pullManagedRepo does — the exact operation that caused the bug
	runGit(t, nodeBPath, "pull", "--rebase", "--autostash", "--quiet")

	// --- Step 5: Assert no corruption ---

	// 5a: No merge conflict markers in ANY file under data/github/
	ghDir := ledger.GitHubDataDir(nodeBPath)
	err = filepath.Walk(ghDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relPath, _ := filepath.Rel(nodeBPath, path)
		assert.NotContains(t, string(data), "<<<<<<<",
			"conflict marker <<<<<<< found in %s", relPath)
		assert.NotContains(t, string(data), ">>>>>>>",
			"conflict marker >>>>>>> found in %s", relPath)
		assert.NotContains(t, string(data), "=======",
			"conflict marker ======= found in %s (check context — could be content)", relPath)
		return nil
	})
	require.NoError(t, err, "walking data/github/ should succeed")

	// 5b: Node B's dirty PR file is still modified (not lost by autostash)
	statusCmd2 := exec.Command("git", "-C", nodeBPath, "status", "--porcelain")
	statusOut2, err := statusCmd2.Output()
	require.NoError(t, err)
	assert.NotEmpty(t, string(statusOut2), "Node B should still have dirty files after pull")
	t.Logf("Node B status after pull:\n%s", string(statusOut2))

	// 5c: The dirty PR file is valid JSON
	prDir := ledger.DateDir(nodeBPath, now, "pr")
	entries, err := os.ReadDir(prDir)
	require.NoError(t, err, "should read pr dir")

	var nodeBPRFile string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "409-") && strings.HasSuffix(e.Name(), ".json") {
			// Check if this file is untracked or modified (Node B's version)
			checkCmd := exec.Command("git", "-C", nodeBPath, "status", "--porcelain",
				filepath.Join("data", "github", "2026", "04", "01", "pr", e.Name()))
			checkOut, _ := checkCmd.Output()
			if len(checkOut) > 0 {
				nodeBPRFile = e.Name()
			}
		}
	}
	require.NotEmpty(t, nodeBPRFile, "should find Node B's dirty PR file")

	dirtyData, err := os.ReadFile(filepath.Join(prDir, nodeBPRFile))
	require.NoError(t, err, "should read Node B's dirty PR file")
	assert.True(t, json.Valid(dirtyData), "Node B's dirty PR file must be valid JSON, got: %s", string(dirtyData[:min(len(dirtyData), 200)]))

	// 5d: Node B can still commit and push successfully
	runGit(t, nodeBPath, "add", "--sparse", ledger.GitHubDataDir(nodeBPath))
	runGit(t, nodeBPath, "commit", "--no-verify", "-m", "github: sync from node B")
	runGit(t, nodeBPath, "push")

	// Final verification: read PR from Node B — should return the latest version
	pr, err := ledger.ReadGitHubPR(nodeBPath, 409, now)
	require.NoError(t, err, "ReadGitHubPR should succeed after full cycle")
	assert.Equal(t, t2, pr.UpdatedAt, "ReadGitHubPR should return Node B's version (latest updated_at)")
	t.Logf("Final PR state: title=%q state=%q updated_at=%v comments=%d",
		pr.Title, pr.State, pr.UpdatedAt, len(pr.Comments))
}

// --- Scenario 6: Real ledger data validation (ox-0js) ---
//
// Uses REAL ledger data (not synthetic) to validate that GitHub sync fixes
// work against actual corrupted data. The real ledger has a 409.json with
// git merge conflict markers — exactly the bug we're fixing.
//
// This test clones the real ledger locally (never touching the real remote),
// pushes it to a local Gitea instance, and then exercises the sync pipeline
// against that real data.
//
// Failure prevented: sync fixes that work on synthetic data but fail on real
// corrupted ledger content (different JSON structure, unexpected fields, etc.).

func TestTwinGitHubSync_RealLedgerData(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin + real ledger")
	}

	const realLedgerPath = "/Users/galex/.local/share/sageox/sageox.ai/ledgers/repo_019c5812-01e9-7b7d-b5b1-321c471c9777"
	if _, err := os.Stat(realLedgerPath); os.IsNotExist(err) {
		t.Skipf("real ledger not found at %s (CI environment)", realLedgerPath)
	}

	// This test targets a specific historical corruption: PR 409 with git merge
	// conflict markers, stored at data/github/2026/04/01/pr/409.json. Once the
	// ledger is repaired (conflict cleared, files re-written with hash-suffixed
	// names), the precondition no longer holds and this scenario becomes
	// unreproducible from the live ledger. Skip rather than fail to keep the
	// suite green on healthy ledgers.
	const pr409LegacyPath = "data/github/2026/04/01/pr/409.json"
	if _, err := os.Stat(filepath.Join(realLedgerPath, pr409LegacyPath)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("real ledger no longer contains legacy corrupted fixture %s (already repaired)", pr409LegacyPath)
		}
		require.NoError(t, err, "failed to stat legacy fixture precondition")
	}

	requireDocker(t)

	g := getSharedGitea(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Create a Gitea repo to host the real data
	safeName := strings.ReplaceAll(t.Name(), "/", "-")
	safeName = strings.ReplaceAll(safeName, " ", "-")
	giteaCloneURL := g.createRepo(t, "twin-gh-"+safeName)

	// --- SAFETY: Clone the real ledger locally, redirect remote to Gitea ---

	// Instead of cloning the real ledger's git history (which has LFS objects
	// and 1500+ commits that are hard to push to Gitea), we copy the working
	// tree files into a fresh Gitea-backed repo. This gives us real file content
	// without needing the real repo's git object store.
	nodeAPath := filepath.Join(t.TempDir(), "node-a")
	g.cloneRepo(t, giteaCloneURL, nodeAPath)
	stripLFSConfig(nodeAPath)

	// Copy data/github/ from the real ledger into Node A
	srcGHDir := filepath.Join(realLedgerPath, "data", "github")
	dstDataDir := filepath.Join(nodeAPath, "data")
	require.NoError(t, os.MkdirAll(dstDataDir, 0o755))
	cpCmd := exec.Command("cp", "-R", srcGHDir, filepath.Join(dstDataDir, "github"))
	cpOut, err := cpCmd.CombinedOutput()
	require.NoError(t, err, "copy data/github/ failed: %s", string(cpOut))

	// Also create .sageox/ and .sync/ directories for ledger structure
	require.NoError(t, os.MkdirAll(filepath.Join(nodeAPath, ".sageox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(nodeAPath, ".sync"), 0o755))

	// Commit and push the real data to Gitea
	runGit(t, nodeAPath, "add", ".")
	runGit(t, nodeAPath, "commit", "--no-verify", "-m", "seed with real ledger data")
	runGit(t, nodeAPath, "push")

	// SAFETY VERIFICATION: confirm remote points to Gitea, not real remote
	getURLCmd := exec.Command("git", "-C", nodeAPath, "remote", "get-url", "origin")
	urlOut, err := getURLCmd.Output()
	require.NoError(t, err, "get-url failed")
	remoteURL := strings.TrimSpace(string(urlOut))
	require.Contains(t, remoteURL, "localhost:13719", "SAFETY: remote must point to Gitea, not real remote")
	require.NotContains(t, remoteURL, "sageox.ai", "SAFETY: must not point to real remote")
	t.Logf("SAFETY VERIFIED: remote = %s", remoteURL)

	// Create Node B by cloning from Gitea (completely independent of real ledger)
	nodeBPath := filepath.Join(t.TempDir(), "node-b")
	g.cloneRepo(t, giteaCloneURL, nodeBPath)
	stripLFSConfig(nodeBPath)

	// SAFETY: verify Node B also points to Gitea
	getURLCmdB := exec.Command("git", "-C", nodeBPath, "remote", "get-url", "origin")
	urlOutB, err := getURLCmdB.Output()
	require.NoError(t, err, "get-url Node B failed")
	require.Contains(t, strings.TrimSpace(string(urlOutB)), "localhost:13719",
		"SAFETY: Node B remote must point to Gitea")
	require.NotContains(t, strings.TrimSpace(string(urlOutB)), "sageox.ai",
		"SAFETY: Node B must not point to real remote")

	// --- Step 1: Verify real data exists and find corrupted files ---

	prBaseDir := filepath.Join(nodeAPath, "data", "github")
	require.DirExists(t, prBaseDir, "data/github/ must exist in real ledger")

	// Walk all PR files and catalog them
	var allPRFiles []string
	var corruptedFiles []string
	err = filepath.Walk(prBaseDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "pr" || !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}
		relPath, _ := filepath.Rel(nodeAPath, path)
		allPRFiles = append(allPRFiles, relPath)

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(data), "<<<<<<<") {
			corruptedFiles = append(corruptedFiles, relPath)
			t.Logf("found corrupted file (conflict markers): %s", relPath)
		}
		return nil
	})
	require.NoError(t, err, "walking data/github/ should succeed")
	require.NotEmpty(t, allPRFiles, "real ledger must have PR files")
	t.Logf("found %d PR files total, %d corrupted", len(allPRFiles), len(corruptedFiles))

	// Verify PR 409 specifically exists (it should be the corrupted one)
	prDir409 := filepath.Join(nodeAPath, "data", "github", "2026", "04", "01", "pr")
	pr409Path := filepath.Join(prDir409, "409.json")
	require.FileExists(t, pr409Path, "PR 409 file must exist in real data")

	pr409Data, err := os.ReadFile(pr409Path)
	require.NoError(t, err, "should read PR 409")
	require.Contains(t, string(pr409Data), "<<<<<<<",
		"PR 409 must have conflict markers (this is the real corruption)")
	t.Logf("PR 409 size: %d bytes, confirmed corrupted", len(pr409Data))

	// --- Step 2: Test that new sync creates clean hash-named files ---

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	syncTimeA := now.Add(5 * time.Minute)

	fetcherA := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "refactor(test): decompose 18 monolithic test files into ~70 behavioral-domain files",
				Body:      "Splits 18 large test files into ~70 smaller files",
				State:     "merged",
				Author:    "rsnodgrass",
				Labels:    []string{"tests", "refactor"},
				CreatedAt: time.Date(2026, 4, 1, 23, 21, 37, 0, time.UTC),
				UpdatedAt: syncTimeA,
				MergedAt:  func() *time.Time { t := time.Date(2026, 4, 2, 1, 6, 21, 0, time.UTC); return &t }(),
				MergeSHA:  "01d02408421af9eecdfc52ad439d430dbe79a332",
				HTMLURL:   "https://github.com/sageox/ox/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "coderabbitai[bot]", Body: "Summary by CodeRabbit", CreatedAt: now},
			},
		},
	}

	resultA, err := ledger.SyncPRs(ctx, fetcherA, nodeAPath, "sageox", "ox", 30, logger)
	require.NoError(t, err, "SyncPRs on Node A should succeed")
	assert.Equal(t, 1, resultA.PRCreated, "should create 1 new PR file")
	t.Logf("SyncPRs result: total=%d created=%d updated=%d", resultA.PRTotal, resultA.PRCreated, resultA.PRUpdated)

	// Verify the new hash-named file exists and is clean
	entries, err := os.ReadDir(prDir409)
	require.NoError(t, err, "should read PR dir")

	var hashFiles []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "409-") && strings.HasSuffix(e.Name(), ".json") {
			hashFiles = append(hashFiles, e.Name())
			// Verify each hash-named file is valid JSON with no conflict markers
			data, readErr := os.ReadFile(filepath.Join(prDir409, e.Name()))
			require.NoError(t, readErr, "should read hash file %s", e.Name())
			assert.NotContains(t, string(data), "<<<<<<<",
				"hash-named file %s must not have conflict markers", e.Name())
			assert.True(t, json.Valid(data),
				"hash-named file %s must be valid JSON", e.Name())
			t.Logf("hash-named file: %s (%d bytes, valid JSON)", e.Name(), len(data))
		}
	}
	require.NotEmpty(t, hashFiles, "SyncPRs must create at least one hash-named file")

	// ReadGitHubPR should return the new clean version (not the corrupted legacy one)
	// Note: createdAt must match the date directory (2026/04/01)
	pr409Read, err := ledger.ReadGitHubPR(nodeAPath, 409, time.Date(2026, 4, 1, 23, 21, 37, 0, time.UTC))
	require.NoError(t, err, "ReadGitHubPR should succeed (prefer hash-named over legacy)")
	assert.Equal(t, "rsnodgrass", pr409Read.Author, "should return the clean synced version")
	assert.Equal(t, "merged", pr409Read.State)
	t.Logf("ReadGitHubPR returned: title=%q author=%q state=%q",
		pr409Read.Title, pr409Read.Author, pr409Read.State)

	// --- Step 3: Commit and push Node A's changes ---

	err = ledger.CommitAndPushGitHubData(ctx, nodeAPath, "sageox", "ox", resultA, simplePush)
	require.NoError(t, err, "Node A CommitAndPushGitHubData should succeed")

	// --- Step 4: Multi-node test with real data ---
	// Node B syncs BEFORE pulling Node A's changes, so it creates its own
	// hash-named file for PR 409 (different content = different hash).
	// This is the real multi-node scenario.

	syncTimeB := now.Add(10 * time.Minute)
	fetcherB := &twinMockFetcher{
		prs: []ledger.FetchedPR{
			{
				Number:    409,
				Title:     "refactor(test): decompose 18 monolithic test files into ~70 behavioral-domain files",
				Body:      "Splits 18 large test files into ~70 smaller files",
				State:     "merged",
				Author:    "rsnodgrass",
				Labels:    []string{"tests", "refactor"},
				CreatedAt: time.Date(2026, 4, 1, 23, 21, 37, 0, time.UTC),
				UpdatedAt: syncTimeB, // different timestamp = different hash
				MergedAt:  func() *time.Time { t := time.Date(2026, 4, 2, 1, 6, 21, 0, time.UTC); return &t }(),
				MergeSHA:  "01d02408421af9eecdfc52ad439d430dbe79a332",
				HTMLURL:   "https://github.com/sageox/ox/pull/409",
			},
		},
		issueComments: map[int][]ledger.FetchedComment{
			409: {
				{Author: "coderabbitai[bot]", Body: "Summary by CodeRabbit", CreatedAt: now},
				{Author: "rsnodgrass", Body: "Addressed review feedback", CreatedAt: syncTimeB},
			},
		},
	}

	resultB, err := ledger.SyncPRs(ctx, fetcherB, nodeBPath, "sageox", "ox", 30, logger)
	require.NoError(t, err, "SyncPRs on Node B should succeed")
	t.Logf("Node B SyncPRs: total=%d created=%d updated=%d", resultB.PRTotal, resultB.PRCreated, resultB.PRUpdated)

	// Node B: commit its version
	if resultB.PRCreated > 0 || resultB.PRUpdated > 0 {
		runGit(t, nodeBPath, "add", "--sparse", ledger.GitHubDataDir(nodeBPath))
		runGit(t, nodeBPath, "commit", "--no-verify", "-m", "github: sync from node B")
	}

	// Now pull Node A's changes (rebase over them) and push
	pushCmdB := exec.Command("git", "-C", nodeBPath, "push")
	pushOutB, pushErrB := pushCmdB.CombinedOutput()
	if pushErrB != nil {
		t.Logf("Node B initial push failed (expected non-fast-forward): %s", string(pushOutB))
		runGit(t, nodeBPath, "pull", "--rebase", "--autostash")
		runGit(t, nodeBPath, "push")
	}

	// Pull on Node B to ensure it has all data
	runGit(t, nodeBPath, "pull", "--rebase", "--autostash")

	// Verify NO conflict markers in any file under data/github/
	ghDirB := ledger.GitHubDataDir(nodeBPath)
	err = filepath.Walk(ghDirB, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relPath, _ := filepath.Rel(nodeBPath, path)

		// Hash-named files must never have conflict markers
		if strings.Contains(info.Name(), "-") && info.Name() != ".gitkeep" {
			assert.NotContains(t, string(data), "<<<<<<<",
				"conflict marker in hash-named file %s", relPath)
		}
		return nil
	})
	require.NoError(t, err, "walking data/github/ on Node B should succeed")

	// --- Step 5: Verify all PR files are readable ---

	var readableCount, legacyCorruptCount int
	err = filepath.Walk(ghDirB, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "pr" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		var pr ledger.PRFile
		if unmarshalErr := json.Unmarshal(data, &pr); unmarshalErr != nil {
			if strings.Contains(string(data), "<<<<<<<") {
				legacyCorruptCount++
				relPath, _ := filepath.Rel(nodeBPath, path)
				t.Logf("legacy corrupted file (expected): %s — unmarshal error: %v", relPath, unmarshalErr)
			} else {
				relPath, _ := filepath.Rel(nodeBPath, path)
				t.Errorf("non-legacy file failed unmarshal: %s — %v", relPath, unmarshalErr)
			}
			return nil
		}
		readableCount++
		return nil
	})
	require.NoError(t, err, "walking PR files should succeed")
	t.Logf("PR files: %d readable, %d legacy-corrupted", readableCount, legacyCorruptCount)
	assert.Greater(t, readableCount, 0, "must have at least one readable PR file")
}
