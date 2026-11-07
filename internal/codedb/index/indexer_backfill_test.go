package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

// ── fixtures ───────────────────────────────────────────────────────────────

// bfGit runs git in dir and returns its trimmed output.
func bfGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.sageox.ai",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.sageox.ai",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// bfCommit writes files (path -> content; *.sh files are executable), deletes
// the removes paths, and commits. Returns the new commit hash.
func bfCommit(t *testing.T, dir, msg string, writes map[string]string, removes ...string) string {
	t.Helper()
	for path, content := range writes {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		mode := os.FileMode(0o644)
		if strings.HasSuffix(path, ".sh") {
			mode = 0o755
		}
		require.NoError(t, os.WriteFile(full, []byte(content), mode))
		bfGit(t, dir, "add", "--", path)
	}
	for _, path := range removes {
		bfGit(t, dir, "rm", "-q", "--", path)
	}
	bfGit(t, dir, "commit", "-q", "-m", msg)
	return bfGit(t, dir, "rev-parse", "HEAD")
}

// bfMergeHistoryCommits is the commit count of bfBuildMergeHistory.
const bfMergeHistoryCommits = 15

// bfBuildMergeHistory builds 15 commits with two merges. feat1 is a long branch
// forked from c2, so a BFS from the tip reaches c2 through main (m1, c4, c3, c2)
// before it reaches f1 (m1, f4, f3, f2, f1): the walk indexes f1 before its
// parent c2.
//
//	c1 - c2 - c3 - c4 -------------- m1 - c5 ------ m2 - c6 - c7   (main)
//	       \                        /       \       /
//	        f1 - f2 - f3 - f4 -----+         g1 - g2               (feat1; feat2 off c3)
//
// It covers add, modify, delete, rename (delete + insert), an executable file,
// a binary file, and a blob that returns to an earlier content (c4).
func bfBuildMergeHistory(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: builds a real git repository for CodeDB indexing")
	}
	dir := t.TempDir()
	bfGit(t, dir, "init", "-q", "-b", "main")

	aV1 := "package a\nfunc A() {}\n"
	bfCommit(t, dir, "c1", map[string]string{
		"a.go":           aV1,
		"lib/util.go":    "package lib\nfunc U() {}\n",
		"docs/readme.md": "# readme\n",
	})
	bfCommit(t, dir, "c2", map[string]string{"a.go": "package a\nfunc A() { _ = 2 }\n"})

	bfGit(t, dir, "checkout", "-q", "-b", "feat1")
	bfCommit(t, dir, "f1", map[string]string{
		"feat1.go":    "package feat\nfunc F1() {}\n",
		"lib/util.go": "package lib\nfunc U() { _ = 1 }\n",
	})
	bfCommit(t, dir, "f2", map[string]string{"feat1.go": "package feat\nfunc F1() { _ = 2 }\n"}, "docs/readme.md")
	bfCommit(t, dir, "f3", map[string]string{"feat3.go": "package feat\nfunc F3() {}\n"})
	bfCommit(t, dir, "f4", map[string]string{"feat3.go": "package feat\nfunc F3() { _ = 4 }\n"})

	bfGit(t, dir, "checkout", "-q", "main")
	main3 := "package main\nfunc M3() {}\n"
	c3 := bfCommit(t, dir, "c3", map[string]string{"main3.go": main3})
	bfCommit(t, dir, "c4", map[string]string{"a.go": aV1}) // blob returns to its c1 content
	bfGit(t, dir, "merge", "-q", "--no-ff", "-m", "m1", "feat1")
	bfCommit(t, dir, "c5", map[string]string{"dir/sub/x.go": "package sub\nfunc X() {}\n"})

	bfGit(t, dir, "checkout", "-q", "-b", "feat2", c3)
	bfCommit(t, dir, "g1", map[string]string{"g1.go": "package g\nfunc G1() {}\n"})
	bfCommit(t, dir, "g2", map[string]string{"main3b.go": main3}, "main3.go") // rename

	bfGit(t, dir, "checkout", "-q", "main")
	bfGit(t, dir, "merge", "-q", "--no-ff", "-m", "m2", "feat2")
	bfCommit(t, dir, "c6", map[string]string{"a.go": "package a\nfunc A() { _ = 6 }\n", "run.sh": "#!/bin/sh\necho hi\n"})
	bfCommit(t, dir, "c7", map[string]string{"blob.bin": "\xff\xfe\x00\x01"})

	require.Equal(t, strconv.Itoa(bfMergeHistoryCommits), bfGit(t, dir, "rev-list", "--count", "main"))
	return dir
}

