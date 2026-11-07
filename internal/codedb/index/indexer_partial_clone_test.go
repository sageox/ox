package index

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

// Three commits, each rewriting a.txt with a unique token and adding one file.
// Tip content: a.txt "tokenthree", b1.txt, b2.txt, b3.txt. History-only blobs:
// the "tokenone" and "tokentwo" versions of a.txt.
const pcHistoryBlobs = 2 // a.txt v1 and v2

func pcBuildSource(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: builds a real git repository for CodeDB indexing")
	}
	dir := t.TempDir()
	bfGit(t, dir, "init", "-q", "-b", "main")
	// file:// clones only honor --filter when the serving side allows it
	bfGit(t, dir, "config", "uploadpack.allowFilter", "true")
	bfCommit(t, dir, "c1", map[string]string{"a.txt": "alpha tokenone\n", "b1.txt": "bravo one\n"})
	bfCommit(t, dir, "c2", map[string]string{"a.txt": "alpha tokentwo\n", "b2.txt": "bravo two\n"})
	bfCommit(t, dir, "c3", map[string]string{"a.txt": "alpha tokenthree\n", "b3.txt": "bravo three\n"})
	return dir
}

// pcBuildPartialClone clones the source with --filter=blob:none. The checkout
// fetches the tip's blobs; every history-only blob stays on the server.
func pcBuildPartialClone(t *testing.T) string {
	t.Helper()
	source := pcBuildSource(t)
	dst := filepath.Join(t.TempDir(), "partial")
	bfGit(t, filepath.Dir(dst), "clone", "-q", "--filter=blob:none", "file://"+source, dst)

	missing := 0
	for _, line := range strings.Split(bfGit(t, dst, "rev-list", "--objects", "--all", "--missing=print"), "\n") {
		if strings.HasPrefix(line, "?") {
			missing++
		}
	}
	if missing == 0 {
		t.Skip("this git did not produce a partial clone")
	}
	require.Equal(t, pcHistoryBlobs, missing, "only the history-only blobs may be missing")
	return dst
}

func pcSearchTotal(t *testing.T, s *store.Store, term string) uint64 {
	t.Helper()
	result, err := s.CodeIndex.Search(bleve.NewSearchRequest(bleve.NewMatchQuery(term)))
	require.NoError(t, err)
	return result.Total
}

func pcSearchDiffTotal(t *testing.T, s *store.Store, term string) uint64 {
	t.Helper()
	result, err := s.DiffIndex.Search(bleve.NewSearchRequest(bleve.NewMatchQuery(term)))
	require.NoError(t, err)
	return result.Total
}

func pcCodeDocs(t *testing.T, s *store.Store) uint64 {
	t.Helper()
	n, err := s.CodeIndex.DocCount()
	require.NoError(t, err)
	return n
}

// Failure prevented: indexing a partial clone either failing on the history
// blobs it does not have, or (after skipping history reads) never indexing the
// content of tip blobs that first appeared in history, which would leave the
// working tree unsearchable.
func TestIndexLocalRepo_PartialClone(t *testing.T) {
	t.Parallel()
	dir := pcBuildPartialClone(t)

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	partial := isPartialClone(repo)
	require.NoError(t, repo.Close())
	require.True(t, partial, "a --filter=blob:none clone must be detected as partial")

	s := openTestStore(t)
	require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

	// every commit, diff and blob row exists, including those of the missing blobs
	require.Equal(t, 3, countQuery(t, s, `SELECT COUNT(*) FROM commits`))
	require.Equal(t, 6, countQuery(t, s, `SELECT COUNT(*) FROM diffs WHERE new_blob_id IS NOT NULL`))
	require.Equal(t, 6, countQuery(t, s, `SELECT COUNT(*) FROM blobs`))
	require.Equal(t, 4, countQuery(t, s, `SELECT COUNT(*) FROM file_revs`))

	// content: the tip's blobs only
	require.Equal(t, uint64(4), pcCodeDocs(t, s))
	require.Equal(t, uint64(1), pcSearchTotal(t, s, "tokenthree"))
	require.Zero(t, pcSearchTotal(t, s, "tokenone"))
	require.Zero(t, pcSearchTotal(t, s, "tokentwo"))
}

