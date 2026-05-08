package store

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// expectedTables lists every table the schema should create.
var expectedTables = []string{
	"repos",
	"commits",
	"commit_parents",
	"refs",
	"blobs",
	"file_revs",
	"diffs",
	"symbols",
	"symbol_refs",
}

// openStore is a test helper that opens a store in a temp dir and registers cleanup.
func openStore(t *testing.T) *Store {
	t.Helper()
	tmp := t.TempDir()
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open(%s): %v", tmp, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesStructure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()
	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// verify filesystem structure
	for _, rel := range []string{
		"metadata.db",
		"repos",
		"bleve/code",
		"bleve/diff",
	} {
		path := filepath.Join(tmp, rel)
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("expected %s to exist: %v", rel, statErr)
		}
	}

	// verify SQL works
	_, err = s.Exec("INSERT INTO repos (name, path) VALUES ('test', '/tmp/test')")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 repo, got %d", count)
	}
}

func TestOpenIdempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// insert data before closing to verify it persists
	if _, err := s1.Exec("INSERT INTO repos (name, path) VALUES ('persist', '/tmp/p')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s1.Close()

	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	var name string
	if err := s2.QueryRow("SELECT name FROM repos WHERE name='persist'").Scan(&name); err != nil {
		t.Fatalf("data did not survive reopen: %v", err)
	}
	if name != "persist" {
		t.Errorf("expected 'persist', got %q", name)
	}
}

func TestOpenCorruptSQLite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	// write garbage to metadata.db before first Open
	dbPath := filepath.Join(tmp, "metadata.db")
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	_, err := Open(tmp)
	if err == nil {
		t.Fatal("expected error from corrupt database, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}

	// corrupt file should be removed so a subsequent Open succeeds
	if _, statErr := os.Stat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("corrupt metadata.db should have been removed")
	}
}

func TestOpenCorruptBleve(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	// first open to create structure
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// corrupt the bleve code index by replacing its directory with a file
	bleveCodeDir := filepath.Join(tmp, "bleve", "code")
	if err := os.RemoveAll(bleveCodeDir); err != nil {
		t.Fatalf("remove bleve/code: %v", err)
	}
	if err := os.WriteFile(bleveCodeDir, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	// openOrCreateBleveIndex should recover by recreating
	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open after bleve corruption should recover, got: %v", err)
	}
	defer s2.Close()

	// verify the recreated index is functional
	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after bleve recovery: %v", err)
	}
}

func TestOpenMissingBleveDir(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// delete bleve code directory entirely
	if err := os.RemoveAll(filepath.Join(tmp, "bleve", "code")); err != nil {
		t.Fatalf("remove bleve/code: %v", err)
	}

	// reopen should recreate the missing index
	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open with missing bleve dir should recreate, got: %v", err)
	}
	defer s2.Close()

	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after bleve recreation: %v", err)
	}
}

func TestOpenPermissionDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	if runtime.GOOS == "windows" {
		t.Skip("permission test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	t.Parallel()

	tmp := t.TempDir()
	unwritable := filepath.Join(tmp, "readonly")
	if err := os.MkdirAll(unwritable, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// ensure cleanup can remove the directory
	t.Cleanup(func() { os.Chmod(unwritable, 0o700) })

	nested := filepath.Join(unwritable, "store")
	_, err := Open(nested)
	if err == nil {
		t.Fatal("expected error opening store in unwritable directory, got nil")
	}
}

func TestCheckIntegrity_Healthy(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	if err := s.CheckIntegrity(); err != nil {
		t.Errorf("fresh store should pass integrity check: %v", err)
	}
}

func TestCheckIntegrity_CorruptDB(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	// write garbage to metadata.db, then open a *new* connection against it.
	// SQLite caches pages in memory, so corrupting the file under an open
	// connection won't always be detected. Instead we corrupt before opening
	// and verify that Open itself returns ErrCorrupt.
	dbPath := filepath.Join(tmp, "metadata.db")

	// first create a valid store so the directory structure exists
	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	s1.Close()

	// now corrupt the database on disk
	garbage := make([]byte, 4096)
	if _, err := rand.Read(garbage); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	// Open should detect corruption via PRAGMA integrity_check and return ErrCorrupt
	_, err = Open(tmp)
	if err == nil {
		t.Fatal("expected error from corrupt database, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Errorf("first Close returned unexpected error: %v", err)
	}

	// second close should be safe (no panic, no error)
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned unexpected error: %v", err)
	}
}

func TestReposDir(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	expected := filepath.Join(tmp, "repos")
	got := s.ReposDir()
	if got != expected {
		t.Errorf("ReposDir() = %q, want %q", got, expected)
	}

	// verify the directory actually exists
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("ReposDir path does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("ReposDir path is not a directory")
	}
}

func TestConcurrentOpen(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	// pre-create the store so concurrent opens don't race on schema creation
	s0, err := Open(tmp)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	s0.Close()

	const goroutines = 8
	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s, openErr := Open(tmp)
			if openErr != nil {
				errsMu.Lock()
				errs = append(errs, openErr)
				errsMu.Unlock()
				return
			}
			// do a small read to exercise WAL concurrency
			var count int
			s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count)
			s.Close()
		}()
	}

	wg.Wait()

	if len(errs) > 0 {
		t.Errorf("concurrent Open produced %d errors; first: %v", len(errs), errs[0])
	}
}

func TestSchemaCreation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	for _, table := range expectedTables {
		t.Run(table, func(t *testing.T) {
			// sqlite_master query to verify table exists
			var name string
			err := s.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
				table,
			).Scan(&name)
			if err != nil {
				t.Fatalf("table %q not found in schema: %v", table, err)
			}
		})
	}
}

func TestSchemaIndexes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	expectedIndexes := []string{
		"idx_commits_repo",
		"idx_refs_repo",
		"idx_file_revs_commit",
		"idx_file_revs_blob",
		"idx_diffs_commit",
		"idx_symbols_blob",
		"idx_symbols_name",
		"idx_symbol_refs_blob",
		"idx_symbol_refs_name",
		"idx_symbol_refs_symbol",
	}

	for _, idx := range expectedIndexes {
		t.Run(idx, func(t *testing.T) {
			var name string
			err := s.QueryRow(
				"SELECT name FROM sqlite_master WHERE type='index' AND name=?",
				idx,
			).Scan(&name)
			if err != nil {
				t.Fatalf("index %q not found in schema: %v", idx, err)
			}
		})
	}
}

func TestSQLConvenienceMethods(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// Exec + QueryRow
	res, err := s.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "r1", "/r1")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if id < 1 {
		t.Errorf("expected positive insert ID, got %d", id)
	}

	// Query
	rows, err := s.Query("SELECT name FROM repos WHERE path = ?", "/r1")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("Query returned no rows")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if name != "r1" {
		t.Errorf("expected 'r1', got %q", name)
	}

	// Begin / transaction
	tx, err := s.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = tx.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "r2", "/r2")
	if err != nil {
		t.Fatalf("tx Exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var count int
	if err := s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 repos after transaction, got %d", count)
	}
}

func TestOpenCorruptBleveDiffIndex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()

	s1, err := Open(tmp)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	// corrupt the diff index specifically
	bleveDiffDir := filepath.Join(tmp, "bleve", "diff")
	if err := os.RemoveAll(bleveDiffDir); err != nil {
		t.Fatalf("remove bleve/diff: %v", err)
	}
	if err := os.WriteFile(bleveDiffDir, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	s2, err := Open(tmp)
	if err != nil {
		t.Fatalf("Open after diff index corruption should recover, got: %v", err)
	}
	defer s2.Close()

	if err := s2.CheckIntegrity(); err != nil {
		t.Errorf("integrity check failed after diff index recovery: %v", err)
	}
}