// bfLinearHistory builds n linear commits on main with one git process and
// returns the repo and the commit hashes, newest first.
func bfLinearHistory(t *testing.T, n int) (string, []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("short: builds a real git repository for CodeDB indexing")
	}
	dir := t.TempDir()
	bfGit(t, dir, "init", "-q", "-b", "main")

	var stream strings.Builder
	for i := 1; i <= n; i++ {
		msg := fmt.Sprintf("commit %d", i)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter test <test@test.sageox.ai> %d +0000\ndata %d\n%s\n", 1700000000+i, len(msg), msg)
		file := fmt.Sprintf("f%d.go", i)
		content := fmt.Sprintf("package main\nfunc F%d() {}\n", i)
		fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", file, len(content), content)
		counter := fmt.Sprintf("package main\nvar Counter = %d\n", i)
		fmt.Fprintf(&stream, "M 100644 inline counter.go\ndata %d\n%s\n", len(counter), counter)
	}
	cmd := exec.Command("git", "fast-import", "--quiet")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.sageox.ai") // safe: git CLI in temp dir needs inherited PATH
	cmd.Stdin = strings.NewReader(stream.String())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git fast-import: %s", out)
	bfGit(t, dir, "reset", "-q", "--hard", "main")

	hashes := strings.Fields(bfGit(t, dir, "rev-list", "main"))
	require.Len(t, hashes, n)
	return dir, hashes
}

// bfGitEdges returns every "child>parent" edge git reports for main, sorted.
func bfGitEdges(t *testing.T, dir string) []string {
	t.Helper()
	var edges []string
	for _, line := range strings.Split(bfGit(t, dir, "rev-list", "--parents", "main"), "\n") {
		fields := strings.Fields(line)
		for _, parent := range fields[1:] {
			edges = append(edges, fields[0]+">"+parent)
		}
	}
	sort.Strings(edges)
	return edges
}

// bfIndex runs IndexLocalRepo and returns how many new commits it reported.
func bfIndex(t *testing.T, s *store.Store, dir string, opts IndexOptions) int {
	t.Helper()
	var reports []string
	opts.Progress = func(msg string) { reports = append(reports, msg) }
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, opts))
	for _, msg := range reports {
		var commits, blobs int
		if n, _ := fmt.Sscanf(msg, "Indexing complete: %d new commits, %d new blobs.", &commits, &blobs); n == 2 {
			return commits
		}
	}
	t.Fatalf("no completion report in %q", reports)
	return 0
}

