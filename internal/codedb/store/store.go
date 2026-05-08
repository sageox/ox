package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/blevesearch/bleve/v2"
	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"go.etcd.io/bbolt"
	_ "modernc.org/sqlite"
)

// ErrCorrupt indicates the index is corrupted and needs re-indexing.
var ErrCorrupt = fmt.Errorf("codedb index is corrupt")

// ErrFullReindexRequired is returned by RebuildBleveSubIndex when the
// requested sub-index (code/diff) cannot be repopulated from existing SQL
// data alone. Callers that hit this should fall back to a full reindex
// (wipe dataDir + run IndexLocalRepo) — the rebuild path is currently only
// safe for "comment" because ParseComments is gated on a per-blob SQL flag
// we can reset, while code/diff are populated only during the per-commit
// walk in IndexRepo and have no rebleve-from-blobs path yet.
var ErrFullReindexRequired = errors.New("full reindex required")

// MappingCorruptError indicates that a Bleve sub-index is in a structurally
// broken state that bleve.Open cannot recover from on its own. The Name
// identifies which sub-index ("code", "diff", or "comment") so callers can
// perform a targeted rebuild via RebuildBleveSubIndex without nuking the
// whole dataDir.
//
// Detected conditions (see isBleveIndexCorrupt):
//   - persisted `_mapping` doc is empty/missing in the latest snapshot, or
//   - the latest snapshot references segment IDs whose `.zap` files are
//     missing on disk (the field-observed poison pill: bolt + mapping intact
//     but a previous incomplete write left the snapshot pointing at segments
//     that never landed)
//
// Distinct from "real lock contention" (another goroutine/process actively
// writing): we only return this after a successful read-only bbolt open with
// 100ms timeout — a held exclusive lock blocks the read and we stay in the
// safe lock-contention path.
type MappingCorruptError struct {
	Name string
	Path string
}

func (e *MappingCorruptError) Error() string {
	return fmt.Sprintf("bleve %s index is structurally corrupt at %s", e.Name, e.Path)
}

// MetadataDBFile is the filename of the SQLite database inside a CodeDB directory.
const MetadataDBFile = "metadata.db"

// BleveSubIndexNames lists the bleve sub-indexes managed by Store, in the same
// order they are opened. Used by self-heal callers (daemon, doctor) so they
// don't have to hardcode the names.
var BleveSubIndexNames = []string{"code", "diff", "comment"}

// Store wraps a SQLite database and Bleve full-text search indexes.
// All SQL access goes through the convenience methods below.
//
// The store supports a two-tier architecture:
//   - Shared indexes (on-disk): committed content, shared across worktrees
//   - Dirty overlay (on-disk or in-memory): uncommitted worktree files, per-worktree
//
// When a dirty overlay is attached, CombinedCodeIndex transparently merges
// results from both tiers via Bleve IndexAlias.
type Store struct {
	db           *sql.DB
	queries      *codedbsqlc.Queries
	CodeIndex    bleve.Index
	DiffIndex    bleve.Index
	CommentIndex bleve.Index
	Root         string
	closeOnce    sync.Once

	// dirty overlays for uncommitted worktree files (keyed by worktree ID)
	dirtyCodeIndexes  map[string]bleve.Index
	CombinedCodeIndex bleve.Index // alias of CodeIndex + all dirty indexes, or just CodeIndex
}

