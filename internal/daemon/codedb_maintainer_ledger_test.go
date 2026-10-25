package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const maintSidecarName = store.MetadataDBFile + ".maint.json"

// seedIndexWithDeadSnapshots creates a codedb at dir whose file_revs holds
// `snapshots` full tree snapshots of 150 files each, only the last of which a
// ref points at — the shape a ledger index reaches after one build per HEAD
// move. The file is checkpointed so its size on disk is the real one.
func seedIndexWithDeadSnapshots(t *testing.T, dir string, snapshots int) {
	t.Helper()
	const filesPerSnapshot = 150
	db, err := codedb.OpenSQLOnly(dir)
	require.NoError(t, err)
	s := db.Store()
	rows := snapshots * filesPerSnapshot

	stmts := []string{
		`INSERT INTO repos (id, name, path) VALUES (1, 'ledger', '/tmp/ledger')`,
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
			INSERT INTO commits (id, repo_id, hash, timestamp) SELECT i, 1, 'commit-' || i, 0 FROM n`, snapshots),
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
			INSERT INTO blobs (id, content_hash, language) SELECT i, 'blob-hash-' || i, 'markdown' FROM n`, rows),
		fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
			INSERT INTO file_revs (commit_id, path, blob_id)
			SELECT (i-1)/%d + 1, 'sessions/some-long-session-directory-name/file-' || ((i-1) %% %d) || '.md', i FROM n`,
			rows, filesPerSnapshot, filesPerSnapshot),
		fmt.Sprintf(`INSERT INTO refs (repo_id, name, commit_id) VALUES (1, 'refs/heads/main', %d)`, snapshots),
	}
	for _, stmt := range stmts {
		_, err := s.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	_, err = s.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

// snapshotCount returns the number of distinct file_revs snapshots in the codedb at dir.
func snapshotCount(t *testing.T, dir string) int {
	t.Helper()
	db, err := codedb.OpenSQLOnly(dir)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.Store().QueryRow(`SELECT COUNT(DISTINCT commit_id) FROM file_revs`).Scan(&n))
	return n
}

// damageTable overwrites the b-tree header of table's root page in the database
// under dir: Open still succeeds (the file header and schema are intact) but the
// first statement that reads the table fails with SQLITE_CORRUPT, and a page scan
// reports the damage. This is how damage presents now that Open does not scan.
func damageTable(t *testing.T, dir, table string) {
	t.Helper()
	dbPath := filepath.Join(dir, store.MetadataDBFile)
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	var rootPage, pageSize int
	require.NoError(t, raw.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = ?`, table).Scan(&rootPage))
	require.NoError(t, raw.QueryRow(`PRAGMA page_size`).Scan(&pageSize))
	require.NoError(t, raw.Close())

	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	_, err = f.WriteAt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, int64(rootPage-1)*int64(pageSize))
	require.NoError(t, err)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// maintTestManager builds a manager whose shared index lives at <root>/codedb and
// whose ledger index lives where production puts it, nested at <root>/codedb/ledger.
func maintTestManager(t *testing.T) (mgr *CodeDBManager, shared, ledger string) {
	t.Helper()
	root := t.TempDir()
	shared = filepath.Join(root, "codedb")
	ledger = filepath.Join(shared, "ledger")
	mgr = NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.dataDir = shared
	mgr.ledgerDataDir = ledger
	return mgr, shared, ledger
}

// The ledger index is the one that reached 5 GB, because maintenance only ever
// looked at the shared index. This is the end-to-end promise: after a
// maintenance pass the ledger index holds one snapshot per ref, the freed pages
// are handed back to the filesystem, and both indexes carry a sidecar so the next
// pass skips the daily work.
//
// Failure prevented: the ledger index growing by ~20 MB per build, forever.
func TestCodeDBMaintainer_PrunesAndVacuumsTheLedgerIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite maintenance of two indexes")
	}
	mgr, shared, ledger := maintTestManager(t)
	seedIndexWithDeadSnapshots(t, shared, 1)
	seedIndexWithDeadSnapshots(t, ledger, 40)
	require.Equal(t, 40, snapshotCount(t, ledger), "fixture")

	res := NewCodeDBMaintainer("codedb", mgr).Maintain(context.Background())

	require.NoError(t, res.Error)
	assert.True(t, res.Healthy)
	assert.Equal(t, 1, snapshotCount(t, ledger), "ledger index must keep exactly the snapshot its ref points at")
	assert.Equal(t, 1, snapshotCount(t, shared), "the shared index's live snapshot is untouched")
	assert.GreaterOrEqual(t, res.Pruned, int64(39*150), "every dead snapshot's rows are counted")
	assert.True(t, res.Vacuumed, "pruning 39 of 40 snapshots leaves far more than a quarter of the file free")
	assert.Less(t, res.SizeAfter, res.SizeBefore, "the vacuum must hand the space back")
	assert.True(t, fileExists(filepath.Join(ledger, maintSidecarName)), "ledger index records its maintenance state")
	assert.True(t, fileExists(filepath.Join(shared, maintSidecarName)), "shared index records its maintenance state")

	// the second pass inside the day does neither expensive job again
	again := NewCodeDBMaintainer("codedb", mgr).Maintain(context.Background())
	assert.False(t, again.Vacuumed)
	assert.Zero(t, again.Pruned)
}