// bfSnapshot dumps the indexed repo as id-free rows, so two stores built in a
// different order compare equal when they hold the same data.
func bfSnapshot(t *testing.T, s *store.Store) map[string][]string {
	t.Helper()
	queries := []struct{ name, query string }{
		{"commits", `SELECT hash || '|' || COALESCE(author, '') || '|' || COALESCE(message, '') || '|' || COALESCE(CAST(timestamp AS TEXT), '') FROM commits`},
		{"commit_parents", `SELECT c.hash || '>' || p.hash FROM commit_parents cp JOIN commits c ON c.id = cp.commit_id JOIN commits p ON p.id = cp.parent_id`},
		{"blobs", `SELECT content_hash || '|' || COALESCE(language, '') FROM blobs`},
		{"file_revs", `SELECT c.hash || '|' || fr.path || '|' || b.content_hash FROM file_revs fr JOIN commits c ON c.id = fr.commit_id JOIN blobs b ON b.id = fr.blob_id`},
		{"diffs", `SELECT c.hash || '|' || d.path || '|' || COALESCE(ob.content_hash, '-') || '|' || COALESCE(nb.content_hash, '-') FROM diffs d JOIN commits c ON c.id = d.commit_id LEFT JOIN blobs ob ON ob.id = d.old_blob_id LEFT JOIN blobs nb ON nb.id = d.new_blob_id`},
		{"refs", `SELECT r.name || '|' || c.hash FROM refs r JOIN commits c ON c.id = r.commit_id`},
	}
	out := make(map[string][]string, len(queries)+2)
	for _, q := range queries {
		rows, err := s.Query(q.query)
		require.NoError(t, err, q.name)
		var lines []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			lines = append(lines, line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		sort.Strings(lines)
		out[q.name] = lines
	}
	codeDocs, err := s.CodeIndex.DocCount()
	require.NoError(t, err)
	diffDocs, err := s.DiffIndex.DocCount()
	require.NoError(t, err)
	out["bleve_code_docs"] = []string{strconv.FormatUint(codeDocs, 10)}
	out["bleve_diff_docs"] = []string{strconv.FormatUint(diffDocs, 10)}
	return out
}

// bfBoundaryMerges counts merge commits that are indexed while at least one of
// their parents is not: the shape a depth-limited pass leaves behind.
func bfBoundaryMerges(t *testing.T, dir string, snap map[string][]string) int {
	t.Helper()
	indexed := make(map[string]bool)
	for _, row := range snap["commits"] {
		indexed[strings.SplitN(row, "|", 2)[0]] = true
	}
	n := 0
	for _, line := range strings.Split(bfGit(t, dir, "rev-list", "--parents", "main"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !indexed[fields[0]] {
			continue
		}
		if slices.ContainsFunc(fields[1:], func(parent string) bool { return !indexed[parent] }) {
			n++
		}
	}
	return n
}

// ── tests ──────────────────────────────────────────────────────────────────

// Failure prevented: the daemon's priority pass (newest N commits) followed by
// a backfill silently producing a different index than one full pass, for
// example a merge whose second parent was beyond the depth limit losing its
// commit_parents row.
func TestIndexLocalRepo_TwoPassEqualsOnePass(t *testing.T) {
	t.Parallel()
	dir := bfBuildMergeHistory(t)

	reference := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), reference, dir, IndexOptions{}))
	want := bfSnapshot(t, reference)
	require.Equal(t, bfGitEdges(t, dir), want["commit_parents"], "one pass must hold every parent edge git reports")

	boundaryMerges := 0
	for _, depth := range []int{2, 3, 5, 7, 9, 11, 13, 14} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			s := openTestStore(t)
			var truncated []string
			require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{
				MaxHistoryDepth:    depth,
				OnHistoryTruncated: func(ref string) { truncated = append(truncated, ref) },
			}))
			require.Equal(t, []string{"refs/heads/main"}, truncated)
			require.Equal(t, depth, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
			boundaryMerges += bfBoundaryMerges(t, dir, bfSnapshot(t, s))

			require.Equal(t, bfMergeHistoryCommits-depth, bfIndex(t, s, dir, IndexOptions{BackfillHistory: true}))

			got := bfSnapshot(t, s)
			for table := range want {
				require.Equal(t, want[table], got[table], table)
			}
		})
	}
	require.Positive(t, boundaryMerges, "fixture must leave at least one merge across the depth boundary")
}

// The tip moves between the priority pass and the backfill: the backfill must
// index the new commit, rebuild the tip snapshot, and still reach the old history.
func TestIndexLocalRepo_BackfillAfterTipMoves(t *testing.T) {
	t.Parallel()
	dir := bfBuildMergeHistory(t)

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{MaxHistoryDepth: 4}))
	bfCommit(t, dir, "c8", map[string]string{"late.go": "package late\n"})

	require.Equal(t, bfMergeHistoryCommits-4+1, bfIndex(t, s, dir, IndexOptions{BackfillHistory: true}), "new tip plus every commit behind the first pass")

	reference := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), reference, dir, IndexOptions{}))
	want := bfSnapshot(t, reference)
	got := bfSnapshot(t, s)
	for table := range want {
		require.Equal(t, want[table], got[table], table)
	}
}