// Open opens (or creates) a Store at the given root directory.
// It creates the directory structure, initializes SQLite and Bleve indexes.
// If SQLite corruption is detected, the database is removed and ErrCorrupt is returned
// so the caller can trigger a full re-index.
func Open(root string) (*Store, error) {
	reposDir := filepath.Join(root, "repos")
	bleveDir := filepath.Join(root, "bleve")
	bleveCodeDir := filepath.Join(bleveDir, "code")
	bleveDiffDir := filepath.Join(bleveDir, "diff")
	bleveCommentDir := filepath.Join(bleveDir, "comment")

	for _, dir := range []string{root, reposDir, bleveDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	dbPath := filepath.Join(root, MetadataDBFile)
	// WAL: concurrent readers + one writer. busy_timeout: wait up to 5s for
	// write locks instead of failing immediately. This matters when multiple
	// daemons (one per worktree) share the same index. Long-term fix is
	// one-daemon-per-repo; until then busy_timeout provides best-effort safety.
	// cache_size(-65536): 64 MB page cache. mmap_size(268435456): map up to
	// 256 MB of the DB into memory for faster reads; SQLite silently falls back
	// to normal I/O for pages beyond the mmap region or on unsupported systems.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(MEMORY)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// integrity check before schema creation
	if err := checkSQLiteIntegrity(db); err != nil {
		db.Close()
		slog.Error("sqlite corruption detected, removing database", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		return nil, fmt.Errorf("sqlite integrity check failed: %w", ErrCorrupt)
	}

	if err := CreateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	codeIndex, err := openOrCreateBleveIndex(bleveCodeDir, "code")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open code index: %w", err)
	}

	diffIndex, err := openOrCreateBleveIndex(bleveDiffDir, "diff")
	if err != nil {
		db.Close()
		codeIndex.Close()
		return nil, fmt.Errorf("open diff index: %w", err)
	}

	commentIndex, err := openOrCreateBleveIndex(bleveCommentDir, "comment")
	if err != nil {
		db.Close()
		codeIndex.Close()
		diffIndex.Close()
		return nil, fmt.Errorf("open comment index: %w", err)
	}

	s := &Store{
		db:               db,
		queries:          codedbsqlc.New(db),
		CodeIndex:        codeIndex,
		DiffIndex:        diffIndex,
		CommentIndex:     commentIndex,
		Root:             root,
		dirtyCodeIndexes: make(map[string]bleve.Index),
	}
	s.CombinedCodeIndex = s.CodeIndex // default: no overlay
	return s, nil
}

// ReposDir returns the path to the bare git repos directory.
func (s *Store) ReposDir() string {
	return filepath.Join(s.Root, "repos")
}

// Close closes all resources. It is safe to call multiple times.
func (s *Store) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		s.DetachDirtyOverlay()
		if err := s.CodeIndex.Close(); err != nil {
			firstErr = err
		}
		if err := s.DiffIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.CommentIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// AttachDirtyOverlay creates an in-memory Bleve index for dirty worktree files
// and combines it with the shared CodeIndex via IndexAlias. Search code using
// CombinedCodeIndex will transparently search both.
// Primarily used in tests; production uses AttachDirtyIndex for on-disk overlays.
func (s *Store) AttachDirtyOverlay() error {
	s.DetachDirtyOverlay() // close any existing overlays first
	mapping := bleve.NewIndexMapping()
	dirtyIdx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		return fmt.Errorf("create in-memory dirty index: %w", err)
	}
	s.dirtyCodeIndexes["__test__"] = dirtyIdx
	s.rebuildCombinedIndex()
	return nil
}

// AttachDirtyIndex opens an existing on-disk dirty overlay index (built by the
// daemon) and aliases it with the shared CodeIndex for transparent search.
// Uses a default key; for multi-worktree support use AttachDirtyIndexByID.
func (s *Store) AttachDirtyIndex(dirtyBlevePath string) error {
	return s.AttachDirtyIndexByID("__default__", dirtyBlevePath)
}