// A complete repo that merely names a promisor remote takes the same path,
// which makes the skip observable: its history blobs are readable, yet they
// are not read.
func TestIndexLocalRepo_PromisorRemoteSkipsHistoryBlobContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		config          [][]string
		wantDocs        uint64
		wantHistoryCode uint64 // code search hits for the history-only "tokenone"
		wantHistoryDiff uint64 // diff search hits for it: the c1 insert and the c2 modification
	}{
		{"no promisor remote reads every blob", nil, 6, 1, 2},
		{"promisor remote skips history blobs", [][]string{{"remote.origin.promisor", "true"}}, 4, 0, 0},
		{"promisor false reads every blob", [][]string{{"remote.origin.promisor", "false"}}, 6, 1, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := pcBuildSource(t)
			for _, kv := range tt.config {
				bfGit(t, dir, "config", kv[0], kv[1])
			}

			s := openTestStore(t)
			require.NoError(t, IndexLocalRepo(context.Background(), s, dir, IndexOptions{}))

			require.Equal(t, tt.wantDocs, pcCodeDocs(t, s))
			require.Equal(t, tt.wantHistoryCode, pcSearchTotal(t, s, "tokenone"))
			require.Equal(t, tt.wantHistoryDiff, pcSearchDiffTotal(t, s, "tokenone"), "diff text must not re-read the blobs ensureBlob skipped")
			require.Equal(t, uint64(1), pcSearchTotal(t, s, "tokenthree"), "tip content is always indexed")
			require.Equal(t, 6, countQuery(t, s, `SELECT COUNT(*) FROM blobs`), "every blob has a row either way")
		})
	}
}

func TestPartialCloneConfigured(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  string
		want bool
	}{
		{"empty", "", false},
		{"ordinary clone", "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = https://example.test/r.git\n", false},
		{"extensions.partialclone", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tpartialclone = origin\n", true},
		{"extensions.partialClone mixed case", "[extensions]\n\tpartialClone = origin\n", true},
		{"unrelated extension", "[extensions]\n\tworktreeconfig = true\n", false},
		{"promisor true", "[remote \"origin\"]\n\tpromisor = true\n", true},
		{"promisor yes", "[remote \"origin\"]\n\tpromisor = yes\n", true},
		{"promisor bare key", "[remote \"origin\"]\n\tpromisor\n", true},
		{"promisor false", "[remote \"origin\"]\n\tpromisor = false\n", false},
		{"promisor on the second remote", "[remote \"origin\"]\n\turl = u\n[remote \"upstream\"]\n\tpromisor = true\n", true},
		{"promisor outside a remote", "[core]\n\tpromisor = true\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.NewConfig()
			require.NoError(t, cfg.Unmarshal([]byte(tt.cfg)))
			require.Equal(t, tt.want, partialCloneConfigured(cfg))
		})
	}

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()
		require.False(t, partialCloneConfigured(nil))
		require.False(t, partialCloneConfigured(&config.Config{}))
	})
}

// ensureBlob in isolation. The blob is readable in every case, so a skipped
// read shows as indexed=false.
func TestEnsureBlob_PartialClone(t *testing.T) {
	t.Parallel()
	const content = "package p\nvar X = 1\n"

	tests := []struct {
		name         string
		partial      bool
		readingTip   []bool // one ensureBlob call per entry, readingTip set to it
		wantIndexed  []bool
		wantCodeDocs int
	}{
		{"complete repo reads history", false, []bool{false}, []bool{true}, 1},
		{"partial clone skips history", true, []bool{false}, []bool{false}, 0},
		{"partial clone reads tip", true, []bool{true}, []bool{true}, 1},
		{"tip read repairs a blob first seen in history", true, []bool{false, true}, []bool{false, true}, 1},
		{"tip read repairs only once", true, []bool{false, true, true}, []bool{false, true, false}, 1},
		{"complete repo does not re-read at tip", false, []bool{false, true}, []bool{true, false}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			repo, err := git.PlainInit(dir, false)
			require.NoError(t, err)
			s := openTestStore(t)
			repoID, err := upsertRepo(context.Background(), s, "pc-"+tt.name, dir)
			require.NoError(t, err)
			tx, err := s.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer tx.Rollback()

			st := &indexState{
				tx:             tx,
				store:          s,
				repo:           repo,
				repoID:         repoID,
				codeBatch:      s.CodeIndex.NewBatch(),
				diffBatch:      s.DiffIndex.NewBatch(),
				knownCommits:   make(map[string]bool),
				treeCache:      make(map[plumbing.Hash]map[string]plumbing.Hash),
				treeCacheLimit: 128,
				blobIDCache:    make(map[string]int64),
				commitIDCache:  make(map[string]int64),
				report:         func(string) {},
				partialClone:   tt.partial,
			}
			oid := writeBlobToRepo(t, repo, content)

			var firstID int64
			for i, tip := range tt.readingTip {
				st.readingTip = tip
				id, text, indexed, err := st.ensureBlob(oid, "p.go")
				require.NoError(t, err)
				require.Equal(t, tt.wantIndexed[i], indexed, "call %d", i)
				if indexed {
					require.Equal(t, content, text, "call %d", i)
				} else {
					require.Empty(t, text, "call %d", i)
				}
				if i == 0 {
					firstID = id
				}
				require.Equal(t, firstID, id, "one row per blob")
			}
			require.Equal(t, tt.wantCodeDocs, st.codeBatchN)
		})
	}
}