// Failure prevented: a feature branch forked before main advanced loses the
// commit_parents row of its first commit, because the BFS walk indexes that
// commit before the parent it reaches over the longer path.
func TestIndexLocalRepo_ParentLinksMatchGitForUnequalMerges(t *testing.T) {
	t.Parallel()
	dir := bfBuildMergeHistory(t)

	// the fixture must make the walk index a commit before its parent, or this
	// test would pass without the repair it exists to cover
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	walked, _ := walkNewCommits(repo, plumbing.NewHash(bfGit(t, dir, "rev-parse", "main")), nil, 0)
	position := make(map[plumbing.Hash]int, len(walked))
	for i, cd := range walked {
		position[cd.oid] = i
	}
	childBeforeParent := 0
	for i, cd := range walked {
		for _, parent := range cd.parentIDs {
			if position[parent] > i {
				childBeforeParent++
			}
		}
	}
	require.Positive(t, childBeforeParent, "fixture has no commit the walk indexes before its parent")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	require.Equal(t, bfGitEdges(t, dir), bfSnapshot(t, s)["commit_parents"])
	require.Zero(t, countQuery(t, s, `SELECT COUNT(*) FROM commit_parents cp WHERE NOT EXISTS (SELECT 1 FROM commits c WHERE c.id = cp.parent_id)`))
}