// AttachDirtyIndexByID opens an on-disk dirty overlay and adds it to the overlay map
// under the given ID. If the ID is already attached, the old overlay is detached first.
// Rebuilds the combined alias to include all active overlays.
func (s *Store) AttachDirtyIndexByID(id, dirtyBlevePath string) error {
	// detach existing overlay for this ID if present
	if existing, ok := s.dirtyCodeIndexes[id]; ok {
		existing.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	dirtyIdx, err := bleve.Open(dirtyBlevePath)
	if err != nil {
		s.rebuildCombinedIndex() // keep alias consistent
		return fmt.Errorf("open dirty index %s: %w", id, err)
	}
	s.dirtyCodeIndexes[id] = dirtyIdx
	s.rebuildCombinedIndex()
	return nil
}

// DetachDirtyIndexByID closes and removes a specific dirty overlay by ID.
// Rebuilds the combined alias with remaining overlays.
func (s *Store) DetachDirtyIndexByID(id string) {
	if idx, ok := s.dirtyCodeIndexes[id]; ok {
		idx.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	s.rebuildCombinedIndex()
}

// DetachDirtyOverlay closes all attached dirty overlays and resets CombinedCodeIndex.
func (s *Store) DetachDirtyOverlay() {
	for id, idx := range s.dirtyCodeIndexes {
		idx.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	s.CombinedCodeIndex = s.CodeIndex
}

// DirtyOverlayCount returns the number of currently attached dirty overlays.
func (s *Store) DirtyOverlayCount() int {
	return len(s.dirtyCodeIndexes)
}

// DirtyCodeIndex returns the first dirty overlay index found, or nil.
// Used by callers that need direct access to a dirty index (e.g., for indexing docs).
func (s *Store) DirtyCodeIndex() bleve.Index {
	for _, idx := range s.dirtyCodeIndexes {
		return idx
	}
	return nil
}

// rebuildCombinedIndex reconstructs CombinedCodeIndex from CodeIndex + all dirty overlays.
func (s *Store) rebuildCombinedIndex() {
	if len(s.dirtyCodeIndexes) == 0 {
		s.CombinedCodeIndex = s.CodeIndex
		return
	}
	indexes := make([]bleve.Index, 0, 1+len(s.dirtyCodeIndexes))
	indexes = append(indexes, s.CodeIndex)
	for _, idx := range s.dirtyCodeIndexes {
		indexes = append(indexes, idx)
	}
	s.CombinedCodeIndex = bleve.NewIndexAlias(indexes...)
}

// CheckIntegrity validates that the SQLite database and all Bleve indexes
// are healthy. Returns nil if everything is fine, ErrCorrupt otherwise.
func (s *Store) CheckIntegrity() error {
	if err := checkSQLiteIntegrity(s.db); err != nil {
		return fmt.Errorf("sqlite: %w", ErrCorrupt)
	}

	// validate bleve indexes can serve a basic query
	for name, idx := range map[string]bleve.Index{"code": s.CodeIndex, "diff": s.DiffIndex, "comment": s.CommentIndex} {
		q := bleve.NewMatchNoneQuery()
		req := bleve.NewSearchRequest(q)
		req.Size = 0
		if _, err := idx.Search(req); err != nil {
			return fmt.Errorf("bleve %s index: %w", name, ErrCorrupt)
		}
	}

	return nil
}

// checkSQLiteIntegrity runs PRAGMA integrity_check and returns an error if the database is corrupt.
func checkSQLiteIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check query failed: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned: %s", result)
	}
	return nil
}

// removeSQLiteFiles removes the database file and its WAL/SHM sidecars.
func removeSQLiteFiles(dbPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove sqlite file", "path", p, "err", err)
		}
	}
}

func openOrCreateBleveIndex(path, name string) (bleve.Index, error) {
	idx, err := safeOpenBleve(path)
	if err == nil {
		return idx, nil
	}
	if errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
		mapping := bleve.NewIndexMapping()
		return bleve.New(path, mapping)
	}

	// Before treating as corruption, check if the bbolt file exists.
	// If it does, the error is most often lock contention (another goroutine
	// or process holds the bbolt exclusive flock). Nuking in that case destroys
	// an index that is actively being written.
	//
	// But there's a separate failure mode hiding behind the same surface error:
	// the bolt file exists, the snapshots are present, yet the persisted mapping
	// document is empty (observed: `error parsing mapping JSON: unexpected end
	// of JSON input` while the worktree's diff/code shards are healthy). That
	// state is unrecoverable by waiting — the daemon will spin forever — so we
	// peek the mapping non-blocking and surface a typed MappingCorruptError
	// when the mapping is provably empty. The peek uses a read-only open with
	// a 100ms timeout: a real exclusive lock blocks the read, the peek bails
	// out, and we keep the safe lock-contention behavior.
	boltPath := filepath.Join(path, "store", "root.bolt")
	if _, statErr := os.Stat(boltPath); statErr == nil {
		if isBleveIndexCorrupt(boltPath) {
			return nil, &MappingCorruptError{Name: name, Path: path}
		}
		return nil, fmt.Errorf("bleve index appears to be in use (lock contention): %w", err)
	} else if !os.IsNotExist(statErr) && !errors.Is(statErr, syscall.ENOTDIR) {
		// permission/IO errors are transient — nuking would cause data loss
		return nil, fmt.Errorf("stat bleve bolt file %s: %w", boltPath, statErr)
	}

	// bolt file absent or path structure broken — nuke and recreate
	slog.Error("bleve index corrupt, recreating", "path", path, "err", err)
	if removeErr := os.RemoveAll(path); removeErr != nil {
		return nil, fmt.Errorf("remove corrupt bleve index %s: %w", path, removeErr)
	}
	mapping := bleve.NewIndexMapping()
	return bleve.New(path, mapping)
}