// A build holds write locks for minutes, so maintenance must not queue behind it
// — or take a VACUUM's exclusive lock out from under it. It skips the index that
// is being written and still maintains the other one.
func TestCodeDBMaintainer_DefersToAnActiveBuild(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite maintenance of two indexes")
	}

	tests := []struct {
		name         string
		setBusy      func(m *CodeDBManager)
		wantShared   int // snapshots left in the shared index
		wantLedger   int // snapshots left in the ledger index
		wantSidecars map[string]bool
	}{
		{
			name:         "worktree index pass in flight",
			setBusy:      func(m *CodeDBManager) { m.indexing = true },
			wantShared:   5,
			wantLedger:   1,
			wantSidecars: map[string]bool{"shared": false, "ledger": true},
		},
		{
			name:         "dirty overlay refresh in flight",
			setBusy:      func(m *CodeDBManager) { m.dirtyRefreshing = true },
			wantShared:   5,
			wantLedger:   1,
			wantSidecars: map[string]bool{"shared": false, "ledger": true},
		},
		{
			name:         "ledger build in flight",
			setBusy:      func(m *CodeDBManager) { m.ledgerIndexing = true },
			wantShared:   1,
			wantLedger:   5,
			wantSidecars: map[string]bool{"shared": true, "ledger": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, shared, ledger := maintTestManager(t)
			seedIndexWithDeadSnapshots(t, shared, 5)
			seedIndexWithDeadSnapshots(t, ledger, 5)
			mgr.mu.Lock()
			tt.setBusy(mgr)
			mgr.mu.Unlock()

			res := NewCodeDBMaintainer("codedb", mgr).Maintain(context.Background())

			require.NoError(t, res.Error)
			assert.Equal(t, tt.wantShared, snapshotCount(t, shared), "shared index")
			assert.Equal(t, tt.wantLedger, snapshotCount(t, ledger), "ledger index")
			assert.Equal(t, tt.wantSidecars["shared"], fileExists(filepath.Join(shared, maintSidecarName)), "shared sidecar")
			assert.Equal(t, tt.wantSidecars["ledger"], fileExists(filepath.Join(ledger, maintSidecarName)), "ledger sidecar")
		})
	}
}

// An index the daily scan proves damaged is discarded for a clean rebuild — what
// the open-time scan used to do — but only that index: the ledger index lives
// inside the shared index's directory and must not pay a 5 GB rebuild for damage
// that is not its own. The next freshness check must rebuild rather than trust
// the HEAD it last indexed.
func TestCodeDBMaintainer_DiscardsOnlyTheDamagedIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite maintenance of two indexes")
	}

	tests := []struct {
		name         string
		damage       func(shared, ledger string) string // returns the damaged dir
		wantHeadKept bool
	}{
		{
			name:         "damaged shared index",
			damage:       func(shared, _ string) string { return shared },
			wantHeadKept: false,
		},
		{
			name:         "damaged ledger index",
			damage:       func(_, ledger string) string { return ledger },
			wantHeadKept: true, // the worktree index fingerprint is not the ledger's
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, shared, ledger := maintTestManager(t)
			seedIndexWithDeadSnapshots(t, shared, 3)
			seedIndexWithDeadSnapshots(t, ledger, 3)
			damaged := tt.damage(shared, ledger)
			healthy := ledger
			if damaged == ledger {
				healthy = shared
			}
			damageTable(t, damaged, "pr_comments")
			// bleve and friends: whatever else is in the directory goes too
			require.NoError(t, os.MkdirAll(filepath.Join(damaged, "bleve"), 0o755))
			mgr.mu.Lock()
			mgr.lastIndexedHead = "refs/heads/main:abc"
			mgr.mu.Unlock()

			res := NewCodeDBMaintainer("codedb", mgr).Maintain(context.Background())

			require.NoError(t, res.Error)
			assert.True(t, res.Healed, "corruption was detected and acted on")
			assert.False(t, res.Healthy)
			assert.False(t, fileExists(filepath.Join(damaged, store.MetadataDBFile)), "damaged database discarded")
			assert.False(t, fileExists(filepath.Join(damaged, "bleve")), "damaged index's other files discarded")
			assert.True(t, fileExists(filepath.Join(healthy, store.MetadataDBFile)), "the healthy index must survive")
			assert.Equal(t, 1, snapshotCount(t, healthy), "the healthy index is still maintained")

			mgr.mu.Lock()
			head := mgr.lastIndexedHead
			mgr.mu.Unlock()
			if tt.wantHeadKept {
				assert.NotEmpty(t, head)
			} else {
				assert.Empty(t, head, "freshness fingerprint must be forgotten so the next pass rebuilds")
			}
		})
	}
}

