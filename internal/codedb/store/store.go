package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	lru "github.com/hashicorp/golang-lru/v2"
	codedbsqlc "github.com/sageox/ox/internal/codedb/sqlc"
	"go.etcd.io/bbolt"
	bbolterrors "go.etcd.io/bbolt/errors"
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

// needsReindexMarkerPrefix is the file prefix used inside a codedb dataDir to
// flag that a specific bleve sub-index was nuked-and-recreated by Open's
// self-heal path and needs a full rebuild on the next indexing pass.
// Marker filename: ".needs_reindex_<name>" (e.g. ".needs_reindex_code").
const needsReindexMarkerPrefix = ".needs_reindex_"

// NeedsReindexMarkerPath returns the on-disk marker path for a bleve sub-index.
// Exported so the daemon and doctor can locate markers without hardcoding the
// prefix.
func NeedsReindexMarkerPath(root, name string) string {
	return filepath.Join(root, needsReindexMarkerPrefix+name)
}

// HasNeedsReindexMarker reports whether a self-heal marker exists for the
// named sub-index. Used by the daemon (to force --full on next pass) and by
// status/doctor commands (to surface "rebuilding" state to the user).
func HasNeedsReindexMarker(root, name string) bool {
	_, err := os.Stat(NeedsReindexMarkerPath(root, name))
	return err == nil
}

// NeedsReindexMarkers returns the names of all sub-indexes with self-heal
// markers present. Returns an empty (non-nil) slice when none are set so
// callers only have one empty-state to handle (`len(out) == 0`).
func NeedsReindexMarkers(root string) []string {
	out := make([]string, 0, len(BleveSubIndexNames))
	for _, name := range BleveSubIndexNames {
		if HasNeedsReindexMarker(root, name) {
			out = append(out, name)
		}
	}
	return out
}