func TestOpenNonexistentRoot(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "a", "b", "c")

	// Open should create intermediate directories
	s, err := Open(nested)
	if err != nil {
		t.Fatalf("Open with nested nonexistent path should succeed: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(filepath.Join(nested, "metadata.db")); err != nil {
		t.Errorf("metadata.db not created in nested path: %v", err)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// inserting a commit with a nonexistent repo_id should fail if foreign keys are on
	_, err := s.Exec(
		"INSERT INTO commits (repo_id, hash, author, message, timestamp) VALUES (999, 'abc', 'a', 'm', 0)",
	)
	if err == nil {
		t.Error("expected foreign key violation, got nil")
	}
}

// TestOpenOrCreateBleveIndex_LockedIndexNotNuked verifies that openOrCreateBleveIndex
// does NOT delete an existing bleve index when an open error occurs and root.bolt is present.
// Regression test: previously, any non-ENOENT error caused os.RemoveAll, which would
// destroy an index being actively written by another goroutine.
//
// bleve opens bbolt without a timeout, so true lock-timeout cannot be triggered in-process.
// Instead the test creates a real index at indexPath (so root.bolt has valid bbolt structure),
// then corrupts root.bolt to produce an immediate open error — the same code path that fires
// when another goroutine holds the bbolt exclusive lock.
func TestOpenOrCreateBleveIndex_LockedIndexNotNuked(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")

	// create a real bleve index at indexPath so root.bolt exists with valid bbolt structure
	first, err := openOrCreateBleveIndex(indexPath, "test")
	if err != nil {
		t.Fatalf("create index: %v", err)
	}
	first.Close()

	boltPath := filepath.Join(indexPath, "store", "root.bolt")

	// corrupt root.bolt — causes bbolt to return an immediate error (invalid magic bytes)
	// rather than waiting indefinitely for a lock, exercising the same "bolt exists → don't nuke"
	// code path that fires during real lock contention
	require.NoError(t, os.WriteFile(boltPath, []byte("corrupted"), 0600))

	// openOrCreateBleveIndex must fail (corrupt bolt) but must NOT delete the index directory
	_, openErr := openOrCreateBleveIndex(indexPath, "test")
	require.Error(t, openErr, "expected error opening corrupt index")

	// bolt file must survive — index was not nuked
	if _, statErr := os.Stat(boltPath); statErr != nil {
		t.Errorf("root.bolt was deleted: openOrCreateBleveIndex nuked on open error: %v", statErr)
	}
}

// emptyMappingForLatestSnapshot opens root.bolt and overwrites the latest
// snapshot's `_mapping` value with zero bytes — reproducing the on-disk state
// observed in the field where bleve.Open fails with "error parsing mapping
// JSON: unexpected end of JSON input" while .zap shards are healthy.
func emptyMappingForLatestSnapshot(t *testing.T, boltPath string) {
	t.Helper()
	db, err := bbolt.Open(boltPath, 0600, &bbolt.Options{Timeout: 2 * time.Second})
	require.NoError(t, err, "open bolt for mapping corruption")
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		require.NotNil(t, snaps, "snapshots bucket missing — bleve layout changed")
		var lastKey []byte
		require.NoError(t, snaps.ForEach(func(k, _ []byte) error {
			lastKey = append(lastKey[:0], k...)
			return nil
		}))
		require.NotNil(t, lastKey, "no snapshots in bolt")
		snap := snaps.Bucket(lastKey)
		require.NotNil(t, snap, "snapshot bucket missing")
		internal := snap.Bucket([]byte{'i'})
		require.NotNil(t, internal, "internal bucket missing — no mapping to corrupt")
		return internal.Put([]byte("_mapping"), []byte{})
	}))
	require.NoError(t, db.Close())
}