// A check that could not run proves nothing about the data (#875), and a rebuild
// costs minutes of CPU, so only a verdict that found damage discards the index.
func TestLicensesDiscard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"check found damage", fmt.Errorf("sqlite: %w: PRAGMA quick_check returned: row 3 missing", store.ErrCorrupt), true},
		{"check could not run", fmt.Errorf("sqlite: %w: PRAGMA quick_check: disk I/O error", store.ErrIntegrityUnknown), false},
		{"check canceled", fmt.Errorf("%w: %w", store.ErrIntegrityUnknown, context.Canceled), false},
		{"database busy", fmt.Errorf("%w: database is locked", store.ErrIntegrityUnknown), false},
		{"no verdict", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, licensesDiscard(tt.err))
		})
	}
}

// A canceled maintenance pass must not discard anything.
func TestCodeDBMaintainer_CanceledPassDiscardsNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite maintenance of two indexes")
	}
	mgr, shared, ledger := maintTestManager(t)
	seedIndexWithDeadSnapshots(t, shared, 3)
	seedIndexWithDeadSnapshots(t, ledger, 3)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := NewCodeDBMaintainer("codedb", mgr).Maintain(ctx)

	assert.False(t, res.Healed)
	assert.Equal(t, 3, snapshotCount(t, shared))
	assert.Equal(t, 3, snapshotCount(t, ledger))
}

// realDamageError provokes the error a damaged page produces from a real statement.
func realDamageError(t *testing.T) error {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "codedb")
	seedIndexWithDeadSnapshots(t, dir, 1)
	damageTable(t, dir, "file_revs")
	db, err := codedb.OpenSQLOnly(dir)
	require.NoError(t, err)
	defer db.Close()
	// no index covers (id, path, blob_id) together, so this reads the damaged table itself
	rows, err := db.Store().Query(`SELECT id, path, blob_id FROM file_revs`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
		}
		err = rows.Err()
	}
	require.Error(t, err, "fixture: damaged table must fail a read")
	return err
}

// discardIfCorrupt covers the pipeline stages after the git index (symbols,
// comments), which the open-time scan used to cover: they would otherwise hit the
// same damaged page on every pass and never recover. Only damage licenses the
// discard.
func TestDiscardIfCorrupt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	damageErr := realDamageError(t)

	tests := []struct {
		name        string
		err         error
		wantDiscard bool
	}{
		{"sqlite damage met by a statement", fmt.Errorf("parse symbols: %w", damageErr), true},
		{"corruption verdict", fmt.Errorf("sqlite: %w", store.ErrCorrupt), true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"a failure that is not damage", errors.New("tree-sitter exploded"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, shared, ledger := maintTestManager(t)
			seedIndexWithDeadSnapshots(t, shared, 1)
			seedIndexWithDeadSnapshots(t, ledger, 1)
			db, err := codedb.OpenSQLOnly(shared)
			require.NoError(t, err)
			defer db.Close()

			mgr.discardIfCorrupt(db, shared, tt.err)

			assert.Equal(t, !tt.wantDiscard, fileExists(filepath.Join(shared, store.MetadataDBFile)), "shared database present")
			assert.True(t, fileExists(filepath.Join(ledger, store.MetadataDBFile)), "the nested ledger index is never collateral")
		})
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.local",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.local",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// The ledger build used to depend on the open-time scan to notice a damaged
// index; without it the build would fail on the same page on every HEAD move and
// never recover.
//
// Failure prevented: a ledger index with one damaged page is never rebuilt.
func TestBuildLedgerIndex_DiscardsADamagedIndexAndRecovers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone + codedb indexing")
	}
	repo := initDirtyTestRepo(t)
	mgr, _, ledger := maintTestManager(t)
	ctx := context.Background()

	mgr.BuildLedgerIndex(ctx, repo)
	require.True(t, fileExists(filepath.Join(ledger, store.MetadataDBFile)), "first build creates the index")

	// a new commit forces the next build to write to the tables damaged below
	require.NoError(t, os.WriteFile(filepath.Join(repo, "second.go"), []byte("package main\n// second\n"), 0o644))
	gitIn(t, repo, "add", "second.go")
	gitIn(t, repo, "commit", "-m", "second")
	for _, table := range []string{"commits", "blobs", "file_revs"} {
		damageTable(t, ledger, table)
	}

	mgr.BuildLedgerIndex(ctx, repo)
	assert.False(t, fileExists(filepath.Join(ledger, store.MetadataDBFile)), "the damaged ledger index must be discarded")

	// the next build starts clean and succeeds
	mgr.BuildLedgerIndex(ctx, repo)
	require.True(t, fileExists(filepath.Join(ledger, store.MetadataDBFile)))
	db, err := codedb.OpenSQLOnly(ledger)
	require.NoError(t, err)
	defer db.Close()
	var commits int
	require.NoError(t, db.Store().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&commits))
	assert.Equal(t, 2, commits, "the rebuilt index covers the whole history")
}
