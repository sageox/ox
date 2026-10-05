package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/require"
)

// commitNewFile adds a file to the repo at dir and commits it, moving HEAD.
func commitNewFile(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("package main\n// "+name+"\n"), 0o644))
	for _, args := range [][]string{{"add", name}, {"commit", "-m", "add " + name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

func countQuery(t *testing.T, s *store.Store, query string) int {
	t.Helper()
	var n int
	require.NoError(t, s.QueryRow(query).Scan(&n), query)
	return n
}

// file_revs holds one tree snapshot per ref tip. Every build that moved the tip
// used to leave the previous snapshot behind, so a ledger index grew by a full
// tree (~123k rows) per build and reached 34M rows across 394 snapshots.
//
// Failure prevented: the ledger index growing without bound, and with it the
// cost of every scan and vacuum of it.
func TestIndexLocalRepo_MovingTipKeepsOneSnapshotPerRef(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)
	s := openTestStore(t)

	const builds = 6
	for build := 1; build <= builds; build++ {
		if build > 1 {
			commitNewFile(t, dir, fmt.Sprintf("later%d.go", build))
		}
		require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}), "build %d", build)

		refs := countQuery(t, s, `SELECT COUNT(*) FROM refs`)
		snapshots := countQuery(t, s, `SELECT COUNT(DISTINCT commit_id) FROM file_revs`)
		require.Equal(t, 1, refs, "build %d: refs", build)
		require.Equal(t, refs, snapshots, "build %d: one snapshot per ref", build)

		// the surviving snapshot is the ref's current tip, and it is complete:
		// the initial commit's file plus one file per later build
		require.Zero(t, countQuery(t, s, `SELECT COUNT(*) FROM file_revs WHERE commit_id NOT IN (SELECT commit_id FROM refs)`),
			"build %d: rows left behind for a commit no ref points at", build)
		require.Equal(t, build, countQuery(t, s, `SELECT COUNT(*) FROM file_revs`), "build %d: tip snapshot rows", build)
	}
}

// A legacy index arrives carrying hundreds of dead snapshots. The build prunes
// at most maxSnapshotsPrunedPerBuild of them inside its single transaction (the
// rest is Store.Maintain's job, in separate transactions), but it must still
// make progress and must never touch the live snapshot.
func TestIndexLocalRepo_BacklogOfDeadSnapshotsIsPrunedInBoundedSteps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)
	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// seed a backlog: dead snapshots owned by commits of this repo that no ref points at
	const backlog = maxSnapshotsPrunedPerBuild + 5
	repoID := countQuery(t, s, `SELECT id FROM repos LIMIT 1`)
	for i := range backlog {
		res, err := s.Exec(`INSERT INTO commits (repo_id, hash, timestamp) VALUES (?, ?, 0)`, repoID, fmt.Sprintf("dead-%d", i))
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = s.Exec(`INSERT INTO file_revs (commit_id, path, blob_id) VALUES (?, 'old.go', (SELECT id FROM blobs LIMIT 1))`, id)
		require.NoError(t, err)
	}
	liveRows := countQuery(t, s, `SELECT COUNT(*) FROM file_revs WHERE commit_id IN (SELECT commit_id FROM refs)`)
	require.NotZero(t, liveRows)

	commitNewFile(t, dir, "next.go") // moves the tip, so the build reaches buildTipFileRevs
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// the backlog plus the snapshot the moved ref just left, minus one bounded batch
	dead := countQuery(t, s, `SELECT COUNT(DISTINCT commit_id) FROM file_revs WHERE commit_id NOT IN (SELECT commit_id FROM refs)`)
	require.Equal(t, backlog+1-maxSnapshotsPrunedPerBuild, dead, "one build prunes a bounded batch of the backlog")
	require.Equal(t, liveRows+1, countQuery(t, s, `SELECT COUNT(*) FROM file_revs WHERE commit_id IN (SELECT commit_id FROM refs)`),
		"the live snapshot must be rebuilt for the new tip and left intact")
}