// TestOpen_EmptyMapping_ReturnsTypedError verifies that the empty-mapping
// failure mode (root.bolt present, _mapping value zero-length) is surfaced
// as MappingCorruptError instead of being misclassified as lock contention.
//
// Failure prevented: daemon spinning at 340% CPU because each indexing pass
// fails with the wrapped lock-contention error and openOrCreateBleveIndex
// refuses to nuke an "in-use" index that is actually permanently broken.
func TestOpen_EmptyMapping_ReturnsTypedError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	s1, err := Open(tmp)
	require.NoError(t, err, "first Open")
	require.NoError(t, s1.Close())

	boltPath := filepath.Join(tmp, "bleve", "comment", "store", "root.bolt")
	emptyMappingForLatestSnapshot(t, boltPath)

	_, err = Open(tmp)
	require.Error(t, err, "Open must fail when mapping is empty")
	var mce *MappingCorruptError
	require.True(t, errors.As(err, &mce), "expected MappingCorruptError in chain, got: %v", err)
	require.Equal(t, "comment", mce.Name, "wrong sub-index name")

	// peer sub-indexes (code, diff) must be untouched
	for _, name := range []string{"code", "diff"} {
		boltOK := filepath.Join(tmp, "bleve", name, "store", "root.bolt")
		_, statErr := os.Stat(boltOK)
		require.NoError(t, statErr, "%s sub-index was disturbed by open of corrupt comment", name)
	}
}

// TestRebuildBleveSubIndex_Comment verifies that a targeted rebuild of the
// comment sub-index recovers Open without affecting code/diff data and
// resets the SQL flag so ParseComments will repopulate on the next pass.
//
// Failure prevented: doctor's only remedy being a full os.RemoveAll(dataDir),
// which destroys ~600MB of code data plus ~600MB of diff data to recover
// from a small mapping-doc corruption.
func TestRebuildBleveSubIndex_Comment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	s1, err := Open(tmp)
	require.NoError(t, err, "first Open")
	// seed a blob so we can verify comments_parsed reset
	_, err = s1.Exec(`INSERT INTO blobs (content_hash, language, parsed, comments_parsed) VALUES (?, ?, 1, 1)`, "deadbeef", "go")
	require.NoError(t, err, "seed blob")
	require.NoError(t, s1.Close())

	boltPath := filepath.Join(tmp, "bleve", "comment", "store", "root.bolt")
	emptyMappingForLatestSnapshot(t, boltPath)

	// Open should fail with MappingCorruptError before rebuild
	_, err = Open(tmp)
	require.Error(t, err)
	var preMCE *MappingCorruptError
	require.True(t, errors.As(err, &preMCE), "expected MappingCorruptError")

	// Capture code sub-index state AFTER the failed Open so the rebuild call
	// is the only operation that could perturb it. We assert the rebuild leaves
	// every code-side artifact byte-for-byte identical.
	codeFingerprint := codeIndexFingerprint(t, tmp)

	// targeted rebuild
	require.NoError(t, RebuildBleveSubIndex(tmp, "comment"), "rebuild comment")

	// code sub-index must be untouched
	require.Equal(t, codeFingerprint, codeIndexFingerprint(t, tmp),
		"rebuild of comment sub-index must not touch code sub-index")

	// Open + integrity must succeed post-rebuild
	s2, err := Open(tmp)
	require.NoError(t, err, "Open post-rebuild")
	defer s2.Close()
	require.NoError(t, s2.CheckIntegrity(), "integrity post-rebuild")

	// comments_parsed must be reset so ParseComments will retry the seeded blob
	var cp int
	require.NoError(t, s2.QueryRow(`SELECT comments_parsed FROM blobs WHERE content_hash = ?`, "deadbeef").Scan(&cp))
	require.Equal(t, 0, cp, "comments_parsed must be cleared after rebuild")
}

// TestRebuildBleveSubIndex_RejectsUnknownName guards the API surface so
// callers can't silently nuke an arbitrary path.
func TestRebuildBleveSubIndex_RejectsUnknownName(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	err := RebuildBleveSubIndex(tmp, "../../etc")
	require.Error(t, err, "unknown name must be rejected")
	require.False(t, errors.Is(err, ErrFullReindexRequired),
		"unknown name must not masquerade as full-reindex-required")
}