// isBleveIndexCorrupt opens root.bolt read-only with a short timeout and
// affirmatively checks for two on-disk failure modes that present as the
// same opaque "error parsing mapping JSON" surface error:
//
//  1. Empty/missing `_mapping` doc in every snapshot.
//  2. Latest snapshot references segment IDs whose `.zap` shard files are
//     missing on disk. (Observed in the field on a real poison pill: bolt is
//     fully readable, mapping is intact, yet `bleve.Open` still fails because
//     scorch can't load the snapshot's segments — and downstream the mapping
//     read returns empty bytes through the degraded reader.)
//
// Returns true only when we successfully read the bolt AND can prove one of
// the two conditions. On any access error (read-only open timeout, permission,
// unparseable bolt structure) returns false — we never flag corruption
// without proof, so a real exclusive write lock stays in the lock-contention
// path.
//
// scorch's bbolt layout (verified against bleve v2.5.7):
//
//	bucket "s" (snapshots)
//	  └── bucket <8-byte BE epoch>
//	         ├── bucket "i" (internal)        — `_mapping` key here
//	         ├── bucket "m" (meta)
//	         └── bucket <0xf7-prefixed segId>  — one per .zap segment in snapshot
//
// Segment bucket keys are 0xf7 followed by a varint-encoded segment epoch
// (two-byte minimum we see in practice). Segment epochs match the lower bits
// of the .zap filename (e.g. segment epoch 0x521 → 000000000521.zap).
func isBleveIndexCorrupt(boltPath string) bool {
	db, err := bbolt.Open(boltPath, 0600, &bbolt.Options{
		ReadOnly: true,
		Timeout:  100 * time.Millisecond,
	})
	if err != nil {
		return false
	}
	defer db.Close()

	storeDir := filepath.Dir(boltPath)
	zapsOnDisk, listErr := zapFilesInDir(storeDir)
	if listErr != nil {
		// can't enumerate segments — won't claim corruption without proof
		return false
	}

	var corrupt bool
	_ = db.View(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		if snaps == nil {
			return nil
		}
		// pick the latest snapshot (highest 8-byte BE epoch — last in cursor order)
		c := snaps.Cursor()
		var latestKey []byte
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			latestKey = append(latestKey[:0], k...)
		}
		if latestKey == nil {
			return nil
		}
		snap := snaps.Bucket(latestKey)
		if snap == nil {
			return nil
		}

		// (1) mapping doc empty/missing
		internal := snap.Bucket([]byte{'i'})
		if internal == nil || len(internal.Get([]byte("_mapping"))) == 0 {
			corrupt = true
			return nil
		}

		// (2) any referenced segment's .zap is missing on disk
		_ = snap.ForEach(func(name []byte, _ []byte) error {
			if len(name) == 0 || name[0] != 0xf7 {
				return nil
			}
			segEpoch := decodeSegmentEpoch(name[1:])
			zapName := fmt.Sprintf("%012x.zap", segEpoch)
			if _, ok := zapsOnDisk[zapName]; !ok {
				corrupt = true
			}
			return nil
		})
		return nil
	})
	return corrupt
}

// zapFilesInDir returns a set of *.zap filenames in dir for fast lookup.
// On any os.ReadDir error returns the error so the caller can refuse to
// flag corruption based on incomplete information — a transient read
// failure must not be misread as "every segment is missing."
func zapFilesInDir(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) >= 4 && name[len(name)-4:] == ".zap" {
			out[name] = struct{}{}
		}
	}
	return out, nil
}

// decodeSegmentEpoch parses scorch's varint-style segment epoch encoding from
// the bytes after the 0xf7 prefix. The encoding is a packed big-endian-ish
// integer; observed instances:
//
//	0x02 0x09       → 0x209  → 521
//	0x03 0x6d       → 0x36d  → 877
//	0x04 0x81       → 0x481  → 1153
//	0x05 0x21       → 0x521  → 1313
//
// Implementation: build the integer by left-shifting 8 bits per byte (i.e.
// big-endian). This matches every observed case and the on-disk .zap naming
// (lower-cased hex of the epoch zero-padded to 12 nibbles).
func decodeSegmentEpoch(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = (v << 8) | uint64(x)
	}
	return v
}