// Failure prevented: a backfill that cannot see past the tip. The default-branch
// "unchanged" short-circuit and a walk that stops at indexed commits each make
// it index nothing, leaving everything behind the priority pass unsearchable.
func TestIndexLocalRepo_BackfillIndexesOlderCommitsWhenTipUnchanged(t *testing.T) {
	t.Parallel()
	dir, _ := bfLinearHistory(t, 30)
	s := openTestStore(t)

	require.Equal(t, 10, bfIndex(t, s, dir, IndexOptions{MaxHistoryDepth: 10}), "priority pass indexes the newest 10")
	require.Equal(t, 10, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
	afterPriority := bfSnapshot(t, s)

	steps := []struct {
		name       string
		opts       IndexOptions
		wantNew    int
		wantCommit int
	}{
		{"plain run with an unchanged tip skips", IndexOptions{}, 0, 10},
		{"backfill indexes the rest", IndexOptions{BackfillHistory: true}, 20, 30},
		{"second backfill finds nothing", IndexOptions{BackfillHistory: true}, 0, 30},
	}
	for _, step := range steps {
		require.Equal(t, step.wantNew, bfIndex(t, s, dir, step.opts), step.name)
		require.Equal(t, step.wantCommit, countQuery(t, s, `SELECT COUNT(*) FROM commits`), step.name)
	}

	// the tip snapshot was not rebuilt: the tip had not moved
	afterBackfill := bfSnapshot(t, s)
	require.Equal(t, afterPriority["refs"], afterBackfill["refs"])
	require.Equal(t, afterPriority["file_revs"], afterBackfill["file_revs"])
	require.Equal(t, 30, countQuery(t, s, `SELECT COUNT(*) FROM diffs WHERE new_blob_id IS NOT NULL AND path = 'counter.go'`))
}

// IndexRepo clones into the store, so it has its own copy of the short-circuit.
func TestIndexRepo_BackfillIndexesOlderCommitsWhenTipUnchanged(t *testing.T) {
	t.Parallel()
	source, _ := bfLinearHistory(t, 12)
	s := openTestStore(t)
	ctx := context.Background()

	require.NoError(t, IndexRepo(ctx, s, source, IndexOptions{MaxHistoryDepth: 5}))
	require.Equal(t, 5, countQuery(t, s, `SELECT COUNT(*) FROM commits`))

	require.NoError(t, IndexRepo(ctx, s, source, IndexOptions{}))
	require.Equal(t, 5, countQuery(t, s, `SELECT COUNT(*) FROM commits`), "plain run with an unchanged tip skips")

	require.NoError(t, IndexRepo(ctx, s, source, IndexOptions{BackfillHistory: true}))
	require.Equal(t, 12, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
	require.Equal(t, 11, countQuery(t, s, `SELECT COUNT(*) FROM commit_parents`))
}

// Failure prevented: a walk that stops at indexed commits is "naive backfill"
// and finds nothing; the backfill walk must collect exactly the unindexed
// commits and report every edge an indexed child has to an unindexed parent.
func TestWalkCommits_BackfillSteppingThroughIndexed(t *testing.T) {
	t.Parallel()
	dir := bfBuildMergeHistory(t)
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	tip := plumbing.NewHash(bfGit(t, dir, "rev-parse", "main"))

	allCommits, _ := walkNewCommits(repo, tip, nil, 0)
	require.Len(t, allCommits, bfMergeHistoryCommits)
	var edges [][2]string // child, parent
	for _, cd := range allCommits {
		for _, p := range cd.parentIDs {
			edges = append(edges, [2]string{cd.oid.String(), p.String()})
		}
	}

	for _, depth := range []int{1, 3, 6, 9, 14} {
		t.Run(fmt.Sprintf("indexed_%d", depth), func(t *testing.T) {
			newest, _ := walkNewCommits(repo, tip, nil, depth)
			known := make(map[string]bool, len(newest))
			for _, cd := range newest {
				known[cd.oid.String()] = true
			}

			plain := walkCommits(repo, tip, known, 0, false)
			require.Empty(t, plain.commits, "a plain walk stops at indexed commits")

			res := walkCommits(repo, tip, known, 0, true)
			require.False(t, res.truncated)
			require.Len(t, res.commits, bfMergeHistoryCommits-depth)
			collected := make(map[string]commitData, len(res.commits))
			for _, cd := range res.commits {
				require.False(t, known[cd.oid.String()], "indexed commit %s collected", cd.oid)
				collected[cd.oid.String()] = cd
			}

			wantBoundary := make(map[string][]string)
			for _, e := range edges {
				if known[e[0]] && !known[e[1]] {
					wantBoundary[e[1]] = append(wantBoundary[e[1]], e[0])
				}
			}
			for parent := range wantBoundary {
				slices.Sort(wantBoundary[parent])
			}
			gotBoundary := make(map[string][]string, len(res.boundary))
			for parent, children := range res.boundary {
				gotBoundary[parent] = slices.Sorted(slices.Values(children))
			}
			require.Equal(t, wantBoundary, gotBoundary)
			for parent := range gotBoundary {
				require.Contains(t, collected, parent, "a boundary parent must be one the walk collects")
			}
		})
	}
}

// The walk hands indexCommit the first parent's tree so it can skip a decode.
func TestWalkCommits_ParentTreeFromWalk(t *testing.T) {
	t.Parallel()
	dir := bfBuildMergeHistory(t)
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	tip := plumbing.NewHash(bfGit(t, dir, "rev-parse", "main"))

	for _, backfill := range []bool{false, true} {
		t.Run(fmt.Sprintf("backfill_%v", backfill), func(t *testing.T) {
			res := walkCommits(repo, tip, nil, 0, backfill)
			require.Len(t, res.commits, bfMergeHistoryCommits)
			for _, cd := range res.commits {
				if len(cd.parentIDs) == 0 {
					require.True(t, cd.parentTree.IsZero(), "root commit has no parent tree")
					continue
				}
				parent, err := repo.CommitObject(cd.parentIDs[0])
				require.NoError(t, err)
				require.Equal(t, parent.TreeHash, cd.parentTree, "commit %s", cd.oid)
			}
		})
	}
}

// Failure prevented: reporting truncation when the depth limit stopped the
// walk exactly at indexed history, which would schedule a backfill with
// nothing to do. Cases use an 8-commit history, newest first.
func TestWalkCommits_TruncatedOnlyWhenUnindexedCommitsRemain(t *testing.T) {
	t.Parallel()
	dir, hashes := bfLinearHistory(t, 8)
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	t.Cleanup(func() { repo.Close() })
	tip := plumbing.NewHash(hashes[0])

	hexSet := func(hs []string) map[string]bool {
		set := make(map[string]bool, len(hs))
		for _, h := range hs {
			set[h] = true
		}
		return set
	}

	tests := []struct {
		name          string
		known         map[string]bool
		depth         int
		backfill      bool
		wantCommits   int
		wantTruncated bool
	}{
		{"no limit", nil, 0, false, 8, false},
		{"limit equals history", nil, 8, false, 8, false},
		{"limit above history", nil, 9, false, 8, false},
		{"limit below history", nil, 7, false, 7, true},
		{"limit equals unindexed newest 3, oldest 5 indexed", hexSet(hashes[3:]), 3, false, 3, false},
		{"limit below unindexed newest 3", hexSet(hashes[3:]), 2, false, 2, true},
		{"backfill limit below the 5 unindexed oldest", hexSet(hashes[:3]), 3, true, 3, true},
		{"backfill limit equals the 5 unindexed oldest", hexSet(hashes[:3]), 5, true, 5, false},
		{"backfill limit above the unindexed oldest", hexSet(hashes[:3]), 6, true, 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := walkCommits(repo, tip, tt.known, tt.depth, tt.backfill)
			require.Len(t, res.commits, tt.wantCommits)
			require.Equal(t, tt.wantTruncated, res.truncated)
		})
	}
}

func TestIndexLocalRepo_OnHistoryTruncated(t *testing.T) {
	t.Parallel()
	dir, _ := bfLinearHistory(t, 12)

	tests := []struct {
		name  string
		depth int
		want  []string
	}{
		{"unlimited", 0, nil},
		{"limit below history", 5, []string{"refs/heads/main"}},
		{"limit equals history", 12, nil},
		{"limit above history", 20, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			var got []string
			require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{
				MaxHistoryDepth:    tt.depth,
				OnHistoryTruncated: func(ref string) { got = append(got, ref) },
			}))
			require.Equal(t, tt.want, got)
		})
	}

	t.Run("backfill with a limit reports remaining history", func(t *testing.T) {
		s := openTestStore(t)
		require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{MaxHistoryDepth: 4}))
		var got []string
		require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{
			BackfillHistory:    true,
			MaxHistoryDepth:    4,
			OnHistoryTruncated: func(ref string) { got = append(got, ref) },
		}))
		require.Equal(t, []string{"refs/heads/main"}, got, "4 of the remaining 8 commits indexed, 4 left")
		require.Equal(t, 8, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
	})
}