// ClearNeedsReindexMarker removes the self-heal marker for a sub-index.
// Safe to call when the marker is absent. Returns nil on success or ENOENT.
func ClearNeedsReindexMarker(root, name string) error {
	err := os.Remove(NeedsReindexMarkerPath(root, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ClearAllNeedsReindexMarkers removes every marker in root. Called by the
// indexer after a successful full pass. Aggregates per-marker failures into
// a joined error so callers can surface the problem; a swallowed failure
// here would leave the daemon re-forcing --full on every freshness pass.
func ClearAllNeedsReindexMarkers(root string) error {
	var errs []error
	for _, name := range BleveSubIndexNames {
		if err := ClearNeedsReindexMarker(root, name); err != nil {
			errs = append(errs, fmt.Errorf("clear %s marker: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

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

	// blob-read pool — lazy-initialized on first ReadBlob call (search read path).
	// Per-repo mutex serializes packfile reads (go-git's packfile reader isn't
	// safe for concurrent access from the same handle). See blob_reader.go.
	blobReposOnce sync.Once
	blobRepos     []blobRepoHandle

	// diff snippet cache — lazy-initialized on first DiffSnippet call.
	// LRU dedupes per-(old_hash, new_hash, path) recomputation of the
	// indexer-format diff text during search. See diff_snippet.go.
	diffCacheOnce sync.Once
	diffCache     *lru.Cache[string, string]
}

// Open opens (or creates) a Store at the given root directory.
// It creates the directory structure, initializes SQLite and Bleve indexes.
// If SQLite corruption is detected, the database is removed and ErrCorrupt is returned
// so the caller can trigger a full re-index.
//
// Bleve self-heal: when a sub-index has a structurally broken `_mapping` doc
// (kill-9 mid-flush, partial scorch snapshot), Open nukes the sub-index dir,
// recreates an empty bleve, and writes a `.needs_reindex_<name>` marker so the
// daemon's next pass does a full rebuild. Open never returns a typed
// MappingCorruptError to callers — the entire recovery is internal. Callers
// that need bleve (e.g. `ox code search`) just see empty results until the
// daemon repopulates; callers that don't need bleve (e.g. `ox code insights`)
// should use OpenSQLOnly to skip bleve entirely.
func Open(root string) (*Store, error) {
	db, err := openSQLite(root)
	if err != nil {
		return nil, err
	}

	codeIndex, diffIndex, commentIndex, err := openBleveIndexes(root)
	if err != nil {
		_ = db.Close()
		return nil, err
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

// OpenSQLOnly opens the SQLite half of a Store without touching bleve.
// Bleve sub-indexes are left nil — search and dirty-overlay APIs MUST NOT be
// used on a SQL-only store; the Store will panic on nil bleve access.
//
// Used by read paths that only query SQL data (e.g. `ox code insights`,
// `ox code status` counters) so they keep working when bleve is mid-rebuild
// or otherwise unavailable. SQLite's WAL mode makes concurrent reads safe
// even while the daemon is actively writing.
func OpenSQLOnly(root string) (*Store, error) {
	db, err := openSQLite(root)
	if err != nil {
		return nil, err
	}
	return &Store{
		db:               db,
		queries:          codedbsqlc.New(db),
		Root:             root,
		dirtyCodeIndexes: make(map[string]bleve.Index),
	}, nil
}

// openSQLite handles the SQLite half of store construction. Shared by Open
// and OpenSQLOnly so the SQLite pragma string and integrity check live in one
// place.
func openSQLite(root string) (*sql.DB, error) {
	reposDir := filepath.Join(root, "repos")
	bleveDir := filepath.Join(root, "bleve")
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

	if err := checkSQLiteIntegrity(db); err != nil {
		_ = db.Close()
		slog.Error("sqlite corruption detected, removing database", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		return nil, fmt.Errorf("sqlite integrity check failed: %w", ErrCorrupt)
	}

	if err := CreateSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return db, nil
}

// bleveMappingFor returns the bleve index mapping for a sub-index.
//
// Per ADR-018, the `code` and `comment` indexes use **index-only** content
// fields (`Store=false`, `IncludeTermVectors=false`): the inverted index — and
// thus full-text search — is unchanged, but the redundant stored copy of the
// source text that existed only to produce ANSI highlight fragments is gone.
// Comment text is still retrievable in full from SQLite (`comments.text`);
// code snippets are re-derived from git blobs at search time.
//
// The `diff` index is also index-only (ADR-018 phase 2 — Option D + Option A):
// diff text is regenerated on the search read path via Store.DiffSnippet
// (LRU-cached, using the shared internal/codedb/diffformat package so the
// indexer's write format and the search re-derivation stay byte-stable).
//
// Bump bleveMappingVersion(name) whenever this returns a structurally different
// mapping; existing on-disk indexes whose stored version is older will be nuked
// and recreated via the existing self-heal path (and the daemon's
// `.needs_reindex_<name>` marker triggers a full rebuild on the next pass).
func bleveMappingFor(name string) mapping.IndexMapping {
	m := bleve.NewIndexMapping()
	switch name {
	case "code", "comment", "diff":
		// ADR-018 phase 2 added "diff" to the index-only set per Option D
		// (drop diff snippet; agents `git show` for the patch). Inverted index
		// is unchanged — diff search keeps working; only the stored copy of
		// the patch text (used previously for ANSI fragments) goes away.
		ft := bleve.NewTextFieldMapping()
		ft.Store = false
		ft.IncludeTermVectors = false
		dm := bleve.NewDocumentMapping()
		dm.AddFieldMappingsAt("content", ft)
		m.DefaultMapping = dm
	}
	return m
}

// bleveMappingVersion is the structural version of bleveMappingFor(name).
// Bump when changing Store / IncludeTermVectors / field set.
func bleveMappingVersion(name string) int {
	switch name {
	case "code", "comment":
		return 2 // ADR-018 phase 1: index-only (was 1 = default stored)
	case "diff":
		return 2 // ADR-018 phase 2: index-only Option D (was 1)
	}
	return 1
}

// mappingVersionMarker is the per-sub-index file recording the structural
// version of the mapping the index was created with. Missing => v1 (pre-marker).
const mappingVersionMarker = ".mapping_version"

// readMappingVersion returns the recorded mapping version for path. Missing
// marker (os.IsNotExist) is the legitimate pre-v2 case → returns (1, nil) and
// the caller may proceed with a destructive upgrade. Any other I/O failure
// (permission, transient EIO, EROFS) returns (0, err); callers MUST refuse to
// upgrade on a non-nil error so a transient read failure can't trigger a
// destructive nuke-and-recreate of a healthy index. Garbage marker contents
// are treated as pre-marker (v1, no error) — that case is recoverable.
func readMappingVersion(path string) (int, error) {
	b, err := os.ReadFile(filepath.Join(path, mappingVersionMarker))
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("read mapping version at %s: %w", path, err)
	}
	n, parseErr := strconv.Atoi(strings.TrimSpace(string(b)))
	if parseErr != nil || n < 1 {
		return 1, nil
	}
	return n, nil
}

func writeMappingVersion(path, name string) error {
	return os.WriteFile(
		filepath.Join(path, mappingVersionMarker),
		[]byte(strconv.Itoa(bleveMappingVersion(name))),
		0o644,
	)
}

// createBleveSubIndex creates a fresh bleve sub-index using the ADR-018 mapping
// for `name`, then writes the mapping-version marker. Used both for first-time
// creation and post-rebuild recreation.
func createBleveSubIndex(path, name string) (bleve.Index, error) {
	idx, err := bleve.New(path, bleveMappingFor(name))
	if err != nil {
		return nil, err
	}
	if err := writeMappingVersion(path, name); err != nil {
		slog.Warn("failed to write bleve mapping version marker",
			"name", name, "path", path, "err", err)
	}
	return idx, nil
}

// upgradeBleveSubIndex nukes an out-of-date sub-index and recreates it empty
// under the current mapping, then writes `.needs_reindex_<name>` so the
// daemon's next pass does a full rebuild via the established self-heal path.
func upgradeBleveSubIndex(root, path, name string, fromVersion int) (bleve.Index, error) {
	slog.Info("bleve mapping version upgrade, rebuilding",
		"name", name, "from", fromVersion, "to", bleveMappingVersion(name))
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("remove out-of-date bleve sub-index %s: %w", path, err)
	}
	idx, err := createBleveSubIndex(path, name)
	if err != nil {
		return nil, fmt.Errorf("recreate bleve sub-index %s: %w", path, err)
	}
	if err := WriteNeedsReindexMarker(root, name); err != nil {
		slog.Warn("failed to write needs_reindex marker after mapping upgrade",
			"name", name, "err", err)
	}
	return idx, nil
}

// openBleveIndexes opens (or self-heals + recreates) the three bleve
// sub-indexes. Returns all three on success; closes any successfully opened
// indexes if a later one fails.
func openBleveIndexes(root string) (codeIdx, diffIdx, commentIdx bleve.Index, err error) {
	bleveDir := filepath.Join(root, "bleve")

	codeIdx, err = openOrCreateBleveIndex(root, filepath.Join(bleveDir, "code"), "code")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open code index: %w", err)
	}
	diffIdx, err = openOrCreateBleveIndex(root, filepath.Join(bleveDir, "diff"), "diff")
	if err != nil {
		_ = codeIdx.Close()
		return nil, nil, nil, fmt.Errorf("open diff index: %w", err)
	}
	commentIdx, err = openOrCreateBleveIndex(root, filepath.Join(bleveDir, "comment"), "comment")
	if err != nil {
		_ = codeIdx.Close()
		_ = diffIdx.Close()
		return nil, nil, nil, fmt.Errorf("open comment index: %w", err)
	}
	return codeIdx, diffIdx, commentIdx, nil
}

// ReposDir returns the path to the bare git repos directory.
func (s *Store) ReposDir() string {
	return filepath.Join(s.Root, "repos")
}

// Close closes all resources. It is safe to call multiple times.
// Bleve sub-indexes may be nil on a SQL-only store (see OpenSQLOnly); Close
// skips nil indexes rather than panicking.
func (s *Store) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		s.DetachDirtyOverlay()
		s.closeBlobRepos()
		for _, idx := range []bleve.Index{s.CodeIndex, s.DiffIndex, s.CommentIndex} {
			if idx == nil {
				continue
			}
			if err := idx.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
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
		_ = existing.Close()
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
		_ = idx.Close()
		delete(s.dirtyCodeIndexes, id)
	}
	s.rebuildCombinedIndex()
}

// DetachDirtyOverlay closes all attached dirty overlays and resets CombinedCodeIndex.
func (s *Store) DetachDirtyOverlay() {
	for id, idx := range s.dirtyCodeIndexes {
		_ = idx.Close()
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
//
// On a SQL-only store (constructed via OpenSQLOnly), the bleve indexes are
// nil by design — those checks are skipped, not treated as a failure.
// Callers that genuinely need bleve integrity must use Open, not OpenSQLOnly.
func (s *Store) CheckIntegrity() error {
	if err := checkSQLiteIntegrity(s.db); err != nil {
		return fmt.Errorf("sqlite: %w", ErrCorrupt)
	}

	// Probe each bleve sub-index with the FST-touching field walk from PR #607.
	// Skip nil indexes — a SQL-only store (OpenSQLOnly) legitimately has no
	// bleve to check, and calling probeBleveIndex on nil would nil-deref before
	// the recover could catch it.
	for name, idx := range map[string]bleve.Index{"code": s.CodeIndex, "diff": s.DiffIndex, "comment": s.CommentIndex} {
		if idx == nil {
			continue
		}
		if err := probeBleveIndex(idx, name); err != nil {
			return err
		}
	}

	return nil
}

// probeBleveIndex runs an FST-touching probe against every indexed field
// and reports ErrCorrupt if the FST cannot be loaded or the probe panics.
//
// What this probes: each indexed field has a term dictionary backed by a
// vellum FST (Finite State Transducer) per segment. Partial-write disk
// corruption can leave a segment whose FST bytes are unloadable — the
// failure mode that wedges the daemon when the next batch write hits it.
//
// Strategy: get the raw IndexReader via Advanced() and call
// TermFieldReader(field) directly for every field returned by Fields().
// TermFieldReader iterates segments and calls segment.Dictionary(field)
// inline on the calling goroutine, so each field's vellum.Load runs where
// runBleveProbe's recover can catch a panic.
//
// Why not Document / FieldDict / MatchAll / MatchNone:
//   - MatchNone short-circuits with no segment work at all.
//   - MatchAll in scorch is served from the live-docs bitmap via
//     DocIDReaderAll(), bypassing the FST.
//   - FieldDict in scorch spawns one goroutine per segment for the
//     Dictionary(field) call (see
//     scorch.IndexSnapshot.newIndexSnapshotFieldDict). A panic in any of
//     those goroutines escapes the recover in runBleveProbe and crashes
//     the process.
//   - Document(id) only loads the "_id" dictionary, so a corrupt FST on
//     the searchable content field (where most ox queries land) would
//     pass.
//
// Scope: catches FST loading failures (corrupt vellum bytes, bad dict
// offsets) for every field in the index. The narrower "segment marks
// field present but dictStart==0, fst is nil" pattern only panics during
// write-path DocNumbers calls — that path is covered by safeBatch in
// internal/codedb/index.
func probeBleveIndex(idx bleve.Index, name string) error {
	return runBleveProbe(name, func() error {
		fields, err := idx.Fields()
		if err != nil {
			return err
		}
		adv, err := idx.Advanced()
		if err != nil {
			return err
		}
		reader, err := adv.Reader()
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()

		probeTerm := []byte("__sageox_codedb_integrity_probe__")
		for _, field := range fields {
			tfr, tfrErr := reader.TermFieldReader(context.Background(), probeTerm, field, false, false, false)
			if tfrErr != nil {
				return tfrErr
			}
			_ = tfr.Close()
		}
		return nil
	})
}

// runBleveProbe wraps a probe function with the panic-recovery and
// ErrCorrupt-wrapping policy used by CheckIntegrity. Factored out so the
// recovery contract can be tested without stubbing the whole bleve.Index
// interface.
func runBleveProbe(name string, probe func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bleve %s index: panic during FST probe (%v): %w", name, r, ErrCorrupt)
		}
	}()
	if sErr := probe(); sErr != nil {
		return fmt.Errorf("bleve %s index: %w", name, errors.Join(ErrCorrupt, sErr))
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

// openOrCreateBleveIndex opens an existing bleve sub-index, creates a new
// empty one if the path doesn't exist yet, or self-heals on proven structural
// corruption.
//
// Self-heal contract: when isBleveIndexCorrupt returns true (empty `_mapping`
// doc or dangling .zap segments), the sub-index is nuked and recreated as an
// empty bleve, and `.needs_reindex_<name>` is written under root so the next
// daemon indexing pass does a full rebuild. Returns the empty index with no
// error — callers see a working but empty bleve, not a typed error.
//
// True lock contention (another writer holds bbolt's exclusive flock) is
// preserved as the pre-existing "lock contention" error: the corruption peek
// retries a read-only open (100ms timeout per attempt) a few times before
// giving up, so a live exclusive lock blocks the peek and we stay in the safe
// wait-and-retry path — but a transient hold that clears within the retry
// budget no longer permanently masks provable corruption (see
// isBleveIndexCorrupt).
//
// path is the bleve sub-index dir (e.g. <root>/bleve/code); root is the
// codedb dataDir (needed to locate the marker file).
func openOrCreateBleveIndex(root, path, name string) (bleve.Index, error) {
	idx, err := safeOpenBleve(path)
	if err == nil {
		// Mapping-version check: an existing index opened cleanly, but if its
		// mapping version is below the current one (e.g., ADR-018 flipped code
		// from default-stored to index-only), it must be rebuilt — the old
		// stored segments don't shrink in place. On a transient marker-read
		// failure (permission, I/O), keep the existing index rather than
		// destructively rebuild — a flaky filesystem must not nuke a healthy
		// bleve dir.
		got, verErr := readMappingVersion(path)
		if verErr != nil {
			slog.Warn("read mapping version failed; keeping existing index (no destructive upgrade)",
				"name", name, "path", path, "err", verErr)
			return idx, nil
		}
		if got < bleveMappingVersion(name) {
			if closeErr := idx.Close(); closeErr != nil {
				slog.Warn("close before mapping upgrade failed",
					"name", name, "err", closeErr)
			}
			return upgradeBleveSubIndex(root, path, name, got)
		}
		return idx, nil
	}
	if errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
		return createBleveSubIndex(path, name)
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
	// peek the mapping non-blocking and self-heal when the mapping is provably
	// empty. The peek retries a read-only open (100ms timeout per attempt,
	// bleveCorruptionPeekAttempts tries): a real exclusive lock blocks each
	// read attempt, but a competing writer that releases within the retry
	// budget no longer strands the peek — only a lock held for the full
	// budget falls through to the safe lock-contention behavior.
	boltPath := filepath.Join(path, "store", "root.bolt")
	if _, statErr := os.Stat(boltPath); statErr == nil {
		if isBleveIndexCorrupt(boltPath) {
			return selfHealBleveSubIndex(root, path, name, err)
		}
		// isBleveIndexCorrupt only proves the corruption it can SEE through a
		// read-only peek. A bolt torn so badly bbolt cannot open it at all (a
		// kill-9 mid-write, "invalid database") reads identically to a live
		// exclusive lock here — both made bleve.Open fail and both defeat the
		// read-only peek. Disambiguate with a bounded exclusive open so the
		// unopenable-corrupt case self-heals instead of crash-looping, while a
		// genuinely locked index is still left untouched.
		if boltCorruptOnExclusiveOpen(boltPath) {
			return selfHealBleveSubIndex(root, path, name, err)
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
	return createBleveSubIndex(path, name)
}

// selfHealBleveSubIndex recovers from proven mapping/snapshot corruption by
// nuking the sub-index dir, recreating an empty bleve, and writing a
// `.needs_reindex_<name>` marker so the next daemon pass does a full rebuild.
// Logs a single warn line so the recovery is visible in daemon logs.
//
// The marker is best-effort: if the marker write fails (disk full, permission
// denied) the function still returns the empty index — the daemon will see
// the empty bleve on its next freshness check via different signals (empty
// search results) but the explicit signal is preferred. Marker failure is
// logged at warn level.
func selfHealBleveSubIndex(root, path, name string, openErr error) (bleve.Index, error) {
	slog.Warn("bleve sub-index structurally corrupt, self-healing (nuke + recreate)",
		"name", name, "path", path, "open_err", openErr)

	if removeErr := os.RemoveAll(path); removeErr != nil {
		return nil, fmt.Errorf("remove corrupt bleve sub-index %s: %w", path, removeErr)
	}
	idx, err := createBleveSubIndex(path, name)
	if err != nil {
		return nil, fmt.Errorf("recreate empty bleve sub-index %s: %w", path, err)
	}

	if markerErr := WriteNeedsReindexMarker(root, name); markerErr != nil {
		slog.Warn("failed to write needs_reindex marker; daemon may not auto-rebuild",
			"name", name, "root", root, "err", markerErr)
	}
	return idx, nil
}

// WriteNeedsReindexMarker creates the marker file that signals to the daemon's
// next indexing pass that a sub-index was nuked and needs a full rebuild.
// The file body is a short human-readable string with the trigger time, so
// `ox doctor` and `cat .needs_reindex_*` can give a meaningful answer to
// "why is this here?". Exported so the daemon can restore markers after a
// failed marker-forced reindex (without restore, a single rebuild failure
// would silently drop the signal and code search would stay empty until
// `ox code index --full`).
func WriteNeedsReindexMarker(root, name string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("bleve sub-index %q self-healed at %s\n", name, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(NeedsReindexMarkerPath(root, name), []byte(body), 0o600)
}

// bleveCorruptionPeekAttempts and bleveCorruptionPeekDelay bound how hard
// isBleveIndexCorrupt retries its read-only peek before conceding the file is
// genuinely, persistently locked. Overridable in tests (kept as package vars
// rather than constants) so a test can shrink the delay for speed or use
// bleveCorruptionPeekRetryHook to synchronize deterministically with a
// concurrent lock holder instead of racing on wall-clock sleeps (see
// .claude/rules/testing.md: prefer hooks over time.Sleep for concurrency
// tests).
var (
	bleveCorruptionPeekAttempts  = 3
	bleveCorruptionPeekDelay     = 150 * time.Millisecond
	bleveCorruptionPeekRetryHook = func(attempt int) {}
)

// isBleveIndexCorrupt opens root.bolt read-only with a short timeout and
// affirmatively checks for three on-disk failure modes that present as the
// same opaque "error parsing mapping JSON" surface error:
//
//  1. Empty/missing `_mapping` doc in the latest snapshot.
//  2. Truncated/unparseable `_mapping` doc — bytes present but the JSON
//     fails validation (kill-mid-write that flushed a partial JSON
//     document). Same surface error as case 1, same recovery needed.
//  3. The latest snapshot has segment buckets but the store dir has zero
//     `.zap` files. (Observed in the field on a real poison pill: bolt is
//     fully readable, mapping is intact, yet `bleve.Open` still fails
//     because scorch can't load the snapshot's segments — every referenced
//     segment is missing on disk.)
//
// Returns true only when we successfully read the bolt AND can prove one of
// the three conditions. The read-only open is retried up to
// bleveCorruptionPeekAttempts times (bleveCorruptionPeekDelay apart) so a
// transient competing writer doesn't strand a genuinely corrupt mapping in
// the lock-contention path — a real writer that finishes within the retry
// budget no longer masks provable corruption. Only once every attempt fails
// (or the bolt structure itself can't be parsed) do we return false — we
// never flag corruption without proof, so a write lock held for the full
// retry budget still stays in the lock-contention path.
//
// scorch's bbolt layout in bleve v2.5.7 (empirically verified, see
// proposals/2026-05-17-bleve-bolt-layout/ if it gets written):
//
//	bucket "s" (snapshots)
//	  └── snapshot bucket <varint epoch>
//	         ├── bucket "i" (internal)  — holds `_mapping`, `TotBytesWritten`
//	         ├── bucket "m" (meta)      — holds `timeStamp`, `type`, `version`
//	         └── bucket <segment id>    — one per segment referenced
//
// Earlier implementations of this function tried to decode segment-bucket
// keys back into .zap filenames so we could check each reference individually.
// That coupling broke silently across bleve versions (different epoch
// encoding), so we now use a coarser but version-independent signal: a
// snapshot with segment buckets and *zero* .zap files on disk is
// unambiguously broken. Anything more granular (e.g., partial segment loss)
// is harder to detect reliably and survives as an empty search result rather
// than a hard open failure.
func isBleveIndexCorrupt(boltPath string) bool {
	var db *bbolt.DB
	var err error
	for attempt := 0; attempt < bleveCorruptionPeekAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(bleveCorruptionPeekDelay)
		}
		bleveCorruptionPeekRetryHook(attempt)
		db, err = bbolt.Open(boltPath, 0600, &bbolt.Options{
			ReadOnly: true,
			Timeout:  100 * time.Millisecond,
		})
		if err == nil {
			break
		}
	}
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()

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

		// (1) mapping doc empty/missing OR truncated/unparseable.
		// Both states surface as the same `error parsing mapping JSON:
		// unexpected end of JSON input` from bleve.Open. We must flag both
		// or callers will misread truncated mappings as lock contention and
		// the cryptic error returns. json.Valid is cheaper than Unmarshal
		// and equivalent for this check (we only care whether bleve will
		// fail to parse, not the value).
		internal := snap.Bucket([]byte{'i'})
		mappingBytes := []byte(nil)
		if internal != nil {
			mappingBytes = internal.Get([]byte("_mapping"))
		}
		if internal == nil || len(mappingBytes) == 0 || !json.Valid(mappingBytes) {
			corrupt = true
			return nil
		}

		// (2) snapshot has segment buckets but the store dir has zero .zap
		// files. We avoid decoding scorch's segment-bucket keys back into
		// filenames — that coupling has broken silently across bleve
		// versions. The coarser signal (snapshot expects segments, disk
		// has none) is unambiguous and version-independent.
		segmentBucketCount := 0
		_ = snap.ForEach(func(name []byte, val []byte) error {
			if val != nil {
				// it's a key/value pair, not a bucket
				return nil
			}
			// inner buckets that aren't "i" or "m" are segment buckets
			if len(name) == 1 && (name[0] == 'i' || name[0] == 'm') {
				return nil
			}
			segmentBucketCount++
			return nil
		})
		if segmentBucketCount > 0 && len(zapsOnDisk) == 0 {
			corrupt = true
		}
		return nil
	})
	return corrupt
}

// bleveExclusiveProbeTimeout bounds how long boltCorruptOnExclusiveOpen waits
// to take bbolt's exclusive flock before conceding a live writer holds it.
// Overridable in tests. A timeout only ever biases toward the safe "assume
// locked, do not nuke" verdict, so shrinking it in tests cannot manufacture a
// false corruption verdict.
var bleveExclusiveProbeTimeout = 2 * time.Second

// boltCorruptOnExclusiveOpen disambiguates the states that surface as a
// bbolt-open failure the read-only peek (isBleveIndexCorrupt) cannot classify:
// a bolt torn so badly bbolt rejects it (kill-9 mid-write) versus a bolt held
// under another writer's exclusive flock versus a transient/environmental
// failure. It attempts a bounded EXCLUSIVE open and returns true (self-heal)
// ONLY on proven on-disk structural corruption:
//
//   - ErrInvalid / ErrVersionMismatch / ErrChecksum: bbolt opened the file and
//     took the exclusive flock, then failed validating the meta pages. The
//     file itself is structurally broken — proven corruption. Return true.
//   - ErrTimeout: a live writer holds the exclusive lock. Return false — never
//     nuke an index that is actively being written.
//   - any OTHER error (permission denied, I/O error, mmap/ENOMEM resource
//     exhaustion): these occur BEFORE meta validation and are transient or
//     environmental, NOT corruption. Return false — nuking a possibly-healthy
//     index on a permission blip would be destructive data loss.
//   - opened cleanly: the bolt is fine. Close and return false.
//
// A nuke only ever follows a *validation* failure (ErrInvalid/version/checksum)
// on a flock we took ourselves, so neither a live-locked index nor a
// transiently-unreadable one is ever destroyed — the same "never nuke without
// proof" guarantee isBleveIndexCorrupt provides, extended to the
// unopenable-bolt class the read-only peek structurally cannot reach.
func boltCorruptOnExclusiveOpen(boltPath string) bool {
	db, err := bbolt.Open(boltPath, 0600, &bbolt.Options{Timeout: bleveExclusiveProbeTimeout})
	if err == nil {
		_ = db.Close()
		return false
	}
	// Proven on-disk corruption: bbolt validated the meta pages and rejected
	// them. These are the only failures that justify a destructive discard.
	if errors.Is(err, bbolterrors.ErrInvalid) ||
		errors.Is(err, bbolterrors.ErrVersionMismatch) ||
		errors.Is(err, bbolterrors.ErrChecksum) {
		slog.Error("bleve bolt failed structural validation; treating as corruption",
			"path", boltPath, "err", err)
		return true
	}
	// ErrTimeout is a live writer; anything else (permission/I/O/resource) is
	// transient or environmental. Neither proves corruption — preserve the
	// index and let the caller fall through to the safe lock-contention path.
	if !errors.Is(err, bbolterrors.ErrTimeout) {
		slog.Warn("bleve bolt open failed without proving corruption; preserving index",
			"path", boltPath, "err", err)
	}
	return false
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
	idx, err := createBleveSubIndex(bleveDir, name)
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
	defer func() { _ = db.Close() }()
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