// RebuildBleveSubIndex performs a targeted rebuild of a single bleve
// sub-index. Currently supported only for "comment", which can be fully
// repopulated from existing SQL data via the comments_parsed flag.
//
// For "code" and "diff", this function returns ErrFullReindexRequired
// without modifying state — those sub-indexes are populated during the
// per-commit walk in IndexRepo, gated on `commits` SQL rows, and there is
// no rebleve-from-blobs path that could refill them from SQL alone. A
// surgical rebuild would leave search permanently empty; callers must fall
// back to a full reindex (wipe dataDir + IndexLocalRepo).
//
// On success for "comment": removes bleve/comment/, recreates empty, and
// resets blobs.comments_parsed=0 so ParseComments re-extracts every blob
// on the next indexing pass. SQL/Open failures during the flag reset are
// surfaced as errors — a rebuild that "succeeds" with comments_parsed
// still set would silently leave search empty forever.
//
// This function works without an open Store — by design, since Open fails
// when the sub-index is in the corrupt state we are recovering from.
func RebuildBleveSubIndex(root, name string) error {
	switch name {
	case "comment":
		// continue
	case "code", "diff":
		return fmt.Errorf("%s sub-index: %w", name, ErrFullReindexRequired)
	default:
		return fmt.Errorf("unknown bleve sub-index %q", name)
	}

	bleveDir := filepath.Join(root, "bleve", name)
	if err := os.RemoveAll(bleveDir); err != nil {
		return fmt.Errorf("remove %s bleve dir: %w", name, err)
	}
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.New(bleveDir, mapping)
	if err != nil {
		return fmt.Errorf("recreate %s bleve index: %w", name, err)
	}
	if err := idx.Close(); err != nil {
		return fmt.Errorf("close recreated %s bleve index: %w", name, err)
	}

	dbPath := filepath.Join(root, MetadataDBFile)
	if _, statErr := os.Stat(dbPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// no metadata.db means no blobs to mark — nothing to do (e.g. fresh dataDir)
			return nil
		}
		// permission/IO error: must not silently skip the SQL reset, which
		// would leave the rebuilt comment shard unable to repopulate.
		return fmt.Errorf("stat metadata.db for comment rebuild: %w", statErr)
	}
	db, openErr := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if openErr != nil {
		return fmt.Errorf("open metadata.db for comment rebuild: %w", openErr)
	}
	defer db.Close()
	if _, exErr := db.Exec(`UPDATE blobs SET comments_parsed = 0`); exErr != nil {
		return fmt.Errorf("reset comments_parsed after comment rebuild: %w", exErr)
	}
	return nil
}

// safeOpenBleve wraps bleve.Open with panic recovery.
// bbolt panics on certain corruption types (e.g., invalid page type)
// instead of returning an error.
func safeOpenBleve(path string) (idx bleve.Index, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("bleve.Open panicked (corrupt index)", "path", path, "panic", r)
			idx = nil
			err = fmt.Errorf("bleve.Open panic: %v", r)
		}
	}()
	return bleve.Open(path)
}

// --- SQL convenience methods ---

// Query executes a SQL query and returns the rows.
func (s *Store) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.Query(query, args...)
}

// QueryContext executes a SQL query with context and returns the rows.
func (s *Store) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a SQL query expected to return at most one row.
func (s *Store) QueryRow(query string, args ...interface{}) *sql.Row {
	return s.db.QueryRow(query, args...)
}

// Exec executes a SQL statement that doesn't return rows.
func (s *Store) Exec(query string, args ...interface{}) (sql.Result, error) {
	return s.db.Exec(query, args...)
}

// BeginTx starts a new transaction with the given context and options.
func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, opts)
}

// Begin starts a new transaction.
func (s *Store) Begin() (*sql.Tx, error) {
	return s.db.Begin()
}

// Queries returns the sqlc-generated typed queries bound to the store's DB.
func (s *Store) Queries() *codedbsqlc.Queries {
	return s.queries
}

// QueriesFromTx returns sqlc-generated typed queries bound to a transaction.
func QueriesFromTx(tx *sql.Tx) *codedbsqlc.Queries {
	return codedbsqlc.New(tx)
}