// Failure prevented: a backfill that never yields the machine (Pace skipped or
// called before the checkpoint it is meant to follow).
func TestIndexLocalRepo_PaceRunsAfterEachCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		commits int
		want    int
	}{
		{"below the first checkpoint", 50, 0},
		{"one checkpoint", 51, 1},
		{"two checkpoints", 101, 2},
		{"three checkpoints", 151, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, _ := bfLinearHistory(t, tt.commits)
			s := openTestStore(t)

			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "run")
			var visible []int
			err := IndexLocalRepo(ctx, s, dir, IndexOptions{Pace: func(ctx context.Context) error {
				require.Equal(t, "run", ctx.Value(ctxKey{}), "Pace gets the run's context")
				visible = append(visible, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
				return nil
			}})
			require.NoError(t, err)

			// the checkpoint at commit index i commits commits 0..i, so a reader
			// sees them by the time Pace runs
			var wantVisible []int
			for k := 1; k <= tt.want; k++ {
				wantVisible = append(wantVisible, k*checkpointEvery+1)
			}
			require.Equal(t, wantVisible, visible)
			require.Equal(t, tt.commits, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
		})
	}
}

// Failure prevented: a pacer error (typically context cancellation during a
// backoff sleep) being swallowed, so a canceled background index keeps running.
func TestIndexLocalRepo_PaceErrorAbortsIndexing(t *testing.T) {
	t.Parallel()
	dir, _ := bfLinearHistory(t, 151)
	s := openTestStore(t)

	errStop := errors.New("pacer says stop")
	calls := 0
	err := IndexLocalRepo(context.Background(), s, dir, IndexOptions{Pace: func(context.Context) error {
		calls++
		if calls == 2 {
			return errStop
		}
		return nil
	}})

	require.ErrorIs(t, err, errStop)
	require.Equal(t, 2, calls, "no commit is indexed after the failing Pace call")
	// both checkpoints were committed before their Pace call; the tip build and
	// ref row never ran
	require.Equal(t, 101, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
	require.Zero(t, countQuery(t, s, `SELECT COUNT(*) FROM refs`))
}