// TestRebuildBleveSubIndex_CodeDiff_RequireFullReindex is the contract that
// prevents a silent broken-heal: code and diff cannot be repopulated from
// SQL alone, so RebuildBleveSubIndex must refuse and force callers to fall
// back to a full reindex. Without this, doctor/daemon would report a
// "successful" rebuild while leaving search permanently empty.
//
// Failure prevented: a code/diff rebuild that recreates an empty bleve and
// returns nil — the previous behavior in this PR's first revision.
func TestRebuildBleveSubIndex_CodeDiff_RequireFullReindex(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()

	for _, name := range []string{"code", "diff"} {
		err := RebuildBleveSubIndex(tmp, name)
		require.Error(t, err, "%s rebuild must not silently succeed", name)
		require.True(t, errors.Is(err, ErrFullReindexRequired),
			"%s rebuild must wrap ErrFullReindexRequired so callers can branch on it", name)
	}

	// Refusal must NOT touch the filesystem — bleve dirs and SQL stay intact.
	// We assert nothing was created by checking the tmpdir is empty afterwards.
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "code/diff refusal must not modify state on disk")
}

// TestRebuildBleveSubIndex_Comment_BubblesSQLFailure verifies that if the
// SQL flag reset fails, the rebuild does NOT silently report success.
// Without this, the comment bleve would be empty AND comments_parsed=1
// would still be set on every blob, leaving comment search dead until a
// future migration or manual intervention.
//
// Failure prevented: silent broken-heal where the bleve is rebuilt empty
// but ParseComments has nothing to repopulate it with.
func TestRebuildBleveSubIndex_Comment_BubblesSQLFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	tmp := t.TempDir()
	s, err := Open(tmp)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Replace metadata.db with garbage so PRAGMA (or the UPDATE) fails.
	dbPath := filepath.Join(tmp, MetadataDBFile)
	require.NoError(t, os.WriteFile(dbPath, []byte("not a sqlite file"), 0o600))

	rbErr := RebuildBleveSubIndex(tmp, "comment")
	require.Error(t, rbErr, "rebuild must surface SQL failure, not return nil")
	require.False(t, errors.Is(rbErr, ErrFullReindexRequired),
		"SQL failure must not be confused with full-reindex-required")
}

// TestIsBleveIndexCorrupt_UnreadableStoreDir_NotFlagged verifies that a
// transient/permission failure to list the store/ directory does NOT
// cause isBleveIndexCorrupt to return true. Without this guard, an
// EACCES on store/ would make every referenced segment look missing and
// route a healthy index to the destructive rebuild path.
//
// Failure prevented: false-positive corruption signal triggering data
// loss on transient I/O.
func TestIsBleveIndexCorrupt_UnreadableStoreDir_NotFlagged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: Bleve operations")
	}
	if runtime.GOOS == "windows" {
		t.Skip("permission test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "idx")
	idx, err := openOrCreateBleveIndex(indexPath, "test")
	require.NoError(t, err)
	require.NoError(t, idx.Close())

	storeDir := filepath.Join(indexPath, "store")
	boltPath := filepath.Join(storeDir, "root.bolt")
	t.Cleanup(func() { _ = os.Chmod(storeDir, 0o700) })
	require.NoError(t, os.Chmod(storeDir, 0o000), "make store dir unreadable")

	// bolt path itself stat-able through parent (we own it); but ReadDir on
	// store/ will EACCES. Must NOT report corrupt.
	require.False(t, isBleveIndexCorrupt(boltPath),
		"unreadable store dir must not be misread as missing segments")
}

func mustModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err, "stat %s", path)
	return info.ModTime()
}

// codeIndexFingerprint hashes file paths + mtimes + sizes under bleve/code/.
// Used to assert a rebuild of one sub-index doesn't perturb a peer.
func codeIndexFingerprint(t *testing.T, root string) string {
	t.Helper()
	codeDir := filepath.Join(root, "bleve", "code")
	var entries []string
	err := filepath.Walk(codeDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(codeDir, p)
		entries = append(entries, rel+":"+info.ModTime().UTC().String()+":"+
			fmtInt64(info.Size()))
		return nil
	})
	require.NoError(t, err, "walk code dir")
	return joinSorted(entries)
}

func fmtInt64(n int64) string {
	return time.Unix(n, 0).UTC().String()
}

func joinSorted(s []string) string {
	out := make([]string, len(s))
	copy(out, s)
	// stable order independent of walk order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	res := ""
	for _, v := range out {
		res += v + "\n"
	}
	return res
}
